package policy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"codingto/internal/documentbridge/model"
)

const (
	MaxInputBytes        = 50 * 1024 * 1024
	MaxCreateOutputBytes = 100 * 1024 * 1024
	MaxCreateSourceBytes = 50 * 1024 * 1024
	MaxCreateTotalBytes  = 200 * 1024 * 1024
	MaxZipEntryBytes     = 64 * 1024 * 1024
	MaxZipExpandedBytes  = 512 * 1024 * 1024
	MaxBlocks            = 200_000
	MaxBlockBytes        = 1024 * 1024
	MaxMediaBytes        = 20 * 1024 * 1024
	MaxMediaTotalBytes   = 200 * 1024 * 1024
	MaxResultChars       = 50 * 1024
	MaxSheetRows         = 500
)

type Policy struct {
	SessionDir string
	WorkDir    string
	roots      []string
}

// ResolveOutput constrains a new output path to WorkDir. Every existing path
// component is checked with Lstat and EvalSymlinks before new directories are
// created, so a symlink or Windows junction cannot redirect the write outside
// the workspace.
func (p *Policy) ResolveOutput(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", model.Error("bad_request", "缺少输出路径", nil)
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(p.WorkDir, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil || !within(p.WorkDir, absolute) || absolute == p.WorkDir {
		return "", model.Error("path_denied", "输出路径必须位于当前工作目录内", err)
	}

	parent, err := p.ensureOutputDir(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, filepath.Base(absolute))
	if !within(p.WorkDir, target) {
		return "", model.Error("path_denied", "输出路径超出当前工作目录", nil)
	}
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", model.Error("path_denied", "输出文件不能是符号链接", nil)
		}
		resolved, resolveErr := filepath.EvalSymlinks(target)
		if resolveErr != nil || !within(p.WorkDir, resolved) {
			return "", model.Error("path_denied", "输出文件真实路径超出当前工作目录", resolveErr)
		}
		if !info.Mode().IsRegular() {
			return "", model.Error("path_denied", "输出目标必须是普通文件", nil)
		}
	} else if !os.IsNotExist(statErr) {
		return "", model.Error("path_denied", "无法检查输出文件", statErr)
	}
	return target, nil
}

func (p *Policy) ensureOutputDir(parent string) (string, error) {
	relative, err := filepath.Rel(p.WorkDir, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", model.Error("path_denied", "输出目录超出当前工作目录", err)
	}
	current := p.WorkDir
	if relative == "." {
		return current, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		next := filepath.Join(current, component)
		info, statErr := os.Lstat(next)
		if os.IsNotExist(statErr) {
			if mkdirErr := os.Mkdir(next, 0o755); mkdirErr != nil && !os.IsExist(mkdirErr) {
				return "", model.Error("internal_error", "无法创建输出目录", mkdirErr)
			}
			info, statErr = os.Lstat(next)
		}
		if statErr != nil {
			return "", model.Error("path_denied", "无法检查输出目录", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", model.Error("path_denied", "输出目录不能包含符号链接或非目录项", nil)
		}
		resolved, resolveErr := filepath.EvalSymlinks(next)
		if resolveErr != nil || !within(p.WorkDir, resolved) {
			return "", model.Error("path_denied", "输出目录真实路径超出当前工作目录", resolveErr)
		}
		current = resolved
	}
	return current, nil
}

func New(sessionDir, workDir string) (*Policy, error) {
	sessionDir, err := absoluteExistingDir(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("session directory: %w", err)
	}
	workDir, err = absoluteExistingDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("work directory: %w", err)
	}
	inputRoot := filepath.Join(sessionDir, "artifacts", "input")
	_ = os.MkdirAll(inputRoot, 0o700)
	inputRoot, err = filepath.EvalSymlinks(inputRoot)
	if err != nil {
		return nil, fmt.Errorf("input artifact root: %w", err)
	}
	return &Policy{
		SessionDir: sessionDir,
		WorkDir:    workDir,
		roots:      []string{filepath.Clean(inputRoot), filepath.Clean(workDir)},
	}, nil
}

