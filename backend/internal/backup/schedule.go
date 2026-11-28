package backup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"zjpanel/backend/internal/model"
)

// 定时备份：配置存 Setting 表（backup.schedule），调度走 boot 注入的共享 cron。
// 定时任务在同一任务里串联「打包 →（可选）自动上传云盘 → 保留清理」，结果经
// bus.Publish("backup.done") 通知（notify 订阅后站内信/webhook 联动）。

const (
	settingScheduleKey  = "backup.schedule"
	defaultScheduleCron = "0 3 * * *" // 每天 03:00
	defaultScheduleKeep = 10          // 默认保留最近 10 份定时备份
)

// ScheduleLastRun 最近一次定时备份的结果（设置页展示）。
type ScheduleLastRun struct {
	Time  time.Time `json:"time"`
	OK    bool      `json:"ok"`
	Name  string    `json:"name"`
	Error string    `json:"error,omitempty"`
}

// ScheduleConfig 定时备份配置（持久化为 Setting 行，无敏感字段）。
type ScheduleConfig struct {
	Enabled      bool             `json:"enabled"`
	Cron         string           `json:"cron"`
	Items        []string         `json:"items"`
	UploadRemote bool             `json:"uploadRemote"` // 打包成功后自动上传到已授权的远程存储
	Keep         int              `json:"keep"`         // 保留份数（仅清理定时产物，0 = 不清理）
	LastRun      *ScheduleLastRun `json:"lastRun,omitempty"`
}

// ScheduleView 返回给前端的配置 + 下次执行时间。
type ScheduleView struct {
	ScheduleConfig
	NextRun time.Time `json:"nextRun"`
}

// Scheduler 面板共享 cron 的最小接口（boot 注入 cronWrap，见 boot/run.go）。
type Scheduler interface {
	AddFunc(spec string, fn func()) any
	RemoveByID(id any)
	NextByID(id any) time.Time
}

// loadSchedule 读取定时配置并归一（缺失/损坏时回退默认值）。
func (s *Service) loadSchedule() ScheduleConfig {
	sc := ScheduleConfig{Cron: defaultScheduleCron, Items: append([]string(nil), AllItems...), Keep: defaultScheduleKeep}
	var v model.Setting
	if err := s.db.Where("key = ?", settingScheduleKey).First(&v).Error; err != nil {
		return sc
	}
	if json.Unmarshal([]byte(v.Value), &sc) != nil {
		sc = ScheduleConfig{Cron: defaultScheduleCron, Items: append([]string(nil), AllItems...), Keep: defaultScheduleKeep}
	}
	sc.Cron = strings.TrimSpace(sc.Cron)
	sc.Items = NormalizeItems(sc.Items)
	sc.Keep = clampKeep(sc.Keep)
	return sc
}

func clampKeep(n int) int {
	if n < 0 {
		return 0
	}
	if n > 999 {
		return 999
	}
	return n
}

// persistSchedule 原样落库（调用方负责 LastRun 保留）。
func (s *Service) persistSchedule(sc ScheduleConfig) error {
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return s.db.Save(&model.Setting{Key: settingScheduleKey, Value: string(b)}).Error
}

// ScheduleView 当前配置 + 下次执行时间。
func (s *Service) ScheduleView() *ScheduleView {
	s.schedMu.Lock()
	sched, id := s.sched, s.schedID
	s.schedMu.Unlock()
	v := &ScheduleView{ScheduleConfig: s.loadSchedule()}
	if sched != nil && id != nil && v.Enabled {
		v.NextRun = sched.NextByID(id)
	}
	return v
}

