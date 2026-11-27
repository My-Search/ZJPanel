package agent

import "testing"

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		panel, agent string
		want         bool
	}{
		{"1.0.40", "1.0.39", true},
		{"1.0.39", "1.0.39", false},
		{"1.0.39", "1.0.40", false},
		{"1.0.39", "1.0.39-dev", true},  // 旧版是 dev 构建，升到正式版
		{"1.0.39-rc1", "1.0.36", false}, // 面板自己是预发布版：不自动升级
		{"v1.0.39", "1.0.39", false},    // 前缀 v 不影响比较
		{"1.2", "1.1.9", true},          // 段数不同按缺失补 0
		{"1.0.39", "", false},
		{"0.1.0-dev", "1.0.0", false},
		{"1.0.39", "1.0.38+build7", true},
	}
	for _, c := range cases {
		if got := versionNewer(c.panel, c.agent); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.panel, c.agent, got, c.want)
		}
	}
}

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{
		"amd64": "amd64", "x86_64": "amd64",
		"arm64": "arm64", "aarch64": "arm64",
		"386": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}
