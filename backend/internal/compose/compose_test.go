package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// TestSyncDBNoopAndDeterministicServices 覆盖两条容易回归的性质：
//  1. compose services 是 map，解析结果必须按名称排序，JSON 才能稳定；
//  2. 项目无变化时 SyncDB 不写库（应用页每次激活都会扫描，空写会放大 SQLite 写锁）。
func TestSyncDBNoopAndDeterministicServices(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const composeYAML = "name: demo\nservices:\n  web:\n    image: nginx:1\n    ports:\n      - \"8080:80\"\n  db:\n    image: mysql:8\n"
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	writeFile(t, composePath, composeYAML)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "compose.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上未关闭的连接会让 t.TempDir 的 RemoveAll 失败（文件被占用）
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.ComposeProject{}); err != nil {
		t.Fatal(err)
	}
	updates := 0
	if err := db.Callback().Update().Before("gorm:update").Register("test:count_updates", func(*gorm.DB) { updates++ }); err != nil {
		t.Fatal(err)
	}
	svc := New(&pkg.Config{ComposeRoot: root}, db, nil, nil)

	list, err := svc.SyncDB()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Services) != 2 {
		t.Fatalf("unexpected project list: %+v", list)
	}
	if list[0].Services[0].Name != "db" || list[0].Services[1].Name != "web" {
		t.Fatalf("services not sorted by name: %+v", list[0].Services)
	}
	var rec model.ComposeProject
	if err := db.First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if rec.Services != mustJSON(list[0].Services) {
		t.Fatalf("stored services mismatch: %s", rec.Services)
	}

	// 同一份文件再扫一次：不应产生任何 UPDATE
	if _, err := svc.SyncDB(); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("unchanged scan wrote DB %d times", updates)
	}

	// 服务镜像变化：应恰好写一次
	writeFile(t, composePath, strings.ReplaceAll(composeYAML, "nginx:1", "nginx:2"))
	if _, err := svc.SyncDB(); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("changed scan wrote DB %d times, want 1", updates)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
