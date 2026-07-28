package service

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"

	"codingto/internal/documentbridge/policy"
	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

type docElement struct {
	Type    string     `json:"type"`
	Text    string     `json:"text"`
	Items   []string   `json:"items"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
	Level   int        `json:"level"`
}

type docContent struct {
	Title    string       `json:"title"`
	Elements []docElement `json:"elements"`
}

func parseContent(raw json.RawMessage) (docContent, error) {
	var c docContent
	if len(raw) == 0 || string(raw) == "null" {
		return c, nil
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&c); err == nil {
		return c, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return docContent{Elements: []docElement{{Type: "paragraph", Text: s}}}, nil
	} else {
		return c, err
	}
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
	).Replace(s)
}

func zipOOXML(files map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, _ := w.Create(name)
		f.Write([]byte(content))
	}
	w.Close()
	return buf.Bytes()
}

// ---------- Markdown ----------

func buildMarkdown(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if c.Title != "" {
		b.WriteString("# ")
		b.WriteString(c.Title)
		b.WriteString("\n\n")
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			level := e.Level
			if level < 1 {
				level = 1
			}
			if level > 6 {
				level = 6
			}
			b.WriteString(strings.Repeat("#", level))
			b.WriteString(" ")
			b.WriteString(e.Text)
			b.WriteString("\n\n")
		case "paragraph":
			b.WriteString(e.Text)
			b.WriteString("\n\n")
		case "list":
			for _, it := range e.Items {
				b.WriteString("- ")
				b.WriteString(it)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		case "table":
			if len(e.Headers) > 0 {
				b.WriteString("| ")
				b.WriteString(strings.Join(e.Headers, " | "))
				b.WriteString(" |\n")
				b.WriteString("| ")
				b.WriteString(strings.Repeat("--- | ", len(e.Headers)))
				b.WriteString("\n")
			}
			for _, row := range e.Rows {
				b.WriteString("| ")
				b.WriteString(strings.Join(row, " | "))
				b.WriteString(" |\n")
			}
			b.WriteString("\n")
		default:
			if e.Text != "" {
				b.WriteString(e.Text)
				b.WriteString("\n\n")
			}
		}
	}
	return []byte(b.String()), nil
}

// ---------- DOCX ----------

const docxDocumentTmpl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>%s<w:sectPr/></w:body>
</w:document>`

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

func paragraphDocx(text, style string) string {
	styleXML := ""
	if style != "" {
		styleXML = fmt.Sprintf("<w:pPr><w:pStyle w:val=\"%s\"/></w:pPr>", style)
	}
	return fmt.Sprintf("<w:p>%s<w:r><w:t xml:space=\"preserve\">%s</w:t></w:r></w:p>", styleXML, xmlEscape(text))
}

func tableRowDocx(cells []string, _ bool) string {
	var sb strings.Builder
	sb.WriteString("<w:tr>")
	for _, cell := range cells {
		sb.WriteString("<w:tc><w:tcPr><w:tcMar><w:top w:w=\"40\" w:type=\"dxa\"/><w:bottom w:w=\"40\" w:type=\"dxa\"/><w:left w:w=\"80\" w:type=\"dxa\"/><w:right w:w=\"80\" w:type=\"dxa\"/></w:tcMar></w:tcPr>")
		sb.WriteString(fmt.Sprintf("<w:p><w:r><w:t xml:space=\"preserve\">%s</w:t></w:r></w:p>", xmlEscape(cell)))
		sb.WriteString("</w:tc>")
	}
	sb.WriteString("</w:tr>")
	return sb.String()
}

func tableDocx(headers []string, rows [][]string) string {
	var sb strings.Builder
	sb.WriteString("<w:tbl><w:tblPr><w:tblW w:w=\"0\" w:type=\"auto\"/>")
	sb.WriteString("<w:tblBorders>")
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		sb.WriteString(fmt.Sprintf("<w:%s w:val=\"single\" w:sz=\"4\" w:space=\"0\" w:color=\"auto\"/>", edge))
	}
	sb.WriteString("</w:tblBorders></w:tblPr>")
	colCount := len(headers)
	if colCount == 0 && len(rows) > 0 {
		colCount = len(rows[0])
	}
	sb.WriteString("<w:tblGrid>")
	for i := 0; i < colCount; i++ {
		sb.WriteString("<w:gridCol w:w=\"2000\"/>")
	}
	sb.WriteString("</w:tblGrid>")
	if len(headers) > 0 {
		sb.WriteString(tableRowDocx(headers, true))
	}
	for _, row := range rows {
		sb.WriteString(tableRowDocx(row, false))
	}
	sb.WriteString("</w:tbl>")
	return sb.String()
}

