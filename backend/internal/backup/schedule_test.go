package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

// testScheduleDB 内存库：只迁移 Setting 表。
func testScheduleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:backup_schedule?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// fakeSched 记录调度的 Scheduler 假实现。
type fakeSched struct {
	adds    []string
	removed []any
}

func (f *fakeSched) AddFunc(spec string, fn func()) any {
	f.adds = append(f.adds, spec)
	return len(f.adds)
}
func (f *fakeSched) RemoveByID(id any) { f.removed = append(f.removed, id) }
func (f *fakeSched) NextByID(id any) time.Time {
	if id == nil {
		return time.Time{}
	}
	return time.Now().Add(time.Hour)
}

// TestSaveScheduleValidationAndReschedule 保存校验 + 调度重排 + 持久化回读。
func TestSaveScheduleValidationAndReschedule(t *testing.T) {
	s := New(testConfig(t.TempDir()), testScheduleDB(t), nil, nil, nil)

	// 非法 cron 被拒绝。
	if _, err := s.SaveSchedule(ScheduleConfig{Enabled: true, Cron: "every day"}); err == nil {
		t.Fatal("非法 cron 应被拒绝")
	}

	// 启用：注册一条调度。
	fs := &fakeSched{}
	s.StartScheduler(fs)
	v, err := s.SaveSchedule(ScheduleConfig{Enabled: true, Cron: "*/5 * * * *", Items: []string{ItemCerts}, Keep: 3})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if len(fs.adds) != 1 || fs.adds[0] != "*/5 * * * *" {
		t.Fatalf("应注册一条调度，实际 adds=%v", fs.adds)
	}
	if v.NextRun.IsZero() {
		t.Fatal("启用状态应返回下次执行时间")
	}

	// 重新保存（改为禁用）：旧条目被移除，不新增。
	v, err = s.SaveSchedule(ScheduleConfig{Enabled: false, Cron: "0 4 * * *", Keep: 0})
	if err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}
	if len(fs.adds) != 1 || len(fs.removed) != 1 {
		t.Fatalf("禁用应只移除旧条目: adds=%v removed=%v", fs.adds, fs.removed)
	}
	if !v.NextRun.IsZero() {
		t.Fatal("禁用状态不应有下次执行时间")
	}

	// 持久化回读：cron/items/keep 与保存值一致，未启用时回默认值。
	got := s.loadSchedule()
	if got.Cron != "0 4 * * *" || got.Keep != 0 {
		t.Fatalf("配置未按保存值回读: %+v", got)
	}

	// 未注入调度器时保存仍生效（仅不调度）。
	s2 := New(testConfig(t.TempDir()), testScheduleDB(t), nil, nil, nil)
	if _, err := s2.SaveSchedule(ScheduleConfig{Enabled: true, Cron: "0 3 * * *"}); err != nil {
		t.Fatalf("无调度器时保存失败: %v", err)
	}
	if s2.ScheduleView().Enabled != true {
		t.Fatal("配置未生效")
	}
}

// TestScheduleDefaults 默认配置：未保存过时每天 03:00 + 全选 + 保留 10。
func TestScheduleDefaults(t *testing.T) {
	s := New(testConfig(t.TempDir()), testScheduleDB(t), nil, nil, nil)
	v := s.loadSchedule()
	if v.Cron != defaultScheduleCron || v.Keep != defaultScheduleKeep || len(v.Items) != len(AllItems) {
		t.Fatalf("默认配置异常: %+v", v)
	}
}

// TestPruneKeepRetention 保留清理：只动 zjpanel-backup-*，最近 keep 份与临时包不碰。
func TestPruneKeepRetention(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)

	// 造 5 个假备份（不同 mtime，倒序可控）+ 1 个恢复临时包 + 1 个外来文件。
	mk := func(name string, age time.Duration) {
		p := filepath.Join(data, "backups", name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := time.Now().Add(-age)
		if err := os.Chtimes(p, st, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(data, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		mk("zjpanel-backup-2026010"+string(rune('1'+i))+"-000000.tar.gz", time.Duration(5-i)*time.Hour)
	}
	mk("restore-tmp.tar.gz", time.Hour)
	mk("unrelated.txt", time.Hour)

	if n := s.pruneKeep(3); n != 2 {
		t.Fatalf("应清理 2 个旧备份，实际 %d", n)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	remain := map[string]bool{}
	for _, rec := range list {
		remain[rec.Name] = true
	}
	if len(remain) != 4 { // 最新的 3 份备份 + restore 临时包
		t.Fatalf("剩余文件数异常: %v", remain)
	}
	// 保留最新的 3 份（mtime 新的在前），最旧的 2 份被清理。
	for _, name := range []string{
		"zjpanel-backup-20260105-000000.tar.gz",
		"zjpanel-backup-20260104-000000.tar.gz",
		"zjpanel-backup-20260103-000000.tar.gz",
		"restore-tmp.tar.gz",
	} {
		if !remain[name] {
			t.Fatalf("%s 不应被清理: %v", name, remain)
		}
	}
	for _, name := range []string{
		"zjpanel-backup-20260101-000000.tar.gz",
		"zjpanel-backup-20260102-000000.tar.gz",
	} {
		if remain[name] {
			t.Fatalf("%s 应被清理: %v", name, remain)
		}
	}
}
