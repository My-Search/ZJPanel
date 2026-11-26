// Package cronjobs 内置插件：定时任务管理。
// 能力：任务 CRUD（名称 + 5 段 cron + shell 命令）、启停、手动执行、超时控制、
// 按 cron 表达式自动调度（复用面板 robfig/cron）、执行日志（环形保留最近 20 次）、
// 下次执行时间展示、绑定终端 SSH 主机远端执行（sshHostId，0=面板本机）。
// API 挂 /api/plugin/cronjobs/*（面板登录态）。
package cronjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	sdk "zjpanel/backend/sdk/plugin"
)

const taskFile = "tasks.json"

type Task struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Cron      string `json:"cron"`
	Command   string `json:"command"`
	WorkDir   string `json:"workDir,omitempty"`
	SSHHostID uint   `json:"sshHostId,omitempty"` // >0 = 绑定终端 SSH 主机远端执行；0 = 面板本机
	Timeout   int    `json:"timeout"`             // 秒；0/负数 = 3600
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
}

type RunLog struct {
	RunID      string `json:"runId,omitempty"`
	TaskID     string `json:"taskId"`
	StartAt    string `json:"startAt"`
	Duration   string `json:"duration"`
	DurationMs int64  `json:"durationMs,omitempty"`
	ExitCode   int    `json:"exitCode"`
	Timeout    bool   `json:"timeout"`
	Status     string `json:"status,omitempty"` // running | success | failed | timeout（旧记录为空按退出码判定）
	Target     string `json:"target,omitempty"` // 执行目标：空=本机；ssh:<主机ID>
	Output     string `json:"output"`
}

// liveRun 一次执行（进行中或刚结束）：buf 是实时输出缓冲，SSE/轮询从这里读。
// 所有可变字段由 Plugin.mu 保护。
type liveRun struct {
	RunID    string
	TaskID   string
	StartAt  string
	Target   string
	started  time.Time
	buf      []byte
	status   string
	exitCode int
	timeout  bool
	subs     map[chan struct{}]struct{}
	done     chan struct{}
	closed   bool
}

func (r *liveRun) durationMsLocked() int64 { return time.Since(r.started).Milliseconds() }

func (r *liveRun) snapshot() RunLog {
	d := time.Since(r.started).Round(time.Millisecond)
	return RunLog{
		RunID: r.RunID, TaskID: r.TaskID, StartAt: r.StartAt, Duration: d.String(),
		DurationMs: d.Milliseconds(), ExitCode: r.exitCode, Timeout: r.timeout,
		Status: r.status, Target: r.Target, Output: string(r.buf),
	}
}

type Plugin struct {
	ctx      sdk.Context
	cron     *cron.Cron
	mu       sync.Mutex
	tasks    map[string]*Task
	entryIDs map[string]cron.EntryID
	logs     map[string][]RunLog // 每 task 环形保留最近 20 次（已结束）
	runs     map[string]*liveRun // runId → 执行（含刚结束，供断线重连/历史查看）
	runOrder []string            // run 插入顺序，用于淘汰旧记录
}

func New() sdk.Plugin {
	return &Plugin{
		tasks: map[string]*Task{}, entryIDs: map[string]cron.EntryID{}, logs: map[string][]RunLog{},
		runs: map[string]*liveRun{},
	}
}

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "cronjobs",
		Name:        "定时任务",
		Version:     "0.3.0",
		Description: "定时任务管理：cron 调度 / 手动执行 / 执行中实时日志（SSE + 轮询降级）/ 超时控制 / 历史记录 / 绑定 SSH 主机远端执行；替代独立 crontab 管理",
		Permissions: []string{"exec"},
		Menu:        []sdk.Menu{{Title: "定时任务", Icon: "clock-circle", Path: "/"}},
	}
}

