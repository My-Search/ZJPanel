// Package dbadmin 内置插件：数据库管理（轻量 Navicat / DBeaver）。
// 支持 MySQL/MariaDB、PostgreSQL、SQLite、SQL Server、Oracle、Redis、MongoDB：
//   - 关系型：库表浏览、结构/索引/DDL、分页数据、行级增删改、SQL 控制台、SQL 文件导入
//   - Redis：db0..dbN 键空间浏览、值预览、删键/改 TTL、命令控制台
//   - MongoDB：库/集合、文档分页浏览、按 _id 替换/删除、JSON 命令控制台
//
// 凭据 AES-GCM(v2, AAD) 加密落盘，列表不回传；HTTP 挂 /api/plugin/dbadmin/*（面板登录态）。
package dbadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/pkg"
	sdk "zjpanel/backend/sdk/plugin"
)

const connFile = "connections.json"

const (
	metaTimeout  = 20 * time.Second
	dataTimeout  = 30 * time.Second
	queryTimeout = 60 * time.Second
	fileTimeout  = 5 * time.Minute
)

// Connection 数据库连接配置。Password 落盘为密文，列表接口不回传。
type Connection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"` // mysql/postgres/sqlite/sqlserver/oracle/redis/mongodb
	Host      string `json:"host,omitempty"`
	Port      string `json:"port,omitempty"`
	User      string `json:"user,omitempty"`
	Password  string `json:"password,omitempty"`
	Database  string `json:"database,omitempty"` // 默认库 / Oracle 服务名 / Mongo 认证库 / Redis 序号
	Path      string `json:"path,omitempty"`     // SQLite 文件路径
	Comment   string `json:"comment,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// QueryHistory SQL/命令控制台执行历史（内存环形保留 50 条）。
type QueryHistory struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	SQL      string `json:"sql"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Elapsed  string `json:"elapsed"`
	Rows     int    `json:"rows"`
	RunAt    string `json:"runAt"`
}

type Plugin struct {
	ctx     sdk.Context
	box     *pkg.SecretBox
	mu      sync.Mutex
	conns   map[string]*Connection
	history []QueryHistory
}

func New() sdk.Plugin { return &Plugin{conns: map[string]*Connection{}} }

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:      "dbadmin",
		Name:    "数据库管理",
		Version: "0.2.0",
		Description: "多数据库管理：MySQL / MariaDB / PostgreSQL / SQLite / SQL Server / Oracle / Redis / MongoDB。" +
			"连接管理 / 库表浏览 / 结构与 DDL / 分页数据与行编辑 / SQL 与命令控制台 / SQL 文件导入（凭据加密存储）",
		Permissions: []string{"http"},
		Menu:        []sdk.Menu{{Title: "数据库", Icon: "database", Path: "/"}},
	}
}

func (p *Plugin) OnInit(ctx sdk.Context) error {
	p.ctx = ctx
	key, err := pkg.EnsureSecretKey(filepath.Dir(filepath.Dir(ctx.DataDir()))) // DataDir=/data/plugins-data/<id>，主密钥在 /data/secret.key
	if err != nil {
		return fmt.Errorf("加载面板主密钥失败: %w", err)
	}
	p.box = pkg.NewSecretBox(key)
	p.loadConns()
	p.migrateLegacy()

	// 路由按「具体 → 泛化」顺序注册：派发按注册顺序首中即返回，
	// 尾部通配 DELETE /connections/* 必须最后注册，否则吞掉嵌套 DELETE（删库/删行）。
	api := func(method, path string, h http.HandlerFunc) { ctx.RegisterRoute(method, path, h) }
	api("GET", "/connections", p.apiListConns)
	api("POST", "/connections", p.apiSaveConn)
	api("POST", "/connections/test", p.apiTestForm)
	api("GET", "/connections/*/databases", p.apiDatabases)
	api("POST", "/connections/*/databases", p.apiCreateDatabase)
	api("GET", "/connections/*/databases/*/tables", p.apiTables)
	api("GET", "/connections/*/databases/*/tables/*/schema", p.apiSchema)
	api("POST", "/connections/*/databases/*/tables/*/truncate", p.apiTruncate)
	api("POST", "/connections/*/databases/*/tables/*/drop", p.apiDropTable)
	api("POST", "/connections/*/databases/*/tables/*/rows", p.apiInsertRow)
	api("PUT", "/connections/*/databases/*/tables/*/rows", p.apiUpdateRow)
	api("DELETE", "/connections/*/databases/*/tables/*/rows", p.apiDeleteRow)
	api("POST", "/connections/*/sqlfile", p.apiRunSQLFile)
	api("DELETE", "/connections/*/databases/*", p.apiDropDatabase) // 嵌套 DELETE 之后注册，防止截胡
	api("POST", "/connections/*/test", p.apiTestConn)
	api("DELETE", "/connections/*", p.apiDeleteConn)
	api("GET", "/rows", p.apiRows)
	api("POST", "/query", p.apiQuery)
	api("GET", "/history", p.apiHistory)
	api("GET", "/redis/keys", p.apiRedisKeys)
	api("GET", "/redis/key", p.apiRedisKeyGet)
	api("POST", "/redis/key", p.apiRedisKeySet)
	api("DELETE", "/redis/key", p.apiRedisKeyDel)
	api("POST", "/redis/key/ttl", p.apiRedisKeyTTL)

	ctx.RegisterMCPTool("list_connections", "列出数据库管理插件中的全部连接（类型/名称/主机/库，不含密码）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) { return p.mcpListConns() })
	ctx.Logf("数据库管理插件已启动，共 %d 个连接", len(p.conns))
	return nil
}

func (p *Plugin) OnShutdown() {}

// ---- 类型与校验 ----

func canonType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mysql", "mariadb":
		return "mysql"
	case "postgres", "postgresql", "pg":
		return "postgres"
	case "sqlite", "sqlite3":
		return "sqlite"
	case "sqlserver", "mssql":
		return "sqlserver"
	case "oracle":
		return "oracle"
	case "redis":
		return "redis"
	case "mongodb", "mongo":
		return "mongodb"
	case "":
		return "mysql" // 兼容旧 mysqladmin 连接文件（无 type 字段）
	}
	return ""
}

