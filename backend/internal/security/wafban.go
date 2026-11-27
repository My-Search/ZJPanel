package security

import (
	"strings"
	"time"
)

// BanRequest 规则引擎（WAF）批量封禁请求。
type BanRequest struct {
	IP      string
	Minutes int // <=0 视为永久（WAF 阶梯封禁不会传永久）
	Reason  string
}

// BanAuto 批量下发自动封禁。
//
// 与逐条 BlockIP 的区别：同一批只在最后保存一次策略、触发一次 nginx reload。
// 攻击期间一条命中一次 reload 会把 nginx 自己拖垮，这也是这里必须批量的原因。
// 合并规则：同一 IP 已有更长剩余封禁则跳过，否则延长到期时间并刷新原因。
func (s *Service) BanAuto(reqs []BanRequest) (int, error) {
	if len(reqs) == 0 {
		return 0, nil
	}
	now := time.Now()
	s.polMu.Lock()
	wl := append([]string(nil), s.pol.Whitelist...)
	exists := map[string]int{}
	for i, b := range s.pol.BlockIPs {
		exists[b.Value] = i
	}
	added := 0
	changed := false
	for _, r := range reqs {
		ip := strings.TrimSpace(r.IP)
		if ip == "" || ipInList(ip, wl) {
			continue
		}
		if err := validIPOrCIDR(ip); err != nil {
			continue
		}
		var until time.Time
		if r.Minutes > 0 {
			until = now.Add(time.Duration(r.Minutes) * time.Minute)
		}
		if idx, ok := exists[ip]; ok {
			old := s.pol.BlockIPs[idx]
			if old.Until.IsZero() {
				continue // 已有人工永久封禁，自动封禁不能把它缩短成临时封禁
			}
			if until.IsZero() || old.Until.After(until) {
				continue // 已有更长的封禁，不用改
			}
			s.pol.BlockIPs[idx].Until = until
			s.pol.BlockIPs[idx].Reason = r.Reason
			changed = true
			continue
		}
		s.pol.BlockIPs = append(s.pol.BlockIPs, Block{
			Value: ip, Until: until, Reason: r.Reason, CreatedAt: now, Auto: true,
		})
		exists[ip] = len(s.pol.BlockIPs) - 1
		added++
		changed = true
	}
	if !changed {
		s.polMu.Unlock()
		return 0, nil
	}
	p := *s.pol
	p.UpdatedAt = now
	s.polMu.Unlock()

	if err := s.savePolicy(&p); err != nil {
		return added, err
	}
	_, err := s.applyPolicy()
	return added, err
}
