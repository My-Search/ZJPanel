package security

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang"

	"zjpanel/backend/internal/nginx"
)

// Block 一条封禁记录：Until 为零值表示永久；Auto=true 表示规则引擎自动下发。
type Block struct {
	Value     string    `json:"value"`
	Until     time.Time `json:"until"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
	Auto      bool      `json:"auto"`
}

// Policy 安全策略（持久化到 data/security/policy.json，配置文件由它渲染）。
type Policy struct {
	Enabled        bool      `json:"enabled"`
	Observe        bool      `json:"observe"` // 观察模式：只判定不拦截（上线前先跑一天）
	AutoBan        bool      `json:"autoBan"` // P2：规则触发自动封禁
	Whitelist      []string  `json:"whitelist"`
	BlockIPs       []Block   `json:"blockIps"`
	BlockCountries []Block   `json:"blockCountries"`
	CCEnabled      bool      `json:"ccEnabled"`
	CCRate         int       `json:"ccRate"`
	CCBurst        int       `json:"ccBurst"`
	Status         int       `json:"status"` // 444 直接断连 / 403
	UpdatedAt      time.Time `json:"updatedAt"`
}

// PolicyView 快照里的策略摘要（不把完整 CIDR 列表每秒推给前端）。
type PolicyView struct {
	Enabled          bool      `json:"enabled"`
	Observe          bool      `json:"observe"`
	AutoBan          bool      `json:"autoBan"`
	BlockedIPs       int       `json:"blockedIps"`
	BlockedCountries int       `json:"blockedCountries"`
	Whitelist        int       `json:"whitelist"`
	CCEnabled        bool      `json:"ccEnabled"`
	CCRate           int       `json:"ccRate"`
	CCBurst          int       `json:"ccBurst"`
	Status           int       `json:"status"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func defaultPolicy() *Policy {
	return &Policy{
		Enabled: true, Status: 444, CCRate: 20, CCBurst: 30,
		Whitelist: []string{"127.0.0.1", "::1"},
	}
}

func policyViewOf(p *Policy) PolicyView {
	return PolicyView{
		Enabled: p.Enabled, Observe: p.Observe, AutoBan: p.AutoBan,
		BlockedIPs: len(p.BlockIPs), BlockedCountries: len(p.BlockCountries),
		Whitelist: len(p.Whitelist), CCEnabled: p.CCEnabled, CCRate: p.CCRate,
		CCBurst: p.CCBurst, Status: p.Status, UpdatedAt: p.UpdatedAt,
	}
}

func (s *Service) policyPath() string {
	return filepath.Join(s.cfg.DataDir, "security", "policy.json")
}

func (s *Service) loadPolicy() error {
	b, err := os.ReadFile(s.policyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	normalizePolicy(&p)
	s.polMu.Lock()
	s.pol = &p
	s.polMu.Unlock()
	s.setTrusted(p.Whitelist)
	s.mu.Lock()
	s.view = policyViewOf(&p)
	s.mu.Unlock()
	return nil
}

func (s *Service) savePolicy(p *Policy) error {
	dir := filepath.Dir(s.policyPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.policyPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.policyPath())
}

func (s *Service) PolicyCopy() Policy {
	s.polMu.Lock()
	defer s.polMu.Unlock()
	b, _ := json.Marshal(s.pol)
	var p Policy
	_ = json.Unmarshal(b, &p)
	return p
}

// SetPolicy 保存并立即下发（含观察模式/CC 开关/白名单变更）。
func (s *Service) SetPolicy(p Policy) error {
	normalizePolicy(&p)
	if err := s.validatePolicy(&p); err != nil {
		return err
	}
	p.UpdatedAt = time.Now()
	s.polMu.Lock()
	s.pol = &p
	s.polMu.Unlock()
	s.setTrusted(p.Whitelist)
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

func normalizePolicy(p *Policy) {
	if p.Status != 444 && p.Status != 403 {
		p.Status = 444
	}
	if p.CCRate <= 0 {
		p.CCRate = 20
	}
	if p.CCRate > 2000 {
		p.CCRate = 2000
	}
	if p.CCBurst < 0 {
		p.CCBurst = 0
	}
	if p.CCBurst > 2000 {
		p.CCBurst = 2000
	}
	var wl []string
	seen := map[string]bool{}
	for _, w := range p.Whitelist {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		wl = append(wl, w)
	}
	p.Whitelist = wl
	now := time.Now()
	var ips []Block
	for _, b := range p.BlockIPs {
		if !b.Until.IsZero() && b.Until.Before(now) {
			continue
		}
		if strings.TrimSpace(b.Value) != "" {
			ips = append(ips, b)
		}
	}
	p.BlockIPs = ips
	var cs []Block
	for _, b := range p.BlockCountries {
		v := strings.ToUpper(strings.TrimSpace(b.Value))
		if len(v) != 2 || !b.Until.IsZero() && b.Until.Before(now) {
			continue
		}
		b.Value = v
		cs = append(cs, b)
	}
	p.BlockCountries = cs
}

func (s *Service) validatePolicy(p *Policy) error {
	for _, w := range p.Whitelist {
		if err := validIPOrCIDR(w); err != nil {
			return fmt.Errorf("白名单 %q 不是合法 IP/CIDR", w)
		}
	}
	for _, b := range p.BlockIPs {
		if err := validIPOrCIDR(b.Value); err != nil {
			return fmt.Errorf("封禁项 %q 不是合法 IP/CIDR", b.Value)
		}
	}
	return nil
}

func validIPOrCIDR(v string) error {
	if net.ParseIP(v) != nil {
		return nil
	}
	if _, _, err := net.ParseCIDR(v); err == nil {
		return nil
	}
	return fmt.Errorf("invalid ip/cidr")
}

func (s *Service) inWhitelist(ip string) bool {
	s.polMu.Lock()
	wl := append([]string(nil), s.pol.Whitelist...)
	s.polMu.Unlock()
	return ipInList(ip, wl)
}

// ccThreshold 读取当前策略的 CC 速率阈值（r/s）；策略缺失或未配置时回落到默认值。
func (s *Service) ccThreshold() int {
	s.polMu.Lock()
	defer s.polMu.Unlock()
	if s.pol == nil || s.pol.CCRate <= 0 {
		return 20
	}
	return s.pol.CCRate
}

// ipInList 纯函数版白名单判定：调用方已持有/不需要策略锁时使用。
func ipInList(ip string, wl []string) bool {
	parsed := net.ParseIP(strings.TrimSuffix(ip, "/32"))
	if parsed == nil {
		return false
	}
	for _, w := range wl {
		if w == ip {
			return true
		}
		if _, ipnet, err := net.ParseCIDR(w); err == nil && ipnet.Contains(parsed) {
			return true
		}
	}
	return false
}

// BlockBatch 规则引擎批量自动封禁：合并成一次写入 + 一次 reload。
// 逐个封禁会让一轮命中触发 N 次 nginx reload，攻击期间 reload 本身就会变成负担。
func (s *Service) BlockBatch(bans []autoBan) error {
	if len(bans) == 0 {
		return nil
	}
	now := time.Now()
	s.polMu.Lock()
	wl := append([]string(nil), s.pol.Whitelist...)
	exists := map[string]bool{}
	for _, b := range s.pol.BlockIPs {
		exists[b.Value] = true
	}
	added := 0
	for _, b := range bans {
		if exists[b.IP] || ipInList(b.IP, wl) {
			continue
		}
		s.pol.BlockIPs = append(s.pol.BlockIPs, Block{
			Value: b.IP, Until: now.Add(60 * time.Minute),
			Reason: b.Reason, CreatedAt: now, Auto: true,
		})
		exists[b.IP] = true
		added++
	}
	if added == 0 {
		s.polMu.Unlock()
		return nil
	}
	p := *s.pol
	p.UpdatedAt = now
	s.polMu.Unlock()
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

// BlockIP 封禁 IP/CIDR；minutes<=0 表示永久。auto=true 由规则引擎调用（默认 60 分钟）。
func (s *Service) BlockIP(ip string, minutes int, reason string, auto bool) error {
	ip = strings.TrimSpace(ip)
	if err := validIPOrCIDR(ip); err != nil {
		return fmt.Errorf("不是合法的 IP/CIDR")
	}
	if s.inWhitelist(strings.TrimSuffix(ip, "/32")) {
		return fmt.Errorf("该地址在白名单中，拒绝封禁")
	}
	now := time.Now()
	b := Block{Value: ip, Reason: reason, CreatedAt: now, Auto: auto}
	if minutes > 0 {
		b.Until = now.Add(time.Duration(minutes) * time.Minute)
	}
	s.polMu.Lock()
	replaced := false
	for i, old := range s.pol.BlockIPs {
		if old.Value == ip {
			s.pol.BlockIPs[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		s.pol.BlockIPs = append(s.pol.BlockIPs, b)
	}
	p := *s.pol
	p.UpdatedAt = now
	s.polMu.Unlock()
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

func (s *Service) UnblockIP(ip string) error {
	ip = strings.TrimSpace(ip)
	s.polMu.Lock()
	removed := false
	var keep []Block
	for _, b := range s.pol.BlockIPs {
		if b.Value == ip {
			removed = true
			continue
		}
		keep = append(keep, b)
	}
	s.pol.BlockIPs = keep
	p := *s.pol
	p.UpdatedAt = time.Now()
	s.polMu.Unlock()
	if !removed {
		return fmt.Errorf("未找到该封禁项")
	}
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

// BlockCountry 按 ISO 国家码封禁：渲染时枚举 GeoIP 库中该国的全部 CIDR 段。
func (s *Service) BlockCountry(code string, minutes int, reason string, auto bool) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return fmt.Errorf("国家码必须是两位 ISO 代码（如 RU/US/SG）")
	}
	if _, err := s.cidrsFor(code); err != nil {
		return err
	}
	now := time.Now()
	b := Block{Value: code, Reason: reason, CreatedAt: now, Auto: auto}
	if minutes > 0 {
		b.Until = now.Add(time.Duration(minutes) * time.Minute)
	}
	s.polMu.Lock()
	replaced := false
	for i, old := range s.pol.BlockCountries {
		if old.Value == code {
			s.pol.BlockCountries[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		s.pol.BlockCountries = append(s.pol.BlockCountries, b)
	}
	p := *s.pol
	p.UpdatedAt = now
	s.polMu.Unlock()
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

func (s *Service) UnblockCountry(code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	s.polMu.Lock()
	removed := false
	var keep []Block
	for _, b := range s.pol.BlockCountries {
		if b.Value == code {
			removed = true
			continue
		}
		keep = append(keep, b)
	}
	s.pol.BlockCountries = keep
	p := *s.pol
	p.UpdatedAt = time.Now()
	s.polMu.Unlock()
	if !removed {
		return fmt.Errorf("未找到该封禁项")
	}
	if err := s.savePolicy(&p); err != nil {
		return err
	}
	_, err := s.applyPolicy()
	return err
}

// expireBlocks 回收到期封禁，返回移除条数（由 tick 调用，随后重新下发）。
func (s *Service) expireBlocks(now time.Time) int {
	s.polMu.Lock()
	n := 0
	var ips []Block
	for _, b := range s.pol.BlockIPs {
		if !b.Until.IsZero() && b.Until.Before(now) {
			n++
			continue
		}
		ips = append(ips, b)
	}
	var cs []Block
	for _, b := range s.pol.BlockCountries {
		if !b.Until.IsZero() && b.Until.Before(now) {
			n++
			continue
		}
		cs = append(cs, b)
	}
	if n == 0 {
		s.polMu.Unlock()
		return 0
	}
	s.pol.BlockIPs, s.pol.BlockCountries = ips, cs
	p := *s.pol
	p.UpdatedAt = now
	s.polMu.Unlock()
	_ = s.savePolicy(&p)
	return n
}

type cidrEntry struct {
	cidrs []string
	at    time.Time
}

// cidrsFor 枚举 GeoIP 库中指定国家的全部 CIDR（10 分钟缓存）。
// 这是"没有 lua/geoip 模块也能按国家封禁"的关键：把国家展开成原生 geo 规则。
func (s *Service) cidrsFor(code string) ([]string, error) {
	s.cidrMu.Lock()
	if e, ok := s.cidrCache[code]; ok && time.Since(e.at) < 10*time.Minute {
		s.cidrMu.Unlock()
		return e.cidrs, nil
	}
	s.cidrMu.Unlock()

	path := filepath.Join(s.cfg.DataDir, "geoip", "GeoLite2-Country.mmdb")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("GeoIP 库未就绪（请先在请求地图页上传 GeoLite2 库）")
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("GeoIP 库打开失败: %w", err)
	}
	defer db.Close()

	var cidrs []string
	nw := db.Networks(maxminddb.SkipAliasedNetworks)
	for nw.Next() {
		var rec struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
		}
		ipnet, err := nw.Network(&rec)
		if err != nil || ipnet == nil {
			continue
		}
		if rec.Country.ISOCode == code {
			cidrs = append(cidrs, ipnet.String())
			if len(cidrs) > 60000 {
				return nil, fmt.Errorf("该国家 CIDR 超过 6 万条，拒绝生成（避免配置过大）")
			}
		}
	}
	if err := nw.Err(); err != nil {
		return nil, err
	}
	sort.Strings(cidrs)
	s.cidrMu.Lock()
	s.cidrCache[code] = cidrEntry{cidrs: cidrs, at: time.Now()}
	s.cidrMu.Unlock()
	return cidrs, nil
}

// render 渲染 http 级策略与站点级 include。
func (s *Service) render(p *Policy) (conf string, inc string, warnings []string) {
	var b strings.Builder
	b.WriteString("# 由 ZJPanel 安全中心自动生成，请勿手动修改（下次保存策略会覆盖）\n")
	b.WriteString("# 生成时间 " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	b.WriteString("geo $zjp_blocked {\n    default 0;\n")
	// 封禁在前、白名单在后：同前缀重复时白名单覆盖；更具体的前缀始终优先
	for _, blk := range p.BlockIPs {
		b.WriteString("    " + geoEntry(blk.Value) + " 1;\n")
	}
	for _, blk := range p.BlockCountries {
		cidrs, err := s.cidrsFor(blk.Value)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("国家 %s 无法生成规则：%v", blk.Value, err))
			continue
		}
		b.WriteString("    # 封禁国家 " + blk.Value + "\n")
		for _, c := range cidrs {
			b.WriteString("    " + c + " 1;\n")
		}
	}
	wl := append([]string{"127.0.0.1", "::1"}, p.Whitelist...)
	seen := map[string]bool{}
	for _, w := range wl {
		key := geoEntry(w)
		if seen[key] {
			continue
		}
		seen[key] = true
		b.WriteString("    " + key + " 0;\n")
	}
	b.WriteString("}\n")
	if p.CCEnabled {
		fmt.Fprintf(&b, "\nlimit_req_zone $binary_remote_addr zone=zjp_cc:10m rate=%dr/s;\n", p.CCRate)
	}

	var c strings.Builder
	c.WriteString("# 由 ZJPanel 安全中心自动生成，请勿手动修改\n")
	if p.Enabled && !p.Observe {
		fmt.Fprintf(&c, "if ($zjp_blocked = 1) { return %d; }\n", p.Status)
	} else {
		c.WriteString("# 观察/停用模式：仅记录不拦截\n")
	}
	if p.CCEnabled {
		fmt.Fprintf(&c, "limit_req zone=zjp_cc burst=%d nodelay;\n", p.CCBurst)
	}
	return b.String(), c.String(), warnings
}

// geoEntry 单条 geo 记录：裸 IP 补 /32，避免 nginx 把 IPv6 误判。
func geoEntry(v string) string {
	if strings.Contains(v, "/") {
		return v
	}
	if ip := net.ParseIP(v); ip != nil && ip.To4() == nil {
		return v + "/128"
	}
	return v + "/32"
}

// applyPolicy 渲染 → 原子写入两个文件 → nginx -t → reload（失败全部回滚）。
func (s *Service) applyPolicy() (string, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	s.polMu.Lock()
	p := *s.pol
	s.polMu.Unlock()

	conf, inc, _ := s.render(&p)
	out, err := s.nginx.SaveFilesAndReload(map[string]string{
		"conf.d/zjp-security.conf": conf,
		nginx.SecurityIncludeRel:   inc,
	})
	if err != nil {
		// 回滚可能把 server.inc 一起删掉，而站点 conf 已经 include 它：
		// 留一个空兜底文件，避免下一次任意 reload 因缺文件直接失败。
		dir := filepath.Join(s.cfg.NginxPrefix, "zjp-security")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(s.cfg.NginxPrefix, nginx.SecurityIncludeRel),
			[]byte("# ZJPanel 安全策略：上次下发失败，已回退为空策略\n"), 0o644)
		return out, err
	}
	s.mu.Lock()
	s.view = policyViewOf(&p)
	s.mu.Unlock()
	return out, nil
}

// ensureIncludes 把安全 include 迁移进已存在的站点 conf（含高级模式手写 conf）。
// 幂等；只在缺少 include 时插入一行，不改动其他内容。
func (s *Service) ensureIncludes() {
	dir := filepath.Join(s.cfg.NginxPrefix, "sites-available")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := string(b)
		if strings.Contains(content, nginx.SecurityIncludeRel) {
			continue
		}
		upd := nginx.EnsureSecurityInclude(content, s.cfg.NginxPrefix)
		if upd == content {
			continue
		}
		_ = os.WriteFile(p, []byte(upd), 0o644)
	}
}

// DryRunResult 封禁前预演：先看清会影响多少正常流量，再决定是否下手。
type DryRunResult struct {
	Kind         string  `json:"kind"`
	Value        string  `json:"value"`
	Minutes      int     `json:"minutes"`
	Requests     int64   `json:"requests"`
	OK           int64   `json:"ok"`
	OKPct        float64 `json:"okPct"`
	IPs          int64   `json:"ips"`
	MatchedCIDRs int     `json:"matchedCidrs"`
	Warning      string  `json:"warning"`
}

func (s *Service) DryRun(kind, value string) (*DryRunResult, error) {
	value = strings.TrimSpace(value)
	res := &DryRunResult{Kind: kind, Value: value, Minutes: 5}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buckets) == 0 {
		res.Warning = "暂无最近请求数据"
		return res, nil
	}

	switch kind {
	case "country":
		code := strings.ToUpper(value)
		if len(code) != 2 {
			return nil, fmt.Errorf("国家码必须是两位 ISO 代码")
		}
		for _, b := range s.buckets {
			res.Requests += b.countries[code]
			res.OK += b.okCountries[code]
			res.IPs += b.countryIPs[code]
		}
		cidrs, err := s.cidrsFor(code)
		if err != nil {
			return nil, err
		}
		res.MatchedCIDRs = len(cidrs)
	case "ip":
		if err := validIPOrCIDR(value); err != nil {
			return nil, fmt.Errorf("不是合法的 IP/CIDR")
		}
		for _, b := range s.buckets {
			res.Requests += b.ips[value]
			res.IPs += 1
		}
	default:
		return nil, fmt.Errorf("未知预演类型: %s", kind)
	}
	if res.Requests > 0 {
		res.OKPct = float64(res.OK) * 100 / float64(res.Requests)
	}
	if kind == "country" && res.IPs > 0 {
		res.IPs = int64(float64(res.IPs) / 300 * 60) // 粗估去重后的 1 分钟独立 IP
		if res.IPs < 1 {
			res.IPs = 1
		}
	}
	switch {
	case res.Requests == 0:
		res.Warning = "近 5 分钟没有该来源的请求，封禁收益为零"
	case res.OKPct >= 30:
		res.Warning = fmt.Sprintf("该来源近 5 分钟仍有 %.0f%% 正常请求（2xx），封禁很可能误伤真实用户", res.OKPct)
	default:
		res.Warning = "正常请求占比低，可以封禁"
	}
	return res, nil
}