func defaultPort(kind string) string {
	switch kind {
	case "mysql":
		return "3306"
	case "postgres":
		return "5432"
	case "sqlserver":
		return "1433"
	case "oracle":
		return "1521"
	case "redis":
		return "6379"
	case "mongodb":
		return "27017"
	}
	return ""
}

func (c *Connection) normalize() {
	c.Type = canonType(c.Type)
	c.ID = strings.TrimSpace(strings.ToLower(c.ID))
	c.Name = strings.TrimSpace(c.Name)
	c.Host = strings.TrimSpace(c.Host)
	c.Port = strings.TrimSpace(c.Port)
	c.User = strings.TrimSpace(c.User)
	c.Database = strings.TrimSpace(c.Database)
	c.Path = strings.TrimSpace(c.Path)
	if c.Type != "sqlite" && c.Port == "" {
		c.Port = defaultPort(c.Type)
	}
	if c.Type == "redis" && c.Database == "" {
		c.Database = "0"
	}
}

func (c *Connection) validate() error {
	switch c.Type {
	case "":
		return fmt.Errorf("数据库类型不支持")
	case "sqlite":
		return sqliteDialect{}.Validate(c)
	case "redis", "mongodb":
		if strings.TrimSpace(c.Host) == "" {
			return fmt.Errorf("主机地址必填")
		}
		return validateHostPort(c.Host, c.Port)
	default:
		d, err := sqlDialectFor(c.Type)
		if err != nil {
			return err
		}
		return d.Validate(c)
	}
}

func validateHostPort(host, port string) error {
	if host == "" || strings.ContainsAny(host, " \t\r\n/?#@\\") {
		return fmt.Errorf("主机地址含非法字符")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("端口须为 1-65535 的整数")
	}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-' || c == '.' || c == '$' || c == '#' || c == ' ' || c > 127) {
			return false
		}
	}
	return true
}

func itoa(i int) string { return strconv.Itoa(i) }

// ---- 存取 ----

func (p *Plugin) connPath() string { return filepath.Join(p.ctx.DataDir(), connFile) }

func (p *Plugin) loadConns() {
	b, err := os.ReadFile(p.connPath())
	if err != nil {
		return
	}
	loaded := map[string]*Connection{}
	if err := json.Unmarshal(b, &loaded); err != nil {
		p.ctx.Logf("连接文件解析失败: %v", err)
		return
	}
	for k, c := range loaded {
		if c.ID == "" {
			c.ID = k
		}
		c.Type = canonType(c.Type)
		p.conns[c.ID] = c
	}
}

func (p *Plugin) saveConns() error {
	_ = os.MkdirAll(p.ctx.DataDir(), 0o755)
	b, err := json.MarshalIndent(p.conns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.connPath(), b, 0o600)
}

