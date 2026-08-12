package steward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"codingto/internal/piagent"
	"codingto/internal/store"
)

// DefaultPermissionTimeout bounds how long a relayed permission request waits
// for the bot user's answer before it is auto-cancelled.
const DefaultPermissionTimeout = 5 * time.Minute

// Built-in persona defaults keep a newly-created (or legacy-empty) steward
// useful before the user has customized the profile page.
const (
	DefaultStewardName   = "管家"
	DefaultStewardTone   = "简洁、专业、主动、友好"
	DefaultStewardPrompt = "准确理解用户意图，主动推进任务并及时同步关键进展；信息不足时先澄清，涉及风险或破坏性操作时先说明影响并征得确认。"
)

// CompactInstructions is the built-in compaction prompt: it tells Pi's
// summarizer which parts of the history matter most when a resident steward
// conversation is compressed after CompactAfterTurns rounds. It must keep the
// model's recent IM replies so a later short answer like "2" still resolves
// against the choices the user was shown.
const CompactInstructions = "压缩历史对话时，请尽量保留最近的近期用户问题，以及你最近通过 IM 发送给用户的消息内容（特别是列表、方案、选项等用户可能用序号或短词回复的内容），确保后续对话能理解用户回复中「2」「第二个」这类指代。"

// recentOutbound bounds the per-thread echo buffer: recentOutboundMaxEntries
// caps how many delivered messages are kept per channel:thread, and
// recentOutboundMaxChars truncates each entry so the re-injected context stays
// small and never impacts runtime performance.
const (
	recentOutboundMaxEntries = 5
	recentOutboundMaxChars   = 400
	recentOutboundTTL        = 30 * time.Minute
	inboundWorkerCount       = 8
	inboundQueueCapacity     = 256
	stewardHistoryRetention  = 30 * 24 * time.Hour
	stewardHistoryBatch      = 500
	stewardHistoryInterval   = time.Hour
)

// onlineNoticeThrottle bounds how often a single channel sends the
// "已上线" notice, so quick reconnect loops cannot spam the chat.
const onlineNoticeThrottle = time.Minute

// Service is the steward runtime: it owns channel connectors, routes inbound
// messages, tracks bot tasks, relays permission requests, and serves the
// steward-tools RPC endpoint.
type Service struct {
	store     *store.Store
	secrets   *SecretStore
	app       AppControl
	factories map[Platform]ConnectorFactory
	emit      func(name string, value any)
	logger    *log.Logger

	mu               sync.Mutex
	residentCreateMu sync.Mutex
	channels         *channelManager
	profile          store.StewardProfile
	stewardAgent     string
	stewardAgentName string
	stewardSession   int64
	current          *InboundMessage         // context of the inbound message being processed
	residentTurns    map[int64]*residentTurn // resident steward sessionID -> current inbound turn progress
	// residentTurnCount counts natural-language rounds of the resident steward
	// conversation since the last compaction. Reaching CompactAfterTurns
	// triggers a context compaction (and truncates the session event log).
	residentTurnCount int
	// lastOnlineNotice tracks the last "已上线" notice time per channel so
	// reconnect loops do not spam the chat.
	lastOnlineNotice    map[int64]time.Time
	onlineNoticePending map[int64]bool
	tasks               map[int64]store.BotTask // sessionID -> task
	managed             map[int64]bool          // sessionID -> bot-managed flag
	pending             map[string]*PermissionRequest
	permissionWaiters   map[string]chan struct{}
	profileParents      map[string]*PermissionRequest // follow-up request id -> original Browser Profile selection
	plans               map[string][]PlanStep         // session[:run] -> latest plan widget
	permissionTimeout   time.Duration
	eventQueue          []store.StewardEvent
	eventDispatching    bool
	activeEvent         *store.StewardEvent
	eventLeaseTimer     *time.Timer
	eventLeaseInterval  time.Duration

	// recentOutbound keeps the last few messages actually delivered to each IM
	// channel:thread. It is re-injected into the resident prompt so later turns
	// can resolve follow-up replies like "2" against what the user saw (tool-call
	// parameters are not a reliable record after compaction).
	recentOutbound            map[string][]outboundEcho
	recentOutboundLastCleanup time.Time

	rpcToken  string
	rpcServer *http.Server
	rpcURL    string

	runCtx       context.Context
	runCancel    context.CancelFunc
	backgroundWG sync.WaitGroup
	inboundQueue chan InboundMessage
	shuttingDown bool
}

// outboundEcho is one message the steward actually delivered to an IM channel.
type outboundEcho struct {
	Text   string
	SentAt time.Time
}

type residentTurn struct {
	ChannelID        int64
	ThreadID         string
	ReceiveIDType    string
	ReplyToMessageID string
	Text             string
	StartedAt        time.Time
	ToolCalls        int
	ReplySent        bool
	TaskStarted      bool
	EventID          int64
	FallbackText     string
	DispatchToken    string
}

func (t *residentTurn) hasProgress() bool {
	// 只有对外产生了可感知的交互才算有进展：正式回复发出，或任务已启动。
	// ToolCalls 不算：模型可能调了工具却以普通文本收尾（例如没有走
	// codingto_steward_reply），这种回合必须触发兜底提示，否则用户收不到任何回复。
	return t != nil && (t.ReplySent || t.TaskStarted)
}

// NewService builds the steward service. emit is used for frontend events
// (steward:status / steward:message / steward:permission / steward:task).
func NewService(st *store.Store, secrets *SecretStore, app AppControl, factories map[Platform]ConnectorFactory, emit func(name string, value any)) *Service {
	logger := log.New(log.Writer(), "[steward] ", log.LstdFlags)
	service := &Service{
		store: st, secrets: secrets, app: app, factories: factories, emit: emit, logger: logger,
		channels:            newChannelManager(factories, nil, logger),
		residentTurns:       make(map[int64]*residentTurn),
		tasks:               make(map[int64]store.BotTask),
		managed:             make(map[int64]bool),
		pending:             make(map[string]*PermissionRequest),
		permissionWaiters:   make(map[string]chan struct{}),
		profileParents:      make(map[string]*PermissionRequest),
		plans:               make(map[string][]PlanStep),
		permissionTimeout:   DefaultPermissionTimeout,
		recentOutbound:      make(map[string][]outboundEcho),
		lastOnlineNotice:    make(map[int64]time.Time),
		onlineNoticePending: make(map[int64]bool),
		eventLeaseInterval:  2 * time.Minute,
		inboundQueue:        make(chan InboundMessage, inboundQueueCapacity),
	}
	service.channels.onStatus = service.emitChannelStatus
	return service
}

