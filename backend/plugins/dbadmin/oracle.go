package dbadmin

// Oracle 方言：sijms/go-ora（纯 Go，无 CGO）。
// Oracle 没有「库」概念，这里把 owner/schema 映射为「库」；
// 建库/删库按实例级操作处理（不支持），表名保持 owner.table 引用。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

type oracleDialect struct{}

func (oracleDialect) Kind() string        { return "oracle" }
func (oracleDialect) DefaultPort() string { return "1521" }

func (oracleDialect) Validate(c *Connection) error {
	if err := validateHostPort(c.Host, c.Port); err != nil {
		return err
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("用户名必填")
	}
	if strings.TrimSpace(c.Database) == "" {
		return fmt.Errorf("Oracle 需填写服务名（Service Name 或 SID）")
	}
	return nil
}

func (oracleDialect) DefaultDB(*Connection) string  { return "" }
func (oracleDialect) MaintainDB(*Connection) string { return "" }

func (oracleDialect) Open(c *Connection, _, plainPwd string) (*sql.DB, error) {
	u := &url.URL{
		Scheme: "oracle",
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + strings.TrimSpace(c.Database),
	}
	if plainPwd != "" {
		u.User = url.UserPassword(c.User, plainPwd)
	} else {
		u.User = url.User(c.User)
	}
	dbh, err := sql.Open("oracle", u.String())
	if err != nil {
		return nil, err
	}
	dbh.SetMaxOpenConns(4)
	dbh.SetMaxIdleConns(2)
	dbh.SetConnMaxLifetime(5 * time.Minute)
	return dbh, nil
}

func (oracleDialect) OpenLong(c *Connection, db, plainPwd string) (*sql.DB, error) {
	return oracleDialect{}.Open(c, db, plainPwd)
}

func (oracleDialect) Quote(ident string) string {
	ident = strings.TrimSpace(ident)
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d oracleDialect) Qualify(db, table string) string {
	table = strings.TrimSpace(table)
	if i := strings.Index(table, "."); i > 0 {
		return d.Quote(table[:i]) + "." + d.Quote(table[i+1:])
	}
	if db != "" {
		return d.Quote(db) + "." + d.Quote(table)
	}
	return d.Quote(table)
}

func (oracleDialect) Ph(i int) string { return ":" + itoa(i) }

func (oracleDialect) ListDatabases(ctx context.Context, db *sql.DB, _ *Connection) ([]string, error) {
	_, rows, err := queryRowsCtx(ctx, db, "SELECT username FROM all_users ORDER BY username")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprint(r[0]))
	}
	return out, nil
}

func (o oracleDialect) Tables(ctx context.Context, db *sql.DB, dbName string) ([]TableInfo, error) {
	out := []TableInfo{}
	_, rows, err := queryRowsCtx(ctx, db,
		`SELECT t.table_name, t.num_rows, c.comments
		 FROM all_tables t
		 LEFT JOIN all_tab_comments c ON c.owner = t.owner AND c.table_name = t.table_name
		 WHERE t.owner = :1 ORDER BY t.table_name`, dbName)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, TableInfo{
			Name: fmt.Sprint(r[0]), Schema: dbName, Rows: r[1],
			Comment: fmt.Sprint(r[2]), Kind: "table",
		})
	}
	_, vrows, verr := queryRowsCtx(ctx, db,
		`SELECT v.view_name FROM all_views v WHERE v.owner = :1 ORDER BY v.view_name`, dbName)
	if verr == nil {
		for _, r := range vrows {
			out = append(out, TableInfo{Name: fmt.Sprint(r[0]), Schema: dbName, Rows: nil, Kind: "view"})
		}
	}
	return out, nil
}

