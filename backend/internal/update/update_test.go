package update

import "testing"

// 面板 tag 为 4 段版本（x.y.z.w），splitSemver 必须保留全部 4 位参与比较。
// 回归：0.1.8.0 与 0.1.8.1 曾因 SplitN 3 段被截断判等，检查更新永远"已是最新"。
func TestSemverLess(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.1.8.0", "0.1.8.1", true},   // 本次报告的 bug：第 4 位补丁号
		{"0.1.7.21", "0.1.8.0", true},  // 7.x → 8.0
		{"0.1.8.0", "0.1.8.0", false},  // 相同
		{"0.1.8.1", "0.1.8.1", false},  // 相同（更新后不再误报）
		{"0.1.8.1", "0.1.8.0", false},  // 本地比线上新
		{"0.1.8.0", "0.2.0.0", true},   // minor 变化
		{"0.1.8.0", "1.0.0.0", true},   // major 变化
		{"0.1.8.0", "0.1.9", true},     // 3 段 latest，缺段按 0：0.1.9.0 > 0.1.8.0
		{"0.1.10.0", "0.1.9.1", false}, // 逐段比较而非字典序
		{"dev", "0.1.8.0", true},       // dev 永远提示
	}
	for _, c := range cases {
		if got := semverLess(c.cur, c.latest); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestSplitSemver(t *testing.T) {
	if got := splitSemver("0.1.8.1"); got != [4]int{0, 1, 8, 1} {
		t.Errorf("splitSemver(0.1.8.1) = %v, want [0 1 8 1]", got)
	}
	if got := splitSemver("v1.8"); got != [4]int{1, 8, 0, 0} {
		t.Errorf("splitSemver(v1.8) = %v, want [1 8 0 0]", got)
	}
	if got := splitSemver("0.1.8.0-rc1"); got != [4]int{0, 1, 8, 0} {
		t.Errorf("splitSemver(0.1.8.0-rc1) = %v, want [0 1 8 0]", got) // 非数字后缀截断
	}
}