// stewardProfileWithDefaults fills only empty persona fields. It is applied at
// every profile ingress so existing databases created before these defaults
// immediately benefit without requiring a write migration.
func stewardProfileWithDefaults(profile store.StewardProfile) store.StewardProfile {
	if strings.TrimSpace(profile.Name) == "" {
		profile.Name = DefaultStewardName
	}
	if strings.TrimSpace(profile.Tone) == "" {
		profile.Tone = DefaultStewardTone
	}
	if strings.TrimSpace(profile.Prompt) == "" {
		profile.Prompt = DefaultStewardPrompt
	}
	return profile
}

// ---- RuntimeModule ----

// Start initializes the profile, reloads bot-managed sessions, starts the RPC
// server and every enabled channel.
func (s *Service) Start(ctx context.Context) error {
	s.logger.Printf("start: loading profile")
	profile, ok, err := s.store.GetStewardProfile()
	if err != nil {
		return fmt.Errorf("steward: load profile: %w", err)
	}
	if !ok {
		profile, err = s.store.SaveStewardProfile(stewardProfileWithDefaults(store.StewardProfile{Enabled: true}))
		if err != nil {
			return fmt.Errorf("steward: create profile: %w", err)
		}
	}
	profile = stewardProfileWithDefaults(profile)
	s.mu.Lock()
	s.profile = profile
	s.runCtx, s.runCancel = context.WithCancel(ctx)
	s.shuttingDown = false
	s.mu.Unlock()
	s.logger.Printf("profile loaded: enabled=%t agent=%q name=%q provider=%q model=%q residentSession=%d compactAfterTurns=%d", profile.Enabled, profile.AgentID, profile.Name, profile.Provider, profile.Model, profile.ResidentSessionID, profile.CompactAfterTurns)

	// Restore the resident steward conversation so restart does not create a
	// fresh one: reuse the persisted session id when the session still exists.
	s.restoreResidentSession()

	// Reload bot-managed sessions so restart does not lose the mapping.
	s.reloadManagedSessions()
	s.reloadPendingPermissions()
	s.reloadStewardEvents()
	if err := s.migrateDingTalkWebhooks(); err != nil {
		s.logger.Printf("dingtalk webhook migration failed: %v", err)
	}
	if err := s.store.CleanupStewardHistory(time.Now().Add(-stewardHistoryRetention).UnixMilli(), stewardHistoryBatch); err != nil {
		s.logger.Printf("steward history cleanup failed: %v", err)
	}

	for i := 0; i < inboundWorkerCount; i++ {
		s.launchBackground(s.inboundWorker)
	}
	s.launchBackground(s.historyCleanupWorker)

	// Start the local RPC endpoint the steward-tools extension talks to.
	if err := s.startRPCServer(); err != nil {
		s.stopBackground()
		return fmt.Errorf("steward: start rpc server: %w", err)
	}

	// Wire the message callback now that the service is live.
	s.channels.onMessage = s.handleInbound

	// Start every enabled channel.
	channels, err := s.store.ListBotChannels()
	if err != nil {
		_ = s.closeRPCServer()
		s.stopBackground()
		return fmt.Errorf("steward: list channels: %w", err)
	}
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if err := s.startChannel(ch.ID); err != nil {
			s.logger.Printf("start channel %d (%s): %v", ch.ID, ch.Platform, err)
			_ = s.store.UpdateBotChannel(ch.ID, map[string]any{"status": "error", "last_error": err.Error()})
		}
	}
	s.logger.Printf("start complete: enabledChannels=%d rpc=%s", countEnabledChannels(channels), s.rpcURL)
	return nil
}

func countEnabledChannels(channels []store.BotChannel) int {
	count := 0
	for _, channel := range channels {
		if channel.Enabled {
			count++
		}
	}
	return count
}

// Shutdown stops all channels and the RPC server.
func (s *Service) Shutdown() error {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return nil
	}
	s.shuttingDown = true
	if s.eventLeaseTimer != nil {
		s.eventLeaseTimer.Stop()
		s.eventLeaseTimer = nil
	}
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.channels.stopAll()
	_ = s.closeRPCServer()
	s.waitBackground(3 * time.Second)
	return nil
}

func (s *Service) launchBackground(fn func()) bool {
	s.mu.Lock()
	if s.shuttingDown || s.runCtx == nil {
		s.mu.Unlock()
		return false
	}
	s.backgroundWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.backgroundWG.Done()
		fn()
	}()
	return true
}

func (s *Service) stopBackground() {
	s.mu.Lock()
	s.shuttingDown = true
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.waitBackground(3 * time.Second)
}

func (s *Service) waitBackground(timeout time.Duration) {
	done := make(chan struct{})
	go func() { s.backgroundWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		s.logger.Printf("background shutdown timed out after %s", timeout)
	}
}

func (s *Service) inboundWorker() {
	for {
		s.mu.Lock()
		ctx := s.runCtx
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case msg := <-s.inboundQueue:
			s.processNaturalInbound(msg)
		}
	}
}

func (s *Service) historyCleanupWorker() {
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	ticker := time.NewTicker(stewardHistoryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.CleanupStewardHistory(time.Now().Add(-stewardHistoryRetention).UnixMilli(), stewardHistoryBatch); err != nil {
				s.logger.Printf("steward history cleanup failed: %v", err)
			}
		}
	}
}

// AgentEnvironment injects the steward-tools RPC endpoint into the resident
// steward conversation only. Steward tools must never surface in a manual
// dialog or a sub-agent run, even when the underlying agent happens to be the
// resolved steward agent: without the RPC endpoint they are inert, and the
// "must always call a steward tool" policy hijacks unrelated tasks.
func (s *Service) AgentEnvironment(agentID string, sessionID int64) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stewardAgent != "" && agentID == s.stewardAgent && s.stewardSession != 0 && sessionID == s.stewardSession {
		return map[string]string{
			"CODINGTO_STEWARD_RPC_URL":   s.rpcURL,
			"CODINGTO_STEWARD_RPC_TOKEN": s.rpcToken,
		}
	}
	return map[string]string{}
}

// ---- channel lifecycle ----

func (s *Service) startChannel(channelID int64) error {
	s.logger.Printf("channel start requested: channel=%d", channelID)
	ch, ok, err := s.store.BotChannelByID(channelID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("channel not found")
	}
	config, err := parseChannelConfig(ch.ConfigJSON)
	if err != nil {
		return err
	}
	// Connector callbacks must carry the owning channel id. Persisted channel
	// config predates this field, so inject it at runtime for every platform.
	config["channelId"] = strconv.FormatInt(channelID, 10)
	secrets, err := s.secrets.Load(channelID)
	if err != nil {
		return err
	}
	// Publish the transitional state before launching the supervisor. The
	// supervisor may report a live connector immediately after it starts.
	s.emitChannelStatus(channelID, "connecting", "")
	if err := s.channels.start(channelID, Platform(ch.Platform), config, secrets); err != nil {
		s.emitChannelStatus(channelID, "error", err.Error())
		return err
	}
	s.logger.Printf("channel connecting: channel=%d platform=%s mode=%s", channelID, ch.Platform, ch.Mode)
	return nil
}

