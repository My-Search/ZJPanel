// Package mysqladmin 内置插件：MySQL 管理工具（轻量 Navicat）。
// 能力：连接管理（凭据 AES-GCM 加密存储）、库/表浏览（表结构/索引/建表语句）、
// 常用操作（建库/删库/清空表/删表）、SQL 控制台（结果表格 + 执行历史环形保留）、
// SQL 文件导入执行（语句拆分/DELIMITER/遇错继续）、数据浏览（分页/筛选/排序）
// 与行级编辑（主键定位的插入/更新/删除）。API 挂 /api/plugin/mysqladmin/*（面板登录态）。
package mysqladmin

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
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"zjpanel/backend/internal/pkg"
	sdk "zjpanel/backend/sdk/plugin"
)

const connFile = "connections.json"

type Connection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	User      string `json:"user"`
	Password  string `json:"password"` // 存储与返回均为密文；明文仅内存中连接时解出
	Comment   string `json:"comment,omitempty"`
	CreatedAt string `json:"createdAt"`
}

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
	history []QueryHistory // 环形保留最近 50 条
}

func New() sdk.Plugin { return &Plugin{conns: map[string]*Connection{}} }

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "mysqladmin",
		Name:        "MySQL 管理",
		Version:     "0.1.0",
		Description: "MySQL 连接管理 / 库表浏览 / 建删库表 / SQL 控制台 / SQL 文件导入执行 / 数据分页浏览与行级编辑（凭据加密存储）",
		Permissions: []string{"http"},
		Menu:        []sdk.Menu{{Title: "MySQL", Icon: "database", Path: "/"}},
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

	// 路由按「具体 → 泛化」顺序注册：派发按注册顺序首中即返回，
	// 且尾通配 * 匹配剩余全部段 —— 泛化尾通配（如 DELETE /connections/*/databases/*）
	// 必须放在所有嵌套它的新增路由之后，否则嵌套 DELETE（删行）会被截胡成删库。
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
	api("POST", "/connections/*/sqlfile", p.apiRunSQLFile)         // 无尾通配，与既有 POST 路由互不冲突
	api("DELETE", "/connections/*/databases/*", p.apiDropDatabase) // 嵌套 DELETE 之后注册，防止截胡
	api("POST", "/connections/*/test", p.apiTestConn)
	api("DELETE", "/connections/*", p.apiDeleteConn)
	api("GET", "/rows", p.apiRows)
	api("POST", "/query", p.apiQuery)
	api("GET", "/history", p.apiHistory)

	ctx.RegisterMCPTool("mysqladmin_list_connections", "列出 MySQL 管理插件中的全部连接（名称/主机/用户，不含密码）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) { return p.mcpListConns() })
	return nil
}

func (p *Plugin) OnShutdown() {}

func (p *Plugin) route(method, path string, h http.HandlerFunc) { p.ctx.RegisterRoute(method, path, h) }

// ---- 存取 ----

func (p *Plugin) connPath() string { return filepath.Join(p.ctx.DataDir(), connFile) }

func (p *Plugin) loadConns() {
	b, err := os.ReadFile(p.connPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &p.conns)
}

func (p *Plugin) saveConns() error {
	_ = os.MkdirAll(p.ctx.DataDir(), 0o755)
	b, _ := json.MarshalIndent(p.conns, "", "  ")
	return os.WriteFile(p.connPath(), b, 0o600)
}

// sealConn / openConn 密码字段加解密（落盘密文；列表返回时密码替换为固定掩码占位）。
func (p *Plugin) sealConn(c *Connection) error {
	if c.Password == "" {
		return nil
	}
	enc, err := p.box.SealE(c.Password)
	if err != nil {
		return err
	}
	c.Password = enc
	return nil
}

func (p *Plugin) openPassword(c *Connection) string {
	if c.Password == "" {
		return ""
	}
	plain, err := p.box.Open(c.Password)
	if err != nil {
		return ""
	}
	return plain
}

