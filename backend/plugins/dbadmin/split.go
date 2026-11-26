package dbadmin

// SQL 脚本拆分：把 SQL 文件拆成可逐条执行的语句。
// 方言差异用 splitOptions 控制：
//   - MySQL：反斜杠转义、反引号标识符、# 行注释、DELIMITER 指令、/*! */ 可执行注释
//   - PostgreSQL/SQLite：标准引号双写、$tag$ 美元引用
//   - SQL Server：[] 标识符（]] 转义）、单独一行 GO 作为批分隔
//   - Oracle：PL/SQL 块以单独一行 / 结束，块内分号不切分

import "strings"

type splitOptions struct {
	backslash bool // 反斜杠转义（MySQL）
	backtick  bool // 反引号标识符（MySQL）
	hash      bool // # 行注释（MySQL）
	delimiter bool // DELIMITER 指令（MySQL）
	dollar    bool // $tag$ 美元引用（PG/SQLite）
	bracket   bool // [..] 标识符（SQL Server）
	goBatch   bool // GO 批分隔（SQL Server）
	slashTerm bool // 单独一行 / 结束语句（Oracle）
	blockDDL  bool // CREATE PROCEDURE 等块内分号不切分（Oracle）
}

func splitStatements(kind, src string) []string {
	var opts splitOptions
	switch kind {
	case "mysql", "mariadb":
		opts = splitOptions{backslash: true, backtick: true, hash: true, delimiter: true}
	case "postgres", "postgresql", "pg", "sqlite", "sqlite3":
		opts = splitOptions{dollar: true}
	case "sqlserver", "mssql":
		opts = splitOptions{bracket: true, goBatch: true}
	case "oracle":
		opts = splitOptions{slashTerm: true, blockDDL: true}
	default:
		opts = splitOptions{dollar: true}
	}
	return splitWithOptions(src, opts)
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

func atLineStart(rs []rune, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if rs[j] == '\n' {
			return true
		}
		if !isSpaceRune(rs[j]) {
			return false
		}
	}
	return true
}

// lineOnly 判断 i 起是否为「单独一行」的目标串（后随换行或 EOF）。
func lineOnly(rs []rune, i int, word string) (bool, int) {
	w := []rune(word)
	n := len(rs)
	if i+len(w) > n {
		return false, 0
	}
	for k, r := range w {
		if rs[i+k] != r {
			return false, 0
		}
	}
	j := i + len(w)
	for j < n && (rs[j] == ' ' || rs[j] == '\t' || rs[j] == '\r') {
		j++
	}
	if j < n && rs[j] != '\n' {
		return false, 0
	}
	if j < n {
		j++ // 吃掉换行
	}
	return true, j
}

