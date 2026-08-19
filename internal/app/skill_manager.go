package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codingto/internal/piagent"
)

type SkillAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type SkillInfo struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Path        string       `json:"path"`
	SourceType  string       `json:"sourceType"`
	Source      string       `json:"source"`
	LoadMode    string       `json:"loadMode"`
	Agents      []SkillAgent `json:"agents"`
}

type SkillPreview struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Count       int    `json:"count"`
}

type SkillArchiveInput struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type InstallSkillsRequest struct {
	Method      string   `json:"method"` // pi, archive, url
	Command     string   `json:"command"`
	URL         string   `json:"url"`
	ArchiveName string   `json:"archiveName"`
	ArchiveData string   `json:"archiveData"`
	AgentIDs    []string `json:"agentIds"`
	LoadMode    string   `json:"loadMode"`
}

type EditSkillRequest struct {
	SkillID  string   `json:"skillId"`
	AgentIDs []string `json:"agentIds"`
	LoadMode string   `json:"loadMode"`
}

type UpdateSkillRequest struct {
	SkillID     string `json:"skillId"`
	URL         string `json:"url"`
	ArchiveName string `json:"archiveName"`
	ArchiveData string `json:"archiveData"`
}

type skillMetadata struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceType  string   `json:"sourceType"`
	Source      string   `json:"source"`
	LoadMode    string   `json:"loadMode"`
	GroupID     string   `json:"groupId"`
	Agents      []string `json:"agents,omitempty"`
}

type discoveredSkill struct {
	SkillInfo
	metadataPath string
	root         string
}

func validateSkillMode(mode string) error {
	if mode != "startup" && mode != "skills_list" {
		return fmt.Errorf("invalid skill load mode %q", mode)
	}
	return nil
}

func normalizeAgentIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	result := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one agent must be selected")
	}
	return result, nil
}

func containsAgentByID(list []SkillAgent, id string) bool {
	for _, agent := range list {
		if agent.ID == id {
			return true
		}
	}
	return false
}

func containsAgent(list []SkillAgent, id string) bool {
	for _, agent := range list {
		if agent.ID == id {
			return true
		}
	}
	return false
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (a *App) skillAgent(agentID string) (AgentProfile, error) {
	cfg := a.store.Get()
	profile, ok := cfg.Agent(agentID)
	if !ok {
		return AgentProfile{}, fmt.Errorf("agent not found: %s", agentID)
	}
	if strings.TrimSpace(profile.DataDir) == "" {
		a.store.EnsureAgentDataDirs(&cfg)
		profile, _ = cfg.Agent(agentID)
	}
	if profile.DataDir == "" {
		return AgentProfile{}, errors.New("agent data directory is required; refuse to use default pi directory")
	}
	return profile, nil
}

func (a *App) ListSkills() ([]SkillInfo, error) {
	cfg := a.store.Get()
	aggregated := map[string]*SkillInfo{}
	addManaged := func(info SkillInfo) {
		existing := aggregated[info.ID]
		if existing == nil {
			copyInfo := info
			copyInfo.Agents = []SkillAgent{}
			aggregated[info.ID] = &copyInfo
			existing = &copyInfo
		}
		for _, agent := range info.Agents {
			if !containsAgentByID(existing.Agents, agent.ID) {
				existing.Agents = append(existing.Agents, agent)
			}
		}
		if existing.Path == "" {
			existing.Path = info.Path
		}
	}
	// 1) Managed (zip/url) skills live in the central registry — one entry per
	// skill, regardless of how many agents it is deployed to.
	registry := registrySkillsDir(a)
	if entries, err := os.ReadDir(registry); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rdir := filepath.Join(registry, e.Name())
			meta, ok := readSkillMetadata(rdir)
			if !ok {
				continue
			}
			if meta.SourceType != "archive" && meta.SourceType != "url" {
				continue
			}
			id := meta.ID
			if id == "" {
				id = e.Name()
			}
			info := SkillInfo{
				ID:          id,
				Name:        meta.Name,
				Description: meta.Description,
				Path:        filepath.Join(rdir, "SKILL.md"),
				SourceType:  meta.SourceType,
				Source:      meta.Source,
				LoadMode:    meta.LoadMode,
			}
			for _, agentID := range meta.Agents {
				if p, ok := cfg.Agent(agentID); ok {
					info.Agents = append(info.Agents, SkillAgent{ID: agentID, Name: p.Name, Mode: meta.LoadMode})
				}
			}
			addManaged(info)
		}
	}
	// 2) pi-installed skills are still discovered from each agent's directories.
	for _, agent := range cfg.Agents {
		items, err := discoverAgentSkills(agent)
		if err != nil {
			return nil, fmt.Errorf("scan skills for %s: %w", agent.Name, err)
		}
		for _, item := range items {
			if item.SourceType != "pi" {
				continue
			}
			if existing := aggregated[item.ID]; existing != nil {
				if !containsAgentByID(existing.Agents, agent.ID) {
					existing.Agents = append(existing.Agents, SkillAgent{ID: agent.ID, Name: agent.Name, Path: item.Path, Mode: item.LoadMode})
				}
				continue
			}
			info := item.SkillInfo
			info.Agents = []SkillAgent{{ID: agent.ID, Name: agent.Name, Path: item.Path, Mode: item.LoadMode}}
			copyInfo := info
			aggregated[item.ID] = &copyInfo
		}
	}
	result := make([]SkillInfo, 0, len(aggregated))
	for _, info := range aggregated {
		sort.Slice(info.Agents, func(i, j int) bool { return info.Agents[i].Name < info.Agents[j].Name })
		result = append(result, *info)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
}

