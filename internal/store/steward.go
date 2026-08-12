package store

import (
	"fmt"
	"time"
)

// DefaultStewardCompactAfterTurns is used for new or unset resident steward
// profiles. Existing explicit values are preserved.
const DefaultStewardCompactAfterTurns = 30

// BotChannel is one IM bot connection (dingtalk/feishu/wecom) configured for
// the steward. Secret values are never persisted here; config_json holds
// platform parameters with secret references resolved by the steward secret
// store at runtime.
type BotChannel struct {
	ID                int64
	Platform          string
	Name              string
	Mode              string
	ConfigJSON        string
	Enabled           bool
	Status            string
	LastError         string
	LastSenderID      string
	LastThreadID      string
	LastReceiveIDType string
	LastMessageID     string
	LastReceivedAt    int64
	// LastWebhook is the DingTalk per-conversation session webhook from the
	// latest inbound message (valid ~2h). Other platforms leave it empty; they
	// address the sender directly.
	LastWebhook   string
	LastWebhookAt int64
	CreatedAt     int64
	UpdatedAt     int64
}

func botChannelFromRow(row map[string]any) BotChannel {
	return BotChannel{
		ID:                asInt(row["id"]),
		Platform:          asString(row["platform"]),
		Name:              asString(row["name"]),
		Mode:              asString(row["mode"]),
		ConfigJSON:        asString(row["config_json"]),
		Enabled:           asInt(row["enabled"]) != 0,
		Status:            asString(row["status"]),
		LastError:         asString(row["last_error"]),
		LastSenderID:      asString(row["last_sender_id"]),
		LastThreadID:      asString(row["last_thread_id"]),
		LastReceiveIDType: asString(row["last_receive_id_type"]),
		LastMessageID:     asString(row["last_message_id"]),
		LastReceivedAt:    asInt(row["last_received_at"]),
		LastWebhook:       asString(row["last_webhook"]),
		LastWebhookAt:     asInt(row["last_webhook_at"]),
		CreatedAt:         asInt(row["created_at"]),
		UpdatedAt:         asInt(row["updated_at"]),
	}
}

func (s *Store) ListBotChannels() ([]BotChannel, error) {
	rows, err := s.db.QueryBySql(`SELECT id, platform, name, mode, config_json, enabled, status, last_error,
		last_sender_id, last_thread_id, last_receive_id_type, last_message_id, last_received_at,
		last_webhook, last_webhook_at, created_at, updated_at
		FROM tbl_bot_channel ORDER BY id ASC`).All()
	if err != nil {
		return nil, err
	}
	items := make([]BotChannel, 0, len(rows))
	for _, row := range rows {
		items = append(items, botChannelFromRow(row))
	}
	return items, nil
}

func (s *Store) BotChannelByID(id int64) (BotChannel, bool, error) {
	row, err := s.db.QuickQuery("tbl_bot_channel",
		"id, platform, name, mode, config_json, enabled, status, last_error, last_sender_id, last_thread_id, last_receive_id_type, last_message_id, last_received_at, last_webhook, last_webhook_at, created_at, updated_at",
		map[string]any{"id": id}).One()
	if err != nil {
		return BotChannel{}, false, err
	}
	if len(row) == 0 {
		return BotChannel{}, false, nil
	}
	return botChannelFromRow(row), true, nil
}

func (s *Store) CreateBotChannel(item BotChannel) (BotChannel, error) {
	now := time.Now().UnixMilli()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Mode == "" {
		item.Mode = "stream"
	}
	if item.Status == "" {
		item.Status = "disconnected"
	}
	id, err := s.db.QuickCreate("tbl_bot_channel", map[string]any{
		"platform":             item.Platform,
		"name":                 item.Name,
		"mode":                 item.Mode,
		"config_json":          item.ConfigJSON,
		"enabled":              boolToInt(item.Enabled),
		"status":               item.Status,
		"last_error":           item.LastError,
		"last_sender_id":       item.LastSenderID,
		"last_thread_id":       item.LastThreadID,
		"last_receive_id_type": item.LastReceiveIDType,
		"last_message_id":      item.LastMessageID,
		"last_received_at":     item.LastReceivedAt,
		"last_webhook":         item.LastWebhook,
		"last_webhook_at":      item.LastWebhookAt,
		"created_at":           item.CreatedAt,
		"updated_at":           item.UpdatedAt,
	}).Exec()
	if err != nil {
		return BotChannel{}, err
	}
	item.ID = asInt(id)
	return item, nil
}

