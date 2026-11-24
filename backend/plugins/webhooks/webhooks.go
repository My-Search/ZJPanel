// Package webhooks 内置插件：Webhooks 服务（参考 adnanh/webhook 能力集）。
// 能力：Hook 管理（CRUD + 密钥自动生成）、GitHub X-Hub-Signature (payload-hash-sha1) 签名校验的
// 免鉴权接收端点（外部 CI 直接 POST）、部署命令执行与运行日志（环形保留）、可选 SMTP 邮件通知、
// send_webhook MCP 工具、绑定终端 SSH 主机远端执行（sshHostId，0=面板本机）。
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

	sdk "zjpanel/backend/sdk/plugin"
)

const hookFile = "hooks.json"

type Hook struct {
	ID          string `json:"id"`                  // URL 路径标识：/api/pub/plugin/webhooks/hook/<id>
	Secret      string `json:"secret"`              // GitHub Webhook Secret（payload-hash-sha1 校验）
	ProjectPath string `json:"projectPath"`         // 工作目录（本机为容器内路径；SSH 时为远端路径）
	DeployCmd   string `json:"deployCmd"`           // 部署命令（bash -c 执行）
	SSHHostID   uint   `json:"sshHostId,omitempty"` // >0 = 绑定终端 SSH 主机远端执行；0 = 面板本机
	NotifyMail  string `json:"notifyMail"`          // 通知邮箱（空=不发邮件）
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
}

type RunLog struct {
	HookID   string `json:"hookId"`
	StartAt  string `json:"startAt"`
	ExitCode int    `json:"exitCode"`
	Target   string `json:"target,omitempty"` // 执行目标：空=本机；ssh:<主机ID>
	Output   string `json:"output"`
}

type Plugin struct {
	ctx   sdk.Context
	mu    sync.Mutex
	hooks map[string]*Hook
	logs  map[string][]RunLog // 每 hook 环形保留最近 10 次
	smtp  struct{ server, user, pass, from string }
}

func New() sdk.Plugin { return &Plugin{hooks: map[string]*Hook{}, logs: map[string][]RunLog{}} }

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "webhooks",
		Name:        "Webhooks 服务",
		Version:     "0.2.0",
		Description: "Webhook 接收端（GitHub 签名校验）+ 部署执行 + 运行日志 + 邮件通知 + 绑定 SSH 主机远端执行；替代独立 webhook 服务",
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
	logs := append([]RunLog{}, p.logs[id]...)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, logs)
}

func (p *Plugin) apiRun(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(pluginPath(r), "/hooks/"), "/")
	id = strings.TrimSuffix(id, "/run")
	out, err := p.runHook(id)
	writeJSON(w, http.StatusOK, ginH("output", out, "error", errStr(err)))
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
		out, err := p.runHook(id)
		p.ctx.Logf("hook %s 执行完成 exit=%v", id, err)
		_ = out
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

// ---- 执行与日志 ----

// runHook 同步执行部署命令并记录日志/邮件通知。
// 绑定 SSH 主机（sshHostId>0）→ 远端登录 shell 执行（默认超时 3600s）；否则面板本机 bash -c。
func (p *Plugin) runHook(id string) (string, error) {
	p.mu.Lock()
	h, ok := p.hooks[id]
	p.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("hook 不存在: %s", id)
	}
	start := time.Now()
	var (
		outBytes []byte
		err      error
		exitCode int
		target   string
	)
	if h.SSHHostID > 0 {
		target = fmt.Sprintf("ssh:%d", h.SSHHostID)
		cmdStr := h.DeployCmd
		if h.ProjectPath != "" {
			cmdStr = "cd '" + strings.ReplaceAll(h.ProjectPath, "'", `'\''`) + "' && " + cmdStr
		}
		var out string
		out, exitCode, err = p.ctx.SSHExec(h.SSHHostID, cmdStr, 3600)
		outBytes = []byte(out)
		if errors.Is(err, sdk.ErrExecTimeout) {
			p.ctx.Logf("hook %s SSH 执行超时", id)
		}
	} else {
		cmd := exec.Command("bash", "-c", h.DeployCmd)
		cmd.Dir = h.ProjectPath
		cmd.Env = append(os.Environ(), "HOME=/root")
		outBytes, err = cmd.CombinedOutput()
		exitCode = 0
		if err != nil {
			exitCode = 1
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			}
		}
	}
	rl := RunLog{
		HookID:   id,
		StartAt:  start.Format("2006-01-02 15:04:05"),
		ExitCode: exitCode,
		Target:   target,
		Output:   string(outBytes),
	}
	p.mu.Lock()
	p.logs[id] = append([]RunLog{rl}, p.logs[id]...)
	if len(p.logs[id]) > 10 {
		p.logs[id] = p.logs[id][:10]
	}
	p.mu.Unlock()

	if exitCode == 0 {
		p.notify(h, fmt.Sprintf("Webhook 成功: %s", id), rl)
	} else {
		p.notify(h, fmt.Sprintf("Webhook 失败: %s (exit %d)", id, exitCode), rl)
	}
	return rl.Output, err
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