func (a *App) InstallSkills(req InstallSkillsRequest) ([]SkillInfo, error) {
	ids, err := normalizeAgentIDs(req.AgentIDs)
	if err != nil {
		return nil, err
	}
	if req.Method == "pi" {
		source, err := piagent.ParsePiInstallCommand(req.Command)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, err := a.skillAgent(id); err != nil {
				return nil, err
			}
		}
		for _, id := range ids {
			profile, profileErr := a.skillAgent(id)
			if profileErr != nil {
				return nil, profileErr
			}
			out, installErr := piagent.InstallAgentPackage(profile.DataDir, source)
			if installErr != nil {
				return nil, fmt.Errorf("install skill for %s: %s", profile.Name, out)
			}
			items, scanErr := discoverAgentSkills(profile)
			if scanErr != nil {
				return nil, scanErr
			}
			found := false
			for _, item := range items {
				if item.SourceType == "pi" && item.Source == source {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("package %s contains no valid SKILL.md", source)
			}
		}
		if err := a.enableBuiltinForAgents(ids, "skills-list"); err != nil {
			return nil, fmt.Errorf("enable Skills List extension: %w", err)
		}
		return a.ListSkills()
	}
	var data []byte
	source := req.URL
	if req.Method == "archive" {
		data, err = archiveBytes(SkillArchiveInput{Name: req.ArchiveName, Data: req.ArchiveData})
		source = req.ArchiveName
	} else if req.Method == "url" {
		data, err = downloadSkillURL(req.URL)
	} else {
		err = errors.New("skill install method must be pi, archive, or url")
	}
	if err != nil {
		return nil, err
	}
	if err := validateSkillMode(req.LoadMode); err != nil {
		return nil, err
	}
	// 解压后将本体写入中央仓库（每个 skill 一份），再按 loadMode 复制到各 agent。
	temp, items, err := extractSkillArchive(data)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	registry := registrySkillsDir(a)
	if err := os.MkdirAll(registry, 0o700); err != nil {
		return nil, err
	}
	groupID := randomID("skill-group")
	for _, item := range items {
		rel, _ := filepath.Rel(temp, item.root)
		id := skillID(req.Method, source, item.Name, filepath.ToSlash(rel))
		rdir := filepath.Join(registry, id)
		if err := os.MkdirAll(rdir, 0o700); err != nil {
			return nil, err
		}
		if err := copyTree(item.root, rdir); err != nil {
			return nil, err
		}
		meta := skillMetadata{
			ID:          id,
			Name:        item.Name,
			Description: item.Description,
			SourceType:  req.Method,
			Source:      source,
			LoadMode:    req.LoadMode,
			GroupID:     groupID,
			Agents:      append([]string(nil), ids...),
		}
		if err := writeSkillMetadata(rdir, meta); err != nil {
			return nil, err
		}
		for _, agentID := range ids {
			profile, err := a.skillAgent(agentID)
			if err != nil {
				return nil, err
			}
			if err := copyManagedSkill(rdir, profile, req.LoadMode, id); err != nil {
				return nil, err
			}
		}
	}
	if err := a.enableBuiltinForAgents(ids, "skills-list"); err != nil {
		return nil, fmt.Errorf("enable Skills List extension: %w", err)
	}
	return a.ListSkills()
}

