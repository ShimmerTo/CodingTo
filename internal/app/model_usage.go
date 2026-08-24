package app

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"codingto/internal/applog"
	"codingto/internal/store"
	"codingto/internal/subagentbridge"
)

const (
	modelUsageRetentionDays  = 60
	apiDetailMarkerFile      = "record-api-details.enabled"
	modelTestDetailDir       = "model-tests"
	maxAPIDetailFileSize     = 32 * 1024 * 1024
	modelUsagePageSize       = 20
	modelUsageMaxPageSize    = 20
	modelUsageSourceSubagent = 0
	modelUsageSourceDatabase = 1
)

func syncAPIDetailMarker(configDir string, enabled bool) error {
	marker := filepath.Join(configDir, apiDetailMarkerFile)
	if enabled {
		if err := ensurePrivateDir(configDir); err != nil {
			return err
		}
		return writePrivateFileAtomic(marker, []byte("enabled\n"))
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// cleanupModelUsageRetention enforces the fixed 60-calendar-day boundary for
// both SQLite counters and opt-in JSON detail files. File deletion is limited
// to direct children of a validated s<id>/api directory with a valid date
// prefix; it never recursively removes a conversation directory.
func (a *App) cleanupModelUsageRetention() {
	now := time.Now()
	cutoff := now.AddDate(0, 0, -(modelUsageRetentionDays - 1))
	cutoffDay := cutoff.Format("2006-01-02")
	if deleted, err := a.store.Store().DeleteTokenUsageBefore(cutoffDay); err != nil {
		applog.Errorf("model usage retention: delete database rows: %v", err)
	} else if deleted > 0 {
		applog.Infof("model usage retention: deleted %d row(s) before %s", deleted, cutoffDay)
	}
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		applog.Errorf("model usage retention: list sessions: %v", err)
		return
	}
	for _, session := range sessions {
		sessionDir := filepath.Clean(session.SessionDir)
		if sessionDir == "." || filepath.Base(sessionDir) != "s"+strconv.FormatInt(session.ID, 10) {
			continue
		}
		apiDir := filepath.Join(sessionDir, "api")
		entries, readErr := os.ReadDir(apiDir)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				applog.Errorf("model usage retention: read session %d api directory: %v", session.ID, readErr)
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(entry.Name()) < len("2006-01-02") || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			fileDay := entry.Name()[:len("2006-01-02")]
			if _, parseErr := time.ParseInLocation("2006-01-02", fileDay, time.Local); parseErr != nil || fileDay >= cutoffDay {
				continue
			}
			if removeErr := os.Remove(filepath.Join(apiDir, entry.Name())); removeErr != nil {
				applog.Errorf("model usage retention: delete session %d detail %s: %v", session.ID, entry.Name(), removeErr)
			}
		}
	}
	cleanupAPIDetailDir(filepath.Join(a.store.Dir(), modelTestDetailDir, "api"), cutoffDay, 0)
}

func cleanupAPIDetailDir(apiDir, cutoffDay string, sessionID int64) {
	entries, err := os.ReadDir(apiDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			applog.Errorf("model usage retention: read detail directory for session %d: %v", sessionID, err)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(entry.Name()) < len("2006-01-02") || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fileDay := entry.Name()[:len("2006-01-02")]
		if _, parseErr := time.ParseInLocation("2006-01-02", fileDay, time.Local); parseErr != nil || fileDay >= cutoffDay {
			continue
		}
		if removeErr := os.Remove(filepath.Join(apiDir, entry.Name())); removeErr != nil {
			applog.Errorf("model usage retention: delete detail for session %d: %v", sessionID, removeErr)
		}
	}
}

// usageRecord is one assistant-message usage sample read from a Pi session file.
// It is used by readSessionTokenStats for the per-conversation detail view; the
// model-page statistics rely on the persisted tbl_token_usage rows instead.
type usageRecord struct {
	Timestamp  int64
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
	Total      int64
}

