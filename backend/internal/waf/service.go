package waf

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zjpanel/backend/internal/logmap"
	"zjpanel/backend/internal/pkg"
)

const (
	hitRing      = 1000             // 命中事件环形容量（聚合后）
	dedupeWindow = 30 * time.Second // 同 IP + 同规则在窗口内合并为一条，只累加次数
	notifyGap    = 10 * time.Minute // 同类严重告警节流
)

var nowFunc = time.Now

// offense 同一 IP 的累犯计数：命中次数决定阶梯封禁时长，超窗清零。
type offense struct {
	count int
	last  time.Time
}

// Service 日志层 WAF：订阅 logmap 实时条目 → 规则判定 → 命中聚合/落盘 → 阶梯封禁。
type Service struct {
	cfg *pkg.Config
	lm  *logmap.Service

	mu       sync.Mutex
	settings Settings
	rules    []Rule
	engine   *Engine
	hits     []*Hit
	agg      map[string]*Hit
	offenses map[string]*offense
	banned   map[string]time.Time // IP -> 当前封禁到期时刻（未到期不重复升级阶梯）
	pend     map[string]BanRequest

	exempt atomic.Pointer[exemptSet]

	banFn    BanFunc
	notifyMu sync.Mutex
	notifyFn func(title, body string)
	lastNote map[string]time.Time

	liveFile *os.File
	liveSize int64

	stop chan struct{}
	once sync.Once
}

// exemptSet 预解析的豁免网段：热路径按 IP 判定，不做字符串比较。
type exemptSet struct {
	exact map[string]bool
	nets  []*net.IPNet
}

func New(cfg *pkg.Config, lm *logmap.Service) *Service {
	return &Service{
		cfg: cfg, lm: lm, engine: NewEngine(),
		agg: map[string]*Hit{}, offenses: map[string]*offense{}, banned: map[string]time.Time{}, pend: map[string]BanRequest{},
		lastNote: map[string]time.Time{},
		stop:     make(chan struct{}),
	}
}

// SetBanFunc 注入封禁下发回调（boot 接到 security.Service.BanAuto，批量合并后一次 reload）。
func (s *Service) SetBanFunc(fn BanFunc) {
	s.mu.Lock()
	s.banFn = fn
	s.mu.Unlock()
}

// SetNotify 注入统一通知回调。
func (s *Service) SetNotify(fn func(title, body string)) {
	s.notifyMu.Lock()
	s.notifyFn = fn
	s.notifyMu.Unlock()
}

// Start 加载配置 → 订阅日志 → 起判定与处置循环。
func (s *Service) Start() {
	st, rules, err := loadConfig(s.cfg.DataDir)
	if err != nil {
		slog.Warn("waf 配置读取失败，使用默认", "err", err)
	}
	s.mu.Lock()
	s.settings, s.rules = st, rules
	s.mu.Unlock()
	s.setExempt(st.Exempt)
	if warns := s.engine.Rebuild(rules, st.BlockMode); len(warns) > 0 {
		for _, w := range warns {
			slog.Warn("waf 规则加载", "warn", w)
		}
	}

	ch, unreg := s.lm.SubscribeFast(true)
	go func() {
		defer unreg()
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				s.handle(e)
			case <-s.stop:
				return
			}
		}
	}()
	go s.loop()
}

func (s *Service) Close() {
	s.once.Do(func() {
		close(s.stop)
		s.mu.Lock()
		if s.liveFile != nil {
			_ = s.liveFile.Close()
			s.liveFile = nil
		}
		s.mu.Unlock()
	})
}

func (s *Service) loop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.flushBans()
			s.prune(now)
		}
	}
}

func (s *Service) setExempt(list []string) {
	set := &exemptSet{exact: map[string]bool{}}
	for _, v := range list {
		if strings.Contains(v, "/") {
			if _, n, err := net.ParseCIDR(v); err == nil {
				set.nets = append(set.nets, n)
			}
			continue
		}
		set.exact[v] = true
	}
	s.exempt.Store(set)
}

