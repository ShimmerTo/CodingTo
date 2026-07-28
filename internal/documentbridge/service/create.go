package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/policy"
)

type createParams struct {
	Format    string          `json:"format"`
	Path      string          `json:"path"`
	Overwrite bool            `json:"overwrite"`
	Content   json.RawMessage `json:"content"`
	Paths     []string        `json:"paths"`
}

var createFormatExt = map[string]string{
	"md":   ".md",
	"txt":  ".txt",
	"csv":  ".csv",
	"html": ".html",
	"json": ".json",
	"rtf":  ".rtf",
	"docx": ".docx",
	"xlsx": ".xlsx",
	"pdf":  ".pdf",
	"zip":  ".zip",
}

func (s *Service) create(ctx context.Context, raw json.RawMessage) (any, error) {
	var p createParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(p.Format))
	if _, ok := createFormatExt[format]; !ok {
		return nil, model.Error("bad_request", "不支持的格式："+format, nil)
	}
	outputPath := strings.TrimSpace(p.Path)
	if outputPath == "" {
		return nil, model.Error("bad_request", "缺少 path", nil)
	}
	wantExt := createFormatExt[format]
	ext := strings.ToLower(filepath.Ext(outputPath))
	if ext != "" && ext != wantExt {
		return nil, model.Error("bad_request", fmt.Sprintf("扩展名 %s 与格式 %s 不匹配", ext, format), nil)
	}
	if ext == "" {
		outputPath += wantExt
	}
	abs, err := s.Policy.ResolveOutput(outputPath)
	if err != nil {
		return nil, err
	}

	var data []byte
	switch format {
	case "md":
		data, err = buildMarkdown(p.Content)
	case "txt":
		data, err = buildText(p.Content)
	case "csv":
		data, err = buildCsv(p.Content)
	case "html":
		data, err = buildHtml(p.Content)
	case "json":
		data, err = buildJson(p.Content)
	case "rtf":
		data, err = buildRtf(p.Content)
	case "docx":
		data, err = buildDocx(p.Content)
	case "xlsx":
		data, err = buildXlsx(p.Content)
	case "pdf":
		data, err = buildPdf(p.Content)
	case "zip":
		resolved := make([]string, 0, len(p.Paths))
		var total int64
		for _, src := range p.Paths {
			sp, size, resolveErr := s.resolveCreateSource(src)
			if resolveErr != nil {
				return nil, resolveErr
			}
			total += size
			if total > policy.MaxCreateTotalBytes {
				return nil, model.Error("resource_limit", "zip 源文件总大小超过 200MB 限制", nil)
			}
			resolved = append(resolved, sp)
		}
		data, err = buildZip(ctx, resolved)
	}
	if err != nil {
		return nil, model.Error("parse_failed", "生成文件失败："+err.Error(), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, model.Error("canceled", "请求已取消", err)
	}
	if len(data) > policy.MaxCreateOutputBytes {
		return nil, model.Error("resource_limit", "生成文件超过 100MB 限制", nil)
	}
	if err := writeCreatedFile(abs, data, p.Overwrite); err != nil {
		if os.IsExist(err) {
			return nil, model.Error("file_exists", fmt.Sprintf("文件已存在：%s（设置 overwrite=true 覆盖）", outputPath), err)
		}
		return nil, model.Error("internal_error", "写入文件失败", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, model.Error("internal_error", "无法读取生成文件信息", err)
	}
	result := map[string]any{
		"status": "created",
		"format": format,
		"path":   abs,
		"name":   filepath.Base(abs),
		"size":   info.Size(),
	}
	// 生成文件同步归档为节点产物；归档失败（如无活动节点）不影响已创建的文件。
	if rel, snapshotErr := s.Store.WriteCreatedArtifact(abs); snapshotErr == nil {
		result["artifactRef"] = rel
	}
	return result, nil
}

func (s *Service) resolveCreateSource(raw string) (string, int64, error) {
	if strings.TrimSpace(raw) == "" {
		return "", 0, model.Error("bad_request", "zip 源路径不能为空", nil)
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.Policy.WorkDir, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", 0, model.Error("bad_request", "无法解析 zip 源路径", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !withinDir(s.Policy.WorkDir, resolved) {
		return "", 0, model.Error("path_denied", "zip 源路径超出当前工作目录："+raw, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", 0, model.Error("file_not_found", "无法读取 zip 源文件："+raw, err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, model.Error("path_denied", "zip 仅支持普通文件："+raw, nil)
	}
	if info.Size() > policy.MaxCreateSourceBytes {
		return "", 0, model.Error("resource_limit", "zip 单个源文件超过 50MB 限制："+raw, nil)
	}
	return resolved, info.Size(), nil
}

func writeCreatedFile(target string, data []byte, overwrite bool) error {
	temp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if !overwrite {
		return installNewFile(tempPath, target)
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse to overwrite non-regular file")
		}
		backup, backupErr := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".backup-*")
		if backupErr != nil {
			return backupErr
		}
		backupPath := backup.Name()
		if closeErr := backup.Close(); closeErr != nil {
			_ = os.Remove(backupPath)
			return closeErr
		}
		if removeErr := os.Remove(backupPath); removeErr != nil {
			return removeErr
		}
		if renameErr := os.Rename(target, backupPath); renameErr != nil {
			return renameErr
		}
		if renameErr := os.Rename(tempPath, target); renameErr != nil {
			restoreErr := os.Rename(backupPath, target)
			if restoreErr != nil {
				return fmt.Errorf("replace output: %v; restore original: %w", renameErr, restoreErr)
			}
			return renameErr
		}
		if removeErr := os.Remove(backupPath); removeErr != nil {
			// The replacement succeeded; a stale backup is safer than deleting
			// the newly generated file or reporting data loss.
			return fmt.Errorf("output replaced but backup cleanup failed: %w", removeErr)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return installNewFile(tempPath, target)
}

func installNewFile(sourcePath, target string) error {
	if linkErr := os.Link(sourcePath, target); linkErr == nil || os.IsExist(linkErr) {
		return linkErr
	}
	// Some network and removable filesystems do not support hard links.
	// Preserve exclusive-create semantics there, even though the fallback
	// cannot make the file visible in a single rename.
	targetFile, createErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if createErr != nil {
		return createErr
	}
	cleanupTarget := true
	defer func() {
		_ = targetFile.Close()
		if cleanupTarget {
			_ = os.Remove(target)
		}
	}()
	source, openErr := os.Open(sourcePath)
	if openErr != nil {
		return openErr
	}
	_, copyErr := io.Copy(targetFile, source)
	closeSourceErr := source.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeSourceErr != nil {
		return closeSourceErr
	}
	if syncErr := targetFile.Sync(); syncErr != nil {
		return syncErr
	}
	if closeErr := targetFile.Close(); closeErr != nil {
		return closeErr
	}
	cleanupTarget = false
	return nil
}

func withinDir(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
