package site

import (
	"strings"
	"testing"

	"zjpanel/backend/internal/pkg"
)

func TestRenderMaintenanceEscapesXSS(t *testing.T) {
	cfg := &pkg.Config{DataDir: t.TempDir()}
	out := RenderMaintenance(cfg, "", `<script>alert(1)</script>`, `<img src=x onerror=alert(2)>`)
	if strings.Contains(out, "<script>") || strings.Contains(out, "<img ") {
		t.Fatalf("维护页未转义 HTML 标签: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&lt;img ") {
		t.Fatalf("维护页转义结果不符合预期: %s", out)
	}
}

func TestValidateSiteInputs(t *testing.T) {
	if err := ValidateDomain("panel.example.com"); err != nil {
		t.Errorf("合法域名应通过: %v", err)
	}
	for _, bad := range []string{"", "localhost", "../etc/passwd", "a.com; root /;", "a.com\n}", ".a.com", "-a.com"} {
		if err := ValidateDomain(bad); err == nil {
			t.Errorf("非法域名应被拒绝: %q", bad)
		}
	}

	if err := ValidateUpstream("127.0.0.1:8080"); err != nil {
		t.Errorf("合法反代目标应通过: %v", err)
	}
	if err := ValidateUpstream("[::1]:8080/api"); err != nil {
		t.Errorf("合法 IPv6 反代目标应通过: %v", err)
	}
	for _, bad := range []string{"", "127.0.0.1:8080; root /;", "127.0.0.1:8080\n}", "http://127.0.0.1/../a"} {
		if err := ValidateUpstream(bad); err == nil {
			t.Errorf("非法反代目标应被拒绝: %q", bad)
		}
	}

	if err := ValidateRoot("/usr/share/nginx/html/site1"); err != nil {
		t.Errorf("合法静态目录应通过: %v", err)
	}
	for _, bad := range []string{"/var/www; autoindex on", "/var/www\n}", "/var/www/../etc", "/var/$host"} {
		if err := ValidateRoot(bad); err == nil {
			t.Errorf("非法静态目录应被拒绝: %q", bad)
		}
	}
}