func (s *Service) stopChannel(channelID int64) {
	s.logger.Printf("channel stop requested: channel=%d", channelID)
	s.channels.stop(channelID)
	s.emitChannelStatus(channelID, "disconnected", "")
}

// StartChannel enables and connects a channel (used by the Wails API).
func (s *Service) StartChannel(channelID int64) error {
	if err := s.startChannel(channelID); err != nil {
		return err
	}
	return s.store.UpdateBotChannel(channelID, map[string]any{"enabled": true})
}

// StopChannel disconnects a channel.
func (s *Service) StopChannel(channelID int64) error {
	s.stopChannel(channelID)
	return s.store.UpdateBotChannel(channelID, map[string]any{"enabled": false})
}

// ChannelStatus returns the live connector status. The database status is
// retained for startup errors, while an active connector is always the source
// of truth for the channel list.
func (s *Service) ChannelStatus(channelID int64) (status, lastError string, active bool) {
	return s.channels.status(channelID)
}

// emitChannelStatus keeps the persisted status useful across refreshes and
// pushes changes immediately to the Steward page. Status updates happen only
// on connector lifecycle transitions, so this does not add polling or a
// periodic database workload.
func (s *Service) emitChannelStatus(channelID int64, status, lastError string) {
	if s.store != nil {
		values := map[string]any{"status": status, "last_error": lastError}
		if err := s.store.UpdateBotChannel(channelID, values); err != nil {
			s.logger.Printf("channel status persist failed: channel=%d status=%s error=%v", channelID, status, err)
		}
	}
	if s.emit != nil {
		s.emit("steward:status", map[string]any{
			"channelId": channelID,
			"status":    status,
			"lastError": lastError,
		})
	}
	if status == channelStatusRunning {
		// A platform send can involve network I/O. Keep connector supervision
		// and application startup responsive while the greeting is delivered.
		s.launchBackground(func() { s.sendOnlineNotice(channelID) })
		s.launchBackground(s.dispatchNextStewardEvent)
	}
}

// sendOnlineNotice posts a "已上线" greeting to a channel after the bot
// connects, so the user knows the steward is back. It only sends when the
// channel has a known destination (a previous inbound message) and throttles
// rapid reconnect loops to one notice per minute.
func (s *Service) sendOnlineNotice(channelID int64) {
	if channelID <= 0 {
		return
	}
	channel, ok, err := s.store.BotChannelByID(channelID)
	if err != nil || !ok {
		return
	}
	if channel.LastSenderID == "" && channel.LastThreadID == "" {
		return
	}
	s.mu.Lock()
	if last, seen := s.lastOnlineNotice[channelID]; seen && time.Since(last) < onlineNoticeThrottle {
		s.mu.Unlock()
		return
	}
	if s.onlineNoticePending[channelID] {
		s.mu.Unlock()
		return
	}
	s.onlineNoticePending[channelID] = true
	name := s.stewardNameLocked()
	s.mu.Unlock()
	text := fmt.Sprintf("😊 %s已上线~等待您的吩咐。", name)
	err = s.SendToChannel(channelID, OutboundMessage{Text: text})
	s.mu.Lock()
	delete(s.onlineNoticePending, channelID)
	if err == nil {
		s.lastOnlineNotice[channelID] = time.Now()
	}
	s.mu.Unlock()
	if err != nil {
		s.logger.Printf("online notice failed: channel=%d error=%v", channelID, err)
	}
}

// stewardNameLocked returns the configured steward name (falling back to the
// default). Caller must hold s.mu.
func (s *Service) stewardNameLocked() string {
	if name := strings.TrimSpace(s.profile.Name); name != "" {
		return name
	}
	return DefaultStewardName
}

// SendToChannel sends an outbound message through the channel connector. When
// no destination is supplied (for example a test/proactive message), it uses
// the latest persisted inbound target so the choice survives connector restarts.
// Long plain-text messages are split into multiple sends (per platform limits)
// so IM clients do not truncate them.
func (s *Service) SendToChannel(channelID int64, msg OutboundMessage) error {
	if msg.ThreadID == "" && msg.ReplyToMessageID == "" {
		resolved, err := s.lastOutboundTarget(channelID, msg)
		if err != nil {
			return err
		}
		msg = resolved
	}
	// Split only plain text messages. Card payloads are short and their plan
	// content is handled separately in QueuePermission (plan-then-confirm).
	platform := s.channelPlatform(channelID)
	if msg.Card == nil && textTooLong(msg.Text, platform) {
		for _, part := range splitOutboundText(msg.Text, platform) {
			partMsg := msg
			partMsg.Text = part
			if err := s.sendOne(channelID, partMsg); err != nil {
				return err
			}
		}
		return nil
	}
	return s.sendOne(channelID, msg)
}

// sendOne delivers a single (already size-checked) outbound message.
func (s *Service) sendOne(channelID int64, msg OutboundMessage) error {
	// Re-seed DingTalk's in-memory reply address from persisted state when no
	// inbound has arrived in this session yet (session webhooks survive ~2h).
	// The connector then finds a valid destination even right after a restart.
	s.seedConnectorWebhook(channelID, msg.ThreadID)
	if err := s.channels.send(channelID, msg); err != nil {
		s.logger.Printf("outbound failed: channel=%d thread=%q error=%s", channelID, msg.ThreadID, stewardLogPreview(err.Error()))
		return err
	}
	s.logger.Printf("outbound sent: channel=%d thread=%q kind=%s content=%s", channelID, msg.ThreadID, outboundKind(msg), stewardLogPreview(outboundLogText(msg)))
	s.emitMessage("out", channelID, msg)
	// Keep a short echo of what the user actually saw so the resident model can
	// resolve follow-up replies (e.g. "2") against it.
	s.recordOutbound(channelID, msg.ThreadID, outboundLogText(msg))
	return nil
}

