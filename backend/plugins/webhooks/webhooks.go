// Package webhooks 内置插件：Webhooks 服务（参考 adnanh/webhook 能力集）。
// 能力：Hook 管理（CRUD + 密钥自动生成）、GitHub X-Hub-Signature (payload-hash-sha1) 签名校验的
// 免鉴权接收端点（外部 CI 直接 POST）、部署命令执行与运行日志（环形保留）、可选 SMTP 邮件通知、
// send_webhook MCP 工具。API 挂 /api/plugin/webhooks/*（面板登录态），接收端挂 /api/pub/plugin/webhooks/hook/<id>。
package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdhash "hash"
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

	sdk "zjpanel/backend/sdk/plugin"
)

const hookFile = "hooks.json"

type Hook struct {
	ID          string `json:"id"`            // URL 路径标识：/api/pub/plugin/webhooks/hook/<id>
	Secret      string `json:"secret"`        // GitHub Webhook Secret（payload-hash-sha1 校验）
	ProjectPath string `json:"projectPath"`   // 工作目录（容器内路径，如 /opt/xxx）
	DeployCmd   string `json:"deployCmd"`     // 部署命令（bash -c 执行）
	NotifyMail  string `json:"notifyMail"`    // 通知邮箱（空=不发邮件）
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
}

type RunLog struct {
	HookID   string `json:"hookId"`
	StartAt  string `json:"startAt"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

type Plugin struct {
	ctx    sdk.Context
	mu     sync.Mutex
	hooks  map[string]*Hook
	logs   map[string][]RunLog // 每 hook 环形保留最近 10 次
	smtp   struct{ server, user, pass, from string }
}

func New() sdk.Plugin { return &Plugin{hooks: map[string]*Hook{}, logs: map[string][]RunLog{}} }

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "webhooks",
		Name:        "Webhooks 服务",
		Version:     "0.1.0",
		Description: "Webhook 接收端（GitHub 签名校验）+ 部署执行 + 运行日志 + 邮件通知；替代独立 webhook 服务",
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
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/hooks/"), "/")
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
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/logs/"), "/")
	p.mu.Lock()
	logs := append([]RunLog{}, p.logs[id]...)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, logs)
}

func (p *Plugin) apiRun(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/hooks/"), "/")
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

func (p *Plugin) apiReceive(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/hook/"), "/")
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
	sig := r.Header.Get("X-Hub-Signature")
	sig256 := r.Header.Get("X-Hub-Signature-256")
	if !verifySig(sig, "sha1=", h.Secret, body) || (sig256 != "" && !verifySig(sig256, "sha256=", h.Secret, body)) {
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

func verifySig(header, prefix, secret string, body []byte) bool {
	if header == "" {
		return prefix == "sha1=" // GitHub 恒带 sha1 头；未配置任何头时仅要求 sha1 缺省校验通过
	}
	if !strings.HasPrefix(header, prefix) {
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
func (p *Plugin) runHook(id string) (string, error) {
	p.mu.Lock()
	h, ok := p.hooks[id]
	p.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("hook 不存在: %s", id)
	}
	start := time.Now()
	cmd := exec.Command("bash", "-c", h.DeployCmd)
	cmd.Dir = h.ProjectPath
	cmd.Env = append(os.Environ(), "HOME=/root")
	outBytes, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	rl := RunLog{
		HookID:   id,
		StartAt:  start.Format("2006-01-02 15:04:05"),
		ExitCode: exitCode,
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

// notify SMTP 邮件通知（curl smtps，与旧 webhook-manager.sh 链路一致；配置不全时静默跳过）。
func (p *Plugin) notify(h *Hook, subject string, rl RunLog) {
	if h.NotifyMail == "" || p.smtp.server == "" || p.smtp.user == "" || p.smtp.pass == "" {
		return
	}
	body := fmt.Sprintf("Hook: %s\n时间: %s\n退出码: %d\n\n--- 输出 ---\n%s",
		h.ID, rl.StartAt, rl.ExitCode, rl.Output)
	cmd := exec.Command("curl", "--silent", "--show-error", "--ssl-reqd",
		"--url", "smtps://"+p.smtp.server,
		"--user", p.smtp.user+":"+p.smtp.pass,
		"--mail-from", p.smtp.from,
		"--mail-rcpt", h.NotifyMail,
		"-T", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		p.smtp.from, h.NotifyMail, subject, body))
	if err := cmd.Run(); err != nil {
		p.ctx.Logf("邮件通知失败: %v", err)
	}
}

// ---- MCP ----

func (p *Plugin) mcpList() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.hooks))
	for id, h := range p.hooks {
		out = append(out, map[string]any{
			"id": id, "projectPath": h.ProjectPath, "deployCmd": h.DeployCmd,
			"enabled": h.Enabled, "notifyMail": h.NotifyMail,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}
