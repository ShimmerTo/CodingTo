package store

import (
	"time"
)

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
// specific target needed for a later test or proactive message, and (for
// DingTalk) the per-conversation session webhook. It is kept in dedicated
// columns so channel configuration edits never overwrite it. An empty webhook
// (non-DingTalk platforms) never clears a previously stored one.
func (s *Store) RecordBotChannelInbound(id int64, senderID, threadID, receiveIDType, messageID, webhook string, receivedAt int64) error {
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
	if webhook != "" {
		values["last_webhook"] = webhook
		values["last_webhook_at"] = receivedAt
	}
	return s.UpdateBotChannel(id, values)
}

func (s *Store) UpdateBotChannel(id int64, values map[string]any) error {
	values["updated_at"] = time.Now().UnixMilli()
	_, err := s.db.QuickUpdate("tbl_bot_channel", map[string]any{"id": id}, values).Exec()
	return err
}

func (s *Store) DeleteBotChannel(id int64) error {
	_, err := s.db.QuickDelete("tbl_bot_channel", map[string]any{"id": id}).Exec()
	return err
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
	CompactAfterTurns int    // 超过该轮数后自动压缩上下文（默认 20）
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

// defaultCompactAfterTurns 归一化压缩轮数：非正数回退默认 20。
func defaultCompactAfterTurns(turns int) int {
	if turns <= 0 {
		return 20
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
		p.CompactAfterTurns = 20
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
		// The profile table is single-row (id=1). The row may already exist
		// (created at steward startup or by a previous save), so insert only
		// when missing and otherwise update — otherwise the INSERT with
		// id=1 hits the UNIQUE constraint on tbl_steward_profile.id.
		existing, ok, err := s.GetStewardProfile()
		if err != nil {
			return StewardProfile{}, err
		}
		if ok {
			p.ID = existing.ID
			_, err = s.db.QuickUpdate("tbl_steward_profile", map[string]any{"id": p.ID}, values).Exec()
			if err != nil {
				return StewardProfile{}, err
			}
		} else {
			values["id"] = 1
			id, err := s.db.QuickCreate("tbl_steward_profile", values).Exec()
			if err != nil {
				return StewardProfile{}, err
			}
			p.ID = asInt(id)
		}
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
	id, err := s.db.QuickCreate("tbl_bot_task", map[string]any{
		"session_id":  item.SessionID,
		"channel_id":  item.ChannelID,
		"sender":      item.Sender,
		"thread":      item.Thread,
		"status":      item.Status,
		"task_brief":  item.TaskBrief,
		"result_text": item.ResultText,
		"created_at":  item.CreatedAt,
	}).Exec()
	if err != nil {
		return BotTask{}, err
	}
	item.ID = asInt(id)
	return item, nil
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
	ID          int64
	RequestID   string
	SessionID   int64
	RunID       string
	ChannelID   int64
	Sender      string
	Method      string
	Title       string
	OptionsJSON string
	Scope       string
	Status      string
	Answer      string
	CreatedAt   int64
	AnsweredAt  int64
}

func stewardPermissionFromRow(row map[string]any) StewardPermission {
	return StewardPermission{
		ID:          asInt(row["id"]),
		RequestID:   asString(row["request_id"]),
		SessionID:   asInt(row["session_id"]),
		RunID:       asString(row["run_id"]),
		ChannelID:   asInt(row["channel_id"]),
		Sender:      asString(row["sender"]),
		Method:      asString(row["method"]),
		Title:       asString(row["title"]),
		OptionsJSON: asString(row["options_json"]),
		Scope:       asString(row["scope"]),
		Status:      asString(row["status"]),
		Answer:      asString(row["answer"]),
		CreatedAt:   asInt(row["created_at"]),
		AnsweredAt:  asInt(row["answered_at"]),
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
		"request_id":   item.RequestID,
		"session_id":   item.SessionID,
		"run_id":       item.RunID,
		"channel_id":   item.ChannelID,
		"sender":       item.Sender,
		"method":       item.Method,
		"title":        item.Title,
		"options_json": item.OptionsJSON,
		"scope":        item.Scope,
		"status":       item.Status,
		"answer":       item.Answer,
		"created_at":   item.CreatedAt,
	}).Exec()
	if err != nil {
		return StewardPermission{}, err
	}
	item.ID = asInt(id)
	return item, nil
}

func (s *Store) ListStewardPermissions() ([]StewardPermission, error) {
	rows, err := s.db.QueryBySql(`SELECT id, request_id, session_id, run_id, channel_id, sender, method, title, options_json, scope, status, answer, created_at, answered_at
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
		"id, request_id, session_id, run_id, channel_id, sender, method, title, options_json, scope, status, answer, created_at, answered_at",
		map[string]any{"request_id": requestID}).One()
	if err != nil {
		return StewardPermission{}, false, err
	}
	if len(row) == 0 {
		return StewardPermission{}, false, nil
	}
	return stewardPermissionFromRow(row), true, nil
}

func (s *Store) UpdateStewardPermission(id int64, values map[string]any) error {
	if _, has := values["answered_at"]; !has {
		values["answered_at"] = time.Now().UnixMilli()
	}
	_, err := s.db.QuickUpdate("tbl_steward_permission", map[string]any{"id": id}, values).Exec()
	return err
}