// migrateLegacy 首次启用时把旧 mysqladmin 插件的 connections.json 迁入本插件。
// 旧插件密码可能仍是 legacy 密文，这里统一解出再按 v2 重新封装（失败则原样保留，
// 读侧 OpenCtx 仍可解 legacy）。
func (p *Plugin) migrateLegacy() {
	if _, err := os.Stat(p.connPath()); err == nil {
		return
	}
	oldPath := filepath.Join(filepath.Dir(p.ctx.DataDir()), "mysqladmin", connFile)
	b, err := os.ReadFile(oldPath)
	if err != nil {
		return
	}
	loaded := map[string]*Connection{}
	if err := json.Unmarshal(b, &loaded); err != nil {
		p.ctx.Logf("旧 mysqladmin 连接文件解析失败，跳过迁移: %v", err)
		return
	}
	migrated := 0
	p.mu.Lock()
	for k, c := range loaded {
		if c.ID == "" {
			c.ID = k
		}
		c.Type = "mysql"
		if c.Port == "" {
			c.Port = "3306"
		}
		if c.Password != "" {
			if plain, err := p.box.OpenCtx(pkg.CtxMySQLConnPwd, c.Password); err == nil {
				if enc, err2 := p.box.SealECtx(pkg.CtxMySQLConnPwd, plain); err2 == nil {
					c.Password = enc
				}
			}
		}
		p.conns[c.ID] = c
		migrated++
	}
	err = p.saveConns()
	p.mu.Unlock()
	if err == nil {
		p.ctx.Logf("已从 mysqladmin 迁移 %d 个数据库连接到 dbadmin", migrated)
	}
}

// openPassword 解密连接密码（legacy/v2 均兼容）。
func (p *Plugin) openPassword(c *Connection) string {
	if c.Password == "" {
		return ""
	}
	plain, err := p.box.OpenCtx(pkg.CtxMySQLConnPwd, c.Password)
	if err != nil {
		p.ctx.Logf("连接 %s 密码解密失败: %v", c.ID, err)
		return ""
	}
	return plain
}

func (p *Plugin) findConn(id string) *Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[id]
}

// openSQL 打开关系型连接（密码解密由调用方传入，便于表单测试未保存的明文密码）。
func (p *Plugin) openSQL(c *Connection, db, plainPwd string, long bool) (*sql.DB, sqlDialect, error) {
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(db) == "" {
		db = d.DefaultDB(c)
	}
	var dbh *sql.DB
	if long {
		dbh, err = d.OpenLong(c, db, plainPwd)
	} else {
		dbh, err = d.Open(c, db, plainPwd)
	}
	if err != nil {
		return nil, nil, err
	}
	return dbh, d, nil
}

// ---- 工具函数 ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func ginH(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// pluginPath 派发层注入的剥离前缀后的子路径。
func pluginPath(r *http.Request) string {
	if p := r.Header.Get("X-Plugin-Path"); p != "" {
		return p
	}
	return r.URL.Path
}

func seg(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n < len(parts) {
		return parts[n]
	}
	return ""
}

func sortedCols(m map[string]any) []string {
	cols := make([]string, 0, len(m))
	for c := range m {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}

func checkCols(m map[string]any) string {
	for col := range m {
		if !validName(col) {
			return "列名非法: " + col
		}
	}
	return ""
}

// queryRowsCtx 通用查询：列名 + 字符串化行数据；保留整型精度（浮点不用科学计数法）。
func queryRowsCtx(ctx context.Context, dbh *sql.DB, q string, args ...any) (cols []string, rows [][]any, err error) {
	rs, err := dbh.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	cols, err = rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		out := make([]any, len(vals))
		for i, v := range vals {
			switch t := v.(type) {
			case []byte:
				out[i] = string(t)
			case nil:
				out[i] = nil
			case int64:
				out[i] = strconv.FormatInt(t, 10)
			case float64:
				out[i] = strconv.FormatFloat(t, 'f', -1, 64)
			case bool:
				out[i] = strconv.FormatBool(t)
			case time.Time:
				out[i] = t.Format("2006-01-02 15:04:05")
			default:
				out[i] = fmt.Sprint(t)
			}
		}
		rows = append(rows, out)
	}
	return cols, rows, rs.Err()
}

func queryRows(dbh *sql.DB, q string, args ...any) (cols []string, rows [][]any, err error) {
	return queryRowsCtx(context.Background(), dbh, q, args...)
}

// ---- 连接管理 API ----

