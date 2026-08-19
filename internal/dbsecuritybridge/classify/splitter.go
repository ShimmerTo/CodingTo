package classify

// SplitStatements 把多语句文本按顶层分号拆分。拆分时正确跳过字符串字面量
// （单引号/双引号/反引号）与注释（--、#、/* */），避免字符串里的分号误切。
// 空语句（仅空白/注释）被丢弃。
func SplitStatements(sqlText string) []string {
	var statements []string
	var current []rune
	runes := []rune(sqlText)
	inSingle, inDouble, inBacktick := false, false, false
	inLineComment, inBlockComment := false, false

	flush := func() {
		stmt := string(current)
		current = current[:0]
		if trimmed := trimStmt(stmt); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case inLineComment:
			current = append(current, r)
			if r == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			current = append(current, r)
			if r == '*' && next == '/' {
				current = append(current, next)
				i++
				inBlockComment = false
			}
		case inSingle:
			current = append(current, r)
			if r == '\'' {
				if next == '\'' { // 转义的单引号
					current = append(current, next)
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			current = append(current, r)
			if r == '"' {
				inDouble = false
			}
		case inBacktick:
			current = append(current, r)
			if r == '`' {
				inBacktick = false
			}
		default:
			switch {
			case r == ';':
				flush()
			case r == '\'':
				inSingle = true
				current = append(current, r)
			case r == '"':
				inDouble = true
				current = append(current, r)
			case r == '`':
				inBacktick = true
				current = append(current, r)
			case r == '-' && next == '-':
				inLineComment = true
				current = append(current, r)
			case r == '#':
				inLineComment = true
				current = append(current, r)
			case r == '/' && next == '*':
				inBlockComment = true
				current = append(current, r)
			default:
				current = append(current, r)
			}
		}
	}
	flush()
	return statements
}

func trimStmt(stmt string) string {
	start, end := 0, len(stmt)
	for start < end && isSpace(rune(stmt[start])) {
		start++
	}
	for end > start && isSpace(rune(stmt[end-1])) {
		end--
	}
	return stmt[start:end]
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
