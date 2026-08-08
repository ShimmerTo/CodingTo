package steward

import (
	"strings"
	"unicode/utf8"
)

// Outbound message size limits. IM platforms truncate or reject oversized
// messages, so long text is split into multiple messages before sending.
const (
	// messageSplitRunes bounds DingTalk/Feishu markdown messages. Both
	// platforms accept far larger payloads (DingTalk markdown ~20k chars,
	// Feishu cards ~30KB); 2000 runes keeps every part comfortably below
	// those ceilings while staying readable in a chat window.
	messageSplitRunes = 2000
	// wecomMessageSplitBytes bounds WeCom markdown/text content. The official
	// API contract caps markdown content at 2048 UTF-8 bytes; the safe margin
	// below keeps CJK/emoji parts from being rejected.
	wecomMessageSplitBytes = 1800
)

// splitOutboundText splits long message text into sendable parts. WeCom is
// byte-limited (official 2048-byte cap), DingTalk/Feishu are split by rune
// count. Splitting is markdown-aware: parts break at headings first, then at
// blank-line paragraph boundaries, so tables, code blocks and lists are not
// torn mid-block.
func splitOutboundText(text, platform string) []string {
	if platform == string(PlatformWeCom) {
		return splitTextByBytes(text, wecomMessageSplitBytes)
	}
	return splitTextByRunes(text, messageSplitRunes)
}

// textTooLong reports whether text exceeds the platform's single-message
// limit and therefore needs splitting.
func textTooLong(text, platform string) bool {
	if platform == string(PlatformWeCom) {
		return len([]byte(text)) > wecomMessageSplitBytes
	}
	return utf8.RuneCountInString(text) > messageSplitRunes
}

// DingTalkMarkdownTitle extracts a short notification title from the first
// non-empty line (with markdown syntax stripped) for DingTalk markdown
// messages, which require a title field.
func DingTalkMarkdownTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.Trim(line, " #>*-`")
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 20 {
			return string(runes[:20]) + "…"
		}
		return line
	}
	return "管家消息"
}

// splitTextByRunes splits text on markdown structure boundaries so no part
// exceeds max runes. See splitMarkdownText for the strategy.
func splitTextByRunes(text string, max int) []string {
	return splitMarkdownText(text, max, utf8.RuneCountInString)
}

// splitTextByBytes splits text on markdown structure boundaries so no part
// exceeds max bytes after UTF-8 encoding. Used for WeCom, whose API caps
// content bytes.
func splitTextByBytes(text string, max int) []string {
	return splitMarkdownText(text, max, byteSize)
}

func byteSize(s string) int { return len([]byte(s)) }

// mdBlock is one logical markdown structure chunk.
type mdBlock struct {
	lines []string
	kind  string // "heading" | "code" | "table" | "para"
}

// splitMarkdownBlocks cuts text into logical markdown blocks:
//   - code: a ``` fenced code block (kept whole; a missing closing fence is
//     carried to the end of the text)
//   - table: consecutive lines starting with "|"
//   - heading: a line starting with "# "
//   - para: blank-line separated ordinary lines (a blank line becomes a
//     one-element para block so it survives splitting and separates blocks)
func splitMarkdownBlocks(text string) []mdBlock {
	lines := splitKeepEmptyLines(text)
	var blocks []mdBlock
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "```"):
			blk := mdBlock{kind: "code", lines: []string{line}}
			i++
			for i < len(lines) && !strings.HasPrefix(lines[i], "```") {
				blk.lines = append(blk.lines, lines[i])
				i++
			}
			if i < len(lines) {
				blk.lines = append(blk.lines, lines[i]) // closing fence
				i++
			}
			blocks = append(blocks, blk)
		case strings.HasPrefix(line, "|"):
			blk := mdBlock{kind: "table"}
			for i < len(lines) && strings.HasPrefix(lines[i], "|") {
				blk.lines = append(blk.lines, lines[i])
				i++
			}
			blocks = append(blocks, blk)
		case isMarkdownHeading(line):
			blocks = append(blocks, mdBlock{kind: "heading", lines: []string{line}})
			i++
		case strings.TrimSpace(line) == "":
			blocks = append(blocks, mdBlock{kind: "para", lines: []string{""}})
			i++
		default:
			blk := mdBlock{kind: "para"}
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !isMarkdownHeading(lines[i]) {
				blk.lines = append(blk.lines, lines[i])
				i++
			}
			blocks = append(blocks, blk)
		}
	}
	return blocks
}

