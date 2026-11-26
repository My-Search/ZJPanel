package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/site"
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

// TestRemoveHidesProjectAndClearsSites 覆盖「删除项目」的关键性质：
//  1. 删除最后一个关联网站后 imported/site_id 自动回退（否则卡片一直挂「已导入」且无法清除）；
//  2. 删除项目会清空关联网站并从扫描列表移除，但保留 zjpanel.json（下次导入按它恢复）；
//  3. 恢复后项目回到列表（未导入），再按声明导入能重建站点并标记已导入。
func TestRemoveHidesProjectAndClearsSites(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "docker-compose.yml"), "services:\n  web:\n    image: nginx:1\n    ports:\n      - \"8080:80\"\n")

	cfg := &pkg.Config{ComposeRoot: root, NginxPrefix: t.TempDir()}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "remove.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.ComposeProject{}, &model.Site{}); err != nil {
		t.Fatal(err)
	}
	ng := nginx.New(cfg)
	siteSvc := site.New(cfg, db, ng)
	svc := New(cfg, db, siteSvc, nil)
	siteSvc.OnChange = func(st *model.Site) { svc.WriteDeclForSite(st) }

	rec := model.ComposeProject{Name: "demo", Dir: dir, Services: "[]", Imported: true}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}
	first := model.Site{Domain: "a.example.com", Type: "proxy", Upstream: "127.0.0.1:8080", ComposeProjectID: &rec.ID}
	second := model.Site{Domain: "b.example.com", Type: "proxy", Upstream: "127.0.0.1:8080", ComposeProjectID: &rec.ID}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	rec.SiteID = &first.ID
	if err := db.Save(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.WriteDecl(rec.ID); err != nil {
		t.Fatal(err)
	}

	// 单独删掉一个站点：仍是已导入（还有一个站点在）
	if err := siteSvc.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	var got model.ComposeProject
	if err := db.First(&got, rec.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.Imported || got.SiteID == nil || *got.SiteID != second.ID {
		t.Fatalf("删掉一个站点后状态异常: imported=%v siteId=%v", got.Imported, got.SiteID)
	}

	removed, err := svc.Remove(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != second.Domain {
		t.Fatalf("remove removed=%v", removed)
	}
	var sites []model.Site
	db.Where("compose_project_id = ?", rec.ID).Find(&sites)
	if len(sites) != 0 {
		t.Fatalf("关联网站未清空: %+v", sites)
	}
	if err := db.First(&got, rec.ID).Error; err != nil {
		t.Fatalf("项目记录不应被删除（恢复需要）: %v", err)
	}
	if got.Imported || got.SiteID != nil || !got.Hidden {
		t.Fatalf("项目状态未回退: imported=%v siteId=%v hidden=%v", got.Imported, got.SiteID, got.Hidden)
	}
	// 扫描列表不再出现该项目
	list, err := svc.SyncDB()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("已删除项目仍出现在扫描列表: %+v", list)
	}
	hidden, err := svc.HiddenProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 1 || hidden[0]["id"] != rec.ID || hidden[0]["exists"] != true {
		t.Fatalf("已删除项目记录异常: %+v", hidden)
	}
	// 声明文件必须保留（删除只解除面板关联，下次导入按它恢复）
	decl, derr := svc.ReadDecl(dir)
	if derr != nil || decl == nil || len(decl.Sites) != 1 || decl.Sites[0].Domain != second.Domain {
		t.Fatalf("删除后声明文件未保留: decl=%+v err=%v", decl, derr)
	}

	// 恢复：回到列表，仍是未导入状态
	if err := svc.Restore(rec.ID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.SyncDB()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Dir != dir || list[0].Imported {
		t.Fatalf("恢复后列表异常: %+v", list)
	}
	// 重新导入：按 zjpanel.json 恢复站点并标记已导入
	created, _, err := svc.ImportDecl(dir)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("按声明恢复站点数异常: created=%d decl=%+v", created, decl)
	}
	db.Where("compose_project_id = ?", rec.ID).Find(&sites)
	if len(sites) != 1 || sites[0].Domain != second.Domain {
		t.Fatalf("按声明恢复的站点异常: %+v", sites)
	}
	if err := db.First(&got, rec.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.Imported || got.Hidden || got.SiteID == nil {
		t.Fatalf("重新导入后状态异常: imported=%v hidden=%v siteId=%v", got.Imported, got.Hidden, got.SiteID)
	}
}
