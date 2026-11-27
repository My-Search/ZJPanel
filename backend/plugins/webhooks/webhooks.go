// Package webhooks 内置插件：Webhooks 服务（参考 adnanh/webhook 能力集）。
// 能力：Hook 管理（CRUD + 密钥自动生成）、GitHub X-Hub-Signature (payload-hash-sha1) 签名校验的
// 免鉴权接收端点（外部 CI 直接 POST）、部署命令执行与运行日志（环形保留）、可选 SMTP 邮件通知、
// send_webhook MCP 工具、绑定主机页中的 SSH 主机远端执行（sshHostId，0=面板本机）。
// API 挂 /api/plugin/webhooks/*（面板登录态），接收端挂 /api/pub/plugin/webhooks/hook/<id>。
package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhash "hash"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sdk "zjpanel/backend/sdk/plugin"
)

const hookFile = "hooks.json"

type Hook struct {
	ID          string `json:"id"`                  // URL 路径标识：/api/pub/plugin/webhooks/hook/<id>
	Secret      string `json:"secret"`              // GitHub Webhook Secret（payload-hash-sha1 校验）
	ProjectPath string `json:"projectPath"`         // 工作目录（本机为容器内路径；SSH 时为远端路径）
	DeployCmd   string `json:"deployCmd"`           // 部署命令（bash -c 执行）
	SSHHostID   uint   `json:"sshHostId,omitempty"` // >0 = 绑定主机页中的 SSH 主机远端执行；0 = 面板本机
	NotifyMail  string `json:"notifyMail"`          // 通知邮箱（空=不发邮件）
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
}

type RunLog struct {
	RunID      string `json:"runId,omitempty"`
	HookID     string `json:"hookId"`
	StartAt    string `json:"startAt"`
	ExitCode   int    `json:"exitCode"`
	Status     string `json:"status,omitempty"` // running | success | failed | timeout（旧记录为空按退出码判定）
	DurationMs int64  `json:"durationMs,omitempty"`
	Target     string `json:"target,omitempty"` // 执行目标：空=本机；ssh:<主机ID>
	Output     string `json:"output"`
}

// liveRun 一次执行（进行中或刚结束）：buf 是实时输出缓冲，SSE/轮询从这里读。
// buf/status/exitCode/durMs/subs/closed 均由 Plugin.mu 保护；StartAt/Target 创建后不变。
type liveRun struct {
	RunID    string
	HookID   string
	StartAt  string
	Target   string
	started  time.Time
	buf      []byte
	status   string
	exitCode int
	durMs    int64
	subs     map[chan struct{}]struct{} // 实时订阅者（每连接一个缓冲信号）
	done     chan struct{}              // 执行结束关闭，供同步等待方（MCP）使用
	closed   bool
}

type Plugin struct {
	ctx      sdk.Context
	mu       sync.Mutex
	hooks    map[string]*Hook
	logs     map[string][]RunLog // 每 hook 环形保留最近 10 次（已结束）
	runs     map[string]*liveRun // runId → 执行（含刚结束，供断线重连/历史查看）
	runOrder []string            // run 插入顺序，用于淘汰旧记录
	smtp     struct{ server, user, pass, from string }
}

func New() sdk.Plugin {
	return &Plugin{
		hooks: map[string]*Hook{}, logs: map[string][]RunLog{},
		runs: map[string]*liveRun{},
	}
}

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "webhooks",
		Name:        "Webhooks 服务",
		Version:     "0.3.3",
		Description: "Webhook 接收端（GitHub 签名校验）+ 部署执行 + 运行日志（执行中实时输出）+ 邮件通知 + 绑定主机页中的 SSH 主机远端执行；替代独立 webhook 服务",
		Permissions: []string{"http", "exec"},
		Menu:        []sdk.Menu{{Title: "Webhooks", Icon: "thunderbolt", Path: "/"}},
		Settings: []sdk.SettingDef{
			{Key: "smtp_server", Title: "SMTP 服务器", Type: "text", Help: "如 smtp.gmail.com，留空则不发邮件通知"},
			{Key: "smtp_port", Title: "SMTP 端口", Type: "text", Default: "465", Help: "SSL 端口，默认 465"},
			{Key: "smtp_user", Title: "SMTP 用户（发件人）", Type: "text"},
			{Key: "smtp_pass", Title: "SMTP 密码/授权码", Type: "password"},
		},
	}
}

