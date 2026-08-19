package classify

// Resource 是从语句中提取出的表引用；Schema 可能为空。
type Resource struct {
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table"`
}

// stopKeywords 结束表名收集的关键字集合。
var stopKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "SET": true, "ON": true,
	"JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true,
	"CROSS": true, "OUTER": true, "GROUP": true, "ORDER": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "UNION": true, "EXCEPT": true, "INTERSECT": true,
	"VALUES": true, "RETURNING": true, "USING": true, "AS": true, "AND": true,
	"OR": true, "NOT": true, "IN": true, "EXISTS": true, "CASE": true,
	"WHEN": true, "THEN": true, "ELSE": true, "END": true, "DEFAULT": true,
	"PRIMARY": true, "FOREIGN": true, "KEY": true, "REFERENCES": true,
	"CONSTRAINT": true, "CHECK": true, "UNIQUE": true, "WITH": true,
	"INTO": true, "TABLE": true, "UPDATE": true, "DELETE": true, "INSERT": true,
	"FOR": true, "LOCK": true, "WINDOW": true, "FETCH": true,
}

// HasWhere 判断语句是否包含顶层 WHERE 子句。
func HasWhere(tokens []Token) bool {
	for _, tok := range tokens {
		if !tok.Quoted && tok.Upper == "WHERE" {
			return true
		}
	}
	return false
}

// HasLimit 判断语句是否包含 LIMIT（或 PG 的 FETCH FIRST）子句。
func HasLimit(tokens []Token) bool {
	for _, tok := range tokens {
		if tok.Quoted {
			continue
		}
		if tok.Upper == "LIMIT" || tok.Upper == "FETCH" {
			return true
		}
	}
	return false
}

// ExtractTables 提取语句引用的表名（FROM/INTO/UPDATE/JOIN/TABLE 后的标识符）。
// 提取是保守的：无法确定的位置直接跳过；调用方在提取为空时应按白名单不匹配处理。
func ExtractTables(tokens []Token) []Resource {
	var resources []Resource
	seen := map[string]bool{}
	add := func(schema, table string) {
		if table == "" {
			return
		}
		key := schema + "." + table
		if seen[key] {
			return
		}
		seen[key] = true
		resources = append(resources, Resource{Schema: schema, Table: table})
	}

	expectName := false
	allowList := false // FROM/INTO 后允许逗号分隔的多个表
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok.Quoted {
			// 引号标识符只能是名字，不可能是关键字。
			if expectName {
				schema, table := splitName(tok.Text, tokens, &i)
				add(schema, table)
				expectName = false
			}
			continue
		}
		switch tok.Upper {
		case "FROM":
			expectName = true
			allowList = true
			continue
		case "INTO":
			// INSERT INTO 只取单一目标表；逗号列表多为列名，不能当表收集。
			expectName = true
			allowList = false
			continue
		case "UPDATE", "TABLE", "TRUNCATE":
			expectName = true
			allowList = tok.Upper != "UPDATE"
			continue
		case "JOIN":
			expectName = true
			allowList = false
			continue
		case ",":
			// 分词器把标点当分隔符，逗号不会成为 token；列表续接靠 allowList 判断。
		}
		if expectName {
			if stopKeywords[tok.Upper] {
				expectName = false
				allowList = false
				continue
			}
			// 跳过可选修饰词：ONLY / IF NOT EXISTS / IGNORE 等。
			if tok.Upper == "ONLY" || tok.Upper == "IF" || tok.Upper == "NOT" ||
				tok.Upper == "EXISTS" || tok.Upper == "IGNORE" || tok.Upper == "TEMP" ||
				tok.Upper == "TEMPORARY" {
				continue
			}
			schema, table := splitName(tok.Text, tokens, &i)
			add(schema, table)
			expectName = allowList // FROM 列表可能还有后续名字
			continue
		}
	}
	return resources
}

// splitName 处理 schema.table 形态：token 内含 "." 直接拆分；否则向后看
// "name . name" 三 token 形态。返回值中 i 已推进到已消费的位置。
func splitName(text string, tokens []Token, i *int) (schema, table string) {
	if dot := indexByte(text, '.'); dot >= 0 {
		return text[:dot], text[dot+1:]
	}
	return "", text
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
