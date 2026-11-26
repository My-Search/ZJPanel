package dbadmin

// SQLite 方言：复用面板已依赖的 glebarez/go-sqlite（modernc 纯 Go 分支，CGO_ENABLED=0）。
// 注意不要直接 import modernc.org/sqlite：两者都注册 driver 名 "sqlite"，同一二进制会
// 在 init 阶段 panic（database/sql duplicate driver）。
// 连接以面板宿主机上的文件路径为准（/opt 挂载与容器同路径，见 README）。
// 「库」= main / 附加库；单连接串行访问，避免 WAL 下的多写锁竞争。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/glebarez/go-sqlite"
)

type sqliteDialect struct{}

func (sqliteDialect) Kind() string        { return "sqlite" }
func (sqliteDialect) DefaultPort() string { return "" }

func (sqliteDialect) Validate(c *Connection) error {
	p := strings.TrimSpace(c.Path)
	if p == "" {
		return fmt.Errorf("数据库文件路径必填")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("SQLite 需使用绝对路径")
	}
	clean := filepath.Clean(p)
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return fmt.Errorf("路径不能包含 ..")
		}
	}
	if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
		return fmt.Errorf("路径是目录，不是数据库文件")
	}
	dir := filepath.Dir(clean)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("目录不存在: %s", dir)
	}
	return nil
}

func (sqliteDialect) DefaultDB(*Connection) string  { return "main" }
func (sqliteDialect) MaintainDB(*Connection) string { return "main" }

func (sqliteDialect) Open(c *Connection, db, _ string) (*sql.DB, error) {
	dbh, err := sql.Open("sqlite", filepath.Clean(c.Path))
	if err != nil {
		return nil, err
	}
	// SQLite 单文件：固定单连接，PRAGMA 对后续查询持续生效
	dbh.SetMaxOpenConns(1)
	dbh.SetMaxIdleConns(1)
	if _, err := dbh.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = dbh.Close()
		return nil, err
	}
	return dbh, nil
}

func (sqliteDialect) OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return sqliteDialect{}.Open(c, db, plainPwd)
}

func (sqliteDialect) Quote(ident string) string {
	ident = strings.TrimSpace(ident)
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d sqliteDialect) Qualify(db, table string) string {
	table = strings.TrimSpace(table)
	if db != "" && db != "main" {
		return d.Quote(db) + "." + d.Quote(table)
	}
	return d.Quote(table)
}

func (sqliteDialect) Ph(int) string { return "?" }

func (d sqliteDialect) schemaPrefix(dbName string) string {
	if dbName == "" || dbName == "main" {
		return ""
	}
	return d.Quote(dbName) + "."
}

func (sqliteDialect) ListDatabases(ctx context.Context, db *sql.DB, _ *Connection) ([]string, error) {
	_, rows, err := queryRowsCtx(ctx, db, "PRAGMA database_list")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 1 {
			out = append(out, fmt.Sprint(r[1]))
		}
	}
	if len(out) == 0 {
		out = []string{"main"}
	}
	return out, nil
}

func (d sqliteDialect) Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error) {
	q := fmt.Sprintf(`SELECT name, type FROM %ssqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%%' ORDER BY name`, d.schemaPrefix(dbName))
	_, rows, err := queryRowsCtx(ctx, db, q)
	if err != nil {
		return nil, err
	}
	out := make([]TableInfo, 0, len(rows))
	for _, r := range rows {
		kind := "table"
		if fmt.Sprint(r[1]) == "view" {
			kind = "view"
		}
		out = append(out, TableInfo{Name: fmt.Sprint(r[0]), Rows: nil, SizeBytes: nil, Kind: kind})
	}
	return out, nil
}

func (d sqliteDialect) Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error) {
	prefix := d.schemaPrefix(dbName)
	_, colRows, err := queryRowsCtx(ctx, db, fmt.Sprintf("PRAGMA %stable_info(%s)", prefix, d.Quote(table)))
	if err != nil {
		return nil, err
	}
	cols := make([]ColumnInfo, 0, len(colRows))
	autoInc := false
	// 取 DDL 判断 INTEGER PRIMARY KEY AUTOINCREMENT
	var ddl string
	_, ddlRows, derr := queryRowsCtx(ctx, db,
		fmt.Sprintf(`SELECT IFNULL(sql,'') FROM %ssqlite_master WHERE name = ? AND type IN ('table','view')`, prefix), table)
	if derr == nil && len(ddlRows) > 0 {
		ddl = fmt.Sprint(ddlRows[0][0])
		autoInc = strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT")
	}
	for _, r := range colRows {
		notnull := fmt.Sprint(r[3]) == "1"
		pk := 0
		fmt.Sscan(fmt.Sprint(r[5]), &pk)
		typ := strings.ToUpper(fmt.Sprint(r[2]))
		extra := ""
		if pk > 0 && strings.Contains(typ, "INT") {
			extra = "rowid auto"
			if autoInc {
				extra = "autoincrement"
			}
		}
		var def *string
		if r[4] != nil {
			s := fmt.Sprint(r[4])
			def = &s
		}
		ci := ColumnInfo{
			Name: fmt.Sprint(r[1]), Type: typ, Nullable: !notnull,
			Default: def, Extra: extra,
		}
		if pk > 0 {
			ci.Key = "PRI"
		}
		cols = append(cols, ci)
	}

	indexes := []IndexInfo{}
	_, idxRows, err := queryRowsCtx(ctx, db, fmt.Sprintf("PRAGMA %sindex_list(%s)", prefix, d.Quote(table)))
	if err == nil {
		for _, r := range idxRows {
			if len(r) < 3 {
				continue
			}
			name := fmt.Sprint(r[1])
			unique := fmt.Sprint(r[2]) == "1"
			var colsTxt []string
			_, infoRows, ierr := queryRowsCtx(ctx, db, fmt.Sprintf("PRAGMA %sindex_info(%s)", prefix, d.Quote(name)))
			if ierr == nil {
				for _, ir := range infoRows {
					if len(ir) > 2 {
						colsTxt = append(colsTxt, fmt.Sprint(ir[2]))
					}
				}
			}
			indexes = append(indexes, IndexInfo{Name: name, Columns: strings.Join(colsTxt, ", "), Unique: unique})
		}
	}
	return &SchemaInfo{Columns: cols, Indexes: indexes, CreateSQL: ddl}, nil
}

func (sqliteDialect) SupportsCreateDB() bool { return false }

func (sqliteDialect) CreateDatabaseSQL(string, string) (string, error) {
	return "", fmt.Errorf("SQLite 是单文件数据库，不支持建库（可直接用新路径新建连接）")
}

func (sqliteDialect) DropDatabaseSQL(string) (string, error) {
	return "", fmt.Errorf("SQLite 是单文件数据库，不支持删库（可直接删除文件）")
}

func (d sqliteDialect) TruncateSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DELETE FROM " + d.Qualify(dbName, table), nil
}

func (d sqliteDialect) DropTableSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DROP TABLE " + d.Qualify(dbName, table), nil
}

func (d sqliteDialect) PageSQL(dbName, table, where, order, dir string, limit, offset int) string {
	q := "SELECT * FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	if order != "" {
		if dir == "" {
			dir = "ASC"
		}
		q += " ORDER BY " + d.Quote(order) + " " + dir
	}
	return fmt.Sprintf("%s LIMIT %d OFFSET %d", q, limit, offset)
}

func (d sqliteDialect) CountSQL(dbName, table, where string) string {
	q := "SELECT COUNT(*) FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	return q
}