func buildDocx(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	if c.Title != "" {
		body.WriteString(paragraphDocx(c.Title, "Heading1"))
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			lvl := e.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			body.WriteString(paragraphDocx(e.Text, fmt.Sprintf("Heading%d", lvl)))
		case "paragraph":
			body.WriteString(paragraphDocx(e.Text, ""))
		case "list":
			for _, it := range e.Items {
				body.WriteString(paragraphDocx("• "+it, ""))
			}
		case "table":
			body.WriteString(tableDocx(e.Headers, e.Rows))
		default:
			if e.Text != "" {
				body.WriteString(paragraphDocx(e.Text, ""))
			}
		}
	}
	document := fmt.Sprintf(docxDocumentTmpl, body.String())
	return zipOOXML(map[string]string{
		"[Content_Types].xml": docxContentTypes,
		"_rels/.rels":         docxRels,
		"word/document.xml":   document,
	}), nil
}

// ---------- XLSX ----------

func colLetter(n int) string {
	var s strings.Builder
	n++
	for n > 0 {
		s.WriteByte(byte('A' + (n-1)%26))
		n = (n - 1) / 26
	}
	r := []byte(s.String())
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func buildXlsx(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	if c.Title != "" {
		rows = append(rows, []string{c.Title})
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading", "paragraph":
			if e.Text != "" {
				rows = append(rows, []string{e.Text})
			}
		case "list":
			for _, it := range e.Items {
				rows = append(rows, []string{it})
			}
		case "table":
			if len(e.Headers) > 0 {
				rows = append(rows, e.Headers)
			}
			rows = append(rows, e.Rows...)
		}
	}
	if len(rows) == 0 {
		rows = append(rows, []string{""})
	}

	uniq := map[string]int{}
	var sst []string
	for _, row := range rows {
		for _, cell := range row {
			if _, ok := uniq[cell]; !ok {
				uniq[cell] = len(sst)
				sst = append(sst, cell)
			}
		}
	}

	var ssb strings.Builder
	totalCells := 0
	for _, row := range rows {
		totalCells += len(row)
	}
	ssb.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="%d" uniqueCount="%d">`, totalCells, len(sst)))
	for _, s := range sst {
		ssb.WriteString(fmt.Sprintf("<si><t xml:space=\"preserve\">%s</t></si>", xmlEscape(s)))
	}
	ssb.WriteString("</sst>")

	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for ri, row := range rows {
		sheet.WriteString(fmt.Sprintf(`<row r="%d">`, ri+1))
		for ci, cell := range row {
			ref := fmt.Sprintf("%s%d", colLetter(ci), ri+1)
			sheet.WriteString(fmt.Sprintf(`<c r="%s" t="s"><v>%d</v></c>`, ref, uniq[cell]))
		}
		sheet.WriteString("</row>")
	}
	sheet.WriteString("</sheetData></worksheet>")

	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

	wbRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`

	xlsxContentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`

	xlsxRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

	files := map[string]string{
		"[Content_Types].xml":        xlsxContentTypes,
		"_rels/.rels":                xlsxRels,
		"xl/workbook.xml":            workbook,
		"xl/_rels/workbook.xml.rels": wbRels,
		"xl/worksheets/sheet1.xml":   sheet.String(),
		"xl/sharedStrings.xml":       ssb.String(),
	}
	return zipOOXML(files), nil
}

// ---------- PDF ----------

type pdfLine struct {
	text string
	size int
}

var (
	pdfFontMu       sync.Mutex
	pdfFontCache    = map[string]*opentype.Font{}
	pdfFontFailures = map[string]struct{}{}
)

func pdfActualText(s string) string {
	var b strings.Builder
	b.WriteString("FEFF")
	for _, unit := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", unit)
	}
	return b.String()
}

func buildPdf(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var lines []pdfLine
	if c.Title != "" {
		lines = append(lines, pdfLine{c.Title, 16})
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			lines = append(lines, pdfLine{e.Text, 14})
		case "paragraph":
			lines = append(lines, pdfLine{e.Text, 11})
		case "list":
			for _, it := range e.Items {
				lines = append(lines, pdfLine{"• " + it, 11})
			}
		case "table":
			if len(e.Headers) > 0 {
				lines = append(lines, pdfLine{strings.Join(e.Headers, "  |  "), 11})
			}
			for _, row := range e.Rows {
				lines = append(lines, pdfLine{strings.Join(row, "  |  "), 11})
			}
		default:
			if e.Text != "" {
				lines = append(lines, pdfLine{e.Text, 11})
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, pdfLine{"", 11})
	}

	const linesPerPage = 46
	pages := make([][]pdfLine, 0)
	for i := 0; i < len(lines); i += linesPerPage {
		end := i + linesPerPage
		if end > len(lines) {
			end = len(lines)
		}
		pages = append(pages, lines[i:end])
	}

	allText := make([]string, 0, len(lines))
	for _, line := range lines {
		allText = append(allText, line.text)
	}
	font, err := loadPDFFont(strings.Join(allText, "\n"))
	if err != nil {
		return nil, err
	}

	objects := [][]byte{
		[]byte("<</Type /Catalog /Pages 2 0 R>>"),
		nil,
	}
	kids := make([]string, 0, len(pages))
	objNum := 3
	for _, pageLines := range pages {
		pageNum := objNum
		contentNum := objNum + 1
		imageNum := objNum + 2
		objNum += 3
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
		imageStream, renderErr := renderPDFPage(font, pageLines)
		if renderErr != nil {
			return nil, renderErr
		}
		pageText := make([]string, 0, len(pageLines))
		for _, line := range pageLines {
			pageText = append(pageText, line.text)
		}
		content := fmt.Sprintf(
			"/Span <</ActualText <%s>>> BDC\nq\n595 0 0 842 0 0 cm\n/Im1 Do\nQ\nEMC",
			pdfActualText(strings.Join(pageText, "\n")),
		)
		contentObject := []byte(fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(content), content))
		pageObject := []byte(fmt.Sprintf(
			"<</Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources <</XObject <</Im1 %d 0 R>>>> /Contents %d 0 R>>",
			imageNum, contentNum,
		))
		var imageObject bytes.Buffer
		fmt.Fprintf(&imageObject, "<</Type /XObject /Subtype /Image /Width 595 /Height 842 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d>>\nstream\n", len(imageStream))
		imageObject.Write(imageStream)
		imageObject.WriteString("\nendstream")
		objects = append(objects, pageObject, contentObject, imageObject.Bytes())
	}
	objects[1] = []byte(fmt.Sprintf("<</Type /Pages /Kids [%s] /Count %d>>", strings.Join(kids, " "), len(pages)))

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n", i+1)
		pdf.Write(obj)
		pdf.WriteString("\nendobj\n")
	}
	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n")
	fmt.Fprintf(&pdf, "0 %d\n", len(objects)+1)
	pdf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}
	pdf.WriteString("trailer\n")
	fmt.Fprintf(&pdf, "<</Size %d /Root 1 0 R>>\n", len(objects)+1)
	fmt.Fprintf(&pdf, "startxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes(), nil
}

func loadPDFFont(text string) (*opentype.Font, error) {
	for _, candidate := range pdfFontCandidates() {
		pdfFontMu.Lock()
		font := pdfFontCache[candidate]
		_, failed := pdfFontFailures[candidate]
		pdfFontMu.Unlock()
		if font == nil && !failed {
			data, err := os.ReadFile(candidate)
			if err == nil {
				if strings.EqualFold(filepath.Ext(candidate), ".ttc") {
					collection, parseErr := opentype.ParseCollection(data)
					if parseErr == nil && collection.NumFonts() > 0 {
						font, err = collection.Font(0)
					} else if parseErr != nil {
						err = parseErr
					}
				} else {
					font, err = opentype.Parse(data)
				}
			}
			pdfFontMu.Lock()
			if err == nil && font != nil {
				pdfFontCache[candidate] = font
			} else {
				pdfFontFailures[candidate] = struct{}{}
			}
			pdfFontMu.Unlock()
		}
		if font != nil && fontSupportsText(font, text) {
			return font, nil
		}
	}
	return nil, fmt.Errorf("未找到覆盖文档字符的系统字体，无法生成 PDF")
}

func fontSupportsText(font *opentype.Font, text string) bool {
	var buffer sfnt.Buffer
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		glyph, err := font.GlyphIndex(&buffer, r)
		if err != nil || glyph == 0 {
			return false
		}
	}
	return true
}

func pdfFontCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		windowsDir := os.Getenv("WINDIR")
		if windowsDir == "" {
			windowsDir = `C:\Windows`
		}
		fontDir := filepath.Join(windowsDir, "Fonts")
		return []string{
			filepath.Join(fontDir, "msyh.ttc"),
			filepath.Join(fontDir, "simsun.ttc"),
			filepath.Join(fontDir, "simhei.ttf"),
		}
	case "darwin":
		return []string{
			"/System/Library/Fonts/PingFang.ttc",
			"/System/Library/Fonts/STHeiti Light.ttc",
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		}
	default:
		return []string{
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/opentype/noto/NotoSerifCJK-Regular.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		}
	}
}

func renderPDFPage(font *opentype.Font, lines []pdfLine) ([]byte, error) {
	page := image.NewRGBA(image.Rect(0, 0, 595, 842))
	draw.Draw(page, page.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	y := 42
	for _, line := range lines {
		face, err := opentype.NewFace(font, &opentype.FaceOptions{
			Size: float64(line.size), DPI: 72, Hinting: xfont.HintingFull,
		})
		if err != nil {
			return nil, err
		}
		drawer := xfont.Drawer{
			Dst: page, Src: image.NewUniform(color.Black), Face: face,
			Dot: fixed.P(50, y),
		}
		drawer.DrawString(line.text)
		_ = face.Close()
		y += 16
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	row := make([]byte, 595*3)
	for y := 0; y < 842; y++ {
		for x := 0; x < 595; x++ {
			offset := x * 3
			pixel := page.RGBAAt(x, y)
			row[offset], row[offset+1], row[offset+2] = pixel.R, pixel.G, pixel.B
		}
		if _, err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

// ---------- ZIP (package existing files) ----------

func buildZip(ctx context.Context, paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("zip 需要至少一个待打包路径")
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make(map[string]struct{}, len(paths))
	var total int64
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			_ = w.Close()
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("无法读取 %s: %w", p, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("仅支持打包普通文件：%s", p)
		}
		if info.Size() > policy.MaxCreateSourceBytes {
			return nil, fmt.Errorf("源文件超过 50MB 限制：%s", p)
		}
		total += info.Size()
		if total > policy.MaxCreateTotalBytes {
			return nil, fmt.Errorf("源文件总大小超过 200MB 限制")
		}
		name := filepath.Base(p)
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("zip 内存在重名文件：%s", name)
		}
		names[name] = struct{}{}
		f, err := w.Create(name)
		if err != nil {
			return nil, err
		}
		source, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		copyErr := copyWithContext(ctx, f, source, policy.MaxCreateSourceBytes)
		closeErr := source.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if buf.Len() > policy.MaxCreateOutputBytes {
			return nil, fmt.Errorf("zip 输出超过 100MB 限制")
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > policy.MaxCreateOutputBytes {
		return nil, fmt.Errorf("zip 输出超过 100MB 限制")
	}
	return buf.Bytes(), nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader, maxBytes int64) error {
	buffer := make([]byte, 64*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := src.Read(buffer)
		if count > 0 {
			copied += int64(count)
			if copied > maxBytes {
				return fmt.Errorf("源文件超过大小限制")
			}
			if _, writeErr := dst.Write(buffer[:count]); writeErr != nil {
				return writeErr
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// ---------- TXT ----------

func buildText(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if c.Title != "" {
		b.WriteString(c.Title)
		b.WriteString("\n\n")
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			b.WriteString(e.Text)
			b.WriteString("\n\n")
		case "paragraph":
			b.WriteString(e.Text)
			b.WriteString("\n\n")
		case "list":
			for _, it := range e.Items {
				b.WriteString("- ")
				b.WriteString(it)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		case "table":
			rows := append([][]string{}, e.Rows...)
			if len(e.Headers) > 0 {
				rows = append([][]string{e.Headers}, rows...)
			}
			for _, row := range rows {
				b.WriteString(strings.Join(row, "\t"))
				b.WriteString("\n")
			}
			b.WriteString("\n")
		default:
			if e.Text != "" {
				b.WriteString(e.Text)
				b.WriteString("\n\n")
			}
		}
	}
	return []byte(b.String()), nil
}

// ---------- CSV ----------

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func buildCsv(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	hasTable := false
	for _, e := range c.Elements {
		if e.Type != "table" {
			continue
		}
		hasTable = true
		if len(e.Headers) > 0 {
			parts := make([]string, len(e.Headers))
			for i, h := range e.Headers {
				parts[i] = csvField(h)
			}
			b.WriteString(strings.Join(parts, ","))
			b.WriteString("\n")
		}
		for _, row := range e.Rows {
			parts := make([]string, len(row))
			for i, cell := range row {
				parts[i] = csvField(cell)
			}
			b.WriteString(strings.Join(parts, ","))
			b.WriteString("\n")
		}
	}
	if !hasTable {
		if c.Title != "" {
			b.WriteString(csvField(c.Title))
			b.WriteString("\n")
		}
		for _, e := range c.Elements {
			text := e.Text
			switch e.Type {
			case "list":
				for _, it := range e.Items {
					b.WriteString(csvField(it))
					b.WriteString("\n")
				}
				continue
			case "heading", "paragraph":
				if text == "" {
					continue
				}
			default:
				if text == "" {
					continue
				}
			}
			if text != "" {
				b.WriteString(csvField(text))
				b.WriteString("\n")
			}
		}
	}
	return []byte(b.String()), nil
}

// ---------- HTML ----------

func escapeHtml(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	).Replace(s)
}

func buildHtml(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n")
	if c.Title != "" {
		b.WriteString("<title>" + escapeHtml(c.Title) + "</title>\n")
	}
	b.WriteString("<style>body{font-family:system-ui,Arial,sans-serif;line-height:1.6;max-width:800px;margin:40px auto;padding:0 16px;}table{border-collapse:collapse;width:100%;}td,th{border:1px solid #ccc;padding:6px 10px;text-align:left;}li{margin:4px 0;}</style>\n</head>\n<body>\n")
	if c.Title != "" {
		b.WriteString("<h1>" + escapeHtml(c.Title) + "</h1>\n")
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			lvl := e.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			b.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", lvl, escapeHtml(e.Text), lvl))
		case "paragraph":
			b.WriteString("<p>" + escapeHtml(e.Text) + "</p>\n")
		case "list":
			b.WriteString("<ul>\n")
			for _, it := range e.Items {
				b.WriteString("<li>" + escapeHtml(it) + "</li>\n")
			}
			b.WriteString("</ul>\n")
		case "table":
			b.WriteString("<table>\n")
			if len(e.Headers) > 0 {
				b.WriteString("<tr>")
				for _, h := range e.Headers {
					b.WriteString("<th>" + escapeHtml(h) + "</th>")
				}
				b.WriteString("</tr>\n")
			}
			for _, row := range e.Rows {
				b.WriteString("<tr>")
				for _, cell := range row {
					b.WriteString("<td>" + escapeHtml(cell) + "</td>")
				}
				b.WriteString("</tr>\n")
			}
			b.WriteString("</table>\n")
		default:
			if e.Text != "" {
				b.WriteString("<p>" + escapeHtml(e.Text) + "</p>\n")
			}
		}
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String()), nil
}

// ---------- JSON ----------

func buildJson(raw json.RawMessage) ([]byte, error) {
	if len(raw) > 0 && string(raw) != "null" {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			if out, err := json.MarshalIndent(v, "", "  "); err == nil {
				return out, nil
			}
		}
	}
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"title":    c.Title,
		"elements": c.Elements,
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- RTF ----------

func rtfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '{':
			b.WriteString(`\{`)
		case '}':
			b.WriteString(`\}`)
		case '\n':
			b.WriteString(`\par `)
		case '\t':
			b.WriteString(`\tab `)
		default:
			if r >= 0x20 && r <= 0x7e {
				b.WriteRune(r)
				continue
			}
			for _, unit := range utf16.Encode([]rune{r}) {
				b.WriteString(`\u`)
				b.WriteString(fmt.Sprint(int16(unit)))
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

func buildRtf(raw json.RawMessage) ([]byte, error) {
	c, err := parseContent(raw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(`{\rtf1\ansi\ansicpg936\uc1\deff0{\fonttbl{\f0\fnil SimSun;}}` + "\n")
	if c.Title != "" {
		b.WriteString(`\pard\b\fs32 ` + rtfEscape(c.Title) + `\b0\par` + "\n")
	}
	for _, e := range c.Elements {
		switch e.Type {
		case "heading":
			lvl := e.Level
			if lvl < 1 {
				lvl = 1
			}
			size := 28 - (lvl-1)*2
			if size < 16 {
				size = 16
			}
			b.WriteString(fmt.Sprintf(`\pard\b\fs%d %s\b0\par`+"\n", size, rtfEscape(e.Text)))
		case "paragraph":
			b.WriteString(`\pard ` + rtfEscape(e.Text) + `\par` + "\n")
		case "list":
			for _, it := range e.Items {
				b.WriteString(`\pard\li360\bullet\tab ` + rtfEscape(it) + `\par` + "\n")
			}
		case "table":
			if len(e.Headers) > 0 {
				b.WriteString(`\pard ` + rtfEscape(strings.Join(e.Headers, "\t")) + `\par` + "\n")
			}
			for _, row := range e.Rows {
				b.WriteString(`\pard ` + rtfEscape(strings.Join(row, "\t")) + `\par` + "\n")
			}
		default:
			if e.Text != "" {
				b.WriteString(`\pard ` + rtfEscape(e.Text) + `\par` + "\n")
			}
		}
	}
	b.WriteString("}\n")
	return []byte(b.String()), nil
}
