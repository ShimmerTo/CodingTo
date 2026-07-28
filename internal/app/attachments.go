package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// AttachmentLimits are the guard rails from
// docs/design/附件上传、输入产物与多模态传递设计.md §6.
const (
	attachmentMaxBytes      int64 = 50 * 1024 * 1024
	attachmentMaxCount            = 10
	attachmentMaxTotalBytes int64 = 100 * 1024 * 1024
)

// ArtifactRef is a single archived attachment on disk (design §4.3). It is the
// shared record for input/document artifacts and is eventually reused by
// BrowserArtifact so every artifact type resolves to one structure.
type ArtifactRef struct {
	RelPath    string `json:"relPath"` // path relative to sessionDir
	AbsPath    string `json:"absPath"` // absolute path on disk
	Name       string `json:"name"`    // sanitized file name
	Kind       string `json:"kind"`    // image/audio/video/document/other
	Mime       string `json:"mime"`    // detected from file header / extension
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	ModifiedAt int64  `json:"modifiedAt"` // unix millis
}

// AttachmentInput carries one attachment from the Wails boundary into a prompt.
// Exactly one of Path (native file picker) or Data (drag-drop base64) is set.
type AttachmentInput struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

var windowsReservedArtifactNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {}, "com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {}, "lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// detectArtifactKind maps a MIME type to one of the coarse kinds used by the
// attachment manifest and the model multimodal pipeline.
func detectArtifactKind(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case strings.HasPrefix(mimeType, "text/"), isDocumentMime(mimeType):
		return "document"
	default:
		return "other"
	}
}

func isDocumentMime(mimeType string) bool {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "application/pdf"),
		strings.HasPrefix(lower, "application/msword"),
		strings.HasPrefix(lower, "application/vnd."),
		strings.Contains(lower, "word"),
		strings.Contains(lower, "excel"),
		strings.Contains(lower, "powerpoint"),
		strings.Contains(lower, "spreadsheet"),
		strings.Contains(lower, "presentation"),
		strings.Contains(lower, "officedocument"),
		strings.Contains(lower, "opendocument"):
		return true
	}
	return false
}

// sanitizeArtifactName keeps a printable basename, drops path separators, and
// bounds the length so it is safe to use as a file name inside the archive.
func sanitizeArtifactName(name string) string {
	base := filepath.Base(name)
	base = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\<>:"|?*`, r) {
			return '_'
		}
		return r
	}, base)
	base = strings.TrimRight(base, " .")
	if base == "" || base == "." || base == ".." {
		base = "attachment"
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if _, reserved := windowsReservedArtifactNames[strings.ToLower(stem)]; reserved {
		base = "_" + base
	}
	runes := []rune(base)
	if len(runes) > 120 {
		ext := filepath.Ext(base)
		keep := 120 - len([]rune(ext))
		if keep < 1 {
			keep = 1
		}
		base = string(runes[:keep]) + ext
	}
	return base
}