func (p *Plugin) OnInit(ctx sdk.Context) error {
	p.ctx = ctx
	p.loadHooks()
	p.loadSMTP()

	// 管理面 API（面板登录态）
	ctx.RegisterRoute("GET", "/hooks", p.apiList)
	ctx.RegisterRoute("POST", "/hooks", p.apiSave)
	ctx.RegisterRoute("DELETE", "/hooks/*", p.apiDelete)
	ctx.RegisterRoute("GET", "/logs/*", p.apiLogs)
	ctx.RegisterRoute("GET", "/active", p.apiActive)
	// 注意顺序：/runs/* 是尾通配，会吞掉 /runs/x/stream，流式路由必须先注册。
	ctx.RegisterRoute("GET", "/runs/*/stream", p.apiRunStream)
	ctx.RegisterRoute("GET", "/runs/*", p.apiRunGet)
	ctx.RegisterRoute("POST", "/hooks/*/run", p.apiRun)

	// 公网接收端点（免面板登录，GitHub X-Hub-Signature 校验）
	ctx.RegisterPublicRoute("POST", "/hook/*", p.apiReceive)

	ctx.RegisterMCPTool("list_webhooks", "列出 Webhooks 插件中的全部 Hook（id/项目路径/启停）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) { return p.mcpList() })

	// 邮件通知走系统 curl smtps（与旧 webhook-manager.sh 同链路，容器自带 curl）
	ctx.RegisterMCPTool("trigger_webhook", "手动触发一次 Hook 部署",
		map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "description": "Hook ID"}}},
		func(args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			return p.runHook(id)
		})
	return nil
}

func (p *Plugin) OnShutdown() {}

// ---- 存取 ----

func (p *Plugin) hookPath() string { return filepath.Join(p.ctx.DataDir(), hookFile) }

func (p *Plugin) loadHooks() {
	b, err := os.ReadFile(p.hookPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &p.hooks)
}

func (p *Plugin) saveHooks() error {
	_ = os.MkdirAll(p.ctx.DataDir(), 0o755)
	b, _ := json.MarshalIndent(p.hooks, "", "  ")
	return os.WriteFile(p.hookPath(), b, 0o600)
}

func (p *Plugin) loadSMTP() {
	p.smtp.server, _ = p.ctx.SettingGet("smtp_server")
	port, _ := p.ctx.SettingGet("smtp_port")
	if port == "" {
		port = "465"
	}
	p.smtp.user, _ = p.ctx.SettingGet("smtp_user")
	p.smtp.pass, _ = p.ctx.SettingGet("smtp_pass")
	p.smtp.from = p.smtp.user
	if p.smtp.server != "" {
		p.smtp.server = p.smtp.server + ":" + port
	}
}

func genSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- 管理面 API ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Plugin) apiList(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	ids := make([]string, 0, len(p.hooks))
	for id := range p.hooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Hook, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.hooks[id])
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (p *Plugin) apiSave(w http.ResponseWriter, r *http.Request) {
	var h Hook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.ID = strings.TrimSpace(strings.ToLower(h.ID))
	if h.ID == "" || !validID(h.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID 只能含小写字母/数字/短横线"})
		return
	}
	if h.ProjectPath == "" || h.DeployCmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "项目路径与部署命令必填"})
		return
	}
	p.mu.Lock()
	if old, ok := p.hooks[h.ID]; ok && h.Secret == "" {
		h.Secret = old.Secret // 编辑时留空则沿用旧密钥
	}
	if h.Secret == "" {
		h.Secret = genSecret()
	}
	if h.CreatedAt == "" {
		h.CreatedAt = time.Now().Format(time.RFC3339)
	}
	p.hooks[h.ID] = &h
	err := p.saveHooks()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.ctx.Logf("hook 已保存: %s", h.ID)
	writeJSON(w, http.StatusOK, ginH("ok", true, "hook", &h))
}

func (p *Plugin) apiDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(pluginPath(r), "/hooks/"), "/")
	p.mu.Lock()
	delete(p.hooks, id)
	delete(p.logs, id)
	err := p.saveHooks()
	p.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ginH("ok", true))
}

