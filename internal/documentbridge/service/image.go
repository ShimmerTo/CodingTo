package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"codingto/internal/documentbridge/model"
)

func (s *Service) image(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		DocumentID string `json:"documentId"`
		ImageID    string `json:"imageId"`
		BlockID    string `json:"blockId"`
	}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if err := requireDocumentID(params.DocumentID); err != nil {
		return nil, err
	}
	if params.ImageID == "" && params.BlockID == "" {
		return nil, model.Error("bad_request", "image 需要 imageId 或 blockId；示例：{\"action\":\"image\",\"documentId\":\"doc_...\",\"imageId\":\"img1\"}", nil)
	}
	metadata, err := s.Store.Metadata(params.DocumentID)
	if err != nil {
		return nil, err
	}
	var selected *model.MediaMeta
	for index := range metadata.Images {
		image := &metadata.Images[index]
		if params.ImageID != "" && image.ID == params.ImageID || params.BlockID != "" && image.BlockID == params.BlockID {
			selected = image
			break
		}
	}
	if selected == nil && params.BlockID != "" {
		var imageID string
		_ = s.scanBlocks(ctx, params.DocumentID, func(block model.Block) (bool, error) {
			if block.ID == params.BlockID {
				imageID = block.Ref
				return false, nil
			}
			return true, nil
		})
		for index := range metadata.Images {
			if metadata.Images[index].ID == imageID {
				selected = &metadata.Images[index]
				break
			}
		}
	}
	if selected == nil {
		return nil, model.Error("image_not_found", "图片不存在", nil)
	}
	artifactPath := filepath.ToSlash(filepath.Join(
		".document-bridge", "objects", params.DocumentID, "media", selected.ID+selected.Ext,
	))
	warnings := []string{}
	for _, warning := range metadata.OCRWarnings {
		if strings.HasPrefix(warning, selected.ID+":") {
			warnings = append(warnings, warning)
		}
	}
	return map[string]any{
		"documentId": params.DocumentID, "imageId": selected.ID, "blockId": selected.BlockID,
		"mime": selected.MIME, "width": selected.Width, "height": selected.Height,
		"size": selected.Size, "page": selected.Page, "artifactPath": artifactPath,
		"ocrText": selected.OCRText, "ocrEngine": metadata.Capabilities.OCR,
		"ocrWarnings": warnings,
	}, nil
}
