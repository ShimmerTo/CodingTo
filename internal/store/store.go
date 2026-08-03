package store

import (
	"database/sql"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/w896736588/go-tool/gsdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const dbName = "codingto.db"

// Store owns the SQLite database and the per-table repositories. Every table is
// migrated with goose (embedded SQL) and read/written through this package so
// that no business object is persisted as a single JSON blob.
type Store struct {
	dir string
	db  *gsdb.GsSqlite
}

// Open opens (creating if needed) the application database rooted at dir and
// runs all pending goose migrations.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, dbName)
	db, err := gsdb.NewSqlite(dbPath, true)
	if err != nil {
		return nil, err
	}
	if err := runMigrations(db.DbPath); err != nil {
		return nil, err
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, err
	}
	return &Store{dir: dir, db: db}, nil
}

// Dir returns the directory the database lives in.
func (s *Store) Dir() string { return s.dir }

// runMigrations executes embedded goose migrations against the SQLite file using
// the same driver the runtime uses.
func runMigrations(dbPath string) error {
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	goose.SetLogger(goose.NopLogger())
	if err := goose.Up(sqlDB, "migrations"); err != nil && !errors.Is(err, goose.ErrNoMigrations) {
		return err
	}
	return nil
}

// --- Setting (single global row) ---

type Setting struct {
	Theme               string
	Language            string
	AccentColor         string
	DefaultProvider     string
	DefaultModel        string
	LastEnvironment     string
	SessionDir          string
	Figma               string // JSON of extensions.FigmaConfig
	GlobalMCP           string // JSON of []extensions.GlobalPackage
	GlobalPlugins       string // JSON of []extensions.GlobalPackage
	UserName            string // end-user display name shown in the chat UI
	UserAvatar          string // end-user avatar (data-URL or emoji), shown in the chat UI
	ChatLayout          string // 'left' (default) or 'side' conversation layout
	ShowIdentity        bool   // show agent/user avatar + name in conversation
	DiffMode            string // 'unified' (default) or 'split' code diff layout
	FontSize            string // 'small' (default), 'medium' or 'large' UI font size
	SubagentConcurrency int    // maximum child Agent runs within one parent conversation
	// SystemNotificationEnabled gates desktop system notifications for plan
	// approval requests and conversation completion (on by default).
	SystemNotificationEnabled bool
	// ToolExecutionTimeoutMinutes bounds one tool (bash) execution in minutes.
	// Zero means the application default (10); values are clamped to 1..60 in
	// the AppConfig layer so a corrupted row cannot disable the watchdog.
	ToolExecutionTimeoutMinutes int
}

func (s *Store) GetSetting() (Setting, error) {
	row, err := s.db.QuickQuery("tbl_setting", "theme, language, accent_color, default_provider, default_model, last_environment, session_dir, figma, global_mcp, global_plugins, user_name, user_avatar, chat_layout, show_identity, diff_mode, font_size, subagent_concurrency, system_notification_enabled, tool_execution_timeout", map[string]any{"id": 1}).One()
	if err != nil {
		return Setting{}, err
	}
	if len(row) == 0 {
		return Setting{Theme: "system", Language: "zh-CN", Figma: "{}", ChatLayout: "left", ShowIdentity: true, DiffMode: "unified", FontSize: "small", SubagentConcurrency: 2, SystemNotificationEnabled: true, ToolExecutionTimeoutMinutes: 10}, nil
	}
	return Setting{
		Theme:                       asString(row["theme"]),
		Language:                    asString(row["language"]),
		AccentColor:                 asString(row["accent_color"]),
		DefaultProvider:             asString(row["default_provider"]),
		DefaultModel:                asString(row["default_model"]),
		LastEnvironment:             asString(row["last_environment"]),
		SessionDir:                  asString(row["session_dir"]),
		Figma:                       asString(row["figma"]),
		GlobalMCP:                   asString(row["global_mcp"]),
		GlobalPlugins:               asString(row["global_plugins"]),
		UserName:                    asString(row["user_name"]),
		UserAvatar:                  asString(row["user_avatar"]),
		ChatLayout:                  asString(row["chat_layout"]),
		ShowIdentity:                asString(row["show_identity"]) != "0",
		DiffMode:                    asString(row["diff_mode"]),
		FontSize:                    asString(row["font_size"]),
		SubagentConcurrency:         int(asInt(row["subagent_concurrency"])),
		SystemNotificationEnabled:   asString(row["system_notification_enabled"]) != "0",
		ToolExecutionTimeoutMinutes: int(asInt(row["tool_execution_timeout"])),
	}, nil
}

