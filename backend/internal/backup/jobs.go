package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// 备份任务：把创建/上传/下载/恢复等分钟级操作移出 HTTP 请求生命周期，
// 前端经 WS 主题 backup-job 实时看进度、快照接口断线补帧（参照 compose/jobs.go 模式）。
// 全局同一时刻只允许一个任务：打包与恢复会写同一批目录，互斥最简单也最安全。

const (
	jobTopic        = "backup-job"
	maxJobLines     = 4000 // 单任务日志行缓冲上限
	keepJobs        = 20   // 内存保留的历史任务数
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

// 任务类型。
const (
	JobCreate   = "create"
	JobUpload   = "upload"
	JobDownload = "download"
	JobRestore  = "restore"
)

// BackupJob 一次备份/恢复任务。元信息（ID/Kind 等）发布前写定免锁，
// status/输出行等可变部分由 mu 保护，统一经 View() 对外读取。
type BackupJob struct {
	ID        string
	Kind      string // create | upload | download | restore
	Target    string // 备份包名（上传/下载）或勾选项摘要（创建/恢复）
	Trigger   string // manual | schedule
	StartedAt time.Time

	mu      sync.Mutex
	status  string // running | success | error
	errStr  string
	endedAt time.Time
	lines   []string
	trimmed int // 因超上限被丢弃的行数（lines[0] 的全局行号 = trimmed）
}

// JobView 任务快照：元信息 + 自全局行号 Since 起的输出行。
type JobView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Total     int       `json:"total"` // 当前全局总行数（= 下一行行号）
	Start     int       `json:"start"` // Lines[0] 的全局行号
	Since     int       `json:"since"` // 请求的起点
	Lines     []string  `json:"lines"`
}

// View 生成快照。since 为调用方已持有的全局行号；若落在被丢弃区段则返回现存全部行。
func (j *BackupJob) View(since int) *JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := &JobView{
		ID: j.ID, Kind: j.Kind, Target: j.Target, Trigger: j.Trigger,
		Status: j.status, Error: j.errStr, StartedAt: j.StartedAt, EndedAt: j.endedAt,
		Total: j.trimmed + len(j.lines), Since: since, Lines: []string{},
	}
	start := since - j.trimmed
	if start < 0 {
		start = 0
	}
	if start > len(j.lines) {
		start = len(j.lines)
	}
	v.Start = j.trimmed + start
	v.Lines = append(v.Lines, j.lines[start:]...)
	return v
}

func (j *BackupJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == "running"
}

// finish 置终态（幂等：仅 running 可转移）。
func (j *BackupJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != "running" {
		return
	}
	if err != nil {
		j.status = "error"
		j.errStr = err.Error()
	} else {
		j.status = "success"
	}
	j.endedAt = time.Now()
}

// push 追加一行输出并实时广播。idx 为全局行号，供前端按序去重、跳号时定向补拉。
func (j *BackupJob) push(s *Service, line string) {
	j.mu.Lock()
	idx := j.trimmed + len(j.lines)
	j.lines = append(j.lines, line)
	if len(j.lines) > maxJobLines {
		drop := len(j.lines) - maxJobLines
		j.lines = j.lines[drop:]
		j.trimmed += drop
	}
	j.mu.Unlock()
	if s.hub != nil {
		b, _ := json.Marshal(map[string]any{"type": "log", "idx": idx, "line": line})
		s.hub.Broadcast(jobTopic, b)
	}
}

// step 返回给打包/上传等长流程的进度回调：每条进度进入任务日志。
func (j *BackupJob) step(s *Service) func(string) {
	return func(line string) { j.push(s, line) }
}

// LatestJob 最近一个任务（无则 nil）。
func (s *Service) LatestJob() *BackupJob {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if len(s.jobs) == 0 {
		return nil
	}
	return s.jobs[0]
}

// JobSummaries 历史任务列表（新→旧，不含日志行）。
func (s *Service) JobSummaries() []JobView {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	out := make([]JobView, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j.View(0))
	}
	return out
}