// SaveSchedule 校验并保存定时配置；调度器可用时立即重排（cert scheduleTask 同款模式）。
func (s *Service) SaveSchedule(sc ScheduleConfig) (*ScheduleView, error) {
	sc.Cron = strings.TrimSpace(sc.Cron)
	if sc.Cron == "" {
		sc.Cron = defaultScheduleCron
	}
	if _, err := cron.ParseStandard(sc.Cron); err != nil {
		return nil, fmt.Errorf("cron 表达式无效: %v", err)
	}
	sc.Items = NormalizeItems(sc.Items)
	sc.Keep = clampKeep(sc.Keep)
	// LastRun 属于运行状态，不随前端回传覆盖。
	sc.LastRun = s.loadSchedule().LastRun
	if err := s.persistSchedule(sc); err != nil {
		return nil, err
	}
	s.reschedule(sc)
	slog.Info("定时备份配置已保存", "enabled", sc.Enabled, "cron", sc.Cron, "keep", sc.Keep, "uploadRemote", sc.UploadRemote)
	return s.ScheduleView(), nil
}

// StartScheduler 注入共享 cron 并按已存配置注册任务（boot 启动时调用一次）。
func (s *Service) StartScheduler(sched Scheduler) {
	s.schedMu.Lock()
	s.sched = sched
	s.schedMu.Unlock()
	s.reschedule(s.loadSchedule())
}

// reschedule 按 cfg 重排定时任务（加锁；sched 未注入时仅跳过）。
func (s *Service) reschedule(sc ScheduleConfig) {
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.sched == nil {
		return
	}
	if s.schedID != nil {
		s.sched.RemoveByID(s.schedID)
		s.schedID = nil
	}
	if !sc.Enabled {
		return
	}
	s.schedID = s.sched.AddFunc(sc.Cron, s.runScheduled)
}

// runScheduled cron 触发入口：已有任务执行中则本次跳过（不排队，下个周期再试）。
func (s *Service) runScheduled() {
	cfg := s.loadSchedule()
	if !cfg.Enabled {
		return
	}
	if _, err := s.StartBuild(cfg.Items, TriggerSchedule); err != nil {
		slog.Info("定时备份本轮跳过", "reason", err)
	}
}

// afterScheduledBuild 定时任务收尾：自动上传 → 保留清理 → 记录 lastRun。
// 打包本身已成功，上传失败不影响本地备份可用性，仅作为任务错误上报。
func (s *Service) afterScheduledBuild(j *BackupJob, rec *BackupRecord) error {
	cfg := s.loadSchedule()
	var uploadErr error
	if cfg.UploadRemote {
		j.push(s, "定时任务：自动上传到远程存储")
		if err := s.UploadToRemote(rec.Name, j.step(s)); err != nil {
			uploadErr = err
			j.push(s, "自动上传失败: "+err.Error())
		} else {
			j.push(s, "自动上传完成")
		}
	}
	if cfg.Keep > 0 {
		if n := s.pruneKeep(cfg.Keep); n > 0 {
			j.push(s, fmt.Sprintf("已按保留 %d 份清理 %d 个旧备份", cfg.Keep, n))
		}
	}
	s.saveLastRun(rec.Name, uploadErr)
	return uploadErr
}

// saveLastRun 更新最近一次定时备份结果（尽力而为，失败只影响设置页展示）。
func (s *Service) saveLastRun(name string, jobErr error) {
	sc := s.loadSchedule()
	sc.LastRun = &ScheduleLastRun{Time: time.Now(), OK: jobErr == nil, Name: name}
	if jobErr != nil {
		sc.LastRun.Error = jobErr.Error()
	}
	if err := s.persistSchedule(sc); err != nil {
		slog.Warn("定时备份结果记录失败", "err", err)
	}
}

// pruneKeep 按保留份数清理最旧的本地备份（只动 zjpanel-backup-* 前缀的定时/手动产物，
// restore-*.tar.gz 临时包与外来文件不碰）。返回清理数量。
func (s *Service) pruneKeep(keep int) int {
	list, err := s.List()
	if err != nil {
		return 0
	}
	removed := 0
	seen := 0
	for _, rec := range list {
		if !strings.HasPrefix(rec.Name, "zjpanel-backup-") {
			continue
		}
		seen++
		if seen <= keep {
			continue
		}
		if err := s.Delete(rec.Name); err == nil {
			removed++
		}
	}
	return removed
}