// ModelUsagePoint is one aggregated bucket: either a calendar day or a session.
// Provider/Model/Agent are populated for the per-session bucket.
type ModelUsagePoint struct {
	Label            string `json:"label"`
	Day              string `json:"day,omitempty"`
	SessionID        int64  `json:"sessionId,omitempty"`
	SessionCreatedAt int64  `json:"sessionCreatedAt,omitempty"`
	ModelTest        bool   `json:"modelTest,omitempty"`
	AgentID          string `json:"agentId,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	API              string `json:"api,omitempty"`
	RequestID        string `json:"requestId,omitempty"`
	RequestTime      int64  `json:"requestTime,omitempty"`
	RequestCount     int64  `json:"requestCount,omitempty"`
	SessionCount     int64  `json:"sessionCount,omitempty"`
	ModelCount       int64  `json:"modelCount,omitempty"`
	StopReason       string `json:"stopReason,omitempty"`
	Success          bool   `json:"success"`
	Synthetic        bool   `json:"synthetic,omitempty"`
	Input            int64  `json:"input"`
	Cached           int64  `json:"cached"`
	CacheWrite       int64  `json:"cacheWrite"`
	Output           int64  `json:"output"`
	Total            int64  `json:"total"`
	usageID          int64
	cursorSource     int
	cursorKey        string
}

// ModelUsageQuery selects one statistics dimension and an explicit local
// calendar-date range within the fixed 60-day retention window.
type ModelUsageQuery struct {
	Dimension string `json:"dimension"`
	StartDay  string `json:"startDay"`
	EndDay    string `json:"endDay"`
	Cursor    string `json:"cursor,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

// ModelUsageQueryResult is the model-page response for one dimension.
type ModelUsageQueryResult struct {
	Dimension     string            `json:"dimension"`
	Days          int               `json:"days"`
	RetentionDays int               `json:"retentionDays"`
	StartDay      string            `json:"startDay"`
	EndDay        string            `json:"endDay"`
	ByDay         []ModelUsagePoint `json:"byDay"`
	Rows          []ModelUsagePoint `json:"rows"`
	Totals        ModelUsagePoint   `json:"totals"`
	HasMore       bool              `json:"hasMore"`
	NextCursor    string            `json:"nextCursor,omitempty"`
}

// ModelUsageSessionRequestQuery selects one cursor page of requests charged
// to a session on one local calendar day. SessionID zero is the model-test
// bucket.
type ModelUsageSessionRequestQuery struct {
	Day       string `json:"day"`
	SessionID int64  `json:"sessionId"`
	Cursor    string `json:"cursor,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

// ModelUsageRequestPage is one bounded page returned to the statistics UI.
type ModelUsageRequestPage struct {
	Items      []ModelUsagePoint `json:"items"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type modelUsageRequestCursor struct {
	Time   int64  `json:"time"`
	Source int    `json:"source"`
	ID     int64  `json:"id,omitempty"`
	Key    string `json:"key,omitempty"`
}

type modelUsageSessionCursor struct {
	CreatedAt int64  `json:"createdAt"`
	SessionID int64  `json:"sessionId"`
	Day       string `json:"day"`
}

// ModelUsageRequestDetail is the opt-in full provider payload and parsed
// response associated with one persisted request counter.
type ModelUsageRequestDetail struct {
	Available   bool   `json:"available"`
	RequestID   string `json:"requestId,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Request     string `json:"request,omitempty"`
	Response    string `json:"response,omitempty"`
}

// QueryModelUsageStats returns daily statistics for the model, session, or
// individual-request dimension. Missing dates default to today; explicit
// ranges must stay inside the latest 60 local calendar days.
func (a *App) QueryModelUsageStats(req ModelUsageQuery) (ModelUsageQueryResult, error) {
	dimension := strings.TrimSpace(req.Dimension)
	if dimension != "model" && dimension != "session" && dimension != "request" {
		dimension = "model"
	}
	startDay, endDay, days, err := normalizeModelUsageRange(req.StartDay, req.EndDay, time.Now())
	if err != nil {
		return ModelUsageQueryResult{}, err
	}

	dayRows, err := a.store.Store().TokenUsageByDaySince(startDay, endDay)
	if err != nil {
		applog.Errorf("query model usage daily totals: %v", err)
		return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
	}
	totalsRow, err := a.store.Store().TokenUsageTotalsSince(startDay, endDay)
	if err != nil {
		applog.Errorf("query model usage totals: %v", err)
		return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
	}
	result := ModelUsageQueryResult{
		Dimension: dimension, Days: days, RetentionDays: modelUsageRetentionDays,
		StartDay: startDay, EndDay: endDay,
		ByDay: make([]ModelUsagePoint, 0, len(dayRows)), Rows: []ModelUsagePoint{},
		Totals: usagePointFromSum(totalsRow),
	}
	for _, row := range dayRows {
		point := usagePointFromSum(row)
		point.Label, point.Day = row.Day, row.Day
		result.ByDay = append(result.ByDay, point)
	}

	switch dimension {
	case "model":
		rows, queryErr := a.store.Store().TokenUsageByModelSince(startDay, endDay)
		if queryErr != nil {
			applog.Errorf("query model usage by model: %v", queryErr)
			return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
		}
		for _, row := range rows {
			point := usagePointFromSum(row)
			point.Day, point.Provider, point.Model = row.Day, row.Provider, row.Model
			point.Label = strings.Trim(strings.TrimSpace(row.Provider)+" / "+strings.TrimSpace(row.Model), " / ")
			result.Rows = append(result.Rows, point)
		}
	case "session":
		rows, queryErr := a.store.Store().TokenUsageBySessionSince(startDay, endDay)
		if queryErr != nil {
			applog.Errorf("query model usage by session: %v", queryErr)
			return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
		}
		for _, row := range rows {
			point := usagePointFromSum(row)
			point.Day, point.SessionID, point.AgentID = row.Day, row.SessionID, row.AgentID
			point.SessionCreatedAt = row.SessionCreatedAt
			point.ModelTest = row.SessionID == 0
			point.Label = modelUsageSessionLabel(row.SessionTitle, row.SessionID)
			result.Rows = append(result.Rows, point)
		}
	}

	subagentSamples := a.collectSubagentUsageSamples()
	var databaseRequests []store.TokenUsage
	if len(subagentSamples) > 0 {
		databaseRequests, err = a.store.Store().TokenUsageIdentitiesSince(startDay, endDay)
		if err != nil {
			applog.Errorf("query model usage request identities: %v", err)
			return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
		}
	}
	mergeSubagentQueryUsage(&result, subagentSamples, databaseRequests, startDay, endDay, dimension != "request")
	if dimension == "request" {
		page, pageErr := a.modelUsageRequestsPage(startDay, endDay, nil, req.Cursor, req.PageSize, subagentSamples)
		if pageErr != nil {
			applog.Errorf("query model usage request page: %v", pageErr)
			return ModelUsageQueryResult{}, errors.New("无法读取模型用量统计，请稍后重试")
		}
		result.Rows, result.HasMore, result.NextCursor = page.Items, page.HasMore, page.NextCursor
	}
	sortModelUsageQuery(&result)
	if dimension == "session" {
		if pageErr := paginateModelUsageSessions(&result, req.Cursor, req.PageSize); pageErr != nil {
			return ModelUsageQueryResult{}, errors.New("invalid session page cursor")
		}
	}
	return result, nil
}

func normalizeModelUsageRange(requestedStart, requestedEnd string, now time.Time) (string, string, int, error) {
	localNow := now.In(time.Local)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)
	startText := strings.TrimSpace(requestedStart)
	endText := strings.TrimSpace(requestedEnd)
	if startText == "" && endText == "" {
		day := today.Format("2006-01-02")
		return day, day, 1, nil
	}
	if startText == "" || endText == "" {
		return "", "", 0, errors.New("请选择完整的统计日期范围")
	}
	start, startErr := time.ParseInLocation("2006-01-02", startText, time.Local)
	end, endErr := time.ParseInLocation("2006-01-02", endText, time.Local)
	if startErr != nil || endErr != nil {
		return "", "", 0, errors.New("统计日期格式无效")
	}
	if start.After(end) {
		return "", "", 0, errors.New("开始日期不能晚于结束日期")
	}
	cutoff := today.AddDate(0, 0, -(modelUsageRetentionDays - 1))
	if start.Before(cutoff) || end.After(today) {
		return "", "", 0, errors.New("统计日期仅支持最近 60 个自然日，且不能晚于今天")
	}
	startUTC := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	days := int(endUTC.Sub(startUTC)/(24*time.Hour)) + 1
	return startText, endText, days, nil
}

// GetModelUsageSessionRequests returns one bounded request page charged to one
// conversation on one local calendar day. Session ID zero is the explicit
// model-test bucket; negative IDs remain invalid.
func (a *App) GetModelUsageSessionRequests(req ModelUsageSessionRequestQuery) (ModelUsageRequestPage, error) {
	day := strings.TrimSpace(req.Day)
	parsed, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return ModelUsageRequestPage{}, errors.New("invalid usage date")
	}
	if req.SessionID < 0 {
		return ModelUsageRequestPage{}, errors.New("session id is required")
	}
	cutoff := time.Now().AddDate(0, 0, -(modelUsageRetentionDays - 1))
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	if parsed.Before(cutoffDay) || parsed.After(time.Now()) {
		return ModelUsageRequestPage{}, errors.New("usage date is outside the retention window")
	}
	sessionID := req.SessionID
	return a.modelUsageRequestsPage(day, day, &sessionID, req.Cursor, req.PageSize, a.collectSubagentUsageSamples())
}

func (a *App) modelUsageRequestsPage(startDay, endDay string, sessionID *int64, encodedCursor string, requestedSize int, subagentSamples []ModelUsagePoint) (ModelUsageRequestPage, error) {
	pageSize := normalizeModelUsagePageSize(requestedSize)
	cursor, hasCursor, err := decodeModelUsageRequestCursor(encodedCursor)
	if err != nil {
		return ModelUsageRequestPage{}, errors.New("invalid request page cursor")
	}
	storeQuery := store.TokenUsagePageQuery{
		StartDay: startDay, EndDay: endDay, SessionID: sessionID, Limit: pageSize + 1,
	}
	if hasCursor {
		storeQuery.BeforeTime = cursor.Time
		if cursor.Source == modelUsageSourceDatabase {
			storeQuery.BeforeID = cursor.ID
		}
	}
	rows, err := a.store.Store().TokenUsageRequestsPage(storeQuery)
	if err != nil {
		return ModelUsageRequestPage{}, err
	}
	combined := make([]ModelUsagePoint, 0, len(rows)+len(subagentSamples))
	for _, row := range rows {
		combined = append(combined, usageRequestPoint(row))
	}
	for _, sample := range subagentSamples {
		if sample.Day < startDay || sample.Day > endDay {
			continue
		}
		if sessionID != nil && sample.SessionID != *sessionID {
			continue
		}
		if hasCursor && !modelUsageRequestFollowsCursor(sample, cursor) {
			continue
		}
		combined = append(combined, sample)
	}
	sort.Slice(combined, func(i, j int) bool { return modelUsageRequestBefore(combined[i], combined[j]) })
	page := ModelUsageRequestPage{Items: combined}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		page.HasMore = true
		page.NextCursor = encodeModelUsageRequestCursor(modelUsageCursorForPoint(page.Items[len(page.Items)-1]))
	}
	return page, nil
}

func normalizeModelUsagePageSize(requestedSize int) int {
	if requestedSize <= 0 {
		return modelUsagePageSize
	}
	if requestedSize > modelUsageMaxPageSize {
		return modelUsageMaxPageSize
	}
	return requestedSize
}

func paginateModelUsageSessions(result *ModelUsageQueryResult, encodedCursor string, requestedSize int) error {
	cursor, hasCursor, err := decodeModelUsageSessionCursor(encodedCursor)
	if err != nil {
		return err
	}
	if hasCursor {
		start := 0
		for start < len(result.Rows) && !modelUsageSessionFollowsCursor(result.Rows[start], cursor) {
			start++
		}
		result.Rows = result.Rows[start:]
	}
	pageSize := normalizeModelUsagePageSize(requestedSize)
	if len(result.Rows) <= pageSize {
		return nil
	}
	result.Rows = result.Rows[:pageSize]
	result.HasMore = true
	last := result.Rows[len(result.Rows)-1]
	result.NextCursor = encodeModelUsageSessionCursor(modelUsageSessionCursor{
		CreatedAt: last.SessionCreatedAt,
		SessionID: last.SessionID,
		Day:       last.Day,
	})
	return nil
}

func decodeModelUsageSessionCursor(value string) (modelUsageSessionCursor, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return modelUsageSessionCursor{}, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return modelUsageSessionCursor{}, false, err
	}
	var cursor modelUsageSessionCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return modelUsageSessionCursor{}, false, err
	}
	if cursor.CreatedAt < 0 || cursor.SessionID < 0 || strings.TrimSpace(cursor.Day) == "" {
		return modelUsageSessionCursor{}, false, errors.New("invalid cursor fields")
	}
	return cursor, true, nil
}

func encodeModelUsageSessionCursor(cursor modelUsageSessionCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func modelUsageSessionFollowsCursor(point ModelUsagePoint, cursor modelUsageSessionCursor) bool {
	if point.SessionCreatedAt != cursor.CreatedAt {
		return point.SessionCreatedAt < cursor.CreatedAt
	}
	if point.SessionID != cursor.SessionID {
		return point.SessionID < cursor.SessionID
	}
	return point.Day < cursor.Day
}

func decodeModelUsageRequestCursor(value string) (modelUsageRequestCursor, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return modelUsageRequestCursor{}, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return modelUsageRequestCursor{}, false, err
	}
	var cursor modelUsageRequestCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return modelUsageRequestCursor{}, false, err
	}
	if cursor.Time <= 0 || (cursor.Source != modelUsageSourceDatabase && cursor.Source != modelUsageSourceSubagent) {
		return modelUsageRequestCursor{}, false, errors.New("invalid cursor fields")
	}
	if cursor.Source == modelUsageSourceDatabase && cursor.ID <= 0 {
		return modelUsageRequestCursor{}, false, errors.New("invalid database cursor")
	}
	if cursor.Source == modelUsageSourceSubagent && cursor.Key == "" {
		return modelUsageRequestCursor{}, false, errors.New("invalid subagent cursor")
	}
	return cursor, true, nil
}

func encodeModelUsageRequestCursor(cursor modelUsageRequestCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func modelUsageCursorForPoint(point ModelUsagePoint) modelUsageRequestCursor {
	cursor := modelUsageRequestCursor{Time: point.RequestTime, Source: point.cursorSource}
	if point.cursorSource == modelUsageSourceDatabase {
		cursor.ID = point.usageID
	} else {
		cursor.Key = point.cursorKey
	}
	return cursor
}

func modelUsageRequestFollowsCursor(point ModelUsagePoint, cursor modelUsageRequestCursor) bool {
	if point.RequestTime != cursor.Time {
		return point.RequestTime < cursor.Time
	}
	if point.cursorSource != cursor.Source {
		return point.cursorSource < cursor.Source
	}
	if point.cursorSource == modelUsageSourceDatabase {
		return point.usageID < cursor.ID
	}
	return point.cursorKey < cursor.Key
}

func modelUsageRequestBefore(left, right ModelUsagePoint) bool {
	if left.RequestTime != right.RequestTime {
		return left.RequestTime > right.RequestTime
	}
	if left.cursorSource != right.cursorSource {
		return left.cursorSource > right.cursorSource
	}
	if left.cursorSource == modelUsageSourceDatabase {
		return left.usageID > right.usageID
	}
	return left.cursorKey > right.cursorKey
}

// GetModelUsageRequestDetail returns the recorded provider payload and result
// for one request. New detail files are addressed directly by a response-ID
// digest; legacy files are scanned only inside the validated session api dir.
func (a *App) GetModelUsageRequestDetail(day string, sessionID int64, requestID string) (ModelUsageRequestDetail, error) {
	day = strings.TrimSpace(day)
	requestID = strings.TrimSpace(requestID)
	if _, err := time.ParseInLocation("2006-01-02", day, time.Local); err != nil {
		return ModelUsageRequestDetail{}, errors.New("请求日期无效")
	}
	if sessionID < 0 || requestID == "" {
		return ModelUsageRequestDetail{}, errors.New("请求标识无效")
	}

	row, found, err := a.store.Store().TokenUsageRequestByPublicID(day, sessionID, requestID)
	if err != nil {
		applog.Errorf("query API detail request for session %d: %v", sessionID, err)
		return ModelUsageRequestDetail{}, errors.New("无法读取请求记录")
	}
	expectedTime := int64(0)
	expectedResponseID := ""
	if found {
		expectedTime = row.CreateTime
		expectedResponseID = responseIDFromUsageKey(row.RequestKey)
	} else {
		for _, sample := range a.collectSubagentUsageSamples() {
			if sample.Day == day && sample.SessionID == sessionID && sample.RequestID == requestID && !sample.Synthetic {
				expectedTime = sample.RequestTime
				expectedResponseID = responseIDFromSubagentRequestID(sample.RequestID)
				break
			}
		}
	}
	if expectedTime <= 0 {
		return ModelUsageRequestDetail{}, errors.New("未找到对应的请求记录")
	}

	apiDir, err := a.modelUsageAPIDetailDir(sessionID)
	if err != nil {
		return ModelUsageRequestDetail{}, err
	}
	if expectedResponseID != "" {
		name := day + "_response_" + apiDetailResponseKey(expectedResponseID) + ".json"
		detail, responseID, readErr := readModelUsageDetailFile(filepath.Join(apiDir, name), name)
		if readErr == nil && responseID == expectedResponseID {
			detail.RequestID = requestID
			return detail, nil
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			applog.Errorf("read direct API detail file for session %d: %v", sessionID, readErr)
		}
	}
	entries, err := os.ReadDir(apiDir)
	if errors.Is(err, os.ErrNotExist) {
		return ModelUsageRequestDetail{RequestID: requestID}, nil
	}
	if err != nil {
		applog.Errorf("read API detail directory for session %d: %v", sessionID, err)
		return ModelUsageRequestDetail{}, errors.New("无法读取请求明细")
	}

	var nearest *ModelUsageRequestDetail
	var nearestDistance int64 = 30_001
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, day+"_") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		detail, responseID, readErr := readModelUsageDetailFile(filepath.Join(apiDir, name), name)
		if readErr != nil {
			applog.Errorf("read API detail file for session %d: %v", sessionID, readErr)
			continue
		}
		detail.RequestID = requestID
		if expectedResponseID != "" && responseID == expectedResponseID {
			return detail, nil
		}
		distance := detail.CompletedAt - expectedTime
		if distance < 0 {
			distance = -distance
		}
		if distance < nearestDistance {
			copy := detail
			nearest = &copy
			nearestDistance = distance
		}
	}
	if nearest != nil {
		return *nearest, nil
	}
	return ModelUsageRequestDetail{RequestID: requestID}, nil
}

func apiDetailResponseKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func (a *App) modelUsageAPIDetailDir(sessionID int64) (string, error) {
	if sessionID == 0 {
		return filepath.Join(a.store.Dir(), modelTestDetailDir, "api"), nil
	}
	session, ok, err := a.store.Store().SessionByID(sessionID)
	if err != nil {
		applog.Errorf("resolve API detail session %d: %v", sessionID, err)
		return "", errors.New("无法读取会话信息")
	}
	if !ok {
		return "", errors.New("会话不存在")
	}
	sessionDir := filepath.Clean(session.SessionDir)
	if sessionDir == "." || filepath.Base(sessionDir) != "s"+strconv.FormatInt(sessionID, 10) {
		return "", errors.New("会话目录无效")
	}
	return filepath.Join(sessionDir, "api"), nil
}

func responseIDFromUsageKey(requestKey string) string {
	const marker = ":response:"
	index := strings.Index(requestKey, marker)
	if index < 0 {
		return ""
	}
	return requestKey[index+len(marker):]
}

func responseIDFromSubagentRequestID(requestID string) string {
	const prefix = "subagent:"
	if !strings.HasPrefix(requestID, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(requestID, prefix)
	index := strings.Index(rest, ":")
	if index < 0 {
		return ""
	}
	return rest[index+1:]
}

func readModelUsageDetailFile(path, name string) (ModelUsageRequestDetail, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return ModelUsageRequestDetail{}, "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ModelUsageRequestDetail{}, "", err
	}
	if info.Size() > maxAPIDetailFileSize {
		return ModelUsageRequestDetail{}, "", errors.New("request detail file exceeds size limit")
	}
	var stored struct {
		RequestID   string          `json:"requestId"`
		StartedAt   int64           `json:"startedAt"`
		CompletedAt int64           `json:"completedAt"`
		Request     json.RawMessage `json:"request"`
		Response    json.RawMessage `json:"response"`
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&stored); err != nil {
		return ModelUsageRequestDetail{}, "", err
	}
	var response struct {
		Result struct {
			ResponseID string `json:"responseId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(stored.Response, &response)
	return ModelUsageRequestDetail{
		Available: true, FileName: name, StartedAt: stored.StartedAt, CompletedAt: stored.CompletedAt,
		Request: prettyJSON(stored.Request), Response: prettyJSON(stored.Response),
	}, response.Result.ResponseID, nil
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var output bytes.Buffer
	if err := json.Indent(&output, raw, "", "  "); err != nil {
		return string(raw)
	}
	return output.String()
}

func usagePointFromSum(row store.TokenUsageSum) ModelUsagePoint {
	return ModelUsagePoint{
		RequestCount: row.RequestCount, SessionCount: row.SessionCount, ModelCount: row.ModelCount,
		Input: row.Input, Cached: row.Cached, CacheWrite: row.CacheWrite,
		Output: row.Output, Total: row.Total, Success: true,
	}
}

func usageRequestPoint(row store.TokenUsage) ModelUsagePoint {
	requestID := row.RequestKey
	if requestID == "" {
		requestID = "usage:" + strconv.FormatInt(row.ID, 10)
	}
	return ModelUsagePoint{
		Label: modelUsageSessionLabel(row.SessionTitle, row.SessionID),
		Day:   row.Day, SessionID: row.SessionID, ModelTest: row.SessionID == 0, AgentID: row.AgentID,
		Provider: row.Provider, Model: row.Model, API: row.API,
		RequestID: requestID, RequestTime: row.CreateTime, RequestCount: 1,
		StopReason: row.StopReason, Success: row.Success,
		Input: row.Input, Cached: row.CacheRead, CacheWrite: row.CacheWrite,
		Output: row.Output, Total: row.Total,
		usageID: row.ID, cursorSource: modelUsageSourceDatabase,
	}
}

func modelUsageSessionLabel(title string, sessionID int64) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	if sessionID == 0 {
		return ""
	}
	return sessionUsageLabel("", sessionID)
}

// sessionUsageTitles returns session id -> title for the per-session label.
func (a *App) sessionUsageTitles() map[int64]string {
	titles := map[int64]string{}
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		return titles
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.Title) != "" {
			titles[session.ID] = session.Title
		}
	}
	return titles
}