func (p *Plugin) apiListConns(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	ids := make([]string, 0, len(p.conns))
	for id := range p.conns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Connection, 0, len(ids))
	for _, id := range ids {
		c := *p.conns[id]
		c.Password = "" // 不回传密码（密文也不回传）
		out = append(out, &c)
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) apiSaveConn(w http.ResponseWriter, r *http.Request) {
	var c Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.normalize()
	if c.ID == "" || c.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标识/数据库类型必填"})
		return
	}
	if !validName(c.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标识只能包含字母/数字/短横线/下划线"})
		return
	}
	if err := c.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.mu.Lock()
	old, exists := p.conns[c.ID]
	reusePwd := false
	if exists {
		if old.Type != "" && old.Type != c.Type {
			p.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "连接类型不可修改，请新建连接"})
			return
		}
		if c.Password == "" {
			c.Password = old.Password
			reusePwd = true
		} else if c.Password == old.Password {
			reusePwd = true // 已是密文，避免二次加密
		}
		if c.CreatedAt == "" {
			c.CreatedAt = old.CreatedAt
		}
	}
	if c.CreatedAt == "" {
		c.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if !reusePwd && c.Password != "" {
		enc, err := p.box.SealECtx(pkg.CtxMySQLConnPwd, c.Password)
		if err != nil {
			p.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		c.Password = enc
	}
	p.conns[c.ID] = &c
	err := p.saveConns()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.ctx.Logf("数据库连接已保存: %s (%s %s)", c.ID, c.Type, c.Host)
	out := c
	out.Password = ""
	writeJSON(w, http.StatusOK, ginH("ok", true, "connection", &out))
}

func (p *Plugin) apiDeleteConn(w http.ResponseWriter, r *http.Request) {
	// 此路由为最泛化尾通配，嵌套路由（删库/删行）必须先注册命中。
	id := seg(strings.Trim(pluginPath(r), "/"), 1)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少连接 ID"})
		return
	}
	p.mu.Lock()
	delete(p.conns, id)
	err := p.saveConns()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

// testConn 探活：关系型执行 SELECT 1；Redis PING；Mongo Ping。
func (p *Plugin) testConn(c *Connection, plainPwd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switch c.Type {
	case "redis":
		cli, err := openRedis(c, redisDBIndex(c.Database), plainPwd)
		if err != nil {
			return err
		}
		defer cli.Close()
		return cli.Ping(ctx).Err()
	case "mongodb":
		cli, err := openMongo(c, plainPwd)
		if err != nil {
			return err
		}
		defer cli.Disconnect(context.Background())
		return nil
	default:
		dbh, _, err := p.openSQL(c, "", plainPwd, false)
		if err != nil {
			return err
		}
		defer dbh.Close()
		var one int
		probe := "SELECT 1"
		if c.Type == "oracle" {
			probe = "SELECT 1 FROM dual"
		}
		return dbh.QueryRowContext(ctx, probe).Scan(&one)
	}
}

func (p *Plugin) apiTestForm(w http.ResponseWriter, r *http.Request) {
	var c Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.normalize()
	if err := c.validate(); err != nil {
		writeJSON(w, http.StatusOK, ginH("ok", false, "error", err.Error()))
		return
	}
	terr := p.testConn(&c, c.Password) // 表单密码为明文
	writeJSON(w, http.StatusOK, ginH("ok", terr == nil, "error", errStr(terr)))
}

func (p *Plugin) apiTestConn(w http.ResponseWriter, r *http.Request) {
	id := seg(strings.Trim(pluginPath(r), "/"), 1)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	err := p.testConn(c, p.openPassword(c))
	writeJSON(w, http.StatusOK, ginH("ok", err == nil, "error", errStr(err)))
}

// ---- 库 / 表 / 结构 ----

func (p *Plugin) apiDatabases(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaTimeout)
	defer cancel()
	var names []string
	var err error
	switch c.Type {
	case "redis":
		names, err = p.redisDatabaseList(ctx, c, p.openPassword(c))
	case "mongodb":
		var cli *mongoClient
		cli, err = p.mongoClient(ctx, c)
		if err == nil {
			defer cli.close()
			names, err = mongoDatabases(ctx, cli.c)
		}
	default:
		var dbh *sql.DB
		var d sqlDialect
		dbh, d, err = p.openSQL(c, "", p.openPassword(c), false)
		if err == nil {
			defer dbh.Close()
			names, err = d.ListDatabases(ctx, dbh, c)
		}
	}
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, ginH("databases", names))
}

func (p *Plugin) apiCreateDatabase(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	var body struct{ Name, Charset string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名必填"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	switch c.Type {
	case "redis":
		writeJSON(w, http.StatusOK, ginH("error", "Redis 的 db 数量由配置决定，不支持建库"))
		return
	case "mongodb":
		writeJSON(w, http.StatusOK, ginH("error", "MongoDB 数据库在首次写入时自动创建，请在集合页新增文档"))
		return
	}
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	if !d.SupportsCreateDB() {
		sqlText, derr := d.CreateDatabaseSQL(body.Name, body.Charset)
		writeJSON(w, http.StatusOK, ginH("error", errorText(sqlText, derr)))
		return
	}
	stmt, err := d.CreateDatabaseSQL(body.Name, body.Charset)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	dbh, _, err := p.openSQL(c, "", p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.ExecContext(ctx, stmt); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("创建数据库: conn=%s name=%s", id, body.Name)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func errorText(sqlText string, err error) string {
	if err != nil {
		return err.Error()
	}
	return sqlText
}

func (p *Plugin) apiDropDatabase(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id, name := seg(path, 1), seg(path, 3)
	// 双保险：本路由尾通配会吞掉多余段；正常应由注册顺序（泛化靠后）避免嵌套 DELETE 进入这里
	if len(strings.Split(path, "/")) != 4 || strings.Contains(path, "/tables/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "路径不匹配删库语义（疑似嵌套路由未先命中）"})
		return
	}
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	switch c.Type {
	case "redis":
		writeJSON(w, http.StatusOK, ginH("error", "Redis 的 db 由配置决定，不支持删除"))
		return
	case "mongodb":
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		if err := mongoDropDatabase(ctx, cli.c, name); err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		p.ctx.Logf("MongoDB 删除数据库: %s", name)
		writeJSON(w, http.StatusOK, ginH("ok", true))
		return
	}
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	stmt, serr := d.DropDatabaseSQL(name)
	if serr != nil {
		writeJSON(w, http.StatusOK, ginH("error", serr.Error()))
		return
	}
	dbh, _, err := p.openSQL(c, d.MaintainDB(c), p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.ExecContext(ctx, stmt); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("删除数据库: %s", name)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiTables(w http.ResponseWriter, r *http.Request) {
	id, db := seg(pluginPath(r), 1), seg(pluginPath(r), 3)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(db) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaTimeout)
	defer cancel()
	if c.Type == "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 为键值存储，没有表结构（请打开键空间）"))
		return
	}
	if c.Type == "mongodb" {
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		tables, err := mongoCollections(ctx, cli.c, db)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, ginH("tables", tables))
		return
	}
	dbh, d, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	tables, err := d.Tables(ctx, dbh, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("tables", tables))
}

