package service

import (
	"context"
	"encoding/json"
	"strings"

	"codingto/internal/documentbridge/model"
)

func (s *Service) search(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		DocumentID string `json:"documentId"`
		Query      string `json:"query"`
		MaxChars   int    `json:"maxChars"`
	}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if err := requireDocumentID(params.DocumentID); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(params.Query)
	if query == "" || len([]rune(query)) > 500 {
		return nil, model.Error("bad_request", "query 不能为空且不能超过 500 个字符", nil)
	}
	metadata, err := s.Store.Metadata(params.DocumentID)
	if err != nil {
		return nil, err
	}
	maxChars := boundedMaxChars(params.MaxChars)
	used := 0
	hits := []map[string]any{}
	truncated := false
	needle := []rune(strings.ToLower(query))
	err = s.scanBlocks(ctx, params.DocumentID, func(block model.Block) (bool, error) {
		index := runeIndex([]rune(strings.ToLower(block.Text)), needle)
		if index < 0 {
			return true, nil
		}
		contextText := searchContext(block.Text, index, len(needle))
		cost := len(contextText)
		if len(hits) >= 100 || used+cost > maxChars {
			truncated = true
			return false, nil
		}
		hits = append(hits, map[string]any{
			"page": block.Page, "pageKind": metadata.PageKind, "blockId": block.ID,
			"text": block.Text, "context": contextText, "type": block.Type,
		})
		used += cost
		return true, nil
	})
	if err != nil {
		return nil, mapOperationError(err, "parse_failed", "搜索解析产物失败")
	}
	return map[string]any{"documentId": params.DocumentID, "query": query, "hits": hits, "truncated": truncated}, nil
}

func runeIndex(value, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(value) {
		return -1
	}
	for start := 0; start <= len(value)-len(needle); start++ {
		match := true
		for offset := range needle {
			if value[start+offset] != needle[offset] {
				match = false
				break
			}
		}
		if match {
			return start
		}
	}
	return -1
}

func searchContext(text string, runePosition, queryRunes int) string {
	runes := []rune(text)
	start := max(0, runePosition-120)
	end := min(len(runes), runePosition+queryRunes+120)
	return string(runes[start:end])
}