func (s *Store) SaveSetting(set Setting) error {
	row, err := s.db.QuickQuery("tbl_setting", "id", map[string]any{"id": 1}).One()
	if err != nil {
		return err
	}
	if _, exists := row["id"]; exists {
		_, err = s.db.QuickUpdate("tbl_setting", map[string]any{"id": 1}, map[string]any{
			"theme":                       set.Theme,
			"language":                    set.Language,
			"accent_color":                set.AccentColor,
			"default_provider":            set.DefaultProvider,
			"default_model":               set.DefaultModel,
			"last_environment":            set.LastEnvironment,
			"session_dir":                 set.SessionDir,
			"figma":                       set.Figma,
			"global_mcp":                  set.GlobalMCP,
			"global_plugins":              set.GlobalPlugins,
			"user_name":                   set.UserName,
			"user_avatar":                 set.UserAvatar,
			"chat_layout":                 set.ChatLayout,
			"show_identity":               boolToInt(set.ShowIdentity),
			"diff_mode":                   set.DiffMode,
			"font_size":                   set.FontSize,
			"subagent_concurrency":        set.SubagentConcurrency,
			"system_notification_enabled": boolToInt(set.SystemNotificationEnabled),
			"tool_execution_timeout":      set.ToolExecutionTimeoutMinutes,
		}).Exec()
		return err
	}
	_, err = s.db.QuickCreate("tbl_setting", map[string]any{
		"id":                          1,
		"theme":                       set.Theme,
		"language":                    set.Language,
		"default_provider":            set.DefaultProvider,
		"default_model":               set.DefaultModel,
		"last_environment":            set.LastEnvironment,
		"session_dir":                 set.SessionDir,
		"figma":                       set.Figma,
		"global_mcp":                  set.GlobalMCP,
		"global_plugins":              set.GlobalPlugins,
		"user_name":                   set.UserName,
		"user_avatar":                 set.UserAvatar,
		"chat_layout":                 set.ChatLayout,
		"show_identity":               boolToInt(set.ShowIdentity),
		"diff_mode":                   set.DiffMode,
		"font_size":                   set.FontSize,
		"subagent_concurrency":        set.SubagentConcurrency,
		"system_notification_enabled": boolToInt(set.SystemNotificationEnabled),
		"tool_execution_timeout":      set.ToolExecutionTimeoutMinutes,
	}).Exec()
	return err
}

// --- Agent ---

type Agent struct {
	ID                   string
	Name                 string
	DataDir              string
	Description          string
	Avatar               string // emoji avatar for this agent
	Builtin              string // JSON map[string]bool
	Recommended          string // JSON map[string]bool
	Subagents            string // JSON []string
	PiTools              string // JSON map[string]bool
	DefaultProvider      string
	DefaultModel         string
	BrowserProfilePolicy string // JSON browser-profile runtime policy
	ForcedPromptModels   string // JSON map[string]bool of "provider/model" keys
	Active               bool
}

func (s *Store) ListAgents() ([]Agent, error) {
	rows, err := s.db.QueryBySql("SELECT agent_id, name, data_dir, description, avatar, builtin, recommended, subagents, pi_tools, default_provider, default_model, browser_profile_policy, forced_prompt_models, active FROM tbl_agent ORDER BY id ASC").All()
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(rows))
	for _, r := range rows {
		agents = append(agents, Agent{
			ID:                   asString(r["agent_id"]),
			Name:                 asString(r["name"]),
			DataDir:              asString(r["data_dir"]),
			Description:          asString(r["description"]),
			Avatar:               asString(r["avatar"]),
			Builtin:              asString(r["builtin"]),
			Recommended:          asString(r["recommended"]),
			Subagents:            asString(r["subagents"]),
			PiTools:              asString(r["pi_tools"]),
			DefaultProvider:      asString(r["default_provider"]),
			DefaultModel:         asString(r["default_model"]),
			BrowserProfilePolicy: asString(r["browser_profile_policy"]),
			ForcedPromptModels:   asString(r["forced_prompt_models"]),
			Active:               asInt(r["active"]) != 0,
		})
	}
	return agents, nil
}

