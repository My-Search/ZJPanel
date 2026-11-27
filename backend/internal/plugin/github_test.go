package plugin

import "testing"

func TestNormalizeGitHubURL(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"My-Search/ZJPanel", "https://github.com/My-Search/ZJPanel/archive/HEAD.zip", true},
		{"https://github.com/My-Search/ZJPanel", "https://github.com/My-Search/ZJPanel/archive/HEAD.zip", true},
		{" https://www.github.com/My-Search/ZJPanel.git ", "https://github.com/My-Search/ZJPanel/archive/HEAD.zip", true},
		{"https://github.com/My-Search/ZJPanel/archive/refs/heads/main.zip", "https://github.com/My-Search/ZJPanel/archive/refs/heads/main.zip", true},
		{"https://github.com/My-Search/ZJPanel/releases/download/v1/plugin.zip", "https://github.com/My-Search/ZJPanel/releases/download/v1/plugin.zip", true},
		{"https://codeload.github.com/My-Search/ZJPanel/zip/refs/heads/main", "https://codeload.github.com/My-Search/ZJPanel/zip/refs/heads/main", true},
		{"https://github.com/My-Search/ZJPanel/tree/main", "", false},
		{"http://github.com/My-Search/ZJPanel", "", false},
		{"https://github.com.evil.com/a/b", "", false},
		{"https://gitlab.com/a/b", "", false},
		{"https://169.254.169.254/latest/meta-data", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeGitHubURL(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("NormalizeGitHubURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("NormalizeGitHubURL(%q) 应拒绝，实际得到 %q", c.in, got)
		}
	}
}