func sessionUsageLabel(title string, id int64) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	return "会话 " + strconv.FormatInt(id, 10)
}

// recordTokenUsage persists one model request's token consumption into
// tbl_token_usage for day/session aggregation. It is called at the request
// return path; failures are logged but never block event dispatch.
func (s *AgentService) recordTokenUsage(sessionID int64, event map[string]any, recordedAt int64) {
	item, ok := s.tokenUsageItem(sessionID, event, recordedAt)
	if !ok {
		return
	}
	if _, err := s.store.Store().RecordTokenUsage(item); err != nil {
		applog.Errorf("[session %d] record token usage: %v", sessionID, err)
	}
}

func (s *AgentService) tokenUsageItem(sessionID int64, event map[string]any, recordedAt int64) (store.TokenUsage, bool) {
	message := mapValue(event["message"])
	if stringValue(message["role"]) != "assistant" {
		return store.TokenUsage{}, false
	}
	usage := mapValue(message["usage"])
	if len(usage) == 0 {
		return store.TokenUsage{}, false
	}
	if recordedAt <= 0 {
		recordedAt = time.Now().UnixMilli()
	}
	input := intValue(usage["input"])
	cached := intValue(usage["cacheRead"])
	cacheWrite := intValue(usage["cacheWrite"])
	output := intValue(usage["output"])
	total := intValue(usage["totalTokens"])
	agentID := ""
	provider := strings.TrimSpace(stringValue(message["provider"]))
	model := strings.TrimSpace(stringValue(message["model"]))
	if session, ok, err := s.store.Store().SessionByID(sessionID); err == nil && ok {
		agentID = session.AgentID
		if provider == "" {
			provider = session.Provider
		}
		if model == "" {
			model = session.Model
		}
	}
	requestKey := tokenUsageRequestKey(sessionID, recordedAt, message)
	stopReason := stringValue(message["stopReason"])
	success := strings.TrimSpace(stringValue(message["errorMessage"])) == "" && stopReason != "error" && stopReason != "aborted"
	return store.TokenUsage{
		RequestKey: requestKey,
		Day:        time.UnixMilli(recordedAt).Format("2006-01-02"),
		SessionID:  sessionID,
		AgentID:    agentID,
		Provider:   provider,
		Model:      model,
		API:        stringValue(message["api"]),
		Input:      input,
		CacheRead:  cached,
		CacheWrite: cacheWrite,
		Output:     output,
		Total:      total,
		StopReason: stopReason,
		Success:    success,
		CreateTime: recordedAt,
	}, true
}

