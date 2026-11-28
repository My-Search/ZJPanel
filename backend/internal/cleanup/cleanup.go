// Package cleanup 全局定时清理框架：各功能模块把「过期数据清理」实现为 Cleaner 注册进来，
// 由 boot 挂到共享 cron 统一调度（每日低峰跑一轮 + 启动补跑一次）。
// 后续其它功能需要清理（通知、指标、备份等）时直接实现 Cleaner 注册，不再各自起 goroutine 循环。
package cleanup

import (
	"log/slog"
	"sync"
	"time"
)

// Cleaner 一个可注册的清理单元：Name 用于日志定位；Cleanup 执行一轮清理，返回删除条数（0=本轮无删除）。
type Cleaner interface {
	Name() string
	Cleanup(now time.Time) (int, error)
}

// Manager 聚合全部 Cleaner，由 boot 注入共享 cron 统一调度。
type Manager struct {
	mu       sync.Mutex
	cleaners []Cleaner
}

func New() *Manager { return &Manager{} }

// Register 注册清理器（启动装配时调用）。
func (m *Manager) Register(c Cleaner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleaners = append(m.cleaners, c)
}

// RunAll 跑一轮全部清理器：单个失败不影响其余；无删除不打日志，避免每轮刷屏。
func (m *Manager) RunAll(now time.Time) {
	m.mu.Lock()
	list := append([]Cleaner(nil), m.cleaners...)
	m.mu.Unlock()
	for _, c := range list {
		n, err := c.Cleanup(now)
		if err != nil {
			slog.Warn("定时清理失败", "cleaner", c.Name(), "err", err)
			continue
		}
		if n > 0 {
			slog.Info("定时清理完成", "cleaner", c.Name(), "removed", n)
		}
	}
}

// Scheduler 共享 cron 的最小接口（boot 注入 cronWrap）。
type Scheduler interface {
	AddFunc(spec string, fn func()) any
}

// Start 挂共享 cron 定时执行；启动时异步补跑一次，弥补停机期间错过的清理。
func (m *Manager) Start(sched Scheduler, spec string) {
	if sched != nil && spec != "" {
		sched.AddFunc(spec, func() { m.RunAll(time.Now()) })
	}
	go m.RunAll(time.Now())
}