// AgentByDataDir returns the agent whose data_dir matches, or false.
func (s *Store) AgentByDataDir(dataDir string) (Agent, bool, error) {
	row, err := s.db.QuickQuery("tbl_agent", "agent_id, name, data_dir, description, avatar, builtin, recommended, subagents, pi_tools, default_provider, default_model, browser_profile_policy, forced_prompt_models, active", map[string]any{"data_dir": dataDir}).One()
	if err != nil {
		return Agent{}, false, err
	}
	if len(row) == 0 {
		return Agent{}, false, nil
	}
	return Agent{
		ID:                   asString(row["agent_id"]),
		Name:                 asString(row["name"]),
		DataDir:              asString(row["data_dir"]),
		Description:          asString(row["description"]),
		Avatar:               asString(row["avatar"]),
		Builtin:              asString(row["builtin"]),
		Recommended:          asString(row["recommended"]),
		Subagents:            asString(row["subagents"]),
		PiTools:              asString(row["pi_tools"]),
		DefaultProvider:      asString(row["default_provider"]),
		DefaultModel:         asString(row["default_model"]),
		BrowserProfilePolicy: asString(row["browser_profile_policy"]),
		ForcedPromptModels:   asString(row["forced_prompt_models"]),
		Active:               asInt(row["active"]) != 0,
	}, true, nil
}

