package parser

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	pathpkg "path"
	"strings"

	"codingto/internal/documentbridge/policy"
)

func ValidateZIP(ctx context.Context, path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) > 20_000 {
		return fmt.Errorf("archive contains too many entries")
	}
	var declared, actual int64
	for _, entry := range archive.File {
		if err := checkContext(ctx); err != nil {
			return err
		}
		portableName := strings.ReplaceAll(entry.Name, `\`, "/")
		clean := pathpkg.Clean(portableName)
		if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") ||
			len(clean) >= 2 && clean[1] == ':' {
			return fmt.Errorf("archive entry escapes the document root: %s", entry.Name)
		}
		if entry.UncompressedSize64 > policy.MaxZipEntryBytes {
			return fmt.Errorf("archive entry exceeds 64MB: %s", entry.Name)
		}
		declared += int64(entry.UncompressedSize64)
		if declared > policy.MaxZipExpandedBytes {
			return fmt.Errorf("archive expanded size exceeds 512MB")
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		count, copyErr := io.Copy(io.Discard, io.LimitReader(reader, policy.MaxZipEntryBytes+1))
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if count > policy.MaxZipEntryBytes {
			return fmt.Errorf("archive entry expands beyond 64MB: %s", entry.Name)
		}
		actual += count
		if actual > policy.MaxZipExpandedBytes {
			return fmt.Errorf("archive actual expanded size exceeds 512MB")
		}
	}
	return nil
}