// RecordBotChannelInbound stores the latest inbound sender, the platform
// specific target needed for a later test or proactive message. Capability
// URLs such as DingTalk session webhooks are persisted by the encrypted
// SecretStore and never written to this table.
func (s *Store) RecordBotChannelInbound(id int64, senderID, threadID, receiveIDType, messageID, _ string, receivedAt int64) error {
	if receivedAt <= 0 {
		receivedAt = time.Now().UnixMilli()
	}
	values := map[string]any{
		"last_sender_id":       senderID,
		"last_thread_id":       threadID,
		"last_receive_id_type": receiveIDType,
		"last_message_id":      messageID,
		"last_received_at":     receivedAt,
	}
	return s.UpdateBotChannel(id, values)
}

func (s *Store) UpdateBotChannel(id int64, values map[string]any) error {
	values["updated_at"] = time.Now().UnixMilli()
	_, err := s.db.QuickUpdate("tbl_bot_channel", map[string]any{"id": id}, values).Exec()
	return err
}

func (s *Store) DeleteBotChannel(id int64) error {
	// Steward tables predate foreign-key constraints. Delete dependent rows
	// explicitly so channel removal cannot leave durable events or permissions
	// that later resolve to a different destination.
	tx, err := s.db.GetTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"DELETE FROM tbl_steward_dialog_state WHERE channel_id = ?",
		"DELETE FROM tbl_steward_inbound_dedup WHERE channel_id = ?",
		"DELETE FROM tbl_steward_event WHERE channel_id = ?",
		"DELETE FROM tbl_steward_permission WHERE channel_id = ?",
		"DELETE FROM tbl_bot_task WHERE channel_id = ?",
	} {
		if _, err := tx.Exec(statement, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM tbl_bot_channel WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimStewardInbound atomically records a platform message id. It returns
// false when the same channel/message pair was already processed, including by
// another inbound worker or a previous application process.
func (s *Store) ClaimStewardInbound(channelID int64, messageID string, createdAt int64) (bool, error) {
	if channelID <= 0 || messageID == "" {
		return true, nil
	}
	rows, err := s.db.ExecBySql(`INSERT OR IGNORE INTO tbl_steward_inbound_dedup
		(channel_id, message_id, created_at) VALUES (?, ?, ?)`, channelID, messageID, createdAt).Exec()
	return rows > 0, err
}

// StewardProfile is the single-row persona configuration of the steward agent.
type StewardProfile struct {
	ID       int64
	AgentID  string
	Name     string // 管家名称（用于人设与上线通知）
	Tone     string
	Prompt   string
	Provider string
	Model    string
	// IdleTimeoutMin / ResidentAlways 已被「默认持续常驻」取代：字段保留以兼容
	// 旧数据库列，但 SaveStewardProfile 始终写入 ResidentAlways=1 且不再做空闲回收。
	IdleTimeoutMin    int
	ResidentAlways    bool
	ManageScope       string // 'all' 接管所有非管家自身会话 | 'butler' 仅管家创建/继续的会话
	ResidentSessionID int64  // 常驻对话会话ID：重启后恢复复用
	CompactAfterTurns int    // 超过该轮数后自动压缩上下文（默认 30）
	Enabled           bool
	UpdatedAt         int64
}

func (s *Store) GetStewardProfile() (StewardProfile, bool, error) {
	row, err := s.db.QuickQuery("tbl_steward_profile",
		"id, agent_id, name, tone, prompt, provider, model, idle_timeout_min, resident_always, manage_all_sessions, manage_scope, resident_session_id, compact_after_turns, enabled, updated_at",
		map[string]any{"id": 1}).One()
	if err != nil {
		return StewardProfile{}, false, err
	}
	if len(row) == 0 {
		return StewardProfile{}, false, nil
	}
	return StewardProfile{
		ID:                asInt(row["id"]),
		AgentID:           asString(row["agent_id"]),
		Name:              asString(row["name"]),
		Tone:              asString(row["tone"]),
		Prompt:            asString(row["prompt"]),
		Provider:          asString(row["provider"]),
		Model:             asString(row["model"]),
		IdleTimeoutMin:    int(asInt(row["idle_timeout_min"])),
		ResidentAlways:    asInt(row["resident_always"]) != 0,
		ManageScope:       deriveManageScope(asInt(row["manage_all_sessions"]) != 0, asString(row["manage_scope"])),
		ResidentSessionID: asInt(row["resident_session_id"]),
		CompactAfterTurns: defaultCompactAfterTurns(int(asInt(row["compact_after_turns"]))),
		Enabled:           asInt(row["enabled"]) != 0,
		UpdatedAt:         asInt(row["updated_at"]),
	}, true, nil
}

// defaultCompactAfterTurns 归一化压缩轮数：非正数回退默认 30。
func defaultCompactAfterTurns(turns int) int {
	if turns <= 0 {
		return DefaultStewardCompactAfterTurns
	}
	return turns
}

// deriveManageScope 解析接管范围：新字段 manage_scope 优先，空则回退旧布尔。
func deriveManageScope(legacyAll bool, scope string) string {
	if scope == "" {
		scope = "butler"
	}
	if scope == "all" || scope == "butler" {
		return scope
	}
	if legacyAll {
		return "all"
	}
	return "butler"
}

func (s *Store) SaveStewardProfile(p StewardProfile) (StewardProfile, error) {
	now := time.Now().UnixMilli()
	p.UpdatedAt = now
	// 「默认持续常驻」：不再支持空闲回收，常驻标记始终为真。
	p.ResidentAlways = true
	if p.CompactAfterTurns <= 0 {
		p.CompactAfterTurns = DefaultStewardCompactAfterTurns
	}
	values := map[string]any{
		"agent_id":            p.AgentID,
		"name":                p.Name,
		"tone":                p.Tone,
		"prompt":              p.Prompt,
		"provider":            p.Provider,
		"model":               p.Model,
		"idle_timeout_min":    p.IdleTimeoutMin,
		"resident_always":     boolToInt(true),
		"manage_scope":        p.ManageScope,
		"manage_all_sessions": boolToInt(p.ManageScope == "all"),
		"resident_session_id": p.ResidentSessionID,
		"compact_after_turns": p.CompactAfterTurns,
		"enabled":             boolToInt(p.Enabled),
		"updated_at":          now,
	}
	if p.ID == 0 {
		// One atomic statement avoids the read-then-insert race while preserving
		// the single-row profile contract.
		_, err := s.db.ExecBySql(`INSERT INTO tbl_steward_profile
			(id, agent_id, name, tone, prompt, provider, model, idle_timeout_min,
			 resident_always, manage_scope, manage_all_sessions, resident_session_id,
			 compact_after_turns, enabled, updated_at)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			 agent_id=excluded.agent_id, name=excluded.name, tone=excluded.tone,
			 prompt=excluded.prompt, provider=excluded.provider, model=excluded.model,
			 idle_timeout_min=excluded.idle_timeout_min,
			 resident_always=excluded.resident_always,
			 manage_scope=excluded.manage_scope,
			 manage_all_sessions=excluded.manage_all_sessions,
			 resident_session_id=excluded.resident_session_id,
			 compact_after_turns=excluded.compact_after_turns,
			 enabled=excluded.enabled, updated_at=excluded.updated_at`,
			p.AgentID, p.Name, p.Tone, p.Prompt, p.Provider, p.Model,
			p.IdleTimeoutMin, boolToInt(true), p.ManageScope,
			boolToInt(p.ManageScope == "all"), p.ResidentSessionID,
			p.CompactAfterTurns, boolToInt(p.Enabled), now).Exec()
		if err != nil {
			return StewardProfile{}, err
		}
		p.ID = 1
	} else {
		_, err := s.db.QuickUpdate("tbl_steward_profile", map[string]any{"id": p.ID}, values).Exec()
		if err != nil {
			return StewardProfile{}, err
		}
	}
	p.UpdatedAt = now
	return p, nil
}

// BotTask links one bot-initiated task to a CodingTo conversation session.
type BotTask struct {
	ID         int64
	SessionID  int64
	ChannelID  int64
	Sender     string
	Thread     string
	Status     string
	TaskBrief  string
	ResultText string
	CreatedAt  int64
	FinishedAt int64
}

func botTaskFromRow(row map[string]any) BotTask {
	return BotTask{
		ID:         asInt(row["id"]),
		SessionID:  asInt(row["session_id"]),
		ChannelID:  asInt(row["channel_id"]),
		Sender:     asString(row["sender"]),
		Thread:     asString(row["thread"]),
		Status:     asString(row["status"]),
		TaskBrief:  asString(row["task_brief"]),
		ResultText: asString(row["result_text"]),
		CreatedAt:  asInt(row["created_at"]),
		FinishedAt: asInt(row["finished_at"]),
	}
}

func (s *Store) CreateBotTask(item BotTask) (BotTask, error) {
	now := time.Now().UnixMilli()
	item.CreatedAt = now
	if item.Status == "" {
		item.Status = "pending"
	}
	_, err := s.db.ExecBySql(`INSERT INTO tbl_bot_task
		(session_id, channel_id, sender, thread, status, task_brief, result_text, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
		 channel_id=excluded.channel_id, sender=excluded.sender, thread=excluded.thread,
		 status=excluded.status, task_brief=excluded.task_brief,
		 result_text=excluded.result_text, created_at=excluded.created_at,
		 finished_at=excluded.finished_at`,
		item.SessionID, item.ChannelID, item.Sender, item.Thread, item.Status,
		item.TaskBrief, item.ResultText, item.CreatedAt, item.FinishedAt).Exec()
	if err != nil {
		return BotTask{}, err
	}
	stored, ok, err := s.BotTaskBySessionID(item.SessionID)
	if err != nil {
		return BotTask{}, err
	}
	if !ok {
		return BotTask{}, fmt.Errorf("bot task upsert did not return session %d", item.SessionID)
	}
	return stored, nil
}

func (s *Store) ListBotTasks() ([]BotTask, error) {
	rows, err := s.db.QueryBySql(`SELECT id, session_id, channel_id, sender, thread, status, task_brief, result_text, created_at, finished_at
		FROM tbl_bot_task ORDER BY id DESC`).All()
	if err != nil {
		return nil, err
	}
	items := make([]BotTask, 0, len(rows))
	for _, row := range rows {
		items = append(items, botTaskFromRow(row))
	}
	return items, nil
}

func (s *Store) BotTaskBySessionID(sessionID int64) (BotTask, bool, error) {
	row, err := s.db.QuickQuery("tbl_bot_task",
		"id, session_id, channel_id, sender, thread, status, task_brief, result_text, created_at, finished_at",
		map[string]any{"session_id": sessionID}).One()
	if err != nil {
		return BotTask{}, false, err
	}
	if len(row) == 0 {
		return BotTask{}, false, nil
	}
	return botTaskFromRow(row), true, nil
}

func (s *Store) UpdateBotTask(id int64, values map[string]any) error {
	_, err := s.db.QuickUpdate("tbl_bot_task", map[string]any{"id": id}, values).Exec()
	return err
}

func (s *Store) DeleteBotTaskBySessionID(sessionID int64) error {
	_, err := s.db.QuickDelete("tbl_bot_task", map[string]any{"session_id": sessionID}).Exec()
	return err
}

// StewardPermission records one extension UI / permission request that was
// relayed to an IM channel and waits for the user's answer.
type StewardPermission struct {
	ID               int64
	RequestID        string
	SessionID        int64
	RunID            string
	ChannelID        int64
	Sender           string
	Thread           string
	Method           string
	Title            string
	Body             string
	OptionsJSON      string
	PlanJSON         string
	ReceiveIDType    string
	ReplyToMessageID string
	Scope            string
	Status           string
	Answer           string
	CreatedAt        int64
	AnsweredAt       int64
}

func stewardPermissionFromRow(row map[string]any) StewardPermission {
	return StewardPermission{
		ID:               asInt(row["id"]),
		RequestID:        asString(row["request_id"]),
		SessionID:        asInt(row["session_id"]),
		RunID:            asString(row["run_id"]),
		ChannelID:        asInt(row["channel_id"]),
		Sender:           asString(row["sender"]),
		Thread:           asString(row["thread"]),
		Method:           asString(row["method"]),
		Title:            asString(row["title"]),
		Body:             asString(row["body"]),
		OptionsJSON:      asString(row["options_json"]),
		PlanJSON:         asString(row["plan_json"]),
		ReceiveIDType:    asString(row["receive_id_type"]),
		ReplyToMessageID: asString(row["reply_to_message_id"]),
		Scope:            asString(row["scope"]),
		Status:           asString(row["status"]),
		Answer:           asString(row["answer"]),
		CreatedAt:        asInt(row["created_at"]),
		AnsweredAt:       asInt(row["answered_at"]),
	}
}

func (s *Store) CreateStewardPermission(item StewardPermission) (StewardPermission, error) {
	now := time.Now().UnixMilli()
	item.CreatedAt = now
	if item.Scope == "" {
		item.Scope = "once"
	}
	if item.Status == "" {
		item.Status = "pending"
	}
	id, err := s.db.QuickCreate("tbl_steward_permission", map[string]any{
		"request_id":          item.RequestID,
		"session_id":          item.SessionID,
		"run_id":              item.RunID,
		"channel_id":          item.ChannelID,
		"sender":              item.Sender,
		"thread":              item.Thread,
		"method":              item.Method,
		"title":               item.Title,
		"body":                item.Body,
		"options_json":        item.OptionsJSON,
		"plan_json":           item.PlanJSON,
		"receive_id_type":     item.ReceiveIDType,
		"reply_to_message_id": item.ReplyToMessageID,
		"scope":               item.Scope,
		"status":              item.Status,
		"answer":              item.Answer,
		"created_at":          item.CreatedAt,
	}).Exec()
	if err != nil {
		return StewardPermission{}, err
	}
	item.ID = asInt(id)
	return item, nil
}

func (s *Store) ListStewardPermissions() ([]StewardPermission, error) {
	rows, err := s.db.QueryBySql(`SELECT id, request_id, session_id, run_id, channel_id, sender, thread, method, title, body, options_json, plan_json, receive_id_type, reply_to_message_id, scope, status, answer, created_at, answered_at
		FROM tbl_steward_permission ORDER BY id DESC`).All()
	if err != nil {
		return nil, err
	}
	items := make([]StewardPermission, 0, len(rows))
	for _, row := range rows {
		items = append(items, stewardPermissionFromRow(row))
	}
	return items, nil
}

func (s *Store) StewardPermissionByRequestID(requestID string) (StewardPermission, bool, error) {
	row, err := s.db.QuickQuery("tbl_steward_permission",
		"id, request_id, session_id, run_id, channel_id, sender, thread, method, title, body, options_json, plan_json, receive_id_type, reply_to_message_id, scope, status, answer, created_at, answered_at",
		map[string]any{"request_id": requestID}).One()
	if err != nil {
		return StewardPermission{}, false, err
	}
	if len(row) == 0 {
		return StewardPermission{}, false, nil
	}
	return stewardPermissionFromRow(row), true, nil
}

// StewardEvent is one durable unit dispatched through the resident steward
// conversation. Waiting approvals remain in tbl_steward_permission; their
// notification event completes as soon as the steward has informed the user.
type StewardEvent struct {
	ID               int64
	Kind             string
	SessionID        int64
	RequestID        string
	ChannelID        int64
	Sender           string
	Thread           string
	ReceiveIDType    string
	ReplyToMessageID string
	PromptText       string
	FallbackText     string
	Priority         int
	Status           string
	LastError        string
	DispatchToken    string
	Attempt          int
	LeaseUntil       int64
	CreatedAt        int64
	ProcessedAt      int64
}

func stewardEventFromRow(row map[string]any) StewardEvent {
	return StewardEvent{
		ID: asInt(row["id"]), Kind: asString(row["kind"]), SessionID: asInt(row["session_id"]),
		RequestID: asString(row["request_id"]), ChannelID: asInt(row["channel_id"]),
		Sender: asString(row["sender"]), Thread: asString(row["thread"]),
		ReceiveIDType: asString(row["receive_id_type"]), ReplyToMessageID: asString(row["reply_to_message_id"]),
		PromptText: asString(row["prompt_text"]), FallbackText: asString(row["fallback_text"]),
		Priority: int(asInt(row["priority"])), Status: asString(row["status"]), LastError: asString(row["last_error"]),
		DispatchToken: asString(row["dispatch_token"]), Attempt: int(asInt(row["attempt"])), LeaseUntil: asInt(row["lease_until"]),
		CreatedAt: asInt(row["created_at"]), ProcessedAt: asInt(row["processed_at"]),
	}
}

// CreateStewardEvent persists an event before it can be dispatched.
func (s *Store) CreateStewardEvent(item StewardEvent) (StewardEvent, error) {
	item.CreatedAt = time.Now().UnixMilli()
	if item.Status == "" {
		item.Status = "queued"
	}
	id, err := s.db.QuickCreate("tbl_steward_event", map[string]any{
		"kind": item.Kind, "session_id": item.SessionID, "request_id": item.RequestID,
		"channel_id": item.ChannelID, "sender": item.Sender, "thread": item.Thread,
		"receive_id_type": item.ReceiveIDType, "reply_to_message_id": item.ReplyToMessageID,
		"prompt_text": item.PromptText, "fallback_text": item.FallbackText,
		"priority": item.Priority, "status": item.Status, "last_error": item.LastError,
		"dispatch_token": item.DispatchToken, "attempt": item.Attempt, "lease_until": item.LeaseUntil,
		"created_at": item.CreatedAt, "processed_at": item.ProcessedAt,
	}).Exec()
	if err != nil {
		return StewardEvent{}, err
	}
	item.ID = asInt(id)
	return item, nil
}

// ListDispatchableStewardEvents returns queued events plus processing events
// left behind by a previous process, ordered deterministically.
func (s *Store) ListDispatchableStewardEvents() ([]StewardEvent, error) {
	rows, err := s.db.QueryBySql(`SELECT id, kind, session_id, request_id, channel_id, sender, thread,
		receive_id_type, reply_to_message_id, prompt_text, fallback_text, priority, status,
		last_error, dispatch_token, attempt, lease_until, created_at, processed_at
		FROM tbl_steward_event WHERE status IN ('queued', 'processing')
		ORDER BY priority DESC, created_at ASC, id ASC`).All()
	if err != nil {
		return nil, err
	}
	items := make([]StewardEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, stewardEventFromRow(row))
	}
	return items, nil
}

// UpdateStewardEvent changes the lifecycle state of one durable event.
func (s *Store) UpdateStewardEvent(id int64, values map[string]any) error {
	_, err := s.db.QuickUpdate("tbl_steward_event", map[string]any{"id": id}, values).Exec()
	return err
}

// StewardDialogState remembers an ambiguous decision while the user chooses
// which pending work item it applies to.
type StewardDialogState struct {
	ID             int64
	ContextKey     string
	ChannelID      int64
	Sender         string
	Thread         string
	Intent         string
	CandidatesJSON string
	CreatedAt      int64
}

// SaveStewardDialogState upserts one per-IM-conversation clarification state.
func (s *Store) SaveStewardDialogState(item StewardDialogState) error {
	item.CreatedAt = time.Now().UnixMilli()
	_, err := s.db.ExecBySql(`INSERT INTO tbl_steward_dialog_state
		(context_key, channel_id, sender, thread, intent, candidates_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(context_key) DO UPDATE SET
		 channel_id=excluded.channel_id, sender=excluded.sender, thread=excluded.thread,
		 intent=excluded.intent, candidates_json=excluded.candidates_json,
		 created_at=excluded.created_at`,
		item.ContextKey, item.ChannelID, item.Sender, item.Thread,
		item.Intent, item.CandidatesJSON, item.CreatedAt).Exec()
	return err
}

// StewardDialogStateByKey returns the pending clarification for one IM context.
func (s *Store) StewardDialogStateByKey(key string) (StewardDialogState, bool, error) {
	row, err := s.db.QuickQuery("tbl_steward_dialog_state",
		"id, context_key, channel_id, sender, thread, intent, candidates_json, created_at",
		map[string]any{"context_key": key}).One()
	if err != nil || len(row) == 0 {
		return StewardDialogState{}, false, err
	}
	return StewardDialogState{
		ID: asInt(row["id"]), ContextKey: asString(row["context_key"]), ChannelID: asInt(row["channel_id"]),
		Sender: asString(row["sender"]), Thread: asString(row["thread"]), Intent: asString(row["intent"]),
		CandidatesJSON: asString(row["candidates_json"]), CreatedAt: asInt(row["created_at"]),
	}, true, nil
}

// DeleteStewardDialogState clears a resolved or expired clarification.
func (s *Store) DeleteStewardDialogState(key string) error {
	_, err := s.db.QuickDelete("tbl_steward_dialog_state", map[string]any{"context_key": key}).Exec()
	return err
}

func (s *Store) UpdateStewardPermission(id int64, values map[string]any) error {
	if _, has := values["answered_at"]; !has {
		values["answered_at"] = time.Now().UnixMilli()
	}
	_, err := s.db.QuickUpdate("tbl_steward_permission", map[string]any{"id": id}, values).Exec()
	return err
}

// CleanupStewardHistory removes a bounded batch of terminal control-plane
// records. Pending/queued/processing/recovery records are deliberately kept.
// The bound prevents a large legacy database from holding SQLite's write lock
// for a noticeable period during application startup.
func (s *Store) CleanupStewardHistory(before int64, limit int) error {
	if before <= 0 || limit <= 0 {
		return nil
	}
	if _, err := s.db.ExecBySql(`DELETE FROM tbl_steward_permission WHERE id IN (
		SELECT id FROM tbl_steward_permission
		WHERE status IN ('answered', 'cancelled') AND answered_at > 0 AND answered_at < ?
		ORDER BY answered_at LIMIT ?
	)`, before, limit).Exec(); err != nil {
		return err
	}
	if _, err := s.db.ExecBySql(`DELETE FROM tbl_steward_event WHERE id IN (
		SELECT id FROM tbl_steward_event
		WHERE status IN ('delivered', 'failed') AND processed_at > 0 AND processed_at < ?
		ORDER BY processed_at LIMIT ?
	)`, before, limit).Exec(); err != nil {
		return err
	}
	_, err := s.db.ExecBySql(`DELETE FROM tbl_steward_inbound_dedup WHERE rowid IN (
		SELECT rowid FROM tbl_steward_inbound_dedup
		WHERE created_at < ? ORDER BY created_at LIMIT ?
	)`, before, limit).Exec()
	return err
}