// SaveAgents upserts the supplied agents by their stable agent_id. Deletion is
// intentionally explicit so a stale or partial configuration cannot erase an
// existing agent.
func (s *Store) SaveAgents(agents []Agent) error {
	now := time.Now().Unix()
	for _, agent := range agents {
		row, err := s.db.QuickQuery("tbl_agent", "id", map[string]any{"agent_id": agent.ID}).One()
		if err != nil {
			return err
		}
		if _, exists := row["id"]; exists {
			_, err = s.db.QuickUpdate("tbl_agent", map[string]any{"agent_id": agent.ID}, map[string]any{
				"name":                   agent.Name,
				"data_dir":               agent.DataDir,
				"description":            agent.Description,
				"avatar":                 agent.Avatar,
				"builtin":                agent.Builtin,
				"recommended":            agent.Recommended,
				"subagents":              agent.Subagents,
				"pi_tools":               agent.PiTools,
				"default_provider":       agent.DefaultProvider,
				"default_model":          agent.DefaultModel,
				"browser_profile_policy": agent.BrowserProfilePolicy,
				"forced_prompt_models":   agent.ForcedPromptModels,
				"active":                 boolToInt(agent.Active),
				"update_time":            now,
			}).Exec()
			if err != nil {
				return err
			}
			continue
		}
		_, err = s.db.QuickCreate("tbl_agent", map[string]any{
			"agent_id":               agent.ID,
			"name":                   agent.Name,
			"data_dir":               agent.DataDir,
			"description":            agent.Description,
			"avatar":                 agent.Avatar,
			"builtin":                agent.Builtin,
			"recommended":            agent.Recommended,
			"subagents":              agent.Subagents,
			"pi_tools":               agent.PiTools,
			"default_provider":       agent.DefaultProvider,
			"default_model":          agent.DefaultModel,
			"browser_profile_policy": agent.BrowserProfilePolicy,
			"forced_prompt_models":   agent.ForcedPromptModels,
			"active":                 boolToInt(agent.Active),
			"create_time":            now,
			"update_time":            now,
		}).Exec()
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteAgent removes exactly one agent by its stable ID.
func (s *Store) DeleteAgent(id string) error {
	_, err := s.db.QuickDelete("tbl_agent", map[string]any{"agent_id": id}).Exec()
	return err
}

// --- SSHConfig ---

type SSHConfig struct {
	ID       string
	Name     string
	Address  string
	Port     int
	Username string
	Password string
	Remark   string
}

func (s *Store) ListSSHConfigs() ([]SSHConfig, error) {
	rows, err := s.db.QueryBySql("SELECT ssh_id, name, address, port, username, password, remark FROM tbl_ssh_config ORDER BY id ASC").All()
	if err != nil {
		return nil, err
	}
	items := make([]SSHConfig, 0, len(rows))
	for _, r := range rows {
		items = append(items, SSHConfig{
			ID:       asString(r["ssh_id"]),
			Name:     asString(r["name"]),
			Address:  asString(r["address"]),
			Port:     int(asInt(r["port"])),
			Username: asString(r["username"]),
			Password: asString(r["password"]),
			Remark:   asString(r["remark"]),
		})
	}
	return items, nil
}

// SaveSSHConfigs replaces the entire SSH config list, keeping the persisted rows
// in sync with the in-memory configuration. Deleted configs are removed.
func (s *Store) SaveSSHConfigs(items []SSHConfig) error {
	now := time.Now().Unix()
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[item.ID] = struct{}{}
		row, err := s.db.QuickQuery("tbl_ssh_config", "id", map[string]any{"ssh_id": item.ID}).One()
		if err != nil {
			return err
		}
		if _, exists := row["id"]; exists {
			_, err = s.db.QuickUpdate("tbl_ssh_config", map[string]any{"ssh_id": item.ID}, map[string]any{
				"name":        item.Name,
				"address":     item.Address,
				"port":        item.Port,
				"username":    item.Username,
				"password":    item.Password,
				"remark":      item.Remark,
				"update_time": now,
			}).Exec()
			if err != nil {
				return err
			}
			continue
		}
		_, err = s.db.QuickCreate("tbl_ssh_config", map[string]any{
			"ssh_id":      item.ID,
			"name":        item.Name,
			"address":     item.Address,
			"port":        item.Port,
			"username":    item.Username,
			"password":    item.Password,
			"remark":      item.Remark,
			"create_time": now,
			"update_time": now,
		}).Exec()
		if err != nil {
			return err
		}
	}
	rows, err := s.db.QueryBySql("SELECT ssh_id FROM tbl_ssh_config").All()
	if err != nil {
		return err
	}
	for _, r := range rows {
		id := asString(r["ssh_id"])
		if _, keep := seen[id]; keep {
			continue
		}
		if _, err := s.db.QuickDelete("tbl_ssh_config", map[string]any{"ssh_id": id}).Exec(); err != nil {
			return err
		}
	}
	return nil
}

// --- GitConfig removed: environments reference global SSH profiles directly. ---

// --- Environment ---

type Environment struct {
	ID          string
	Name        string
	Path        string
	Description string
	Remotes     string
	Active      bool
}

// ListEnvironments returns all stored environments. Remotes are persisted as a
// JSON string and rehydrated by the caller.
func (s *Store) ListEnvironments() ([]Environment, error) {
	rows, err := s.db.QueryBySql("SELECT environment_id, name, path, description, remotes, active FROM tbl_environment ORDER BY id ASC").All()
	if err != nil {
		return nil, err
	}
	items := make([]Environment, 0, len(rows))
	for _, r := range rows {
		items = append(items, Environment{
			ID:          asString(r["environment_id"]),
			Name:        asString(r["name"]),
			Path:        asString(r["path"]),
			Description: asString(r["description"]),
			Remotes:     asString(r["remotes"]),
			Active:      asInt(r["active"]) != 0,
		})
	}
	return items, nil
}

// SaveEnvironments replaces the entire environment list. Deleted environments
// are removed.
func (s *Store) SaveEnvironments(items []Environment) error {
	now := time.Now().Unix()
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[item.ID] = struct{}{}
		row, err := s.db.QuickQuery("tbl_environment", "id", map[string]any{"environment_id": item.ID}).One()
		if err != nil {
			return err
		}
		if _, exists := row["id"]; exists {
			_, err = s.db.QuickUpdate("tbl_environment", map[string]any{"environment_id": item.ID}, map[string]any{
				"name":        item.Name,
				"path":        item.Path,
				"description": item.Description,
				"remotes":     item.Remotes,
				"active":      boolToInt(item.Active),
				"update_time": now,
			}).Exec()
			if err != nil {
				return err
			}
			continue
		}
		_, err = s.db.QuickCreate("tbl_environment", map[string]any{
			"environment_id": item.ID,
			"name":           item.Name,
			"path":           item.Path,
			"description":    item.Description,
			"remotes":        item.Remotes,
			"active":         boolToInt(item.Active),
			"create_time":    now,
			"update_time":    now,
		}).Exec()
		if err != nil {
			return err
		}
	}
	rows, err := s.db.QueryBySql("SELECT environment_id FROM tbl_environment").All()
	if err != nil {
		return err
	}
	for _, r := range rows {
		id := asString(r["environment_id"])
		if _, keep := seen[id]; keep {
			continue
		}
		if _, err := s.db.QuickDelete("tbl_environment", map[string]any{"environment_id": id}).Exec(); err != nil {
			return err
		}
	}
	return nil
}

