// Package waf 日志层 WAF 规则引擎：从 nginx access log 的 URI/参数/UA/Referer
// 做内容级攻击检测，命中后按阶梯封禁下发 nginx geo 规则。
//
// 参考 SamWaf 的检测思路（libinjection + 多轮解码 + 规则先放行/拦截/观察仲裁），
// 但适配 ZJPanel「面板只管 nginx、不做反向代理」的架构取舍：
// 请求体与 Cookie 不在 access log 里，本层只覆盖 URI/查询串/UA/Referer，
// 属于「看得见就判、判得准再封」的保守实现，详见 docs/design-waf.md。
package waf

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 规则类别。内置规则每个类别一条，可单独开关/切换动作。
const (
	CatSQLi      = "sqli"      // SQL 注入
	CatXSS       = "xss"       // 跨站脚本
	CatRCE       = "rce"       // 命令/代码执行
	CatLFI       = "lfi"       // 路径穿越/任意文件读取
	CatWebshell  = "webshell"  // webshell 访问特征
	CatScanner   = "scanner"   // 扫描器 UA
	CatSensitive = "sensitive" // 敏感文件/后台路径探测
	CatCustom    = "custom"    // 自定义正则规则
)

// 处置动作。
const (
	ActionBlock   = "block"   // 拦截：命中后下发 nginx 封禁（受全局 blockMode 约束）
	ActionObserve = "observe" // 观察：只记录事件，不封禁（上线灰度用）
)

// 严重级别。
const (
	SevHigh   = "high"
	SevMedium = "medium"
	SevLow    = "low"
)

// Settings 全局设置（持久化到 data/waf/config.json）。
type Settings struct {
	Enabled bool `json:"enabled"`
	// BlockMode 全局处置模式：block=按规则动作拦截；observe=所有规则降级为只记录。
	// 默认 observe：与安全中心「上线前先跑一天观察」的一致哲学，避免一开启就误封真实用户。
	BlockMode string `json:"blockMode"`
	// AutoBan block 命中时是否真正写封禁；关闭后命中只记录，用于「先看日志再决定封谁」。
	AutoBan bool `json:"autoBan"`
	// BanLadder 阶梯封禁时长（分钟）：同一 IP 第 1/2/3…次命中依次取阶梯，超出则取最后一级。
	BanLadder []int `json:"banLadder"`
	// BanResetHours 累犯计数清零窗口（小时）：超过该时长没有新命中，重新从第一级算起。
	BanResetHours int `json:"banResetHours"`
	// Exempt 豁免地址（IP/CIDR）：本机回环默认豁免，避免面板自检/运维被判定成攻击。
	Exempt    []string  `json:"exempt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Rule 一条检测规则。内置规则带固定 ID，不可删除，只能开关/改动作；
// 自定义规则由用户新增，支持正则 + 作用范围。
type Rule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Action   string `json:"action"`
	// Scope 仅自定义规则使用：url（路径）/query（查询串）/ua/referer/any
	Scope   []string `json:"scope,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Enabled bool     `json:"enabled"`
	Builtin bool     `json:"builtin"`
	Desc    string   `json:"desc,omitempty"`
}

// Hit 一次规则命中（同一 IP + 规则在抑制窗口内合并计数）。
type Hit struct {
	Time     time.Time `json:"time"`
	RuleID   string    `json:"ruleId"`
	RuleName string    `json:"ruleName"`
	Category string    `json:"category"`
	Severity string    `json:"severity"`
	Action   string    `json:"action"` // 实际生效动作（已被 blockMode 归一）
	Site     string    `json:"site,omitempty"`
	IP       string    `json:"ip"`
	Country  string    `json:"country,omitempty"`
	Method   string    `json:"method,omitempty"`
	Path     string    `json:"path"`
	Snippet  string    `json:"snippet,omitempty"`
	UA       string    `json:"ua,omitempty"`
	Status   int       `json:"status,omitempty"`
	Count    int       `json:"count"`
}

// Request 待检测的一次请求（由 logmap.Entry 转换而来）。
type Request struct {
	IP      string
	Country string
	Site    string
	Method  string
	Path    string // 含查询串，与 nginx "request" 的第二段一致
	UA      string
	Referer string
	Status  int
	Time    time.Time
}

// BanRequest 一次封禁请求（由 WAF 服务汇总后批量交给安全中心）。
type BanRequest struct {
	IP      string
	Minutes int
	Reason  string
}

// BanFunc 封禁下发回调（boot 注入，实际落到 security.Service.BanAuto）。
type BanFunc func([]BanRequest) (int, error)

// View 前端读取的完整配置视图。
type View struct {
	Settings Settings `json:"settings"`
	Rules    []Rule   `json:"rules"`
	// 运行时信息
	ExemptOK bool `json:"exemptOk"`
}

