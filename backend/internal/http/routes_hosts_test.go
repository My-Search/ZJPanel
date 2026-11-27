package httpapi

import "testing"

func TestHostListStatus(t *testing.T) {
	cases := []struct {
		name           string
		local, online  bool
		stored, expect string
	}{
		{"本机始终 local", true, false, "online", "local"},
		{"连接存在即 online", false, true, "pending", "online"},
		{"断线后不保留 online", false, false, "online", "offline"},
		{"新建未安装", false, false, "", "none"},
		{"待回连保持 pending", false, false, "pending", "pending"},
		{"已卸载保持原值", false, false, "uninstalled", "uninstalled"},
	}
	for _, c := range cases {
		if got := hostListStatus(c.local, c.online, c.stored); got != c.expect {
			t.Errorf("%s: hostListStatus(%v,%v,%q)=%q, want %q", c.name, c.local, c.online, c.stored, got, c.expect)
		}
	}
}