func tokenUsageRequestKey(sessionID, recordedAt int64, message map[string]any) string {
	if responseID := strings.TrimSpace(stringValue(message["responseId"])); responseID != "" {
		return "session:" + strconv.FormatInt(sessionID, 10) + ":response:" + responseID
	}
	return "session:" + strconv.FormatInt(sessionID, 10) + ":event:" + strconv.FormatInt(recordedAt, 10)
}

func (s *AgentService) recordModelTestUsage(req TestModelRequest, message map[string]any, recordedAt int64) {
	if len(message) == 0 {
		return
	}
	clone := make(map[string]any, len(message)+2)
	for k, v := range message {
		clone[k] = v
	}
	clone["provider"] = req.Provider
	clone["model"] = req.Model
	event := map[string]any{"message": clone}
	s.recordTokenUsage(0, event, recordedAt)
}

// collectSessionUsage iterates the Pi session JSONL files in a session
// directory (excluding CodingTo's own event log and subdirectories) and
// returns one record per assistant message that carries a usage payload.
func collectSessionUsage(sessionDir string) []usageRecord {
	var records []usageRecord
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return records
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if entry.Name() == sessionEventFile {
			continue
		}
		file, err := os.Open(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if stringValue(event["type"]) != "message" {
				continue
			}
			msg := mapValue(event["message"])
			if stringValue(msg["role"]) != "assistant" {
				continue
			}
			usage := mapValue(msg["usage"])
			if len(usage) == 0 {
				continue
			}
			records = append(records, usageRecord{
				Timestamp:  parsePiEventTime(stringValue(event["timestamp"])),
				Input:      intValue(usage["input"]),
				Cached:     intValue(usage["cacheRead"]),
				CacheWrite: intValue(usage["cacheWrite"]),
				Output:     intValue(usage["output"]),
				Total:      intValue(usage["totalTokens"]),
			})
		}
		_ = file.Close()
	}
	return records
}

