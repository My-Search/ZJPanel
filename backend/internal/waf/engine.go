package waf

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// compiledRule 运行态规则：内置规则挂函数，自定义规则挂预编译正则。
type compiledRule struct {
	rule  Rule
	re    *regexp.Regexp
	check func(*scan) (snippet string, ok bool)
}

type engineSnap struct {
	rules []*compiledRule
	mode  string // 全局处置模式
}

// Engine 无锁读的规则引擎：配置变更时整体替换快照，热路径只做一次原子读。
type Engine struct {
	snap atomic.Pointer[engineSnap]
	mu   sync.Mutex
}

func NewEngine() *Engine {
	e := &Engine{}
	e.snap.Store(&engineSnap{})
	return e
}

var builtinChecks = map[string]func(*scan) (string, bool){
	"sqli":      checkSQLi,
	"xss":       checkXSS,
	"rce":       checkRCE,
	"lfi":       checkLFI,
	"webshell":  checkWebshell,
	"scanner":   checkScanner,
	"sensitive": checkSensitive,
}

// Compile 校验自定义规则正则是否可编译（保存前先校验，避免坏规则进配置）。
func Compile(pattern string) (*regexp.Regexp, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return nil, fmt.Errorf("正则不能为空")
	}
	if len(p) > 2000 {
		return nil, fmt.Errorf("正则过长（上限 2000 字符）")
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return nil, fmt.Errorf("正则无法编译: %v", err)
	}
	return re, nil
}

// Rebuild 用最新规则集与处置模式重建快照；非法自定义规则跳过并返回告警。
func (e *Engine) Rebuild(rules []Rule, mode string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*compiledRule
	var warns []string
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		c := &compiledRule{rule: r}
		if r.Builtin {
			fn, ok := builtinChecks[r.ID]
			if !ok {
				continue // 代码里已删除的内置规则 ID，直接忽略
			}
			c.check = fn
		} else {
			re, err := Compile(r.Pattern)
			if err != nil {
				warns = append(warns, fmt.Sprintf("规则 %s 已跳过：%v", r.Name, err))
				continue
			}
			c.re = re
		}
		out = append(out, c)
	}
	if mode != ActionBlock {
		mode = ActionObserve
	}
	e.snap.Store(&engineSnap{rules: out, mode: mode})
	return warns
}

// Rules 当前生效规则条数（快照，供状态展示）。
func (e *Engine) Rules() int {
	s := e.snap.Load()
	if s == nil {
		return 0
	}
	return len(s.rules)
}

// Evaluate 对一次请求跑全部规则，返回命中的 Hit 列表（不做封禁决策，由服务层处理）。
func (e *Engine) Evaluate(req Request) []Hit {
	s := e.snap.Load()
	if s == nil || len(s.rules) == 0 {
		return nil
	}
	sc := newScan(req)
	var hits []Hit
	for _, c := range s.rules {
		snippet, ok := e.match(c, sc)
		if !ok {
			continue
		}
		action := c.rule.Action
		if s.mode == ActionObserve {
			action = ActionObserve
		}
		h := Hit{
			Time: req.Time, RuleID: c.rule.ID, RuleName: c.rule.Name, Category: c.rule.Category,
			Severity: c.rule.Severity, Action: action, Site: req.Site, IP: req.IP,
			Country: req.Country, Method: req.Method, Path: clip(req.Path, 512), Snippet: snippet,
			UA: clip(req.UA, 200), Status: req.Status, Count: 1,
		}
		if h.Time.IsZero() {
			h.Time = nowFunc()
		}
		hits = append(hits, h)
	}
	return hits
}

func (e *Engine) match(c *compiledRule, sc *scan) (string, bool) {
	if c.check != nil {
		return c.check(sc)
	}
	for _, field := range c.rule.Scope {
		switch strings.ToLower(field) {
		case "url", "path":
			if c.re.MatchString(sc.pathD) {
				return clip(sc.pathD, 160), true
			}
		case "query":
			if sc.queryD != "" && c.re.MatchString(sc.queryD) {
				return clip(sc.queryD, 160), true
			}
		case "ua":
			if c.re.MatchString(sc.ua) {
				return clip(sc.ua, 160), true
			}
		case "referer":
			if c.re.MatchString(sc.referer) {
				return clip(sc.referer, 160), true
			}
		case "any", "":
			full := sc.pathD
			if sc.queryD != "" {
				full += "?" + sc.queryD
			}
			if c.re.MatchString(full) || (sc.ua != "" && c.re.MatchString(sc.ua)) ||
				(sc.referer != "" && c.re.MatchString(sc.referer)) {
				return clip(full, 160), true
			}
		}
	}
	return "", false
}
