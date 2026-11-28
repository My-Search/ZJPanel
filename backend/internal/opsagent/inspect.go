package opsagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/notify"
	"zjpanel/backend/internal/pkg"
)

// 定时巡检：crontab 触发的智能体巡检任务。到点创建一个 inspect 会话，
// 智能体按巡检项用面板工具自查并输出报告（可发通知）；配置存 Setting（opsagent.inspects），
// 调度走 boot 注入的共享 cron（与定时备份同一范式），保存即重排、无需重启。

// InspectItem 巡检项目录（UI 勾选清单与指令渲染共用）。
var InspectCatalog = []InspectItem{
	{Key: "security", Title: "安全态势", Hint: "security_overview 看健康分与活跃严重事件，异常时用 security_events 查明细"},
	{Key: "hosts-defense", Title: "主机防御基线", Hint: "先 host_list 列出主机，逐台 host_check(kind) 做基线检查，重点关注 ssh/exposure/webshell"},
	{Key: "certs", Title: "证书到期", Hint: "list_cert_tasks 检查已失效/即将到期（<30 天）的任务与最近签发失败"},
	{Key: "sites", Title: "站点可用性", Hint: "list_sites 检查站点探测状态、异常码与高延迟"},
	{Key: "containers", Title: "容器状态", Hint: "list_containers 检查非 running 的容器与频繁重启的实例"},
	{Key: "resources", Title: "主机资源水位", Hint: "host_metrics 逐台检查 CPU/内存/磁盘水位，超 85% 标记风险"},
	{Key: "backups", Title: "备份状态", Hint: "backup_status / backup_list 确认最近一次备份成功且距今不超过备份周期"},
}

type InspectItem struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Hint  string `json:"hint"`
}

// InspectTask 一个巡检任务。
type InspectTask struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Cron    string   `json:"cron"`  // 标准 5 段 crontab
	Items   []string `json:"items"` // InspectCatalog key 子集
	HostID  *uint    `json:"hostId"` // 可选：绑定主机（主机类检查项只巡这一台）
	Enabled bool     `json:"enabled"`
}

// InspectView 列表视图：附带下次执行时间。
type InspectView struct {
	InspectTask
	Next time.Time `json:"next"`
}

// CronScheduler 面板共享 cron 的最小接口（boot 注入 cronWrap，见 boot/run.go）。
type CronScheduler interface {
	AddFunc(spec string, fn func()) any
	RemoveByID(id any)
	NextByID(id any) time.Time
}

const settingInspectKey = "opsagent.inspects"

func (s *Service) loadInspects() []InspectTask {
	var rec model.Setting
	if err := s.db.Where("key = ?", settingInspectKey).First(&rec).Error; err != nil {
		return []InspectTask{}
	}
	var list []InspectTask
	if json.Unmarshal([]byte(rec.Value), &list) != nil {
		return []InspectTask{}
	}
	return list
}

func (s *Service) saveInspects(list []InspectTask) error {
	b, _ := json.Marshal(list)
	if err := s.db.Save(&model.Setting{Key: settingInspectKey, Value: string(b)}).Error; err != nil {
		return err
	}
	s.rescheduleInspects()
	return nil
}

// InspectViews 任务列表 + 下次执行时间。
func (s *Service) InspectViews() []InspectView {
	list := s.loadInspects()
	out := make([]InspectView, 0, len(list))
	for _, t := range list {
		v := InspectView{InspectTask: t}
		if s.sched != nil {
			v.Next = s.sched.NextByID(s.inspectEntries[t.ID])
		}
		out = append(out, v)
	}
	return out
}

