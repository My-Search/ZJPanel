package dbadmin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSQLiteChinook 用示例数据集做端到端校验：SQL 拆分 → 建库 → 表清单 → 结构 → 分页。
func TestSQLiteChinook(t *testing.T) {
	const src = `D:\data\users\zhuangjie\Downloads\kangaroo-master\databases\sqlite\Chinook_Sqlite.sql`
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skip("示例 SQLite 数据集不存在，跳过: " + err.Error())
	}
	dbPath := filepath.Join(t.TempDir(), "chinook.db")
	c := &Connection{Type: "sqlite", Path: dbPath}
	c.normalize()
	if err := c.validate(); err != nil {
		t.Fatalf("路径校验失败: %v", err)
	}
	d := sqliteDialect{}
	dbh, err := d.Open(c, "main", "")
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer dbh.Close()

	stmts := splitStatements("sqlite", string(b))
	if len(stmts) < 20 {
		t.Fatalf("语句拆分异常，仅 %d 条", len(stmts))
	}
	for i, s := range stmts {
		if _, err := dbh.Exec(s); err != nil {
			t.Fatalf("第 %d 条执行失败: %v\n%s", i+1, err, s)
		}
	}

	ctx := context.Background()
	tables, err := d.Tables(ctx, dbh, "main")
	if err != nil {
		t.Fatalf("表清单失败: %v", err)
	}
	if len(tables) < 5 {
		t.Fatalf("表数量异常: %d", len(tables))
	}
	var album string
	for _, tb := range tables {
		if strings.EqualFold(tb.Name, "Album") {
			album = tb.Name
		}
	}
	if album == "" {
		t.Fatalf("未找到 Album 表: %+v", tables)
	}
	s, err := d.Schema(ctx, dbh, "main", album)
	if err != nil {
		t.Fatalf("读取结构失败: %v", err)
	}
	if len(s.Columns) == 0 || s.CreateSQL == "" {
		t.Fatalf("结构为空: %+v", s)
	}
	if _, rows, err := queryRowsCtx(ctx, dbh, d.PageSQL("main", album, "", "AlbumId", "ASC", 5, 0)); err != nil || len(rows) != 5 {
		t.Fatalf("分页查询失败: rows=%d err=%v sql=%s", len(rows), err, d.PageSQL("main", album, "", "AlbumId", "ASC", 5, 0))
	}
	var total int64
	if err := dbh.QueryRowContext(ctx, d.CountSQL("main", album, "")).Scan(&total); err != nil || total < 5 {
		t.Fatalf("计数失败: total=%d err=%v", total, err)
	}
}
