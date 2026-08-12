// Package steward implements the always-on "管家" (Steward) agent: it keeps
// IM bot long connections (DingTalk / Feishu / WeCom) alive, routes incoming
// bot messages to commands or the resident steward Pi agent, manages bot
// tasks bound to CodingTo conversations, relays permission requests to the
// IM user, and reports task results back to the originating channel.
//
// internal/steward never imports internal/app; it depends on the narrow
// AppControl interface so the Wails boundary (internal/app) wires the real
// capabilities and tests can substitute a fake.
package steward

import (
	"context"
	"time"
)

// Platform identifies an IM bot provider.
type Platform string

const (
	PlatformDingTalk Platform = "dingtalk"
	PlatformFeishu   Platform = "feishu"
	PlatformWeCom    Platform = "wecom"
	PlatformLoopback Platform = "loopback"
)

// InboundMessage is a normalized message received from an IM channel.
type InboundMessage struct {
	ChannelID  int64
	Platform   Platform
	SenderID   string
	SenderName string
	ThreadID   string // group chat id or private chat open-conversation id
	// ReceiveIDType is the Feishu receive_id_type for ThreadID when known.
	// Other connectors leave it empty.
	ReceiveIDType string
	// ReplyToMessageID lets Feishu reply through the message reply API,
	// avoiding receive_id conversion for the immediate response.
	ReplyToMessageID string
	// MessageID is the platform's stable inbound id used for durable duplicate
	// suppression. Connectors that cannot provide one leave it empty.
	MessageID string
	// Webhook is the DingTalk per-conversation session webhook (valid ~2h)
	// carried by the latest inbound message. It is persisted so replies and
	// test sends still work after a restart or reconnect. Other platforms
	// leave it empty.
	Webhook string
	Text    string
	Raw     any
}

// CardOption is one selectable option rendered as a button on IM cards.
type CardOption struct {
	Label         string `json:"label"`
	Value         string `json:"value"`
	Description   string `json:"description,omitempty"`
	CreateProfile bool   `json:"createProfile,omitempty"`
	TargetURL     string `json:"targetUrl,omitempty"`
}

// CardPayload is an optional rich card attached to an outbound message.
// Confirm marks the card as a binary allow/deny confirmation.
type CardPayload struct {
	Title   string       `json:"title"`
	Body    string       `json:"body"`
	Options []CardOption `json:"options,omitempty"`
	Confirm bool         `json:"confirm,omitempty"`
}

// OutboundMessage is a normalized message sent to an IM channel.
type OutboundMessage struct {
	ThreadID string
	// ReceiveIDType is used by Feishu when ThreadID is a user open_id rather
	// than a chat_id. Empty means the connector infers it from the ID.
	ReceiveIDType string
	// ReplyToMessageID uses Feishu's reply endpoint when available.
	ReplyToMessageID string
	Text             string
	Card             *CardPayload
	// Markdown sends the message with the platform's markdown message type
	// (DingTalk markdown / Feishu interactive card / WeCom markdown) so the
	// bot client renders formatting instead of raw text. Plain-text sends
	// leave it false.
	Markdown bool
}

// Connector is the transport abstraction for one IM platform. Implementations
// must be safe for concurrent Send calls; Connect is called once by the
// channel manager, which also drives reconnects with backoff.
type Connector interface {
	// Connect establishes the long connection (or starts the callback
	// server), calls ready when outbound sends are safe, and blocks until the
	// connection is lost or ctx is cancelled. The manager de-duplicates ready
	// calls within an attempt. Returning an error before ready keeps the channel
	// in connecting state and retries it.
	// Incoming messages are delivered through the callback registered via
	// the connector factory.
	Connect(ctx context.Context, ready func()) error
	Send(ctx context.Context, msg OutboundMessage) error
	Close() error
}

// ConnectorFactory builds a Connector from resolved channel configuration.
// config holds non-secret platform parameters; secrets holds the decrypted
// credentials for this channel. onMessage must be called for every inbound
// message. The returned connector must deliver messages until ctx is
// cancelled (the manager owns the context).
type ConnectorFactory func(config map[string]string, secrets map[string]string, onMessage func(InboundMessage)) (Connector, error)

// WebhookSeeder is implemented by connectors that keep per-conversation reply
// addresses in memory (DingTalk session webhooks) and can be re-seeded from
// persisted state so sends work after a restart before any new inbound.
type WebhookSeeder interface {
	SeedWebhook(threadID, webhook string)
}

// ---- Views passed over the AppControl boundary ----

