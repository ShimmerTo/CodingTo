package parser

import (
	"context"
	"strings"

	"codingto/internal/documentbridge/model"
	pdf "github.com/ledongthuc/pdf"
)

type PDFParser struct{}

func (PDFParser) Supports(kind model.FileKind) bool { return kind == model.KindPDF }

func (PDFParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	file, reader, err := pdf.Open(source.Path)
	if err != nil {
		return model.Summary{}, err
	}
	defer file.Close()
	pages := reader.NumPage()
	blockNumber := 0
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		if err := checkContext(ctx); err != nil {
			return model.Summary{}, err
		}
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return model.Summary{}, err
		}
		text = strings.ReplaceAll(text, "\r\n", "\n")
		for _, paragraph := range strings.Split(text, "\n") {
			paragraph = strings.TrimSpace(paragraph)
			if paragraph == "" {
				continue
			}
			blockNumber++
			if err := sink.WriteBlock(ctx, model.Block{
				ID: "b" + itoa(blockNumber), Type: "paragraph", Text: paragraph, Page: pageNumber,
			}); err != nil {
				return model.Summary{}, err
			}
		}
	}
	if pages < 1 {
		pages = 1
	}
	return model.Summary{
		Pages: pages, PageKind: "physical",
		Capabilities: model.Capabilities{Text: true, Tables: "best_effort", Images: false, OCR: "none"},
	}, nil
}
