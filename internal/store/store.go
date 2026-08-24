package store

import (
	"database/sql"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"
	"unsafe"

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

// Close releases the underlying SQLite connection. gsdb does not expose its
// handle, so the *sql.DB is reached via reflection; used by tests and on
// application shutdown.
func (s *Store) Close() error {
	field := reflect.ValueOf(s.db).Elem().FieldByName("db")
	if !field.IsValid() || field.IsNil() {
		return nil
	}
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	raw, ok := field.Interface().(*sql.DB)
	if !ok {
		return nil
	}
	return raw.Close()
}

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
	Theme           string
	Language        string
	AccentColor     string
	DefaultProvider string
	DefaultModel    string
	LastEnvironment string
	SessionDir      string
	Figma           string // JSON of extensions.FigmaConfig
	GlobalMCP       string // JSON of []extensions.GlobalPackage
	GlobalPlugins   string // JSON of []extensions.GlobalPackage
	UserName        string // end-user display name shown in the chat UI
	UserAvatar      string // end-user avatar (data-URL or emoji), shown in the chat UI
	ChatLayout      string // 'left' (default) or 'side' conversation layout
	ShowIdentity    bool   // show agent/user avatar + name in conversation
	DiffMode        string // 'unified' (default) or 'split' code diff layout
	FontSize        string // 'small' (default), 'medium' or 'large' UI font size
	// ConciseChat folds thinking steps and tool calls in conversation details
	// into single-line summary blocks (default off).
	ConciseChat         bool
	SubagentConcurrency int // maximum child Agent runs within one parent conversation
	// SystemNotificationEnabled gates desktop system notifications for plan
	// approval requests and conversation completion (on by default).
	SystemNotificationEnabled bool
	// ToolExecutionTimeoutMinutes bounds one tool (bash) execution in minutes.
	// Zero means the application default (10); values are clamped to 1..60 in
	// the AppConfig layer so a corrupted row cannot disable the watchdog.
	ToolExecutionTimeoutMinutes int
	// ProjectHistoryLimit is the maximum number of project memory records kept
	// under each workspace's .codingto/history directory.
	ProjectHistoryLimit int
	// DBConfig is the JSON of dbsecurity.DBConfig: the global database
	// connection inventory (each connection carries its own policy).
	DBConfig string
	// DCGPolicy is the JSON of app.DCGSettings: the per-severity dcg
	// disposition map and the workspace allow switch.
	DCGPolicy string
	// SessionCleanupEnabled gates startup auto-cleanup of expired session
	// data (database rows plus their on-disk session directories).
	SessionCleanupEnabled bool
	// SessionCleanupDays is the retention cutoff in days for the cleanup;
	// sessions whose last update is older than this many days are removed.
	// Clamped to 1..100 by the AppConfig layer.
	SessionCleanupDays int
	// RecordAPIDetails enables opt-in full provider request/result files.
	RecordAPIDetails bool
}

