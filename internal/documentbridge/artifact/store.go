package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/ocr"
	"codingto/internal/documentbridge/policy"
)

var documentIDPattern = regexp.MustCompile(`^doc_[0-9a-f]{24}$`)
var nodeIDPattern = regexp.MustCompile(`^turn-[0-9]+$`)

type Store struct {
	SessionDir string
	Root       string
	ObjectsDir string
	StagingDir string
	OCR        ocr.Engine
	mu         sync.Mutex
}

func NewStore(sessionDir string) (*Store, error) {
	root := filepath.Join(sessionDir, ".document-bridge")
	store := &Store{
		SessionDir: sessionDir,
		Root:       root,
		ObjectsDir: filepath.Join(root, "objects"),
		StagingDir: filepath.Join(root, "staging"),
		OCR:        ocr.Discover(),
	}
	for _, directory := range []string{store.ObjectsDir, store.StagingDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	entries, _ := os.ReadDir(store.StagingDir)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "req-") {
			_ = os.RemoveAll(filepath.Join(store.StagingDir, entry.Name()))
		}
	}
	return store, nil
}

func (s *Store) Identity(path string, kind model.FileKind) (documentID, contentHash string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	content := sha256.New()
	identity := sha256.New()
	written, err := io.Copy(io.MultiWriter(content, identity), io.LimitReader(file, policy.MaxInputBytes+1))
	if err != nil {
		return "", "", err
	}
	if written > policy.MaxInputBytes {
		return "", "", model.Error("file_too_large", "文件在读取时增长并超过 50MB 限制", nil)
	}
	contentHash = hex.EncodeToString(content.Sum(nil))
	_, _ = identity.Write([]byte{0})
	_, _ = identity.Write([]byte(model.ParserSchemaVersion))
	_, _ = identity.Write([]byte{0})
	_, _ = identity.Write([]byte(kind))
	documentID = "doc_" + hex.EncodeToString(identity.Sum(nil))[:24]
	return documentID, contentHash, nil
}

func (s *Store) Exists(documentID string) bool {
	if !documentIDPattern.MatchString(documentID) {
		return false
	}
	info, err := os.Stat(filepath.Join(s.ObjectsDir, documentID, "metadata.json"))
	return err == nil && info.Mode().IsRegular()
}

func (s *Store) Begin(requestID string) (*Writer, string, error) {
	safeID := regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(requestID, "_")
	if safeID == "" {
		safeID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	dir := filepath.Join(s.StagingDir, "req-"+safeID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, "", err
	}
	writer, err := NewWriter(dir, s.OCR)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", err
	}
	return writer, dir, nil
}

func (s *Store) Commit(stagingDir, documentID string) error {
	if !documentIDPattern.MatchString(documentID) {
		return fmt.Errorf("invalid document id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target := filepath.Join(s.ObjectsDir, documentID)
	if _, err := os.Stat(target); err == nil {
		return os.RemoveAll(stagingDir)
	}
	if err := os.Rename(stagingDir, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			return os.RemoveAll(stagingDir)
		}
		return err
	}
	return s.updateIndex(documentID)
}

func (s *Store) RemoveStaging(stagingDir string) {
	relative, err := filepath.Rel(s.StagingDir, stagingDir)
	if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		_ = os.RemoveAll(stagingDir)
	}
}