// startJob 注册并异步执行任务；已有任务运行中时拒绝。fn 返回 error 即任务失败。
func (s *Service) startJob(kind, target, trigger string, fn func(j *BackupJob) error) (*BackupJob, error) {
	s.activeMu.Lock()
	if s.active {
		s.activeMu.Unlock()
		return nil, errors.New("已有备份任务在执行中，请等待完成后再操作")
	}
	s.active = true
	s.activeMu.Unlock()

	job := &BackupJob{
		ID:        fmt.Sprintf("%s-%d", kind, time.Now().UnixMilli()),
		Kind:      kind,
		Target:    target,
		Trigger:   trigger,
		StartedAt: time.Now(),
		status:    "running",
	}
	s.jobsMu.Lock()
	s.jobs = append([]*BackupJob{job}, s.jobs...)
	if len(s.jobs) > keepJobs {
		s.jobs = s.jobs[:keepJobs]
	}
	s.jobsMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				job.finish(fmt.Errorf("内部错误: %v", r))
			}
			s.jobDone(job)
			s.activeMu.Lock()
			s.active = false
			s.activeMu.Unlock()
		}()
		err := fn(job)
		job.finish(err)
		if err != nil {
			job.push(s, "任务失败: "+err.Error())
		}
	}()
	return job, nil
}

// jobDone 广播终帧并发布面板事件（notify 订阅后站内信/webhook 联动；
// 手动任务成功不打扰——操作者正在看进度页，只有定时成功与一切失败才进通知）。
func (s *Service) jobDone(j *BackupJob) {
	if s.hub != nil {
		v := j.View(0)
		b, _ := json.Marshal(map[string]any{
			"type": "done", "ok": v.Status == "success", "error": v.Error, "kind": j.Kind, "id": j.ID,
		})
		s.hub.Broadcast(jobTopic, b)
	}
	if s.bus == nil {
		return
	}
	v := j.View(0)
	s.bus.Publish("backup.done", map[string]any{
		"ok": v.Status == "success", "kind": j.Kind, "name": j.Target,
		"trigger": j.Trigger, "error": v.Error,
	})
}

// StartBuild 异步创建备份包。定时触发时串联自动上传与保留清理（见 afterScheduledBuild）。
func (s *Service) StartBuild(items []string, trigger string) (*BackupJob, error) {
	items = NormalizeItems(items)
	return s.startJob(JobCreate, strings.Join(items, ","), trigger, func(j *BackupJob) error {
		rec, err := s.Build(items, j.step(s))
		if err != nil {
			return err
		}
		j.push(s, fmt.Sprintf("备份完成：%s（%s）", rec.Name, humanBytes(rec.Size)))
		if trigger == TriggerSchedule {
			return s.afterScheduledBuild(j, rec)
		}
		return nil
	})
}

// StartUpload 异步上传备份包到远程存储。
func (s *Service) StartUpload(name string, trigger string) (*BackupJob, error) {
	if _, err := s.OpenPath(name); err != nil {
		return nil, err
	}
	return s.startJob(JobUpload, name, trigger, func(j *BackupJob) error {
		return s.UploadToRemote(name, j.step(s))
	})
}

// StartDownload 异步从远程存储拉取备份包到本地。
func (s *Service) StartDownload(name string) (*BackupJob, error) {
	if !isSafeName(name) {
		return nil, os.ErrInvalid
	}
	return s.startJob(JobDownload, name, TriggerManual, func(j *BackupJob) error {
		return s.DownloadFromRemote(name, j.step(s))
	})
}

// StartRestore 异步执行恢复（临时包用完即删）。恢复直接覆盖现有文件，
// 完成后需要重启面板 / 重载 nginx 才完全生效，日志里会提醒。
func (s *Service) StartRestore(tempPath string, items []string) (*BackupJob, error) {
	items = NormalizeItems(items)
	return s.startJob(JobRestore, strings.Join(items, ","), TriggerManual, func(j *BackupJob) error {
		defer s.RemoveTemp(tempPath)
		j.push(s, "开始恢复：将覆盖现有数据库/配置/应用目录，完成后建议重启面板并重载 nginx")
		res, err := s.Restore(tempPath, items, j.step(s))
		if err != nil {
			return err
		}
		for _, e := range res.Errors {
			j.push(s, "恢复出错: "+e)
		}
		j.push(s, fmt.Sprintf("恢复结束：写回 %d 个文件，跳过 %d 项，失败 %d 处", len(res.Restored), len(res.Skipped), len(res.Errors)))
		return nil
	})
}

// humanBytes 人类可读的字节数（日志展示用）。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