type subagentUsageBucket struct {
	RunID            string
	SessionID        int64
	AgentID          string
	Provider         string
	Model            string
	Day              string
	Input            int64
	Cached           int64
	CacheWrite       int64
	Output           int64
	Total            int64
	EndedAt          int64
	SessionCreatedAt int64
	Requests         []subagentbridge.TokenUsageRequest
}

// collectSubagentUsage scans every session's subagents directory for terminal
// run records that carry cumulative token usage, attributing it to the parent
// session and the run's ending day. Only the small run.json files are read, not
// the full session logs.
func (a *App) collectSubagentUsage() []subagentUsageBucket {
	var buckets []subagentUsageBucket
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		return buckets
	}
	for _, session := range sessions {
		if session.SessionDir == "" || filepath.Base(filepath.Clean(session.SessionDir)) != "s"+strconv.FormatInt(session.ID, 10) {
			continue
		}
		root := filepath.Join(session.SessionDir, "subagents")
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			record, err := subagentbridge.ReadRunRecord(filepath.Join(root, entry.Name(), "run.json"))
			if err != nil || record.Status == "running" {
				continue
			}
			if record.TokenStats == nil || record.TokenStats.Total == 0 {
				continue
			}
			day := record.Day()
			day = strings.TrimSpace(day)
			if day == "" {
				day = time.Now().Format("2006-01-02")
			}
			buckets = append(buckets, subagentUsageBucket{
				RunID:            record.RunID,
				SessionID:        session.ID,
				AgentID:          session.AgentID,
				Provider:         record.Provider,
				Model:            record.Model,
				Day:              day,
				Input:            record.TokenStats.Input,
				Cached:           record.TokenStats.Cached,
				CacheWrite:       record.TokenStats.CacheWrite,
				Output:           record.TokenStats.Output,
				Total:            record.TokenStats.Total,
				EndedAt:          record.EndedAt,
				SessionCreatedAt: session.CreateTime,
				Requests:         record.TokenRequests,
			})
		}
	}
	return buckets
}

