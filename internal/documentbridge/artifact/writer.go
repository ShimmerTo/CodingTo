package artifact

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codingto/internal/documentbridge/model"
	"codingto/internal/documentbridge/ocr"
	"codingto/internal/documentbridge/policy"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type Writer struct {
	dir         string
	blocksFile  *os.File
	blocks      *bufio.Writer
	blockCount  int
	sheets      map[string]*sheetWriter
	images      []model.MediaMeta
	mediaBytes  int64
	ocr         ocr.Engine
	ocrWarnings []string
	closed      bool
}

const MarkdownArtifactName = "content.md"

type sheetWriter struct {
	meta   model.SheetMeta
	file   *os.File
	writer *bufio.Writer
}

func NewWriter(dir string, engine ocr.Engine) (*Writer, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sheets"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "media"), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "blocks.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{
		dir: dir, blocksFile: file, blocks: bufio.NewWriterSize(file, 64*1024),
		sheets: map[string]*sheetWriter{}, ocr: engine,
	}, nil
}

func (w *Writer) WriteBlock(ctx context.Context, block model.Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(block.Text) > policy.MaxBlockBytes {
		return fmt.Errorf("block %s exceeds 1MB", block.ID)
	}
	if w.blockCount >= policy.MaxBlocks {
		return fmt.Errorf("block count exceeds %d", policy.MaxBlocks)
	}
	if strings.TrimSpace(block.ID) == "" {
		return fmt.Errorf("block id is required")
	}
	raw, err := json.Marshal(block)
	if err != nil {
		return err
	}
	if _, err := w.blocks.Write(append(raw, '\n')); err != nil {
		return err
	}
	w.blockCount++
	return nil
}

