package sitecheck

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/model"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:sitecheck_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Site{}, &model.ComposeProject{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

func info(domain, status string) *Info {
	return &Info{Domain: domain, Status: status, CheckedAt: time.Now()}
}

// 停用应用下的站点掉线不发 sitecheck.down；正常应用的站点照常发。
func TestDisabledProjectSuppressesDownEvent(t *testing.T) {
	db := testDB(t)
	p := model.ComposeProject{Name: "stopped", Dir: "/tmp/stopped", Disabled: true}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	site := model.Site{Domain: "off.example", Type: "proxy", ComposeProjectID: &p.ID}
	if err := db.Create(&site).Error; err != nil {
		t.Fatal(err)
	}

	b := bus.New()
	var down []string
	b.Subscribe("sitecheck.down", func(p map[string]any) { down = append(down, fmt.Sprint(p["domain"])) })

	s := New(db, b)
	// 先来一次 online 作为基线，再来一次 offline 触发状态转移。
	s.record(site, info(site.Domain, "online"), true, false)
	s.record(site, info(site.Domain, "offline"), true, true)
	if len(down) != 0 {
		t.Fatalf("停用应用站点不应告警，实际收到 %v", down)
	}

	// 对照：正常应用同样走一遍，必须收到 down。
	normal := model.Site{Domain: "live.example", Type: "proxy"}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatal(err)
	}
	s.record(normal, info(normal.Domain, "online"), false, false)
	s.record(normal, info(normal.Domain, "offline"), false, true)
	if len(down) != 1 || down[0] != normal.Domain {
		t.Fatalf("正常站点掉线应告警，实际 %v", down)
	}
}

// 探测全轮：停用项目状态落库，但不发掉线事件。
func TestProbeAllPersistsAndRespectsDisabled(t *testing.T) {
	db := testDB(t)
	p := model.ComposeProject{Name: "stopped", Dir: "/tmp/p2", Disabled: true}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	site := model.Site{Domain: "off2.example", Type: "proxy", ComposeProjectID: &p.ID}
	if err := db.Create(&site).Error; err != nil {
		t.Fatal(err)
	}
	b := bus.New()
	gotDown := 0
	b.Subscribe("sitecheck.down", func(map[string]any) { gotDown++ })

	s := New(db, b)
	online := true
	s.prober = func(st model.Site) *Info {
		if online {
			return info(st.Domain, "online")
		}
		return info(st.Domain, "offline")
	}
	s.probeAll() // 基线 online
	online = false
	s.probeAll() // 掉线，但应用已停用

	if gotDown != 0 {
		t.Fatalf("停用应用探测全轮不应告警，实际 %d 次", gotDown)
	}
	var rec model.Site
	if err := db.First(&rec, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rec.CheckStatus != "offline" {
		t.Fatalf("探测结果应落库，实际 %q", rec.CheckStatus)
	}
}

// 主动全量探测（一切安好）：结果同步返回并落库，但不制造 down 告警。
func TestProbeAllNowSilent(t *testing.T) {
	db := testDB(t)
	site := model.Site{Domain: "now.example", Type: "proxy"}
	if err := db.Create(&site).Error; err != nil {
		t.Fatal(err)
	}
	b := bus.New()
	gotDown := 0
	b.Subscribe("sitecheck.down", func(map[string]any) { gotDown++ })

	s := New(db, b)
	online := true
	s.prober = func(st model.Site) *Info {
		if online {
			return info(st.Domain, "online")
		}
		return info(st.Domain, "offline")
	}
	s.probeAll() // 基线 online
	online = false
	out, err := s.ProbeAllNow()
	if err != nil {
		t.Fatal(err)
	}
	if out[site.Domain] == nil || out[site.Domain].Status != "offline" {
		t.Fatalf("主动探测应返回最新结果，实际 %+v", out)
	}
	if gotDown != 0 {
		t.Fatalf("主动探测不应告警，实际 %d 次", gotDown)
	}
	var rec model.Site
	if err := db.First(&rec, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rec.CheckStatus != "offline" {
		t.Fatalf("主动探测结果应落库，实际 %q", rec.CheckStatus)
	}
}
