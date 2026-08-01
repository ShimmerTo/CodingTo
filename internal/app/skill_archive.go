package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxSkillArchiveBytes        = 50 << 20
	maxSkillArchiveUncompressed = 200 << 20
	maxSkillArchiveEntries      = 5000
)

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