func (p *Plugin) apiSchema(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaTimeout)
	defer cancel()
	if c.Type == "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 没有表结构"))
		return
	}
	if c.Type == "mongodb" {
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		s, err := mongoSchema(ctx, cli.c, db, table)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, s)
		return
	}
	dbh, d, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	s, err := d.Schema(ctx, dbh, db, table)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// ---- 表级操作 ----

func (p *Plugin) apiTruncate(w http.ResponseWriter, r *http.Request) {
	p.tableOp(w, r, "truncate")
}

func (p *Plugin) apiDropTable(w http.ResponseWriter, r *http.Request) {
	p.tableOp(w, r, "drop")
}

func (p *Plugin) tableOp(w http.ResponseWriter, r *http.Request, verb string) {
	path := strings.Trim(pluginPath(r), "/")
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	switch c.Type {
	case "redis":
		writeJSON(w, http.StatusOK, ginH("error", "Redis 没有表结构"))
		return
	case "mongodb":
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		if verb == "truncate" {
			n, err := mongoClearCollection(ctx, cli.c, db, table)
			if err != nil {
				writeJSON(w, http.StatusOK, ginH("error", err.Error()))
				return
			}
			p.ctx.Logf("MongoDB 清空集合 %s.%s（%d 文档）", db, table, n)
		} else {
			if err := mongoDropCollection(ctx, cli.c, db, table); err != nil {
				writeJSON(w, http.StatusOK, ginH("error", err.Error()))
				return
			}
			p.ctx.Logf("MongoDB 删除集合 %s.%s", db, table)
		}
		writeJSON(w, http.StatusOK, ginH("ok", true))
		return
	}
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	var stmt string
	if verb == "truncate" {
		stmt, err = d.TruncateSQL(db, table)
	} else {
		stmt, err = d.DropTableSQL(db, table)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	dbh, _, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.ExecContext(ctx, stmt); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("执行 %s: %s.%s", verb, db, table)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

// ---- 数据浏览 ----

func (p *Plugin) apiRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connID, db, table := q.Get("conn"), q.Get("db"), q.Get("table")
	page, _ := strconv.ParseInt(q.Get("page"), 10, 64)
	size, _ := strconv.ParseInt(q.Get("size"), 10, 64)
	where := strings.TrimSpace(q.Get("where"))
	order := strings.TrimSpace(q.Get("order"))
	dir := strings.ToUpper(strings.TrimSpace(q.Get("dir")))
	if connID == "" || !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数缺失或库名/表名非法"})
		return
	}
	if order != "" && !validName(order) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "排序列名非法"})
		return
	}
	if dir != "ASC" && dir != "DESC" {
		dir = ""
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	c := p.findConn(connID)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	if c.Type == "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 请使用键空间浏览"))
		return
	}
	if c.Type == "mongodb" {
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		cols, rows, raw, total, sqlText, err := mongoRows(ctx, cli.c, db, table, where, order, dir, int(page), int(size))
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, ginH("columns", cols, "rows", rows, "raw", raw, "total", total,
			"page", page, "size", size, "sql", sqlText))
		return
	}
	dbh, d, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	var total int64
	if err := dbh.QueryRowContext(ctx, d.CountSQL(db, table, where)).Scan(&total); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	pageSQL := d.PageSQL(db, table, where, order, dir, int(size), int((page-1)*size))
	cols, rows, err := queryRowsCtx(ctx, dbh, pageSQL)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("columns", cols, "rows", rows, "total", total,
		"page", page, "size", size, "sql", pageSQL))
}

// ---- 行级编辑 ----

type rowBody struct {
	Key    map[string]any `json:"key"`
	Values map[string]any `json:"values"`
}

func (p *Plugin) isMongo(id string) bool {
	c := p.findConn(id)
	return c != nil && c.Type == "mongodb"
}