// recordOutbound appends a delivered message to the per-thread echo buffer,
// skipping empty text (cards carry their body separately) and transport-level
// noise the user would never answer with a choice. The buffer is bounded per
// thread so memory stays flat and the re-injected prompt stays small.
func (s *Service) recordOutbound(channelID int64, threadID, text string) {
	text = strings.TrimSpace(text)
	if text == "" || isOutboundNoise(text) {
		return
	}
	key := outboundEchoKey(channelID, threadID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.recentOutboundLastCleanup.IsZero() || now.Sub(s.recentOutboundLastCleanup) >= 10*time.Minute {
		cutoff := now.Add(-recentOutboundTTL)
		for echoKey, buffered := range s.recentOutbound {
			kept := buffered[:0]
			for _, entry := range buffered {
				if entry.SentAt.After(cutoff) {
					kept = append(kept, entry)
				}
			}
			if len(kept) == 0 {
				delete(s.recentOutbound, echoKey)
			} else {
				s.recentOutbound[echoKey] = kept
			}
		}
		s.recentOutboundLastCleanup = now
	}
	entries := s.recentOutbound[key]
	entries = append(entries, outboundEcho{Text: truncateRunes(text, recentOutboundMaxChars), SentAt: now})
	if len(entries) > recentOutboundMaxEntries {
		entries = entries[len(entries)-recentOutboundMaxEntries:]
	}
	s.recentOutbound[key] = entries
}

// recentOutboundPrompt renders the buffered outbound messages for a
// channel:thread as an assistant-visible block, appended to the next inbound
// prompt so the model knows what the user is replying to. Returns "" when
// there is nothing to show.
func (s *Service) recentOutboundPrompt(channelID int64, threadID string) string {
	key := outboundEchoKey(channelID, threadID)
	s.mu.Lock()
	entries := append([]outboundEcho(nil), s.recentOutbound[key]...)
	s.mu.Unlock()
	return renderRecentOutbound(entries)
}

// renderRecentOutbound formats echo entries as "你最近通过 IM 发送给用户的消息"
// bullets. Pure function, unit-tested directly.
func renderRecentOutbound(entries []outboundEcho) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[你最近通过 IM 发送给用户的消息]\n")
	for _, entry := range entries {
		b.WriteString("- ")
		b.WriteString(entry.Text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// outboundEchoKey indexes the echo buffer by channel and thread, so concurrent
// conversations on different threads never leak each other's context.
func outboundEchoKey(channelID int64, threadID string) string {
	return strconv.FormatInt(channelID, 10) + ":" + threadID
}

// isOutboundNoise reports whether a delivered message is transport-level
// chatter (greetings, acks) that should not be re-injected into the prompt.
func isOutboundNoise(text string) bool {
	return strings.HasPrefix(text, "😊") && strings.Contains(text, "已上线")
}

// channelPlatform returns the persisted platform of a channel, or "" when the
// channel is missing. Used to pick the per-platform message size limit.
func (s *Service) channelPlatform(channelID int64) string {
	ch, ok, err := s.store.BotChannelByID(channelID)
	if err != nil || !ok {
		return ""
	}
	return ch.Platform
}

// dingWebhookFreshWindow bounds how long a persisted DingTalk session webhook
// is considered usable (the platform documents roughly 2 hours).
const dingWebhookFreshWindow = 90 * time.Minute

const (
	dingWebhookSecretKey       = "_dingtalkSessionWebhook"
	dingWebhookThreadSecretKey = "_dingtalkWebhookThread"
	dingWebhookAtSecretKey     = "_dingtalkWebhookAt"
)

func (s *Service) persistDingTalkWebhook(channelID int64, threadID, webhook string, receivedAt int64) error {
	if channelID <= 0 || threadID == "" || webhook == "" || s.secrets == nil {
		return nil
	}
	return s.secrets.Merge(channelID, map[string]string{
		dingWebhookSecretKey: webhook, dingWebhookThreadSecretKey: threadID,
		dingWebhookAtSecretKey: strconv.FormatInt(receivedAt, 10),
	})
}

// migrateDingTalkWebhooks moves legacy plaintext capability URLs into the
// encrypted per-channel secret blob. The old columns remain for schema
// compatibility but are cleared once the encrypted copy succeeds.
func (s *Service) migrateDingTalkWebhooks() error {
	channels, err := s.store.ListBotChannels()
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Platform != string(PlatformDingTalk) || channel.LastWebhook == "" {
			continue
		}
		if err := s.persistDingTalkWebhook(channel.ID, channel.LastThreadID, channel.LastWebhook, channel.LastWebhookAt); err != nil {
			return err
		}
		if err := s.store.UpdateBotChannel(channel.ID, map[string]any{"last_webhook": "", "last_webhook_at": 0}); err != nil {
			return err
		}
	}
	return nil
}

// seedConnectorWebhook injects the channel's persisted DingTalk session
// webhook into the active connector when it is still fresh. Non-DingTalk
// platforms and missing/stale webhooks are skipped silently.
func (s *Service) seedConnectorWebhook(channelID int64, threadID string) {
	if channelID <= 0 || threadID == "" {
		return
	}
	connector, ok := s.channels.connector(channelID)
	if !ok {
		return
	}
	seeder, ok := connector.(WebhookSeeder)
	if !ok {
		return
	}
	secrets, err := s.secrets.Load(channelID)
	if err != nil || secrets[dingWebhookSecretKey] == "" || secrets[dingWebhookThreadSecretKey] != threadID {
		return
	}
	webhookAt, _ := strconv.ParseInt(secrets[dingWebhookAtSecretKey], 10, 64)
	if webhookAt <= 0 || time.Since(time.UnixMilli(webhookAt)) > dingWebhookFreshWindow {
		s.logger.Printf("webhook seed skipped: channel=%d thread=%q reason=stale", channelID, threadID)
		return
	}
	seeder.SeedWebhook(threadID, secrets[dingWebhookSecretKey])
	s.logger.Printf("webhook seeded: channel=%d thread=%q", channelID, threadID)
}

// outboundLogText returns the full human-readable text of an outbound message
// (cards include their title and body) so replies are recorded verbatim in the
// daily log.
func outboundLogText(msg OutboundMessage) string {
	if msg.Card == nil {
		return msg.Text
	}
	text := msg.Card.Title
	if msg.Card.Body != "" {
		if text != "" {
			text += " "
		}
		text += msg.Card.Body
	}
	for index, option := range msg.Card.Options {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			label = strings.TrimSpace(option.Value)
		}
		if label != "" {
			text += fmt.Sprintf("\n%d. %s", index+1, label)
		}
	}
	return text
}

func outboundKind(msg OutboundMessage) string {
	if msg.Card != nil {
		return "card"
	}
	return "text"
}

// lastOutboundTarget selects a valid destination from the latest inbound
// message. Feishu and WeCom can address the sender directly; DingTalk's send
// API requires the conversation webhook, so it uses the persisted conversation
// id while retaining the sender id for audit/display.
func (s *Service) lastOutboundTarget(channelID int64, msg OutboundMessage) (OutboundMessage, error) {
	channel, ok, err := s.store.BotChannelByID(channelID)
	if err != nil {
		return msg, err
	}
	if !ok {
		return msg, fmt.Errorf("channel not found: %d", channelID)
	}
	switch channel.Platform {
	case string(PlatformFeishu):
		// The persisted inbound target is authoritative: group messages store a
		// chat_id, while p2p messages store an open_id. LastSenderID is audit
		// metadata and must not turn a group target into a private message.
		msg.ThreadID = channel.LastThreadID
		msg.ReceiveIDType = channel.LastReceiveIDType
		if msg.ThreadID == "" {
			msg.ThreadID = channel.LastSenderID
			msg.ReceiveIDType = "open_id"
		}
	case string(PlatformWeCom):
		msg.ThreadID = channel.LastSenderID
		if msg.ThreadID != "" {
			// Feishu sender ids are user open_ids; a group receive type must
			// not accidentally be reused when the proactive target is the sender.
			if channel.Platform == string(PlatformFeishu) {
				msg.ReceiveIDType = "open_id"
			} else {
				msg.ReceiveIDType = channel.LastReceiveIDType
			}
		} else {
			msg.ThreadID = channel.LastThreadID
			msg.ReceiveIDType = channel.LastReceiveIDType
		}
	default:
		msg.ThreadID = channel.LastThreadID
		if msg.ThreadID == "" {
			msg.ThreadID = channel.LastSenderID
		}
	}
	return msg, nil
}

// ---- inbound routing ----

func (s *Service) handleInbound(msg InboundMessage) {
	if msg.MessageID != "" {
		claimed, err := s.store.ClaimStewardInbound(msg.ChannelID, msg.MessageID, time.Now().UnixMilli())
		if err != nil {
			s.logger.Printf("inbound duplicate check failed: channel=%d error=%s", msg.ChannelID, stewardLogPreview(err.Error()))
		} else if !claimed {
			s.logger.Printf("inbound duplicate ignored: channel=%d", msg.ChannelID)
			return
		}
	}
	s.emitMessage("in", msg.ChannelID, OutboundMessage{Text: msg.Text})
	if strings.TrimSpace(msg.Text) == "" {
		s.recordInbound(msg)
		s.logInbound(msg, "ignored-empty")
		return
	}
	// Only a confirmation initiated by the resident steward itself is consumed
	// here, because its ask_confirm tool call is synchronously waiting. Replies
	// to worker/session permissions continue to the resident AI, which resolves
	// their meaning and applies decisions through scoped RPC tools.
	if s.answerInboundPermission(msg) {
		s.recordInbound(msg)
		s.logInbound(msg, "permission-answer")
		return
	}
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		s.recordInbound(msg)
		s.logInbound(msg, "command")
		s.handleCommand(msg, text)
		return
	}

	// Never make the connector callback wait for model startup or tool
	// execution. A fixed worker pool bounds resource use under an inbound burst.
	s.logInbound(msg, "natural-async")
	s.mu.Lock()
	ctx := s.runCtx
	closing := s.shuttingDown || ctx == nil
	s.mu.Unlock()
	if closing {
		return
	}
	select {
	case s.inboundQueue <- msg:
	case <-ctx.Done():
	case <-time.After(50 * time.Millisecond):
		// Preserve the message when the worker buffer is saturated. This path
		// performs only the durable/local work synchronously and skips the
		// transport-level acknowledgement instead of spawning an unbounded
		// overflow goroutine or silently dropping user input.
		s.logger.Printf("inbound worker queue saturated: channel=%d", msg.ChannelID)
		s.recordInbound(msg)
		s.handleNatural(msg)
	}
}