func (s *Service) isExempt(ip string) bool {
	set := s.exempt.Load()
	if set == nil {
		return false
	}
	if set.exact[ip] {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range set.nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// handle 一条实时访问日志的检测入口。
func (s *Service) handle(e logmap.Entry) {
	s.mu.Lock()
	enabled := s.settings.Enabled
	s.mu.Unlock()
	if !enabled || s.isExempt(e.IP) {
		return
	}
	req := Request{
		IP: e.IP, Country: e.Country, Site: e.Site, Method: e.Method,
		Path: e.Path, UA: e.UA, Referer: e.Referer, Status: e.Status, Time: e.Time,
	}
	hits := s.engine.Evaluate(req)
	if len(hits) == 0 {
		return
	}
	s.record(hits)
}

// record 命中聚合 → 落盘 → 累犯计数 → 排队封禁。
func (s *Service) record(hits []Hit) {
	var fresh []*Hit
	var bans []BanRequest
	s.mu.Lock()
	for i := range hits {
		h := hits[i]
		key := h.IP + "|" + h.RuleID
		if old, ok := s.agg[key]; ok && h.Time.Sub(old.Time) <= dedupeWindow && h.Time.After(old.Time) {
			old.Count++
			old.Time = h.Time
			continue
		}
		hp := &h
		s.hits = append(s.hits, hp)
		if len(s.hits) > hitRing {
			s.hits = s.hits[len(s.hits)-hitRing:]
		}
		s.agg[key] = hp
		fresh = append(fresh, hp)

		if hp.Action != ActionBlock || !s.settings.AutoBan {
			continue
		}
		// 已在封禁期内不再重复升级：阶梯只惩罚"解封后再次作恶"的累犯，
		// 否则一次持续攻击会在几十秒内把自己顶到 24 小时档。
		if until, ok := s.banned[hp.IP]; ok && h.Time.Before(until) {
			continue
		}
		off := s.offenses[hp.IP]
		if off == nil || h.Time.Sub(off.last) > time.Duration(s.settings.BanResetHours)*time.Hour {
			off = &offense{}
			s.offenses[hp.IP] = off
		}
		off.count++
		off.last = h.Time
		minutes := s.settings.banMinutes(off.count)
		s.banned[hp.IP] = h.Time.Add(time.Duration(minutes) * time.Minute)
		bans = append(bans, BanRequest{IP: hp.IP, Minutes: minutes, Reason: hp.RuleName})
	}
	s.mu.Unlock()

	for _, h := range fresh {
		s.persist(h)
		s.maybeNotify(h)
	}
	if len(bans) > 0 {
		s.mergeBans(bans)
	}
}

// mergeBans 同一秒内同 IP 多次命中只保留最长的一次封禁。
func (s *Service) mergeBans(bans []BanRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range bans {
		if old, ok := s.pend[b.IP]; ok && old.Minutes >= b.Minutes {
			continue
		}
		s.pend[b.IP] = b
	}
}

// flushBans 每秒批量下发一次封禁：攻击期间逐条下发会把 nginx reload 变成拒绝服务。
func (s *Service) flushBans() {
	s.mu.Lock()
	if len(s.pend) == 0 || s.banFn == nil {
		s.mu.Unlock()
		return
	}
	bans := make([]BanRequest, 0, len(s.pend))
	for _, b := range s.pend {
		bans = append(bans, b)
	}
	s.pend = map[string]BanRequest{}
	banFn := s.banFn
	s.mu.Unlock()

	sort.Slice(bans, func(i, j int) bool { return bans[i].IP < bans[j].IP })
	if n, err := banFn(bans); err != nil {
		slog.Warn("waf 自动封禁下发失败", "err", err, "count", n)
	}
}

// prune 回收累犯表与聚合索引，避免被打时随请求量无限增长。
func (s *Service) prune(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reset := time.Duration(s.settings.BanResetHours) * time.Hour
	for ip, off := range s.offenses {
		if now.Sub(off.last) > reset {
			delete(s.offenses, ip)
		}
	}
	for ip, until := range s.banned {
		if now.After(until) {
			delete(s.banned, ip)
		}
	}
	// 告警节流表同样要回收：被打时会有大量不同 IP+规则组合，不清理会随运行时长增长
	if len(s.lastNote) > 2000 {
		s.notifyMu.Lock()
		for k, t := range s.lastNote {
			if now.Sub(t) > notifyGap*2 {
				delete(s.lastNote, k)
			}
		}
		s.notifyMu.Unlock()
	}
	if len(s.agg) > 4000 {
		for k, h := range s.agg {
			if now.Sub(h.Time) > dedupeWindow*2 {
				delete(s.agg, k)
			}
		}
	}
}

func (s *Service) maybeNotify(h *Hit) {
	if h.Severity != SevHigh {
		return
	}
	key := h.RuleID + "|" + h.IP
	s.notifyMu.Lock()
	fn := s.notifyFn
	if fn == nil || nowFunc().Sub(s.lastNote[key]) < notifyGap {
		s.notifyMu.Unlock()
		return
	}
	s.lastNote[key] = nowFunc()
	s.notifyMu.Unlock()
	detail := fmt.Sprintf("%s 命中 %s（%s）%s", h.IP, h.RuleName, h.Site, clip(h.Snippet, 80))
	go fn("WAF 告警："+h.RuleName, detail)
}

// persist 命中证据落盘（data/waf/hits.jsonl，8MB 滚动一份），用于事后回溯攻击载荷。
func (s *Service) persist(h *Hit) {
	dir := filepath.Join(s.cfg.DataDir, "waf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveFile == nil {
		path := filepath.Join(dir, "hits.jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		if fi, err := f.Stat(); err == nil {
			s.liveSize = fi.Size()
		}
		s.liveFile = f
	}
	if s.liveSize > 8<<20 {
		_ = s.liveFile.Close()
		s.liveFile = nil
		path := filepath.Join(dir, "hits.jsonl")
		_ = os.Rename(path, filepath.Join(dir, "hits.1.jsonl"))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		s.liveFile = f
		s.liveSize = 0
	}
	if s.liveFile != nil {
		if _, err := s.liveFile.Write(append(b, '\n')); err == nil {
			s.liveSize += int64(len(b) + 1)
		}
	}
}

// HitFilter 命中日志查询条件。
type HitFilter struct {
	IP       string
	Category string
	RuleID   string
	Site     string
	Limit    int
}

// Hits 查询最近命中（按时间倒序）。
func (s *Service) Hits(f HitFilter) []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	out := make([]Hit, 0, f.Limit)
	for i := len(s.hits) - 1; i >= 0 && len(out) < f.Limit; i-- {
		h := s.hits[i]
		if f.IP != "" && h.IP != f.IP {
			continue
		}
		if f.Category != "" && h.Category != f.Category {
			continue
		}
		if f.RuleID != "" && h.RuleID != f.RuleID {
			continue
		}
		if f.Site != "" && h.Site != f.Site {
			continue
		}
		out = append(out, *h)
	}
	return out
}

// KV 排行项。
type KV struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// Stats WAF 概况（前端头部卡片）。
type Stats struct {
	Enabled   bool           `json:"enabled"`
	BlockMode string         `json:"blockMode"`
	AutoBan   bool           `json:"autoBan"`
	Rules     int            `json:"rules"`
	Hits      int            `json:"hits"`
	ByCat     map[string]int `json:"byCat"`
	ByAction  map[string]int `json:"byAction"`
	TopIPs    []KV           `json:"topIps"`
	TopRules  []KV           `json:"topRules"`
	LastHour  int            `json:"lastHour"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// Stat 统计最近一小时的命中分布。
func (s *Service) Stat() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{
		Enabled: s.settings.Enabled, BlockMode: s.settings.BlockMode, AutoBan: s.settings.AutoBan,
		Rules: len(s.rules), Hits: len(s.hits), ByCat: map[string]int{}, ByAction: map[string]int{},
		UpdatedAt: s.settings.UpdatedAt,
	}
	cut := nowFunc().Add(-time.Hour)
	ips := map[string]int64{}
	ruleNames := map[string]int64{}
	for _, h := range s.hits {
		if h.Time.Before(cut) {
			continue
		}
		st.LastHour += h.Count
		st.ByCat[h.Category] += h.Count
		if h.Action == ActionBlock {
			st.ByAction["block"] += h.Count
		} else {
			st.ByAction["observe"] += h.Count
		}
		ips[h.IP] += int64(h.Count)
		ruleNames[h.RuleName] += int64(h.Count)
	}
	st.TopIPs = topK(ips, 5)
	st.TopRules = topK(ruleNames, 5)
	return st
}

func topK(m map[string]int64, n int) []KV {
	type pair struct {
		k string
		v int64
	}
	list := make([]pair, 0, len(m))
	for k, v := range m {
		list = append(list, pair{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	if len(list) > n {
		list = list[:n]
	}
	out := make([]KV, 0, len(list))
	for _, p := range list {
		out = append(out, KV{Name: p.k, Count: p.v})
	}
	return out
}

// View 当前配置。
func (s *Service) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := append([]Rule(nil), s.rules...)
	return View{Settings: s.settings, Rules: rules, ExemptOK: true}
}

// SetSettings 保存设置并热生效。
func (s *Service) SetSettings(st Settings) error {
	normalizeSettings(&st)
	if err := st.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	st.UpdatedAt = nowFunc()
	s.settings = st
	rules := append([]Rule(nil), s.rules...)
	s.mu.Unlock()

	s.setExempt(st.Exempt)
	if warns := s.engine.Rebuild(rules, st.BlockMode); len(warns) > 0 {
		slog.Warn("waf 规则热加载告警", "warns", warns)
	}
	return saveConfig(s.cfg.DataDir, st, rules)
}

// SetRule 开关内置/自定义规则或切换动作。
func (s *Service) SetRule(id string, enabled bool, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.rules {
		if s.rules[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("规则不存在")
	}
	if action != "" && action != ActionBlock && action != ActionObserve {
		return fmt.Errorf("动作只能是 block/observe")
	}
	s.rules[idx].Enabled = enabled
	if action != "" {
		s.rules[idx].Action = action
	}
	s.settings.UpdatedAt = nowFunc()
	st := s.settings
	rules := append([]Rule(nil), s.rules...)
	warns := s.engine.Rebuild(rules, st.BlockMode)
	if len(warns) > 0 {
		slog.Warn("waf 规则热加载告警", "warns", warns)
	}
	return saveConfig(s.cfg.DataDir, st, rules)
}

// AddRule 新增自定义规则。
func (s *Service) AddRule(r Rule) (Rule, error) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return r, fmt.Errorf("规则名不能为空")
	}
	if len([]rune(r.Name)) > 40 {
		return r, fmt.Errorf("规则名过长（上限 40 字）")
	}
	r.Pattern = strings.TrimSpace(r.Pattern)
	if _, err := Compile(r.Pattern); err != nil {
		return r, err
	}
	scope := normalizeScope(r.Scope)
	if len(scope) == 0 {
		return r, fmt.Errorf("作用范围至少选一个（url/query/ua/referer）")
	}
	r.Scope = scope
	if r.Action != ActionBlock && r.Action != ActionObserve {
		r.Action = ActionObserve
	}
	switch r.Severity {
	case SevHigh, SevMedium, SevLow:
	default:
		r.Severity = SevMedium
	}
	r.ID = newID()
	r.Category = CatCustom
	r.Builtin = false
	r.Enabled = true

	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	s.settings.UpdatedAt = nowFunc()
	st := s.settings
	rules := append([]Rule(nil), s.rules...)
	warns := s.engine.Rebuild(rules, st.BlockMode)
	if len(warns) > 0 {
		slog.Warn("waf 规则热加载告警", "warns", warns)
	}
	if err := saveConfig(s.cfg.DataDir, st, rules); err != nil {
		return r, err
	}
	return r, nil
}

// DeleteRule 删除自定义规则（内置规则只能关，不能删）。
func (s *Service) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.rules {
		if s.rules[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("规则不存在")
	}
	if s.rules[idx].Builtin {
		return fmt.Errorf("内置规则不能删除，只能关闭")
	}
	s.rules = append(s.rules[:idx], s.rules[idx+1:]...)
	s.settings.UpdatedAt = nowFunc()
	st := s.settings
	rules := append([]Rule(nil), s.rules...)
	s.engine.Rebuild(rules, st.BlockMode)
	return saveConfig(s.cfg.DataDir, st, rules)
}

// Test 对一条构造请求跑规则（前端「规则测试」与排障用，不做封禁）。
func (s *Service) Test(req Request) []Hit {
	if req.Time.IsZero() {
		req.Time = nowFunc()
	}
	if req.Site == "" {
		req.Site = "test"
	}
	if req.IP == "" {
		req.IP = "127.0.0.1"
	}
	return s.engine.Evaluate(req)
}

func normalizeScope(in []string) []string {
	valid := map[string]bool{"url": true, "query": true, "ua": true, "referer": true, "any": true}
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if !valid[v] || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "custom-" + hex.EncodeToString(b)
}