// isMarkdownHeading reports whether line is a markdown heading (1-6 # followed
// by a space or end of line, so "### 标题" is a heading but "#foo" is not).
func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return false
	}
	rest := trimmed[count:]
	return rest == "" || strings.HasPrefix(rest, " ")
}

// splitMarkdownText splits markdown text into parts that each fit within max
// units (runes or bytes, as measured by measure). Parts are grouped greedily,
// with these structure-preserving rules:
//  1. a heading always starts a new part (chapters stay readable on their own);
//  2. ordinary blocks join the current part while it has room, separated by a
//     blank line;
//  3. a single block larger than max is split internally by lines, keeping
//     tables (header repeated per part) and code fences (fence re-opened and
//     closed per part) syntactically complete;
//  4. only a single overlong line is hard-cut, always on rune boundaries.
func splitMarkdownText(text string, max int, measure func(string) int) []string {
	if max <= 0 {
		return []string{text}
	}
	blocks := splitMarkdownBlocks(text)
	var parts []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			parts = append(parts, joinLines(buf))
			buf = nil
		}
	}
	for _, blk := range blocks {
		// Headings are chapter boundaries: close the current part first so
		// every heading starts a fresh message.
		if blk.kind == "heading" {
			flush()
		}
		if linesSize(blk.lines, measure) > max {
			flush()
			parts = append(parts, splitOverlongBlock(blk, max, measure)...)
			continue
		}
		if len(buf) > 0 {
			candidate := append(append([]string(nil), buf...), "")
			candidate = append(candidate, blk.lines...)
			if linesSize(candidate, measure) > max {
				flush()
			}
		}
		if len(buf) > 0 {
			buf = append(buf, "")
		}
		buf = append(buf, blk.lines...)
	}
	flush()
	return parts
}

// splitOverlongBlock splits one block that exceeds max into several complete
// markdown parts. Blocks with structural requirements (tables, code fences)
// are rebuilt so every part renders on its own.
func splitOverlongBlock(blk mdBlock, max int, measure func(string) int) []string {
	switch blk.kind {
	case "code":
		return splitCodeBlock(blk, max, measure)
	case "table":
		return splitTableBlock(blk, max, measure)
	default: // para, heading, blank
		chunks := chunkByLines(blk.lines, max, measure)
		parts := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			parts = append(parts, joinLines(chunk))
		}
		return parts
	}
}

// splitTableBlock splits an overlong table so every part carries the header
// and separator row: a bare data row is not a table on its own.
func splitTableBlock(blk mdBlock, max int, measure func(string) int) []string {
	lines := blk.lines
	header := []string{}
	rest := lines
	if len(lines) >= 2 && isTableSeparator(lines[1]) {
		header = []string{lines[0], lines[1]}
		rest = lines[2:]
	}
	// Pathological header that alone does not fit: fall back to plain rows.
	if len(header) > 0 && linesSize(header, measure) >= max {
		header = nil
	}
	withHeader := func(data []string) string {
		return joinLines(append(append([]string(nil), header...), data...))
	}
	var parts []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			parts = append(parts, withHeader(buf))
			buf = nil
		}
	}
	for _, line := range rest {
		lineSize := measure(line)
		// A single overlong row: hard-cut it, every piece keeps the header.
		if lineSize > max {
			flush()
			for _, piece := range hardCut(line, max, measure) {
				parts = append(parts, withHeader([]string{piece}))
			}
			continue
		}
		// The header plus one row already overflows (abnormally wide header):
		// emit the row alone, there is no room for a second row anyway.
		if len(header) > 0 && linesSize(append(append([]string(nil), header...), line), measure) > max {
			flush()
			parts = append(parts, withHeader([]string{line}))
			continue
		}
		// Would the full candidate (header + buffered rows + this row) fit?
		if len(buf) > 0 {
			candidate := append(append([]string(nil), header...), buf...)
			candidate = append(candidate, line)
			if linesSize(candidate, measure) > max {
				flush()
			}
		}
		buf = append(buf, line)
	}
	flush()
	return parts
}