func (s *Store) Metadata(documentID string) (model.Metadata, error) {
	var metadata model.Metadata
	if !documentIDPattern.MatchString(documentID) {
		return metadata, model.Error("document_not_found", "文档不存在", nil)
	}
	raw, err := os.ReadFile(filepath.Join(s.ObjectsDir, documentID, "metadata.json"))
	if os.IsNotExist(err) {
		return metadata, model.Error("document_not_found", "文档不存在", nil)
	}
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func (s *Store) ObjectDir(documentID string) (string, error) {
	if !s.Exists(documentID) {
		return "", model.Error("document_not_found", "文档不存在", nil)
	}
	return filepath.Join(s.ObjectsDir, documentID), nil
}

func (s *Store) activeNodeID() (string, error) {
	nodeRaw, err := os.ReadFile(filepath.Join(s.SessionDir, ".active-change-node"))
	if err != nil {
		return "", model.Error("change_node_unavailable", "当前会话没有活动的变更节点", err)
	}
	nodeID := strings.TrimSpace(string(nodeRaw))
	if !nodeIDPattern.MatchString(nodeID) {
		return "", model.Error("change_node_unavailable", "当前变更节点无效", nil)
	}
	return nodeID, nil
}

// WriteCreatedArtifact snapshots a file produced by the create action into the
// active change node, so generated documents appear as node artifacts and are
// removed together with the session. The workspace original is left untouched.
func (s *Store) WriteCreatedArtifact(sourcePath string) (string, error) {
	nodeID, err := s.activeNodeID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.SessionDir, "artifacts", "output", nodeID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.Base(sourcePath))
	if err := copyRegularFile(sourcePath, target); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(s.SessionDir, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

// copyRegularFile replaces target with the content of sourcePath using a
// temp-then-rename write, so a re-created document updates its node snapshot
// atomically.
func copyRegularFile(sourcePath, target string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("created artifact is not a regular file: %s", sourcePath)
	}
	temp := target + ".tmp"
	output, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, source)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func (s *Store) WriteNodeRef(documentID, sourcePath string, kind model.FileKind) (string, error) {
	nodeID, err := s.activeNodeID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.SessionDir, "artifacts", "document", nodeID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	objectRel := filepath.ToSlash(filepath.Join(".document-bridge", "objects", documentID))
	parsedRel := filepath.ToSlash(filepath.Join(objectRel, MarkdownArtifactName))
	parsedPath := filepath.Join(s.ObjectsDir, documentID, MarkdownArtifactName)
	if info, err := os.Stat(parsedPath); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("parsed markdown artifact is unavailable for %s", documentID)
	}
	sourceRel, err := s.openableNodeSource(dir, documentID, sourcePath, kind)
	if err != nil {
		return "", err
	}
	ref := model.DocumentRef{
		Kind: "document", DocumentID: documentID, ObjectPath: objectRel,
		ParsedArtifact: parsedRel, SourceArtifact: sourceRel,
		Name: parsedArtifactDisplayName(sourcePath), CreatedAt: time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, documentID+".json")
	temp := target + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temp, target); err != nil {
		return "", err
	}
	relative, _ := filepath.Rel(s.SessionDir, target)
	return filepath.ToSlash(relative), nil
}

func parsedArtifactDisplayName(sourcePath string) string {
	base := filepath.Base(sourcePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.TrimSpace(stem) == "" {
		stem = "document"
	}
	return stem + ".md"
}

// openableNodeSource returns a session-relative artifact path that the desktop
// can safely open with the operating system. Uploaded inputs already have an
// immutable archived copy and can be reused when they have a suffix. Workspace
// files and suffix-less inputs are snapshotted beside the node reference using
// an extension derived from the validated document kind.
func (s *Store) openableNodeSource(nodeDir, documentID, sourcePath string, kind model.FileKind) (string, error) {
	extension := documentExtension(sourcePath, kind)
	if relative, ok := relativeArtifactPath(s.SessionDir, sourcePath); ok && filepath.Ext(sourcePath) != "" {
		return filepath.ToSlash(relative), nil
	}

	target := filepath.Join(nodeDir, documentID+extension)
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		relative, _ := filepath.Rel(s.SessionDir, target)
		return filepath.ToSlash(relative), nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open document source for node artifact: %w", err)
	}
	defer source.Close()

	temp := target + ".tmp"
	output, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create document node artifact: %w", err)
	}
	_, copyErr := io.Copy(output, source)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		if copyErr != nil {
			return "", copyErr
		}
		if syncErr != nil {
			return "", syncErr
		}
		return "", closeErr
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	relative, _ := filepath.Rel(s.SessionDir, target)
	return filepath.ToSlash(relative), nil
}

func relativeArtifactPath(sessionDir, candidate string) (string, bool) {
	artifactsRoot := filepath.Join(sessionDir, "artifacts")
	relative, err := filepath.Rel(artifactsRoot, candidate)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join("artifacts", relative), true
}

func documentExtension(sourcePath string, kind model.FileKind) string {
	if extension := strings.ToLower(filepath.Ext(sourcePath)); extension != "" {
		return extension
	}
	switch kind {
	case model.KindPDF:
		return ".pdf"
	case model.KindDOCX:
		return ".docx"
	case model.KindXLSX:
		return ".xlsx"
	case model.KindCSV:
		return ".csv"
	case model.KindMD:
		return ".md"
	case model.KindImage:
		file, err := os.Open(sourcePath)
		if err == nil {
			defer file.Close()
			header := make([]byte, 512)
			count, _ := file.Read(header)
			switch http.DetectContentType(header[:count]) {
			case "image/jpeg":
				return ".jpg"
			case "image/gif":
				return ".gif"
			case "image/webp":
				return ".webp"
			case "image/bmp":
				return ".bmp"
			case "image/tiff":
				return ".tiff"
			}
		}
		return ".png"
	default:
		return ".txt"
	}
}

func (s *Store) updateIndex(documentID string) error {
	index := map[string]string{}
	indexPath := filepath.Join(s.Root, "index.json")
	if raw, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(raw, &index)
	}
	metadata, err := s.Metadata(documentID)
	if err != nil {
		return err
	}
	index[metadata.ContentSHA256] = documentID
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	temp := indexPath + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, indexPath)
}
