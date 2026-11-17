package compose

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"zjpanel/backend/internal/model"
)

// ===================== Compose 操作后台任务 =====================
// up/down/restart 的异步执行：逐行广播到 WS 主题 compose-act-<projectId>（前端实时滚动显示），
// 行缓冲保留在内存，供快照接口补齐（WS 断连丢帧、页面刷新后恢复实时视图）。
// 每个项目仅保留最新一个任务，新任务自动覆盖旧任务。

const maxJobLines = 4000

// ActionJob 一次 compose 生命周期操作。元信息（ID/Project 等）发布前写定免锁，
// status/输出行等可变部分由 mu 保护，统一经 View() 对外读取。
type ActionJob struct {
	ID        string
	ProjectID uint
	Project   string
	Action    string
	StartedAt time.Time

	mu      sync.Mutex
	status  string    // running | success | error
	errStr  string
	endedAt time.Time
	lines   []string
	trimmed int // 因超上限被丢弃的行数（lines[0] 的全局行号 = trimmed）
}

// JobView 任务快照：元信息 + 自全局行号 Start 起的输出行
type JobView struct {
	ID        string    `json:"id"`
	ProjectID uint      `json:"projectId"`
	Project   string    `json:"project"`
	Action    string    `json:"action"`
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
func (j *ActionJob) View(since int) *JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := &JobView{
		ID: j.ID, ProjectID: j.ProjectID, Project: j.Project, Action: j.Action,
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

// push 追加一行输出并实时广播。idx 为全局行号，供前端按序去重、跳号时定向补拉。
func (j *ActionJob) push(s *Service, line string) {
	j.mu.Lock()
	idx := j.trimmed + len(j.lines)
	j.lines = append(j.lines, line)
	if len(j.lines) > maxJobLines {
		drop := len(j.lines) - maxJobLines
		j.lines = j.lines[drop:]
		j.trimmed += drop
	}
	j.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "log", "idx": idx, "line": line})
	s.hub.Broadcast(s.actionTopic(j.ProjectID), b)
}

// finish 置终态（幂等：仅 running 可转移）
func (j *ActionJob) finish(err error) {
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

func (j *ActionJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == "running"
}

// actionTopic 该项目操作任务的 WS 主题
func (s *Service) actionTopic(projectID uint) string {
	return fmt.Sprintf("compose-act-%d", projectID)
}

// StartAction 异步执行 compose 生命周期操作，立即返回任务；同项目已有执行中任务时拒绝。
// 实时输出经 WS 主题推送，最终结果以 done 帧与快照接口为准。
func (s *Service) StartAction(id uint, action string) (*ActionJob, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, id).Error; err != nil {
		return nil, fmt.Errorf("项目不存在")
	}
	args, err := s.actionArgs(&rec, action)
	if err != nil {
		return nil, err
	}
	s.jobsMu.Lock()
	if cur := s.jobs[id]; cur != nil && cur.running() {
		s.jobsMu.Unlock()
		return nil, fmt.Errorf("该项目已有任务在执行（%s），请等待完成后再操作", cur.Action)
	}
	name := rec.Name
	if name == "" {
		name = filepath.Base(rec.Dir)
	}
	job := &ActionJob{
		ID:        fmt.Sprintf("%d-%d", id, time.Now().UnixMilli()),
		ProjectID: id,
		Project:   name,
		Action:    action,
		StartedAt: time.Now(),
		status:    "running",
	}
	s.jobs[id] = job
	s.jobsMu.Unlock()
	go s.runAction(&rec, job, args)
	return job, nil
}

// LatestJob 项目最近一次操作任务（可能已结束；无任务返回 nil）
func (s *Service) LatestJob(id uint) *ActionJob {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	return s.jobs[id]
}

// runAction 执行 docker compose 命令并逐行推送输出；结束时广播 done 帧
func (s *Service) runAction(rec *model.ComposeProject, job *ActionJob, args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = rec.Dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		job.finish(err)
		s.broadcastDone(job)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		job.finish(err)
		s.broadcastDone(job)
		return
	}
	if err := cmd.Start(); err != nil {
		job.finish(fmt.Errorf("启动 docker 失败: %w", err))
		s.broadcastDone(job)
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.scanJobOutput(job, stdout) }()
	go func() { defer wg.Done(); s.scanJobOutput(job, stderr) }()
	wg.Wait()
	waitErr := cmd.Wait()
	if waitErr == nil && ctx.Err() != nil {
		waitErr = fmt.Errorf("执行超时（超过 5 分钟已终止）")
	}
	job.finish(waitErr)
	s.broadcastDone(job)
}

// scanJobOutput 逐行消费命令输出。按 \r 或 \n 切分——compose 非终端下拉镜像进度
// 用 \r 原地刷新，逐帧可见才能实时显示下载百分比。
func (s *Service) scanJobOutput(job *ActionJob, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(splitCRLF)
	for sc.Scan() {
		job.push(s, sc.Text())
	}
}

// splitCRLF bufio.Scanner 切分函数：\r / \n 均为行界（push 会跳过 \r\n 相邻产生的空行）
func splitCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// broadcastDone 任务终帧：前端以本帧触发最终快照定稿与界面刷新
func (s *Service) broadcastDone(job *ActionJob) {
	v := job.View(0)
	b, _ := json.Marshal(map[string]any{"type": "done", "ok": v.Status == "success", "error": v.Error})
	s.hub.Broadcast(s.actionTopic(job.ProjectID), b)
}