// uniqueArtifactName appends -2, -3, ... before the extension to avoid
// clobbering files that share a name within the same batch.
func uniqueArtifactName(base string, seen map[string]struct{}) string {
	if base == "" {
		base = "attachment"
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, suffix, ext)
		}
		key := strings.ToLower(candidate)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		return candidate
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// detectMimeFromContent uses content sniffing as the authority and the file
// extension only to refine generic text, zip-based office, and unknown files.
func detectMimeFromContent(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	header := make([]byte, 512)
	n, _ := io.ReadFull(file, header)
	detected := cleanMime(http.DetectContentType(header[:n]))
	byExtension := cleanMime(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	switch {
	case byExtension != "" && detected == "application/octet-stream" &&
		(strings.HasPrefix(byExtension, "text/") || isDocumentMime(byExtension)):
		return byExtension
	case byExtension != "" && detected == "application/zip" && isDocumentMime(byExtension):
		return byExtension
	case strings.HasPrefix(detected, "text/plain") && strings.HasPrefix(byExtension, "text/"):
		return byExtension
	case detected != "":
		return detected
	case byExtension != "":
		return byExtension
	default:
		return "application/octet-stream"
	}
}

func cleanMime(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err == nil {
		return strings.ToLower(mediaType)
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
}

// openAttachmentSource resolves a single AttachmentInput to a readable file
// path. For Path inputs it rejects symlinks/directories; for Data inputs it
// decodes base64 into a temp file. The returned cleanup removes any temp file.
func openAttachmentSource(in AttachmentInput) (string, func(), error) {
	cleanup := func() {}
	if (in.Path == "") == (in.Data == "") {
		return "", cleanup, errors.New("attachment must contain exactly one of path or data")
	}
	if in.Path != "" {
		resolved, err := filepath.EvalSymlinks(in.Path)
		if err != nil {
			return "", cleanup, fmt.Errorf("resolve %q: %w", in.Name, err)
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			return "", cleanup, fmt.Errorf("stat %q: %w", in.Name, err)
		}
		if !fi.Mode().IsRegular() {
			return "", cleanup, fmt.Errorf("not a regular file: %q", in.Name)
		}
		return resolved, cleanup, nil
	}
	if in.Data != "" {
		maxEncoded := base64.StdEncoding.EncodedLen(int(attachmentMaxBytes)) + 4
		if len(in.Data) > maxEncoded {
			return "", cleanup, fmt.Errorf("attachment %q exceeds %d bytes", in.Name, attachmentMaxBytes)
		}
		tmp, err := os.CreateTemp("", "codingto-att-*.bin")
		if err != nil {
			return "", cleanup, fmt.Errorf("temp %q: %w", in.Name, err)
		}
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(in.Data))
		written, writeErr := io.Copy(tmp, io.LimitReader(decoder, attachmentMaxBytes+1))
		if closeErr := tmp.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			os.Remove(tmp.Name())
			return "", cleanup, fmt.Errorf("decode %q: %w", in.Name, writeErr)
		}
		if written > attachmentMaxBytes {
			os.Remove(tmp.Name())
			return "", cleanup, fmt.Errorf("attachment %q exceeds %d bytes", in.Name, attachmentMaxBytes)
		}
		if _, err := os.Stat(tmp.Name()); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", cleanup, fmt.Errorf("stat temp %q: %w", in.Name, err)
		}
		cleanup = func() { os.Remove(tmp.Name()) }
		return tmp.Name(), cleanup, nil
	}
	return "", cleanup, errors.New("attachment has neither path nor data")
}

func copyFile(src, dst string, maxBytes int64) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(out, io.LimitReader(in, maxBytes+1))
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(dst)
		return 0, copyErr
	}
	if written > maxBytes {
		_ = os.Remove(dst)
		return written, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return written, os.Chmod(dst, 0o600)
}

// promoteDir atomically renames src -> dst. Both paths are deliberately below
// the same session artifact directory, so a cross-filesystem fallback is not
// needed and would weaken the all-or-nothing guarantee.
func promoteDir(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("artifact directory already exists: %s", filepath.Base(dst))
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

// archiveAttachments validates the requested attachments, copies them into a
// staging directory, then atomically promotes the staging dir into the node's
// input artifact directory. It returns ArtifactRefs resolved against the final
// location. On any failure the staging dir is removed and nothing is archived.
func archiveAttachments(sessionDir, nodeID string, inputs []AttachmentInput) ([]ArtifactRef, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > attachmentMaxCount {
		return nil, fmt.Errorf("too many attachments: max %d, got %d", attachmentMaxCount, len(inputs))
	}
	stagingDir := filepath.Join(sessionDir, "artifacts", "input", ".staging", nodeID)
	inputDir := filepath.Join(sessionDir, "artifacts", "input", nodeID)
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("protect staging dir: %w", err)
	}
	names := make(map[string]struct{})
	var total int64
	for _, in := range inputs {
		src, cleanup, err := openAttachmentSource(in)
		if err != nil {
			cleanup()
			_ = os.RemoveAll(stagingDir)
			return nil, err
		}
		remaining := attachmentMaxTotalBytes - total
		if remaining <= 0 {
			cleanup()
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("total attachment size exceeds %d bytes", attachmentMaxTotalBytes)
		}
		name := uniqueArtifactName(sanitizeArtifactName(in.Name), names)
		copyLimit := min(attachmentMaxBytes, remaining)
		written, copyErr := copyFile(src, filepath.Join(stagingDir, name), copyLimit)
		if copyErr != nil {
			cleanup()
			_ = os.RemoveAll(stagingDir)
			if written > remaining {
				return nil, fmt.Errorf("total attachment size exceeds %d bytes", attachmentMaxTotalBytes)
			}
			return nil, fmt.Errorf("copy %q: %w", in.Name, copyErr)
		}
		total += written
		cleanup()
	}
	if err := os.MkdirAll(filepath.Dir(inputDir), 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("create input dir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(inputDir), 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("protect input dir: %w", err)
	}
	if err := promoteDir(stagingDir, inputDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("move attachments: %w", err)
	}
	refs, err := collectInputArtifacts(inputDir, sessionDir)
	if err != nil {
		_ = os.RemoveAll(inputDir)
		return nil, fmt.Errorf("index attachments: %w", err)
	}
	return refs, nil
}

// collectInputArtifacts scans a node's input artifact directory and builds the
// ArtifactRef list. The directory on disk is the source of truth; readSessionChanges
// uses this to enrich ChangeNodes.
func collectInputArtifacts(inputDir, sessionDir string) ([]ArtifactRef, error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	refs := make([]ArtifactRef, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("input artifact must not be a symbolic link: %s", e.Name())
		}
		abs := filepath.Join(inputDir, e.Name())
		fi, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat input artifact %q: %w", e.Name(), err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("input artifact is not a regular file: %s", e.Name())
		}
		mimeType := detectMimeFromContent(abs)
		sha, err := sha256File(abs)
		if err != nil {
			return nil, fmt.Errorf("hash input artifact %q: %w", e.Name(), err)
		}
		rel, err := filepath.Rel(sessionDir, abs)
		if err != nil {
			rel = abs
		}
		refs = append(refs, ArtifactRef{
			RelPath:    filepath.ToSlash(rel),
			AbsPath:    abs,
			Name:       e.Name(),
			Kind:       detectArtifactKind(mimeType),
			Mime:       mimeType,
			Size:       fi.Size(),
			SHA256:     sha,
			ModifiedAt: fi.ModTime().UnixMilli(),
		})
	}
	return refs, nil
}

