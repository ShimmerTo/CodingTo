package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"codingto/internal/subagentbridge"
	"github.com/pmezard/go-difflib/difflib"
)

const (
	changeSnapshotDir  = "changes"
	changeNodesDir     = "nodes"
	changeManifestFile = "manifest.json"
	changeCaptureDir   = "captures"
	changeCapturePaths = "paths"
	maxDiffFileSize    = 5 * 1024 * 1024
)

var changeTrackerMu sync.Mutex

// changeNodeManifest is one user prompt and the edits performed before the
// matching agent_end event. A mandatory Pi extension captures before/after
// snapshots inside blocking tool hooks; Go compacts that journal when the node
// ends so a later prompt cannot change an earlier node's diff.
type changeNodeManifest struct {
	Version   int                    `json:"version"`
	ID        string                 `json:"id"`
	Root      string                 `json:"root"`
	Prompt    string                 `json:"prompt"`
	StartedAt int64                  `json:"startedAt"`
	EndedAt   int64                  `json:"endedAt,omitempty"`
	Status    string                 `json:"status"`
	Files     map[string]trackedFile `json:"files"`
	Subagents []SubagentRunRef       `json:"subagentRuns,omitempty"`
}

type trackedFile struct {
	Before fileSnapshot `json:"before"`
	After  fileSnapshot `json:"after"`
}

type fileSnapshot struct {
	Exists bool   `json:"exists"`
	Hash   string `json:"hash,omitempty"`
	Size   int64  `json:"size"`
	Text   bool   `json:"text"`
	Blob   string `json:"blob,omitempty"`
}

type capturePathMeta struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
}

type captureSnapshotRecord struct {
	Version    int          `json:"version"`
	RecordedAt int64        `json:"recordedAt"`
	Final      bool         `json:"final,omitempty"`
	Snapshot   fileSnapshot `json:"snapshot"`
}

type SessionChanges struct {
	Root    string       `json:"root"`
	Nodes   []ChangeNode `json:"nodes"`
	Files   []FileChange `json:"files"`
	Added   int          `json:"added"`
	Deleted int          `json:"deleted"`
}

type ChangeNode struct {
	ID               string            `json:"id"`
	Prompt           string            `json:"prompt"`
	StartedAt        int64             `json:"startedAt"`
	EndedAt          int64             `json:"endedAt,omitempty"`
	Status           string            `json:"status"`
	Files            []FileChange      `json:"files"`
	Added            int               `json:"added"`
	Deleted          int               `json:"deleted"`
	BrowserArtifacts []BrowserArtifact `json:"browserArtifacts,omitempty"`
	// InputArtifacts are files uploaded by the user for this turn, archived under
	// <sessionDir>/artifacts/input/<nodeID>/. The directory on disk is the
	// source of truth; this slice is populated by readSessionChanges.
	InputArtifacts []ArtifactRef `json:"inputArtifacts,omitempty"`
	// DocumentArtifacts are immutable references created by Document Bridge for
	// this turn. The parsed object itself remains in the session-level cache.
	DocumentArtifacts []DocumentArtifactRef `json:"documentArtifacts,omitempty"`
	// OutputArtifacts are files produced by the document create action during
	// this turn, snapshotted under artifacts/output/<nodeID>/.
	OutputArtifacts   []OutputArtifact      `json:"outputArtifacts,omitempty"`
	SubagentRuns      []SubagentRunRef      `json:"subagentRuns,omitempty"`
	SubagentArtifacts []SubagentArtifactRef `json:"subagentArtifacts,omitempty"`
}

type SubagentRunRef struct {
	RunID        string   `json:"runId"`
	AgentKey     string   `json:"agentKey"`
	AgentName    string   `json:"agentName,omitempty"`
	ParentNodeID string   `json:"parentNodeId"`
	ToolCallID   string   `json:"toolCallId"`
	ChildNodeIDs []string `json:"childNodeIds"`
	Status       string   `json:"status"`
}

type ChangeSource struct {
	AgentKey string `json:"agentKey"`
	RunID    string `json:"runId"`
	NodeID   string `json:"nodeId,omitempty"`
}