func (p *Plugin) apiInsertRow(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	var body rowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少行数据"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	if c.Type == "mongodb" {
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		if err := mongoInsert(ctx, cli.c, db, table, body.Values); err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		p.ctx.Logf("MongoDB 插入文档 %s.%s", db, table)
		writeJSON(w, http.StatusOK, ginH("ok", true))
		return
	}
	if c.Type == "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 没有表结构"))
		return
	}
	if msg := checkCols(body.Values); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	dbh, _, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	cols := sortedCols(body.Values)
	quoted := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, col := range cols {
		quoted = append(quoted, d.Quote(col))
		args = append(args, normValue(body.Values[col]))
	}
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", d.Qualify(db, table),
		strings.Join(quoted, ", "), phList(d, len(cols)))
	if _, err := dbh.ExecContext(ctx, stmt, args...); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("插入行 %s.%s", db, table)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiUpdateRow(w http.ResponseWriter, r *http.Request) {
	p.rowWrite(w, r, "update")
}

func (p *Plugin) apiDeleteRow(w http.ResponseWriter, r *http.Request) {
	p.rowWrite(w, r, "delete")
}

func (p *Plugin) rowWrite(w http.ResponseWriter, r *http.Request, verb string) {
	path := strings.Trim(pluginPath(r), "/")
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	var body rowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Key) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少主键"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	if c.Type == "mongodb" {
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		if verb == "update" {
			if len(body.Values) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少文档内容"})
				return
			}
			if err := mongoReplace(ctx, cli.c, db, table, body.Key, body.Values); err != nil {
				writeJSON(w, http.StatusOK, ginH("error", err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, ginH("ok", true, "affected", 1))
			return
		}
		n, err := mongoDelete(ctx, cli.c, db, table, body.Key)
		if err != nil {
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, ginH("ok", true, "affected", n))
		return
	}
	if c.Type == "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 没有表结构"))
		return
	}
	if msg := checkCols(body.Key); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if verb == "update" {
		if len(body.Values) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少行数据"})
			return
		}
		if msg := checkCols(body.Values); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
	}
	d, err := sqlDialectFor(c.Type)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	dbh, _, err := p.openSQL(c, db, p.openPassword(c), false)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	schema, err := d.Schema(ctx, dbh, db, table)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	if msg := requirePK(schema, body.Key); msg != "" {
		writeJSON(w, http.StatusOK, ginH("error", msg))
		return
	}
	ph := 1
	var stmt string
	args := []any{}
	if verb == "update" {
		sets := make([]string, 0, len(body.Values))
		for _, col := range sortedCols(body.Values) {
			v := body.Values[col]
			if v == nil {
				sets = append(sets, d.Quote(col)+" = NULL")
				continue
			}
			sets = append(sets, d.Quote(col)+" = "+d.Ph(ph))
			args = append(args, normValue(v))
			ph++
		}
		conds, condArgs := whereClause(d, body.Key, ph)
		args = append(args, condArgs...)
		stmt = fmt.Sprintf("UPDATE %s SET %s WHERE %s", d.Qualify(db, table), strings.Join(sets, ", "), conds)
	} else {
		conds, condArgs := whereClause(d, body.Key, ph)
		args = append(args, condArgs...)
		stmt = fmt.Sprintf("DELETE FROM %s WHERE %s", d.Qualify(db, table), conds)
	}
	res, err := dbh.ExecContext(ctx, stmt, args...)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	affected, _ := res.RowsAffected()
	if verb == "delete" {
		p.ctx.Logf("删除行 %s.%s (%d 行)", db, table, affected)
	}
	writeJSON(w, http.StatusOK, ginH("ok", true, "affected", affected))
}

// requirePK 校验 key 覆盖完整主键（避免漏列导致误伤多行）。
func requirePK(s *SchemaInfo, key map[string]any) string {
	if s == nil {
		return "无法读取表结构"
	}
	pk := make([]string, 0, 4)
	for _, c := range s.Columns {
		if c.Key == "PRI" {
			pk = append(pk, c.Name)
		}
	}
	if len(pk) == 0 {
		return "表无主键，不可执行此操作（建议先定义主键）"
	}
	pkSet := map[string]bool{}
	for _, c := range pk {
		pkSet[c] = true
	}
	for c := range key {
		if !pkSet[c] {
			return "键列 " + c + " 不是主键列"
		}
	}
	for _, c := range pk {
		if _, ok := key[c]; !ok {
			return "主键列不完整（缺少 " + c + "）"
		}
	}
	return ""
}

// whereClause 主键条件：NULL 走 IS NULL，其余参数化绑定。
func whereClause(d sqlDialect, key map[string]any, ph int) (string, []any) {
	cols := sortedCols(key)
	conds := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, col := range cols {
		v := key[col]
		if v == nil {
			conds = append(conds, d.Quote(col)+" IS NULL")
			continue
		}
		conds = append(conds, d.Quote(col)+" = "+d.Ph(ph))
		args = append(args, normValue(v))
		ph++
	}
	return strings.Join(conds, " AND "), args
}

