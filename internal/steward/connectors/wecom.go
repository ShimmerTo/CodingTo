package connectors

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"codingto/internal/steward"
)

// wecom implements the WeCom (企业微信) callback channel. WeCom has no official
// long connection for receiving messages, so this connector runs a local HTTP
// server and requires a public callback URL (reverse proxy) configured by the
// user. Sending uses the message/send API with a corp access token.
type wecom struct {
	channelID int64
	config    map[string]string
	secrets   map[string]string
	onMessage func(steward.InboundMessage)

	mu      sync.Mutex
	server  *http.Server
	port    string
	token   string // cached access token
	tokenAt time.Time
}

func wecomFactory(config, secrets map[string]string, onMessage func(steward.InboundMessage)) (steward.Connector, error) {
	return &wecom{channelID: channelIDFromConfig(config), config: config, secrets: secrets, onMessage: onMessage}, nil
}

func (w *wecom) Connect(ctx context.Context, ready func()) error {
	if err := required(w.config, KeyToken); err != nil {
		return err
	}
	if err := required(w.config, KeyEncodingAESKey); err != nil {
		return err
	}
	port := w.config["callbackPort"]
	if port == "" {
		port = "9588"
	}
	w.port = port
	mux := http.NewServeMux()
	mux.HandleFunc("/wecom/callback", w.handleCallback)
	server := &http.Server{Handler: mux}
	w.mu.Lock()
	w.server = server
	w.mu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return fmt.Errorf("企微回调端口 %s 被占用：%w", port, err)
	}
	go func() {
		_ = server.Serve(listener)
	}()
	ready()
	<-ctx.Done()
	_ = server.Close()
	return nil
}

