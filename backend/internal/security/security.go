package security

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/logmap"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

const (
	winSec     = 60  // 判定窗口（秒）
	bucketKeep = 300 // 秒级桶保留时长：覆盖 5 分钟 dry-run/回看
	maxEvents  = 500 // 内存事件环形容量
)

// KV 通用排行项。
type KV struct {
	Name  string  `json:"name"`
	Count int64   `json:"count"`
	Pct   float64 `json:"pct"`
}

// Event 一次攻击判定事件。
type Event struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"` // info | warn | critical
	Site     string    `json:"site,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Country  string    `json:"country,omitempty"`
	Path     string    `json:"path,omitempty"`
	UA       string    `json:"ua,omitempty"`
	Detail   string    `json:"detail"`
	Count    int       `json:"count"`
}

// L4Stat 内核层观测：连接数/半连接/带宽。日志层看不见的流量型攻击靠这一层兜底。
type L4Stat struct {
	Conn      int     `json:"conn"`
	SynRecv   int     `json:"synRecv"`
	TimeWait  int     `json:"timeWait"`
	RxBps     float64 `json:"rxBps"`
	TxBps     float64 `json:"txBps"`
	Overflows uint64  `json:"overflows"`
	Drops     uint64  `json:"drops"`
	Supported bool    `json:"supported"`
}

type SiteStat struct {
	Site  string `json:"site"`
	Count int64  `json:"count"`
	Err4  int64  `json:"err4"`
	Err5  int64  `json:"err5"`
}

// Snapshot 安全态势快照：每秒广播一次，前端只渲染这一份数据。
type Snapshot struct {
	Time         time.Time        `json:"time"`
	RPS          float64          `json:"rps"`
	BaselineRPS  float64          `json:"baselineRps"`
	Total        int64            `json:"total"`
	Status       map[string]int64 `json:"status"`
	ErrRate      float64          `json:"errRate"`
	Health       int              `json:"health"`
	Level        string           `json:"level"`
	Active       int              `json:"active"`
	TopIPs       []KV             `json:"topIps"`
	TopCountries []KV             `json:"topCountries"`
	TopPaths     []KV             `json:"topPaths"`
	TopUAs       []KV             `json:"topUas"`
	Sites        []SiteStat       `json:"sites"`
	Events       []Event          `json:"events"`
	L4           L4Stat           `json:"l4"`
	Policy       PolicyView       `json:"policy"`
	GeoLoaded    bool             `json:"geoLoaded"`
}

type siteTick struct {
	total int64
	e4    int64
	e5    int64
}

type bucket struct {
	t              time.Time
	total          int64
	s2, s3, s4, s5 int64
	bytes          uint64
	ips            map[string]int64
	countries      map[string]int64
	okCountries    map[string]int64
	countryIPs     map[string]int64 // 国家 -> 桶内独立 IP 数（dry-run 估算用）
	paths          map[string]int64
	uas            map[string]int64
	sites          map[string]*siteTick
	sensitive      map[string]int64  // ip -> 敏感路径命中次数
	loginFail      map[string]int64  // ip -> 登录接口 401/403 次数
	ipCountry      map[string]string // ip -> 国家（每桶首见）
	ipSite         map[string]string // ip -> 站点（每桶首见）
	ipPath         map[string]string // ip -> 最近命中的敏感路径
	ipUA           map[string]string
}

func newBucket(t time.Time) *bucket {
	return &bucket{
		t:   t,
		ips: map[string]int64{}, countries: map[string]int64{}, okCountries: map[string]int64{}, countryIPs: map[string]int64{},
		paths: map[string]int64{}, uas: map[string]int64{}, sites: map[string]*siteTick{},
		sensitive: map[string]int64{}, loginFail: map[string]int64{},
		ipCountry: map[string]string{}, ipSite: map[string]string{},
		ipPath: map[string]string{}, ipUA: map[string]string{},
	}
}

// Service 主机安全中心：订阅 logmap 实时条目 → 秒级聚合 → 规则判定 → WS 推送 + 策略处置。
type Service struct {
	cfg    *pkg.Config
	logmap *logmap.Service
	nginx  *nginx.Service
	hub    *ws.Hub
	bus    *bus.Bus // 攻击事件发布到总线，供 AI 运维（opsagent）订阅分析处置

	mu       sync.Mutex
	buckets  []*bucket
	events   []Event
	evSeq    int64
	lastEmit map[string]time.Time
	snap     Snapshot
	view     PolicyView
	ewRPS    float64
	ewErr    float64
	ewCtry   map[string]float64
	warm     int
	l4prev   l4Prev
	liveFile *os.File
	liveSize int64

	// PublishAttack（面板自身等非 nginx 来源攻击事件）的独立节流
	attackMu   sync.Mutex
	lastAttack map[string]time.Time

	polMu     sync.Mutex
	pol       *Policy
	cidrMu    sync.Mutex
	cidrCache map[string]cidrEntry
	applyMu   sync.Mutex

	// trusted 本机回环 + 白名单来源：完全跳过秒级聚合与攻击判定
	//（面板自身/站点探测/同机运维不应被判定为 CC、爆破或扫描）。
	trusted atomic.Pointer[trustedSet]

	// ignoredSites 停用应用关联网站：人工关停的站点掉线/5xx 不属于安全事件，
	// 整条来源从聚合里跳过（与可信来源区别：这里按站点名过滤，不看来源 IP）。
	ignoredMu   sync.Mutex
	ignoredFn   func() map[string]bool
	ignored     atomic.Pointer[ignoredSites]
	lastIgnored time.Time

	notifyMu   sync.Mutex
	lastNotify map[string]time.Time
	notifyFn   func(title, body string)

	stop chan struct{}
	once sync.Once
}

func New(cfg *pkg.Config, lm *logmap.Service, ng *nginx.Service, hub *ws.Hub, eventBus *bus.Bus) *Service {
	pol := defaultPolicy()
	s := &Service{
		cfg: cfg, logmap: lm, nginx: ng, hub: hub, bus: eventBus,
		lastEmit:   map[string]time.Time{},
		ewCtry:     map[string]float64{},
		cidrCache:  map[string]cidrEntry{},
		lastNotify: map[string]time.Time{},
		lastAttack: map[string]time.Time{},
		pol:        pol,
		view:       policyViewOf(pol),
		stop:       make(chan struct{}),
	}
	s.setTrusted(pol.Whitelist)
	return s
}

// SetNotify 注入统一通知回调（P2：严重事件自动告警）。
func (s *Service) SetNotify(fn func(title, body string)) {
	s.notifyMu.Lock()
	s.notifyFn = fn
	s.notifyMu.Unlock()
}

// Start 启动：加载策略 → 迁移已有站点 conf → 下发策略 → 订阅日志 → 每秒聚合。
func (s *Service) Start() {
	if err := s.loadPolicy(); err != nil {
		slog.Warn("安全策略读取失败，使用默认", "err", err)
	}
	s.ensureIncludes()
	if _, err := s.applyPolicy(); err != nil {
		slog.Warn("安全策略首次下发失败（不影响面板启动）", "err", err)
	}
	ch, unreg := s.logmap.SubscribeFast(true)
	go func() {
		defer unreg()
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				s.ingest(e)
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
			s.tick(now)
		}
	}
}

// ingest 归并一条实时请求到对应秒桶。
func (s *Service) ingest(e logmap.Entry) {
	ip := strings.TrimSpace(e.IP)
	// 本机回环与白名单来源不参与判定：面板自身页面/接口、站点探测、同机运维都会以
	// 127.0.0.1 进 nginx，计入 CC/爆破判定会把正常运维打成攻击（也会污染 Top 源与失血判定）。
	if s.isTrusted(ip) {
		return
	}
	// 停用应用关联网站：人工关停的站点掉线/5xx 不是安全事件，整站来源不计入态势与判定。
	if s.isIgnoredSite(strings.TrimSpace(e.Site)) {
		return
	}
	sec := e.Time.Truncate(time.Second)
	if sec.IsZero() {
		sec = time.Now().Truncate(time.Second)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b *bucket
	if n := len(s.buckets); n > 0 {
		last := s.buckets[n-1]
		if last.t.Equal(sec) {
			b = last
		} else if last.t.Before(sec) {
			b = newBucket(sec)
			s.buckets = append(s.buckets, b)
		}
	} else {
		b = newBucket(sec)
		s.buckets = append(s.buckets, b)
	}
	if b == nil {
		return // 乱序/历史补读：丢弃，不进实时统计
	}

	if ip == "" {
		ip = "-"
	}
	country := e.Country
	if country == "" {
		country = "unknown"
	}
	site := e.Site
	if site == "" {
		site = "-"
	}
	path := e.Path
	if path == "" {
		path = "-"
	}

	b.total++
	b.bytes += e.Bytes
	switch {
	case e.Status >= 500:
		b.s5++
	case e.Status >= 400:
		b.s4++
	case e.Status >= 300:
		b.s3++
	default:
		b.s2++
	}
	b.ips[ip]++
	b.countries[country]++
	if e.Status < 300 {
		b.okCountries[country]++
	}
	b.paths[path]++
	if e.UA != "" {
		b.uas[e.UA]++
	}
	st := b.sites[site]
	if st == nil {
		st = &siteTick{}
		b.sites[site] = st
	}
	st.total++
	if e.Status >= 500 {
		st.e5++
	} else if e.Status >= 400 {
		st.e4++
	}
	if _, ok := b.ipCountry[ip]; !ok {
		b.ipCountry[ip] = country
		b.ipSite[ip] = site
		b.countryIPs[country]++
		if e.UA != "" {
			b.ipUA[ip] = e.UA
		}
	}
	if isSensitivePath(path) {
		b.sensitive[ip]++
		b.ipPath[ip] = path
	}
	if isLoginPath(path) && (e.Status == 401 || e.Status == 403) {
		b.loginFail[ip]++
	}
}

// tick 每秒：滚动窗口 → 规则判定 → 快照推送 → 断联快照落盘 → 到期策略回收。
func (s *Service) tick(now time.Time) {
	l4 := readL4(&s.l4prev)
	// 停用应用集合周期性刷新（10s），读侧无锁。
	s.refreshIgnored(now)

	s.mu.Lock()
	cut := now.Add(-bucketKeep * time.Second)
	for len(s.buckets) > 0 && s.buckets[0].t.Before(cut) {
		s.buckets = s.buckets[1:]
	}
	w := s.window(now)

	// EWMA 基线：攻击判定用"偏离自身基线"而不是绝对阈值
	if s.warm < 30 {
		s.warm++
		s.ewRPS = float64(w.curRPS)
	} else {
		s.ewRPS = 0.95*s.ewRPS + 0.05*float64(w.curRPS)
	}
	er := 0.0
	if w.total > 0 {
		er = float64(w.s4+w.s5) / float64(w.total)
	}
	if s.warm >= 30 {
		s.ewErr = 0.9*s.ewErr + 0.1*er
	} else {
		s.ewErr = er
	}
	for c, n := range w.countries {
		if w.total > 0 {
			share := float64(n) / float64(w.total)
			if s.warm >= 30 {
				s.ewCtry[c] = 0.95*s.ewCtry[c] + 0.05*share
			} else {
				s.ewCtry[c] = share
			}
		}
	}
	bans := s.detect(w, l4, now)
	// 去重表回收：被打时会有大量不同源 IP，不清理会随运行时长无限增长
	if len(s.lastEmit) > 2000 {
		for k, t := range s.lastEmit {
			if now.Sub(t) > 10*time.Minute {
				delete(s.lastEmit, k)
			}
		}
	}
	activeCritical, activeWarn := 0, 0
	for _, e := range s.events {
		if now.Sub(e.Time) >= winSec*time.Second {
			continue
		}
		if e.Severity == "critical" {
			activeCritical++
		} else if e.Severity == "warn" {
			activeWarn++
		}
	}
	snap := s.build(w, l4, now, activeCritical, activeWarn)
	s.snap = snap
	events := append([]Event(nil), s.events...)
	s.mu.Unlock()

	if s.hub.Count("security") > 0 {
		if b, err := json.Marshal(snap); err == nil {
			s.hub.Broadcast("security", b)
		}
	}
	s.persistLive(snap, events)
	if err := s.BlockBatch(bans); err != nil {
		slog.Warn("自动封禁下发失败", "err", err)
	}
	if s.expireBlocks(now) > 0 {
		if _, err := s.applyPolicy(); err != nil {
			slog.Warn("到期策略回收后下发失败", "err", err)
		}
	}
}

type win struct {
	total          int64
	s2, s3, s4, s5 int64
	curRPS         int64
	ips            map[string]int64
	ip10           map[string]int64
	countries      map[string]int64
	okCountries    map[string]int64
	paths          map[string]int64
	uas            map[string]int64
	sites          map[string]*siteTick
	sensitive      map[string]int64
	loginFail      map[string]int64
	ipCountry      map[string]string
	ipSite         map[string]string
	ipPath         map[string]string
	ipUA           map[string]string
}

func (s *Service) window(now time.Time) *win {
	w := &win{
		ips: map[string]int64{}, ip10: map[string]int64{}, countries: map[string]int64{},
		okCountries: map[string]int64{}, paths: map[string]int64{}, uas: map[string]int64{},
		sites: map[string]*siteTick{}, sensitive: map[string]int64{}, loginFail: map[string]int64{},
		ipCountry: map[string]string{}, ipSite: map[string]string{}, ipPath: map[string]string{}, ipUA: map[string]string{},
	}
	t10 := now.Add(-10 * time.Second)
	cut60 := now.Add(-winSec * time.Second)
	// 最近 5 个完整秒的平均作为实时 RPS
	recent := 0
	for i := len(s.buckets) - 1; i >= 0 && i >= len(s.buckets)-5; i-- {
		recent += int(s.buckets[i].total)
	}
	if n := min(5, len(s.buckets)); n > 0 {
		w.curRPS = int64(recent / n)
	}
	for _, b := range s.buckets {
		if b.t.Before(cut60) {
			continue
		}
		w.total += b.total
		w.s2 += b.s2
		w.s3 += b.s3
		w.s4 += b.s4
		w.s5 += b.s5
		mergeInt(w.ips, b.ips)
		mergeInt(w.countries, b.countries)
		mergeInt(w.okCountries, b.okCountries)
		mergeInt(w.paths, b.paths)
		mergeInt(w.uas, b.uas)
		mergeInt(w.sensitive, b.sensitive)
		mergeInt(w.loginFail, b.loginFail)
		for site, st := range b.sites {
			dst := w.sites[site]
			if dst == nil {
				dst = &siteTick{}
				w.sites[site] = dst
			}
			dst.total += st.total
			dst.e4 += st.e4
			dst.e5 += st.e5
		}
		for ip, c := range b.ipCountry {
			if _, ok := w.ipCountry[ip]; !ok {
				w.ipCountry[ip] = c
			}
		}
		for ip, sv := range b.ipSite {
			if _, ok := w.ipSite[ip]; !ok {
				w.ipSite[ip] = sv
			}
		}
		for ip, p := range b.ipPath {
			w.ipPath[ip] = p
		}
		for ip, ua := range b.ipUA {
			if _, ok := w.ipUA[ip]; !ok {
				w.ipUA[ip] = ua
			}
		}
		if b.t.After(t10) {
			mergeInt(w.ip10, b.ips)
		}
	}
	return w
}

func mergeInt(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

type autoBan struct {
	IP     string
	Reason string
}

// detect 五类特征分开判定：处置方式不同，绝不用一个绝对阈值概括全部。
func (s *Service) detect(w *win, l4 L4Stat, now time.Time) []autoBan {
	var bans []autoBan

	for ip, c := range w.sensitive {
		if c < 5 {
			continue
		}
		s.emit("扫描探测", "warn", w.ipSite[ip], ip, w.ipCountry[ip], w.ipPath[ip], w.ipUA[ip],
			fmt.Sprintf("60s 内命中敏感路径 %d 次", c), int(c), now)
		if c >= 20 && s.view.AutoBan {
			bans = append(bans, autoBan{IP: ip, Reason: "扫描敏感路径"})
		}
	}

	for ip, c := range w.loginFail {
		if c < 8 {
			continue
		}
		s.emit("暴力破解", "critical", w.ipSite[ip], ip, w.ipCountry[ip], w.ipPath[ip], w.ipUA[ip],
			fmt.Sprintf("60s 内登录失败 %d 次", c), int(c), now)
		if c >= 20 && s.view.AutoBan {
			bans = append(bans, autoBan{IP: ip, Reason: "登录暴力破解"})
		}
	}

	// CC 攻击：10s 窗口内请求数达到「速率阈值(r/s) × 10」才算突发；阈值走策略配置（默认 20 r/s）。
	ccReq := s.ccThreshold() * 10
	for ip, c := range w.ip10 {
		if c < int64(ccReq) {
			continue
		}
		rps := c / 10
		s.emit("CC 攻击", "warn", w.ipSite[ip], ip, w.ipCountry[ip], w.ipPath[ip], w.ipUA[ip],
			fmt.Sprintf("10s 内 %d 次请求（约 %d rps）", c, rps), int(c), now)
	}

	if w.total >= 60 && w.s5*100 >= w.total*5 {
		rate := float64(w.s5) * 100 / float64(w.total)
		top := topK(w.paths, 1)
		p := ""
		if len(top) > 0 {
			p = top[0].Name
		}
		// 失血定位到具体站点：取 5xx 次数最多的非停用站点，来源列直接显示域名，
		// 不再只写「全站」让用户自己猜是哪个站被打挂。
		siteName := "全站"
		var worst *siteTick
		for name, st := range w.sites {
			if name == "" || name == "-" || s.isIgnoredSite(name) || st.e5 <= 0 {
				continue
			}
			if worst == nil || st.e5 > worst.e5 {
				worst, siteName = st, name
			}
		}
		detail := fmt.Sprintf("60s 内 5xx 占 %.1f%%（%d 次），站点可能已被打挂", rate, w.s5)
		if worst != nil && worst.total > 0 {
			detail = fmt.Sprintf("60s 内全站 5xx 占 %.1f%%（%d 次），该站 %d 次（%.1f%%），站点可能已被打挂",
				rate, w.s5, worst.e5, float64(worst.e5)*100/float64(worst.total))
		}
		s.emit("站点失血", "critical", siteName, "", "", p, "", detail, int(w.s5), now)
	}

	for c, n := range w.countries {
		if w.total < 200 || c == "unknown" {
			continue
		}
		share := float64(n) / float64(w.total)
		base := s.ewCtry[c]
		if share >= 0.25 && base > 0 && share >= base*3 {
			s.emit("地区突变", "warn", "全站", "", c, "", "",
				fmt.Sprintf("该地区请求占比 %.0f%%（基线 %.0f%%），疑似僵尸网络", share*100, base*100), int(n), now)
		}
	}

	if l4.Supported {
		if l4.SynRecv > 1000 {
			s.emit("L4 攻击", "critical", "主机", "", "", "", "",
				fmt.Sprintf("SYN_RECV 半连接堆积 %d 个，疑似 SYN Flood", l4.SynRecv), l4.SynRecv, now)
		}
		if l4.Overflows > 0 {
			s.emit("L4 攻击", "critical", "主机", "", "", "", "",
				fmt.Sprintf("accept 队列溢出 %d 次，新建连接被打满", l4.Overflows), int(l4.Overflows), now)
		}
	}
	return dedupBans(bans)
}

func dedupBans(in []autoBan) []autoBan {
	seen := map[string]bool{}
	out := in[:0]
	for _, b := range in {
		if seen[b.IP] {
			continue
		}
		seen[b.IP] = true
		out = append(out, b)
	}
	return out
}

func (s *Service) emit(kind, sev, site, ip, country, path, ua, detail string, count int, now time.Time) {
	key := kind + "|" + ip + "|" + country
	cool := 120 * time.Second
	if sev == "critical" {
		cool = 60 * time.Second
	}
	if t, ok := s.lastEmit[key]; ok && now.Sub(t) < cool {
		return
	}
	s.lastEmit[key] = now
	s.evSeq++
	e := Event{ID: s.evSeq, Time: now, Kind: kind, Severity: sev, Site: site, IP: ip,
		Country: country, Path: path, UA: ua, Detail: detail, Count: count}
	s.events = append(s.events, e)
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
	// 攻击事件进总线：AI 运维（opsagent）订阅 security.attack 自动分析处置
	//（emit 自带 60/120s 冷却，不会打爆总线）
	if s.bus != nil {
		s.bus.Publish("security.attack", map[string]any{
			"kind":     kind,
			"severity": sev,
			"site":     site,
			"ip":       ip,
			"country":  country,
			"path":     path,
			"count":    count,
			"detail":   detail,
			"time":     now.Format(time.RFC3339),
		})
	}
	if sev == "critical" {
		s.notifyCritical(kind, detail, now)
	}
}

// PublishAttack 面板自身等非 nginx 来源的攻击事件（如登录爆破触发限速）：
// 不进安全中心事件环（无站点流量上下文），直接发布 security.attack 供 AI 订阅，
// 按 kind+ip 节流 60s。
func (s *Service) PublishAttack(kind, sev, ip, detail string) {
	if s.bus == nil {
		return
	}
	now := time.Now()
	key := kind + "|" + ip
	s.attackMu.Lock()
	if t, ok := s.lastAttack[key]; ok && now.Sub(t) < 60*time.Second {
		s.attackMu.Unlock()
		return
	}
	s.lastAttack[key] = now
	s.attackMu.Unlock()
	s.bus.Publish("security.attack", map[string]any{
		"kind":     kind,
		"severity": sev,
		"ip":       ip,
		"detail":   detail,
		"time":     now.Format(time.RFC3339),
	})
}

// notifyCritical 严重事件告警：同类事件 5 分钟内只推一次，避免被打时把通知通道自己打爆。
func (s *Service) notifyCritical(kind, detail string, now time.Time) {
	s.notifyMu.Lock()
	fn := s.notifyFn
	last, ok := s.lastNotify[kind]
	if fn == nil || (ok && now.Sub(last) < 5*time.Minute) {
		s.notifyMu.Unlock()
		return
	}
	s.lastNotify[kind] = now
	s.notifyMu.Unlock()
	go fn("安全告警："+kind, detail)
}

func (s *Service) build(w *win, l4 L4Stat, now time.Time, activeCritical, activeWarn int) Snapshot {
	status := map[string]int64{
		"2xx": w.s2, "3xx": w.s3, "4xx": w.s4, "5xx": w.s5,
	}
	errRate := 0.0
	if w.total > 0 {
		errRate = math.Round(float64(w.s4+w.s5)*1000/float64(w.total)) / 10
	}
	h := 100
	if w.total >= 60 {
		err5 := float64(w.s5) * 100 / float64(w.total)
		switch {
		case err5 >= 20:
			h -= 45
		case err5 >= 5:
			h -= 30
		case err5 >= 1:
			h -= 10
		}
		if float64(w.s4)*100/float64(w.total) > 50 {
			h -= 15
		}
	}
	if activeCritical > 0 {
		h -= 25
	}
	// 仅 warn 级（扫描、爆破尝试）也要在健康度上体现，否则"被扫但没 5xx"时页面一片绿
	if activeWarn > 0 {
		pen := activeWarn * 3
		if pen > 15 {
			pen = 15
		}
		h -= pen
	}
	if l4.SynRecv > 1000 {
		h -= 10
	}
	if h < 0 {
		h = 0
	}
	level := "normal"
	if h < 50 {
		level = "critical"
	} else if h < 80 {
		level = "warn"
	}

	sites := make([]SiteStat, 0, len(w.sites))
	for name, st := range w.sites {
		sites = append(sites, SiteStat{Site: name, Count: st.total, Err4: st.e4, Err5: st.e5})
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Count > sites[j].Count })
	if len(sites) > 12 {
		sites = sites[:12]
	}

	evs := make([]Event, 0, 20)
	for i := len(s.events) - 1; i >= 0 && len(evs) < 20; i-- {
		evs = append(evs, s.events[i])
	}

	return Snapshot{
		Time: now, RPS: math.Round(float64(w.curRPS)*10) / 10,
		BaselineRPS: math.Round(s.ewRPS*10) / 10,
		Total:       w.total, Status: status, ErrRate: errRate,
		Health: h, Level: level, Active: activeCritical,
		TopIPs:       topK(w.ips, 8),
		TopCountries: topK(w.countries, 8),
		TopPaths:     topK(w.paths, 8),
		TopUAs:       topK(w.uas, 6),
		Sites:        sites, Events: evs, L4: l4,
		Policy: s.view, GeoLoaded: s.logmap.GeoIPLoaded(),
	}
}

func topK(m map[string]int64, n int) []KV {
	type pair struct {
		k string
		v int64
	}
	list := make([]pair, 0, len(m))
	var total int64
	for k, v := range m {
		list = append(list, pair{k, v})
		total += v
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	if len(list) > n {
		list = list[:n]
	}
	out := make([]KV, 0, len(list))
	for _, p := range list {
		pct := 0.0
		if total > 0 {
			pct = math.Round(float64(p.v)*1000/float64(total)) / 10
		}
		out = append(out, KV{Name: p.k, Count: p.v, Pct: pct})
	}
	return out
}

func isSensitivePath(p string) bool {
	lp := strings.ToLower(p)
	for _, s := range sensitivePaths {
		if strings.Contains(lp, s) {
			return true
		}
	}
	return false
}

var sensitivePaths = []string{
	"/.env", "/.git", "/.aws", "/wp-admin", "/wp-login", "/phpmyadmin",
	"/xmlrpc.php", "/actuator", "/config.php", "/backup", "/.svn", "/vendor/",
	"/cgi-bin/", "/admin.php", "/db.php", "/.DS_Store",
}

func isLoginPath(p string) bool {
	lp := strings.ToLower(p)
	return strings.Contains(lp, "login") || strings.Contains(lp, "signin") ||
		strings.HasSuffix(lp, "/admin") || strings.Contains(lp, "auth")
}

// Latest 返回最近一次快照（HTTP 轮询兜底，WebSocket 断线时也不至于空白）。
func (s *Service) Latest() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// RecentEvents 返回最近事件（按时间倒序）。
func (s *Service) RecentEvents(limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}
	out := make([]Event, 0, limit)
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.events[i])
	}
	return out
}

type liveLine struct {
	T       int64   `json:"t"`
	RPS     float64 `json:"rps"`
	Total   int64   `json:"total"`
	E4      int64   `json:"e4"`
	E5      int64   `json:"e5"`
	Health  int     `json:"health"`
	Conn    int     `json:"conn"`
	SynRecv int     `json:"synRecv"`
	RxBps   float64 `json:"rxBps"`
	Active  int     `json:"active"`
}

// persistLive 每秒落一行精简快照：攻击把带宽打满时面板会失联，
// 恢复后靠这份环形文件回放"断联前发生了什么"。8MB 一个文件，滚动保留。
func (s *Service) persistLive(snap Snapshot, events []Event) {
	dir := filepath.Join(s.cfg.DataDir, "security")
	_ = os.MkdirAll(dir, 0o755)
	line := liveLine{T: snap.Time.Unix(), RPS: snap.RPS, Total: snap.Total,
		E4: snap.Status["4xx"], E5: snap.Status["5xx"], Health: snap.Health,
		Conn: snap.L4.Conn, SynRecv: snap.L4.SynRecv, RxBps: snap.L4.RxBps, Active: snap.Active}
	b, _ := json.Marshal(line)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveFile == nil {
		path := filepath.Join(dir, "live.jsonl")
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
		path := filepath.Join(dir, "live.jsonl")
		_ = os.Rename(path, filepath.Join(dir, "live.1.jsonl"))
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
