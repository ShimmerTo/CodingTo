package classify

import (
	"strings"
	"unicode"

	"codingto/internal/dbsecurity"
)

// Token 是语句中的一个词法单元；字符串字面量与注释在分词阶段被整体跳过，
// 因此任何基于 token 的分类/提取都不会被字符串内容欺骗。
type Token struct {
	// Text 是原始文本；引号包裹的标识符已去掉引号。
	Text string
	// Upper 是大写形式，用于关键字比较。
	Upper string
	// Quoted 表示该 token 来自引号包裹的标识符（不可视为关键字）。
	Quoted bool
}

// Tokenize 对单条语句分词。跳过字符串字面量与注释；括号等标点作为分隔符。
func Tokenize(sql string) []Token {
	var tokens []Token
	runes := []rune(sql)
	var current []rune
	quoted := false

	flush := func() {
		if len(current) == 0 {
			return
		}
		text := string(current)
		tokens = append(tokens, Token{Text: text, Upper: strings.ToUpper(text), Quoted: quoted})
		current = current[:0]
		quoted = false
	}

	inSingle, inDouble, inBacktick := false, false, false
	inLineComment, inBlockComment := false, false

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case inLineComment:
			if r == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			if r == '*' && next == '/' {
				i++
				inBlockComment = false
			}
		case inSingle:
			if r == '\'' {
				if next == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			// 双引号内容既可能是字符串（MySQL）也可能是标识符（PG），
			// 统一按带引号 token 保留，保守处理。
			if r == '"' {
				inDouble = false
				flush()
			} else {
				current = append(current, r)
				quoted = true
			}
		case inBacktick:
			if r == '`' {
				inBacktick = false
				flush()
			} else {
				current = append(current, r)
				quoted = true
			}
		default:
			switch {
			case r == '\'':
				flush()
				inSingle = true
			case r == '"':
				flush()
				current = current[:0]
				inDouble = true
				quoted = true
			case r == '`':
				flush()
				current = current[:0]
				inBacktick = true
				quoted = true
			case r == '-' && next == '-':
				flush()
				inLineComment = true
				i++
			case r == '#':
				flush()
				inLineComment = true
			case r == '/' && next == '*':
				flush()
				inBlockComment = true
				i++
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '$':
				current = append(current, r)
			default:
				flush()
			}
		}
	}
	flush()
	return tokens
}

// Classify 对单条语句做保守分类。逃逸口扫描优先于首词映射：
// 任何命中文件/程序/外部访问逃逸口的语句都会升级为 database.external.*，
// 无法识别的语句一律 database.unknown（所有预设默认拒绝）。
func Classify(sql string) dbsecurity.Action {
	tokens := Tokenize(sql)
	if len(tokens) == 0 {
		return dbsecurity.ActionUnknown
	}
	if external := scanEscapeHatch(tokens); external != "" {
		return external
	}
	first := tokens[0]
	if first.Quoted {
		return dbsecurity.ActionUnknown
	}
	second := ""
	if len(tokens) > 1 && !tokens[1].Quoted {
		second = tokens[1].Upper
	}
	switch first.Upper {
	case "SELECT", "WITH", "VALUES", "TABLE":
		return dbsecurity.ActionReadSelect
	case "SHOW", "DESCRIBE", "DESC", "EXPLAIN":
		return dbsecurity.ActionReadMeta
	case "INSERT", "REPLACE":
		return dbsecurity.ActionWriteInsert
	case "UPDATE":
		return dbsecurity.ActionWriteUpdate
	case "DELETE":
		return dbsecurity.ActionWriteDelete
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME":
		return schemaAction(second)
	case "GRANT", "REVOKE", "SET", "USE", "PRAGMA", "VACUUM", "ANALYZE", "LOCK", "UNLOCK":
		return dbsecurity.ActionAdmin
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END":
		return dbsecurity.ActionTransaction
	case "START":
		if second == "TRANSACTION" {
			return dbsecurity.ActionTransaction
		}
		return dbsecurity.ActionUnknown
	}
	return dbsecurity.ActionUnknown
}

func schemaAction(object string) dbsecurity.Action {
	switch object {
	case "TABLE", "TEMPORARY", "UNLOGGED":
		return dbsecurity.ActionSchemaTable
	case "INDEX", "UNIQUE", "FULLTEXT":
		return dbsecurity.ActionSchemaIndex
	case "VIEW", "MATERIALIZED":
		return dbsecurity.ActionSchemaView
	default:
		return dbsecurity.ActionSchemaOther
	}
}

// scanEscapeHatch 全语句扫描文件/外部访问逃逸口，命中即返回 external 子动作。
func scanEscapeHatch(tokens []Token) dbsecurity.Action {
	upper := make([]string, len(tokens))
	for i, tok := range tokens {
		if tok.Quoted {
			upper[i] = "\x00" // 引号 token 不参与逃逸口关键字匹配
		} else {
			upper[i] = tok.Upper
		}
	}
	for i, word := range upper {
		nextWord := ""
		if i+1 < len(upper) {
			nextWord = upper[i+1]
		}
		switch word {
		case "COPY":
			if containsWord(upper, "PROGRAM") {
				return dbsecurity.ActionExternalProgram
			}
			return dbsecurity.ActionExternalFile
		case "LOAD":
			if nextWord == "DATA" {
				return dbsecurity.ActionExternalFile
			}
		case "ATTACH":
			return dbsecurity.ActionExternalAttach
		case "CREATE":
			if nextWord == "EXTENSION" {
				return dbsecurity.ActionExternalOther
			}
		case "INTO":
			if nextWord == "OUTFILE" || nextWord == "DUMPFILE" {
				return dbsecurity.ActionExternalFile
			}
		case "LOAD_FILE", "PG_READ_FILE", "PG_READ_BINARY_FILE", "PG_WRITE_FILE",
			"LO_IMPORT", "LO_EXPORT", "PG_LS_DIR", "PG_STAT_FILE":
			return dbsecurity.ActionExternalFile
		case "DBLINK", "DBLINK_CONNECT", "HTTP", "PG_CURL":
			return dbsecurity.ActionExternalNetwork
		}
	}
	return ""
}

func containsWord(words []string, target string) bool {
	for _, w := range words {
		if w == target {
			return true
		}
	}
	return false
}
