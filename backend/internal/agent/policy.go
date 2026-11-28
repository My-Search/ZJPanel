// 主机主动防御策略：全局默认一份（存 Setting agent.defense），Agent 上线即下发。
//
// 策略在面板、检测在 Agent（design-host-agent.md 硬原则）：Agent 把策略持久化在
// /var/lib/zjagent/defense.json，断网按最后下发的策略继续检测与封禁。
package agent

import (
	"encoding/json"
	"strings"
	"time"

	"zjpanel/backend/internal/model"
)

const defenseSettingKey = "agent.defense"

// HostDefensePolicy 与 zjagent 侧 defensePolicy 的 JSON 结构保持一致。
type HostDefensePolicy struct {
	Enabled    bool     `json:"enabled"`
	SSH        SSHRules `json:"ssh"`
	Whitelist  []string `json:"whitelist"`  // IP 或 CIDR，永不封禁
	BanPrivate bool     `json:"banPrivate"` // 是否允许封私网段（内网横向移动场景才开启）
}

// SSHRules SSH 爆破检测参数。
type SSHRules struct {
	WindowSecs int  `json:"windowSecs"` // 滑动窗口秒数
	MaxFails   int  `json:"maxFails"`   // 窗口内失败次数阈值
	BanSeconds int  `json:"banSeconds"` // 封禁时长（到期自动解封）
	AutoBan    bool `json:"autoBan"`    // 达阈值本地自动封禁
}

// DefaultDefensePolicy 新部署/未配置时的默认值（fail2ban 式，私网默认保护）。
func DefaultDefensePolicy() HostDefensePolicy {
	return HostDefensePolicy{
		Enabled: true,
		SSH:     SSHRules{WindowSecs: 600, MaxFails: 10, BanSeconds: 3600, AutoBan: true},
	}
}

// GetDefensePolicy 读当前策略（未配置时返回默认值）。
func (s *Service) GetDefensePolicy() HostDefensePolicy {
	pol := DefaultDefensePolicy()
	var rec model.Setting
	if err := s.db.Where("`key` = ?", defenseSettingKey).First(&rec).Error; err == nil {
		if json.Unmarshal([]byte(rec.Value), &pol) != nil {
			pol = DefaultDefensePolicy()
		}
	}
	return sanitizeDefensePolicy(pol)
}

// SaveDefensePolicy 保存策略并推送全部在线主机，返回推送成功数。
func (s *Service) SaveDefensePolicy(pol HostDefensePolicy) (int, error) {
	pol = sanitizeDefensePolicy(pol)
	b, err := json.Marshal(pol)
	if err != nil {
		return 0, err
	}
	if err := s.db.Save(&model.Setting{Key: defenseSettingKey, Value: string(b)}).Error; err != nil {
		return 0, err
	}
	return s.PushDefensePolicy(0), nil
}

// PushDefensePolicy 下发策略：hostID>0 指定主机，否则全部在线主机。返回推送成功数。
// 离线主机不必入队：上线握手时 onHello 会重新推送，避免队列里堆积过期策略。
func (s *Service) PushDefensePolicy(hostID uint) int {
	pol := s.GetDefensePolicy()
	params, _ := json.Marshal(pol)
	var m map[string]any
	if json.Unmarshal(params, &m) != nil {
		return 0
	}
	count := 0
	push := func(id uint) {
		if !s.Online(id) {
			return
		}
		if _, err := s.Exec(id, "defense_policy", m, 15*time.Second); err == nil {
			count++
		}
	}
	if hostID > 0 {
		push(hostID)
		return count
	}
	s.mu.Lock()
	ids := make([]uint, 0, len(s.conns))
	for id := range s.conns {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		push(id)
	}
	return count
}

// sanitizeDefensePolicy 钳位非法参数（与 zjagent 侧 applyPolicy 同口径）。
func sanitizeDefensePolicy(pol HostDefensePolicy) HostDefensePolicy {
	if pol.SSH.WindowSecs <= 0 {
		pol.SSH.WindowSecs = 600
	}
	pol.SSH.WindowSecs = clampInt(pol.SSH.WindowSecs, 60, 24*3600)
	pol.SSH.MaxFails = clampInt(pol.SSH.MaxFails, 3, 1000)
	pol.SSH.BanSeconds = clampInt(pol.SSH.BanSeconds, 60, 7*24*3600)
	seen := map[string]bool{}
	out := []string{}
	for _, w := range pol.Whitelist {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	pol.Whitelist = out
	return pol
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
