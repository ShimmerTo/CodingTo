package store

import (
	"strconv"
	"strings"
	"time"
)

// TokenUsage is one per-request token consumption record written when a model
// turn completes. Each assistant message contributes one row so the stats can
// be aggregated by day (day) or conversation (session_id).
type TokenUsage struct {
	ID           int64
	RequestKey   string
	Day          string
	SessionID    int64
	SessionTitle string
	AgentID      string
	Provider     string
	Model        string
	API          string
	Input        int64
	CacheRead    int64
	CacheWrite   int64
	Output       int64
	Total        int64
	StopReason   string
	Success      bool
	CreateTime   int64
}

// TokenUsageSum is one aggregation bucket: either a calendar day or a session.
// For the per-session bucket the provider/model/agent are the latest values of
// that conversation (a session may switch model mid-flight).
type TokenUsageSum struct {
	Day              string
	SessionID        int64
	SessionTitle     string
	SessionCreatedAt int64
	AgentID          string
	Provider         string
	Model            string
	RequestCount     int64
	SessionCount     int64
	ModelCount       int64
	Input            int64
	Cached           int64
	CacheWrite       int64
	Output           int64
	Total            int64
}

// TokenUsagePageQuery defines one bounded cursor page over persisted model
// requests. A nil SessionID includes all sessions; a non-nil zero value selects
// the explicit model-test bucket.
type TokenUsagePageQuery struct {
	StartDay   string
	EndDay     string
	SessionID  *int64
	BeforeTime int64
	BeforeID   int64
	Limit      int
}