func (p *Policy) Resolve(raw string) (model.Source, error) {
	if strings.TrimSpace(raw) == "" {
		return model.Source{}, model.Error("bad_request", "inspect 缺少 path；示例：{\"action\":\"inspect\",\"path\":\"C:/path/demo.pdf\"}", nil)
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.WorkDir, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return model.Source{}, model.Error("bad_request", "无法解析文件路径", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		return model.Source{}, model.Error("file_not_found", "文件不存在", err)
	}
	if err != nil {
		return model.Source{}, model.Error("path_denied", "无法解析文件真实路径", err)
	}
	allowed := false
	for _, root := range p.roots {
		if within(root, resolved) {
			allowed = true
			break
		}
	}
	if !allowed {
		return model.Source{}, model.Error("path_denied", "只允许读取当前工作目录或本会话输入附件目录中的文件", nil)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return model.Source{}, model.Error("file_not_found", "无法读取文件", err)
	}
	if !info.Mode().IsRegular() {
		return model.Source{}, model.Error("path_denied", "只允许读取普通文件", nil)
	}
	if info.Size() > MaxInputBytes {
		return model.Source{}, model.Error("file_too_large", "文件超过 50MB 限制", nil)
	}
	kind, err := DetectKind(resolved)
	if err != nil {
		return model.Source{}, err
	}
	return model.Source{Path: resolved, Kind: kind, Size: info.Size()}, nil
}

func DetectKind(path string) (model.FileKind, error) {
	ext := strings.ToLower(filepath.Ext(path))
	file, err := os.Open(path)
	if err != nil {
		return "", model.Error("file_not_found", "无法读取文件头", err)
	}
	header := make([]byte, 16)
	count, readErr := io.ReadFull(file, header)
	_ = file.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return "", model.Error("parse_failed", "无法读取文件头", readErr)
	}
	header = header[:count]
	switch ext {
	case ".pdf":
		if !bytes.HasPrefix(header, []byte("%PDF-")) {
			return "", model.Error("unsupported_format", "文件扩展名为 PDF，但文件头不是 PDF", nil)
		}
		return model.KindPDF, nil
	case ".docx":
		if !bytes.HasPrefix(header, []byte("PK")) {
			return "", model.Error("unsupported_format", "文件扩展名为 DOCX，但文件头不是 ZIP/OOXML", nil)
		}
		return model.KindDOCX, nil
	case ".xlsx":
		if !bytes.HasPrefix(header, []byte("PK")) {
			return "", model.Error("unsupported_format", "文件扩展名为 XLSX，但文件头不是 ZIP/OOXML", nil)
		}
		return model.KindXLSX, nil
	case ".csv":
		return model.KindCSV, nil
	case ".txt":
		return model.KindText, nil
	case ".md", ".markdown":
		return model.KindMD, nil
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		if !imageHeader(header) {
			return "", model.Error("unsupported_format", "图片扩展名与文件头不匹配或图片格式不受支持", nil)
		}
		return model.KindImage, nil
	default:
		return "", model.Error("unsupported_format", "仅支持 PDF、DOCX、XLSX、CSV、TXT、MD 和常见图片格式", nil)
	}
}

func imageHeader(header []byte) bool {
	return bytes.HasPrefix(header, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) ||
		bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}) ||
		bytes.HasPrefix(header, []byte("GIF87a")) ||
		bytes.HasPrefix(header, []byte("GIF89a")) ||
		len(header) >= 12 && bytes.HasPrefix(header, []byte("RIFF")) && string(header[8:12]) == "WEBP" ||
		bytes.HasPrefix(header, []byte("BM")) ||
		bytes.HasPrefix(header, []byte{'I', 'I', 42, 0}) ||
		bytes.HasPrefix(header, []byte{'M', 'M', 0, 42})
}

func within(root, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		(runtime.GOOS != "windows" || !filepath.IsAbs(rel))
}

func absoluteExistingDir(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return filepath.Clean(resolved), nil
}