// ---- SQL / 命令控制台 ----

type queryBody struct {
	Conn     string `json:"conn"`
	Database string `json:"database"`
	SQL      string `json:"sql"`
}

func (p *Plugin) apiQuery(w http.ResponseWriter, r *http.Request) {
	var body queryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	body.SQL = strings.TrimSpace(body.SQL)
	if body.Conn == "" || body.SQL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "连接与语句必填"})
		return
	}
	c := p.findConn(body.Conn)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	switch c.Type {
	case "redis":
		cli, err := openRedis(c, redisDBIndex(body.Database), p.openPassword(c))
		if err != nil {
			p.recordHistory(body, false, err.Error(), 0, "0ms")
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.Close()
		cols, rows, err := redisExec(ctx, cli, body.SQL)
		el := time.Since(start).Round(time.Millisecond)
		if err != nil {
			p.recordHistory(body, false, err.Error(), 0, el.String())
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		p.recordHistory(body, true, "", len(rows), el.String())
		writeJSON(w, http.StatusOK, ginH("ok", true, "columns", cols, "rows", rows, "rowsAffected", 0, "elapsed", el.String()))
		return
	case "mongodb":
		if body.Database == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择数据库"})
			return
		}
		cli, err := p.mongoClient(ctx, c)
		if err != nil {
			p.recordHistory(body, false, err.Error(), 0, "0ms")
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		defer cli.close()
		cols, rows, err := mongoCommand(ctx, cli.c, body.Database, body.SQL)
		el := time.Since(start).Round(time.Millisecond)
		if err != nil {
			p.recordHistory(body, false, err.Error(), 0, el.String())
			writeJSON(w, http.StatusOK, ginH("error", err.Error()))
			return
		}
		p.recordHistory(body, true, "", len(rows), el.String())
		writeJSON(w, http.StatusOK, ginH("ok", true, "columns", cols, "rows", rows, "rowsAffected", 0, "elapsed", el.String()))
		return
	}

	dbh, d, err := p.openSQL(c, body.Database, p.openPassword(c), false)
	if err != nil {
		p.recordHistory(body, false, err.Error(), 0, "0ms")
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	_ = d

	if isQuerySQL(body.SQL) {
		cols, rows, qerr := queryRowsCtx(ctx, dbh, body.SQL)
		el := time.Since(start).Round(time.Millisecond)
		if qerr != nil {
			p.recordHistory(body, false, qerr.Error(), 0, el.String())
			writeJSON(w, http.StatusOK, ginH("error", qerr.Error()))
			return
		}
		p.recordHistory(body, true, "", len(rows), el.String())
		writeJSON(w, http.StatusOK, ginH("ok", true, "columns", cols, "rows", rows, "rowsAffected", 0, "elapsed", el.String()))
		return
	}
	res, xerr := dbh.ExecContext(ctx, body.SQL)
	el := time.Since(start).Round(time.Millisecond)
	if xerr != nil {
		p.recordHistory(body, false, xerr.Error(), 0, el.String())
		writeJSON(w, http.StatusOK, ginH("error", xerr.Error()))
		return
	}
	affected, _ := res.RowsAffected()
	_ = d
	p.recordHistory(body, true, "", 0, el.String())
	writeJSON(w, http.StatusOK, ginH("ok", true, "rowsAffected", affected, "elapsed", el.String()))
}

// isQuerySQL 判断语句是否返回结果集。Exec 类语句走 Exec，其余走 Query。
func isQuerySQL(sqlText string) bool {
	s := strings.ToLower(strings.TrimSpace(sqlText))
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	switch strings.TrimRight(fields[0], "(") {
	case "select", "show", "desc", "describe", "explain", "with", "values", "table",
		"pragma", "call", "exec", "execute":
		return true
	}
	return strings.Contains(s, " returning ")
}

func (p *Plugin) recordHistory(body queryBody, ok bool, errMsg string, rows int, elapsed string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.history = append([]QueryHistory{{
		ConnID: body.Conn, Database: body.Database, SQL: body.SQL,
		OK: ok, Error: errMsg, Elapsed: elapsed, Rows: rows,
		RunAt: time.Now().Format("2006-01-02 15:04:05"),
	}}, p.history...)
	if len(p.history) > 50 {
		p.history = p.history[:50]
	}
}

func (p *Plugin) apiHistory(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	writeJSON(w, http.StatusOK, p.history)
}

// ---- SQL 文件导入 ----

// apiRunSQLFile POST /connections/<id>/sqlfile?db=<库>&cont=<0|1>
// 请求体为 SQL 文件原文（text/plain，最大 512MB）；按方言拆分逐条执行并返回统计。
func (p *Plugin) apiRunSQLFile(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id := seg(path, 1)
	c := p.findConn(id)
	if c == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	if !isSQLKind(c.Type) {
		writeJSON(w, http.StatusOK, ginH("error", "该数据库类型不支持 SQL 文件导入"))
		return
	}
	q := r.URL.Query()
	db := q.Get("db")
	cont := q.Get("cont") == "1"
	if db != "" && !validName(db) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", "读取文件失败（过大或传输中断）: "+err.Error()))
		return
	}
	stmts := splitStatements(c.Type, string(body))
	if len(stmts) == 0 {
		writeJSON(w, http.StatusOK, ginH("error", "文件中没有可执行的 SQL 语句"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileTimeout)
	defer cancel()
	dbh, _, err := p.openSQL(c, db, p.openPassword(c), true)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	start := time.Now()
	executed, failed := 0, 0
	errs := make([]map[string]any, 0, 8)
	for i, s := range stmts {
		if _, err := dbh.ExecContext(ctx, s); err != nil {
			failed++
			if len(errs) < 50 {
				stmt := s
				if len(stmt) > 160 {
					stmt = stmt[:160] + "…"
				}
				errs = append(errs, ginH("index", i+1, "stmt", stmt, "error", err.Error()))
			}
			if !cont {
				break
			}
			continue
		}
		executed++
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	p.ctx.Logf("执行 SQL 文件: conn=%s db=%s 共 %d 条，成功 %d，失败 %d（%s）", id, db, len(stmts), executed, failed, elapsed)
	writeJSON(w, http.StatusOK, ginH(
		"ok", failed == 0, "total", len(stmts), "executed", executed,
		"failed", failed, "elapsed", elapsed.String(), "errors", errs,
		"stoppedEarly", failed > 0 && !cont,
	))
}

// ---- Redis 键空间 API ----

func (p *Plugin) redisFor(w http.ResponseWriter, r *http.Request, connID, db string) (*redisClientHandle, bool) {
	c := p.findConn(connID)
	if c == nil || c.Type != "redis" {
		writeJSON(w, http.StatusOK, ginH("error", "Redis 连接不存在"))
		return nil, false
	}
	cli, err := openRedis(c, redisDBIndex(db), p.openPassword(c))
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return nil, false
	}
	return &redisClientHandle{cli}, true
}

func (p *Plugin) apiRedisKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connID, db := q.Get("conn"), q.Get("db")
	pattern := strings.TrimSpace(q.Get("pattern"))
	cursor, _ := strconv.ParseUint(q.Get("cursor"), 10, 64)
	handle, ok := p.redisFor(w, r, connID, db)
	if !ok {
		return
	}
	defer handle.cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	keys, next, err := redisScan(ctx, handle.cli, pattern, cursor, redisScanCount)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("keys", keys, "cursor", strconv.FormatUint(next, 10)))
}

func (p *Plugin) apiRedisKeyGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	handle, ok := p.redisFor(w, r, q.Get("conn"), q.Get("db"))
	if !ok {
		return
	}
	defer handle.cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	detail, err := redisKeyDetail(ctx, handle.cli, q.Get("key"))
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (p *Plugin) apiRedisKeySet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Conn  string `json:"conn"`
		DB    string `json:"db"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少键名"})
		return
	}
	handle, ok := p.redisFor(w, r, body.Conn, body.DB)
	if !ok {
		return
	}
	defer handle.cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	if err := handle.cli.Set(ctx, body.Key, body.Value, 0).Err(); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiRedisKeyDel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Conn string `json:"conn"`
		DB   string `json:"db"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少键名"})
		return
	}
	handle, ok := p.redisFor(w, r, body.Conn, body.DB)
	if !ok {
		return
	}
	defer handle.cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	n, err := handle.cli.Del(ctx, body.Key).Result()
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("Redis 删除键 %s/%s（%d）", body.DB, body.Key, n)
	writeJSON(w, http.StatusOK, ginH("ok", true, "affected", n))
}

func (p *Plugin) apiRedisKeyTTL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Conn string `json:"conn"`
		DB   string `json:"db"`
		Key  string `json:"key"`
		TTL  int64  `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少键名"})
		return
	}
	handle, ok := p.redisFor(w, r, body.Conn, body.DB)
	if !ok {
		return
	}
	defer handle.cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dataTimeout)
	defer cancel()
	var err error
	if body.TTL <= 0 {
		err = handle.cli.Persist(ctx, body.Key).Err()
	} else {
		err = handle.cli.Expire(ctx, body.Key, time.Duration(body.TTL)*time.Second).Err()
	}
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

// ---- MCP ----

func (p *Plugin) mcpListConns() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.conns))
	for id, c := range p.conns {
		out = append(out, map[string]any{
			"id": id, "name": c.Name, "type": c.Type, "host": c.Host, "port": c.Port,
			"user": c.User, "database": c.Database, "path": c.Path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"]) })
	b, _ := json.Marshal(out)
	return string(b), nil
}
