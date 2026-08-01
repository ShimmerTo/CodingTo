package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func copyTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if info.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o600)
	})
}

// skillModeDir maps a skill load mode to the directory name inside the agent
// data dir: "startup" skills live in "skills" (loaded when pi starts), while
// "skills_list" skills live in "skills_list" (loaded on demand).
func skillModeDir(mode string) string {
	if mode == "skills_list" {
		return "skills_list"
	}
	return "skills"
}

func removeSkillPath(agent AgentProfile, item discoveredSkill) error {
	root := filepath.Join(agent.DataDir, skillModeDir(item.LoadMode))
	if !skillWithinPath(root, item.root) {
		return errors.New("refuse to remove a path outside the agent skill directory")
	}
	return os.RemoveAll(item.root)
}

// removeSingleSkillFromAgent 仅删除某一 agent 数据目录内匹配 skillID 的单个 skill 目录。
// 对 pi 包而言只移除该 skill 本身，不会动外层包目录（否则同包其它 skill 会被一并删除）；
// 对 managed skill 仅卸载该 agent 的那一份副本。
func (a *App) removeSingleSkillFromAgent(agentID, skillID string) {
	profile, err := a.skillAgent(agentID)
	if err != nil {
		return
	}
	items, err := discoverAgentSkills(profile)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.ID != skillID {
			continue
		}
		if it.SourceType == "managed" {
			undeployManagedSkill(a, profile, skillID)
			return
		}
		// pi（及其它路径型 skill）：只删除该 skill 所在目录，不动外层包目录。
		_ = os.RemoveAll(filepath.Dir(it.Path))
		return
	}
}

// registrySkillsDir returns the central skill library directory. Every zip/url
// skill is stored here exactly once (one sub-directory per skill) and then
// copied into each assigned agent's skills/ or skills_list/ directory based on
// its load mode.
func registrySkillsDir(a *App) string {
	return filepath.Join(a.store.Dir(), "skills")
}

// undeployManagedSkill removes every deployed copy of a managed skill (matched
// by metadata id) from an agent's skills and skills_list directories.
func undeployManagedSkill(a *App, profile AgentProfile, id string) {
	for _, mode := range []string{"startup", "skills_list"} {
		root := filepath.Join(profile.DataDir, skillModeDir(mode))
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(root, e.Name())
			if meta, ok := readSkillMetadata(sub); ok && meta.ID == id {
				_ = os.RemoveAll(sub)
			}
		}
	}
}

func copyManagedSkill(source string, agent AgentProfile, mode, id string) error {
	root := filepath.Join(agent.DataDir, skillModeDir(mode))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	name, description, err := parseSkillFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		return err
	}
	target := filepath.Join(root, name+"-"+strings.TrimPrefix(id, "skill-")[:12])
	if !skillWithinPath(root, target) {
		return errors.New("skill destination escapes agent directory")
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	if err := copyTree(source, target); err != nil {
		return err
	}
	old, _ := readSkillMetadata(source)
	old.ID, old.Name, old.Description, old.LoadMode = id, name, description, mode
	return writeSkillMetadata(target, old)
}

func validateAgentSkillPath(agent AgentProfile, requested string) (string, error) {
	requested, err := filepath.Abs(strings.TrimSpace(requested))
	if err != nil || requested == "" {
		return "", errors.New("invalid skill path")
	}
	items, err := AgentSkillPaths(agent)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		path, pathErr := filepath.Abs(item.Path)
		if pathErr == nil && filepath.Clean(path) == filepath.Clean(requested) {
			return path, nil
		}
	}
	return "", errors.New("selected skill is not installed for this agent")
}

// AgentSkillPaths is used by prompt startup validation and by the default
// skills_list tool's contract tests. It intentionally returns only this agent's
// files and never falls back to the process/global PI_CODING_AGENT_DIR.
func AgentSkillPaths(agent AgentProfile) ([]SkillInfo, error) {
	items, err := discoverAgentSkills(agent)
	if err != nil {
		return nil, err
	}
	result := make([]SkillInfo, len(items))
	for i, item := range items {
		result[i] = item.SkillInfo
	}
	return result, nil
}

func findSkillByID(a *App, id string) (*discoveredSkill, AgentProfile, error) {
	// 优先从中央仓库查找 managed（zip/url）skill。
	registry := registrySkillsDir(a)
	rdir := filepath.Join(registry, id)
	if meta, ok := readSkillMetadata(rdir); ok {
		info := SkillInfo{
			ID:          meta.ID,
			Name:        meta.Name,
			Description: meta.Description,
			Path:        filepath.Join(rdir, "SKILL.md"),
			SourceType:  meta.SourceType,
			Source:      meta.Source,
			LoadMode:    meta.LoadMode,
		}
		cfg := a.store.Get()
		for _, agentID := range meta.Agents {
			if p, ok := cfg.Agent(agentID); ok {
				info.Agents = append(info.Agents, SkillAgent{ID: agentID, Name: p.Name, Mode: meta.LoadMode})
			}
		}
		return &discoveredSkill{SkillInfo: info, metadataPath: filepath.Join(rdir, ".codingto-skill.json"), root: rdir}, AgentProfile{}, nil
	}
	// 回退：扫描各 agent 目录（pi 或遗留 managed）。
	cfg := a.store.Get()
	var found *discoveredSkill
	var foundAgent AgentProfile
	for _, agent := range cfg.Agents {
		items, err := discoverAgentSkills(agent)
		if err != nil {
			return nil, AgentProfile{}, err
		}
		for i := range items {
			if items[i].ID != id {
				continue
			}
			if found == nil {
				copyItem := items[i]
				found = &copyItem
				foundAgent = agent
			}
			found.Agents = append(found.Agents, SkillAgent{ID: agent.ID, Name: agent.Name, Path: items[i].Path, Mode: items[i].LoadMode})
		}
	}
	if found != nil {
		return found, foundAgent, nil
	}
	return nil, AgentProfile{}, fmt.Errorf("skill not found: %s", id)
}
