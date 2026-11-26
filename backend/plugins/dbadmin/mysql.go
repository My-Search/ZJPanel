package dbadmin

// MySQL / MariaDB 方言：go-sql-driver/mysql + information_schema。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
)

type mysqlDialect struct{}

func (mysqlDialect) Kind() string        { return "mysql" }
func (mysqlDialect) DefaultPort() string { return "3306" }

func (mysqlDialect) Validate(c *Connection) error {
	if err := validateHostPort(c.Host, c.Port); err != nil {
		return err
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("用户名必填")
	}
	return nil
}

func (mysqlDialect) DefaultDB(*Connection) string  { return "information_schema" }
func (mysqlDialect) MaintainDB(*Connection) string { return "information_schema" }

func (d mysqlDialect) Open(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return openMySQL(c, db, plainPwd, 30*time.Second)
}

func (d mysqlDialect) OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return openMySQL(c, db, plainPwd, 5*time.Minute)
}

func openMySQL(c *Connection, db, plainPwd string, rwTimeout time.Duration) (*sql.DB, error) {
	cfg := mysqldrv.NewConfig()
	cfg.User = c.User
	cfg.Passwd = plainPwd
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, c.Port)
	cfg.DBName = db
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	cfg.ParseTime = true
	cfg.Loc = time.Local
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = rwTimeout
	cfg.WriteTimeout = rwTimeout
	cfg.AllowAllFiles = false
	dbh, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	dbh.SetMaxOpenConns(4)
	dbh.SetMaxIdleConns(2)
	dbh.SetConnMaxLifetime(5 * time.Minute)
	return dbh, nil
}

func (mysqlDialect) Quote(ident string) string {
	ident = strings.TrimSpace(ident)
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func (d mysqlDialect) Qualify(db, table string) string {
	if db == "" {
		return d.Quote(table)
	}
	return d.Quote(db) + "." + d.Quote(table)
}

func (mysqlDialect) Ph(int) string { return "?" }

func (mysqlDialect) ListDatabases(ctx context.Context, db *sql.DB, _ *Connection) ([]string, error) {
	_, rows, err := queryRowsCtx(ctx, db, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if s, ok := r[0].(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (mysqlDialect) Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error) {
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT TABLE_NAME, TABLE_ROWS, IFNULL(DATA_LENGTH+INDEX_LENGTH,0), IFNULL(TABLE_COMMENT,''), TABLE_TYPE
		 FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, dbName)
	if err != nil {
		return nil, err
	}
	out := make([]TableInfo, 0, len(rows))
	for _, r := range rows {
		kind := "table"
		if s, _ := r[4].(string); strings.Contains(strings.ToUpper(s), "VIEW") {
			kind = "view"
		}
		out = append(out, TableInfo{Name: fmt.Sprint(r[0]), Rows: r[1], SizeBytes: r[2], Comment: fmt.Sprint(r[3]), Kind: kind})
	}
	return out, nil
}

func (d mysqlDialect) Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error) {
	_, colRows, err := queryRowsCtx(ctx, db,
		`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLUMN_DEFAULT, EXTRA, IFNULL(COLUMN_COMMENT,'')
		 FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		 ORDER BY ORDINAL_POSITION`, dbName, table)
	if err != nil {
		return nil, err
	}
	cols := make([]ColumnInfo, 0, len(colRows))
	for _, r := range colRows {
		nullable := strings.EqualFold(fmt.Sprint(r[2]), "YES")
		var def *string
		if r[4] != nil {
			s := fmt.Sprint(r[4])
			def = &s
		}
		cols = append(cols, ColumnInfo{
			Name: fmt.Sprint(r[0]), Type: fmt.Sprint(r[1]), Nullable: nullable,
			Key: fmt.Sprint(r[3]), Default: def, Extra: fmt.Sprint(r[5]), Comment: fmt.Sprint(r[6]),
		})
	}

	idxCols, err := queryIndexColumns(ctx, db, dbName, table)
	if err != nil {
		// 部分老版本不支持 expression 列，退回原查询
		idxCols, _ = queryIndexColumnsLegacy(ctx, db, dbName, table)
	}

	var createSQL string
	_, crRows, err := queryRowsCtx(ctx, db, "SHOW CREATE TABLE "+d.Qualify(dbName, table))
	if err == nil && len(crRows) > 0 && len(crRows[0]) > 1 {
		if s, ok := crRows[0][1].(string); ok {
			createSQL = s
		}
	}
	return &SchemaInfo{Columns: cols, Indexes: idxCols, CreateSQL: createSQL}, nil
}

func queryIndexColumns(ctx context.Context, db *sql.DB, dbName, table string) ([]IndexInfo, error) {
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT INDEX_NAME, IFNULL(GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX),''),
		        IF(NON_UNIQUE = 0, 1, 0), INDEX_TYPE
		 FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		 GROUP BY INDEX_NAME, NON_UNIQUE, INDEX_TYPE ORDER BY INDEX_NAME`, dbName, table)
	if err != nil {
		return nil, err
	}
	out := make([]IndexInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, IndexInfo{
			Name: fmt.Sprint(r[0]), Columns: fmt.Sprint(r[1]),
			Unique: fmt.Sprint(r[2]) == "1", Type: fmt.Sprint(r[3]),
		})
	}
	return out, nil
}

func queryIndexColumnsLegacy(ctx context.Context, db *sql.DB, dbName, table string) ([]IndexInfo, error) {
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT INDEX_NAME, COLUMN_NAME, NON_UNIQUE, INDEX_TYPE
		 FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, dbName, table)
	if err != nil {
		return nil, err
	}
	out := make([]IndexInfo, 0, len(rows))
	byName := map[string]*IndexInfo{}
	order := []string{}
	for _, r := range rows {
		n := fmt.Sprint(r[0])
		ix, ok := byName[n]
		if !ok {
			ix = &IndexInfo{Name: n, Unique: fmt.Sprint(r[2]) == "0", Type: fmt.Sprint(r[3])}
			byName[n] = ix
			order = append(order, n)
		}
		if c := fmt.Sprint(r[1]); c != "" {
			if ix.Columns != "" {
				ix.Columns += ", "
			}
			ix.Columns += c
		}
	}
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

func (mysqlDialect) SupportsCreateDB() bool { return true }

func (mysqlDialect) CreateDatabaseSQL(name, charset string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	if charset == "" {
		charset = "utf8mb4"
	}
	switch strings.ToLower(charset) {
	case "utf8mb4", "utf8", "utf8mb3", "latin1", "gbk", "gb18030", "big5", "ascii":
	default:
		return "", fmt.Errorf("不支持的字符集: %s", charset)
	}
	return fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET %s", name, charset), nil
}

func (mysqlDialect) DropDatabaseSQL(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	return "DROP DATABASE `" + name + "`", nil
}

func (d mysqlDialect) TruncateSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "TRUNCATE TABLE " + d.Qualify(dbName, table), nil
}

func (d mysqlDialect) DropTableSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DROP TABLE " + d.Qualify(dbName, table), nil
}

func (d mysqlDialect) PageSQL(dbName, table, where, order, dir string, limit, offset int) string {
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

func (d mysqlDialect) CountSQL(dbName, table, where string) string {
	q := "SELECT COUNT(*) FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	return q
}
