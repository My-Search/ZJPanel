package compose

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

func reconcileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reconcile_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.ComposeProject{}, &model.Site{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// 「一切安好」：有站点 online → 正常；全 down → 停用；只回显真正变化的应用。
func TestReconcileAllGood(t *testing.T) {
	db := reconcileTestDB(t)
	projects := []model.ComposeProject{
		{Name: "keep-normal", Dir: "/tmp/a"},                   // 一 up 一 down → 维持正常
		{Name: "to-disabled", Dir: "/tmp/b"},                   // 全 down，当前正常
		{Name: "to-normal", Dir: "/tmp/c", Disabled: true},     // 有 up，当前停用
		{Name: "keep-disabled", Dir: "/tmp/d", Disabled: true}, // 全 down，当前停用
		{Name: "no-sites", Dir: "/tmp/e"},                      // 无网站 → 跳过
		{Name: "unknown", Dir: "/tmp/f"},                       // 站点缺探测结果 → 跳过
	}
	for i := range projects {
		if err := db.Create(&projects[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	sites := []model.Site{
		{Domain: "a1.test", Type: "proxy", ComposeProjectID: &projects[0].ID},
		{Domain: "a2.test", Type: "proxy", ComposeProjectID: &projects[0].ID},
		{Domain: "b1.test", Type: "proxy", ComposeProjectID: &projects[1].ID},
		{Domain: "b2.test", Type: "proxy", ComposeProjectID: &projects[1].ID},
		{Domain: "c1.test", Type: "proxy", ComposeProjectID: &projects[2].ID},
		{Domain: "d1.test", Type: "proxy", ComposeProjectID: &projects[3].ID},
		{Domain: "f1.test", Type: "proxy", ComposeProjectID: &projects[5].ID},
	}
	for i := range sites {
		if err := db.Create(&sites[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	statuses := map[string]string{
		"a1.test": "online", "a2.test": "offline",
		"b1.test": "offline", "b2.test": "error",
		"c1.test": "online",
		"d1.test": "offline",
	}
	s := &Service{db: db, cfg: nil}
	res, err := s.ReconcileAllGood(statuses)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 2 {
		t.Fatalf("变化数应为 2，实际 %+v", res.Changes)
	}
	if res.NormalToDisabled != 1 || res.DisabledToNormal != 1 {
		t.Fatalf("方向统计异常: %+v", res)
	}
	if res.SkippedNoSites != 1 || res.SkippedUnknown != 1 {
		t.Fatalf("跳过统计异常: %+v", res)
	}
	got := map[string]AppStatusChange{}
	for _, c := range res.Changes {
		got[c.Name] = c
	}
	if c := got["to-disabled"]; c.From != "正常" || c.To != "停用" {
		t.Fatalf("b 方向错误: %+v", c)
	}
	if c := got["to-normal"]; c.From != "停用" || c.To != "正常" {
		t.Fatalf("c 方向错误: %+v", c)
	}

	var reloaded []model.ComposeProject
	if err := db.Order("id").Find(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded[1].Disabled != true || reloaded[2].Disabled != false {
		t.Fatalf("落库状态错误: %+v", reloaded)
	}
	if reloaded[3].Disabled != true || reloaded[4].Disabled {
		t.Fatalf("未变化应用被误改: %+v", reloaded)
	}
}
