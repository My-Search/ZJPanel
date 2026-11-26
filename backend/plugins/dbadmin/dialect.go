package dbadmin

// SQL 方言抽象：把各数据库在「元数据查询 / 标识符引用 / 分页语法 / 建删语句」
// 上的差异收敛到 sqlDialect，上层处理器只负责参数校验与 JSON 封装。
// Redis / MongoDB 不是 SQL 数据库，走独立的 kv.go / doc.go 分支。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// TableInfo 统一表/集合信息（前端树与列表复用）。
type TableInfo struct {
	Name      string `json:"name"`
	Schema    string `json:"schema,omitempty"`
	Rows      any    `json:"rows"`
	SizeBytes any    `json:"sizeBytes"`
	Comment   string `json:"comment,omitempty"`
	Kind      string `json:"kind,omitempty"` // table / view / collection
}

// ColumnInfo 统一列信息（等价 MySQL 的 Field/Type/Null/Key/Default/Extra/Comment）。
type ColumnInfo struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Key      string  `json:"key,omitempty"` // PRI / UNI / ""
	Default  *string `json:"default"`
	Extra    string  `json:"extra,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// IndexInfo 统一索引信息（多列索引已聚合为一行）。
type IndexInfo struct {
	Name       string `json:"name"`
	Columns    string `json:"columns"`
	Unique     bool   `json:"unique"`
	Type       string `json:"type,omitempty"`
	Definition string `json:"definition,omitempty"`
}

// SchemaInfo 统一结构视图：字段 + 索引 + 建表语句（拿不到原生 DDL 时按字段生成）。
type SchemaInfo struct {
	Columns   []ColumnInfo `json:"columns"`
	Indexes   []IndexInfo  `json:"indexes"`
	CreateSQL string       `json:"createSQL"`
}

// sqlDialect 一种关系型数据库的适配器。
type sqlDialect interface {
	Kind() string
	DefaultPort() string
	Validate(c *Connection) error
	// Open 用明文密码打开连接；db 为空时由调用方传入 DefaultDB。
	Open(c *Connection, db, plainPwd string) (*sql.DB, error)
	// OpenLong 放宽读超时（SQL 文件导入等长耗时场景）。
	OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error)
	DefaultDB(c *Connection) string
	Quote(ident string) string
	// Qualify 库/模式 + 表名 → 可直接拼进 SQL 的引用表达式。
	Qualify(db, table string) string
	// Ph 参数占位符（i 从 1 起）。
	Ph(i int) string

	ListDatabases(ctx context.Context, db *sql.DB, c *Connection) ([]string, error)
	Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error)
	Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error)

	SupportsCreateDB() bool
	CreateDatabaseSQL(name, charset string) (string, error)
	DropDatabaseSQL(name string) (string, error)
	MaintainDB(c *Connection) string // 删除库时重连的维护库
	TruncateSQL(dbName, table string) (string, error)
	DropTableSQL(dbName, table string) (string, error)

	PageSQL(dbName, table, where, order, dir string, limit, offset int) string
	CountSQL(dbName, table, where string) string
}

func sqlDialectFor(kind string) (sqlDialect, error) {
	switch kind {
	case "mysql", "mariadb":
		return mysqlDialect{}, nil
	case "postgres", "postgresql", "pg":
		return postgresDialect{}, nil
	case "sqlite", "sqlite3":
		return sqliteDialect{}, nil
	case "sqlserver", "mssql":
		return mssqlDialect{}, nil
	case "oracle":
		return oracleDialect{}, nil
	}
	return nil, fmt.Errorf("不支持的数据库类型: %s", kind)
}

func isSQLKind(kind string) bool {
	switch kind {
	case "mysql", "mariadb", "postgres", "postgresql", "pg", "sqlite", "sqlite3", "sqlserver", "mssql", "oracle":
		return true
	}
	return false
}

// phList 生成 "(占位符,占位符…)"，按方言递增编号。
func phList(d sqlDialect, n int) string {
	if n <= 0 {
		return "()"
	}
	ps := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ps = append(ps, d.Ph(i))
	}
	return "(" + strings.Join(ps, ", ") + ")"
}

// generateDDL 拿不到原生建表语句时，按字段信息生成一份可读 DDL。
// 索引不并入 DDL（结构页已单独展示，避免生成重复语句）。
func generateDDL(d sqlDialect, dbName, table string, cols []ColumnInfo) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(d.Qualify(dbName, table))
	b.WriteString(" (\n")
	pk := make([]string, 0, 2)
	for i, c := range cols {
		b.WriteString("  ")
		b.WriteString(d.Quote(c.Name))
		b.WriteString(" ")
		b.WriteString(c.Type)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.Default != nil {
			v := strings.TrimSpace(*c.Default)
			// PostgreSQL 的 nextval/identity 默认值原样保留；空串视为无默认值
			if v != "" {
				b.WriteString(" DEFAULT ")
				b.WriteString(v)
			}
		}
		if i < len(cols)-1 || len(pk) > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		if c.Key == "PRI" {
			pk = append(pk, d.Quote(c.Name))
		}
	}
	if len(pk) > 0 {
		b.WriteString("  PRIMARY KEY (")
		b.WriteString(strings.Join(pk, ", "))
		b.WriteString(")\n")
	}
	b.WriteString(");")
	return b.String()
}

// strOrNil 把 sql.NullString 转成 *string（JSON null 表示无默认值）。
func strOrNil(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// normValue 把 JSON 数字/布尔等规整成 database/sql 可绑定的值。
// json.Number 统一转 int64/float64：直接按字符串传给 pg 会因类型不匹配报错。
func normValue(v any) any {
	if n, ok := v.(jsonNumber); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
		return n.String()
	}
	switch t := v.(type) {
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return v
}

// jsonNumber 与 encoding/json 的 Number 同源，单独定义避免各文件重复导入。
type jsonNumber = interface {
	Int64() (int64, error)
	Float64() (float64, error)
	String() string
}

func normArgs(vals map[string]any) []any {
	cols := sortedCols(vals)
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		args = append(args, normValue(vals[c]))
	}
	return args
}
