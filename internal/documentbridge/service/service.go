package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"codingto/internal/documentbridge/artifact"
	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/parser"
	"codingto/internal/documentbridge/policy"
)

type Service struct {
	Policy   *policy.Policy
	Store    *artifact.Store
	Registry *parser.Registry
}

func New(sessionDir, workDir string) (*Service, error) {
	filePolicy, err := policy.New(sessionDir, workDir)
	if err != nil {
		return nil, err
	}
	store, err := artifact.NewStore(filePolicy.SessionDir)
	if err != nil {
		return nil, err
	}
	return &Service{Policy: filePolicy, Store: store, Registry: parser.DefaultRegistry()}, nil
}

func (s *Service) Handle(ctx context.Context, requestID, action string, raw json.RawMessage) (any, error) {
	switch action {
	case "inspect":
		return s.inspect(ctx, requestID, raw)
	case "read":
		return s.read(ctx, raw)
	case "search":
		return s.search(ctx, raw)
	case "sheet":
		return s.sheet(ctx, raw)
	case "image":
		return s.image(ctx, raw)
	case "create":
		return s.create(ctx, raw)
	default:
		return nil, model.Error("bad_request", "未知 action；可用值为 inspect、read、search、sheet、image、create", nil)
	}
}

func (s *Service) inspect(ctx context.Context, requestID string, raw json.RawMessage) (any, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	source, err := s.Policy.Resolve(params.Path)
	if err != nil {
		return nil, err
	}
	documentID, contentHash, err := s.Store.Identity(source.Path, source.Kind)
	if err != nil {
		return nil, model.Error("parse_failed", "无法计算文档内容指纹", err)
	}
	cached := s.Store.Exists(documentID)
	if !cached {
		if source.Kind == model.KindDOCX || source.Kind == model.KindXLSX {
			if err := parser.ValidateZIP(ctx, source.Path); err != nil {
				return nil, mapOperationError(err, "resource_limit", "压缩文档超过资源限制或结构无效")
			}
		}
		documentParser, err := s.Registry.For(source.Kind)
		if err != nil {
			return nil, model.Error("unsupported_format", err.Error(), err)
		}
		writer, stagingDir, err := s.Store.Begin(requestID)
		if err != nil {
			return nil, model.Error("internal_error", "无法创建解析暂存目录", err)
		}
		defer func() {
			writer.Close()
			s.Store.RemoveStaging(stagingDir)
		}()
		summary, parseErr := documentParser.Parse(ctx, source, writer)
		if parseErr != nil {
			return nil, mapOperationError(parseErr, "parse_failed", "文档解析失败")
		}
		verifiedID, verifiedHash, verifyErr := s.Store.Identity(source.Path, source.Kind)
		if verifyErr != nil || verifiedID != documentID || verifiedHash != contentHash {
			return nil, model.Error("parse_failed", "文件在解析过程中发生变化，请重试", verifyErr)
		}
		metadata := model.Metadata{
			DocumentID: documentID, Type: source.Kind, SourceName: fileName(source.Path),
			SourcePath: source.Path, ContentSHA256: contentHash, ParserSchema: model.ParserSchemaVersion,
			Size: source.Size, Pages: summary.Pages, PageKind: summary.PageKind,
			HasTable: summary.HasTable, HasImage: summary.HasImage,
			Capabilities: summary.Capabilities, CreatedAt: time.Now().UTC(),
		}
		if summary.HasImage {
			metadata.Capabilities.OCR = s.Store.OCR.Name()
		}
		if err := writer.Finalize(&metadata); err != nil {
			return nil, mapOperationError(err, "resource_limit", "写入解析产物失败")
		}
		if err := s.Store.Commit(stagingDir, documentID); err != nil {
			return nil, model.Error("internal_error", "提交解析产物失败", err)
		}
	}
	artifactRef, err := s.Store.WriteNodeRef(documentID, source.Path, source.Kind)
	if err != nil {
		return nil, err
	}
	metadata, err := s.Store.Metadata(documentID)
	if err != nil {
		return nil, err
	}
	return inspectResult(metadata, cached, artifactRef), nil
}

func inspectResult(metadata model.Metadata, cached bool, artifactRef string) map[string]any {
	sheets := make([]map[string]any, 0, len(metadata.Sheets))
	for _, sheet := range metadata.Sheets {
		sheets = append(sheets, map[string]any{"name": sheet.Name, "rows": sheet.Rows, "cols": sheet.Cols})
	}
	return map[string]any{
		"documentId": metadata.DocumentID, "type": metadata.Type, "size": metadata.Size,
		"pages": metadata.Pages, "pageKind": metadata.PageKind, "blocks": metadata.Blocks,
		"hasTable": metadata.HasTable, "hasImage": metadata.HasImage, "sheets": sheets,
		"images": len(metadata.Images), "capabilities": metadata.Capabilities,
		"ocrWarnings": metadata.OCRWarnings, "cached": cached, "artifactRef": artifactRef,
	}
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(target); err != nil {
		return model.Error("bad_request", "参数无效："+err.Error(), err)
	}
	return nil
}

func mapOperationError(err error, code, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return model.Error("canceled", "请求已取消", err)
	}
	var bridgeError *model.BridgeError
	if errors.As(err, &bridgeError) {
		return err
	}
	return model.Error(code, message, err)
}

func fileName(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func requireDocumentID(value string) error {
	if strings.TrimSpace(value) == "" {
		return model.Error("bad_request", "缺少 documentId；请先调用 inspect 获取", nil)
	}
	return nil
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	return data, nil
}