func (s *Service) recordInbound(msg InboundMessage) {
	if msg.ChannelID <= 0 {
		s.logger.Printf("record channel latest sender skipped: invalid channel=%d", msg.ChannelID)
		return
	}
	receivedAt := time.Now().UnixMilli()
	if msg.Platform == PlatformDingTalk && msg.Webhook != "" {
		if err := s.persistDingTalkWebhook(msg.ChannelID, msg.ThreadID, msg.Webhook, receivedAt); err != nil {
			s.logger.Printf("persist dingtalk webhook failed: channel=%d error=%v", msg.ChannelID, err)
		}
	}
	if err := s.store.RecordBotChannelInbound(msg.ChannelID, msg.SenderID, msg.ThreadID, msg.ReceiveIDType, msg.MessageID, "", receivedAt); err != nil {
		s.logger.Printf("record channel latest sender: channel=%d error=%v", msg.ChannelID, err)
	}
}

func (s *Service) processNaturalInbound(msg InboundMessage) {
	if err := s.sendInboundAck(msg); err != nil {
		s.logger.Printf("inbound ack failed: channel=%d error=%v", msg.ChannelID, err)
	}
	s.recordInbound(msg)
	s.handleNatural(msg)
}

func (s *Service) sendInboundAck(msg InboundMessage) error {
	if msg.ChannelID <= 0 {
		return fmt.Errorf("invalid channel context: channel=%d", msg.ChannelID)
	}
	ack := OutboundMessage{
		ThreadID:         msg.ThreadID,
		ReceiveIDType:    msg.ReceiveIDType,
		ReplyToMessageID: msg.ReplyToMessageID,
		Text:             "已收到，正在处理。",
	}
	if err := s.channels.sendWithTimeout(msg.ChannelID, ack, 5*time.Second); err != nil {
		return err
	}
	s.logger.Printf("inbound ack sent: channel=%d thread=%q", msg.ChannelID, msg.ThreadID)
	s.emitMessage("out", msg.ChannelID, ack)
	return nil
}

func (s *Service) armResidentTurn(sessionID int64, msg InboundMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.residentTurns[sessionID] != nil {
		return false
	}
	s.residentTurns[sessionID] = &residentTurn{
		ChannelID: msg.ChannelID, ThreadID: msg.ThreadID,
		ReceiveIDType: msg.ReceiveIDType, ReplyToMessageID: msg.ReplyToMessageID,
		Text: msg.Text, StartedAt: time.Now(),
	}
	contextMsg := msg
	s.current = &contextMsg
	return true
}

func (s *Service) clearResidentTurn(sessionID int64) {
	s.mu.Lock()
	delete(s.residentTurns, sessionID)
	s.current = nil
	s.mu.Unlock()
}

func (s *Service) markResidentTool(sessionID int64) {
	s.mu.Lock()
	if turn := s.residentTurns[sessionID]; turn != nil {
		turn.ToolCalls++
	}
	s.mu.Unlock()
}

func (s *Service) markResidentReply(sessionID int64) {
	s.mu.Lock()
	if turn := s.residentTurns[sessionID]; turn != nil {
		turn.ReplySent = true
	}
	s.mu.Unlock()
}

func (s *Service) markResidentTask(sessionID int64) {
	s.mu.Lock()
	if turn := s.residentTurns[sessionID]; turn != nil {
		turn.TaskStarted = true
	}
	s.mu.Unlock()
}

func (s *Service) finishResidentTurn(sessionID int64, dispatchToken string) (int64, int64, OutboundMessage, bool) {
	s.mu.Lock()
	turn := s.residentTurns[sessionID]
	if turn == nil || dispatchToken == "" || turn.DispatchToken != dispatchToken {
		s.mu.Unlock()
		return 0, 0, OutboundMessage{}, false
	}
	delete(s.residentTurns, sessionID)
	s.current = nil
	s.mu.Unlock()
	if turn.hasProgress() {
		return turn.EventID, 0, OutboundMessage{}, false
	}
	s.logger.Printf("resident silent turn fallback: session=%d channel=%d toolCalls=%d text=%q", sessionID, turn.ChannelID, turn.ToolCalls, stewardLogPreview(turn.Text))
	fallback := strings.TrimSpace(turn.FallbackText)
	if fallback == "" {
		fallback = "我收到你的请求了，但这轮总管没有成功触发任何管家工具。请再发一次，或稍后重试；我会继续用高思考模式处理，避免请求静默丢失。"
	}
	return turn.EventID, turn.ChannelID, OutboundMessage{
		ThreadID: turn.ThreadID, ReceiveIDType: turn.ReceiveIDType,
		ReplyToMessageID: turn.ReplyToMessageID,
		Text:             fallback,
	}, true
}