func (a *App) EditSkill(req EditSkillRequest) ([]SkillInfo, error) {
	ids, err := normalizeAgentIDs(req.AgentIDs)
	if err != nil {
		return nil, err
	}
	if err := validateSkillMode(req.LoadMode); err != nil {
		return nil, err
	}
	item, _, err := findSkillByID(a, req.SkillID)
	if err != nil {
		return nil, err
	}
	if item.SourceType == "pi" {
		for _, id := range ids {
			profile, err := a.skillAgent(id)
			if err != nil {
				return nil, err
			}
			if !containsAgent(item.Agents, id) {
				out, installErr := piagent.InstallAgentPackage(profile.DataDir, item.Source)
				if installErr != nil {
					return nil, fmt.Errorf("install skill for %s: %s", profile.Name, out)
				}
			}
		}
		cfg := a.store.Get()
		for _, agent := range cfg.Agents {
			if containsAgent(item.Agents, agent.ID) && !containsString(ids, agent.ID) {
				_, _ = a.UninstallAgentExtension(AgentExtensionKeyRequest{AgentID: agent.ID, Key: item.Source})
			}
		}
		if err := a.enableBuiltinForAgents(ids, "skills-list"); err != nil {
			return nil, fmt.Errorf("enable Skills List extension: %w", err)
		}
		return a.ListSkills()
	}
	// managed（zip/url）：以中央仓库为准，更新元数据后重新部署到各 agent。
	rdir := item.root
	meta, ok := readSkillMetadata(rdir)
	if !ok {
		return nil, fmt.Errorf("skill metadata missing: %s", req.SkillID)
	}
	oldAgents := make([]string, 0, len(item.Agents))
	for _, agent := range item.Agents {
		oldAgents = append(oldAgents, agent.ID)
	}
	for _, agentID := range oldAgents {
		if !containsString(ids, agentID) {
			if profile, err := a.skillAgent(agentID); err == nil {
				undeployManagedSkill(a, profile, req.SkillID)
			}
		}
	}
	meta.Agents = append([]string(nil), ids...)
	meta.LoadMode = req.LoadMode
	if err := writeSkillMetadata(rdir, meta); err != nil {
		return nil, err
	}
	for _, agentID := range ids {
		profile, err := a.skillAgent(agentID)
		if err != nil {
			return nil, err
		}
		undeployManagedSkill(a, profile, req.SkillID)
		if err := copyManagedSkill(rdir, profile, req.LoadMode, req.SkillID); err != nil {
			return nil, err
		}
	}
	if err := a.enableBuiltinForAgents(ids, "skills-list"); err != nil {
		return nil, fmt.Errorf("enable Skills List extension: %w", err)
	}
	return a.ListSkills()
}

