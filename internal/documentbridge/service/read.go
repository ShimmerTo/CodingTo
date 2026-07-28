package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/policy"
)

func (s *Service) read(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		DocumentID string `json:"documentId"`
		PageStart  int    `json:"pageStart"`
		PageEnd    int    `json:"pageEnd"`
		MaxChars   int    `json:"maxChars"`
	}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if err := requireDocumentID(params.DocumentID); err != nil {
		return nil, err
	}
	metadata, err := s.Store.Metadata(params.DocumentID)
	if err != nil {
		return nil, err
	}
	if params.PageStart <= 0 {
		params.PageStart = 1
	}
	if params.PageEnd <= 0 {
		params.PageEnd = min(metadata.Pages, params.PageStart+4)
	}
	if params.PageEnd < params.PageStart {
		return nil, model.Error("bad_request", "pageEnd 不能小于 pageStart", nil)
	}
	maxChars := boundedMaxChars(params.MaxChars)
	var content strings.Builder
	var citations []map[string]any
	truncated := false
	err = s.scanBlocks(ctx, params.DocumentID, func(block model.Block) (bool, error) {
		if block.Page < params.PageStart || block.Page > params.PageEnd {
			return true, nil
		}
		line := fmt.Sprintf("[page %d | %s | %s] %s\n", block.Page, block.ID, block.Type, block.Text)
		remaining := maxChars - content.Len()
		if remaining <= 0 {
			truncated = true
			return false, nil
		}
		if len(line) > remaining {
			content.WriteString(truncateUTF8(line, remaining))
			truncated = true
			return false, nil
		}
		content.WriteString(line)
		citations = append(citations, map[string]any{"page": block.Page, "blockId": block.ID})
		return true, nil
	})
	if err != nil {
		return nil, mapOperationError(err, "parse_failed", "读取解析产物失败")
	}
	images := []map[string]any{}
	for _, image := range metadata.Images {
		if image.Page >= params.PageStart && image.Page <= params.PageEnd {
			images = append(images, map[string]any{
				"imageId": image.ID, "blockId": image.BlockID, "page": image.Page,
				"hasOCRText": image.OCRText != "",
			})
		}
	}
	return map[string]any{
		"documentId": params.DocumentID, "pageStart": params.PageStart, "pageEnd": params.PageEnd,
		"pageKind": metadata.PageKind, "content": strings.TrimSpace(content.String()),
		"citations": citations, "images": images, "truncated": truncated,
	}, nil
}

func (s *Service) scanBlocks(ctx context.Context, documentID string, visit func(model.Block) (bool, error)) error {
	objectDir, err := s.Store.ObjectDir(documentID)
	if err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(objectDir, "blocks.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), policy.MaxBlockBytes+64*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var block model.Block
		if err := json.Unmarshal(scanner.Bytes(), &block); err != nil {
			return err
		}
		keepGoing, err := visit(block)
		if err != nil {
			return err
		}
		if !keepGoing {
			return nil
		}
	}
	return scanner.Err()
}

func boundedMaxChars(value int) int {
	if value <= 0 {
		return 30_000
	}
	return min(value, policy.MaxResultChars)
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}
