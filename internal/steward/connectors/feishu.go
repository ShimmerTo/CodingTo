package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	dispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"codingto/internal/steward"
)

// feishu implements the Feishu long-connection bot (WebSocket). Outbound uses
// the open platform message API, so replies are not bound to the session
// webhook like DingTalk; the thread id carries the chat id.
type feishu struct {
	channelID int64
	config    map[string]string
	secrets   map[string]string
	onMessage func(steward.InboundMessage)

	mu       sync.Mutex
	client   *lark.Client
	wsClient *larkws.Client
	// lastTarget allows the manual test action to reuse the latest valid
	// recipient after a message has been received, even when no test ID is
	// configured in the channel form.
	lastTarget steward.OutboundMessage
}

func feishuFactory(config, secrets map[string]string, onMessage func(steward.InboundMessage)) (steward.Connector, error) {
	return &feishu{channelID: channelIDFromConfig(config), config: config, secrets: secrets, onMessage: onMessage}, nil
}

func (f *feishu) Connect(ctx context.Context, ready func()) error {
	appID := f.config[KeyAppID]
	appSecret := f.secrets[KeyAppSecret]
	if appID == "" || appSecret == "" {
		return fmt.Errorf("飞书渠道缺少 AppID / AppSecret")
	}
	client := lark.NewClient(appID, appSecret)
	dispatcher := dispatcher.NewEventDispatcher("", "")
	dispatcher.OnP2MessageReceiveV1(func(c context.Context, event *larkim.P2MessageReceiveV1) error {
		f.handleMessage(event)
		return nil
	})
	wsClient := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithAutoReconnect(true),
		larkws.WithOnReady(ready),
		larkws.WithOnReconnected(ready),
	)
	f.mu.Lock()
	f.client = client
	f.wsClient = wsClient
	f.mu.Unlock()
	return wsClient.Start(ctx)
}

func (f *feishu) handleMessage(event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return
	}
	message := event.Event.Message
	// Only handle text messages; drop bot's own messages.
	if message.MessageType == nil || *message.MessageType != "text" {
		return
	}
	if event.Event.Sender != nil && event.Event.Sender.SenderType != nil && *event.Event.Sender.SenderType == "bot" {
		return
	}
	// Parse {"text":"..."} content.
	var payload struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(deref(message.Content)), &payload)
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return
	}
	chatType := deref(message.ChatType)
	if chatType == "group" && !feishuMentioned(event) {
		return
	}
	senderID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = firstNonEmpty(deref(event.Event.Sender.SenderId.OpenId), deref(event.Event.Sender.SenderId.UserId))
	}
	threadID := deref(message.ChatId)
	receiveIDType := larkim.CreateMessageV1ReceiveIDTypeChatId
	// Feishu's p2p event contains a chat_id, but the send API expects a user
	// identifier for a private conversation. Prefer the sender open_id so
	// replies do not fail with invalid receive_id.
	if chatType == "p2p" && senderID != "" {
		threadID = senderID
		receiveIDType = larkim.CreateMessageV1ReceiveIDTypeOpenId
	}
	target := steward.OutboundMessage{
		ThreadID: threadID, ReceiveIDType: receiveIDType,
		ReplyToMessageID: deref(message.MessageId),
	}
	f.mu.Lock()
	f.lastTarget = target
	f.mu.Unlock()
	f.onMessage(steward.InboundMessage{
		ChannelID:        f.channelID,
		Platform:         steward.PlatformFeishu,
		SenderID:         senderID,
		ThreadID:         threadID,
		ReceiveIDType:    receiveIDType,
		ReplyToMessageID: deref(message.MessageId),
		MessageID:        deref(message.MessageId),
		Text:             text,
		Raw:              event,
	})
}

func (f *feishu) Send(ctx context.Context, msg steward.OutboundMessage) error {
	f.mu.Lock()
	client := f.client
	target := f.lastTarget
	f.mu.Unlock()
	if client == nil {
		return fmt.Errorf("飞书：客户端未就绪")
	}
	if msg.ThreadID == "" && msg.ReplyToMessageID == "" {
		msg = withFeishuTarget(msg, target)
	}
	text := msg.Text
	if msg.Card != nil {
		if msg.Card.Confirm {
			text = fmt.Sprintf("%s\n%s\n\n回复「确认」允许，或「拒绝」取消。", msg.Card.Title, msg.Card.Body)
		} else {
			text = fmt.Sprintf("%s\n%s", msg.Card.Title, msg.Card.Body)
		}
	}
	msgType := "text"
	var content []byte
	if msg.Markdown {
		// Feishu renders markdown through interactive cards; a plain text
		// message would show the raw markdown source.
		msgType = "interactive"
		content, _ = json.Marshal(map[string]any{
			"config":   map[string]any{"wide_screen_mode": true},
			"elements": []any{map[string]any{"tag": "markdown", "content": text}},
		})
	} else {
		content, _ = json.Marshal(map[string]string{"text": text})
	}
	if msg.ReplyToMessageID != "" {
		resp, err := client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(msg.ReplyToMessageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(msgType).
				Content(string(content)).
				Build()).
			Build())
		if err != nil {
			return fmt.Errorf("飞书回复失败：%w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("飞书回复失败：code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}
	if strings.TrimSpace(msg.ThreadID) == "" {
		return fmt.Errorf("飞书发送失败：未配置有效的测试接收 ID，请填写 chat_id 或 open_id")
	}
	receiveIDType := feishuReceiveIDType(msg)
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ThreadID).
			MsgType(msgType).
			Content(string(content)).
			Build()).
		Build()
	resp, err := client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("飞书发送失败：%w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("飞书发送失败：code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (f *feishu) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wsClient != nil {
		f.wsClient.Close()
	}
	return nil
}

// withFeishuTarget fills a manual test message with the latest received
// recipient. It intentionally does not copy ReplyToMessageID: a test sends
// to the target ID instead of replying to the latest message.
func withFeishuTarget(msg, target steward.OutboundMessage) steward.OutboundMessage {
	msg.ThreadID = target.ThreadID
	msg.ReceiveIDType = target.ReceiveIDType
	return msg
}

// feishuReceiveIDType selects the API query type for a configured or
// persisted recipient. Explicit metadata wins; the prefix fallback keeps
// existing bot tasks compatible because they only persisted ThreadID.
func feishuReceiveIDType(msg steward.OutboundMessage) string {
	if msg.ReceiveIDType != "" {
		return msg.ReceiveIDType
	}
	if strings.HasPrefix(msg.ThreadID, "ou_") {
		return larkim.CreateMessageV1ReceiveIDTypeOpenId
	}
	return larkim.CreateMessageV1ReceiveIDTypeChatId
}

// feishuMentioned reports whether the bot is mentioned in a group message.
func feishuMentioned(event *larkim.P2MessageReceiveV1) bool {
	for _, mention := range event.Event.Message.Mentions {
		if mention.MentionedType != nil && *mention.MentionedType == "bot" {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
