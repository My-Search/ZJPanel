package dbadmin

// SQL Server 方言：go-mssqldb + sys / INFORMATION_SCHEMA 目录视图。
// 「库」= 实例上的 database；「表」名统一为 schema.table。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

type mssqlDialect struct{}

func (mssqlDialect) Kind() string        { return "sqlserver" }
func (mssqlDialect) DefaultPort() string { return "1433" }

func (mssqlDialect) Validate(c *Connection) error {
	if err := validateHostPort(c.Host, c.Port); err != nil {
		return err
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("用户名必填（当前仅支持 SQL 登录）")
	}
	return nil
}

func (mssqlDialect) DefaultDB(c *Connection) string {
	if strings.TrimSpace(c.Database) != "" {
		return strings.TrimSpace(c.Database)
	}
	return "master"
}

func (mssqlDialect) MaintainDB(*Connection) string { return "master" }

func (mssqlDialect) Open(c *Connection, db, plainPwd string) (*sql.DB, error) {
	u := &url.URL{Scheme: "sqlserver", Host: net.JoinHostPort(c.Host, c.Port)}
	if plainPwd != "" {
		u.User = url.UserPassword(c.User, plainPwd)
	} else {
		u.User = url.User(c.User)
	}
	q := url.Values{}
	q.Set("database", db)
	q.Set("encrypt", "true")
	q.Set("TrustServerCertificate", "true")
	q.Set("connection timeout", "10")
	u.RawQuery = q.Encode()
	dbh, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
	dbh.SetMaxOpenConns(4)
	dbh.SetMaxIdleConns(2)
	dbh.SetConnMaxLifetime(5 * time.Minute)
	return dbh, nil
}

func (mssqlDialect) OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return mssqlDialect{}.Open(c, db, plainPwd)
}

func (mssqlDialect) Quote(ident string) string {
	ident = strings.TrimSpace(ident)
	return "[" + strings.ReplaceAll(ident, "]", "]]") + "]"
}

func (d mssqlDialect) Qualify(db, table string) string {
	table = strings.TrimSpace(table)
	if i := strings.Index(table, "."); i > 0 {
		return d.Quote(table[:i]) + "." + d.Quote(table[i+1:])
	}
	if db != "" {
		return d.Quote(db) + "." + d.Quote(table)
	}
	return d.Quote(table)
}

func (mssqlDialect) Ph(i int) string { return "@p" + itoa(i) }

func (mssqlDialect) ListDatabases(ctx context.Context, db *sql.DB, _ *Connection) ([]string, error) {
	_, rows, err := queryRowsCtx(ctx, db, "SELECT name FROM sys.databases ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprint(r[0]))
	}
	return out, nil
}

