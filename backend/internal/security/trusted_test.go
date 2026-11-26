package security

import "testing"

func TestTrustedLocalAndWhitelist(t *testing.T) {
	s := &Service{}
	s.setTrusted([]string{"203.0.113.0/24", "198.51.100.7"})

	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},     // 面板/探测/同机运维
		{"127.9.9.9", true},     // 回环整段
		{"::1", true},           // IPv6 回环
		{"203.0.113.9", true},   // 白名单 CIDR
		{"198.51.100.7", true},  // 白名单单 IP
		{"198.51.100.8", false}, // 白名单外
		{"10.0.0.1", false},     // 内网不默认可信（避免漏报内网攻击）
		{"8.8.8.8", false},
	}
	for _, c := range cases {
		if got := s.isTrusted(c.ip); got != c.want {
			t.Errorf("isTrusted(%q) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestIgnoredSitesProvider(t *testing.T) {
	s := &Service{}
	s.SetIgnoredSites(func() map[string]bool {
		return map[string]bool{"off.example": true}
	})
	if !s.isIgnoredSite("off.example") {
		t.Fatal("停用应用站点应被过滤")
	}
	if s.isIgnoredSite("live.example") {
		t.Fatal("正常站点不应被过滤")
	}
}
