package piagent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"codingto/internal/extensions"
)

var npmPackageSpecPattern = regexp.MustCompile(`^(@?[^@]+(?:/[^@]+)?)(?:@.+)?$`)

type piPackageSettings struct {
	Packages []json.RawMessage `json:"packages"`
}

type piPackageManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
}

// InstalledPackageStatuses returns every package configured in the agent's Pi
// settings. This is the authoritative inventory written by `pi install`; using
// it keeps the UI independent from a hard-coded list of known extensions.
func InstalledPackageStatuses(dataDir string) ([]extensions.Status, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if os.IsNotExist(err) {
		return []extensions.Status{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Pi package settings: %w", err)
	}

	var settings piPackageSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("parse Pi package settings: %w", err)
	}

	statuses := make([]extensions.Status, 0, len(settings.Packages))
	seen := make(map[string]bool, len(settings.Packages))
	for _, entry := range settings.Packages {
		source := packageSource(entry)
		if source == "" || seen[source] {
			continue
		}
		seen[source] = true

		status := extensions.Status{
			Key:         source,
			Name:        packageDisplayName(source),
			Description: source,
			InstallHint: source,
			Enabled:     true,
		}
		if installedPath, resolvable := packageInstalledPath(dataDir, source); resolvable {
			status.SourcePath = installedPath
			if info, statErr := os.Stat(installedPath); statErr == nil {
				status.Installed = info.IsDir() || info.Mode().IsRegular()
				applyPackageManifest(&status, installedPath)
			} else if !os.IsNotExist(statErr) {
				status.Error = statErr.Error()
			} else {
				status.Error = "configured package is missing from disk"
			}
		} else {
			// Pi supports additional source forms over time. Keep configured
			// packages visible even when this application cannot resolve their
			// on-disk path yet; settings.json remains Pi's source of truth.
			status.Installed = true
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func packageSource(raw json.RawMessage) string {
	var source string
	if json.Unmarshal(raw, &source) == nil {
		return strings.TrimSpace(source)
	}
	var filtered struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(raw, &filtered) == nil {
		return strings.TrimSpace(filtered.Source)
	}
	return ""
}

func packageDisplayName(source string) string {
	if name, ok := npmPackageName(source); ok {
		return name
	}
	trimmed := strings.TrimSuffix(strings.TrimSpace(source), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 && index+1 < len(trimmed) {
		return trimmed[index+1:]
	}
	return trimmed
}

func npmPackageName(source string) (string, bool) {
	if !strings.HasPrefix(source, "npm:") {
		return "", false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(source, "npm:"))
	match := npmPackageSpecPattern.FindStringSubmatch(spec)
	if len(match) < 2 || match[1] == "" {
		return "", false
	}
	return match[1], true
}

func packageInstalledPath(dataDir, source string) (string, bool) {
	if name, ok := npmPackageName(source); ok {
		return filepath.Join(dataDir, "npm", "node_modules", filepath.FromSlash(name)), true
	}
	if host, repoPath, ok := gitPackageIdentity(source); ok {
		root := filepath.Join(dataDir, "git")
		candidate := filepath.Join(root, host, filepath.FromSlash(repoPath))
		if pathWithin(root, candidate) {
			return candidate, true
		}
		return "", false
	}

	local := source
	if strings.HasPrefix(local, "file:") {
		local = strings.TrimPrefix(local, "file:")
	}
	if filepath.IsAbs(local) {
		return filepath.Clean(local), true
	}
	if strings.HasPrefix(local, ".") {
		return filepath.Join(dataDir, filepath.FromSlash(local)), true
	}
	return "", false
}

func gitPackageIdentity(source string) (string, string, bool) {
	value := strings.TrimSpace(strings.TrimPrefix(source, "git:"))
	if value == "" {
		return "", "", false
	}

	host := ""
	repoPath := ""
	if strings.HasPrefix(value, "git@") {
		withoutUser := strings.TrimPrefix(value, "git@")
		parts := strings.SplitN(withoutUser, ":", 2)
		if len(parts) != 2 {
			return "", "", false
		}
		host, repoPath = parts[0], parts[1]
	} else if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		host, repoPath = parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/")
	} else {
		parts := strings.SplitN(value, "/", 2)
		if len(parts) != 2 || (!strings.Contains(parts[0], ".") && parts[0] != "localhost") {
			return "", "", false
		}
		host, repoPath = parts[0], parts[1]
	}

	// Pi stores a ref after @/# outside the repository path.
	if index := strings.Index(repoPath, "#"); index >= 0 {
		repoPath = repoPath[:index]
	}
	if index := strings.LastIndex(repoPath, "@"); index >= 0 {
		repoPath = repoPath[:index]
	}
	repoPath = strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git")
	if host == "" || repoPath == "" || strings.Contains(host, `\`) {
		return "", "", false
	}
	for _, part := range strings.Split(filepath.ToSlash(repoPath), "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	return host, filepath.ToSlash(repoPath), true
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func applyPackageManifest(status *extensions.Status, installedPath string) {
	manifestPath := installedPath
	if info, err := os.Stat(installedPath); err == nil && info.IsDir() {
		manifestPath = filepath.Join(installedPath, "package.json")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var manifest piPackageManifest
	if json.Unmarshal(raw, &manifest) != nil {
		return
	}
	if strings.TrimSpace(manifest.Name) != "" {
		status.Name = manifest.Name
	}
	status.Version = strings.TrimSpace(manifest.Version)
	if strings.TrimSpace(manifest.Description) != "" {
		status.Description = manifest.Description
	}
	status.Homepage = strings.TrimSpace(manifest.Homepage)
}