func (d mssqlDialect) Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error) {
	prefix := d.Quote(dbName) + "."
	out := []TableInfo{}
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT s.name, t.name, COALESCE(SUM(p.rows),0),
		        COALESCE(CAST(ep.value AS nvarchar(400)),'')
		 FROM `+prefix+`sys.tables t
		 JOIN `+prefix+`sys.schemas s ON s.schema_id = t.schema_id
		 LEFT JOIN `+prefix+`sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0,1)
		 LEFT JOIN `+prefix+`sys.extended_properties ep
		        ON ep.class = 1 AND ep.major_id = t.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		 GROUP BY s.name, t.name, ep.value
		 ORDER BY s.name, t.name`)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, TableInfo{
			Name: fmt.Sprint(r[0]) + "." + fmt.Sprint(r[1]), Schema: fmt.Sprint(r[0]), Rows: r[2],
			Comment: fmt.Sprint(r[3]), Kind: "table",
		})
	}
	_, vrows, verr := queryRowsCtx(ctx, db,
		`SELECT s.name, v.name FROM `+prefix+`sys.views v
		 JOIN `+prefix+`sys.schemas s ON s.schema_id = v.schema_id
		 ORDER BY s.name, v.name`)
	if verr == nil {
		for _, r := range vrows {
			out = append(out, TableInfo{Name: fmt.Sprint(r[0]) + "." + fmt.Sprint(r[1]), Schema: fmt.Sprint(r[0]), Rows: nil, Kind: "view"})
		}
	}
	return out, nil
}

func (d mssqlDialect) Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error) {
	schema, rel := splitSchema(table)
	prefix := d.Quote(dbName) + "."
	_, colRows, err := queryRowsCtx(ctx, db,
		`SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, ORDINAL_POSITION,
		        CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE
		 FROM `+prefix+`INFORMATION_SCHEMA.COLUMNS
		 WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2
		 ORDER BY ORDINAL_POSITION`, schema, rel)
	if err != nil {
		return nil, err
	}
	cols := make([]ColumnInfo, 0, len(colRows))
	for _, r := range colRows {
		typ := fmt.Sprint(r[1])
		switch typ {
		case "char", "varchar", "nchar", "nvarchar", "binary", "varbinary":
			if r[5] != nil {
				l := fmt.Sprint(r[5])
				if l == "-1" {
					l = "max"
				}
				typ += "(" + l + ")"
			}
		case "decimal", "numeric":
			if r[6] != nil {
				p := fmt.Sprint(r[6])
				if r[7] != nil {
					p += "," + fmt.Sprint(r[7])
				}
				typ += "(" + p + ")"
			}
		}
		var def *string
		if r[3] != nil {
			s := fmt.Sprint(r[3])
			def = &s
		}
		cols = append(cols, ColumnInfo{
			Name: fmt.Sprint(r[0]), Type: typ,
			Nullable: strings.EqualFold(fmt.Sprint(r[2]), "YES"), Default: def,
		})
	}

	_, pkRows, err := queryRowsCtx(ctx, db,
		`SELECT kcu.COLUMN_NAME
		 FROM `+prefix+`INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
		 JOIN `+prefix+`INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
		   ON kcu.CONSTRAINT_NAME = tc.CONSTRAINT_NAME AND kcu.TABLE_SCHEMA = tc.TABLE_SCHEMA
		 WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY' AND tc.TABLE_SCHEMA = @p1 AND tc.TABLE_NAME = @p2
		 ORDER BY kcu.ORDINAL_POSITION`, schema, rel)
	if err == nil {
		for _, r := range pkRows {
			name := fmt.Sprint(r[0])
			for i := range cols {
				if cols[i].Name == name {
					cols[i].Key = "PRI"
				}
			}
		}
	}

	indexes := []IndexInfo{}
	byName := map[string]*IndexInfo{}
	order := []string{}
	_, idxRows, err := queryRowsCtx(ctx, db,
		`SELECT i.name, c.name, i.is_unique, i.type_desc, ic.key_ordinal
		 FROM `+prefix+`sys.indexes i
		 JOIN `+prefix+`sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		 JOIN `+prefix+`sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		 JOIN `+prefix+`sys.tables t ON t.object_id = i.object_id
		 JOIN `+prefix+`sys.schemas s ON s.schema_id = t.schema_id
		 WHERE s.name = @p1 AND t.name = @p2 AND i.name IS NOT NULL
		 ORDER BY i.name, ic.key_ordinal`, schema, rel)
	if err == nil {
		for _, r := range idxRows {
			name := fmt.Sprint(r[0])
			ix, ok := byName[name]
			if !ok {
				ix = &IndexInfo{Name: name, Unique: fmt.Sprint(r[2]) == "true" || fmt.Sprint(r[2]) == "1", Type: fmt.Sprint(r[3])}
				byName[name] = ix
				order = append(order, name)
			}
			if c := fmt.Sprint(r[1]); c != "" {
				if ix.Columns != "" {
					ix.Columns += ", "
				}
				ix.Columns += c
			}
		}
	}
	for _, n := range order {
		indexes = append(indexes, *byName[n])
	}
	return &SchemaInfo{Columns: cols, Indexes: indexes, CreateSQL: generateDDL(d, dbName, table, cols)}, nil
}

func (mssqlDialect) SupportsCreateDB() bool { return true }

func (mssqlDialect) CreateDatabaseSQL(name, _ string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	return "CREATE DATABASE [" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

func (mssqlDialect) DropDatabaseSQL(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	return "DROP DATABASE [" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

func (d mssqlDialect) TruncateSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "TRUNCATE TABLE " + d.Qualify(dbName, table), nil
}

func (d mssqlDialect) DropTableSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DROP TABLE " + d.Qualify(dbName, table), nil
}

func (d mssqlDialect) PageSQL(dbName, table, where, order, dir string, limit, offset int) string {
	q := "SELECT * FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	if order != "" {
		if dir == "" {
			dir = "ASC"
		}
		q += " ORDER BY " + d.Quote(order) + " " + dir
	} else {
		q += " ORDER BY (SELECT NULL)"
	}
	return fmt.Sprintf("%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", q, offset, limit)
}

func (d mssqlDialect) CountSQL(dbName, table, where string) string {
	q := "SELECT COUNT(*) FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	return q
}