// handleCallback validates the WeCom signature, decrypts the payload and
// forwards the message into the steward pipeline.
func (w *wecom) handleCallback(writer http.ResponseWriter, r *http.Request) {
	timestamp := r.URL.Query().Get("timestamp")
	nonce := r.URL.Query().Get("nonce")
	signature := r.URL.Query().Get("msg_signature")

	if r.Method == http.MethodGet {
		echostr := r.URL.Query().Get("echostr")
		if echostr == "" || !w.verifySignature(timestamp, nonce, echostr, signature) {
			http.Error(writer, "verify failed", http.StatusBadRequest)
			return
		}
		plain, err := w.decryptMsg(echostr)
		if err != nil {
			http.Error(writer, "decrypt failed", http.StatusBadRequest)
			return
		}
		_, _ = writer.Write(plain)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(writer, "read body", http.StatusBadRequest)
		return
	}
	var envelope struct {
		Encrypt string `xml:"Encrypt"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil || envelope.Encrypt == "" {
		http.Error(writer, "bad xml", http.StatusBadRequest)
		return
	}
	if !w.verifySignature(timestamp, nonce, envelope.Encrypt, signature) {
		http.Error(writer, "signature mismatch", http.StatusBadRequest)
		return
	}
	plain, err := w.decryptMsg(envelope.Encrypt)
	if err != nil {
		http.Error(writer, "decrypt failed", http.StatusBadRequest)
		return
	}
	var msg struct {
		FromUserName string `xml:"FromUserName"`
		ToUserName   string `xml:"ToUserName"`
		MsgType      string `xml:"MsgType"`
		Content      string `xml:"Content"`
		MsgId        string `xml:"MsgId"`
	}
	if err := xml.Unmarshal(plain, &msg); err != nil {
		http.Error(writer, "bad message", http.StatusBadRequest)
		return
	}
	// Reply "" immediately (WeCom requires a response within 5s); the actual
	// outbound message goes through the message/send API later.
	_, _ = writer.Write([]byte(""))

	if msg.MsgType != "text" || strings.TrimSpace(msg.Content) == "" {
		return
	}
	text := strings.TrimSpace(msg.Content)
	// 群机器人消息会以 @机器人 开头。
	text = strings.TrimPrefix(text, "@机器人")
	text = strings.TrimSpace(text)
	w.onMessage(steward.InboundMessage{
		ChannelID: w.channelID,
		Platform:  steward.PlatformWeCom,
		SenderID:  msg.FromUserName,
		ThreadID:  msg.FromUserName, // send back to the same user
		Text:      text,
		Raw:       msg,
	})
}

func (w *wecom) Send(ctx context.Context, msg steward.OutboundMessage) error {
	token, err := w.accessToken(ctx)
	if err != nil {
		return err
	}
	text := msg.Text
	if msg.Card != nil {
		if msg.Card.Confirm {
			text = fmt.Sprintf("%s\n%s\n\n回复「确认」允许，或「拒绝」取消。", msg.Card.Title, msg.Card.Body)
		} else {
			text = fmt.Sprintf("%s\n%s", msg.Card.Title, msg.Card.Body)
		}
	}
	body := map[string]any{
		"touser":  msg.ThreadID,
		"agentid": w.config[KeyAgentID],
		"safe":    0,
	}
	if msg.Markdown {
		// WeCom markdown content is capped at 2048 UTF-8 bytes by the API;
		// the steward layer splits long messages to that bound already.
		body["msgtype"] = "markdown"
		body["markdown"] = map[string]string{"content": text}
	} else {
		body["msgtype"] = "text"
		body["text"] = map[string]string{"content": text}
	}
	raw, _ := json.Marshal(body)
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("企微发送失败：%w", err)
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("企微发送响应解析失败：%w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("企微发送失败：%s (code %d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

func (w *wecom) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.server != nil {
		_ = w.server.Close()
	}
	return nil
}

// accessToken fetches and caches the corp access token.
func (w *wecom) accessToken(ctx context.Context) (string, error) {
	w.mu.Lock()
	if w.token != "" && time.Since(w.tokenAt) < 50*time.Minute {
		token := w.token
		w.mu.Unlock()
		return token, nil
	}
	w.mu.Unlock()

	corpID := w.config[KeyCorpID]
	secret := w.secrets[KeySecret]
	if corpID == "" || secret == "" {
		return "", fmt.Errorf("企微渠道缺少 CorpID / Secret")
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=" + url.QueryEscape(corpID) + "&corpsecret=" + url.QueryEscape(secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("企微获取 token 失败：%w", err)
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("企微获取 token 失败：%s (code %d)", result.ErrMsg, result.ErrCode)
	}
	w.mu.Lock()
	w.token = result.AccessToken
	w.tokenAt = time.Now()
	w.mu.Unlock()
	return result.AccessToken, nil
}

// ---- WeCom crypto ----

func (w *wecom) verifySignature(timestamp, nonce, payload, signature string) bool {
	if signature == "" || timestamp == "" || nonce == "" {
		return false
	}
	items := []string{w.config[KeyToken], timestamp, nonce, payload}
	sort.Strings(items)
	joined := strings.Join(items, "")
	sum := sha1.Sum([]byte(joined))
	return fmt.Sprintf("%x", sum) == signature
}

func (w *wecom) aesKey() ([]byte, error) {
	raw := w.config[KeyEncodingAESKey]
	if len(raw) != 43 {
		return nil, fmt.Errorf("EncodingAESKey 长度无效")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw + "=")
	if err != nil {
		return nil, err
	}
	if len(decoded) != 43 {
		return nil, fmt.Errorf("EncodingAESKey 解码长度无效")
	}
	return decoded[:32], nil
}

func (w *wecom) decryptMsg(encrypted string) ([]byte, error) {
	key, err := w.aesKey()
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aes.BlockSize || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("密文长度无效")
	}
	iv := key[:aes.BlockSize]
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(ciphertext))
	mode.CryptBlocks(plain, ciphertext)
	// Remove PKCS7 padding.
	padLen := int(plain[len(plain)-1])
	if padLen <= 0 || padLen > aes.BlockSize || padLen > len(plain) {
		return nil, fmt.Errorf("填充无效")
	}
	plain = plain[:len(plain)-padLen]
	if len(plain) < 20 {
		return nil, fmt.Errorf("消息过短")
	}
	// random(16) + msgLen(4, big-endian) + msg
	msgLen := binary.BigEndian.Uint32(plain[16:20])
	msg := plain[20 : 20+int(msgLen)]
	corpID := string(plain[20+int(msgLen):])
	if corpID != w.config[KeyCorpID] {
		return nil, fmt.Errorf("接收方不匹配")
	}
	return msg, nil
}

func (w *wecom) encryptReply(plain string) (string, error) {
	key, err := w.aesKey()
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(plain)))
	receiveID := w.config[KeyCorpID]
	content := append(append(append(random, length...), []byte(plain)...), []byte(receiveID)...)
	// PKCS7 pad to block size.
	padLen := aes.BlockSize - len(content)%aes.BlockSize
	padding := make([]byte, padLen)
	for i := range padding {
		padding[i] = byte(padLen)
	}
	content = append(content, padding...)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := key[:aes.BlockSize]
	mode := cipher.NewCBCEncrypter(block, iv)
	encrypted := make([]byte, len(content))
	mode.CryptBlocks(encrypted, content)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}
