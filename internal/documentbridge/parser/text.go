package parser

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"codingto/internal/documentbridge/model"
)

type TextParser struct{}

func (TextParser) Supports(kind model.FileKind) bool {
	return kind == model.KindText || kind == model.KindMD
}

func (TextParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	file, err := os.Open(source.Path)
	if err != nil {
		return model.Summary{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	page, blockNumber := 1, 0
	for {
		if err := checkContext(ctx); err != nil {
			return model.Summary{}, err
		}
		line, readErr := reader.ReadString('\n')
		if !utf8.ValidString(line) {
			return model.Summary{}, errors.New("text file is not valid UTF-8")
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) != "" {
			blockNumber++
			blockType := "paragraph"
			if source.Kind == model.KindMD && strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
				blockType = "heading"
				if blockNumber > 1 {
					page++
				}
			} else if blockNumber > 1 && (blockNumber-1)%50 == 0 {
				page++
			}
			if err := sink.WriteBlock(ctx, model.Block{
				ID: "b" + itoa(blockNumber), Type: blockType, Text: line, Page: page,
			}); err != nil {
				return model.Summary{}, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return model.Summary{}, readErr
		}
	}
	if page < 1 {
		page = 1
	}
	return model.Summary{
		Pages: page, PageKind: "logical",
		Capabilities: model.Capabilities{Text: true, Tables: "none", Images: false, OCR: "none"},
	}, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [24]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}
