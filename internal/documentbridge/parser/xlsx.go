package parser

import (
	"context"
	"strings"

	"codingto/internal/documentbridge/model"
	"github.com/xuri/excelize/v2"
)

type XLSXParser struct{}

func (XLSXParser) Supports(kind model.FileKind) bool { return kind == model.KindXLSX }

func (XLSXParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	workbook, err := excelize.OpenFile(source.Path, excelize.Options{RawCellValue: false})
	if err != nil {
		return model.Summary{}, err
	}
	defer workbook.Close()
	sheets := workbook.GetSheetList()
	blockNumber := 0
	for sheetIndex, sheet := range sheets {
		if err := checkContext(ctx); err != nil {
			return model.Summary{}, err
		}
		rows, err := workbook.Rows(sheet)
		if err != nil {
			return model.Summary{}, err
		}
		rowNumber := 0
		for rows.Next() {
			rowNumber++
			values, err := rows.Columns()
			if err != nil {
				rows.Close()
				return model.Summary{}, err
			}
			cells := make([]model.Cell, len(values))
			for index, value := range values {
				cells[index] = model.Cell{Value: value}
			}
			if err := sink.WriteSheetRow(ctx, sheet, rowNumber, cells); err != nil {
				rows.Close()
				return model.Summary{}, err
			}
			if rowNumber <= 2 {
				blockNumber++
				if err := sink.WriteBlock(ctx, model.Block{
					ID: "b" + itoa(blockNumber), Type: "row", Text: strings.Join(values, " | "),
					Page: sheetIndex + 1, Sheet: sheet, Row: rowNumber,
				}); err != nil {
					rows.Close()
					return model.Summary{}, err
				}
			}
		}
		if err := rows.Error(); err != nil {
			rows.Close()
			return model.Summary{}, err
		}
		rows.Close()
	}
	pages := len(sheets)
	if pages == 0 {
		pages = 1
	}
	return model.Summary{
		Pages: pages, PageKind: "logical", HasTable: true,
		Capabilities: model.Capabilities{Text: true, Tables: "structured", Images: false, OCR: "none"},
	}, nil
}
