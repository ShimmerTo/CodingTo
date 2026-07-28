package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func collectDocumentArtifacts(root, sessionDir string) []DocumentArtifactRef {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	result := []DocumentArtifactRef{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ref DocumentArtifactRef
		if json.Unmarshal(raw, &ref) != nil || ref.Kind != "document" ||
			!strings.HasPrefix(ref.DocumentID, "doc_") {
			continue
		}
		relative, err := filepath.Rel(sessionDir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		ref.RelPath = filepath.ToSlash(relative)
		ref.AbsPath = path
		if ref.Name == "" {
			ref.Name = entry.Name()
		}
		if parsedPath, parsedRelative, ok := resolveDocumentParsedArtifact(sessionDir, ref.ParsedArtifact); ok {
			ref.RelPath = parsedRelative
			ref.AbsPath = parsedPath
			if ref.Name == entry.Name() {
				ref.Name = filepath.Base(parsedPath)
			}
		} else if sourcePath, sourceRelative, ok := resolveDocumentSourceArtifact(sessionDir, ref.SourceArtifact); ok {
			ref.RelPath = sourceRelative
			ref.AbsPath = sourcePath
			ref.Name = filepath.Base(sourcePath)
		}
		result = append(result, ref)
	}
	return result
}

// resolveDocumentParsedArtifact accepts only the canonical Markdown file below
// the immutable Document Bridge object cache. Media references in that Markdown
// stay relative to the sibling media directory.
func resolveDocumentParsedArtifact(sessionDir, parsedArtifact string) (string, string, bool) {
	return resolveDocumentArtifact(
		sessionDir,
		filepath.Join(sessionDir, ".document-bridge", "objects"),
		parsedArtifact,
	)
}

// resolveDocumentSourceArtifact resolves both new node snapshots and references
// written by older versions. Only regular, non-symlink files below the session
// artifact root are returned, matching OpenSessionArtifact's trust boundary.
func resolveDocumentSourceArtifact(sessionDir, sourceArtifact string) (string, string, bool) {
	return resolveDocumentArtifact(
		sessionDir,
		filepath.Join(sessionDir, "artifacts"),
		sourceArtifact,
	)
}

func resolveDocumentArtifact(sessionDir, allowedRoot, artifactPath string) (string, string, bool) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", "", false
	}
	candidate := filepath.FromSlash(artifactPath)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(sessionDir, candidate)
	}
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(allowedRoot, candidate)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", false
	}
	sessionRelative, err := filepath.Rel(sessionDir, candidate)
	if err != nil {
		return "", "", false
	}
	return candidate, filepath.ToSlash(sessionRelative), true
}