// codeFenceOverhead is how much a fenced part adds on top of its content:
// the opening fence (up to "```lang", 5 runes) + newline + newline + the
// closing "```" (3 runes). Reserved so rebuilt parts never overflow max.
const codeFenceOverhead = 10

// splitCodeBlock splits an overlong code block so every part is a complete
// fenced block: the first part keeps the original opening fence (language
// tag), later parts and the closing fence are re-added by this function.
func splitCodeBlock(blk mdBlock, max int, measure func(string) int) []string {
	lines := blk.lines
	opening := "```"
	if len(lines) > 0 && strings.HasPrefix(lines[0], "```") {
		opening = lines[0]
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "```") {
		lines = lines[:len(lines)-1]
	}
	budget := max - codeFenceOverhead
	if budget < 1 {
		budget = 1
	}
	chunks := chunkByLines(lines, budget, measure)
	if len(chunks) == 0 {
		return []string{joinLines(blk.lines)}
	}
	parts := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		head := "```"
		if i == 0 {
			head = opening
		}
		fenced := append([]string{head}, chunk...)
		fenced = append(fenced, "```")
		parts = append(parts, joinLines(fenced))
	}
	return parts
}

// chunkByLines groups lines into chunks whose joined size never exceeds max.
// A single overlong line is hard-cut into rune-safe pieces.
func chunkByLines(lines []string, max int, measure func(string) int) [][]string {
	var chunks [][]string
	var buf []string
	bufSize := 0
	flush := func() {
		if len(buf) > 0 {
			chunks = append(chunks, buf)
			buf = nil
			bufSize = 0
		}
	}
	for _, line := range lines {
		lineSize := measure(line)
		if lineSize > max {
			flush()
			for _, piece := range hardCut(line, max, measure) {
				chunks = append(chunks, []string{piece})
			}
			continue
		}
		if bufSize > 0 && bufSize+1+lineSize > max {
			flush()
		}
		if bufSize > 0 {
			bufSize++
		}
		buf = append(buf, line)
		bufSize += lineSize
	}
	flush()
	return chunks
}

// hardCut splits a single overlong line into pieces that each fit within max.
// Iteration is rune-based so a multibyte character is never torn apart.
func hardCut(line string, max int, measure func(string) int) []string {
	if max <= 0 {
		return []string{line}
	}
	var pieces []string
	var buf strings.Builder
	bufSize := 0
	for _, r := range line {
		rs := string(r)
		rsSize := measure(rs)
		if bufSize > 0 && bufSize+rsSize > max {
			pieces = append(pieces, buf.String())
			buf.Reset()
			bufSize = 0
		}
		buf.WriteString(rs)
		bufSize += rsSize
	}
	if bufSize > 0 {
		pieces = append(pieces, buf.String())
	}
	return pieces
}

// isTableSeparator reports whether line is a markdown table separator row
// (| --- | :---: | etc.), i.e. composed of |, :, - and spaces with at least
// one dash.
func isTableSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return false
	}
	hasDash := false
	for _, r := range trimmed {
		switch r {
		case '|', ':', '-', ' ':
			if r == '-' {
				hasDash = true
			}
		default:
			return false
		}
	}
	return hasDash
}

// linesSize returns the measured size of lines joined by single newlines.
func linesSize(lines []string, measure func(string) int) int {
	total := 0
	for i, line := range lines {
		if i > 0 {
			total++
		}
		total += measure(line)
	}
	return total
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// splitKeepEmptyLines splits on newlines but drops the single empty element
// produced by a trailing newline, so the reconstructed text matches the input
// exactly (an interior empty line is preserved).
func splitKeepEmptyLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