func (s *Store) GetSetting() (Setting, error) {
	row, err := s.db.QuickQuery("tbl_setting", "theme, language, accent_color, default_provider, default_model, last_environment, session_dir, figma, global_mcp, global_plugins, user_name, user_avatar, chat_layout, show_identity, diff_mode, font_size, concise_chat, subagent_concurrency, system_notification_enabled, tool_execution_timeout, project_history_limit, db_config, dcg_policy, session_cleanup_enabled, session_cleanup_days, record_api_details", map[string]any{"id": 1}).One()
	if err != nil {
		return Setting{}, err
	}
	if len(row) == 0 {
		return Setting{Theme: "system", Language: "zh-CN", Figma: "{}", ChatLayout: "left", ShowIdentity: true, DiffMode: "unified", FontSize: "small", SubagentConcurrency: 2, SystemNotificationEnabled: true, ToolExecutionTimeoutMinutes: 10, ProjectHistoryLimit: 100, SessionCleanupDays: 60}, nil
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
		ConciseChat:                 asString(row["concise_chat"]) != "0",
		SubagentConcurrency:         int(asInt(row["subagent_concurrency"])),
		SystemNotificationEnabled:   asString(row["system_notification_enabled"]) != "0",
		ToolExecutionTimeoutMinutes: int(asInt(row["tool_execution_timeout"])),
		ProjectHistoryLimit:         int(asInt(row["project_history_limit"])),
		DBConfig:                    asString(row["db_config"]),
		DCGPolicy:                   asString(row["dcg_policy"]),
		SessionCleanupEnabled:       asString(row["session_cleanup_enabled"]) != "0",
		SessionCleanupDays:          int(asInt(row["session_cleanup_days"])),
		RecordAPIDetails:            asString(row["record_api_details"]) != "0",
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
			"concise_chat":                boolToInt(set.ConciseChat),
			"subagent_concurrency":        set.SubagentConcurrency,
			"system_notification_enabled": boolToInt(set.SystemNotificationEnabled),
			"tool_execution_timeout":      set.ToolExecutionTimeoutMinutes,
			"project_history_limit":       set.ProjectHistoryLimit,
			"db_config":                   set.DBConfig,
			"dcg_policy":                  set.DCGPolicy,
			"session_cleanup_enabled":     boolToInt(set.SessionCleanupEnabled),
			"session_cleanup_days":        set.SessionCleanupDays,
			"record_api_details":          boolToInt(set.RecordAPIDetails),
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
		"concise_chat":                boolToInt(set.ConciseChat),
		"subagent_concurrency":        set.SubagentConcurrency,
		"system_notification_enabled": boolToInt(set.SystemNotificationEnabled),
		"tool_execution_timeout":      set.ToolExecutionTimeoutMinutes,
		"project_history_limit":       set.ProjectHistoryLimit,
		"db_config":                   set.DBConfig,
		"dcg_policy":                  set.DCGPolicy,
		"session_cleanup_enabled":     boolToInt(set.SessionCleanupEnabled),
		"session_cleanup_days":        set.SessionCleanupDays,
		"record_api_details":          boolToInt(set.RecordAPIDetails),
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
	ID                   string
	Name                 string
	Address              string
	Port                 int
	Username             string
	AuthMode             string // "password" | "key"
	Password             string
	PrivateKey           string
	PrivateKeyPassphrase string
	HostKeyFingerprint   string
	Remark               string
	PolicyPreset         string
	PolicyOverrides      []SSHPolicyOverride
	CustomCapabilities   []SSHCapability
}

// SSHPolicyOverride is one normalized capability policy row for an SSH profile.
type SSHPolicyOverride struct {
	ID         string
	Capability string
	Effect     string
	Reason     string
}

// SSHCapability is one normalized custom capability row. Args and Params are
// bounded JSON collections inside the row; identity and policy remain scalar.
type SSHCapability struct {
	Name           string
	Group          string
	Description    string
	Executable     string
	Args           string
	Params         string
	Permission     string
	TimeoutSeconds int
}

func (s *Store) ListSSHConfigs() ([]SSHConfig, error) {
	rows, err := s.db.QueryBySql("SELECT ssh_id, name, address, port, username, auth_mode, password, private_key, private_key_passphrase, host_key_fingerprint, remark, policy_preset FROM tbl_ssh_config ORDER BY id ASC").All()
	if err != nil {
		return nil, err
	}
	items := make([]SSHConfig, 0, len(rows))
	positions := make(map[string]int, len(rows))
	for _, r := range rows {
		sshID := asString(r["ssh_id"])
		positions[sshID] = len(items)
		items = append(items, SSHConfig{
			ID:                   sshID,
			Name:                 asString(r["name"]),
			Address:              asString(r["address"]),
			Port:                 int(asInt(r["port"])),
			Username:             asString(r["username"]),
			AuthMode:             asString(r["auth_mode"]),
			Password:             asString(r["password"]),
			PrivateKey:           asString(r["private_key"]),
			PrivateKeyPassphrase: asString(r["private_key_passphrase"]),
			HostKeyFingerprint:   asString(r["host_key_fingerprint"]),
			Remark:               asString(r["remark"]),
			PolicyPreset:         asString(r["policy_preset"]),
			PolicyOverrides:      []SSHPolicyOverride{},
			CustomCapabilities:   []SSHCapability{},
		})
	}
	overrides, err := s.db.QueryBySql("SELECT ssh_id, override_id, capability, effect, reason FROM tbl_ssh_policy_override ORDER BY ssh_id ASC, position ASC, id ASC").All()
	if err != nil {
		return nil, err
	}
	for _, row := range overrides {
		index, ok := positions[asString(row["ssh_id"])]
		if !ok {
			continue
		}
		items[index].PolicyOverrides = append(items[index].PolicyOverrides, SSHPolicyOverride{
			ID: asString(row["override_id"]), Capability: asString(row["capability"]),
			Effect: asString(row["effect"]), Reason: asString(row["reason"]),
		})
	}
	capabilities, err := s.db.QueryBySql("SELECT ssh_id, capability_name, group_name, description, executable, args, params, permission, timeout_seconds FROM tbl_ssh_capability ORDER BY ssh_id ASC, position ASC, id ASC").All()
	if err != nil {
		return nil, err
	}
	for _, row := range capabilities {
		index, ok := positions[asString(row["ssh_id"])]
		if !ok {
			continue
		}
		items[index].CustomCapabilities = append(items[index].CustomCapabilities, SSHCapability{
			Name: asString(row["capability_name"]), Group: asString(row["group_name"]),
			Description: asString(row["description"]), Executable: asString(row["executable"]),
			Args: asString(row["args"]), Params: asString(row["params"]),
			Permission: asString(row["permission"]), TimeoutSeconds: int(asInt(row["timeout_seconds"])),
		})
	}
	return items, nil
}

// SaveSSHConfigs replaces the entire SSH config list, keeping the persisted rows
// in sync with the in-memory configuration. Deleted configs are removed.
func (s *Store) SaveSSHConfigs(items []SSHConfig) error {
	now := time.Now().Unix()
	tx, err := s.db.GetTx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.Query("SELECT ssh_id FROM tbl_ssh_config")
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		existing[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[item.ID] = struct{}{}
		if existing[item.ID] {
			_, err = tx.Exec(`UPDATE tbl_ssh_config SET name = ?, address = ?, port = ?, username = ?, auth_mode = ?, password = ?, private_key = ?, private_key_passphrase = ?, host_key_fingerprint = ?, remark = ?, policy_preset = ?, update_time = ? WHERE ssh_id = ?`,
				item.Name, item.Address, item.Port, item.Username, item.AuthMode, item.Password, item.PrivateKey, item.PrivateKeyPassphrase, item.HostKeyFingerprint, item.Remark, item.PolicyPreset, now, item.ID)
			if err != nil {
				return err
			}
		} else {
			_, err = tx.Exec(`INSERT INTO tbl_ssh_config (ssh_id, name, address, port, username, auth_mode, password, private_key, private_key_passphrase, host_key_fingerprint, remark, policy_preset, create_time, update_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				item.ID, item.Name, item.Address, item.Port, item.Username, item.AuthMode, item.Password, item.PrivateKey, item.PrivateKeyPassphrase, item.HostKeyFingerprint, item.Remark, item.PolicyPreset, now, now)
			if err != nil {
				return err
			}
			existing[item.ID] = true
		}
		if err := replaceSSHPolicyRows(tx, item, now); err != nil {
			return err
		}
	}
	for id := range existing {
		if _, keep := seen[id]; keep {
			continue
		}
		if _, err := tx.Exec("DELETE FROM tbl_ssh_policy_override WHERE ssh_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM tbl_ssh_capability WHERE ssh_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM tbl_ssh_config WHERE ssh_id = ?", id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func replaceSSHPolicyRows(tx *sql.Tx, item SSHConfig, now int64) error {
	if _, err := tx.Exec("DELETE FROM tbl_ssh_policy_override WHERE ssh_id = ?", item.ID); err != nil {
		return err
	}
	for position, rule := range item.PolicyOverrides {
		if _, err := tx.Exec(`INSERT INTO tbl_ssh_policy_override (ssh_id, override_id, capability, effect, reason, position, create_time, update_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, rule.ID, rule.Capability, rule.Effect, rule.Reason, position, now, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM tbl_ssh_capability WHERE ssh_id = ?", item.ID); err != nil {
		return err
	}
	for position, capability := range item.CustomCapabilities {
		if _, err := tx.Exec(`INSERT INTO tbl_ssh_capability (ssh_id, capability_name, group_name, description, executable, args, params, permission, timeout_seconds, position, create_time, update_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, capability.Name, capability.Group, capability.Description, capability.Executable,
			capability.Args, capability.Params, capability.Permission, capability.TimeoutSeconds, position, now, now); err != nil {
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
	// DBConnections is a JSON []string of DB connection IDs authorized for
	// this workspace's sessions.
	DBConnections string
	// DefaultAgentID is the agent used when opening a new conversation in
	// this workspace; empty means fall back to the first agent.
	DefaultAgentID string
}

// ListEnvironments returns all stored environments. Remotes are persisted as a
// JSON string and rehydrated by the caller.
func (s *Store) ListEnvironments() ([]Environment, error) {
	rows, err := s.db.QueryBySql("SELECT environment_id, name, path, description, remotes, active, db_connections, default_agent_id FROM tbl_environment ORDER BY id ASC").All()
	if err != nil {
		return nil, err
	}
	items := make([]Environment, 0, len(rows))
	for _, r := range rows {
		items = append(items, Environment{
			ID:             asString(r["environment_id"]),
			Name:           asString(r["name"]),
			Path:           asString(r["path"]),
			Description:    asString(r["description"]),
			Remotes:        asString(r["remotes"]),
			Active:         asInt(r["active"]) != 0,
			DBConnections:  asString(r["db_connections"]),
			DefaultAgentID: asString(r["default_agent_id"]),
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
				"name":             item.Name,
				"path":             item.Path,
				"description":      item.Description,
				"remotes":          item.Remotes,
				"active":           boolToInt(item.Active),
				"db_connections":   item.DBConnections,
				"default_agent_id": item.DefaultAgentID,
				"update_time":      now,
			}).Exec()
			if err != nil {
				return err
			}
			continue
		}
		_, err = s.db.QuickCreate("tbl_environment", map[string]any{
			"environment_id":   item.ID,
			"name":             item.Name,
			"path":             item.Path,
			"description":      item.Description,
			"remotes":          item.Remotes,
			"active":           boolToInt(item.Active),
			"db_connections":   item.DBConnections,
			"default_agent_id": item.DefaultAgentID,
			"create_time":      now,
			"update_time":      now,
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

// SetSessionUpdateTime backdates a session's update_time directly (used by
// cleanup tests and retention tooling; normal flows go through UpdateSession).
func (s *Store) SetSessionUpdateTime(id int64, ts int64) error {
	_, err := s.db.ExecBySql("UPDATE tbl_session SET update_time = ? WHERE id = ?", ts, id).Exec()
	return err
}

func (s *Store) DeleteSession(id int64) error {
	tx, err := s.db.GetTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"DELETE FROM tbl_steward_event WHERE session_id = ?",
		"DELETE FROM tbl_steward_permission WHERE session_id = ?",
		"DELETE FROM tbl_bot_task WHERE session_id = ?",
	} {
		if _, err := tx.Exec(statement, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM tbl_session WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
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