func (p *Plugin) apiLogs(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(pluginPath(r), "/logs/"), "/")
	p.mu.Lock()
	out := make([]RunLog, 0, 12)
	// 进行中的执行优先展示（历史环形里只有已结束记录）
	for _, rid := range p.runOrder {
		if run := p.runs[rid]; run != nil && run.HookID == id && run.status == "running" {
			out = append(out, run.meta())
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
			out = append(out, run.meta()) // 横幅/卡片徽标只需要元信息，避免高输出任务每 2.5s 全量回传
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
// 事件格式与面板其它 SSE 一致：默认 message 内 {"type":"out","data":{"text":...}}。
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
		status, exitCode, durMs := run.status, run.exitCode, run.durMs
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
		if more { // 还有积压输出，先发完再等新数据
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
// 只处理“合法序列被截断”的情况；其它非法字节原样返回（JSON 编码会替换为 U+FFFD），
// 不能简单按“末尾最多 3 字节”回退，否则非法字节会让已发送内容被重复输出。
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

func (p *Plugin) apiRun(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(pluginPath(r), "/hooks/"), "/")
	id = strings.TrimSuffix(id, "/run")
	// wait=1 保留旧的同步语义（旧调用方可直接拿完整输出）；默认立即返回 runId 并后台执行。
	if r.URL.Query().Get("wait") == "1" {
		out, err := p.runHook(id)
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

func validID(id string) bool {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
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

// ---- 接收端点（免鉴权 + 签名校验）----

// pluginPath 派发层注入的剥离前缀后的子路径（handler 不能用 r.URL.Path，那是完整原始路径）。
func pluginPath(r *http.Request) string {
	if p := r.Header.Get("X-Plugin-Path"); p != "" {
		return p
	}
	return r.URL.Path
}

func (p *Plugin) apiReceive(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(pluginPath(r), "/hook/"), "/")
	p.mu.Lock()
	h, ok := p.hooks[id]
	p.mu.Unlock()
	if !ok || !h.Enabled {
		http.Error(w, "hook 不存在或未启用", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "读取 body 失败", http.StatusBadRequest)
		return
	}
	// GitHub 风格签名：X-Hub-Signature = sha1=<hex(hmac-sha1(secret, body))>；同时兼容 sha256=
	// 必须配置非空 Secret 且至少携带一个有效签名头，严禁无头放行。
	sig := r.Header.Get("X-Hub-Signature")
	sig256 := r.Header.Get("X-Hub-Signature-256")
	if !verifyWebhookRequest(h.Secret, body, sig, sig256) {
		p.ctx.Logf("hook %s 签名校验失败", id)
		http.Error(w, "signature mismatch", http.StatusForbidden)
		return
	}
	go func() {
		run, err := p.startRun(id)
		if err != nil {
			p.ctx.Logf("hook %s 触发失败: %v", id, err)
			return
		}
		p.ctx.Logf("hook %s 已触发 run=%s", id, run.RunID)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "hook": id})
}

func verifyWebhookRequest(secret string, body []byte, sig1, sig256 string) bool {
	if secret == "" || (sig1 == "" && sig256 == "") {
		return false
	}
	if sig256 != "" && !verifySig(sig256, "sha256=", secret, body) {
		return false
	}
	if sig1 != "" && !verifySig(sig1, "sha1=", secret, body) {
		return false
	}
	return true
}

func verifySig(header, prefix, secret string, body []byte) bool {
	if header == "" || secret == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	var mac stdhash.Hash
	if prefix == "sha256=" {
		mac = hmac.New(sha256.New, []byte(secret))
	} else {
		mac = hmac.New(sha1.New, []byte(secret))
	}
	mac.Write(body)
	expect := prefix + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expect), []byte(header))
}

// ---- 执行与日志（实时流）----

func genRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// meta 只返回运行态元信息，不复制输出缓冲（横幅/日志列表轮询用）。
func (r *liveRun) meta() RunLog {
	return RunLog{
		RunID: r.RunID, HookID: r.HookID, StartAt: r.StartAt, ExitCode: r.exitCode,
		Status: r.status, DurationMs: r.durMs, Target: r.Target,
	}
}

func (r *liveRun) snapshot() RunLog {
	rl := r.meta()
	rl.Output = string(r.buf)
	return rl
}

// startRun 后台执行一次 Hook，立即返回 run（前端拿 runId 订阅实时输出）。
// 同一 Hook 允许并发触发：每次执行独立 runId/缓冲，历史逐条可见（与旧行为一致）。
func (p *Plugin) startRun(id string) (*liveRun, error) {
	p.mu.Lock()
	h, ok := p.hooks[id]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("hook 不存在: %s", id)
	}
	hc := *h
	now := time.Now()
	run := &liveRun{
		RunID: genRunID(), HookID: id, StartAt: now.Format("2006-01-02 15:04:05"),
		started: now, status: "running", subs: map[chan struct{}]struct{}{},
		done: make(chan struct{}),
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

// execute 真正执行命令：输出边到边写入 run.buf（SSE/轮询据此读取）。
// 绑定 SSH 主机（sshHostId>0）→ 远端登录 shell 执行（默认超时 3600s）；否则面板本机 bash -c。
func (p *Plugin) execute(h Hook, run *liveRun) {
	var (
		exitCode int
		err      error
	)
	if h.SSHHostID > 0 {
		cmdStr := h.DeployCmd
		if h.ProjectPath != "" {
			cmdStr = "cd " + shellQuote(h.ProjectPath) + " && " + cmdStr
		}
		exitCode, err = p.ctx.SSHExecStream(h.SSHHostID, cmdStr, 3600, func(b []byte) { p.appendOut(run, b) })
	} else {
		cmd := exec.Command("bash", "-c", h.DeployCmd)
		cmd.Dir = h.ProjectPath
		cmd.Env = append(os.Environ(), "HOME=/root")
		cmd.Stdout = runOutput{p: p, run: run}
		cmd.Stderr = runOutput{p: p, run: run}
		err = cmd.Run()
		exitCode = 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
	}
	status := "success"
	switch {
	case errors.Is(err, sdk.ErrExecTimeout):
		status, exitCode = "timeout", -1
	case exitCode != 0:
		status = "failed"
	}
	// 连接失败/超时/本机无 bash 等“没有输出”的失败，补一条面板侧说明，避免日志空白。
	if err != nil && exitCode <= 0 {
		if status == "timeout" {
			p.appendOut(run, []byte("[面板] 执行超时，已中断: "+err.Error()+"\n"))
		} else {
			p.appendOut(run, []byte("[面板] 执行失败: "+err.Error()+"\n"))
		}
	}
	rl := p.finishRun(run, status, exitCode)
	p.ctx.Logf("hook %s 执行完成 run=%s status=%s exit=%d", run.HookID, run.RunID, status, exitCode)
	if status == "success" {
		p.notify(&h, fmt.Sprintf("Webhook 成功: %s", run.HookID), rl)
	} else {
		p.notify(&h, fmt.Sprintf("Webhook 失败: %s (%s, exit %d)", run.HookID, status, exitCode), rl)
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
func (p *Plugin) finishRun(run *liveRun, status string, exitCode int) RunLog {
	p.mu.Lock()
	if run.closed {
		rl := run.snapshot()
		p.mu.Unlock()
		return rl
	}
	run.status, run.exitCode = status, exitCode
	run.durMs = time.Since(run.started).Milliseconds()
	run.closed = true
	rl := run.snapshot()
	p.logs[run.HookID] = append([]RunLog{rl}, p.logs[run.HookID]...)
	if len(p.logs[run.HookID]) > 10 {
		p.logs[run.HookID] = p.logs[run.HookID][:10]
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

// runHook 同步执行（MCP 工具/旧调用方用）：等执行结束返回完整输出与错误。
func (p *Plugin) runHook(id string) (string, error) {
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// notify SMTP 邮件通知（原生 TLS/STARTTLS，避免 curl --user 在进程命令行暴露密码及邮件头 CRLF 注入）。
func (p *Plugin) notify(h *Hook, subject string, rl RunLog) {
	if h.NotifyMail == "" || p.smtp.server == "" || p.smtp.user == "" || p.smtp.pass == "" {
		return
	}
	body := fmt.Sprintf("Hook: %s\n时间: %s\n执行目标: %s\n退出码: %d\n\n--- 输出 ---\n%s",
		h.ID, rl.StartAt, orLocal(rl.Target), rl.ExitCode, rl.Output)
	if err := sendHookMail(p.smtp.server, p.smtp.user, p.smtp.pass, p.smtp.from, h.NotifyMail, subject, body); err != nil {
		p.ctx.Logf("邮件通知失败: %v", err)
	}
}

func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// orLocal 日志/邮件里展示执行目标（空=面板本机）。
func orLocal(target string) string {
	if target == "" {
		return "本机"
	}
	return target
}

func sendHookMail(addr, user, pass, from, to, subject, body string) error {
	host := addr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host = addr[:i]
	}
	from, to = stripCRLF(from), stripCRLF(to)
	msg := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte(body)),
	}, "\r\n") + "\r\n"

	var c *smtp.Client
	if strings.HasSuffix(addr, ":465") {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return err
		}
		cli, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return err
		}
		c = cli
	} else {
		cli, err := smtp.Dial(addr)
		if err != nil {
			return err
		}
		if ok, _ := cli.Extension("STARTTLS"); ok {
			if err := cli.StartTLS(&tls.Config{ServerName: host}); err != nil {
				_ = cli.Close()
				return err
			}
		}
		c = cli
	}
	defer c.Close()
	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// ---- MCP ----

func (p *Plugin) mcpList() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.hooks))
	for id, h := range p.hooks {
		out = append(out, map[string]any{
			"id": id, "projectPath": h.ProjectPath, "deployCmd": h.DeployCmd,
			"enabled": h.Enabled, "notifyMail": h.NotifyMail, "sshHostId": h.SSHHostID,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}
