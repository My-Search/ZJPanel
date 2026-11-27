package waf

import (
	"testing"
	"time"

	"zjpanel/backend/internal/logmap"
	"zjpanel/backend/internal/pkg"
)

func newTestService(t *testing.T) (*Service, *[]BanRequest) {
	t.Helper()
	s := New(&pkg.Config{DataDir: t.TempDir()}, nil)
	st := defaultSettings()
	st.Enabled = true
	st.BlockMode = ActionBlock
	s.settings = st
	s.rules = builtinRules()
	s.engine.Rebuild(s.rules, st.BlockMode)
	s.setExempt(st.Exempt)
	bans := &[]BanRequest{}
	s.SetBanFunc(func(reqs []BanRequest) (int, error) {
		*bans = append(*bans, reqs...)
		return len(reqs), nil
	})
	t.Cleanup(s.Close) // 关闭命中日志句柄，否则 Windows 上临时目录无法清理
	return s, bans
}

func entry(ip, path string, at time.Time) logmap.Entry {
	return logmap.Entry{IP: ip, Site: "a.example.com", Method: "GET", Path: path, UA: "Mozilla/5.0", Status: 200, Time: at}
}

func TestServiceRecordAndBan(t *testing.T) {
	s, bans := newTestService(t)
	now := time.Now()
	s.handle(entry("203.0.113.9", `/search?q=1%27%20OR%201%3D1--`, now))

	hits := s.Hits(HitFilter{Limit: 10})
	if len(hits) != 1 || hits[0].RuleID != "sqli" {
		t.Fatalf("应记录 1 条 SQLi 命中，实际 %d 条", len(hits))
	}
	s.flushBans()
	if len(*bans) != 1 || (*bans)[0].Minutes != 10 {
		t.Fatalf("首次封禁应为 10 分钟，实际 %+v", *bans)
	}

	// 封禁期内再次作恶不升级阶梯
	s.handle(entry("203.0.113.9", `/search?q=1%27%20OR%201%3D1--`, now.Add(2*time.Minute)))
	s.flushBans()
	if len(*bans) != 1 {
		t.Fatalf("封禁期内不应重复下发，实际 %+v", *bans)
	}

	// 封禁到期后累犯，阶梯升到 60 分钟
	s.mu.Lock()
	s.banned["203.0.113.9"] = now.Add(-time.Minute)
	s.mu.Unlock()
	s.handle(entry("203.0.113.9", `/api?id=1%20UNION%20SELECT%20a,b%20FROM%20users`, now.Add(20*time.Minute)))
	s.flushBans()
	if len(*bans) != 2 || (*bans)[1].Minutes != 60 {
		t.Fatalf("累犯第二次应为 60 分钟，实际 %+v", *bans)
	}
}

func TestServiceDedupeAndExempt(t *testing.T) {
	s, bans := newTestService(t)
	now := time.Now()
	s.handle(entry("198.51.100.7", `/x?q=%3Cscript%3Ealert(1)%3C/script%3E`, now))
	s.handle(entry("198.51.100.7", `/x?q=%3Cscript%3Ealert(1)%3C/script%3E`, now.Add(5*time.Second)))

	hits := s.Hits(HitFilter{Limit: 10})
	if len(hits) != 1 {
		t.Fatalf("同 IP 同规则窗口内应合并为 1 条，实际 %d", len(hits))
	}
	if hits[0].Count != 2 {
		t.Fatalf("合并后次数应为 2，实际 %d", hits[0].Count)
	}
	s.flushBans()
	if len(*bans) != 1 {
		t.Fatalf("合并期内只应下发一次封禁，实际 %+v", *bans)
	}

	// 本机回环默认豁免
	s.handle(entry("127.0.0.1", `/search?q=1%27%20OR%201%3D1--`, now))
	if got := len(s.Hits(HitFilter{Limit: 10})); got != 1 {
		t.Fatalf("回环地址不应产生命中，实际 %d 条", got)
	}
}

func TestServiceDisabled(t *testing.T) {
	s, bans := newTestService(t)
	s.mu.Lock()
	s.settings.Enabled = false
	s.mu.Unlock()
	s.handle(entry("203.0.113.9", `/search?q=1%27%20OR%201%3D1--`, time.Now()))
	if len(s.Hits(HitFilter{Limit: 10})) != 0 {
		t.Fatal("关闭 WAF 后不应产生命中")
	}
	s.flushBans()
	if len(*bans) != 0 {
		t.Fatal("关闭 WAF 后不应下发封禁")
	}
}

func TestSettingsValidation(t *testing.T) {
	s, _ := newTestService(t)
	bad := s.View().Settings
	bad.Exempt = []string{"not-an-ip"}
	if err := s.SetSettings(bad); err == nil {
		t.Fatal("非法豁免项应被拒绝")
	}
	st := defaultSettings()
	st.BanLadder = []int{0, -5, 1440, 99999}
	if err := s.SetSettings(st); err != nil {
		t.Fatalf("阶梯应被归一化而不是拒绝: %v", err)
	}
}
