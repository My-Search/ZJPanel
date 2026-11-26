package security

import (
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// 本地/白名单来源不参与攻击判定：面板自身、站点探测、同机运维流量都从 127.0.0.1 进 nginx，
// 若计入 CC/扫描/登录爆破判定，正常运维操作会被自己的面板判成攻击（见 v1.0.19 实测截图）。
// 这里把回环段做成内置可信段，再叠加用户白名单（IP 或 CIDR）。

var builtinTrusted = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
}

type trustedSet struct {
	prefixes []netip.Prefix
}

// buildTrusted 从策略白名单构建匹配器：IP 视为单地址前缀，CIDR 原样使用。
func buildTrusted(whitelist []string) *trustedSet {
	ts := &trustedSet{prefixes: append([]netip.Prefix(nil), builtinTrusted...)}
	for _, w := range whitelist {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if p, err := netip.ParsePrefix(w); err == nil {
			ts.prefixes = append(ts.prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(w); err == nil {
			a = a.Unmap()
			ts.prefixes = append(ts.prefixes, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return ts
}

func (t *trustedSet) contains(ip string) bool {
	if t == nil {
		return false
	}
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// setTrusted 重建可信来源匹配器（策略加载/保存时调用）。读侧无锁，避免每条请求抢策略锁。
func (s *Service) setTrusted(whitelist []string) {
	s.trusted.Store(buildTrusted(whitelist))
}

// isTrusted 判断来源是否为本机回环或白名单地址。
func (s *Service) isTrusted(ip string) bool {
	return s.trusted.Load().contains(ip)
}

// ignoredSites 停用应用关联网站域名集合（站点级过滤，与来源 IP 无关）。
type ignoredSites struct {
	m map[string]bool
}

// SetIgnoredSites 注入停用站点来源（compose.Service.DisabledSiteDomains），并立即预热。
func (s *Service) SetIgnoredSites(fn func() map[string]bool) {
	s.ignoredMu.Lock()
	s.ignoredFn = fn
	s.ignoredMu.Unlock()
	s.refreshIgnored(time.Now())
}

// refreshIgnored 每 10s 刷新一次停用站点集合：启用/停用应用后最迟 10s 生效，
// 不按每条日志查库（秒级日志量下会把 SQLite 读成瓶颈）。
func (s *Service) refreshIgnored(now time.Time) {
	s.ignoredMu.Lock()
	fn := s.ignoredFn
	if fn == nil || now.Sub(s.lastIgnored) < 10*time.Second {
		s.ignoredMu.Unlock()
		return
	}
	s.lastIgnored = now
	s.ignoredMu.Unlock()

	m := fn()
	if m == nil {
		m = map[string]bool{}
	}
	s.ignored.Store(&ignoredSites{m: m})
}

func (s *Service) isIgnoredSite(domain string) bool {
	p := s.ignored.Load()
	return p != nil && p.m[domain]
}

// 编译期确认 atomic 用法正确（atomic.Pointer 零值可用）。
var (
	_ atomic.Pointer[trustedSet]
	_ atomic.Pointer[ignoredSites]
)
