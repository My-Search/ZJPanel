package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zjpanel/backend/internal/pkg"
)

func testConfig(data string) *pkg.Config {
	return &pkg.Config{
		DataDir:     data,
		NginxPrefix: filepath.Join(data, "nginx"),
		ComposeRoot: filepath.Join(data, "compose"),
	}
}

func TestComposeWholeDirBackup(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)

	// 一个含 compose 的应用目录（带嵌套数据），一个不含 compose 的普通目录。
	app := filepath.Join(data, "compose", "blog")
	for _, p := range []string{
		"docker-compose.yml",
		"config/app.yaml",
		"data/upload/2026/logo.txt",
	} {
		full := filepath.Join(app, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(data, "compose", "empty")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var files []collectedFile
	if err := s.collectCompose(&files); err != nil {
		t.Fatalf("collectCompose: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[filepath.ToSlash(f.archive)] = true
	}
	for _, want := range []string{
		"compose/blog/docker-compose.yml",
		"compose/blog/config/app.yaml",
		"compose/blog/data/upload/2026/logo.txt",
	} {
		if !got[want] {
			t.Fatalf("整目录备份缺少 %s；实际: %v", want, got)
		}
	}
	if got["compose/empty/readme.txt"] {
		t.Fatal("不含 compose 文件的目录不应被收录")
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	if _, err := safeJoin(base, "../escape.txt"); err == nil {
		t.Fatal("应拒绝 .. 越界路径")
	}
	if _, err := safeJoin(base, "a/../../escape.txt"); err == nil {
		t.Fatal("应拒绝嵌套 .. 越界路径")
	}
	if _, err := safeJoin(base, "a/../b.txt"); err == nil {
		t.Fatal("含 .. 段的路径一律拒绝（备份包内不应出现）")
	}
	if p, err := safeJoin(base, "a/b/c.txt"); err != nil {
		t.Fatalf("正常路径不应报错: %v", err)
	} else if !strings.HasPrefix(p, base) {
		t.Fatalf("拼接结果越界: %s", p)
	}
}

func TestSanitizeRedirectURI(t *testing.T) {
	okURL := "https://panel.example.com/api/backup/oauth/callback"
	if sanitizeRedirectURI(okURL) != okURL {
		t.Fatal("合法回调地址被拒绝")
	}
	for _, bad := range []string{
		"",
		"ftp://panel.example.com/api/backup/oauth/callback",
		"https://panel.example.com/evil",
		"https://panel.example.com/api/backup/oauth/callback?next=evil",
		"//panel.example.com/api/backup/oauth/callback",
		"https://panel.example.com/api/backup/oauth/callback\nX: y",
	} {
		if sanitizeRedirectURI(bad) != "" {
			t.Fatalf("非法回调地址未拒绝: %q", bad)
		}
	}
}

func TestBuildAndRestoreRoundTrip(t *testing.T) {
	// 临时数据目录，构造少量可备份文件。
	data := t.TempDir()
	cfg := testConfig(data)
	s := New(cfg, nil, nil, nil, nil)

	// 写一个证书文件与一个 nginx 站点配置。
	mk := func(dir, name, body string) {
		if err := os.MkdirAll(filepath.Join(data, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("certs/1", "fullchain.pem", "cert-body")
	mk("nginx/sites-available", "a.example.com.conf", "server { listen 80; }")

	rec, err := s.Build([]string{ItemCerts, ItemNginx}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rec.FileCount == 0 {
		t.Fatal("备份包内无文件")
	}

	// 清空源文件后从包恢复。
	_ = os.RemoveAll(filepath.Join(data, "certs"))
	_ = os.RemoveAll(filepath.Join(data, "nginx"))

	res, err := s.Restore(rec.Path, []string{ItemCerts, ItemNginx}, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(res.Restored) == 0 {
		t.Fatal("恢复后无文件")
	}
	// 校验证书内容还原。
	b, err := os.ReadFile(filepath.Join(data, "certs/1/fullchain.pem"))
	if err != nil || string(b) != "cert-body" {
		t.Fatalf("证书未正确恢复: %v", err)
	}
	// 未勾选的项目不会被恢复：这里 items 只含 certs/nginx，无 db/compose。
	for _, sk := range res.Skipped {
		if strings.HasPrefix(sk, "db/") || strings.HasPrefix(sk, "compose/") {
			t.Fatalf("不应恢复未勾选项: %s", sk)
		}
	}
}