// DeleteSkill 删除技能。
//   - agentID 为空：全局删除（zip/url 中央仓库本体 + 所有已部署副本；pi 则删除各 agent 副本）。
//   - agentID 非空：仅从该 agent 取消部署（其它分配了该技能的 agent 不受影响）。
func (a *App) DeleteSkill(skillID string, agentID string) ([]SkillInfo, error) {
	item, _, err := findSkillByID(a, skillID)
	if err != nil {
		return nil, err
	}
	if agentID != "" {
		a.removeSingleSkillFromAgent(agentID, skillID)
		// managed（zip/url）还存在于中央仓库：同步移除该 agent 引用，
		// 若已无其它 agent 引用则删除中央仓库本体。
		if item.SourceType != "pi" {
			registryID := filepath.Join(registrySkillsDir(a), skillID)
			if item.root == registryID {
				if meta, ok := readSkillMetadata(item.root); ok {
					remaining := make([]string, 0, len(meta.Agents))
					for _, ag := range meta.Agents {
						if ag != agentID {
							remaining = append(remaining, ag)
						}
					}
					if len(remaining) == 0 {
						_ = os.RemoveAll(item.root)
					} else {
						meta.Agents = remaining
						_ = writeSkillMetadata(item.root, meta)
					}
				}
			}
		}
		return a.ListSkills()
	}
	if item.SourceType == "pi" {
		// 只删除该 skill 自身目录，而不是卸载整个 npm 包；
		// 否则同一包内的其它 skill 会被一并删除（例如一个包含 3 个 skill，
		// 删除其中 1 个会把另外 2 个也删掉）。
		for _, agent := range item.Agents {
			a.removeSingleSkillFromAgent(agent.ID, skillID)
		}
		return a.ListSkills()
	}
	// managed（zip/url）：从所有分配 agent 卸载已部署副本，再删除中央仓库本体。
	for _, agent := range item.Agents {
		if profile, err := a.skillAgent(agent.ID); err == nil {
			undeployManagedSkill(a, profile, skillID)
		}
	}
	if err := os.RemoveAll(item.root); err != nil {
		return nil, err
	}
	return a.ListSkills()
}

func (a *App) UpdateSkill(req UpdateSkillRequest) ([]SkillInfo, error) {
	item, _, err := findSkillByID(a, req.SkillID)
	if err != nil {
		return nil, err
	}
	if item.SourceType == "pi" {
		for _, agent := range item.Agents {
			profile, profileErr := a.skillAgent(agent.ID)
			if profileErr != nil {
				return nil, profileErr
			}
			out, updateErr := piagent.UpdateAgentPackage(profile.DataDir, item.Source)
			if updateErr != nil {
				return nil, fmt.Errorf("update skill for %s: %s", agent.Name, out)
			}
		}
		return a.ListSkills()
	}
	rdir := item.root
	meta, ok := readSkillMetadata(rdir)
	if !ok {
		return nil, fmt.Errorf("skill metadata missing: %s", req.SkillID)
	}
	var data []byte
	if strings.TrimSpace(req.URL) != "" {
		data, err = downloadSkillURL(req.URL)
	} else {
		data, err = archiveBytes(SkillArchiveInput{Name: req.ArchiveName, Data: req.ArchiveData})
	}
	if err != nil {
		return nil, err
	}
	temp, replacements, err := extractSkillArchive(data)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	var replacement discoveredSkill
	for _, candidate := range replacements {
		if candidate.Name == meta.Name || len(replacements) == 1 {
			replacement = candidate
			break
		}
	}
	if replacement.root == "" {
		return nil, fmt.Errorf("updated archive does not contain skill %s", meta.Name)
	}
	// 用新包内容覆盖中央仓库本体，再重新部署到各分配 agent。
	if err := os.RemoveAll(rdir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		return nil, err
	}
	if err := copyTree(replacement.root, rdir); err != nil {
		return nil, err
	}
	meta.Source = req.URL
	if strings.TrimSpace(req.URL) == "" {
		meta.Source = req.ArchiveName
	}
	if err := writeSkillMetadata(rdir, meta); err != nil {
		return nil, err
	}
	for _, agentID := range meta.Agents {
		profile, err := a.skillAgent(agentID)
		if err != nil {
			return nil, err
		}
		undeployManagedSkill(a, profile, req.SkillID)
		if err := copyManagedSkill(rdir, profile, meta.LoadMode, req.SkillID); err != nil {
			return nil, err
		}
	}
	return a.ListSkills()
}