// handleNatural delivers a non-command message to the resident steward Pi.
func (s *Service) handleNatural(msg InboundMessage) {
	s.mu.Lock()
	if !s.profile.Enabled {
		s.mu.Unlock()
		s.logger.Printf("natural ignored: channel=%d reason=profile-disabled", msg.ChannelID)
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: "管家当前未启用（请在 CodingTo 管家设置中开启）。"})
		return
	}
	s.mu.Unlock()
	prompt := fmt.Sprintf("[来自 %s 的消息]\n%s", displayName(msg), msg.Text)
	pendingCount := 0
	for _, request := range s.pendingPermissionsFor(msg) {
		if request.answerCh == nil {
			pendingCount++
		}
	}
	if pendingCount > 0 {
		prompt += fmt.Sprintf("\n\n[当前 IM 上下文有 %d 个待处理审批/交互项。请先调用 codingto_steward_list_permissions 获取准确请求 ID 和内容，再根据用户本条消息的真实意图调用 codingto_steward_answer_permissions；不要仅用文字声称已处理。]", pendingCount)
	}
	if _, err := s.enqueueStewardEvent(store.StewardEvent{
		Kind: "inbound", ChannelID: msg.ChannelID, Sender: msg.SenderID, Thread: msg.ThreadID,
		ReceiveIDType: msg.ReceiveIDType, ReplyToMessageID: msg.ReplyToMessageID,
		PromptText: prompt, FallbackText: "管家处理失败，请稍后重试。", Priority: stewardPriorityNatural,
	}); err != nil {
		s.logger.Printf("queue inbound failed: channel=%d error=%v", msg.ChannelID, err)
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: "管家消息入队失败：" + err.Error()})
	}
}

// trackResidentTurn counts one natural-language round of the resident steward
// conversation and reports whether a context compaction is due. Only the
// resident conversation is counted; bot-managed task sessions are unaffected.
func (s *Service) trackResidentTurn(sessionID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stewardSession == 0 || sessionID != s.stewardSession {
		return false
	}
	s.residentTurnCount++
	return s.residentTurnCount >= s.compactAfterTurnsLocked()
}

// compactAfterTurnsLocked returns the configured compaction threshold (default
// 30). Caller must hold s.mu.
func (s *Service) compactAfterTurnsLocked() int {
	turns := s.profile.CompactAfterTurns
	if turns <= 0 {
		return store.DefaultStewardCompactAfterTurns
	}
	return turns
}

// compactResident asks Pi to compact the resident conversation context and
// resets the turn counter regardless of the outcome so a failing runtime does
// not trigger a retry on every subsequent message.
func (s *Service) compactResident(sessionID int64) error {
	s.mu.Lock()
	turns := s.compactAfterTurnsLocked()
	s.mu.Unlock()
	s.logger.Printf("resident compact triggered: session=%d turns=%d", sessionID, turns)
	err := s.app.CompactSession(sessionID, CompactInstructions)
	s.mu.Lock()
	s.residentTurnCount = 0
	s.mu.Unlock()
	if err == nil {
		s.logger.Printf("resident compact dispatched: session=%d", sessionID)
	}
	return err
}

// personaPrompt composes the steward's name/tone/prompt into a system-style
// instruction block prepended to each natural-language message.
func (s *Service) personaPrompt() string {
	s.mu.Lock()
	profile := s.profile
	s.mu.Unlock()
	var b strings.Builder
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = DefaultStewardName
	}
	fmt.Fprintf(&b, "【名称】你的名字是「%s」。\n", name)
	if strings.TrimSpace(profile.Tone) != "" {
		fmt.Fprintf(&b, "【语气】%s\n", profile.Tone)
	}
	b.WriteString("【职责】你是 CodingTo 的管家：通过 codingto_steward_* 工具管理环境、会话与授权；" +
		"每轮都必须调用至少一个 codingto_steward_* 工具，不能只输出普通文本；" +
		"回复用户必须使用 codingto_steward_reply 工具；遇到审批或交互回复时先调用 codingto_steward_list_permissions，再按用户语义调用 codingto_steward_answer_permissions，工具成功前不得声称已经处理；" +
		"用户要求继续既有对话时调用 codingto_steward_continue_task，要求创建/启动/派发独立新任务时调用 codingto_steward_start_task，再用 codingto_steward_reply 告知结果；" +
		"无法执行或需要澄清时也必须调用 codingto_steward_reply 说明；执行破坏性操作（删除环境/对话）前先用 codingto_steward_ask_confirm 向用户确认。\n")
	if strings.TrimSpace(profile.Prompt) != "" {
		b.WriteString(profile.Prompt)
		b.WriteString("\n")
	}
	return b.String()
}

// restoreResidentSession reuses the persisted resident conversation across
// restarts: it validates that the session still exists before binding it, and
// clears the persisted reference when the user deleted the session (or it is
// otherwise gone) so the next inbound message creates a fresh one.
func (s *Service) restoreResidentSession() {
	s.mu.Lock()
	id := s.profile.ResidentSessionID
	s.mu.Unlock()
	if id <= 0 {
		return
	}
	if !s.sessionExists(id) {
		s.logger.Printf("resident session restore skipped: session=%d reason=not-found", id)
		s.persistResidentSession(0)
		return
	}
	s.mu.Lock()
	s.stewardSession = id
	s.mu.Unlock()
	_ = s.app.PinSession(id)
	s.logger.Printf("resident session restored: session=%d", id)
}

// persistResidentSession writes the resident session id back into the profile
// so a restart reuses the same conversation.
func (s *Service) persistResidentSession(sessionID int64) {
	s.mu.Lock()
	profile := s.profile
	profile.ResidentSessionID = sessionID
	s.profile = profile
	s.mu.Unlock()
	if _, err := s.store.SaveStewardProfile(profile); err != nil {
		s.logger.Printf("persist resident session failed: session=%d error=%v", sessionID, err)
	}
}

// sessionExists reports whether the session id exists in the conversation list.
func (s *Service) sessionExists(sessionID int64) bool {
	sessions, err := s.app.ListSessions()
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return true
		}
	}
	return false
}

