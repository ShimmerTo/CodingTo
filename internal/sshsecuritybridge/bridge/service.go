package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codingto/internal/sshsecurity"
	"codingto/internal/sshsecuritybridge/audit"
	"codingto/internal/sshsecuritybridge/config"
	"codingto/internal/sshsecuritybridge/executor"
	"codingto/internal/sshsecuritybridge/protocol"
)

const confirmTTL = 2 * time.Minute

type executeRequest struct {
	ResourceID string         `json:"resourceId"`
	Capability string         `json:"capability"`
	Params     map[string]any `json:"params"`
}

type pendingConfirm struct {
	resource   sshsecurity.Resource
	capability sshsecurity.Capability
	prepared   executor.Prepared
	createdAt  time.Time
}

// Service performs capability lookup, policy decisions, confirmation and execution.
type Service struct {
	snapshot *config.Snapshot
	recorder *audit.Recorder
	known    *sshsecurity.KnownHosts
	mu       sync.Mutex
	pending  map[string]*pendingConfirm
}

// New initializes an SSH security service from a private session snapshot.
// knownHostsPath enables TOFU host-key recording when set; it is shared with
// the main process via CODINGTO_SSH_KNOWN_HOSTS so a first accepted server is
// remembered across the bridge and the editor's test connection.
func New(configPath string, knownHostsPath string) (*Service, error) {
	sessionDir := filepath.Dir(filepath.Dir(configPath))
	recorder, err := audit.NewRecorder(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("初始化 SSH 审计失败：%w", err)
	}
	return &Service{
		snapshot: config.NewSnapshot(configPath),
		recorder: recorder,
		known:    sshsecurity.LoadKnownHosts(knownHostsPath),
		pending:  map[string]*pendingConfirm{},
	}, nil
}

// Close releases audit resources.
func (s *Service) Close() { s.recorder.Close() }

