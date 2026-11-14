// Package cronjobs 内置插件：定时任务管理。
// 能力：任务 CRUD（名称 + 5 段 cron + shell 命令）、启停、手动执行、超时控制、
// 按 cron 表达式自动调度（复用面板 robfig/cron）、执行日志（环形保留最近 20 次）、
// 下次执行时间展示。API 挂 /api/plugin/cronjobs/*（面板登录态）。
package cronjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	Timeout   int    `json:"timeout"` // 秒；0/负数 = 3600
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
}

type RunLog struct {
	TaskID   string `json:"taskId"`
	StartAt  string `json:"startAt"`
	Duration string `json:"duration"`
	ExitCode int    `json:"exitCode"`
	Timeout  bool   `json:"timeout"`
	Output   string `json:"output"`
}

type Plugin struct {
	ctx      sdk.Context
	cron     *cron.Cron
	mu       sync.Mutex
	tasks    map[string]*Task
	entryIDs map[string]cron.EntryID
	logs     map[string][]RunLog // 每 task 环形保留最近 20 次
}

func New() sdk.Plugin {
	return &Plugin{tasks: map[string]*Task{}, entryIDs: map[string]cron.EntryID{}, logs: map[string][]RunLog{}}
}

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "cronjobs",
		Name:        "定时任务",
		Version:     "0.1.0",
		Description: "定时任务管理：cron 调度 / 手动执行 / 超时控制 / 执行日志；替代独立 crontab 管理",
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
	out, err := p.runTask(id)
	writeJSON(w, http.StatusOK, ginH("output", out, "error", errStr(err)))
}

func (p *Plugin) apiLogs(w http.ResponseWriter, r *http.Request) {
	id := seg(pluginPath(r), 1)
	p.mu.Lock()
	logs := append([]RunLog{}, p.logs[id]...)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, logs)
}

// ---- 执行 ----

// runTask 同步执行任务命令（带超时控制）并记录日志。
func (p *Plugin) runTask(id string) (string, error) {
	p.mu.Lock()
	t, ok := p.tasks[id]
	p.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("任务不存在: %s", id)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3600
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", t.Command)
	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}
	cmd.Env = append(os.Environ(), "HOME=/root")
	outBytes, err := cmd.CombinedOutput()
	timedOut := ctx.Err() == context.DeadlineExceeded
	exitCode := 0
	if err != nil {
		exitCode = 1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	rl := RunLog{
		TaskID:   id,
		StartAt:  start.Format("2006-01-02 15:04:05"),
		Duration: time.Since(start).Round(time.Millisecond).String(),
		ExitCode: exitCode,
		Timeout:  timedOut,
		Output:   string(outBytes),
	}
	p.mu.Lock()
	p.logs[id] = append([]RunLog{rl}, p.logs[id]...)
	if len(p.logs[id]) > 20 {
		p.logs[id] = p.logs[id][:20]
	}
	p.mu.Unlock()
	// 失败 → 统一通知（站内信 + 按订阅邮件）；成功不通知，避免刷屏
	if err != nil || timedOut {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		if timedOut {
			detail = fmt.Sprintf("执行超时（>%ds）", timeout)
		}
		out := string(outBytes)
		if len(out) > 500 {
			out = out[len(out)-500:] + "（尾部 500 字符）"
		}
		p.ctx.Notify("cron.failed", "error",
			fmt.Sprintf("定时任务失败: %s（exit %d）", t.Name, exitCode),
			detail+"\n输出：\n"+out)
	}
	return rl.Output, err
}

// ---- MCP ----

func (p *Plugin) mcpList() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.tasks))
	for id, t := range p.tasks {
		out = append(out, map[string]any{
			"id": id, "name": t.Name, "cron": t.Cron, "command": t.Command,
			"enabled": t.Enabled, "nextRun": nextRun(t),
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