func (a *App) collectSubagentUsageSamples() []ModelUsagePoint {
	titles := a.sessionUsageTitles()
	result := []ModelUsagePoint{}
	for _, bucket := range a.collectSubagentUsage() {
		if len(bucket.Requests) == 0 {
			requestID := "subagent-run:" + bucket.RunID
			result = append(result, ModelUsagePoint{
				Label: modelUsageSessionLabel(titles[bucket.SessionID], bucket.SessionID),
				Day:   bucket.Day, SessionID: bucket.SessionID, AgentID: bucket.AgentID,
				SessionCreatedAt: bucket.SessionCreatedAt,
				Provider:         bucket.Provider, Model: bucket.Model,
				RequestID: requestID, RequestTime: bucket.EndedAt,
				RequestCount: 1, Synthetic: true, Success: true,
				Input: bucket.Input, Cached: bucket.Cached, CacheWrite: bucket.CacheWrite,
				Output: bucket.Output, Total: bucket.Total,
				cursorSource: modelUsageSourceSubagent, cursorKey: requestID,
			})
			continue
		}
		for index, request := range bucket.Requests {
			requestID := request.RequestKey
			if requestID == "" {
				requestID = strconv.Itoa(index + 1)
			}
			provider, model := request.Provider, request.Model
			if provider == "" {
				provider = bucket.Provider
			}
			if model == "" {
				model = bucket.Model
			}
			publicRequestID := "subagent:" + bucket.RunID + ":" + requestID
			result = append(result, ModelUsagePoint{
				Label:     modelUsageSessionLabel(titles[bucket.SessionID], bucket.SessionID),
				Day:       time.UnixMilli(request.Timestamp).Format("2006-01-02"),
				SessionID: bucket.SessionID, AgentID: bucket.AgentID,
				SessionCreatedAt: bucket.SessionCreatedAt,
				Provider:         provider, Model: model, API: request.API,
				RequestID:   publicRequestID,
				RequestTime: request.Timestamp, RequestCount: 1,
				StopReason: request.StopReason, Success: request.Success,
				Input: request.Input, Cached: request.Cached, CacheWrite: request.CacheWrite,
				Output: request.Output, Total: request.Total,
				cursorSource: modelUsageSourceSubagent, cursorKey: publicRequestID,
			})
		}
	}
	return result
}