// Handle dispatches SSH bridge protocol actions.
func (s *Service) Handle(ctx context.Context, _ string, action string, params json.RawMessage) (any, error) {
	switch action {
	case "resources":
		return s.resources()
	case "catalog":
		var req struct {
			ResourceID string `json:"resourceId"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, protocol.Errorf("bad_request", "参数解析失败")
		}
		return s.catalog(req.ResourceID)
	case "execute":
		var req executeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, protocol.Errorf("bad_request", "参数解析失败")
		}
		return s.execute(ctx, req)
	case "confirm":
		var req struct {
			Token    string `json:"token"`
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, protocol.Errorf("bad_request", "参数解析失败")
		}
		return s.confirm(ctx, req.Token, req.Decision)
	default:
		return nil, protocol.Errorf("bad_request", "未知动作：%s", action)
	}
}

func (s *Service) resources() (any, error) {
	cfg, err := s.snapshot.Config()
	if err != nil {
		return nil, protocol.Errorf("config_error", "%v", err)
	}
	items := make([]map[string]any, 0, len(cfg.Resources))
	for _, resource := range cfg.Resources {
		items = append(items, map[string]any{"id": resource.ID, "name": resource.Name, "address": resource.Address, "workDir": resource.WorkDir, "preset": resource.Policy.Preset})
	}
	return map[string]any{"resources": items}, nil
}

func (s *Service) catalog(resourceID string) (any, error) {
	resource, err := s.resource(resourceID)
	if err != nil {
		return nil, err
	}
	capabilities := append(sshsecurity.BuiltinCapabilities(), resource.CustomCapabilities...)
	items := make([]map[string]any, 0, len(capabilities))
	for _, capability := range capabilities {
		capability.Normalize()
		effect, reason := sshsecurity.ResolveEffect(resource.Policy, capability)
		params := map[string]any{}
		for name, spec := range capability.Params {
			params[name] = spec
		}
		items = append(items, map[string]any{"name": capability.Name, "group": capability.Group, "description": capability.Description, "permission": effect, "reason": reason, "params": params})
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["name"]) < fmt.Sprint(items[j]["name"]) })
	return map[string]any{"resourceId": resource.ID, "capabilities": items}, nil
}

func (s *Service) execute(ctx context.Context, req executeRequest) (any, error) {
	resource, err := s.resource(strings.TrimSpace(req.ResourceID))
	if err != nil {
		return nil, err
	}
	capability, ok := sshsecurity.CapabilityByName(resource, strings.ToLower(strings.TrimSpace(req.Capability)))
	if !ok {
		return nil, protocol.Errorf("capability_not_found", "能力不存在：%s", req.Capability)
	}
	prepared, err := executor.Prepare(resource, capability, req.Params)
	if err != nil {
		return nil, protocol.Errorf("bad_request", "%v", err)
	}
	effect, reason := sshsecurity.ResolveEffect(resource.Policy, capability)
	if reason == "" {
		reason = defaultReason(effect, capability.Name)
	}
	switch effect {
	case sshsecurity.EffectDeny:
		s.record(resource.ID, capability.Name, "deny", reason, 0, 0, "")
		return nil, protocol.Errorf("policy_denied", "%s", reason)
	case sshsecurity.EffectAsk:
		token, tokenErr := newToken()
		if tokenErr != nil {
			return nil, protocol.Errorf("internal_error", "生成确认令牌失败")
		}
		s.mu.Lock()
		s.sweepExpiredLocked()
		s.pending[token] = &pendingConfirm{resource: resource, capability: capability, prepared: prepared, createdAt: time.Now()}
		s.mu.Unlock()
		s.record(resource.ID, capability.Name, "ask", reason, 0, 0, "")
		return map[string]any{"needsConfirm": map[string]any{"token": token, "resourceId": resource.ID, "resourceName": resource.Name, "capability": capability.Name, "description": capability.Description, "params": prepared.Summary, "reason": reason, "expiresInMs": confirmTTL.Milliseconds()}}, nil
	case sshsecurity.EffectAllow:
		return s.run(ctx, resource, capability, prepared, "allow")
	default:
		return nil, protocol.Errorf("policy_denied", "能力策略无有效裁决")
	}
}

func (s *Service) confirm(ctx context.Context, token, decision string) (any, error) {
	s.mu.Lock()
	s.sweepExpiredLocked()
	pending := s.pending[token]
	delete(s.pending, token)
	s.mu.Unlock()
	if pending == nil {
		return nil, protocol.Errorf("invalid_token", "确认令牌无效或已过期")
	}
	if decision == "deny" {
		s.record(pending.resource.ID, pending.capability.Name, "deny", "用户拒绝", 0, 0, "")
		return map[string]any{"denied": true}, nil
	}
	if decision != "allow" {
		return nil, protocol.Errorf("bad_request", "confirm decision 必须是 allow 或 deny")
	}
	return s.run(ctx, pending.resource, pending.capability, pending.prepared, "confirmed")
}

func (s *Service) run(ctx context.Context, resource sshsecurity.Resource, capability sshsecurity.Capability, prepared executor.Prepared, decision string) (any, error) {
	result, err := executor.Run(ctx, resource, capability, prepared, s.known)
	if err != nil {
		s.record(resource.ID, capability.Name, "error", decision, result.DurationMs, result.ExitCode, err.Error())
		return nil, protocol.Errorf("execution_failed", "%v", err)
	}
	s.record(resource.ID, capability.Name, decision, "", result.DurationMs, result.ExitCode, "")
	return map[string]any{"resourceId": resource.ID, "capability": capability.Name, "output": result.Output, "exitCode": result.ExitCode, "durationMs": result.DurationMs, "truncated": result.Truncated}, nil
}

func (s *Service) resource(id string) (sshsecurity.Resource, error) {
	if id == "" {
		return sshsecurity.Resource{}, protocol.Errorf("bad_request", "resourceId 不能为空")
	}
	cfg, err := s.snapshot.Config()
	if err != nil {
		return sshsecurity.Resource{}, protocol.Errorf("config_error", "%v", err)
	}
	resource, ok := cfg.ByID(id)
	if !ok {
		return sshsecurity.Resource{}, protocol.Errorf("resource_not_found", "SSH 资源不存在：%s", id)
	}
	return resource, nil
}

func (s *Service) record(resourceID, capability, decision, reason string, durationMs int64, exitCode int, errText string) {
	s.recorder.Record(audit.Event{ResourceID: resourceID, Capability: capability, Decision: decision, Reason: reason, DurationMs: durationMs, ExitCode: exitCode, Error: errText})
}

func (s *Service) sweepExpiredLocked() {
	now := time.Now()
	for token, pending := range s.pending {
		if now.Sub(pending.createdAt) > confirmTTL {
			delete(s.pending, token)
		}
	}
}

func defaultReason(effect sshsecurity.Effect, name string) string {
	if effect == sshsecurity.EffectDeny {
		return "策略禁止执行 " + name
	}
	return "策略要求用户确认 " + name
}

func newToken() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