func (s *Service) ensureStewardSession(agentID, agentName string) (int64, error) {
	s.residentCreateMu.Lock()
	defer s.residentCreateMu.Unlock()

	s.mu.Lock()
	if s.stewardSession != 0 {
		id := s.stewardSession
		s.mu.Unlock()
		s.logger.Printf("resident session reused: session=%d", id)
		return id, nil
	}
	provider, model := "", ""
	if profile, ok, _ := s.store.GetStewardProfile(); ok {
		provider, model = profile.Provider, profile.Model
	}
	s.mu.Unlock()
	session, err := s.app.CreateSession(agentID, "", "管家-"+agentName, provider, model)
	if err != nil {
		return 0, err
	}
	s.logger.Printf("resident session created: session=%d agent=%q provider=%q model=%q thinking=high", session.ID, agentID, provider, model)
	s.mu.Lock()
	if s.stewardSession == 0 {
		s.stewardSession = session.ID
	}
	id := s.stewardSession
	s.mu.Unlock()
	_ = s.app.PinSession(id)
	// 持久化常驻会话ID：重启后恢复复用，避免每次启动新建对话。
	s.persistResidentSession(id)
	return id, nil
}

// ToolKey is the default_tools directory name of the steward toolset.
const ToolKey = "steward"

// resolveStewardAgent resolves the configured (or default) agent for the
// steward and caches it. Called once at startup and on profile save.
func (s *Service) resolveStewardAgent() {
	s.mu.Lock()
	profile := s.profile
	s.mu.Unlock()
	agent, err := s.app.ResolveAgent(profile.AgentID)
	if err != nil {
		s.logger.Printf("resolve agent failed: error=%v", err)
		return
	}
	s.mu.Lock()
	s.stewardAgent = agent.ID
	s.stewardAgentName = agent.Name
	s.mu.Unlock()
	s.logger.Printf("agent resolved: id=%q name=%q", agent.ID, agent.Name)

	// The steward toolset is a hard requirement of the resident agent: it is
	// the only delivery path for IM replies (steward_reply). Materialize it
	// even when the agent's own settings do not list it, so the model can
	// always reach the bot channel.
	if dir, ok := s.app.AgentDataDir(agent.ID); ok {
		if err := piagent.MaterializeBuiltinTool(dir, ToolKey); err != nil {
			s.logger.Printf("materialize steward tool failed: agent=%q error=%v", agent.ID, err)
		} else {
			s.logger.Printf("steward tool materialized: agent=%q dir=%q", agent.ID, dir)
		}
		// 内置压缩提示词：压缩历史对话时尽量保留最近的近期用户问题。仅在文件
		// 缺失时写入默认内容，用户可后续在 Agent 配置中覆盖。
		compressPromptPath := filepath.Join(dir, "PROMPT_COMPRESS.md")
		if data, err := os.ReadFile(compressPromptPath); err != nil || strings.TrimSpace(string(data)) == "" {
			if werr := os.WriteFile(compressPromptPath, []byte(CompactInstructions+"\n"), 0o600); werr != nil {
				s.logger.Printf("write default compress prompt failed: agent=%q error=%v", agent.ID, werr)
			}
		}
	} else {
		s.logger.Printf("steward tool materialize skipped: agent data dir unavailable agent=%q", agent.ID)
	}
}

// SetProfile applies a new persona profile and re-resolves the agent.
func (s *Service) SetProfile(p store.StewardProfile) error {
	// 人设视图不携带常驻会话ID：合并当前持久化的值，避免保存人设时清掉它。
	s.mu.Lock()
	p.ResidentSessionID = s.profile.ResidentSessionID
	s.mu.Unlock()
	p = stewardProfileWithDefaults(p)
	s.logger.Printf("profile save requested: agent=%q name=%q provider=%q model=%q compactAfterTurns=%d enabled=%t", p.AgentID, p.Name, p.Provider, p.Model, p.CompactAfterTurns, p.Enabled)
	saved, err := s.store.SaveStewardProfile(p)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.profile = saved
	s.mu.Unlock()
	s.resolveStewardAgent()
	s.logger.Printf("profile saved: agent=%q name=%q provider=%q model=%q enabled=%t", saved.AgentID, saved.Name, saved.Provider, saved.Model, saved.Enabled)
	if saved.Enabled {
		s.launchBackground(s.dispatchNextStewardEvent)
	}
	return nil
}

// Profile returns the current persona profile.
func (s *Service) Profile() store.StewardProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stewardProfileWithDefaults(s.profile)
}

// ResolvedAgentID returns the id of the agent currently driving the steward,
// or "" before the first resolution. The app uses it to protect the forced
// steward toolset from config-save cleanups.
func (s *Service) ResolvedAgentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stewardAgent
}

// ---- task tracking ----

func (s *Service) reloadManagedSessions() {
	tasks, err := s.store.ListBotTasks()
	if err != nil {
		s.logger.Printf("list tasks failed: error=%v", err)
		return
	}
	s.mu.Lock()
	for _, t := range tasks {
		if t.Status == "pending" || t.Status == "running" {
			s.managed[t.SessionID] = true
		}
		s.tasks[t.SessionID] = t
	}
	s.mu.Unlock()
}

// IsBotManaged reports whether the steward should take over the given session:
//   - 管家自身（常驻）会话永远不接管；
//   - "all" 模式：接管所有非管家自身的会话；
//   - "butler" 模式（默认）：仅接管管家创建/继续的会话（managed 映射）。
func (s *Service) IsBotManaged(sessionID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 管家自身会话永不被接管（避免与常驻对话自循环）
	if s.stewardSession != 0 && s.stewardSession == sessionID {
		return false
	}
	switch s.takeoverScopeLocked() {
	case "all":
		return true
	default: // "butler"
		return s.managed[sessionID]
	}
}

// takeoverScopeLocked 必须在已持有 s.mu 时调用。
func (s *Service) takeoverScopeLocked() string {
	if s.profile.ManageScope == "all" || s.profile.ManageScope == "butler" {
		return s.profile.ManageScope
	}
	return "butler"
}

// takeoverScope 返回当前接管范围（加锁安全版本）。
func (s *Service) takeoverScope() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeoverScopeLocked()
}

// reportTarget 为没有绑定机器人任务（tbl_bot_task）的会话解析一个回退授权
// 目标。授权只能由一个渠道回答，因此仍取首个已启用、且有最近收信人的渠道。
// 本地会话的完成结果由 reportResultToEnabledChannels 广播到全部启用渠道。
func (s *Service) reportTarget() (channelID int64, sender, thread string) {
	channels, err := s.store.ListBotChannels()
	if err != nil {
		return 0, "", ""
	}
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if ch.LastSenderID == "" && ch.LastThreadID == "" {
			continue
		}
		return ch.ID, ch.LastSenderID, ch.LastThreadID
	}
	return 0, "", ""
}

