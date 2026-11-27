package agent

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

func TestHostCheckPersistAndReload(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:hostcheck?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.HostCheck{}); err != nil {
		t.Fatal(err)
	}

	s := &Service{db: db}
	s.saveCheck(7, "ssh", true, `{"findings":[{"key":"PermitRootLogin","risk":"warn"}]}`, "")
	got := s.LatestChecks(7)["ssh"]
	if !got.OK || got.Kind != "ssh" || got.CheckedAt.IsZero() {
		t.Fatalf("首次保存结果不对: %+v", got)
	}
	m, ok := got.Result.(map[string]any)
	if !ok || m["findings"] == nil {
		t.Fatalf("结果 JSON 未还原: %#v", got.Result)
	}

	// 同一主机 + 同一类型再次保存应覆盖，而不是新增一条。
	s.saveCheck(7, "ssh", false, "", "connection refused")
	var count int64
	db.Model(&model.HostCheck{}).Where("host_id = ? AND kind = ?", 7, "ssh").Count(&count)
	if count != 1 {
		t.Fatalf("同类型检查记录数=%d，期望 1", count)
	}
	got = s.LatestChecks(7)["ssh"]
	if got.OK || got.Error != "connection refused" {
		t.Fatalf("失败结果未覆盖: %+v", got)
	}

	// 其他主机不串数据。
	if _, ok := s.LatestChecks(8)["ssh"]; ok {
		t.Fatal("不应读到其他主机的检查结果")
	}
}

func TestAuditKindsCoverCommands(t *testing.T) {
	if len(AuditKinds) != len(AuditCommands) {
		t.Fatalf("AuditKinds=%d, AuditCommands=%d", len(AuditKinds), len(AuditCommands))
	}
	for _, kind := range AuditKinds {
		if AuditCommands[kind] == "" {
			t.Fatalf("检查类型 %s 没有对应 Agent 指令", kind)
		}
	}
}