func splitWithOptions(src string, o splitOptions) []string {
	rs := []rune(src)
	n := len(rs)
	out := []string{}
	delim := []rune(";")
	var cur strings.Builder
	sawCode := false

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" && sawCode {
			out = append(out, s)
		}
		cur.Reset()
		sawCode = false
	}

	i := 0
	for i < n {
		c := rs[i]

		// DELIMITER 指令（MySQL）：行首、当前语句尚无实际代码
		if o.delimiter && !sawCode && (c == 'd' || c == 'D') && atLineStart(rs, i) &&
			strings.EqualFold(string(rs[i:min(i+9, n)]), "delimiter") {
			j := i + 9
			for j < n && (rs[j] == ' ' || rs[j] == '\t') {
				j++
			}
			k := j
			for k < n && rs[k] != '\n' {
				k++
			}
			if d := strings.TrimSpace(string(rs[j:k])); d != "" {
				delim = []rune(d)
			}
			cur.Reset()
			i = k
			continue
		}

		// GO 批分隔（SQL Server）：单独一行、大小写不敏感
		if o.goBatch && atLineStart(rs, i) && (c == 'g' || c == 'G') {
			if ok, next := lineOnlyFold(rs, i, "go"); ok {
				flush()
				i = next
				continue
			}
		}

		// 单独一行 / 结束语句（Oracle PL/SQL）
		if o.slashTerm && c == '/' && atLineStart(rs, i) {
			if ok, next := lineOnly(rs, i, "/"); ok {
				flush()
				// 去掉块尾残留的换行/空白
				cur.Reset()
				i = next
				continue
			}
		}

		// 字符串字面量
		if c == '\'' || c == '"' {
			cur.WriteRune(c)
			i++
			for i < n {
				cur.WriteRune(rs[i])
				if o.backslash && rs[i] == '\\' && i+1 < n {
					cur.WriteRune(rs[i+1])
					i += 2
					continue
				}
				if rs[i] == c {
					if i+1 < n && rs[i+1] == c {
						cur.WriteRune(c)
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			sawCode = true
			continue
		}

		// 反引号标识符（MySQL）
		if o.backtick && c == '`' {
			cur.WriteRune(c)
			i++
			for i < n {
				cur.WriteRune(rs[i])
				if rs[i] == '`' {
					if i+1 < n && rs[i+1] == '`' {
						cur.WriteRune('`')
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			sawCode = true
			continue
		}

		// [] 标识符（SQL Server，]] 转义）
		if o.bracket && c == '[' {
			cur.WriteRune(c)
			i++
			for i < n {
				cur.WriteRune(rs[i])
				if rs[i] == ']' {
					if i+1 < n && rs[i+1] == ']' {
						cur.WriteRune(']')
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			sawCode = true
			continue
		}

		// $tag$ ... $tag$（PG/SQLite）
		if o.dollar && c == '$' {
			if tagEnd, ok := dollarTagEnd(rs, i); ok {
				tag := string(rs[i : tagEnd+1])
				cur.WriteString(tag)
				if idx := strings.Index(string(rs[tagEnd+1:]), tag); idx >= 0 {
					cur.WriteString(string(rs[tagEnd+1 : tagEnd+1+idx+len(tag)]))
					i = tagEnd + 1 + idx + len(tag)
				} else {
					i = tagEnd + 1
				}
				sawCode = true
				continue
			}
		}

		// 行注释 -- 与 #
		if (c == '-' && i+1 < n && rs[i+1] == '-') || (o.hash && c == '#') {
			for i < n && rs[i] != '\n' {
				cur.WriteRune(rs[i])
				i++
			}
			continue
		}

		// 块注释 /* */；MySQL 的 /*! ... */ 为可执行注释，视为代码
		if c == '/' && i+1 < n && rs[i+1] == '*' {
			cur.WriteString("/*")
			i += 2
			if o.delimiter && i < n && rs[i] == '!' {
				sawCode = true
			}
			for i < n {
				if rs[i] == '*' && i+1 < n && rs[i+1] == '/' {
					cur.WriteString("*/")
					i += 2
					break
				}
				cur.WriteRune(rs[i])
				i++
			}
			continue
		}

		// 命中当前分隔符
		if i+len(delim) <= n {
			match := true
			for j, d := range delim {
				if rs[i+j] != d {
					match = false
					break
				}
			}
			if match {
				if o.slashTerm && o.blockDDL && isOracleBlock(cur.String()) {
					cur.WriteString(string(delim))
					i += len(delim)
					sawCode = true
					continue
				}
				flush()
				i += len(delim)
				continue
			}
		}

		if !isSpaceRune(c) {
			sawCode = true
		}
		cur.WriteRune(c)
		i++
	}
	flush()
	return out
}

// dollarTagEnd 识别 $...$ 起始标记（$$ 或 $tag$），返回结束 $ 的下标。
func dollarTagEnd(rs []rune, i int) (int, bool) {
	for j := i + 1; j < len(rs) && j-i <= 64; j++ {
		r := rs[j]
		if r == '$' {
			return j, true
		}
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return 0, false
		}
	}
	return 0, false
}

// lineOnlyFold 大小写不敏感的 lineOnly（GO）。
func lineOnlyFold(rs []rune, i int, word string) (bool, int) {
	w := []rune(word)
	n := len(rs)
	if i+len(w) > n {
		return false, 0
	}
	for k, r := range w {
		if !strings.EqualFold(string(rs[i+k]), string(r)) {
			return false, 0
		}
	}
	j := i + len(w)
	for j < n && (rs[j] == ' ' || rs[j] == '\t' || rs[j] == '\r') {
		j++
	}
	if j < n && rs[j] != '\n' {
		return false, 0
	}
	if j < n {
		j++
	}
	return true, j
}

// isOracleBlock Oracle PL/SQL 块：块内分号不是语句结束，需等单独一行 /。
func isOracleBlock(buf string) bool {
	s := strings.ToLower(strings.TrimSpace(buf))
	for _, p := range []string{
		"create or replace procedure", "create procedure", "create or replace function",
		"create function", "create or replace trigger", "create trigger",
		"create or replace package", "create package", "create or replace type", "create type",
		"declare", "begin",
	} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
