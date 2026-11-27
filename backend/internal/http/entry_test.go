package httpapi

import (
	"bytes"
	"testing"
)

func TestNormalizeEntryPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"   ", "", true},
		{"admin-x7k2", "/admin-x7k2", true},
		{"/admin-x7k2/", "/admin-x7k2", true},
		{"/a", "", false},          // 过短
		{"/api", "", false},        // 保留前缀
		{"/api/foo", "", false},
		{"/mcp", "", false},
		{"/login", "", false},
		{"/bad path!", "", false}, // 非法字符
		{"/" + repeat("a", 65), "", false},
	}
	for _, c := range cases {
		got, err := NormalizeEntryPath(c.in)
		if c.ok && err != nil {
			t.Errorf("NormalizeEntryPath(%q) unexpected err: %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("NormalizeEntryPath(%q) expected error, got %q", c.in, got)
		}
		if c.ok && got != c.want {
			t.Errorf("NormalizeEntryPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestUnderEntry(t *testing.T) {
	const e = "/admin-x7k2"
	cases := []struct {
		path string
		want bool
	}{
		{"/admin-x7k2", true},
		{"/admin-x7k2/", true},
		{"/admin-x7k2/settings", true},
		{"/admin-x7k2/login", true},
		{"/admin-x7k2xyz", false}, // 前缀部分匹配不得放行
		{"/", false},
		{"/login", false},
		{"/api/settings", false},
	}
	for _, c := range cases {
		if got := underEntry(c.path, e); got != c.want {
			t.Errorf("underEntry(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestInjectEntryBase(t *testing.T) {
	html := []byte("<html><head><title>x</title></head><body></body></html>")
	out := injectEntryBase(html, "/admin-x7k2")
	if !bytes.Contains(out, []byte(`window.__ZJPANEL_BASE__="/admin-x7k2"`)) {
		t.Fatalf("未正确注入 base: %s", out)
	}
	// 注入位置必须在 <head> 之后、业务脚本之前（这里仅校验在 title 之前）。
	if bytes.Index(out, []byte("__ZJPANEL_BASE__")) > bytes.Index(out, []byte("<title>")) {
		t.Fatal("注入脚本应位于 head 内靠前位置")
	}
}