// reportResultToEnabledChannels queues an unbound local-session result for the
// resident steward once per enabled channel. An empty destination lets the
// eventual steward_reply resolve each platform's latest persisted target.
func (s *Service) reportResultToEnabledChannels(sessionID int64, resultText string) int {
	channels, err := s.store.ListBotChannels()
	if err != nil {
		s.logger.Printf("task finish broadcast failed: session=%d list-channels error=%v", sessionID, err)
		return 0
	}
	enabled := make([]store.BotChannel, 0, len(channels))
	for _, channel := range channels {
		if channel.Enabled {
			enabled = append(enabled, channel)
		}
	}
	for _, channel := range enabled {
		prompt := fmt.Sprintf("接管的对话 #%d 已结束。请阅读以下结果，使用 codingto_steward_reply 向用户发送清晰、完整的结果；保留其中的列表、选项和待用户决定的内容，方便用户继续回复：\n\n%s", sessionID, resultText)
		if _, err := s.enqueueStewardEvent(store.StewardEvent{
			Kind: "task_result", SessionID: sessionID, ChannelID: channel.ID,
			Sender:     channel.LastSenderID,
			PromptText: prompt, FallbackText: resultText, Priority: stewardPriorityResult,
		}); err != nil {
			s.logger.Printf("task finish queue failed: session=%d channel=%d error=%v", sessionID, channel.ID, err)
		}
	}
	return len(enabled)
}

// RegisterTask records a bot task bound to a session.
func (s *Service) RegisterTask(sessionID, channelID int64, sender, thread, brief string) (int64, error) {
	task, err := s.store.CreateBotTask(store.BotTask{
		SessionID: sessionID, ChannelID: channelID, Sender: sender, Thread: thread,
		Status: "running", TaskBrief: brief,
	})
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.tasks[sessionID] = task
	s.managed[sessionID] = true
	s.mu.Unlock()
	s.logger.Printf("task registered: task=%d session=%d channel=%d sender=%q thread=%q brief=%q", task.ID, sessionID, channelID, sender, thread, stewardLogPreview(brief))
	s.emitTask(task, "started")
	return task.ID, nil
}

// FinishTask marks a task settled and sends the result to its channel.
func (s *Service) FinishTask(sessionID int64, status, resultText string) {
	s.mu.Lock()
	task, ok := s.tasks[sessionID]
	channelID, thread := task.ChannelID, task.Thread
	s.mu.Unlock()
	if !ok {
		// 无绑定任务（本地会话）："all" 模式向所有已启用渠道尽力广播结果。
		if s.takeoverScope() != "all" {
			s.logger.Printf("task finish ignored: session=%d reason=task-not-found", sessionID)
			return
		}
		if attempted := s.reportResultToEnabledChannels(sessionID, resultText); attempted == 0 {
			s.logger.Printf("task finish ignored: session=%d reason=no-enabled-channel", sessionID)
		}
		return
	}
	if task.Status != "pending" && task.Status != "running" {
		s.logger.Printf("task finish ignored: task=%d session=%d reason=already-%s", task.ID, sessionID, task.Status)
		return
	}
	now := time.Now().UnixMilli()
	if err := s.store.UpdateBotTask(task.ID, map[string]any{"status": status, "result_text": resultText, "finished_at": now}); err != nil {
		s.logger.Printf("task finish persistence failed: task=%d error=%v", task.ID, err)
	}
	s.mu.Lock()
	updated := task
	updated.Status = status
	updated.ResultText = resultText
	updated.FinishedAt = now
	s.tasks[sessionID] = updated
	s.mu.Unlock()
	s.logger.Printf("task finished: task=%d session=%d status=%s result=%q", task.ID, sessionID, status, stewardLogPreview(resultText))
	s.emitTask(updated, "finished")

	if channelID > 0 {
		prompt := fmt.Sprintf("接管的对话 #%d（%s）已结束。请阅读以下结果并理解下一步语义：可直接用 codingto_steward_reply 告知结果；用户后续要求基于此结果继续时使用 codingto_steward_continue_task(sessionId=%d)，若是独立工作则使用 codingto_steward_start_task 新建对话。保留结果中的列表、选项和待用户决定内容：\n\n%s", sessionID, task.TaskBrief, sessionID, resultText)
		if _, err := s.enqueueStewardEvent(store.StewardEvent{
			Kind: "task_result", SessionID: sessionID, ChannelID: channelID,
			Sender: task.Sender, Thread: thread, PromptText: prompt,
			FallbackText: resultText, Priority: stewardPriorityResult,
		}); err != nil {
			s.logger.Printf("task finish queue failed: task=%d session=%d error=%v", task.ID, sessionID, err)
			_ = s.SendToChannel(channelID, OutboundMessage{ThreadID: thread, Text: resultText, Markdown: true})
		}
	}
}

// Tasks returns the current bot task list (for the Wails API).
func (s *Service) Tasks() []store.BotTask {
	tasks, err := s.store.ListBotTasks()
	if err != nil {
		return nil
	}
	return tasks
}

// DeleteTaskBySession removes the bot task bound to a deleted session.
func (s *Service) DeleteTaskBySession(sessionID int64) {
	s.finishTaskBySession(sessionID, "aborted", "🗑 对话已删除。")
}

// ---- frontend events ----

func (s *Service) emitMessage(direction string, channelID int64, msg OutboundMessage) {
	if s.emit == nil {
		return
	}
	value := map[string]any{"direction": direction, "channelId": channelID, "text": msg.Text}
	if msg.Card != nil {
		value["card"] = msg.Card
	}
	s.emit("steward:message", value)
}

func (s *Service) emitTask(task store.BotTask, state string) {
	if s.emit == nil {
		return
	}
	s.emit("steward:task", map[string]any{
		"taskId": task.ID, "sessionId": task.SessionID, "state": state,
		"status": task.Status, "channelId": task.ChannelID, "sender": task.Sender,
		"brief": task.TaskBrief, "result": task.ResultText,
	})
}

// ---- helpers ----

func displayName(msg InboundMessage) string {
	if msg.SenderName != "" {
		return msg.SenderName
	}
	return msg.SenderID
}

// parseChannelConfig decodes the config_json blob into a string map.
func parseChannelConfig(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, fmt.Errorf("decode channel config: %w", err)
	}
	return config, nil
}

// randomToken returns a 32-byte hex token for the RPC endpoint.
func randomToken() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// startRPCServer binds the local HTTP endpoint for steward-tools.
func (s *Service) startRPCServer() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token := randomToken()
	s.mu.Lock()
	s.rpcToken = token
	s.rpcURL = "http://" + listener.Addr().String()
	s.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", s.handleRPC)
	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: DefaultPermissionTimeout + 30*time.Second, IdleTimeout: 60 * time.Second,
	}
	s.mu.Lock()
	s.rpcServer = server
	s.mu.Unlock()
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.logger.Printf("rpc server stopped unexpectedly: %v", serveErr)
		}
	}()
	s.logger.Printf("rpc ready: url=%s", s.rpcURL)
	return nil
}

func (s *Service) closeRPCServer() error {
	s.mu.Lock()
	server := s.rpcServer
	s.rpcServer = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
