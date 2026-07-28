package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/policy"
)

var cellRangePattern = regexp.MustCompile(`(?i)^([A-Z]+)([1-9][0-9]*)(?::([A-Z]+)([1-9][0-9]*))?$`)

func (s *Service) sheet(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		DocumentID string `json:"documentId"`
		Sheet      string `json:"sheet"`
		Range      string `json:"range"`
		MaxRows    int    `json:"maxRows"`
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
	if params.Sheet == "" {
		sheets := make([]map[string]any, 0, len(metadata.Sheets))
		for _, sheet := range metadata.Sheets {
			sheets = append(sheets, map[string]any{"name": sheet.Name, "rows": sheet.Rows, "cols": sheet.Cols})
		}
		return map[string]any{"documentId": params.DocumentID, "sheets": sheets}, nil
	}
	var selected *model.SheetMeta
	for index := range metadata.Sheets {
		if metadata.Sheets[index].Name == params.Sheet {
			selected = &metadata.Sheets[index]
			break
		}
	}
	if selected == nil {
		return nil, model.Error("sheet_not_found", "工作表不存在："+params.Sheet, nil)
	}
	startCol, startRow, endCol, endRow, normalizedRange, err := parseRange(params.Range, selected.Rows, selected.Cols)
	if err != nil {
		return nil, err
	}
	maxRows := params.MaxRows
	if maxRows <= 0 {
		maxRows = 200
	}
	maxRows = min(maxRows, policy.MaxSheetRows)
	objectDir, err := s.Store.ObjectDir(params.DocumentID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(objectDir, "sheets", selected.File))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var headers []string
	rows := [][]any{}
	totalMatching := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, mapOperationError(err, "canceled", "请求已取消")
		}
		var record struct {
			Row   int          `json:"row"`
			Cells []model.Cell `json:"cells"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		if record.Row < startRow || record.Row > endRow {
			continue
		}
		values := selectCells(record.Cells, startCol, endCol)
		if record.Row == startRow {
			headers = make([]string, len(values))
			for index, value := range values {
				headers[index] = fmt.Sprint(value)
			}
			continue
		}
		totalMatching++
		if len(rows) < maxRows {
			rows = append(rows, values)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"documentId": params.DocumentID, "sheet": params.Sheet, "range": normalizedRange,
		"headers": headers, "rows": rows, "totalRows": selected.Rows,
		"truncated": totalMatching > len(rows),
	}, nil
}

func parseRange(value string, totalRows, totalCols int) (startCol, startRow, endCol, endRow int, normalized string, err error) {
	if totalRows < 1 {
		totalRows = 1
	}
	if totalCols < 1 {
		totalCols = 1
	}
	value = strings.TrimSpace(value)
	if value == "" {
		startCol, startRow, endCol, endRow = 1, 1, totalCols, totalRows
		return startCol, startRow, endCol, endRow, fmt.Sprintf("A1:%s%d", columnName(endCol), endRow), nil
	}
	match := cellRangePattern.FindStringSubmatch(value)
	if match == nil {
		err = model.Error("bad_request", "range 必须使用 A1:H100 格式", nil)
		return
	}
	startCol, _ = columnNumber(match[1])
	startRow, _ = strconv.Atoi(match[2])
	endCol, endRow = startCol, startRow
	if match[3] != "" {
		endCol, _ = columnNumber(match[3])
		endRow, _ = strconv.Atoi(match[4])
	}
	if endCol < startCol || endRow < startRow {
		err = model.Error("bad_request", "range 结束位置不能早于开始位置", nil)
		return
	}
	if endCol > 16_384 || endRow > 1_048_576 {
		err = model.Error("bad_request", "range 超出工作表最大边界 XFD1048576", nil)
		return
	}
	normalized = fmt.Sprintf("%s%d:%s%d", columnName(startCol), startRow, columnName(endCol), endRow)
	return
}

func columnNumber(value string) (int, error) {
	number := 0
	for _, char := range strings.ToUpper(value) {
		if char < 'A' || char > 'Z' {
			return 0, fmt.Errorf("invalid column")
		}
		number = number*26 + int(char-'A'+1)
	}
	return number, nil
}

func columnName(number int) string {
	if number < 1 {
		return "A"
	}
	result := ""
	for number > 0 {
		number--
		result = string(rune('A'+number%26)) + result
		number /= 26
	}
	return result
}

func selectCells(cells []model.Cell, startCol, endCol int) []any {
	result := make([]any, endCol-startCol+1)
	for column := startCol; column <= endCol; column++ {
		if column-1 < len(cells) {
			result[column-startCol] = cells[column-1].Value
		} else {
			result[column-startCol] = ""
		}
	}
	return result
}