// --- Session (global across all agents) ---

type Session struct {
	ID             int64
	AgentID        string
	EnvironmentID  string
	Title          string
	SessionDir     string
	SessionPath    string
	Provider       string
	Model          string
	Status         string
	ExecDurationMs int64
	CreateTime     int64
	UpdateTime     int64
}

func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.QueryBySql(`SELECT id, agent_id, environment_id, title, session_dir, session_path,
		provider, model, status, exec_duration_ms, create_time, update_time
		FROM tbl_session ORDER BY update_time DESC, id DESC`).All()
	if err != nil {
		return nil, err
	}
	items := make([]Session, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionFromRow(row))
	}
	return items, nil
}

func (s *Store) SessionByID(id int64) (Session, bool, error) {
	row, err := s.db.QuickQuery("tbl_session",
		"id, agent_id, environment_id, title, session_dir, session_path, provider, model, status, exec_duration_ms, create_time, update_time",
		map[string]any{"id": id}).One()
	if err != nil {
		return Session{}, false, err
	}
	if len(row) == 0 {
		return Session{}, false, nil
	}
	return sessionFromRow(row), true, nil
}

func (s *Store) CreateSession(item Session) (Session, error) {
	now := time.Now().UnixMilli()
	if item.CreateTime == 0 {
		item.CreateTime = now
	}
	item.UpdateTime = now
	if item.Status == "" {
		item.Status = "active"
	}
	id, err := s.db.QuickCreate("tbl_session", map[string]any{
		"agent_id":         item.AgentID,
		"environment_id":   item.EnvironmentID,
		"title":            item.Title,
		"session_dir":      item.SessionDir,
		"session_path":     item.SessionPath,
		"provider":         item.Provider,
		"model":            item.Model,
		"status":           item.Status,
		"exec_duration_ms": item.ExecDurationMs,
		"create_time":      item.CreateTime,
		"update_time":      item.UpdateTime,
	}).Exec()
	if err != nil {
		return Session{}, err
	}
	item.ID = asInt(id)
	return item, nil
}

func (s *Store) UpdateSession(id int64, values map[string]any) error {
	values["update_time"] = time.Now().UnixMilli()
	_, err := s.db.QuickUpdate("tbl_session", map[string]any{"id": id}, values).Exec()
	return err
}

func (s *Store) DeleteSession(id int64) error {
	_, err := s.db.QuickDelete("tbl_session", map[string]any{"id": id}).Exec()
	return err
}

func (s *Store) RecoverSessions() error {
	_, err := s.db.ExecBySql("UPDATE tbl_session SET status = 'active' WHERE status = 'running'").Exec()
	return err
}

func sessionFromRow(row map[string]any) Session {
	return Session{
		ID:             asInt(row["id"]),
		AgentID:        asString(row["agent_id"]),
		EnvironmentID:  asString(row["environment_id"]),
		Title:          asString(row["title"]),
		SessionDir:     asString(row["session_dir"]),
		SessionPath:    asString(row["session_path"]),
		Provider:       asString(row["provider"]),
		Model:          asString(row["model"]),
		Status:         asString(row["status"]),
		ExecDurationMs: asInt(row["exec_duration_ms"]),
		CreateTime:     asInt(row["create_time"]),
		UpdateTime:     asInt(row["update_time"]),
	}
}

// --- helpers ---

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case bool:
		if t {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case []byte:
		return int64(0)
	default:
		return 0
	}
}
