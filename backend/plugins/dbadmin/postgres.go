package dbadmin

// PostgreSQL 方言：pgx stdlib + pg_catalog / information_schema。
// 「库」= 真实 database；「表」名统一为 schema.table，跨 schema 可见。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type postgresDialect struct{}

func (postgresDialect) Kind() string        { return "postgres" }
func (postgresDialect) DefaultPort() string { return "5432" }

func (postgresDialect) Validate(c *Connection) error {
	if err := validateHostPort(c.Host, c.Port); err != nil {
		return err
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("用户名必填")
	}
	return nil
}

func (postgresDialect) DefaultDB(*Connection) string  { return "postgres" }
func (postgresDialect) MaintainDB(*Connection) string { return "postgres" }

func (postgresDialect) Open(c *Connection, db, plainPwd string) (*sql.DB, error) {
	u := &url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, c.Port), Path: "/" + db}
	if plainPwd != "" {
		u.User = url.UserPassword(c.User, plainPwd)
	} else {
		u.User = url.User(c.User)
	}
	q := url.Values{}
	q.Set("sslmode", "prefer")
	q.Set("connect_timeout", "10")
	q.Set("application_name", "zjpanel")
	u.RawQuery = q.Encode()
	dbh, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, err
	}
	dbh.SetMaxOpenConns(4)
	dbh.SetMaxIdleConns(2)
	dbh.SetConnMaxLifetime(5 * time.Minute)
	return dbh, nil
}

func (postgresDialect) OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return postgresDialect{}.Open(c, db, plainPwd)
}

func (postgresDialect) Quote(ident string) string {
	ident = strings.TrimSpace(ident)
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d postgresDialect) Qualify(db, table string) string {
	table = strings.TrimSpace(table)
	if i := strings.Index(table, "."); i > 0 {
		return d.Quote(table[:i]) + "." + d.Quote(table[i+1:])
	}
	if db != "" {
		return d.Quote(db) + "." + d.Quote(table)
	}
	return d.Quote(table)
}

func (postgresDialect) Ph(i int) string { return "$" + itoa(i) }

func (postgresDialect) ListDatabases(ctx context.Context, db *sql.DB, _ *Connection) ([]string, error) {
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprint(r[0]))
	}
	return out, nil
}

func (postgresDialect) Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error) {
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT n.nspname, c.relname, c.reltuples::bigint,
		        COALESCE(pg_total_relation_size(c.oid), 0),
		        COALESCE(obj_description(c.oid), ''), c.relkind::text
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r','p','v','m','f')
		   AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
		 ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, err
	}
	out := make([]TableInfo, 0, len(rows))
	for _, r := range rows {
		kind := "table"
		switch fmt.Sprint(r[5]) {
		case "v", "m":
			kind = "view"
		}
		var n int64
		if _, err := fmt.Sscan(fmt.Sprint(r[2]), &n); err != nil || n < 0 {
			n = 0
		}
		out = append(out, TableInfo{
			Name: fmt.Sprint(r[0]) + "." + fmt.Sprint(r[1]), Schema: fmt.Sprint(r[0]), Rows: n,
			SizeBytes: r[3], Comment: fmt.Sprint(r[4]), Kind: kind,
		})
	}
	return out, nil
}

func (d postgresDialect) Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error) {
	schema, rel := splitSchema(table)
	_, colRows, err := queryRowsCtx(ctx, db,
		`SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		        pg_get_expr(ad.adbin, ad.adrelid), a.attnum,
		        COALESCE(col_description(a.attrelid, a.attnum), ''),
		        COALESCE(a.attidentity::text, '')
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		 WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, schema, rel)
	if err != nil {
		return nil, err
	}
	cols := make([]ColumnInfo, 0, len(colRows))
	for _, r := range colRows {
		var def *string
		if r[3] != nil {
			s := fmt.Sprint(r[3])
			def = &s
		}
		extra := ""
		switch fmt.Sprint(r[6]) {
		case "a":
			extra = "identity always"
		case "d":
			extra = "identity by default"
		}
		cols = append(cols, ColumnInfo{
			Name: fmt.Sprint(r[0]), Type: fmt.Sprint(r[1]),
			Nullable: fmt.Sprint(r[2]) == "false", Default: def, Extra: extra,
			Comment: fmt.Sprint(r[5]),
		})
	}

	_, pkRows, err := queryRowsCtx(ctx, db,
		`SELECT kcu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON kcu.constraint_name = tc.constraint_name AND kcu.constraint_schema = tc.constraint_schema
		 WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		 ORDER BY kcu.ordinal_position`, schema, rel)
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
	_, idxRows, err := queryRowsCtx(ctx, db,
		`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname`, schema, rel)
	if err == nil {
		for _, r := range idxRows {
			def := fmt.Sprint(r[1])
			indexes = append(indexes, IndexInfo{
				Name: fmt.Sprint(r[0]), Columns: indexColumns(def),
				Unique: strings.Contains(strings.ToUpper(def), "UNIQUE INDEX"), Definition: def,
			})
		}
	}
	return &SchemaInfo{
		Columns: cols, Indexes: indexes,
		CreateSQL: generateDDL(d, dbName, table, cols),
	}, nil
}

// splitSchema 拆 "schema.table"（表名本身不含点号时按首点切分）。
func splitSchema(table string) (string, string) {
	if i := strings.Index(table, "."); i > 0 {
		return table[:i], table[i+1:]
	}
	return "public", table
}

// indexColumns 从 CREATE INDEX 语句里抽取括号内的列清单。
func indexColumns(def string) string {
	i := strings.Index(def, "(")
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(def); j++ {
		switch def[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(def[i+1 : j])
			}
		}
	}
	return ""
}

func (postgresDialect) SupportsCreateDB() bool { return true }

func (postgresDialect) CreateDatabaseSQL(name, _ string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	return `CREATE DATABASE "` + name + `"`, nil
}

func (postgresDialect) DropDatabaseSQL(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("库名含非法字符")
	}
	return `DROP DATABASE "` + name + `"`, nil
}

func (d postgresDialect) TruncateSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "TRUNCATE TABLE " + d.Qualify(dbName, table), nil
}

func (d postgresDialect) DropTableSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DROP TABLE " + d.Qualify(dbName, table), nil
}

func (d postgresDialect) PageSQL(dbName, table, where, order, dir string, limit, offset int) string {
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

func (d postgresDialect) CountSQL(dbName, table, where string) string {
	q := "SELECT COUNT(*) FROM " + d.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	return q
}
