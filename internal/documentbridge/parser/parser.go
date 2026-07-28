package parser

import (
	"context"
	"fmt"
	"io"

	"codingto/internal/documentbridge/model"
)

type Parser interface {
	Supports(kind model.FileKind) bool
	Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error)
}

type Sink interface {
	WriteBlock(ctx context.Context, block model.Block) error
	WriteSheetRow(ctx context.Context, sheet string, row int, cells []model.Cell) error
	WriteMedia(ctx context.Context, media model.MediaMeta, content io.Reader) error
}

type Registry struct {
	parsers []Parser
}

func DefaultRegistry() *Registry {
	return &Registry{parsers: []Parser{
		TextParser{},
		CSVParser{},
		XLSXParser{},
		DOCXParser{},
		PDFParser{},
		ImageParser{},
	}}
}

func (r *Registry) For(kind model.FileKind) (Parser, error) {
	for _, candidate := range r.parsers {
		if candidate.Supports(kind) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("no parser for %s", kind)
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
