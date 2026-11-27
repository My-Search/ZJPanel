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
		DataDir:      data,
		NginxPrefix:  filepath.Join(data, "nginx"),
		ComposeRoot:  filepath.Join(data, "compose"),
	}
}

func TestBuildAndRestoreRoundTrip(t *testing.T) {
	// 临时数据目录，构造少量可备份文件。
	data := t.TempDir()
	cfg := testConfig(data)
	s := New(cfg, nil, nil)

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

	rec, err := s.Build([]string{ItemCerts, ItemNginx})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rec.FileCount == 0 {
		t.Fatal("备份包内无文件")
	}

	// 清空源文件后从包恢复。
	_ = os.RemoveAll(filepath.Join(data, "certs"))
	_ = os.RemoveAll(filepath.Join(data, "nginx"))

	res, err := s.Restore(rec.Path, []string{ItemCerts, ItemNginx})
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