type attachmentTransferStatus struct {
	Direct  bool
	Ordinal int
	Reason  string
}

// buildAttachmentManifest renders the upload manifest injected into the user
// message (design §9.1).
func buildAttachmentManifest(refs []ArtifactRef, transfers map[string]attachmentTransferStatus) string {
	var b strings.Builder
	b.WriteString("\n用户本次上传了以下本地附件：\n")
	for i, r := range refs {
		status, found := transfers[r.RelPath]
		delivery := ""
		switch {
		case found && status.Direct:
			delivery = fmt.Sprintf("已作为图片 %d 直传模型", status.Ordinal)
		case found && status.Reason != "":
			delivery = "未直传：" + status.Reason
			if r.Kind == "image" {
				delivery += "；如需读取图片文字，请使用 codingto_document inspect/read/image 获取本地 OCR 结果"
			}
		case r.Kind == "document":
			delivery = "未直传：请按需使用 codingto_document 读取归档文件"
		case r.Kind == "audio" || r.Kind == "video":
			delivery = "未直传：当前运行时尚不支持该媒体 content"
		default:
			delivery = "未直传：无可直传模态"
		}
		b.WriteString(fmt.Sprintf(
			"- [附件 %d] %s\n  文件名：%s；类型：%s；大小：%d 字节；%s。\n",
			i+1, r.AbsPath, r.Name, r.Mime, r.Size, delivery,
		))
	}
	return b.String()
}

func appendManifest(message, manifest string) string {
	if strings.TrimSpace(message) == "" {
		return manifest
	}
	return message + manifest
}

// imageAttachmentInputs converts as many archived image artifacts as fit within
// the existing image channel limits. Anything that does not fit is retained as
// a path-only attachment and receives an explicit reason in the manifest.
func imageAttachmentInputs(refs []ArtifactRef, existing []ImageInput, supportsImages bool) ([]ImageInput, map[string]attachmentTransferStatus) {
	out := make([]ImageInput, 0, len(refs))
	statuses := make(map[string]attachmentTransferStatus)
	total := int64(0)
	for _, image := range existing {
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err == nil {
			total += int64(len(decoded))
		}
	}
	count := len(existing)
	ordinal := 0
	for _, r := range refs {
		if r.Kind != "image" {
			continue
		}
		status := attachmentTransferStatus{}
		switch {
		case !supportsImages:
			status.Reason = "当前模型不支持图片输入"
		case count >= attachmentMaxCount:
			status.Reason = fmt.Sprintf("图片数量超过 %d 个直传限制", attachmentMaxCount)
		case total+r.Size > attachmentMaxTotalBytes:
			status.Reason = fmt.Sprintf("图片总大小超过 %d 字节直传限制", attachmentMaxTotalBytes)
		}
		if status.Reason != "" {
			statuses[r.RelPath] = status
			continue
		}
		data, err := os.ReadFile(r.AbsPath)
		if err != nil {
			status.Reason = "读取归档图片失败"
			statuses[r.RelPath] = status
			continue
		}
		count++
		ordinal++
		total += int64(len(data))
		out = append(out, ImageInput{
			Name:     r.Name,
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(data),
			MimeType: r.Mime,
		})
		status.Direct = true
		status.Ordinal = ordinal
		statuses[r.RelPath] = status
	}
	return out, statuses
}