func defaultSettings() Settings {
	return Settings{
		Enabled:       true,
		BlockMode:     ActionObserve,
		AutoBan:       true,
		BanLadder:     []int{10, 60, 1440},
		BanResetHours: 24,
		Exempt:        []string{"127.0.0.1", "::1"},
	}
}

func normalizeSettings(s *Settings) {
	if s.BlockMode != ActionBlock && s.BlockMode != ActionObserve {
		s.BlockMode = ActionObserve
	}
	if s.BanResetHours <= 0 {
		s.BanResetHours = 24
	}
	if len(s.BanLadder) == 0 {
		s.BanLadder = []int{10, 60, 1440}
	}
	var ladder []int
	for _, m := range s.BanLadder {
		if m < 1 {
			continue // 0/负数会被当成「永久封禁」，自动封禁一律不允许永久，避免手滑把整片网段锁死
		}
		if m > 10080 { // 上限 7 天
			m = 10080
		}
		ladder = append(ladder, m)
	}
	if len(ladder) == 0 {
		ladder = []int{10, 60, 1440}
	}
	sort.Ints(ladder)
	s.BanLadder = ladder

	seen := map[string]bool{}
	var wl []string
	for _, v := range s.Exempt {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		wl = append(wl, v)
	}
	s.Exempt = wl
}

func (s Settings) validate() error {
	for _, v := range s.Exempt {
		if _, _, err := net.ParseCIDR(v); err != nil {
			if net.ParseIP(v) == nil {
				return fmt.Errorf("豁免项 %q 不是合法 IP/CIDR", v)
			}
		}
	}
	return nil
}

// banMinutes 第 n 次命中对应的封禁时长（分钟）。
func (s Settings) banMinutes(n int) int {
	if n < 1 {
		n = 1
	}
	i := n - 1
	if i >= len(s.BanLadder) {
		i = len(s.BanLadder) - 1
	}
	return s.BanLadder[i]
}

// configPath 配置落盘位置。
func configPath(dataDir string) string {
	return filepath.Join(dataDir, "waf", "config.json")
}

type persisted struct {
	Settings Settings `json:"settings"`
	Rules    []Rule   `json:"rules"`
}

// loadConfig 读取配置；不存在时返回默认设置 + 全部内置规则。
// 内置规则以代码为准，落盘文件只覆盖「开关/动作」，代码新增的内置规则会自动补齐。
func loadConfig(dataDir string) (Settings, []Rule, error) {
	st := defaultSettings()
	rules := builtinRules()
	b, err := os.ReadFile(configPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			normalizeSettings(&st)
			return st, rules, nil
		}
		return st, rules, err
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return st, rules, fmt.Errorf("waf 配置解析失败: %w", err)
	}
	if p.Settings.UpdatedAt.IsZero() && p.Settings.BanResetHours == 0 {
		// 几乎空的文件：按默认处理，避免零值设置把 WAF 关死
		p.Settings = st
	}
	normalizeSettings(&p.Settings)

	byID := map[string]*Rule{}
	for i := range rules {
		byID[rules[i].ID] = &rules[i]
	}
	seen := map[string]bool{}
	var custom []Rule
	for _, r := range p.Rules {
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			continue
		}
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if r.Builtin || byID[r.ID] != nil {
			if b, ok := byID[r.ID]; ok {
				b.Enabled = r.Enabled
				if r.Action == ActionBlock || r.Action == ActionObserve {
					b.Action = r.Action
				}
			}
			continue
		}
		r.Builtin = false
		r.Category = CatCustom
		if r.Action != ActionBlock && r.Action != ActionObserve {
			r.Action = ActionObserve
		}
		custom = append(custom, r)
	}
	rules = append(rules, custom...)
	return p.Settings, rules, nil
}

func saveConfig(dataDir string, st Settings, rules []Rule) error {
	dir := filepath.Dir(configPath(dataDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(persisted{Settings: st, Rules: rules}, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath(dataDir))
}

// decodeLayers 多轮 URL 解码（最多 3 层）：%2527 → %27 → ' 这类双重编码是绕过正则的常见手法。
func decodeLayers(s string, times int) string {
	out := s
	for i := 0; i < times; i++ {
		v, err := url.QueryUnescape(out)
		if err != nil || v == out {
			return out
		}
		out = v
	}
	return out
}

// decodeEntities HTML 实体与常见 JS 转义还原（&#60; / &lt; / \u003c），供 XSS 检测第二轮比对。
func decodeEntities(s string) string {
	if !strings.ContainsAny(s, "&\\") {
		return s
	}
	r := strings.NewReplacer(
		"&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'", "&#x27;", "'",
		"&amp;", "&", "\\u003c", "<", "\\u003e", ">", "\\x3c", "<", "\\x3e", ">",
	)
	return r.Replace(s)
}

// splitPath 拆出路径与查询串（Path 来自 nginx request 行，含查询串）。
func splitPath(p string) (string, string) {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// clip 截断过长的证据片段，并保证截断处仍是合法 UTF-8。
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}