func (p *Plugin) OnInit(ctx sdk.Context) error {
	p.ctx = ctx
	p.cron = cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger)))
	p.cron.Start()
	p.loadTasks()

	api := func(method, path string, h http.HandlerFunc) { ctx.RegisterRoute(method, path, h) }
	api("GET", "/tasks", p.apiList)
	api("POST", "/tasks", p.apiSave)
	api("DELETE", "/tasks/*", p.apiDelete)
	api("POST", "/tasks/*/run", p.apiRun)
	api("POST", "/tasks/*/toggle", p.apiToggle)
	api("GET", "/logs/*", p.apiLogs)
	api("GET", "/active", p.apiActive)
	// /runs/* 是尾通配，会吞掉 /runs/x/stream，流式路由必须先注册。
	api("GET", "/runs/*/stream", p.apiRunStream)
	api("GET", "/runs/*", p.apiRunGet)

	ctx.RegisterMCPTool("cronjobs_list", "列出定时任务插件中的全部任务（名称/表达式/启停/下次执行时间）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) { return p.mcpList() })
	ctx.RegisterMCPTool("cronjobs_run", "手动触发一次定时任务",
		map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "description": "任务 ID"}}},
		func(args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			return p.runTask(id)
		})
	return nil
}

func (p *Plugin) OnShutdown() {
	if p.cron != nil {
		p.cron.Stop()
	}
}

// ---- 存取与调度 ----

func (p *Plugin) taskPath() string { return filepath.Join(p.ctx.DataDir(), taskFile) }

func (p *Plugin) loadTasks() {
	b, err := os.ReadFile(p.taskPath())
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &p.tasks); err != nil {
		return
	}
	for id, t := range p.tasks {
		if t.Enabled {
			p.schedule(id, t)
		}
	}
}

func (p *Plugin) saveTasks() error {
	_ = os.MkdirAll(p.ctx.DataDir(), 0o755)
	b, _ := json.MarshalIndent(p.tasks, "", "  ")
	return os.WriteFile(p.taskPath(), b, 0o600)
}

// schedule 注册 cron 调度（先移除旧条目）。
func (p *Plugin) schedule(id string, t *Task) {
	if old, ok := p.entryIDs[id]; ok {
		p.cron.Remove(old)
		delete(p.entryIDs, id)
	}
	if !t.Enabled {
		return
	}
	entry, err := p.cron.AddFunc(t.Cron, func() {
		out, err := p.runTask(id)
		p.ctx.Logf("任务 %s(%s) 执行完成 err=%v", t.Name, id, err)
		_ = out
	})
	if err != nil {
		p.ctx.Logf("任务 %s cron 表达式非法(%s): %v", id, t.Cron, err)
		return
	}
	p.entryIDs[id] = entry
}

func nextRun(t *Task) string {
	if !t.Enabled {
		return ""
	}
	sched, err := cron.ParseStandard(t.Cron)
	if err != nil {
		return ""
	}
	return sched.Next(time.Now()).Format("2006-01-02 15:04:05")
}

// ---- API ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func ginH(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// pluginPath 派发层注入的剥离前缀后的子路径。
func pluginPath(r *http.Request) string {
	if p := r.Header.Get("X-Plugin-Path"); p != "" {
		return p
	}
	return r.URL.Path
}

func seg(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n < len(parts) {
		return parts[n]
	}
	return ""
}

type taskView struct {
	*Task
	NextRun string `json:"nextRun"`
}

func (p *Plugin) apiList(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	ids := make([]string, 0, len(p.tasks))
	for id := range p.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]taskView, 0, len(ids))
	for _, id := range ids {
		t := p.tasks[id]
		out = append(out, taskView{Task: t, NextRun: nextRun(t)})
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) apiSave(w http.ResponseWriter, r *http.Request) {
	var t Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	t.ID = strings.TrimSpace(strings.ToLower(t.ID))
	t.Name = strings.TrimSpace(t.Name)
	t.Cron = strings.TrimSpace(t.Cron)
	t.Command = strings.TrimSpace(t.Command)
	if t.ID == "" || !validID(t.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID 只能含小写字母/数字/短横线"})
		return
	}
	if t.Name == "" || t.Cron == "" || t.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "名称/表达式/命令必填"})
		return
	}
	if _, err := cron.ParseStandard(t.Cron); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cron 表达式非法（5 段式）: " + err.Error()})
		return
	}
	p.mu.Lock()
	if t.CreatedAt == "" {
		if old, ok := p.tasks[t.ID]; ok {
			t.CreatedAt = old.CreatedAt
		} else {
			t.CreatedAt = time.Now().Format(time.RFC3339)
		}
	}
	p.tasks[t.ID] = &t
	clone := t
	err := p.saveTasks()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.schedule(t.ID, &clone)
	p.ctx.Logf("定时任务已保存: %s (%s)", t.ID, t.Cron)
	writeJSON(w, http.StatusOK, ginH("ok", true, "task", taskView{Task: &clone, NextRun: nextRun(&clone)}))
}

