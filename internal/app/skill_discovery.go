package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"codingto/internal/piagent"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

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