func (o oracleDialect) Schema(ctx context.Context, db *sql.DB, dbName, table string) (*SchemaInfo, error) {
	rel := table
	if i := strings.Index(table, "."); i > 0 { // 兼容 owner.table 形式
		dbName, rel = table[:i], table[i+1:]
	}
	_, colRows, err := queryRowsCtx(ctx, db,
		`SELECT c.column_name, c.data_type, c.data_length, c.data_precision, c.data_scale,
		        c.nullable, c.data_default, c.column_id, NVL(cc.comments,'')
		 FROM all_tab_columns c
		 LEFT JOIN all_col_comments cc
		   ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
		 WHERE c.owner = :1 AND c.table_name = :2
		 ORDER BY c.column_id`, dbName, rel)
	if err != nil {
		return nil, err
	}
	cols := make([]ColumnInfo, 0, len(colRows))
	for _, r := range colRows {
		typ := fmt.Sprint(r[1])
		switch typ {
		case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR", "RAW":
			if r[2] != nil {
				typ += "(" + fmt.Sprint(r[2]) + ")"
			}
		case "NUMBER":
			if r[3] != nil {
				typ += "(" + fmt.Sprint(r[3])
				if r[4] != nil {
					typ += "," + fmt.Sprint(r[4])
				}
				typ += ")"
			}
		}
		var def *string
		if r[6] != nil {
			s := strings.TrimSpace(fmt.Sprint(r[6]))
			if s != "" {
				def = &s
			}
		}
		cols = append(cols, ColumnInfo{
			Name: fmt.Sprint(r[0]), Type: typ,
			Nullable: strings.EqualFold(fmt.Sprint(r[5]), "Y"), Default: def,
			Comment: fmt.Sprint(r[8]),
		})
	}

	_, pkRows, err := queryRowsCtx(ctx, db,
		`SELECT cc.column_name
		 FROM all_constraints c
		 JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		 WHERE c.constraint_type = 'P' AND c.owner = :1 AND c.table_name = :2
		 ORDER BY cc.position`, dbName, rel)
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
		`SELECT i.index_name, c.column_name, i.uniqueness, i.index_type
		 FROM all_indexes i
		 JOIN all_ind_columns c ON c.index_owner = i.owner AND c.index_name = i.index_name
		 WHERE i.table_owner = :1 AND i.table_name = :2
		 ORDER BY i.index_name, c.column_position`, dbName, rel)
	if err == nil {
		for _, r := range idxRows {
			name := fmt.Sprint(r[0])
			ix, ok := byName[name]
			if !ok {
				ix = &IndexInfo{Name: name, Unique: strings.EqualFold(fmt.Sprint(r[2]), "UNIQUE"), Type: fmt.Sprint(r[3])}
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

	createSQL := ""
	_, ddlRows, derr := queryRowsCtx(ctx, db,
		`SELECT DBMS_METADATA.GET_DDL('TABLE', :1, :2) FROM dual`, rel, dbName)
	if derr == nil && len(ddlRows) > 0 && ddlRows[0][0] != nil {
		createSQL = fmt.Sprint(ddlRows[0][0])
	}
	if strings.TrimSpace(createSQL) == "" {
		createSQL = generateDDL(o, dbName, rel, cols)
	}
	return &SchemaInfo{Columns: cols, Indexes: indexes, CreateSQL: createSQL}, nil
}

func (oracleDialect) SupportsCreateDB() bool { return false }

func (oracleDialect) CreateDatabaseSQL(string, string) (string, error) {
	return "", fmt.Errorf("Oracle 数据库由实例创建，面板不支持在线建库")
}

func (oracleDialect) DropDatabaseSQL(string) (string, error) {
	return "", fmt.Errorf("Oracle 数据库由实例管理，面板不支持在线删库")
}

func (o oracleDialect) TruncateSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "TRUNCATE TABLE " + o.Qualify(dbName, table), nil
}

func (o oracleDialect) DropTableSQL(dbName, table string) (string, error) {
	if !validName(table) {
		return "", fmt.Errorf("表名含非法字符")
	}
	return "DROP TABLE " + o.Qualify(dbName, table), nil
}

func (o oracleDialect) PageSQL(dbName, table, where, order, dir string, limit, offset int) string {
	q := "SELECT * FROM " + o.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	if order != "" {
		if dir == "" {
			dir = "ASC"
		}
		q += " ORDER BY " + o.Quote(order) + " " + dir
	} else {
		q += " ORDER BY 1"
	}
	return fmt.Sprintf("%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", q, offset, limit)
}

func (o oracleDialect) CountSQL(dbName, table, where string) string {
	q := "SELECT COUNT(*) FROM " + o.Qualify(dbName, table)
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	return q
}
