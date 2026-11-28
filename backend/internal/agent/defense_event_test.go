package agent

import (
	"encoding/json"
	"testing"
	"time"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/ws"
)

// TestOnEventPublishesSecurity 安全类事件必须同时进入事件总线（AI 运维订阅 host.security），
// 普通事件（如心跳类 event）不应触发发布。
func TestOnEventPublishesSecurity(t *testing.T) {
	b := bus.New()
	got := make(chan map[string]any, 4)
	b.Subscribe("host.security", func(payload map[string]any) { got <- payload })

	s := New(nil, nil, nil, ws.NewHub(), b)
	s.onEvent(3, map[string]any{"event": map[string]any{
		"kind": "ssh_bruteforce", "severity": "warning", "ip": "203.0.113.9",
		"user": "admin", "count": 12, "action": "banned", "detail": "10 分钟内 12 次 SSH 认证失败",
		"time": time.Now().Format(time.RFC3339),
	}})

	select {
	case p := <-got:
		if p["kind"] != "ssh_bruteforce" || p["ip"] != "203.0.113.9" {
			t.Fatalf("载荷字段异常: %+v", p)
		}
		if hid, ok := p["hostId"].(uint); !ok || hid != 3 {
			if f, ok2 := p["hostId"].(float64); !ok2 || uint(f) != 3 {
				t.Fatalf("hostId 应为 3: %+v", p["hostId"])
			}
		}
		if p["action"] != "banned" {
			t.Fatalf("action 应为 banned: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("host.security 未发布")
	}

	// 普通事件不发布
	s.onEvent(3, map[string]any{"event": map[string]any{"kind": "sys", "detail": "x"}})
	select {
	case p := <-got:
		t.Fatalf("普通事件不应发布 host.security: %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestDefensePolicySanitize 策略钳位与白名单规范化（非法参数回落默认，防下发畸形值）。
func TestDefensePolicySanitize(t *testing.T) {
	pol := sanitizeDefensePolicy(HostDefensePolicy{
		SSH:       SSHRules{WindowSecs: 5, MaxFails: 1, BanSeconds: 10},
		Whitelist: []string{" 10.0.0.0/8 ", "", "10.0.0.0/8", "203.0.113.9"},
	})
	if pol.SSH.WindowSecs != 60 || pol.SSH.MaxFails != 3 || pol.SSH.BanSeconds != 60 {
		t.Fatalf("非法参数应钳位到下限: %+v", pol.SSH)
	}
	if len(pol.Whitelist) != 2 || pol.Whitelist[0] != "10.0.0.0/8" || pol.Whitelist[1] != "203.0.113.9" {
		t.Fatalf("白名单应去空去重: %+v", pol.Whitelist)
	}
}

// TestDefensePolicyRoundTrip 面板策略 JSON 结构与 Agent 侧一致（下发依赖字段名完全对应）。
func TestDefensePolicyRoundTrip(t *testing.T) {
	pol := DefaultDefensePolicy()
	b, err := json.Marshal(pol)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"enabled", "ssh", "whitelist", "banPrivate"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺少字段 %s: %s", k, b)
		}
	}
	ssh := m["ssh"].(map[string]any)
	for _, k := range []string{"windowSecs", "maxFails", "banSeconds", "autoBan"} {
		if _, ok := ssh[k]; !ok {
			t.Fatalf("ssh 缺少字段 %s: %s", k, b)
		}
	}
}