// dsn 组装连接串（已保存连接，密码为密文需解密）；db 为空时连 information_schema。
func (p *Plugin) dsn(c *Connection, db string) string {
	return p.dsnWithPassword(c, db, p.openPassword(c))
}

// dsnWithPassword 用明文密码直接组装连接串。db 为空时连 information_schema。
func (p *Plugin) dsnWithPassword(c *Connection, db, plainPwd string) string {
	if db == "" {
		db = "information_schema"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&timeout=5s&readTimeout=30s&writeTimeout=30s",
		c.User, plainPwd, c.Host, c.Port, db)
}

func (p *Plugin) findConn(id string) *Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[id]
}

// open 打开连接（每次即开即用，插件不持有长连接池，避免跨请求状态）。
func (p *Plugin) open(connID, db string) (*sql.DB, *Connection, error) {
	c := p.findConn(connID)
	if c == nil {
		return nil, nil, fmt.Errorf("连接不存在: %s", connID)
	}
	dbh, err := sql.Open("mysql", p.dsn(c, db))
	if err != nil {
		return nil, nil, err
	}
	dbh.SetMaxOpenConns(4)
	dbh.SetMaxIdleConns(2)
	dbh.SetConnMaxLifetime(5 * time.Minute)
	return dbh, c, nil
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

// pluginPath 派发层注入的剥离前缀后的子路径（handler 不能用 r.URL.Path，那是完整原始路径）。
func pluginPath(r *http.Request) string {
	if p := r.Header.Get("X-Plugin-Path"); p != "" {
		return p
	}
	return r.URL.Path
}

// seg 取路径第 n 段（0 起，跳过开头空段）。
func seg(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n < len(parts) {
		return parts[n]
	}
	return ""
}

// queryRows 通用查询：返回列名 + 字符串化行数据（兼容任意类型，前端表格直接渲染）。
func queryRows(dbh *sql.DB, q string, args ...any) (cols []string, rows [][]any, err error) {
	rs, err := dbh.Query(q, args...)
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
			default:
				out[i] = fmt.Sprintf("%v", t)
			}
		}
		rows = append(rows, out)
	}
	return cols, rows, rs.Err()
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
	c.ID = strings.TrimSpace(strings.ToLower(c.ID))
	c.Host = strings.TrimSpace(c.Host)
	c.Port = strings.TrimSpace(c.Port)
	if c.Port == "" {
		c.Port = "3306"
	}
	if c.ID == "" || c.Host == "" || c.User == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标识/主机/用户名必填"})
		return
	}
	p.mu.Lock()
	old, exists := p.conns[c.ID]
	if c.Password == "" && exists {
		c.Password = old.Password // 编辑时留空沿用旧密码
	}
	if c.CreatedAt == "" {
		c.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if err := p.sealConn(&c); err != nil {
		p.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.conns[c.ID] = &c
	err := p.saveConns()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.ctx.Logf("MySQL 连接已保存: %s (%s:%s)", c.ID, c.Host, c.Port)
	out := c
	out.Password = ""
	writeJSON(w, http.StatusOK, ginH("ok", true, "connection", &out))
}

func (p *Plugin) apiDeleteConn(w http.ResponseWriter, r *http.Request) {
	// /connections/<id> → 段1。此路由为最泛化尾通配，嵌套路由（删库/删表）必须先注册命中。
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

// testConn 探活：连接超时 6s，SELECT 1 验证。plainPwd 由调用方按密码形态传入：
// 表单直测传明文（未加密），已存连接测试传 openPassword 解密结果。
func (p *Plugin) testConn(c *Connection, plainPwd string) error {
	dbh, err := sql.Open("mysql", p.dsnWithPassword(c, "", plainPwd))
	if err != nil {
		return err
	}
	defer dbh.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var one int
	return dbh.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// apiTestForm POST /connections/test：用未保存的表单参数直接测试（保存前验证）。
func (p *Plugin) apiTestForm(w http.ResponseWriter, r *http.Request) {
	var c Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if c.Host == "" || c.User == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "主机/用户名必填"})
		return
	}
	if c.Port == "" {
		c.Port = "3306"
	}
	terr := p.testConn(&c, c.Password) // 表单密码为明文
	writeJSON(w, http.StatusOK, ginH("ok", terr == nil, "error", errStr(terr)))
}

