package connectors

import (
	"context"
	"fmt"
	"sync"

	dingchatbot "github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	dinglogger "github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"

	"codingto/internal/applog"
	"codingto/internal/steward"
)

// dingtalk implements the DingTalk Stream (WebSocket long connection) bot.
// Replies use the per-conversation session webhook from the latest inbound
// message (valid ~2h), so outbound is bound to the originating conversation.
type dingtalk struct {
	channelID int64
	config    map[string]string
	secrets   map[string]string
	onMessage func(steward.InboundMessage)

	mu          sync.Mutex
	webhooks    map[string]string // threadID -> sessionWebhook
	webhookTime map[string]int64
	client      *dingclient.StreamClient
	stop        chan struct{}
}

func dingtalkFactory(config, secrets map[string]string, onMessage func(steward.InboundMessage)) (steward.Connector, error) {
	return &dingtalk{
		channelID: channelIDFromConfig(config),
		config:    config, secrets: secrets, onMessage: onMessage,
		webhooks: make(map[string]string), webhookTime: make(map[string]int64),
		stop: make(chan struct{}),
	}, nil
}

func (d *dingtalk) Connect(ctx context.Context, ready func()) error {
	clientID := d.config[KeyClientID]
	clientSecret := d.secrets[KeyClientSecret]
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("钉钉渠道缺少 ClientID / ClientSecret")
	}
	wireDingTalkSDKLogger()

	d.mu.Lock()
	if d.client == nil {
		// One StreamClient per connector lifetime. The SDK's internal
		// AutoReconnect keeps the same client (and therefore the in-memory
		// per-conversation webhook map) alive across network drops, so a
		// late reply still finds its conversation's reply address.
		d.client = dingclient.NewStreamClient(dingclient.WithAppCredential(
			dingclient.NewAppCredentialConfig(clientID, clientSecret),
		))
		d.client.RegisterChatBotCallbackRouter(func(c context.Context, data *dingchatbot.BotCallbackDataModel) ([]byte, error) {
			d.handleMessage(c, data)
			return []byte(""), nil
		})
	}
	d.mu.Unlock()

	// The DingTalk stream SDK's Start dials the WebSocket and returns
	// immediately; the connection and its read/reconnect loop run in the
	// background on the same StreamClient. The Connector contract requires
	// Connect to block until the connection is lost or ctx is cancelled, so
	// block here. Without this the supervisor would spin up a fresh client
	// (with an empty webhook map) on every backoff cycle, and outbound
	// replies would fail with "未找到会话的回复地址".
	if err := d.client.Start(ctx); err != nil {
		return err
	}
	ready()
	<-ctx.Done()
	d.mu.Lock()
	if d.client != nil {
		d.client.Close()
	}
	d.mu.Unlock()
	return nil
}

func (d *dingtalk) handleMessage(ctx context.Context, data *dingchatbot.BotCallbackDataModel) {
	if data == nil || data.Text.Content == "" {
		return
	}
	// Group conversations must @ the bot to avoid noise.
	if data.ConversationType == "2" && !data.IsInAtList && len(data.AtUsers) == 0 {
		return
	}
	d.mu.Lock()
	d.webhooks[data.ConversationId] = data.SessionWebhook
	d.webhookTime[data.ConversationId] = data.SessionWebhookExpiredTime
	d.mu.Unlock()

	text := data.Text.Content
	if data.ConversationType == "2" {
		text = stripDingTalkAt(text)
	}
	d.onMessage(steward.InboundMessage{
		ChannelID:  d.channelID,
		Platform:   steward.PlatformDingTalk,
		SenderID:   firstNonEmpty(data.SenderStaffId, data.SenderId),
		SenderName: data.SenderNick,
		ThreadID:   data.ConversationId,
		Webhook:    data.SessionWebhook,
		Text:       text,
		Raw:        data,
	})
}

func (d *dingtalk) Send(ctx context.Context, msg steward.OutboundMessage) error {
	d.mu.Lock()
	webhook := d.webhooks[msg.ThreadID]
	d.mu.Unlock()
	if webhook == "" {
		return fmt.Errorf("钉钉：尚未收到会话 %s 的最近消息，暂时无法确定回复地址（请先给机器人发一条消息）", msg.ThreadID)
	}
	replier := dingchatbot.NewChatbotReplier()
	if msg.Card != nil && msg.Card.Confirm {
		body := fmt.Sprintf("%s\n%s\n\n回复「确认」允许，或「拒绝」取消。", msg.Card.Title, msg.Card.Body)
		if msg.Markdown {
			return replier.SimpleReplyMarkdown(ctx, webhook, []byte(steward.DingTalkMarkdownTitle(msg.Card.Title)), []byte(body))
		}
		return replier.SimpleReplyText(ctx, webhook, []byte(body))
	}
	text := msg.Text
	if msg.Card != nil {
		text = fmt.Sprintf("%s\n%s", msg.Card.Title, msg.Card.Body)
	}
	if msg.Markdown {
		return replier.SimpleReplyMarkdown(ctx, webhook, []byte(steward.DingTalkMarkdownTitle(text)), []byte(text))
	}
	return replier.SimpleReplyText(ctx, webhook, []byte(text))
}

func (d *dingtalk) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		d.client.Close()
	}
	return nil
}

// SeedWebhook re-arms the per-conversation reply address from persisted state
// (used before a send when no inbound has arrived yet in this session).
func (d *dingtalk) SeedWebhook(threadID, webhook string) {
	if threadID == "" || webhook == "" {
		return
	}
	d.mu.Lock()
	d.webhooks[threadID] = webhook
	d.mu.Unlock()
}

// stripDingTalkAt removes the @bot mention text from group messages.
func stripDingTalkAt(text string) string {
	out := []rune{}
	runes := []rune(text)
	skip := false
	for i := 0; i < len(runes); i++ {
		if !skip && runes[i] == '@' {
			// skip until whitespace
			skip = true
			continue
		}
		if skip {
			if runes[i] == ' ' || runes[i] == '\u3000' || runes[i] == '\n' {
				skip = false
			}
			continue
		}
		out = append(out, runes[i])
	}
	return string(out)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// dingSDKLogger bridges the DingTalk stream SDK's internal logger into the
// application log. The SDK's default logger is a silent no-op, which made
// connection/reconnect failures invisible in the daily log file.
type dingSDKLogger struct{}

func (dingSDKLogger) Debugf(format string, args ...any) {
	applog.Debugf("[dingtalk-sdk] "+format, args...)
}
func (dingSDKLogger) Infof(format string, args ...any) {
	applog.Infof("[dingtalk-sdk] "+format, args...)
}
func (dingSDKLogger) Warningf(format string, args ...any) {
	applog.Warnf("[dingtalk-sdk] "+format, args...)
}
func (dingSDKLogger) Errorf(format string, args ...any) {
	applog.Errorf("[dingtalk-sdk] "+format, args...)
}
func (dingSDKLogger) Fatalf(format string, args ...any) {
	applog.Errorf("[dingtalk-sdk] "+format, args...)
}

var wireDingTalkSDKLoggerOnce sync.Once

// wireDingTalkSDKLogger installs the log bridge exactly once (the SDK logger
// is a process-global).
func wireDingTalkSDKLogger() {
	wireDingTalkSDKLoggerOnce.Do(func() { dinglogger.SetLogger(dingSDKLogger{}) })
}