// SessionView is the minimal session representation the steward needs.
type SessionView struct {
	ID             int64  `json:"id"`
	AgentID        string `json:"agentId"`
	EnvironmentID  string `json:"environmentId"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	ExecDurationMs int64  `json:"execDurationMs"`
	RunningSince   int64  `json:"runningSince,omitempty"`
	LastActivity   string `json:"lastActivity,omitempty"`
	LastEventType  string `json:"lastEventType,omitempty"`
	LastEventAt    int64  `json:"lastEventAt,omitempty"`
	CreateTime     int64  `json:"createTime"`
	UpdateTime     int64  `json:"updateTime"`
}

// EnvironmentView is the minimal environment representation the steward needs.
type EnvironmentView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
}

// AgentView is the minimal agent profile representation the steward needs.
type AgentView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	DefaultProvider string `json:"defaultProvider"`
	DefaultModel    string `json:"defaultModel"`
}

// SubagentUIAnswer is the shape needed to answer a subagent UI request.
type SubagentUIAnswer struct {
	ID        string `json:"id"`
	Value     any    `json:"value,omitempty"`
	Confirmed *bool  `json:"confirmed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// AppControl is implemented by the Wails App / AgentService. It is the only
// bridge between the steward and the existing conversation runtime.
type AppControl interface {
	ListSessions() ([]SessionView, error)
	CreateSession(agentID, envID, title, provider, model string) (SessionView, error)
	StartPrompt(sessionID int64, message string) error
	// StartStewardPrompt starts a resident turn with a durable correlation
	// token that is echoed on lifecycle events and persisted in session logs.
	StartStewardPrompt(sessionID int64, message, dispatchToken string) error
	SessionRuntimeState(sessionID int64) SessionRuntimeState
	RecoverStewardTurn(sessionID int64, dispatchToken string) (StewardTurnRecovery, error)
	StopSession(sessionID int64) error
	DeleteSession(sessionID int64) error
	ListEnvironments() ([]EnvironmentView, error)
	// AddEnvironment appends a new environment (read-modify-write, since the
	// underlying store replaces the whole list).
	AddEnvironment(env EnvironmentView) ([]EnvironmentView, error)
	ListAgents() ([]AgentView, error)
	// ResolveAgent returns the agent for an id or name prefix; empty means
	// the default agent.
	ResolveAgent(key string) (AgentView, error)
	// AgentDataDir returns the absolute data directory of an agent, whose
	// extensions/ folder hosts its Pi tools. Used to force-materialize the
	// steward toolset for the resident steward agent.
	AgentDataDir(agentID string) (string, bool)
	// AckExtensionUI acknowledges a main-agent interactive UI request so the
	// watchdog is disarmed while the steward manages its own timeout.
	AckExtensionUI(sessionID int64, requestID string) error
	// SendExtensionUIResponse answers a main-agent interactive UI request.
	SendExtensionUIResponse(sessionID int64, requestID string, confirmed *bool, value any) error
	// AckSubagentUI acknowledges a subagent interactive UI request.
	AckSubagentUI(sessionID int64, runID, requestID string) error
	// RespondSubagentUI answers a subagent interactive UI request.
	RespondSubagentUI(sessionID int64, runID string, answer SubagentUIAnswer) error
	// SaveBrowserProfile creates a global persistent profile for a remote
	// Browser Profile selection flow and returns its immutable id.
	SaveBrowserProfile(key, targetURL string) (string, error)
	// PinSession marks a session runtime as non-evictable (the resident
	// steward conversation).
	PinSession(sessionID int64) error
	// RemoveEnvironment deletes an environment by id.
	RemoveEnvironment(envID string) error
	// CompactSession triggers a context compaction on the session's runtime
	// with the given instructions for the summarizer. Empty instructions let
	// Pi use its built-in compaction behavior.
	CompactSession(sessionID int64, instructions string) error
}

// SessionRuntimeState is the lease check used by the steward queue. Known is
// false when no materialized runtime exists for the conversation.
type SessionRuntimeState struct {
	Known   bool
	Running bool
}

// StewardTurnRecovery is reconstructed from the durable session event log.
// Started without Settled is intentionally treated as uncertain, never as a
// safe automatic retry.
type StewardTurnRecovery struct {
	Started bool
	Settled bool
}

// PlanStep is one parsed plan item from the plan-todos widget.
type PlanStep struct {
	Index     int    `json:"index"`
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

// PermissionAnswer is the user's reply to a relayed permission/confirmation
// request, resolved from IM text.
type PermissionAnswer struct {
	Confirmed *bool
	Value     any
	Cancelled bool
	Raw       string
}

// PermissionRequest is an interactive extension UI request that must be
// relayed to the bot user, or a steward-initiated confirmation that waits for
// the bot user's answer.
type PermissionRequest struct {
	RecordID         int64
	RequestID        string
	SessionID        int64
	RunID            string // empty for main-agent requests
	Method           string // select | confirm | input | editor
	Title            string
	Body             string
	Options          []CardOption
	Plan             []PlanStep
	ChannelID        int64
	Sender           string
	ThreadID         string
	ReceiveIDType    string
	ReplyToMessageID string
	CreatedAt        time.Time
	// ProfileParent is set for the second step of a remote Browser Profile
	// creation flow. It is kept in memory only; the original request remains
	// the one answered back to the extension.
	ProfileParent *PermissionRequest
	answerCh      chan *PermissionAnswer
}

// PublicPermissionView is the JSON shape emitted to the frontend.
type PublicPermissionView struct {
	ID        int64        `json:"id"`
	Code      string       `json:"code"`
	RequestID string       `json:"requestId"`
	SessionID int64        `json:"sessionId"`
	RunID     string       `json:"runId,omitempty"`
	ChannelID int64        `json:"channelId"`
	Method    string       `json:"method"`
	Title     string       `json:"title"`
	Body      string       `json:"body"`
	Options   []CardOption `json:"options,omitempty"`
	Scope     string       `json:"scope"`
	Status    string       `json:"status"`
	Answer    string       `json:"answer,omitempty"`
	CreatedAt int64        `json:"createdAt"`
}

// ReplySender sends outbound messages; implemented by Service so command
// handlers and the relay share one send path.
type ReplySender interface {
	SendToChannel(channelID int64, msg OutboundMessage) error
}
