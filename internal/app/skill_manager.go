package app

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codingto/internal/piagent"
)

const (
	maxSkillArchiveBytes        = 50 << 20
	maxSkillArchiveUncompressed = 200 << 20
	maxSkillArchiveEntries      = 5000
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

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

func validateSkillFrontmatter(raw []byte) (string, string, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", errors.New("SKILL.md frontmatter is not closed")
	}
	var name, description string
	for _, line := range lines[1:end] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.Trim(value, "\"'"))
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	if !skillNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("invalid skill name %q: use lowercase letters, numbers and hyphens", name)
	}
	if description == "" || len(description) > 1024 {
		return "", "", errors.New("SKILL.md description is required and must be at most 1024 characters")
	}
	return name, description, nil
}

func validateSkillMode(mode string) error {
	if mode != "startup" && mode != "skills_list" {
		return fmt.Errorf("invalid skill load mode %q", mode)
	}
	return nil
}

func skillID(sourceType, source, name, relative string) string {
	sum := sha256.Sum256([]byte(sourceType + "\x00" + source + "\x00" + name + "\x00" + relative))
	return "skill-" + hex.EncodeToString(sum[:8])
}

func randomID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}

func readSkillMetadata(root string) (skillMetadata, bool) {
	raw, err := os.ReadFile(filepath.Join(root, ".codingto-skill.json"))
	if err != nil {
		return skillMetadata{}, false
	}
	var meta skillMetadata
	if json.Unmarshal(raw, &meta) != nil || meta.ID == "" {
		return skillMetadata{}, false
	}
	return meta, true
}

func writeSkillMetadata(root string, meta skillMetadata) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ".codingto-skill.json"), append(raw, '\n'), 0o600)
}

func skillWithinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseSkillFile(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return validateSkillFrontmatter(raw)
}

func discoverSkillRoots(root string) ([]discoveredSkill, error) {
	var result []discoveredSkill
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return result, nil
	} else if err != nil {
		return nil, err
	}
	seenRoots := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if path != root && (info.Name() == ".git" || info.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "SKILL.md" {
			return nil
		}
		skillRoot := filepath.Dir(path)
		canonical, err := filepath.Abs(skillRoot)
		if err != nil || seenRoots[canonical] {
			return nil
		}
		name, description, err := parseSkillFile(path)
		if err != nil {
			return nil // Pi ignores invalid skills; the installer validates before copying.
		}
		seenRoots[canonical] = true
		meta, hasMeta := readSkillMetadata(skillRoot)
		if !hasMeta {
			meta = skillMetadata{
				ID:   skillID("managed", root, name, filepath.ToSlash(strings.TrimPrefix(canonical, filepath.Clean(root)+string(filepath.Separator)))),
				Name: name, Description: description, SourceType: "managed", Source: root, LoadMode: "startup",
			}
		}
		mode := meta.LoadMode
		if mode == "" {
			mode = "startup"
		}
		result = append(result, discoveredSkill{
			SkillInfo:    SkillInfo{ID: meta.ID, Name: name, Description: description, Path: filepath.Join(canonical, "SKILL.md"), SourceType: meta.SourceType, Source: meta.Source, LoadMode: mode},
			metadataPath: filepath.Join(skillRoot, ".codingto-skill.json"), root: skillRoot,
		})
		return nil
	})
	return result, err
}

func discoverAgentSkills(agent AgentProfile) ([]discoveredSkill, error) {
	if strings.TrimSpace(agent.DataDir) == "" {
		return nil, errors.New("agent data directory is required")
	}
	dataDir, err := filepath.Abs(agent.DataDir)
	if err != nil {
		return nil, err
	}
	result := []discoveredSkill{}
	for _, dir := range []string{filepath.Join(dataDir, "skills"), filepath.Join(dataDir, "skills_list")} {
		items, err := discoverSkillRoots(dir)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].SourceType == "" {
				items[i].SourceType = "managed"
				items[i].SkillInfo.SourceType = "managed"
			}
			result = append(result, items[i])
		}
	}
	packages, err := piagent.InstalledPackageStatuses(dataDir)
	if err != nil {
		return nil, err
	}
	for _, pkg := range packages {
		if pkg.SourcePath == "" || !skillWithinPath(dataDir, pkg.SourcePath) {
			continue
		}
		items, err := discoverSkillRoots(pkg.SourcePath)
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].SkillInfo.SourceType = "pi"
			items[i].SkillInfo.Source = pkg.Key
			items[i].SkillInfo.LoadMode = "startup"
			relative, _ := filepath.Rel(pkg.SourcePath, items[i].Path)
			items[i].SkillInfo.ID = skillID("pi", pkg.Key, items[i].Name, filepath.ToSlash(relative))
			result = append(result, items[i])
		}
	}
	return result, nil
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

