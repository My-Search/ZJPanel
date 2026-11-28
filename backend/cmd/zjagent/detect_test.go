package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// 捕获 emit 事件的辅助：list() 返回自上次调用以来的新事件（排水语义）。
func captureEmit() (func(map[string]any), func() []map[string]any) {
	var mu = make(chan struct{}, 1)
	var evs []map[string]any
	mu <- struct{}{}
	emit := func(ev map[string]any) {
		<-mu
		evs = append(evs, ev)
		mu <- struct{}{}
	}
	return emit, func() []map[string]any {
		<-mu
		defer func() { mu <- struct{}{} }()
		out := evs
		evs = nil
		return out
	}
}

// freshPolicy 让用例之间互不污染：写空策略文件并重置全局缓存。
func freshPolicy(t *testing.T, pol defensePolicy) {
	t.Helper()
	polLoaded = false
	polCur = pol
	polLoaded = true
	t.Cleanup(func() { polLoaded = false })
}

func TestSSHLineParse(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	lines := []struct {
		line string
		fail bool
		ip   string
		user string
	}{
		{"Nov 28 10:00:01 srv sshd[913]: Failed password for invalid user admin from 203.0.113.9 port 45632 ssh2", true, "203.0.113.9", "admin"},
		{"Failed password for root from 203.0.113.9 port 45634 ssh2", true, "203.0.113.9", "root"},
		{"Invalid user oracle from 198.51.100.7 port 33912", true, "198.51.100.7", "oracle"},
		{"error: maximum authentication attempts exceeded for root from 203.0.113.9 port 45636 ssh2 [preauth]", true, "203.0.113.9", ""},
		{"Too many authentication failures for root from 198.51.100.7 port 33914 ssh2", true, "198.51.100.7", "root"},
		{"Connection closed by authenticating user pi 203.0.113.9 port 45638 [preauth]", true, "203.0.113.9", ""},
		{"Accepted password for root from 10.0.0.2 port 50000 ssh2", false, "10.0.0.2", "root"},
		{"systemd-logind[512]: New session 42 of user root.", false, "", ""},
	}
	tr := newAttackTracker()
	seen := map[string]bool{}
	emit, list := captureEmit()
	for _, l := range lines {
		tr.sshLine(l.line, emit)
	}
	for _, ev := range list() {
		seen[ev["ip"].(string)] = true
		if ev["kind"] != "ssh_bruteforce" {
			t.Fatalf("意外事件: %+v", ev)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("仅单条日志不应达阈值: %v", seen)
	}
}

// TestBruteforceThreshold 达阈值只上报一次（同窗口聚合），继续失败不重复上报。
func TestBruteforceThreshold(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	tr := newAttackTracker()
	emit, list := captureEmit()
	for i := 0; i < 9; i++ {
		tr.sshLine("Failed password for root from 203.0.113.9 port 1 ssh2", emit)
	}
	if n := len(list()); n != 0 {
		t.Fatalf("未达阈值不应上报，实际 %d 条", n)
	}
	tr.sshLine("Failed password for root from 203.0.113.9 port 1 ssh2", emit) // 第 10 次
	evs := list()
	if len(evs) != 1 {
		t.Fatalf("达阈值应恰好上报 1 条，实际 %d 条", len(evs))
	}
	ev := evs[0]
	if ev["count"] != 10 || ev["kind"] != "ssh_bruteforce" || ev["severity"] != "warning" {
		t.Fatalf("事件内容异常: %+v", ev)
	}
	// 同窗口内继续失败（未到 2 倍阈值）：不再重复上报
	for i := 0; i < 5; i++ {
		tr.sshLine("Failed password for root from 203.0.113.9 port 1 ssh2", emit)
	}
	if n := len(list()); n != 0 {
		t.Fatalf("同窗口不应重复上报，实际多出 %d 条", n)
	}
}

// TestBruteforceCritical 双倍阈值升级为 critical（升级允许突破同窗口聚合再报一次）。
func TestBruteforceCritical(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	tr := newAttackTracker()
	emit, list := captureEmit()
	for i := 0; i < 21; i++ {
		tr.sshLine("Invalid user u from 198.51.100.7", emit)
	}
	evs := list()
	if len(evs) != 2 {
		t.Fatalf("应报 2 次（阈值 + 升级），实际 %d: %+v", len(evs), evs)
	}
	if evs[0]["severity"] != "warning" || evs[1]["severity"] != "critical" {
		t.Fatalf("级别应从 warning 升级到 critical: %+v", evs)
	}
	if evs[1]["count"] != 20 {
		t.Fatalf("升级事件计数应为 20: %+v", evs[1])
	}
}

// TestBreakinCorrelation 达阈值后同 IP 成功登录 → 疑似爆破成功（critical），且只报一次。
func TestBreakinCorrelation(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	tr := newAttackTracker()
	emit, list := captureEmit()
	for i := 0; i < 10; i++ {
		tr.sshLine("Failed password for root from 203.0.113.9 port 1 ssh2", emit)
	}
	if n := len(list()); n != 1 {
		t.Fatalf("应先有爆破事件: %d", n)
	}
	tr.sshLine("Accepted password for root from 203.0.113.9 port 2 ssh2", emit)
	evs := list()
	if len(evs) != 1 || evs[0]["kind"] != "ssh_breakin" || evs[0]["severity"] != "critical" {
		t.Fatalf("爆破后成功登录应产生 ssh_breakin: %+v", evs)
	}
	// 再次成功登录不重复关联
	tr.sshLine("Accepted publickey for root from 203.0.113.9 port 3 ssh2", emit)
	if n := len(list()); n != 0 {
		t.Fatalf("关联只报一次，多出 %d 条", n)
	}
}

// TestIPProtected 白名单保护：回环/私网/白名单网段/面板地址（面板用 IP 字面量，不依赖 DNS）。
func TestIPProtected(t *testing.T) {
	freshPolicy(t, defensePolicy{
		Enabled:   true,
		SSH:       sshRules{WindowSecs: 600, MaxFails: 10, BanSeconds: 3600, AutoBan: true},
		Whitelist: []string{"203.0.113.0/24", "8.8.8.8"},
	})
	panelAddrFn = func() string { return "" } // 隔离 DNS：面板地址保护单独验证
	t.Cleanup(func() { panelAddrFn = func() string { return loadConfig().Panel } })

	cases := map[string]bool{
		"127.0.0.1":   true,
		"192.168.1.5": true,  // 私网默认保护
		"10.20.30.40": true,  // 私网默认保护
		"203.0.113.9": true,  // 白名单网段
		"8.8.8.8":     true,  // 白名单单 IP
		"8.8.4.4":     false, // 公网不在名单
	}
	for ip, want := range cases {
		if got := ipProtected(ip, currentPolicy()); got != want {
			t.Fatalf("ipProtected(%s) = %v, want %v", ip, got, want)
		}
	}
	// 面板地址（IP 字面量）受保护；开启 banPrivate 后私网可封
	panelAddrFn = func() string { return "8.8.4.4" }
	if !ipProtected("8.8.4.4", currentPolicy()) {
		t.Fatal("面板地址应受保护")
	}
	pol := currentPolicy()
	pol.BanPrivate = true
	if ipProtected("192.168.1.5", pol) {
		t.Fatal("banPrivate 开启后私网应可封")
	}
	if ip := ipProtected("", pol); ip {
		t.Fatal("非法地址不归白名单管")
	}
}

// TestPanelHost 解析面板地址的主机名/IP。
func TestPanelHost(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:9000":  "127.0.0.1",
		"https://panel.x.com/":   "panel.x.com",
		"panel.x.com:9000":       "panel.x.com",
		"[::1]:9000":             "::1",
		"http://[fd00::1]:9000/": "fd00::1",
	}
	for in, want := range cases {
		if got := panelHost(in); got != want {
			t.Fatalf("panelHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefensePolicyCmd 指令解析 + 钳位 + 生效反馈。
func TestDefensePolicyCmd(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	ok, out := defensePolicyCmd(map[string]any{
		"enabled": true,
		"ssh":     map[string]any{"windowSecs": 120, "maxFails": 5, "banSeconds": 1800, "autoBan": true},
	})
	if !ok || !strings.Contains(out, "已生效") {
		t.Fatalf("策略下发应成功: %v %s", ok, out)
	}
	pol := currentPolicy()
	if pol.SSH.WindowSecs != 120 || pol.SSH.MaxFails != 5 || pol.SSH.BanSeconds != 1800 {
		t.Fatalf("策略未生效: %+v", pol.SSH)
	}
	// 畸形值钳位
	ok, _ = defensePolicyCmd(map[string]any{"ssh": map[string]any{"windowSecs": -1, "maxFails": 0, "banSeconds": 99999999}})
	if !ok {
		t.Fatal("含畸形值的策略也应落地（钳位处理）")
	}
	if pol := currentPolicy(); pol.SSH.WindowSecs != 60 || pol.SSH.MaxFails != 3 || pol.SSH.BanSeconds != 7*24*3600 {
		t.Fatalf("钳位结果异常: %+v", pol.SSH)
	}
}

// TestTrimSpool spool 超限裁掉最旧一半（与 flushSpool ≤200 条补传语义一致）。
func TestTrimSpool(t *testing.T) {
	dir := t.TempDir()
	old := spoolPath
	spoolPath = dir + "/events.jsonl"
	t.Cleanup(func() { spoolPath = old })

	write := func(n int) {
		var b strings.Builder
		for i := 0; i < n; i++ {
			_ = json.NewEncoder(&b).Encode(map[string]any{"i": i})
		}
		_ = os.WriteFile(spoolPath, []byte(b.String()), 0o600)
	}
	write(250)
	trimSpool()
	b, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != spoolMax/2 {
		t.Fatalf("裁剪后应剩 %d 行，实际 %d", spoolMax/2, len(lines))
	}
	var last map[string]any
	if json.Unmarshal([]byte(lines[len(lines)-1]), &last) != nil || last["i"].(float64) != 249 {
		t.Fatalf("应保留最新的行: %v", last)
	}
	// 未超限不动
	write(10)
	trimSpool()
	if b, _ := os.ReadFile(spoolPath); len(strings.Split(strings.TrimSpace(string(b)), "\n")) != 10 {
		t.Fatal("未超限不应裁剪")
	}
}

// TestEventToSpoolWhenOffline 断线时事件必须落 spool（补传依赖）。
func TestEventToSpoolWhenOffline(t *testing.T) {
	dir := t.TempDir()
	old := spoolPath
	spoolPath = dir + "/events.jsonl"
	t.Cleanup(func() { spoolPath = old })

	c := &client{} // 无连接 → send 失败 → spool
	c.emitEvent(map[string]any{"kind": "ssh_bruteforce", "ip": "203.0.113.9"})
	b, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatalf("断线事件未落 spool: %v", err)
	}
	var ev map[string]any
	if json.Unmarshal(b, &ev) != nil {
		t.Fatalf("spool 行非法: %s", b)
	}
	if _, ok := ev["time"]; !ok {
		t.Fatalf("spool 事件应带 time: %v", ev)
	}
}

// TestAttackTrackerGC 窗口过期后状态被清理。
func TestAttackTrackerGC(t *testing.T) {
	freshPolicy(t, defaultDefensePolicy())
	tr := newAttackTracker()
	tr.fails["1.2.3.4"] = []time.Time{time.Now().Add(-2 * time.Hour)}
	tr.armed["5.6.7.8"] = time.Now().Add(-time.Hour)
	tr.report["9.10.11.12"] = time.Now().Add(-2 * time.Hour)
	tr.gc()
	if len(tr.fails) != 0 || len(tr.armed) != 0 || len(tr.report) != 0 {
		t.Fatalf("过期状态应被清理: %+v", tr)
	}
}
