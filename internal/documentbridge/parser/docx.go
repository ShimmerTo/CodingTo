package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"

	"codingto/internal/documentbridge/model"
)

type DOCXParser struct{}

func (DOCXParser) Supports(kind model.FileKind) bool { return kind == model.KindDOCX }

func (DOCXParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	archive, err := zip.OpenReader(source.Path)
	if err != nil {
		return model.Summary{}, err
	}
	defer archive.Close()
	document, err := zipEntryBytes(archive.File, "word/document.xml")
	if err != nil {
		return model.Summary{}, err
	}
	blocks, hasTable, pages, err := parseDOCXBlocks(ctx, document)
	if err != nil {
		return model.Summary{}, err
	}
	for _, block := range blocks {
		if err := sink.WriteBlock(ctx, block); err != nil {
			return model.Summary{}, err
		}
	}
	imageNumber := 0
	for _, entry := range archive.File {
		if err := checkContext(ctx); err != nil {
			return model.Summary{}, err
		}
		if entry.FileInfo().IsDir() || !strings.HasPrefix(filepath.ToSlash(entry.Name), "word/media/") {
			continue
		}
		imageNumber++
		reader, err := entry.Open()
		if err != nil {
			return model.Summary{}, err
		}
		extension := strings.ToLower(filepath.Ext(entry.Name))
		mimeType := mime.TypeByExtension(extension)
		if mimeType == "" {
			mimeType = imageMIME("", extension)
		}
		imageID := "img" + itoa(imageNumber)
		blockID := "b" + itoa(len(blocks)+imageNumber)
		media := model.MediaMeta{
			ID: imageID, MIME: mimeType, Ext: extension, Size: int64(entry.UncompressedSize64),
			Page: pages, BlockID: blockID, Meta: map[string]string{"source": filepath.Base(entry.Name)},
		}
		if err := sink.WriteMedia(ctx, media, reader); err != nil {
			reader.Close()
			return model.Summary{}, err
		}
		if err := reader.Close(); err != nil {
			return model.Summary{}, err
		}
		if err := sink.WriteBlock(ctx, model.Block{
			ID: blockID, Type: "image", Text: "文档图片 " + filepath.Base(entry.Name),
			Page: pages, Ref: imageID,
		}); err != nil {
			return model.Summary{}, err
		}
	}
	return model.Summary{
		Pages: pages, PageKind: "logical", HasTable: hasTable, HasImage: imageNumber > 0,
		Capabilities: model.Capabilities{
			Text: true, Tables: "best_effort", Images: imageNumber > 0,
			OCR: map[bool]string{true: "local", false: "none"}[imageNumber > 0],
		},
	}, nil
}

func parseDOCXBlocks(ctx context.Context, data []byte) ([]model.Block, bool, int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var blocks []model.Block
	var paragraph strings.Builder
	var cell strings.Builder
	var row []string
	inParagraph, inCell, inTable, inText := false, false, false, false
	style := ""
	page := 1
	hasTable := false
	tableIndex := 0
	for {
		if err := checkContext(ctx); err != nil {
			return nil, false, 0, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, 0, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "tbl":
				inTable, hasTable = true, true
				tableIndex++
			case "tr":
				row = nil
			case "tc":
				inCell = true
				cell.Reset()
			case "p":
				inParagraph = true
				paragraph.Reset()
				style = ""
			case "pStyle":
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "val" {
						style = attribute.Value
					}
				}
			case "t":
				inText = true
			case "tab":
				if inParagraph {
					paragraph.WriteByte('\t')
				}
				if inCell {
					cell.WriteByte('\t')
				}
			case "br":
				if inParagraph {
					paragraph.WriteByte('\n')
				}
				if inCell {
					cell.WriteByte('\n')
				}
			}
		case xml.CharData:
			text := string(value)
			if inParagraph && inText {
				paragraph.WriteString(text)
			}
			if inCell && inText {
				cell.WriteString(text)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				text := strings.TrimSpace(paragraph.String())
				if text != "" && !inTable {
					blockType := "paragraph"
					if strings.HasPrefix(strings.ToLower(style), "heading") || strings.EqualFold(style, "title") {
						blockType = "heading"
						if len(blocks) > 0 {
							page++
						}
					} else if len(blocks) > 0 && len(blocks)%50 == 0 {
						page++
					}
					blocks = append(blocks, model.Block{
						ID: "b" + itoa(len(blocks)+1), Type: blockType, Text: text, Page: page,
						Meta: map[string]string{"style": style},
					})
				}
				inParagraph = false
			case "tc":
				row = append(row, strings.TrimSpace(cell.String()))
				inCell = false
			case "tr":
				if len(row) > 0 {
					blocks = append(blocks, model.Block{
						ID: "b" + itoa(len(blocks)+1), Type: "row",
						Text: strings.Join(row, " | "), Page: page,
						Meta: map[string]string{"table": itoa(tableIndex)},
					})
				}
			case "tbl":
				inTable = false
			}
		}
	}
	if page < 1 {
		page = 1
	}
	return blocks, hasTable, page, nil
}

func zipEntryBytes(entries []*zip.File, name string) ([]byte, error) {
	for _, entry := range entries {
		if filepath.ToSlash(entry.Name) != name {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	}
	return nil, fmt.Errorf("required DOCX entry not found: %s", name)
}