func archiveBytes(input SkillArchiveInput) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input.Data))
	if err != nil || len(data) == 0 {
		return nil, errors.New("invalid ZIP data")
	}
	if len(data) > maxSkillArchiveBytes {
		return nil, fmt.Errorf("skill archive is larger than %d MB", maxSkillArchiveBytes>>20)
	}
	return data, nil
}

func safeZipPath(name string) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\x00") {
		return "", errors.New("invalid ZIP entry path")
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == ".." || part == "" {
			return "", errors.New("ZIP entry escapes its extraction directory")
		}
	}
	return name, nil
}

func extractSkillArchive(data []byte) (string, []discoveredSkill, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, fmt.Errorf("read ZIP archive: %w", err)
	}
	if len(reader.File) > maxSkillArchiveEntries {
		return "", nil, errors.New("skill archive contains too many files")
	}
	temp, err := os.MkdirTemp("", "codingto-skills-")
	if err != nil {
		return "", nil, err
	}
	var total int64
	for _, entry := range reader.File {
		name, err := safeZipPath(entry.Name)
		if err != nil {
			os.RemoveAll(temp)
			return "", nil, err
		}
		target := filepath.Join(temp, filepath.FromSlash(name))
		if !skillWithinPath(temp, target) {
			os.RemoveAll(temp)
			return "", nil, errors.New("ZIP entry escapes its extraction directory")
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				os.RemoveAll(temp)
				return "", nil, err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			os.RemoveAll(temp)
			return "", nil, errors.New("symbolic links are not allowed in skill archives")
		}
		total += int64(entry.UncompressedSize64)
		if total > maxSkillArchiveUncompressed {
			os.RemoveAll(temp)
			return "", nil, errors.New("skill archive expands beyond the allowed size")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			os.RemoveAll(temp)
			return "", nil, err
		}
		in, err := entry.Open()
		if err != nil {
			os.RemoveAll(temp)
			return "", nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err == nil {
			_, err = io.CopyN(out, in, int64(entry.UncompressedSize64)+1)
		}
		_ = in.Close()
		_ = out.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			os.RemoveAll(temp)
			return "", nil, err
		}
	}
	items, err := discoverSkillRoots(temp)
	if err != nil || len(items) == 0 {
		os.RemoveAll(temp)
		if err != nil {
			return "", nil, err
		}
		return "", nil, errors.New("ZIP does not contain a valid SKILL.md")
	}
	// A nested wrapper is harmless. If a skill contains another SKILL.md, the
	// parent owns that subtree and is the install unit, matching Pi's discovery.
	roots := make([]discoveredSkill, 0, len(items))
	for _, item := range items {
		nested := false
		for _, other := range items {
			if item.root != other.root && skillWithinPath(other.root, item.root) {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, item)
		}
	}
	return temp, roots, nil
}

func downloadSkillURL(rawURL string) ([]byte, error) {
	u := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return nil, errors.New("skill URL must use http:// or https://")
	}
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many URL redirects")
		}
		return nil
	}}
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("download skill archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download skill archive returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillArchiveBytes {
		return nil, fmt.Errorf("downloaded archive is larger than %d MB", maxSkillArchiveBytes>>20)
	}
	return data, nil
}

func (a *App) PreviewSkillArchive(input SkillArchiveInput) (SkillPreview, error) {
	data, err := archiveBytes(input)
	if err != nil {
		return SkillPreview{}, err
	}
	temp, items, err := extractSkillArchive(data)
	if err != nil {
		return SkillPreview{}, err
	}
	defer os.RemoveAll(temp)
	first := items[0]
	return SkillPreview{Name: first.Name, Description: first.Description, Path: first.Path, Count: len(items)}, nil
}

func (a *App) PreviewSkillURL(rawURL string) (SkillPreview, error) {
	data, err := downloadSkillURL(rawURL)
	if err != nil {
		return SkillPreview{}, err
	}
	return a.PreviewSkillArchive(SkillArchiveInput{Data: base64.StdEncoding.EncodeToString(data)})
}

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
	return a.ListSkills()
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

func containsAgentByID(list []SkillAgent, id string) bool {
	for _, agent := range list {
		if agent.ID == id {
			return true
		}
	}
	return false
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
	return a.ListSkills()
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

func (a *App) DeleteSkill(skillID string) ([]SkillInfo, error) {
	item, _, err := findSkillByID(a, skillID)
	if err != nil {
		return nil, err
	}
	if item.SourceType == "pi" {
		for _, agent := range item.Agents {
			if _, err := a.skillAgent(agent.ID); err == nil {
				a.UninstallAgentExtension(AgentExtensionKeyRequest{AgentID: agent.ID, Key: item.Source})
			}
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