// apiTestConn POST /connections/<id>/test：测试已保存的连接（密码按密文解密）。
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

// ---- 库/表浏览 ----

func (p *Plugin) apiDatabases(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	dbh, _, err := p.open(id, "")
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	_, rows, err := queryRows(dbh, "SHOW DATABASES")
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if s, ok := row[0].(string); ok {
			out = append(out, s)
		}
	}
	writeJSON(w, http.StatusOK, ginH("databases", out))
}

func (p *Plugin) apiCreateDatabase(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	var body struct{ Name, Charset string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名必填"})
		return
	}
	if body.Name == "" || !validName(body.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	charset := body.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	dbh, _, err := p.open(id, "")
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET %s", body.Name, charset)); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiDropDatabase(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id, name := seg(path, 1), seg(path, 3) // /connections/<id>/databases/<name>
	// 双保险：本路由尾通配会吞掉多余段；正常应由注册顺序（泛化靠后）避免嵌套 DELETE 进入这里
	if len(strings.Split(path, "/")) != 4 || strings.Contains(path, "/tables/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "路径不匹配删库语义（疑似嵌套路由未先命中）"})
		return
	}
	if !validName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	dbh, _, err := p.open(id, "")
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.Exec(fmt.Sprintf("DROP DATABASE `%s`", name)); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("MySQL 删除数据库: %s", name)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiTables(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	// /connections/<id>/databases/<db>/tables
	id, db := seg(path, 1), seg(path, 3)
	if !validName(db) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	// 表清单 + 行数估算 + 注释（information_schema 一次拿全，避免逐表 COUNT）
	cols, rows, err := queryRows(dbh,
		`SELECT TABLE_NAME, TABLE_ROWS, IFNULL(DATA_LENGTH+INDEX_LENGTH,0), IFNULL(TABLE_COMMENT,'')
		 FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var rn int64
		_ = json.Unmarshal([]byte(fmt.Sprintf("%v", row[1])), &rn)
		out = append(out, map[string]any{
			"name": row[0], "rows": row[1], "sizeBytes": row[2], "comment": row[3],
		})
	}
	_ = cols
	writeJSON(w, http.StatusOK, ginH("tables", out))
}

func (p *Plugin) apiSchema(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	// /connections/<id>/databases/<db>/tables/<table>/schema
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	_, colRows, err := queryRows(dbh, "SHOW FULL COLUMNS FROM `"+table+"`")
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	colNames := []string{"Field", "Type", "Collation", "Null", "Key", "Default", "Extra", "Privileges", "Comment"}
	columns := make([]map[string]any, 0, len(colRows))
	for _, row := range colRows {
		m := map[string]any{}
		for i, n := range colNames {
			if i < len(row) {
				m[n] = row[i]
			}
		}
		columns = append(columns, m)
	}
	// 索引：information_schema.STATISTICS 按列名取值（SHOW INDEX 返回裸数组，前端无法按名取列）
	idxNames := []string{"Key_name", "Non_unique", "Index_type", "Column_name", "Seq_in_index"}
	_, idxRows, err := queryRows(dbh,
		`SELECT INDEX_NAME, NON_UNIQUE, INDEX_TYPE, COLUMN_NAME, SEQ_IN_INDEX
		 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		 ORDER BY INDEX_NAME, SEQ_IN_INDEX`, db, table)
	if err != nil {
		// 回退 SHOW INDEX（老版本 MySQL 兼容性无虞，仅失列名）
		_, idxRows, _ = queryRows(dbh, "SHOW INDEX FROM `"+table+"`")
	}
	indexes := make([]map[string]any, 0, len(idxRows))
	for _, row := range idxRows {
		m := map[string]any{}
		for j, n := range idxNames {
			if j < len(row) {
				m[n] = row[j]
			}
		}
		indexes = append(indexes, m)
	}
	var createSQL string
	_, crRows, err := queryRows(dbh, "SHOW CREATE TABLE `"+table+"`")
	if err == nil && len(crRows) > 0 && len(crRows[0]) > 1 {
		if s, ok := crRows[0][1].(string); ok {
			createSQL = s
		}
	}
	writeJSON(w, http.StatusOK, ginH("columns", columns, "indexes", indexes, "createSQL", createSQL))
}

func (p *Plugin) apiTruncate(w http.ResponseWriter, r *http.Request) {
	p.tableOp(w, r, "TRUNCATE TABLE")
}

func (p *Plugin) apiDropTable(w http.ResponseWriter, r *http.Request) {
	p.tableOp(w, r, "DROP TABLE")
}

func (p *Plugin) tableOp(w http.ResponseWriter, r *http.Request, verb string) {
	path := strings.Trim(pluginPath(r), "/")
	id, db, table := seg(path, 1), seg(path, 3), seg(path, 5)
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if _, err = dbh.Exec(fmt.Sprintf("%s `%s`", verb, table)); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("MySQL %s %s.%s", verb, db, table)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

// ---- 行级编辑（插入/更新/删除，主键校验） ----
//
// 值由 JSON 直接作为参数传给 driver（字符串/数字/null），MySQL 按列类型隐式转换；
// 列名与主键列均做白名单校验，杜绝注入面。

// tableCtx 解析嵌套路由 /connections/<id>/databases/<db>/tables/<table>。
func (p *Plugin) tableCtx(r *http.Request) (id, db, table string) {
	path := strings.Trim(pluginPath(r), "/")
	return seg(path, 1), seg(path, 3), seg(path, 5)
}

// priCols 查表主键列（SHOW COLUMNS 第 4 列 Key == PRI）。
func priCols(dbh *sql.DB, table string) ([]string, error) {
	_, rows, err := queryRows(dbh, "SHOW COLUMNS FROM `"+table+"`")
	if err != nil {
		return nil, err
	}
	pk := []string{}
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		if key, _ := row[3].(string); key == "PRI" {
			if f, _ := row[0].(string); f != "" {
				pk = append(pk, f)
			}
		}
	}
	return pk, nil
}

// rowBody 行级操作请求体：key 仅更新/删除时需要（主键值对，null 走 NULL 安全比较）。
type rowBody struct {
	Key    map[string]any `json:"key"`
	Values map[string]any `json:"values"`
}

// sortedCols 取列名有序列表（map 遍历随机，固定顺序才能与参数一一对应）。
func sortedCols(m map[string]any) []string {
	cols := make([]string, 0, len(m))
	for c := range m {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}

// checkCols 校验列名合法性，返回错误消息（空串表示通过）。
func checkCols(m map[string]any) string {
	for col := range m {
		if !validName(col) {
			return "列名非法: " + col
		}
	}
	return ""
}

// requirePK 查表主键并校验 key 列均属于主键。msg 非空表示校验失败。
func requirePK(dbh *sql.DB, table string, key map[string]any) (msg string) {
	pk, err := priCols(dbh, table)
	if err != nil {
		return err.Error()
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
	return ""
}

func (p *Plugin) apiInsertRow(w http.ResponseWriter, r *http.Request) {
	id, db, table := p.tableCtx(r)
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	var body rowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少行数据"})
		return
	}
	if msg := checkCols(body.Values); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	cols := sortedCols(body.Values)
	quoted, ph := make([]string, 0, len(cols)), make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, "`"+c+"`")
		args = append(args, body.Values[c])
		ph = append(ph, "?")
	}
	q := fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)", table, strings.Join(quoted, ", "), strings.Join(ph, ", "))
	if _, err := dbh.Exec(q, args...); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	p.ctx.Logf("MySQL 插入行 %s.%s", db, table)
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiUpdateRow(w http.ResponseWriter, r *http.Request) {
	id, db, table := p.tableCtx(r)
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	var body rowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Key) == 0 || len(body.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少主键或行数据"})
		return
	}
	if msg := checkCols(body.Key); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := checkCols(body.Values); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if msg := requirePK(dbh, table, body.Key); msg != "" {
		writeJSON(w, http.StatusOK, ginH("error", msg))
		return
	}
	cols := sortedCols(body.Values)
	sets := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols)+len(body.Key))
	for _, c := range cols {
		sets = append(sets, "`"+c+"`=?")
		args = append(args, body.Values[c])
	}
	// 主键定位用 NULL 安全比较（<=>），支持主键值本身为 NULL 的极端场景
	kcols := sortedCols(body.Key)
	conds := make([]string, 0, len(kcols))
	for _, c := range kcols {
		conds = append(conds, "`"+c+"` <=> ?")
		args = append(args, body.Key[c])
	}
	q := fmt.Sprintf("UPDATE `%s` SET %s WHERE %s", table, strings.Join(sets, ", "), strings.Join(conds, " AND "))
	res, err := dbh.Exec(q, args...)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	affected, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, ginH("ok", true, "affected", affected))
}

func (p *Plugin) apiDeleteRow(w http.ResponseWriter, r *http.Request) {
	id, db, table := p.tableCtx(r)
	if !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名/表名含非法字符"})
		return
	}
	var body rowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Key) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少主键"})
		return
	}
	if msg := checkCols(body.Key); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	dbh, _, err := p.open(id, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	if msg := requirePK(dbh, table, body.Key); msg != "" {
		writeJSON(w, http.StatusOK, ginH("error", msg))
		return
	}
	kcols := sortedCols(body.Key)
	conds := make([]string, 0, len(kcols))
	args := make([]any, 0, len(kcols))
	for _, c := range kcols {
		conds = append(conds, "`"+c+"` <=> ?")
		args = append(args, body.Key[c])
	}
	q := fmt.Sprintf("DELETE FROM `%s` WHERE %s", table, strings.Join(conds, " AND "))
	res, err := dbh.Exec(q, args...)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	affected, _ := res.RowsAffected()
	p.ctx.Logf("MySQL 删除行 %s.%s (%d 行)", db, table, affected)
	writeJSON(w, http.StatusOK, ginH("ok", true, "affected", affected))
}

// ---- 数据浏览（分页） ----

func (p *Plugin) apiRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connID, db, table := q.Get("conn"), q.Get("db"), q.Get("table")
	page, _ := json.Number(q.Get("page")).Int64()
	size, _ := json.Number(q.Get("size")).Int64()
	where := strings.TrimSpace(q.Get("where"))
	order := q.Get("order")
	if connID == "" || !validName(db) || !validName(table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数缺失或库名/表名非法"})
		return
	}
	if order != "" && !validName(order) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "排序列名非法"})
		return
	}
	dir := strings.ToUpper(strings.TrimSpace(q.Get("dir")))
	if dir != "ASC" && dir != "DESC" {
		dir = ""
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	dbh, _, err := p.open(connID, db)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()

	cond, args := "", []any{}
	if where != "" {
		cond = " WHERE " + where // 允许手写条件（面板登录态操作者可信；值参数化由调用方用 ? 传入）
	}
	orderSQL := ""
	if order != "" {
		if dir == "" {
			dir = "ASC"
		}
		orderSQL = fmt.Sprintf(" ORDER BY `%s` %s", order, dir)
	}
	var total int64
	if err := dbh.QueryRow("SELECT COUNT(*) FROM `"+table+"`"+cond, args...).Scan(&total); err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	offset := (page - 1) * size
	cols, rows, err := queryRows(dbh, fmt.Sprintf("SELECT * FROM `%s`%s%s LIMIT %d OFFSET %d", table, cond, orderSQL, size, offset), args...)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ginH("columns", cols, "rows", rows, "total", total, "page", page, "size", size))
}

// ---- SQL 控制台 ----

func (p *Plugin) apiQuery(w http.ResponseWriter, r *http.Request) {
	var body struct{ Conn, Database, SQL string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	body.SQL = strings.TrimSpace(body.SQL)
	if body.Conn == "" || body.SQL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "连接与 SQL 必填"})
		return
	}
	if body.Database != "" && !validName(body.Database) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	start := time.Now()
	dbh, _, err := p.open(body.Conn, body.Database)
	if err != nil {
		p.recordHistory(body, false, err.Error(), 0, "0ms", 0)
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()

	h := ginH()
	sqlLower := strings.ToLower(body.SQL)
	switch {
	case strings.HasPrefix(sqlLower, "select"), strings.HasPrefix(sqlLower, "show"), strings.HasPrefix(sqlLower, "desc"), strings.HasPrefix(sqlLower, "explain"):
		cols, rows, qerr := queryRows(dbh, body.SQL)
		elapsed := time.Since(start).Round(time.Millisecond)
		if qerr != nil {
			p.recordHistory(body, false, qerr.Error(), 0, elapsed.String(), 0)
			writeJSON(w, http.StatusOK, ginH("error", qerr.Error()))
			return
		}
		p.recordHistory(body, true, "", len(rows), elapsed.String(), 0)
		h["columns"], h["rows"], h["rowsAffected"], h["elapsed"] = cols, rows, 0, elapsed.String()
	default:
		res, xerr := dbh.Exec(body.SQL)
		elapsed := time.Since(start).Round(time.Millisecond)
		if xerr != nil {
			p.recordHistory(body, false, xerr.Error(), 0, elapsed.String(), 0)
			writeJSON(w, http.StatusOK, ginH("error", xerr.Error()))
			return
		}
		affected, _ := res.RowsAffected()
		p.recordHistory(body, true, "", 0, elapsed.String(), affected)
		h["rowsAffected"], h["elapsed"] = affected, elapsed.String()
	}
	h["ok"] = true
	writeJSON(w, http.StatusOK, h)
}

// ---- SQL 文件导入执行 ----

// isSpaceRune 手写空白判断（避免为单个字符引入 unicode 包）。
func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

// atLineStart 判断 rs[i] 之前到行首只有空白。
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

// splitSQLFile 把 SQL 脚本拆成可逐条执行的语句。
// 支持：'...'/"..."/`...`（反斜杠转义与双写引号）、-- 与 # 行注释、/* */ 块注释、
// DELIMITER 指令（mysqldump 触发器/存储过程）、版本化注释 /*!...*/ 原样保留随语句执行。
// 纯注释或空白片段不产生语句；文件不以分隔符结尾时最后一段也视为一条语句。
func splitSQLFile(src string) []string {
	rs := []rune(src)
	n := len(rs)
	out := []string{}
	delim := []rune(";")
	var cur strings.Builder
	sawCode := false

	flush := func() {
		if sawCode {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
		}
		cur.Reset()
		sawCode = false
	}

	i := 0
	for i < n {
		c := rs[i]
		// DELIMITER 指令：行首、当前语句尚无实际代码
		if !sawCode && (c == 'd' || c == 'D') && atLineStart(rs, i) &&
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
		// 字符串字面量：反斜杠转义 + 同引号双写续行
		if c == '\'' || c == '"' {
			cur.WriteRune(c)
			i++
			for i < n {
				cur.WriteRune(rs[i])
				if rs[i] == '\\' && i+1 < n {
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
			continue
		}
		// 反引号标识符（内可含分号）
		if c == '`' {
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
			continue
		}
		// 行注释 -- 与 #
		if (c == '-' && i+1 < n && rs[i+1] == '-') || c == '#' {
			for i < n && rs[i] != '\n' {
				cur.WriteRune(rs[i])
				i++
			}
			continue
		}
		// 块注释 /* */；/*! ... */ 为版本化可执行注释（mysqldump 常用），视为代码
		if c == '/' && i+1 < n && rs[i+1] == '*' {
			cur.WriteString("/*")
			i += 2
			if i < n && rs[i] == '!' {
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
		// 命中当前分隔符 → 结束一条语句
		if i+len(delim) <= n {
			match := true
			for j, d := range delim {
				if rs[i+j] != d {
					match = false
					break
				}
			}
			if match {
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

// openTimeout 打开连接并放宽网络超时（SQL 文件等长耗时操作使用）。
func (p *Plugin) openTimeout(connID, db string, t time.Duration) (*sql.DB, *Connection, error) {
	c := p.findConn(connID)
	if c == nil {
		return nil, nil, fmt.Errorf("连接不存在: %s", connID)
	}
	if db == "" {
		db = "information_schema"
	}
	dbh, err := sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&timeout=10s&readTimeout=%s&writeTimeout=%s",
		c.User, p.openPassword(c), c.Host, c.Port, db, t, t))
	if err != nil {
		return nil, nil, err
	}
	dbh.SetMaxOpenConns(2)
	dbh.SetConnMaxLifetime(30 * time.Minute)
	return dbh, c, nil
}

// apiRunSQLFile POST /connections/<id>/sqlfile?db=<库>&cont=<0|1>
// 请求体为 SQL 文件原文（text/plain，最大 512MB）；逐条执行并返回统计与错误清单。
// db 为空时以 information_schema 作为默认库（脚本内的 USE 语句自行切换）。
func (p *Plugin) apiRunSQLFile(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(pluginPath(r), "/")
	id := seg(path, 1)
	q := r.URL.Query()
	db := q.Get("db")
	cont := q.Get("cont") == "1"
	if db != "" && !validName(db) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "库名含非法字符"})
		return
	}
	if p.findConn(id) == nil {
		writeJSON(w, http.StatusOK, ginH("error", "连接不存在"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", "读取文件失败（过大或传输中断）: "+err.Error()))
		return
	}
	stmts := splitSQLFile(string(body))
	if len(stmts) == 0 {
		writeJSON(w, http.StatusOK, ginH("error", "文件中没有可执行的 SQL 语句"))
		return
	}
	dbh, _, err := p.openTimeout(id, db, 5*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusOK, ginH("error", err.Error()))
		return
	}
	defer dbh.Close()
	start := time.Now()
	executed, failed := 0, 0
	errs := make([]map[string]any, 0, 8)
	for i, s := range stmts {
		if _, err := dbh.Exec(s); err != nil {
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
	p.ctx.Logf("MySQL 执行 SQL 文件: conn=%s db=%s 共 %d 条，成功 %d，失败 %d（%s）", id, db, len(stmts), executed, failed, elapsed)
	writeJSON(w, http.StatusOK, ginH(
		"ok", failed == 0, "total", len(stmts), "executed", executed,
		"failed", failed, "elapsed", elapsed.String(), "errors", errs,
		"stoppedEarly", failed > 0 && !cont,
	))
}

func (p *Plugin) recordHistory(body struct{ Conn, Database, SQL string }, ok bool, errMsg string, rows int, elapsed string, affected int64) {
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
	_ = affected
}

func (p *Plugin) apiHistory(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	writeJSON(w, http.StatusOK, p.history)
}

// ---- MCP ----

func (p *Plugin) mcpListConns() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.conns))
	for id, c := range p.conns {
		out = append(out, map[string]any{
			"id": id, "name": c.Name, "host": c.Host, "port": c.Port, "user": c.User,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c > 127) {
			return false
		}
	}
	return true
}
