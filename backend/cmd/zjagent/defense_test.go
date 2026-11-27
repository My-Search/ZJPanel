package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseBans(t *testing.T) {
	out := `table inet zjpanel {
	set banned {
		type ipv4_addr
		flags timeout
		elements = { 203.0.113.9 expires 3599s, 198.51.100.7 expires 86390s }
	}
}`
	items := parseBans(out)
	if len(items) != 2 {
		t.Fatalf("期望 2 条封禁，实际 %d: %+v", len(items), items)
	}
	if items[0].IP != "198.51.100.7" || items[0].Expires != "86390s" {
		t.Fatalf("排序或解析异常: %+v", items[0])
	}
	if items[1].IP != "203.0.113.9" || items[1].Expires != "3599s" {
		t.Fatalf("解析异常: %+v", items[1])
	}
}

func TestParseBansIgnoresHeader(t *testing.T) {
	out := `table inet zjpanel {
	set banned {
		type ipv4_addr
		elements = { 10.0.0.1 expires 60s }
	}
}`
	items := parseBans(out)
	if len(items) != 1 || items[0].IP != "10.0.0.1" {
		t.Fatalf("应只保留 elements 行内的地址: %+v", items)
	}
}

func TestBanRejectsInvalidIP(t *testing.T) {
	for _, ip := range []string{"", "not-an-ip", "127.0.0.1; rm -rf /", "::1"} {
		if ok, _ := banIP(map[string]any{"ip": ip}); ok {
			t.Fatalf("非法地址 %q 不应通过", ip)
		}
	}
}

func TestWebshellPatterns(t *testing.T) {
	samples := []string{
		`<?php eval($_POST['cmd']); ?>`,
		`<?php system($_GET['c']); ?>`,
		`<?php $x = base64_decode($_REQUEST['x']); ?>`,
	}
	for _, s := range samples {
		matched := false
		for _, p := range webPatterns {
			if p.re.MatchString(s) && p.risk == "critical" {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("未命中样本: %s", s)
		}
	}
	safe := `<?php echo "hello"; $name = $_POST['name']; ?>`
	for _, p := range webPatterns {
		if p.re.MatchString(safe) && p.risk == "critical" {
			t.Fatalf("安全样本被误报: %s (%s)", p.label, safe)
		}
	}
}

func TestExecDispatchDefense(t *testing.T) {
	c := &client{}
	for _, kind := range []string{"webshell_audit", "firewall_status", "exposure_audit"} {
		ok, out := c.exec(kind, nil)
		if !ok {
			t.Fatalf("%s 应成功，返回: %s", kind, out)
		}
		if !strings.Contains(out, "findings") && !strings.Contains(out, "backend") {
			t.Fatalf("%s 返回结构异常: %s", kind, out)
		}
	}
	if ok, _ := c.exec("ban_ip", map[string]any{"ip": "not-an-ip"}); ok {
		t.Fatal("非法 IP 不应封禁成功")
	}
}

func TestAuditPayloadShape(t *testing.T) {
	ok, out := auditPayload([]finding{{Key: "k", Value: "v", Risk: "ok"}})
	if !ok {
		t.Fatal("auditPayload 应返回成功")
	}
	var payload struct {
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Findings) != 1 || payload.Findings[0].Key != "k" {
		t.Fatalf("findings 结构异常: %s", out)
	}
	if !strings.Contains(out, "checkedAt") {
		t.Fatalf("缺少 checkedAt: %s", out)
	}
}