func mergeSubagentQueryUsage(result *ModelUsageQueryResult, samples []ModelUsagePoint, databaseRequests []store.TokenUsage, startDay, endDay string, appendRequestRows bool) {
	byDay := map[string]int{}
	for index, point := range result.ByDay {
		byDay[point.Day] = index
	}
	modelRows := map[string]int{}
	sessionRows := map[string]int{}
	totalSessions := map[string]struct{}{}
	totalModels := map[string]struct{}{}
	daySessions := map[string]struct{}{}
	dayModels := map[string]struct{}{}
	modelSessions := map[string]struct{}{}
	sessionModels := map[string]struct{}{}
	remember := func(day string, sessionID int64, provider, model string) {
		session := strconv.FormatInt(sessionID, 10)
		modelKey := provider + "\x00" + model
		totalSessions[session] = struct{}{}
		totalModels[modelKey] = struct{}{}
		daySessions[day+"\x00"+session] = struct{}{}
		dayModels[day+"\x00"+modelKey] = struct{}{}
		modelSessions[day+"\x00"+modelKey+"\x00"+session] = struct{}{}
		sessionModels[day+"\x00"+session+"\x00"+modelKey] = struct{}{}
	}
	for _, request := range databaseRequests {
		remember(request.Day, request.SessionID, request.Provider, request.Model)
	}
	for index, point := range result.Rows {
		if result.Dimension == "model" {
			modelRows[point.Day+"\x00"+point.Provider+"\x00"+point.Model] = index
		} else if result.Dimension == "session" {
			sessionRows[point.Day+"\x00"+strconv.FormatInt(point.SessionID, 10)] = index
		}
	}
	for _, sample := range samples {
		if sample.Day < startDay || sample.Day > endDay {
			continue
		}
		addUsagePoint(&result.Totals, sample)
		result.Totals.RequestCount++
		session := strconv.FormatInt(sample.SessionID, 10)
		modelKey := sample.Provider + "\x00" + sample.Model
		if _, exists := totalSessions[session]; !exists {
			result.Totals.SessionCount++
		}
		if _, exists := totalModels[modelKey]; !exists {
			result.Totals.ModelCount++
		}
		if index, ok := byDay[sample.Day]; ok {
			addUsagePoint(&result.ByDay[index], sample)
			result.ByDay[index].RequestCount++
			if _, exists := daySessions[sample.Day+"\x00"+session]; !exists {
				result.ByDay[index].SessionCount++
			}
			if _, exists := dayModels[sample.Day+"\x00"+modelKey]; !exists {
				result.ByDay[index].ModelCount++
			}
		} else {
			point := sample
			point.Label, point.RequestCount = sample.Day, 1
			point.SessionID, point.AgentID, point.Provider, point.Model = 0, "", "", ""
			point.SessionCount, point.ModelCount = 1, 1
			result.ByDay = append(result.ByDay, point)
			byDay[sample.Day] = len(result.ByDay) - 1
		}
		switch result.Dimension {
		case "request":
			if appendRequestRows {
				result.Rows = append(result.Rows, sample)
			}
		case "model":
			key := sample.Day + "\x00" + sample.Provider + "\x00" + sample.Model
			if index, ok := modelRows[key]; ok {
				addUsagePoint(&result.Rows[index], sample)
				result.Rows[index].RequestCount++
				if _, exists := modelSessions[key+"\x00"+session]; !exists {
					result.Rows[index].SessionCount++
				}
			} else {
				point := sample
				point.Label = strings.Trim(strings.TrimSpace(sample.Provider)+" / "+strings.TrimSpace(sample.Model), " / ")
				point.SessionID, point.RequestID, point.RequestTime = 0, "", 0
				point.RequestCount, point.SessionCount = 1, 1
				result.Rows = append(result.Rows, point)
				modelRows[key] = len(result.Rows) - 1
			}
		case "session":
			key := sample.Day + "\x00" + strconv.FormatInt(sample.SessionID, 10)
			if index, ok := sessionRows[key]; ok {
				addUsagePoint(&result.Rows[index], sample)
				if result.Rows[index].SessionCreatedAt == 0 {
					result.Rows[index].SessionCreatedAt = sample.SessionCreatedAt
				}
				result.Rows[index].RequestCount++
				if _, exists := sessionModels[key+"\x00"+modelKey]; !exists {
					result.Rows[index].ModelCount++
				}
			} else {
				point := sample
				point.RequestID, point.RequestTime = "", 0
				point.RequestCount, point.ModelCount = 1, 1
				result.Rows = append(result.Rows, point)
				sessionRows[key] = len(result.Rows) - 1
			}
		}
		remember(sample.Day, sample.SessionID, sample.Provider, sample.Model)
	}
}

func addUsagePoint(target *ModelUsagePoint, value ModelUsagePoint) {
	target.Input += value.Input
	target.Cached += value.Cached
	target.CacheWrite += value.CacheWrite
	target.Output += value.Output
	target.Total += value.Total
}

func sortModelUsageQuery(result *ModelUsageQueryResult) {
	sort.Slice(result.ByDay, func(i, j int) bool { return result.ByDay[i].Day > result.ByDay[j].Day })
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Dimension == "session" {
			if result.Rows[i].SessionCreatedAt != result.Rows[j].SessionCreatedAt {
				return result.Rows[i].SessionCreatedAt > result.Rows[j].SessionCreatedAt
			}
			if result.Rows[i].SessionID != result.Rows[j].SessionID {
				return result.Rows[i].SessionID > result.Rows[j].SessionID
			}
			return result.Rows[i].Day > result.Rows[j].Day
		}
		if result.Rows[i].Day != result.Rows[j].Day {
			return result.Rows[i].Day > result.Rows[j].Day
		}
		if result.Dimension == "request" {
			return modelUsageRequestBefore(result.Rows[i], result.Rows[j])
		}
		return result.Rows[i].Total > result.Rows[j].Total
	})
}