// RecordTokenUsage appends one request's token consumption for later day/session
// aggregation. It never mutates existing rows, so a mis-timestamped record can
// at worst land on the wrong day without corrupting other statistics.
func (s *Store) RecordTokenUsage(item TokenUsage) (int64, error) {
	createdAt := item.CreateTime
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	id, err := s.db.ExecBySql(`INSERT OR IGNORE INTO tbl_token_usage
		(day, session_id, agent_id, provider, model, input, cache_read, cache_write,
		 output, total, create_time, request_key, api, stop_reason, success)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Day, item.SessionID, item.AgentID, item.Provider, item.Model,
		item.Input, item.CacheRead, item.CacheWrite, item.Output, item.Total,
		createdAt, item.RequestKey, item.API, item.StopReason, boolToInt(item.Success)).Exec()
	if err != nil {
		return 0, err
	}
	return asInt(id), nil
}

// DeleteTokenUsageBefore removes records strictly older than cutoffDay. The
// explicit day predicate is the safety boundary for the fixed 60-day retention.
func (s *Store) DeleteTokenUsageBefore(cutoffDay string) (int64, error) {
	return s.db.ExecBySql("DELETE FROM tbl_token_usage WHERE day < ?", cutoffDay).Exec()
}

// TokenUsageByDaySince aggregates recent token consumption by calendar day.
func (s *Store) TokenUsageByDaySince(startDay, endDay string) ([]TokenUsageSum, error) {
	rows, err := s.db.QueryBySql(`SELECT day, COUNT(*) AS request_count,
		COUNT(DISTINCT session_id) AS session_count,
		COUNT(DISTINCT provider || CHAR(0) || model) AS model_count,
		COALESCE(SUM(input), 0) AS input,
		COALESCE(SUM(cache_read), 0) AS cache_read,
		COALESCE(SUM(cache_write), 0) AS cache_write,
		COALESCE(SUM(output), 0) AS output,
		COALESCE(SUM(total), 0) AS total
		FROM tbl_token_usage WHERE day >= ? AND day <= ?
		GROUP BY day ORDER BY day DESC`, startDay, endDay).All()
	if err != nil {
		return nil, err
	}
	items := make([]TokenUsageSum, 0, len(rows))
	for _, row := range rows {
		items = append(items, tokenUsageSumFromRow(row))
	}
	return items, nil
}

// TokenUsageByModelSince aggregates recent usage by day and provider/model.
func (s *Store) TokenUsageByModelSince(startDay, endDay string) ([]TokenUsageSum, error) {
	rows, err := s.db.QueryBySql(`SELECT day, provider, model,
		COUNT(*) AS request_count, COUNT(DISTINCT session_id) AS session_count,
		COALESCE(SUM(input), 0) AS input,
		COALESCE(SUM(cache_read), 0) AS cache_read,
		COALESCE(SUM(cache_write), 0) AS cache_write,
		COALESCE(SUM(output), 0) AS output,
		COALESCE(SUM(total), 0) AS total
		FROM tbl_token_usage WHERE day >= ? AND day <= ?
		GROUP BY day, provider, model ORDER BY day DESC, total DESC`, startDay, endDay).All()
	if err != nil {
		return nil, err
	}
	items := make([]TokenUsageSum, 0, len(rows))
	for _, row := range rows {
		items = append(items, tokenUsageSumFromRow(row))
	}
	return items, nil
}

// TokenUsageBySessionSince aggregates usage by day and conversation. Grouping
// includes day so a conversation used across midnight is charged separately.
func (s *Store) TokenUsageBySessionSince(startDay, endDay string) ([]TokenUsageSum, error) {
	rows, err := s.db.QueryBySql(`SELECT u.day, u.session_id,
		COALESCE(s.title, '') AS session_title,
		COALESCE(NULLIF(MAX(s.create_time), 0), MAX(u.create_time)) AS session_create_time,
		MAX(u.agent_id) AS agent_id, COUNT(*) AS request_count,
		COUNT(DISTINCT u.provider || CHAR(0) || u.model) AS model_count,
		COALESCE(SUM(u.input), 0) AS input,
		COALESCE(SUM(u.cache_read), 0) AS cache_read,
		COALESCE(SUM(u.cache_write), 0) AS cache_write,
		COALESCE(SUM(u.output), 0) AS output,
		COALESCE(SUM(u.total), 0) AS total
		FROM tbl_token_usage u LEFT JOIN tbl_session s ON s.id = u.session_id
		WHERE u.day >= ? AND u.day <= ? GROUP BY u.day, u.session_id, s.title
		ORDER BY u.day DESC, total DESC`, startDay, endDay).All()
	if err != nil {
		return nil, err
	}
	items := make([]TokenUsageSum, 0, len(rows))
	for _, row := range rows {
		items = append(items, tokenUsageSumFromRow(row))
	}
	return items, nil
}

// TokenUsageRequestsPage returns a bounded request page ordered newest first.
// BeforeTime/BeforeID form a stable keyset cursor and never use OFFSET.
func (s *Store) TokenUsageRequestsPage(req TokenUsagePageQuery) ([]TokenUsage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT u.id, u.request_key, u.day, u.session_id,
		COALESCE(s.title, '') AS session_title, u.agent_id, u.provider, u.model,
		u.api, u.input, u.cache_read, u.cache_write, u.output, u.total,
		u.stop_reason, u.success, u.create_time
		FROM tbl_token_usage u LEFT JOIN tbl_session s ON s.id = u.session_id
		WHERE u.day >= ? AND u.day <= ?`
	params := []any{req.StartDay, req.EndDay}
	if req.SessionID != nil {
		query += " AND u.session_id = ?"
		params = append(params, *req.SessionID)
	}
	if req.BeforeTime > 0 {
		if req.BeforeID > 0 {
			query += " AND (u.create_time < ? OR (u.create_time = ? AND u.id < ?))"
			params = append(params, req.BeforeTime, req.BeforeTime, req.BeforeID)
		} else {
			query += " AND u.create_time < ?"
			params = append(params, req.BeforeTime)
		}
	}
	query += " ORDER BY u.create_time DESC, u.id DESC LIMIT ?"
	params = append(params, limit)
	rows, err := s.db.QueryBySql(query, params...).All()
	if err != nil {
		return nil, err
	}
	return tokenUsageRows(rows), nil
}

// TokenUsageRequestByPublicID resolves one persisted request directly within
// its calendar-day and session boundary.
func (s *Store) TokenUsageRequestByPublicID(day string, sessionID int64, requestID string) (TokenUsage, bool, error) {
	usageID := int64(-1)
	if strings.HasPrefix(requestID, "usage:") {
		if parsed, err := strconv.ParseInt(strings.TrimPrefix(requestID, "usage:"), 10, 64); err == nil && parsed > 0 {
			usageID = parsed
		}
	}
	row, err := s.db.QueryBySql(`SELECT u.id, u.request_key, u.day, u.session_id,
		COALESCE(s.title, '') AS session_title, u.agent_id, u.provider, u.model,
		u.api, u.input, u.cache_read, u.cache_write, u.output, u.total,
		u.stop_reason, u.success, u.create_time
		FROM tbl_token_usage u LEFT JOIN tbl_session s ON s.id = u.session_id
		WHERE u.day = ? AND u.session_id = ?
		AND (u.request_key = ? OR (u.request_key = '' AND u.id = ?))`,
		day, sessionID, requestID, usageID).One()
	if err != nil {
		return TokenUsage{}, false, err
	}
	if len(row) == 0 {
		return TokenUsage{}, false, nil
	}
	return tokenUsageFromRow(row), true, nil
}

// TokenUsageIdentitiesSince returns only the distinct fields needed to merge
// subagent aggregates without loading every persisted request into memory.
func (s *Store) TokenUsageIdentitiesSince(startDay, endDay string) ([]TokenUsage, error) {
	rows, err := s.db.QueryBySql(`SELECT DISTINCT day, session_id, provider, model
		FROM tbl_token_usage WHERE day >= ? AND day <= ?`, startDay, endDay).All()
	if err != nil {
		return nil, err
	}
	items := make([]TokenUsage, 0, len(rows))
	for _, row := range rows {
		items = append(items, TokenUsage{
			Day: asString(row["day"]), SessionID: asInt(row["session_id"]),
			Provider: asString(row["provider"]), Model: asString(row["model"]),
		})
	}
	return items, nil
}

// TokenUsageTotalsSince returns recent totals inside the retention window.
func (s *Store) TokenUsageTotalsSince(startDay, endDay string) (TokenUsageSum, error) {
	row, err := s.db.QueryBySql(`SELECT COUNT(*) AS request_count,
		COUNT(DISTINCT session_id) AS session_count,
		COUNT(DISTINCT provider || CHAR(0) || model) AS model_count,
		COALESCE(SUM(input), 0) AS input,
		COALESCE(SUM(cache_read), 0) AS cache_read,
		COALESCE(SUM(cache_write), 0) AS cache_write,
		COALESCE(SUM(output), 0) AS output,
		COALESCE(SUM(total), 0) AS total
		FROM tbl_token_usage WHERE day >= ? AND day <= ?`, startDay, endDay).One()
	if err != nil {
		return TokenUsageSum{}, err
	}
	return tokenUsageSumFromRow(row), nil
}

func tokenUsageSumFromRow(row map[string]any) TokenUsageSum {
	return TokenUsageSum{
		Day:              asString(row["day"]),
		SessionID:        asInt(row["session_id"]),
		SessionTitle:     asString(row["session_title"]),
		SessionCreatedAt: asInt(row["session_create_time"]),
		AgentID:          asString(row["agent_id"]),
		Provider:         asString(row["provider"]),
		Model:            asString(row["model"]),
		RequestCount:     asInt(row["request_count"]),
		SessionCount:     asInt(row["session_count"]),
		ModelCount:       asInt(row["model_count"]),
		Input:            asInt(row["input"]),
		Cached:           asInt(row["cache_read"]),
		CacheWrite:       asInt(row["cache_write"]),
		Output:           asInt(row["output"]),
		Total:            asInt(row["total"]),
	}
}

func tokenUsageRows(rows []map[string]any) []TokenUsage {
	items := make([]TokenUsage, 0, len(rows))
	for _, row := range rows {
		items = append(items, tokenUsageFromRow(row))
	}
	return items
}

func tokenUsageFromRow(row map[string]any) TokenUsage {
	return TokenUsage{
		ID:           asInt(row["id"]),
		RequestKey:   asString(row["request_key"]),
		Day:          asString(row["day"]),
		SessionID:    asInt(row["session_id"]),
		SessionTitle: asString(row["session_title"]),
		AgentID:      asString(row["agent_id"]),
		Provider:     asString(row["provider"]),
		Model:        asString(row["model"]),
		API:          asString(row["api"]),
		Input:        asInt(row["input"]),
		CacheRead:    asInt(row["cache_read"]),
		CacheWrite:   asInt(row["cache_write"]),
		Output:       asInt(row["output"]),
		Total:        asInt(row["total"]),
		StopReason:   asString(row["stop_reason"]),
		Success:      asString(row["success"]) != "0",
		CreateTime:   asInt(row["create_time"]),
	}
}