func (w *Writer) WriteSheetRow(ctx context.Context, sheet string, row int, cells []model.Cell) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state := w.sheets[sheet]
	if state == nil {
		fileName := safeSheetFileName(sheet) + ".jsonl"
		file, err := os.OpenFile(filepath.Join(w.dir, "sheets", fileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		state = &sheetWriter{
			meta: model.SheetMeta{Name: sheet, File: fileName},
			file: file, writer: bufio.NewWriterSize(file, 64*1024),
		}
		w.sheets[sheet] = state
	}
	record := struct {
		Row   int          `json:"row"`
		Cells []model.Cell `json:"cells"`
	}{Row: row, Cells: cells}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := state.writer.Write(append(raw, '\n')); err != nil {
		return err
	}
	if row > state.meta.Rows {
		state.meta.Rows = row
	}
	if len(cells) > state.meta.Cols {
		state.meta.Cols = len(cells)
	}
	return nil
}

func (w *Writer) WriteMedia(ctx context.Context, media model.MediaMeta, content io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if media.ID == "" || strings.ContainsAny(media.ID, `/\`) {
		return fmt.Errorf("invalid media id")
	}
	extension := strings.ToLower(media.Ext)
	if extension == "" {
		extension = extensionForMIME(media.MIME)
	}
	target := filepath.Join(w.dir, "media", media.ID+extension)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(content, policy.MaxMediaBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > policy.MaxMediaBytes {
		return fmt.Errorf("media %s exceeds 20MB", media.ID)
	}
	w.mediaBytes += written
	if w.mediaBytes > policy.MaxMediaTotalBytes {
		return fmt.Errorf("document media exceeds 200MB")
	}
	media.Size, media.Ext = written, extension
	if media.Width == 0 || media.Height == 0 {
		if input, err := os.Open(target); err == nil {
			if config, _, err := image.DecodeConfig(input); err == nil {
				media.Width, media.Height = config.Width, config.Height
			}
			input.Close()
		}
	}
	if strings.HasPrefix(media.MIME, "image/") {
		if w.ocr.Available() {
			text, recognizeErr := w.ocr.Recognize(ctx, target)
			if recognizeErr != nil {
				w.ocrWarnings = append(w.ocrWarnings, media.ID+": "+recognizeErr.Error())
			} else if text != "" {
				media.OCRText = text
				if err := os.WriteFile(filepath.Join(w.dir, "media", media.ID+".ocr.txt"), []byte(text), 0o600); err != nil {
					return err
				}
				if err := w.WriteBlock(ctx, model.Block{
					ID: "ocr_" + media.ID, Type: "image_ocr", Text: text,
					Page: max(1, media.Page), Ref: media.ID,
					Meta: map[string]string{"ocrEngine": w.ocr.Name()},
				}); err != nil {
					return err
				}
			}
		} else {
			w.ocrWarnings = append(w.ocrWarnings, media.ID+": no local OCR engine is available")
		}
	}
	w.images = append(w.images, media)
	return nil
}

func (w *Writer) Finalize(metadata *model.Metadata) error {
	if w.closed {
		return fmt.Errorf("writer is already closed")
	}
	w.closed = true
	if err := w.blocks.Flush(); err != nil {
		return err
	}
	if err := w.blocksFile.Sync(); err != nil {
		return err
	}
	if err := w.blocksFile.Close(); err != nil {
		return err
	}
	sheets := make([]model.SheetMeta, 0, len(w.sheets))
	for _, state := range w.sheets {
		if err := state.writer.Flush(); err != nil {
			return err
		}
		if err := state.file.Sync(); err != nil {
			return err
		}
		if err := state.file.Close(); err != nil {
			return err
		}
		sheets = append(sheets, state.meta)
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].Name < sheets[j].Name })
	manifest, err := json.MarshalIndent(map[string]any{"sheets": sheets}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(w.dir, "sheets", "manifest.json"), append(manifest, '\n'), 0o600); err != nil {
		return err
	}
	metadata.Blocks = w.blockCount
	metadata.Sheets = sheets
	metadata.Images = w.images
	metadata.OCRWarnings = w.ocrWarnings
	if err := w.writeMarkdownArtifact(*metadata); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, "metadata.json"), append(raw, '\n'), 0o600)
}

func (w *Writer) writeMarkdownArtifact(metadata model.Metadata) error {
	target := filepath.Join(w.dir, MarkdownArtifactName)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 64*1024)
	cleanup := func(writeErr error) error {
		_ = writer.Flush()
		_ = file.Close()
		_ = os.Remove(target)
		return writeErr
	}

	title := strings.TrimSuffix(metadata.SourceName, filepath.Ext(metadata.SourceName))
	if strings.TrimSpace(title) == "" {
		title = metadata.DocumentID
	}
	if _, err := fmt.Fprintf(writer, "# %s\n\n> 源文件：`%s`\n\n",
		escapeMarkdownText(title), escapeMarkdownCode(metadata.SourceName)); err != nil {
		return cleanup(err)
	}
	if len(metadata.Sheets) == 0 {
		if err := w.writeMarkdownBlocks(writer, metadata); err != nil {
			return cleanup(err)
		}
	} else if err := w.writeMarkdownSheets(writer, metadata.Sheets); err != nil {
		return cleanup(err)
	}
	if err := writer.Flush(); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

func (w *Writer) writeMarkdownBlocks(output *bufio.Writer, metadata model.Metadata) error {
	file, err := os.Open(filepath.Join(w.dir, "blocks.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()

	images := make(map[string]model.MediaMeta, len(metadata.Images))
	for _, media := range metadata.Images {
		images[media.ID] = media
	}
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 64*1024))
	activeTable := ""
	for {
		var block model.Block
		if err := decoder.Decode(&block); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if block.Type == "image_ocr" {
			continue
		}
		if block.Type == "row" {
			tableID := block.Meta["table"]
			if tableID == "" {
				tableID = "table"
			}
			cells := strings.Split(block.Text, " | ")
			if activeTable != tableID {
				if activeTable != "" {
					if _, err := output.WriteString("\n"); err != nil {
						return err
					}
				}
				if err := writeMarkdownTableRow(output, cells); err != nil {
					return err
				}
				if err := writeMarkdownTableSeparator(output, len(cells)); err != nil {
					return err
				}
				activeTable = tableID
			} else if err := writeMarkdownTableRow(output, cells); err != nil {
				return err
			}
			continue
		}
		if activeTable != "" {
			if _, err := output.WriteString("\n"); err != nil {
				return err
			}
			activeTable = ""
		}

		switch block.Type {
		case "heading":
			if metadata.Type == model.KindMD && strings.HasPrefix(strings.TrimSpace(block.Text), "#") {
				if _, err := fmt.Fprintf(output, "%s\n\n", block.Text); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintf(output, "%s %s\n\n",
				markdownHeadingPrefix(block.Meta["style"]), escapeMarkdownText(block.Text)); err != nil {
				return err
			}
		case "image":
			media, ok := images[block.Ref]
			if !ok {
				if _, err := fmt.Fprintf(output, "_%s_\n\n", escapeMarkdownText(block.Text)); err != nil {
					return err
				}
				continue
			}
			alt := strings.ReplaceAll(strings.ReplaceAll(block.Text, "[", ""), "]", "")
			if _, err := fmt.Fprintf(output, "![%s](media/%s%s)\n\n",
				escapeMarkdownText(alt), media.ID, media.Ext); err != nil {
				return err
			}
			if strings.TrimSpace(media.OCRText) != "" {
				if _, err := output.WriteString("**图片文字（OCR）：**\n\n"); err != nil {
					return err
				}
				if err := writeMarkdownParagraph(output, media.OCRText); err != nil {
					return err
				}
			}
		default:
			if metadata.Type == model.KindMD {
				if _, err := fmt.Fprintf(output, "%s\n\n", block.Text); err != nil {
					return err
				}
			} else if err := writeMarkdownParagraph(output, block.Text); err != nil {
				return err
			}
		}
	}
	if activeTable != "" {
		_, err = output.WriteString("\n")
	}
	return err
}

func (w *Writer) writeMarkdownSheets(output *bufio.Writer, sheets []model.SheetMeta) error {
	for _, sheet := range sheets {
		if _, err := fmt.Fprintf(output, "## %s\n\n", escapeMarkdownText(sheet.Name)); err != nil {
			return err
		}
		file, err := os.Open(filepath.Join(w.dir, "sheets", sheet.File))
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bufio.NewReaderSize(file, 64*1024))
		rowNumber := 0
		for {
			var record struct {
				Row   int          `json:"row"`
				Cells []model.Cell `json:"cells"`
			}
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				file.Close()
				return err
			}
			cells := make([]string, max(sheet.Cols, len(record.Cells)))
			for index, cell := range record.Cells {
				cells[index] = cell.Value
			}
			if err := writeMarkdownTableRow(output, cells); err != nil {
				file.Close()
				return err
			}
			if rowNumber == 0 {
				if err := writeMarkdownTableSeparator(output, len(cells)); err != nil {
					file.Close()
					return err
				}
			}
			rowNumber++
		}
		if err := file.Close(); err != nil {
			return err
		}
		if _, err := output.WriteString("\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeMarkdownParagraph(output *bufio.Writer, text string) error {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if index > 0 {
			if _, err := output.WriteString("  \n"); err != nil {
				return err
			}
		}
		if _, err := output.WriteString(escapeMarkdownText(line)); err != nil {
			return err
		}
	}
	_, err := output.WriteString("\n\n")
	return err
}

func writeMarkdownTableRow(output *bufio.Writer, cells []string) error {
	if len(cells) == 0 {
		cells = []string{""}
	}
	if _, err := output.WriteString("|"); err != nil {
		return err
	}
	for _, cell := range cells {
		value := strings.ReplaceAll(strings.ReplaceAll(cell, "\r\n", "\n"), "\n", "<br>")
		value = strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "|", `\|`)
		if _, err := fmt.Fprintf(output, " %s |", value); err != nil {
			return err
		}
	}
	_, err := output.WriteString("\n")
	return err
}

func writeMarkdownTableSeparator(output *bufio.Writer, columns int) error {
	if columns < 1 {
		columns = 1
	}
	if _, err := output.WriteString("|"); err != nil {
		return err
	}
	for range columns {
		if _, err := output.WriteString(" --- |"); err != nil {
			return err
		}
	}
	_, err := output.WriteString("\n")
	return err
}

func markdownHeadingPrefix(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	if style == "title" {
		return "#"
	}
	style = strings.TrimPrefix(style, "heading")
	style = strings.TrimLeft(style, " _-")
	if len(style) > 0 && style[0] >= '1' && style[0] <= '6' {
		return strings.Repeat("#", int(style[0]-'0'))
	}
	return "##"
}

func escapeMarkdownText(value string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"#", `\#`,
		"`", "\\`",
		"|", `\|`,
	).Replace(value)
}

func escapeMarkdownCode(value string) string {
	return strings.ReplaceAll(value, "`", "'")
}

func (w *Writer) Close() {
	if w.closed {
		return
	}
	w.closed = true
	if w.blocks != nil {
		_ = w.blocks.Flush()
	}
	if w.blocksFile != nil {
		_ = w.blocksFile.Close()
	}
	for _, state := range w.sheets {
		_ = state.writer.Flush()
		_ = state.file.Close()
	}
}

func safeSheetFileName(name string) string {
	var out strings.Builder
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			out.WriteRune(char)
		} else {
			out.WriteString(fmt.Sprintf("_%x", char))
		}
	}
	if out.Len() == 0 {
		out.WriteString("sheet")
	}
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%s-%x", out.String(), hash[:4])
}

func extensionForMIME(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	default:
		return ".png"
	}
}
