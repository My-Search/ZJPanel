package dbadmin

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestSQLiteHandlers 用 httptest 直接驱动 HTTP 处理器，校验前端依赖的响应结构。
// 复用 Chinook 数据集；不存在则跳过。
func TestSQLiteHandlers(t *testing.T) {
	const src = `D:\data\users\zhuangjie\Downloads\kangaroo-master\databases\sqlite\Chinook_Sqlite.sql`
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skip("示例数据集不存在，跳过")
	}
	dbPath := filepath.Join(t.TempDir(), "chinook.db")
	c := &Connection{ID: "chinook", Name: "Chinook", Type: "sqlite", Path: dbPath}
	c.normalize()
	d := sqliteDialect{}
	dbh, err := d.Open(c, "main", "")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	for _, s := range splitStatements("sqlite", string(b)) {
		if _, err := dbh.Exec(s); err != nil {
			t.Fatalf("建库失败: %v", err)
		}
	}
	dbh.Close()

	p := &Plugin{conns: map[string]*Connection{"chinook": c}}

	// 表清单
	req := httptest.NewRequest("GET", "/connections/chinook/databases/main/tables", nil)
	req.Header.Set("X-Plugin-Path", "/connections/chinook/databases/main/tables")
	w := httptest.NewRecorder()
	p.apiTables(w, req)
	var tablesResp struct {
		Tables []TableInfo `json:"tables"`
		Error  string      `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tablesResp); err != nil || tablesResp.Error != "" {
		t.Fatalf("表清单响应异常: %s err=%v", w.Body.String(), err)
	}
	if len(tablesResp.Tables) < 5 {
		t.Fatalf("表数量异常: %d", len(tablesResp.Tables))
	}

	// 结构
	req = httptest.NewRequest("GET", "/connections/chinook/databases/main/tables/Album/schema", nil)
	req.Header.Set("X-Plugin-Path", "/connections/chinook/databases/main/tables/Album/schema")
	w = httptest.NewRecorder()
	p.apiSchema(w, req)
	var schema SchemaInfo
	if err := json.Unmarshal(w.Body.Bytes(), &schema); err != nil || len(schema.Columns) == 0 {
		t.Fatalf("结构响应异常: %s err=%v", w.Body.String(), err)
	}

	// 分页数据
	v := url.Values{"conn": {"chinook"}, "db": {"main"}, "table": {"Album"}, "page": {"1"}, "size": {"5"}}
	req = httptest.NewRequest("GET", "/rows?"+v.Encode(), nil)
	w = httptest.NewRecorder()
	p.apiRows(w, req)
	var rowsResp struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
		Total   int64    `json:"total"`
		SQL     string   `json:"sql"`
		Error   string   `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rowsResp); err != nil || rowsResp.Error != "" {
		t.Fatalf("分页响应异常: %s err=%v", w.Body.String(), err)
	}
	if len(rowsResp.Rows) != 5 || rowsResp.Total < 5 || rowsResp.SQL == "" {
		t.Fatalf("分页数据异常: rows=%d total=%d sql=%q", len(rowsResp.Rows), rowsResp.Total, rowsResp.SQL)
	}
	if rowsResp.Columns[0] != "AlbumId" {
		t.Fatalf("列顺序异常: %v", rowsResp.Columns)
	}
}
