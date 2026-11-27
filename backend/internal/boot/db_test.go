package boot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// TestRepairLegacyComposeIDs 复现线上历史库：integer 列里混入字符串空值后，
// GORM 扫描 *uint 会报 invalid syntax；清洗后所有站点应能正常读取。
func TestRepairLegacyComposeIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:repair_legacy_ids?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Site{}, &model.ComposeProject{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 模拟旧版本以字符串落库的脏值：NULL / 空串 / 合法数字 / 非数字 / 0 / 带空格
	values := []any{nil, "", 2, "abc", 0, " 3 "}
	for i, v := range values {
		domain := "site" + string(rune('a'+i)) + ".test"
		if err := db.Exec(
			"INSERT INTO sites (domain, type, compose_project_id) VALUES (?, 'proxy', ?)",
			domain, v,
		).Error; err != nil {
			t.Fatalf("写入测试站点失败: %v", err)
		}
	}
	if err := db.Exec(
		"INSERT INTO compose_projects (name, dir, services, site_id, imported) VALUES ('p', '/tmp/p', '[]', '', 0)",
	).Error; err != nil {
		t.Fatalf("写入测试项目失败: %v", err)
	}

	// 修复前必须能复现故障（否则说明测试场景没有覆盖线上问题）
	if err := db.Order("id").Find(&[]model.Site{}).Error; err == nil {
		t.Fatal("期望历史脏值触发 *uint 扫描错误，实际未报错")
	}

	repairLegacyComposeIDs(db)
	repairLegacyComposeIDs(db) // 幂等：重复启动不重复改坏数据

	var sites []model.Site
	if err := db.Order("id").Find(&sites).Error; err != nil {
		t.Fatalf("清洗后站点读取仍失败: %v", err)
	}
	if len(sites) != len(values) {
		t.Fatalf("站点数量不符: got %d want %d", len(sites), len(values))
	}
	want := []*uint{nil, nil, uintPtr(2), nil, nil, uintPtr(3)}
	for i, w := range want {
		got := sites[i].ComposeProjectID
		if w == nil {
			if got != nil {
				t.Errorf("站点 %d: 期望 NULL，实际 %d", i, *got)
			}
			continue
		}
		if got == nil || *got != *w {
			t.Errorf("站点 %d: 期望 %d，实际 %v", i, *w, got)
		}
	}

	var projects []model.ComposeProject
	if err := db.Order("id").Find(&projects).Error; err != nil {
		t.Fatalf("清洗后项目读取仍失败: %v", err)
	}
	if len(projects) != 1 || projects[0].SiteID != nil {
		t.Fatalf("项目 site_id 期望 NULL，实际 %+v", projects)
	}
}

func uintPtr(v uint) *uint { return &v }

// TestOpenDBRepairsLegacyTextColumn 模拟旧库把 compose_project_id 建成了 TEXT 列：
// 启动流程应先清洗脏值，再交给 AutoMigrate 改列，最终读取不再报扫描错误。
func TestOpenDBRepairsLegacyTextColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zjpanel.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if err := legacy.Exec(`CREATE TABLE sites (
		id integer PRIMARY KEY AUTOINCREMENT, domain text, type text, upstream text, root text,
		ssl numeric, cert_task_id integer, advanced numeric, maintenance numeric,
		maintenance_html text, compose_project_id text, remark text, created_at datetime, updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	for _, row := range []struct {
		domain string
		id     any
	}{
		{"null.test", nil}, {"empty.test", ""}, {"ok.test", "9"}, {"bad.test", "abc"},
	} {
		if err := legacy.Exec(
			"INSERT INTO sites (domain, type, compose_project_id) VALUES (?, 'proxy', ?)",
			row.domain, row.id,
		).Error; err != nil {
			t.Fatalf("写入旧数据失败: %v", err)
		}
	}
	if sqlDB, err := legacy.DB(); err == nil {
		_ = sqlDB.Close()
	}

	cfg := &pkg.Config{DataDir: dir}
	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("OpenDB 失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	var sites []model.Site
	if err := db.Order("id").Find(&sites).Error; err != nil {
		t.Fatalf("启动后站点读取失败: %v", err)
	}
	if len(sites) != 4 {
		t.Fatalf("站点数量不符: got %d", len(sites))
	}
	if sites[0].ComposeProjectID != nil || sites[1].ComposeProjectID != nil || sites[3].ComposeProjectID != nil {
		t.Errorf("脏值未被清洗: %+v", sites)
	}
	if sites[2].ComposeProjectID == nil || *sites[2].ComposeProjectID != 9 {
		t.Errorf("合法关联丢失: %v", sites[2].ComposeProjectID)
	}
	var colType string
	if err := db.Raw("SELECT type FROM pragma_table_info('sites') WHERE name = 'compose_project_id'").Scan(&colType).Error; err != nil {
		t.Fatalf("读取列类型失败: %v", err)
	}
	if !strings.EqualFold(colType, "integer") {
		t.Errorf("AutoMigrate 未把 TEXT 列改回 integer，当前为 %q", colType)
	}
}

// TestMigrateSSHHostLocalFixesLegacyNull 复现旧库升级：local 列是后加的，
// 历史主机该值为 NULL，/api/ssh/hosts 的 local=false 过滤会把它们全部漏掉。
func TestMigrateSSHHostLocalFixesLegacyNull(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ssh_host_local?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.SshHost{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	for _, row := range []struct {
		name  string
		local any
	}{
		{"legacy", nil}, {"remote", 0}, {"builtin-local", 1},
	} {
		if err := db.Exec(
			"INSERT INTO ssh_hosts (name, host, port, username, auth_type, local) VALUES (?, '10.0.0.1', 22, 'root', 'password', ?)",
			row.name, row.local,
		).Error; err != nil {
			t.Fatalf("写入主机失败: %v", err)
		}
	}

	migrateSSHHostLocal(db)

	var hosts []model.SshHost
	if err := db.Where("local = ? OR local IS NULL", false).Order("id").Find(&hosts).Error; err != nil {
		t.Fatalf("读取 SSH 主机失败: %v", err)
	}
	if len(hosts) != 2 || hosts[0].Name != "legacy" || hosts[1].Name != "remote" {
		t.Fatalf("历史主机未被修复: %+v", hosts)
	}
	var nullCount int64
	if err := db.Raw("SELECT COUNT(*) FROM ssh_hosts WHERE local IS NULL").Scan(&nullCount).Error; err != nil {
		t.Fatalf("统计 NULL 失败: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("仍有 %d 行 local 为 NULL", nullCount)
	}
}
