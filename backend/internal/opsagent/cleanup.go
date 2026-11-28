package opsagent

import (
	"log/slog"
	"time"

	"zjpanel/backend/internal/cleanup"
	"zjpanel/backend/internal/model"
)

// SessionCleaner 会话保留期清理器：注册进全局 cleanup.Manager（见 boot/run.go），每日检查到期会话并删除。
// 保留天数每次执行时读 AgentConfig（改设置即时生效），0=永久；活跃会话（running/waiting_confirm）不清理。
func (s *Service) SessionCleaner() cleanup.Cleaner { return sessionCleaner{s} }

type sessionCleaner struct{ s *Service }

func (sessionCleaner) Name() string { return "opsagent.sessions" }

func (c sessionCleaner) Cleanup(now time.Time) (int, error) {
	days := c.s.AgentConfig().SessionRetentionDays
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.AddDate(0, 0, -days)
	// 只摘已到期的会话 ID；单轮限量 500，积压时分多天消化，避免一次性巨删拖慢共享 cron
	var ids []uint
	if err := c.s.db.Model(&model.OpsAgentSession{}).
		Where("updated_at < ? AND status NOT IN ?", cutoff, []string{"running", "waiting_confirm"}).
		Limit(500).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	removed := 0
	for _, id := range ids {
		// 复用 DeleteSession：运行中的会话会被拒绝（摘取与删除之间恰好恢复运行时跳过），并级联删消息
		if err := c.s.DeleteSession(id); err != nil {
			slog.Warn("清理到期会话失败", "sessionId", id, "err", err)
			continue
		}
		removed++
	}
	return removed, nil
}