type SubagentArtifactRef struct {
	Source  ChangeSource `json:"source"`
	RelPath string       `json:"relPath"`
	AbsPath string       `json:"absPath"`
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Size    int64        `json:"size"`
}

type DocumentArtifactRef struct {
	Kind           string `json:"kind"`
	DocumentID     string `json:"documentId"`
	ObjectPath     string `json:"objectPath"`
	ParsedArtifact string `json:"parsedArtifact"`
	SourceArtifact string `json:"sourceArtifact"`
	CreatedAt      string `json:"createdAt"`
	RelPath        string `json:"relPath"`
	AbsPath        string `json:"absPath"`
	Name           string `json:"name"`
}

// BrowserArtifact is a file produced by Pi Agent Browser Native during a browser
// task, recorded under the change node that triggered it.
type BrowserArtifact struct {
	RelPath    string `json:"relPath"`
	AbsPath    string `json:"absPath"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
}

func browserArtifactKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".pdf":
		return "pdf"
	case ".md", ".markdown":
		return "markdown"
	default:
		return "download"
	}
}

// OutputArtifact is a file produced by the document create action, snapshotted
// under artifacts/output/<nodeID>/ by Document Bridge. It shares the browser
// artifact shape so the frontend can render both groups identically.
type OutputArtifact = BrowserArtifact

// collectBrowserArtifacts walks the per-node browser artifact directory and
// returns every regular file, scoped safely within the session artifact root.
func collectBrowserArtifacts(root string) []BrowserArtifact {
	return collectProducedArtifacts(root, "browser")
}

func collectOutputArtifacts(root string) []OutputArtifact {
	return collectProducedArtifacts(root, "output")
}

func collectProducedArtifacts(root, group string) []BrowserArtifact {
	var out []BrowserArtifact
	if root == "" {
		return out
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return out
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(rootAbs, p)
		if err != nil {
			return nil
		}
		out = append(out, BrowserArtifact{
			RelPath:    filepath.ToSlash(filepath.Join("artifacts", group, filepath.Base(rootAbs), rel)),
			AbsPath:    p,
			Name:       d.Name(),
			Kind:       browserArtifactKind(d.Name()),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UnixMilli(),
		})
		return nil
	})
	return out
}

type FileChange struct {
	Path    string        `json:"path"`
	Status  string        `json:"status"`
	Added   int           `json:"added"`
	Deleted int           `json:"deleted"`
	Binary  bool          `json:"binary"`
	Hunks   []DiffHunk    `json:"hunks"`
	Source  *ChangeSource `json:"source,omitempty"`
}

// ChangeSummary is the lightweight, durable completion payload attached to an
// agent_end event. The full hunks remain in GetSessionChanges; chat history only
// needs enough information to render the per-turn file list and navigate back
// to the corresponding node in the right sidebar.
type ChangeSummary struct {
	NodeID  string              `json:"nodeId"`
	Status  string              `json:"status"`
	Files   []FileChangeSummary `json:"files"`
	Added   int                 `json:"added"`
	Deleted int                 `json:"deleted"`
}

type FileChangeSummary struct {
	Path    string        `json:"path"`
	Status  string        `json:"status"`
	Added   int           `json:"added"`
	Deleted int           `json:"deleted"`
	Binary  bool          `json:"binary"`
	Source  *ChangeSource `json:"source,omitempty"`
}

type DiffHunk struct {
	Header string     `json:"header"`
	Lines  []DiffLine `json:"lines"`
}

type DiffLine struct {
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	OldNumber int    `json:"oldNumber,omitempty"`
	NewNumber int    `json:"newNumber,omitempty"`
}

func (a *App) GetSessionChanges(id int64) (SessionChanges, error) {
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return SessionChanges{}, err
	}
	if !ok {
		return SessionChanges{}, fmt.Errorf("conversation not found: %d", id)
	}
	changes, err := readSessionChanges(item.SessionDir)
	if err != nil {
		return SessionChanges{}, err
	}
	workspace := a.sessionWorkspace(item.EnvironmentID, changes.Root)
	if workspace != "" {
		changes.Root = workspace
	}
	return changes, nil
}

func readChangeSummary(sessionDir, nodeID string) (ChangeSummary, error) {
	changeTrackerMu.Lock()
	defer changeTrackerMu.Unlock()

	manifest, err := readChangeNodeManifest(sessionDir, nodeID)
	if err != nil {
		return ChangeSummary{}, err
	}
	if manifest.Status == "running" {
		if err := mergeChangeCaptureJournal(changeNodeDir(sessionDir, nodeID), &manifest, false); err != nil {
			return ChangeSummary{}, err
		}
	}
	summary := ChangeSummary{
		NodeID: manifest.ID, Status: manifest.Status,
		Files: []FileChangeSummary{},
	}
	paths := make([]string, 0, len(manifest.Files))
	for path := range manifest.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file, changed, err := buildFileChange(
			changeNodeDir(sessionDir, manifest.ID),
			path,
			manifest.Files[path],
		)
		if err != nil {
			return ChangeSummary{}, err
		}
		if !changed {
			continue
		}
		summary.Files = append(summary.Files, FileChangeSummary{
			Path: file.Path, Status: file.Status, Added: file.Added,
			Deleted: file.Deleted, Binary: file.Binary, Source: file.Source,
		})
		summary.Added += file.Added
		summary.Deleted += file.Deleted
	}
	if len(manifest.Subagents) == 0 {
		manifest.Subagents = discoverSubagentRuns(sessionDir, manifest.ID)
	}
	childNode := ChangeNode{
		ID: manifest.ID, Files: []FileChange{},
		SubagentRuns: append([]SubagentRunRef(nil), manifest.Subagents...),
	}
	childSession := SessionChanges{Files: []FileChange{}}
	appendSubagentChanges(sessionDir, manifest.Subagents, &childNode, &childSession)
	for _, file := range childNode.Files {
		summary.Files = append(summary.Files, FileChangeSummary{
			Path: file.Path, Status: file.Status, Added: file.Added,
			Deleted: file.Deleted, Binary: file.Binary, Source: file.Source,
		})
		summary.Added += file.Added
		summary.Deleted += file.Deleted
	}
	return summary, nil
}

func beginChangeNode(sessionDir, root, prompt string, startedAt int64) (string, error) {
	changeTrackerMu.Lock()
	defer changeTrackerMu.Unlock()

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	id := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	manifest := changeNodeManifest{
		Version: 2, ID: id, Root: absoluteRoot, Prompt: prompt,
		StartedAt: startedAt, Status: "running", Files: map[string]trackedFile{},
	}
	if err := writeChangeNodeManifest(sessionDir, manifest); err != nil {
		return "", err
	}
	return id, nil
}

func finishChangeNode(sessionDir, nodeID, status string, endedAt int64) error {
	if nodeID == "" {
		return nil
	}
	changeTrackerMu.Lock()
	defer changeTrackerMu.Unlock()
	manifest, err := readChangeNodeManifest(sessionDir, nodeID)
	if err != nil {
		return err
	}
	if err := mergeChangeCaptureJournal(changeNodeDir(sessionDir, nodeID), &manifest, true); err != nil {
		return err
	}
	if endedAt <= 0 {
		endedAt = time.Now().UnixMilli()
	}
	manifest.EndedAt = endedAt
	manifest.Status = status
	manifest.Subagents = discoverSubagentRuns(sessionDir, nodeID)
	return writeChangeNodeManifest(sessionDir, manifest)
}

func readSessionChanges(sessionDir string) (SessionChanges, error) {
	changeTrackerMu.Lock()
	defer changeTrackerMu.Unlock()

	result := SessionChanges{Nodes: []ChangeNode{}, Files: []FileChange{}}
	nodesRoot := filepath.Join(sessionDir, changeSnapshotDir, changeNodesDir)
	entries, err := os.ReadDir(nodesRoot)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return SessionChanges{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readChangeNodeManifest(sessionDir, entry.Name())
		if err != nil {
			continue
		}
		if manifest.Status == "running" {
			if err := mergeChangeCaptureJournal(changeNodeDir(sessionDir, manifest.ID), &manifest, false); err != nil {
				return SessionChanges{}, err
			}
		}
		if len(manifest.Subagents) == 0 {
			manifest.Subagents = discoverSubagentRuns(sessionDir, manifest.ID)
		}
		if result.Root == "" {
			result.Root = manifest.Root
		}
		node := ChangeNode{
			ID: manifest.ID, Prompt: manifest.Prompt, StartedAt: manifest.StartedAt,
			EndedAt: manifest.EndedAt, Status: manifest.Status, Files: []FileChange{},
			SubagentRuns: append([]SubagentRunRef(nil), manifest.Subagents...),
		}
		paths := make([]string, 0, len(manifest.Files))
		for path := range manifest.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			change, changed, err := buildFileChange(changeNodeDir(sessionDir, manifest.ID), path, manifest.Files[path])
			if err != nil {
				return SessionChanges{}, err
			}
			if !changed {
				continue
			}
			node.Added += change.Added
			node.Deleted += change.Deleted
			node.Files = append(node.Files, change)
			result.Files = append(result.Files, change)
		}
		node.BrowserArtifacts = collectBrowserArtifacts(filepath.Join(sessionDir, "artifacts", "browser", manifest.ID))
		refs, err := collectInputArtifacts(filepath.Join(sessionDir, "artifacts", "input", manifest.ID), sessionDir)
		if err != nil {
			return SessionChanges{}, fmt.Errorf("collect input artifacts for %s: %w", manifest.ID, err)
		}
		if len(refs) > 0 {
			node.InputArtifacts = refs
		}
		node.DocumentArtifacts = collectDocumentArtifacts(
			filepath.Join(sessionDir, "artifacts", "document", manifest.ID),
			sessionDir,
		)
		node.OutputArtifacts = collectOutputArtifacts(filepath.Join(sessionDir, "artifacts", "output", manifest.ID))
		appendSubagentChanges(sessionDir, manifest.Subagents, &node, &result)
		result.Added += node.Added
		result.Deleted += node.Deleted
		result.Nodes = append(result.Nodes, node)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].StartedAt > result.Nodes[j].StartedAt })
	return result, nil
}

func discoverSubagentRuns(sessionDir, parentNodeID string) []SubagentRunRef {
	root := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(root)
	if err != nil {
		return []SubagentRunRef{}
	}
	result := []SubagentRunRef{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := subagentbridge.ReadRunRecord(filepath.Join(root, entry.Name(), "run.json"))
		if err != nil || record.RunID != entry.Name() || record.ParentNodeID != parentNodeID {
			continue
		}
		result = append(result, SubagentRunRef{
			RunID: record.RunID, AgentKey: record.AgentKey, AgentName: record.AgentName,
			ParentNodeID: record.ParentNodeID, ToolCallID: record.ToolCallID,
			ChildNodeIDs: append([]string(nil), record.ChildNodeIDs...), Status: record.Status,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RunID < result[j].RunID })
	return result
}

func appendSubagentChanges(sessionDir string, refs []SubagentRunRef, node *ChangeNode, result *SessionChanges) {
	for index := range refs {
		ref := refs[index]
		runDir := filepath.Join(sessionDir, "subagents", ref.RunID)
		record, err := subagentbridge.ReadRunRecord(filepath.Join(runDir, "run.json"))
		if err == nil {
			node.SubagentRuns[index].Status = record.Status
			ref.Status = record.Status
		}
		for _, childNodeID := range ref.ChildNodeIDs {
			manifest, err := readChangeNodeManifest(runDir, childNodeID)
			if err != nil {
				continue
			}
			childDir := changeNodeDir(runDir, childNodeID)
			if err := mergeChangeCaptureJournal(childDir, &manifest, false); err != nil {
				continue
			}
			// Persist the compacted child manifest in its own run directory so
			// later reads never depend on the parent node's blob directory.
			_ = writeChangeNodeManifest(runDir, manifest)
			paths := make([]string, 0, len(manifest.Files))
			for path := range manifest.Files {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				change, changed, err := buildFileChange(childDir, path, manifest.Files[path])
				if err != nil || !changed {
					continue
				}
				change.Source = &ChangeSource{AgentKey: ref.AgentKey, RunID: ref.RunID, NodeID: childNodeID}
				node.Added += change.Added
				node.Deleted += change.Deleted
				node.Files = append(node.Files, change)
				result.Files = append(result.Files, change)
			}
		}
		if err != nil {
			continue
		}
		for _, file := range record.Files {
			if file.Change != "artifact" {
				continue
			}
			relative, relErr := filepath.Rel(sessionDir, file.Path)
			if relErr != nil {
				continue
			}
			node.SubagentArtifacts = append(node.SubagentArtifacts, SubagentArtifactRef{
				Source:  ChangeSource{AgentKey: ref.AgentKey, RunID: ref.RunID},
				RelPath: filepath.ToSlash(relative), AbsPath: file.Path,
				Name: filepath.Base(file.Path), Kind: file.Kind, Size: file.Bytes,
			})
		}
	}
}

func mergeChangeCaptureJournal(nodeDir string, manifest *changeNodeManifest, finalize bool) error {
	pathsRoot := filepath.Join(nodeDir, changeCaptureDir, changeCapturePaths)
	entries, err := os.ReadDir(pathsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read change capture paths: %w", err)
	}
	if manifest.Files == nil {
		manifest.Files = map[string]trackedFile{}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		captureDir := filepath.Join(pathsRoot, entry.Name())
		meta, before, ok, err := readCaptureBefore(captureDir)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		absolutePath, relativePath, err := resolveTrackedPath(manifest.Root, meta.Path)
		if err != nil {
			return fmt.Errorf("resolve captured path %q: %w", meta.Path, err)
		}
		if changeCapturePathKey(relativePath) != entry.Name() {
			return fmt.Errorf("change capture path key mismatch for %s", relativePath)
		}
		if err := validateSnapshot(before.Snapshot); err != nil {
			return fmt.Errorf("validate before snapshot for %s: %w", relativePath, err)
		}

		after := before.Snapshot
		if finalize {
			after, err = captureFileSnapshot(nodeDir, absolutePath)
			if err != nil {
				return fmt.Errorf("finalize captured path %s: %w", relativePath, err)
			}
		} else if latest, found, latestErr := readLatestCaptureAfter(captureDir); latestErr != nil {
			return latestErr
		} else if found {
			if err := validateSnapshot(latest.Snapshot); err != nil {
				return fmt.Errorf("validate after snapshot for %s: %w", relativePath, err)
			}
			after = latest.Snapshot
		}
		// The in-process tool_call hook is authoritative for the original
		// baseline. It deliberately replaces any legacy stdout-based snapshot
		// that may have raced and captured the already-modified file.
		manifest.Files[relativePath] = trackedFile{Before: before.Snapshot, After: after}
	}
	return nil
}

func readCaptureBefore(captureDir string) (capturePathMeta, captureSnapshotRecord, bool, error) {
	var meta capturePathMeta
	metaRaw, err := os.ReadFile(filepath.Join(captureDir, "meta.json"))
	if os.IsNotExist(err) {
		return meta, captureSnapshotRecord{}, false, nil
	}
	if err != nil {
		return meta, captureSnapshotRecord{}, false, fmt.Errorf("read change capture metadata: %w", err)
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return meta, captureSnapshotRecord{}, false, fmt.Errorf("parse change capture metadata: %w", err)
	}
	if meta.Version != 1 || strings.TrimSpace(meta.Path) == "" {
		return meta, captureSnapshotRecord{}, false, fmt.Errorf("unsupported change capture metadata")
	}

	var before captureSnapshotRecord
	beforeRaw, err := os.ReadFile(filepath.Join(captureDir, "before.json"))
	if os.IsNotExist(err) {
		return meta, before, false, nil
	}
	if err != nil {
		return meta, before, false, fmt.Errorf("read before change capture for %s: %w", meta.Path, err)
	}
	if err := json.Unmarshal(beforeRaw, &before); err != nil {
		return meta, before, false, fmt.Errorf("parse before change capture for %s: %w", meta.Path, err)
	}
	if before.Version != 1 {
		return meta, before, false, fmt.Errorf("unsupported before change capture version for %s", meta.Path)
	}
	return meta, before, true, nil
}

func readLatestCaptureAfter(captureDir string) (captureSnapshotRecord, bool, error) {
	afterRoot := filepath.Join(captureDir, "after")
	entries, err := os.ReadDir(afterRoot)
	if os.IsNotExist(err) {
		return captureSnapshotRecord{}, false, nil
	}
	if err != nil {
		return captureSnapshotRecord{}, false, fmt.Errorf("read after change captures: %w", err)
	}
	var latest captureSnapshotRecord
	latestName := ""
	found := false
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(afterRoot, entry.Name()))
		if err != nil {
			return captureSnapshotRecord{}, false, fmt.Errorf("read after change capture %s: %w", entry.Name(), err)
		}
		var candidate captureSnapshotRecord
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return captureSnapshotRecord{}, false, fmt.Errorf("parse after change capture %s: %w", entry.Name(), err)
		}
		if candidate.Version != 1 {
			return captureSnapshotRecord{}, false, fmt.Errorf("unsupported after change capture version in %s", entry.Name())
		}
		if !found || candidate.RecordedAt > latest.RecordedAt ||
			(candidate.RecordedAt == latest.RecordedAt && entry.Name() > latestName) {
			latest = candidate
			latestName = entry.Name()
			found = true
		}
	}
	return latest, found, nil
}

func validateSnapshot(snapshot fileSnapshot) error {
	if snapshot.Blob == "" {
		return nil
	}
	if filepath.Base(snapshot.Blob) != snapshot.Blob || snapshot.Hash == "" || snapshot.Blob != snapshot.Hash+".txt" {
		return fmt.Errorf("invalid snapshot blob %q", snapshot.Blob)
	}
	return nil
}

func changeCapturePathKey(relativePath string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(relativePath)))
	return hex.EncodeToString(sum[:])
}

func buildFileChange(nodeDir, path string, tracked trackedFile) (FileChange, bool, error) {
	if snapshotsEqual(tracked.Before, tracked.After) {
		return FileChange{}, false, nil
	}
	change := FileChange{Path: path, Hunks: []DiffHunk{}}
	switch {
	case !tracked.Before.Exists && tracked.After.Exists:
		change.Status = "added"
	case tracked.Before.Exists && !tracked.After.Exists:
		change.Status = "deleted"
	default:
		change.Status = "modified"
	}
	change.Binary = (tracked.Before.Exists && !tracked.Before.Text) || (tracked.After.Exists && !tracked.After.Text)
	if change.Binary {
		return change, true, nil
	}
	before, err := readSnapshotText(nodeDir, tracked.Before)
	if err != nil {
		return FileChange{}, false, err
	}
	after, err := readSnapshotText(nodeDir, tracked.After)
	if err != nil {
		return FileChange{}, false, err
	}
	change.Hunks, change.Added, change.Deleted = makeDiff(before, after)
	return change, true, nil
}

func readSnapshotText(nodeDir string, snapshot fileSnapshot) (string, error) {
	if !snapshot.Exists || snapshot.Blob == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(nodeDir, "blobs", snapshot.Blob))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func snapshotsEqual(before, after fileSnapshot) bool {
	return before.Exists == after.Exists && before.Hash == after.Hash && before.Size == after.Size
}

func captureFileSnapshot(nodeDir, path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileSnapshot{Text: true}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	snapshot := fileSnapshot{Exists: true, Hash: hash, Size: int64(len(data))}
	if len(data) > maxDiffFileSize || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return snapshot, nil
	}
	snapshot.Text = true
	snapshot.Blob = hash + ".txt"
	blobDir := filepath.Join(nodeDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return fileSnapshot{}, err
	}
	blobPath := filepath.Join(blobDir, snapshot.Blob)
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, data, 0o600); err != nil {
			return fileSnapshot{}, err
		}
	}
	return snapshot, nil
}

func resolveTrackedPath(root, toolPath string) (string, string, error) {
	path := filepath.FromSlash(strings.TrimSpace(toolPath))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	relativePath, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return "", "", err
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("edited file is outside the active workspace: %s", toolPath)
	}
	return filepath.Clean(absolutePath), filepath.ToSlash(relativePath), nil
}

func changeNodeDir(sessionDir, nodeID string) string {
	return filepath.Join(sessionDir, changeSnapshotDir, changeNodesDir, nodeID)
}

func readChangeNodeManifest(sessionDir, nodeID string) (changeNodeManifest, error) {
	data, err := os.ReadFile(filepath.Join(changeNodeDir(sessionDir, nodeID), changeManifestFile))
	if err != nil {
		return changeNodeManifest{}, err
	}
	var manifest changeNodeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return changeNodeManifest{}, err
	}
	if manifest.Files == nil {
		manifest.Files = map[string]trackedFile{}
	}
	return manifest, nil
}

func writeChangeNodeManifest(sessionDir string, manifest changeNodeManifest) error {
	nodeDir := changeNodeDir(sessionDir, manifest.ID)
	if err := os.MkdirAll(nodeDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(nodeDir, changeManifestFile)
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempPath, path)
}

func makeDiff(before, after string) ([]DiffHunk, int, int) {
	a := difflib.SplitLines(before)
	b := difflib.SplitLines(after)
	matcher := difflib.NewMatcher(a, b)
	added, deleted := 0, 0
	for _, op := range matcher.GetOpCodes() {
		switch op.Tag {
		case 'i':
			added += op.J2 - op.J1
		case 'd':
			deleted += op.I2 - op.I1
		case 'r':
			added += op.J2 - op.J1
			deleted += op.I2 - op.I1
		}
	}
	hunks := make([]DiffHunk, 0)
	for _, group := range matcher.GetGroupedOpCodes(3) {
		if len(group) == 0 {
			continue
		}
		first, last := group[0], group[len(group)-1]
		hunk := DiffHunk{
			Header: fmt.Sprintf("@@ -%s +%s @@", formatDiffRange(first.I1, last.I2), formatDiffRange(first.J1, last.J2)),
			Lines:  []DiffLine{},
		}
		for _, op := range group {
			switch op.Tag {
			case 'e':
				for offset, line := range a[op.I1:op.I2] {
					hunk.Lines = append(hunk.Lines, DiffLine{Kind: "context", Text: trimDiffLine(line), OldNumber: op.I1 + offset + 1, NewNumber: op.J1 + offset + 1})
				}
			case 'd':
				for offset, line := range a[op.I1:op.I2] {
					hunk.Lines = append(hunk.Lines, DiffLine{Kind: "deleted", Text: trimDiffLine(line), OldNumber: op.I1 + offset + 1})
				}
			case 'i':
				for offset, line := range b[op.J1:op.J2] {
					hunk.Lines = append(hunk.Lines, DiffLine{Kind: "added", Text: trimDiffLine(line), NewNumber: op.J1 + offset + 1})
				}
			case 'r':
				for offset, line := range a[op.I1:op.I2] {
					hunk.Lines = append(hunk.Lines, DiffLine{Kind: "deleted", Text: trimDiffLine(line), OldNumber: op.I1 + offset + 1})
				}
				for offset, line := range b[op.J1:op.J2] {
					hunk.Lines = append(hunk.Lines, DiffLine{Kind: "added", Text: trimDiffLine(line), NewNumber: op.J1 + offset + 1})
				}
			}
		}
		hunks = append(hunks, hunk)
	}
	return hunks, added, deleted
}

func formatDiffRange(start, stop int) string {
	count := stop - start
	begin := start + 1
	if count == 0 {
		begin = start
	}
	if count == 1 {
		return fmt.Sprintf("%d", begin)
	}
	return fmt.Sprintf("%d,%d", begin, count)
}

func trimDiffLine(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}