func (p *Plugin) apiDelete(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	p.mu.Lock()
	delete(p.tasks, id)
	delete(p.logs, id)
	err := p.saveTasks()
	p.mu.Unlock()
	if old, ok := p.entryIDs[id]; ok {
		p.cron.Remove(old)
		delete(p.entryIDs, id)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiToggle(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	var body struct{ Enabled bool }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.mu.Lock()
	t, ok := p.tasks[id]
	if ok {
		t.Enabled = body.Enabled
		err := p.saveTasks()
		clone := *t
		p.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		p.schedule(id, &clone)
		writeJSON(w, http.StatusOK, ginH("ok", true, "nextRun", nextRun(&clone)))
		return
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
}

func (p *Plugin) apiRun(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	id = strings.TrimSuffix(id, "/run")
	// wait=1 保留旧的同步语义（MCP/旧调用方可直接拿完整输出）；默认立即返回 runId 并后台执行。
	if r.URL.Query().Get("wait") == "1" {
		out, err := p.runTask(id)
		writeJSON(w, http.StatusOK, ginH("output", out, "error", errStr(err)))
		return
	}
	run, err := p.startRun(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true, "runId", run.RunID, "startAt", run.StartAt, "target", run.Target))
}

func (p *Plugin) apiLogs(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	p.mu.Lock()
	out := make([]RunLog, 0, 22)
	// 进行中的执行优先展示（历史环形里只有已结束记录）
	for _, rid := range p.runOrder {
		if run := p.runs[rid]; run != nil && run.TaskID == id && run.status == "running" {
			out = append(out, run.snapshot())
		}
	}
	out = append(out, p.logs[id]...)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) apiActive(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	out := make([]RunLog, 0, 4)
	for _, rid := range p.runOrder {
		if run := p.runs[rid]; run != nil && run.status == "running" {
			rl := run.snapshot()
			rl.Output = "" // 横幅只需要元信息，避免高输出任务每 2.5s 全量回传
			out = append(out, rl)
		}
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// apiRunGet 单次执行快照：前端断线/轮询降级、历史记录查看用。
func (p *Plugin) apiRunGet(w http.ResponseWriter, r *http.Request) {
	runID := strings.Trim(strings.TrimPrefix(pluginPath(r), "/runs/"), "/")
	p.mu.Lock()
	var found *RunLog
	if run, ok := p.runs[runID]; ok {
		rl := run.snapshot()
		found = &rl
	} else {
		for _, ls := range p.logs {
			for _, l := range ls {
				if l.RunID == runID {
					l := l
					found = &l
					break
				}
			}
		}
	}
	p.mu.Unlock()
	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "执行记录不存在或已过期"})
		return
	}
	writeJSON(w, http.StatusOK, *found)
}

// apiRunStream SSE 实时输出：连接建立先补发缓冲全量，之后增量推送，结束发 done 事件。
func (p *Plugin) apiRunStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "当前连接不支持流式输出"})
		return
	}
	runID := strings.TrimSuffix(strings.Trim(strings.TrimPrefix(pluginPath(r), "/runs/"), "/"), "/stream")
	p.mu.Lock()
	run, ok := p.runs[runID]
	if !ok {
		p.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "执行记录不存在或已过期"})
		return
	}
	ch := make(chan struct{}, 1)
	if run.status == "running" {
		run.subs[ch] = struct{}{}
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(run.subs, ch)
		p.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 走反代时禁缓冲
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	off := 0
	var pending []byte // 跨事件未凑齐的 UTF-8 半字符
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		p.mu.Lock()
		n := len(run.buf)
		var chunk []byte
		if n > off {
			end := n
			if end-off > 64<<10 { // 单条事件不超过 64KB，避免历史输出很大时一次性灌给浏览器
				end = off + 64<<10
			}
			chunk = append([]byte(nil), run.buf[off:end]...)
			off = end
		}
		more := n > off
		status, exitCode, durMs := run.status, run.exitCode, run.durationMsLocked()
		p.mu.Unlock()

		data := append(pending, chunk...)
		whole, rest := splitUTF8(data)
		pending = rest
		finished := status != "running"
		if finished && !more {
			whole = data // 收尾且无积压：剩余字节一并送出（不完整字节由 JSON 转义兜底）
			pending = nil
		}
		if len(whole) > 0 {
			if err := writeSSEEvent(w, "out", map[string]any{"text": string(whole)}); err != nil {
				return
			}
			flusher.Flush()
		}
		if more {
			continue
		}
		if !finished {
			select {
			case <-ch:
			case <-ping.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
			continue
		}
		_ = writeSSEEvent(w, "done", map[string]any{
			"status": status, "exitCode": exitCode, "durationMs": durMs,
		})
		flusher.Flush()
		return
	}
}