// SaveInspect 新建/更新巡检任务（校验 cron 与巡检项）并重排调度。
func (s *Service) SaveInspect(t InspectTask) error {
	t.Name = strings.TrimSpace(t.Name)
	t.Cron = strings.TrimSpace(t.Cron)
	if t.Name == "" {
		return fmt.Errorf("任务名称必填")
	}
	if _, err := cron.ParseStandard(t.Cron); err != nil {
		return fmt.Errorf("cron 表达式非法: %v", err)
	}
	if len(t.Items) == 0 {
		return fmt.Errorf("至少勾选一个巡检项")
	}
	valid := map[string]bool{}
	for _, it := range InspectCatalog {
		valid[it.Key] = true
	}
	for _, k := range t.Items {
		if !valid[k] {
			return fmt.Errorf("未知巡检项: %s", k)
		}
	}
	list := s.loadInspects()
	if t.ID == "" {
		t.ID = "in" + pkg.RandomHex(4)
		list = append(list, t)
	} else {
		replaced := false
		for i := range list {
			if list[i].ID == t.ID {
				list[i] = t
				replaced = true
				break
			}
		}
		if !replaced {
			return fmt.Errorf("任务不存在")
		}
	}
	return s.saveInspects(list)
}

// DeleteInspect 删除任务并摘除调度。
func (s *Service) DeleteInspect(id string) error {
	list := s.loadInspects()
	kept := list[:0]
	found := false
	for _, t := range list {
		if t.ID == id {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return fmt.Errorf("任务不存在")
	}
	return s.saveInspects(kept)
}

// RunInspectNow 立即执行一次巡检（手动触发，不改动调度）。
func (s *Service) RunInspectNow(id string) error {
	for _, t := range s.loadInspects() {
		if t.ID == id {
			return s.runInspect(t)
		}
	}
	return fmt.Errorf("任务不存在")
}

// StartInspectScheduler 注入共享 cron 并按当前配置排布（boot 调用一次）。
func (s *Service) StartInspectScheduler(sched CronScheduler) {
	s.sched = sched
	s.inspectEntries = map[string]any{}
	s.rescheduleInspects()
}

// rescheduleInspects 全量重排：先摘除本域全部 entry，再为启用任务重新注册。
func (s *Service) rescheduleInspects() {
	if s.sched == nil {
		return
	}
	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	for id, entry := range s.inspectEntries {
		s.sched.RemoveByID(entry)
		delete(s.inspectEntries, id)
	}
	for _, t := range s.loadInspects() {
		if !t.Enabled {
			continue
		}
		task := t
		entry := s.sched.AddFunc(task.Cron, func() { s.cronInspect(task) })
		s.inspectEntries[task.ID] = entry
	}
}

// cronInspect cron 到点触发：失败/跳过走节流通知（不返回错误造成静默）。
func (s *Service) cronInspect(task InspectTask) {
	if err := s.runInspect(task); err != nil {
		s.notifyThrottled("inspect:"+task.ID, "定时巡检未能执行",
			fmt.Sprintf("巡检任务「%s」触发失败：%s", task.Name, err.Error()), 30*time.Minute)
	}
}

// runInspect 创建巡检会话并启动（LLM 未配置/并发满时返回错误，由调用方决定提示方式）。
func (s *Service) runInspect(task InspectTask) error {
	if _, err := s.LLMDetail(); err != nil {
		return err
	}
	if s.RunningCount() >= int64(maxConcurrent) {
		return fmt.Errorf("并发会话已达上限（%d）", maxConcurrent)
	}
	var hostID *uint
	if task.HostID != nil && *task.HostID > 0 {
		hostID = task.HostID
	}
	sess, err := s.CreateSession("巡检: "+task.Name, "inspect", hostID, "event", renderInspect(task))
	if err != nil {
		return err
	}
	s.broadcast(map[string]any{"type": "session", "session": *sess})
	if err := s.StartRunner(sess); err != nil {
		s.finish(sess.ID, "error", err.Error())
		return err
	}
	return nil
}

// notifyThrottled 同 key 节流的统一通知（巡检触发失败等场景，避免每次都打扰）。
func (s *Service) notifyThrottled(key, title, body string, d time.Duration) {
	if v, ok := s.throttle.Load(key); ok {
		if time.Since(v.(time.Time)) < d {
			return
		}
	}
	s.throttle.Store(key, time.Now())
	if s.notify != nil {
		s.notify.Send("opsagent", notify.Input{Type: "opsagent.inspect", Level: "warn", Title: title, Body: body})
	}
}
