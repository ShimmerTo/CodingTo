package parser

import (
	"bufio"
	"context"
	"encoding/csv"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"codingto/internal/documentbridge/model"
)

type CSVParser struct{}

func (CSVParser) Supports(kind model.FileKind) bool { return kind == model.KindCSV }

func (CSVParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	file, err := os.Open(source.Path)
	if err != nil {
		return model.Summary{}, err
	}
	defer file.Close()
	reader := csv.NewReader(bufio.NewReaderSize(file, 64*1024))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	rows, maxColumns := 0, 0
	for {
		if err := checkContext(ctx); err != nil {
			return model.Summary{}, err
		}
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return model.Summary{}, readErr
		}
		rows++
		cells := make([]model.Cell, len(record))
		for index, value := range record {
			if rows == 1 && index == 0 {
				value = strings.TrimPrefix(value, "\ufeff")
			}
			if !utf8.ValidString(value) {
				return model.Summary{}, &encodingError{}
			}
			cells[index] = model.Cell{Value: value}
		}
		if len(cells) > maxColumns {
			maxColumns = len(cells)
		}
		if err := sink.WriteSheetRow(ctx, "CSV", rows, cells); err != nil {
			return model.Summary{}, err
		}
		if rows <= 2 {
			if err := sink.WriteBlock(ctx, model.Block{
				ID: "b" + itoa(rows), Type: "row", Text: strings.Join(record, " | "),
				Page: 1, Sheet: "CSV", Row: rows,
			}); err != nil {
				return model.Summary{}, err
			}
		}
	}
	_ = maxColumns
	return model.Summary{
		Pages: 1, PageKind: "logical", HasTable: true,
		Capabilities: model.Capabilities{Text: true, Tables: "structured", Images: false, OCR: "none"},
	}, nil
}

type encodingError struct{}

func (*encodingError) Error() string { return "CSV is not valid UTF-8" }