func writeSSEEvent(w io.Writer, typ string, data any) error {
	b, err := json.Marshal(map[string]any{"type": typ, "data": data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// splitUTF8 把末尾不完整的 UTF-8 序列留到下一次拼接，避免跨块截断中文输出。
func splitUTF8(b []byte) (whole, rest []byte) {
	if len(b) == 0 || utf8.Valid(b) {
		return b, nil
	}
	for i := 1; i <= 3 && i <= len(b); i++ {
		size := utf8SeqLen(b[len(b)-i])
		if size == 0 || i >= size {
			continue
		}
		ok := true
		for j := len(b) - i + 1; j < len(b); j++ {
			if !utf8Cont(b[j]) {
				ok = false
				break
			}
		}
		if ok {
			return b[:len(b)-i], b[len(b)-i:]
		}
		return b, nil
	}
	return b, nil
}

func utf8SeqLen(c byte) int {
	switch {
	case c >= 0xC2 && c <= 0xDF:
		return 2
	case c >= 0xE0 && c <= 0xEF:
		return 3
	case c >= 0xF0 && c <= 0xF4:
		return 4
	}
	return 0
}

func utf8Cont(c byte) bool { return c >= 0x80 && c <= 0xBF }

// ---- 执行 ----

func genRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startRun 后台执行一次任务，立即返回 run（前端拿 runId 订阅实时输出）。
// 同一任务允许并发执行（cron 周期短于任务耗时时不会互相吞掉），历史逐条可见。
func (p *Plugin) startRun(id string) (*liveRun, error) {
	p.mu.Lock()
	t, ok := p.tasks[id]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("任务不存在: %s", id)
	}
	hc := *t
	now := time.Now()
	run := &liveRun{
		RunID: genRunID(), TaskID: id, StartAt: now.Format("2006-01-02 15:04:05"),
		started: now, status: "running", subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
	}
	if hc.SSHHostID > 0 {
		run.Target = fmt.Sprintf("ssh:%d", hc.SSHHostID)
	}
	p.runs[run.RunID] = run
	p.runOrder = append(p.runOrder, run.RunID)
	p.trimRunsLocked()
	p.mu.Unlock()
	go p.execute(hc, run)
	return run, nil
}

// trimRunsLocked 淘汰已结束的旧 run（保留最近 40 条；进行中的永不淘汰）。需持 p.mu。
func (p *Plugin) trimRunsLocked() {
	for len(p.runOrder) > 40 {
		idx := -1
		for i, rid := range p.runOrder {
			if r := p.runs[rid]; r != nil && r.status != "running" {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		rid := p.runOrder[idx]
		p.runOrder = append(p.runOrder[:idx], p.runOrder[idx+1:]...)
		delete(p.runs, rid)
	}
}

// runTask 同步执行（MCP/旧调用方用）：等执行结束返回完整输出与错误。
func (p *Plugin) runTask(id string) (string, error) {
	run, err := p.startRun(id)
	if err != nil {
		return "", err
	}
	<-run.done
	p.mu.Lock()
	rl := run.snapshot()
	p.mu.Unlock()
	if rl.Status == "success" {
		return rl.Output, nil
	}
	return rl.Output, fmt.Errorf("执行失败（%s，exit %d）", rl.Status, rl.ExitCode)
}

// execute 真正执行任务命令：输出边到边写入 run.buf（SSE/轮询据此读取）。
// 绑定 SSH 主机（sshHostId>0）→ 远端登录 shell 执行；否则面板本机 bash -c。
func (p *Plugin) execute(t Task, run *liveRun) {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3600
	}
	var (
		err      error
		exitCode int
		timedOut bool
	)
	if t.SSHHostID > 0 {
		cmdStr := t.Command
		if t.WorkDir != "" {
			cmdStr = "cd '" + strings.ReplaceAll(t.WorkDir, "'", `'\''`) + "' && " + cmdStr
		}
		exitCode, err = p.ctx.SSHExecStream(t.SSHHostID, cmdStr, timeout, func(b []byte) { p.appendOut(run, b) })
		timedOut = errors.Is(err, sdk.ErrExecTimeout)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", t.Command)
		if t.WorkDir != "" {
			cmd.Dir = t.WorkDir
		}
		cmd.Env = append(os.Environ(), "HOME=/root")
		cmd.Stdout = runOutput{p: p, run: run}
		cmd.Stderr = runOutput{p: p, run: run}
		err = cmd.Run()
		timedOut = ctx.Err() == context.DeadlineExceeded
		if err != nil {
			exitCode = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
		}
	}
	status := "success"
	switch {
	case timedOut:
		status, exitCode = "timeout", -1
	case err != nil:
		status = "failed"
		if exitCode == 0 {
			exitCode = -1
		}
	case exitCode != 0:
		status = "failed"
	}
	// 连接失败/超时/本机无 bash 等“没有输出”的失败，补一条面板侧说明，避免日志空白。
	if err != nil && exitCode <= 0 {
		if timedOut {
			p.appendOut(run, []byte("[面板] 执行超时，已中断: "+err.Error()+"\n"))
		} else {
			p.appendOut(run, []byte("[面板] 执行失败: "+err.Error()+"\n"))
		}
	}
	rl := p.finishRun(run, status, exitCode, timedOut)
	p.ctx.Logf("任务 %s(%s) 执行完成 status=%s exit=%d", t.Name, t.ID, status, exitCode)
	// 失败 → 统一通知（站内信 + 按订阅邮件）；成功不通知，避免刷屏
	if status != "success" {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		if timedOut {
			detail = fmt.Sprintf("执行超时（>%ds）", timeout)
		}
		if rl.Target != "" {
			detail = fmt.Sprintf("[执行目标 %s] %s", rl.Target, detail)
		}
		out := rl.Output
		if len(out) > 500 {
			out = out[len(out)-500:] + "（尾部 500 字符）"
		}
		p.ctx.Notify("cron.failed", "error",
			fmt.Sprintf("定时任务失败: %s（exit %d）", t.Name, exitCode),
			detail+"\n输出：\n"+out)
	}
}

// runOutput 承接本机命令的 stdout/stderr，写入实时缓冲（exec 内部会并发调用，appendOut 加锁）。
type runOutput struct {
	p   *Plugin
	run *liveRun
}

func (w runOutput) Write(b []byte) (int, error) {
	w.p.appendOut(w.run, b)
	return len(b), nil
}

func (p *Plugin) appendOut(run *liveRun, b []byte) {
	if len(b) == 0 {
		return
	}
	p.mu.Lock()
	if !run.closed { // 超时后远端可能仍回吐尾部数据，结束后一律丢弃
		run.buf = append(run.buf, b...)
		for ch := range run.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	p.mu.Unlock()
}

// finishRun 定稿状态/耗时/历史并唤醒所有订阅者与等待者。
func (p *Plugin) finishRun(run *liveRun, status string, exitCode int, timedOut bool) RunLog {
	p.mu.Lock()
	if run.closed {
		rl := run.snapshot()
		p.mu.Unlock()
		return rl
	}
	run.status, run.exitCode, run.timeout = status, exitCode, timedOut
	run.closed = true
	rl := run.snapshot()
	p.logs[run.TaskID] = append([]RunLog{rl}, p.logs[run.TaskID]...)
	if len(p.logs[run.TaskID]) > 20 {
		p.logs[run.TaskID] = p.logs[run.TaskID][:20]
	}
	close(run.done)
	for ch := range run.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
	return rl
}

// ---- MCP ----

func (p *Plugin) mcpList() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.tasks))
	for id, t := range p.tasks {
		out = append(out, map[string]any{
			"id": id, "name": t.Name, "cron": t.Cron, "command": t.Command,
			"enabled": t.Enabled, "nextRun": nextRun(t), "sshHostId": t.SSHHostID,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func validID(id string) bool {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
