// Package notifywebhook 示例内置插件：Webhook 通知。
// 同时作为插件机制的样板：声明式设置、事件订阅、MCP 工具（聚合到面板 /mcp）。
// 外部形态（plugin.yaml + main.go）见 deploy/examples/notify-webhook.zip 的构建方式。
package notifywebhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sdk "zjpanel/backend/sdk/plugin"
)

type Plugin struct {
	ctx    sdk.Context
	url    string
	secret string
	client *http.Client
}

func New() sdk.Plugin {
	return &Plugin{client: &http.Client{Timeout: 10 * time.Second}}
}

func (p *Plugin) Meta() sdk.Meta {
	return sdk.Meta{
		ID:          "notify-webhook",
		Name:        "Webhook 通知",
		Version:     "0.1.0",
		Description: "站点离线/证书签发/证书到期等事件推送到自定义 Webhook；并提供 send_notification MCP 工具",
		Permissions: []string{"http"},
		Settings: []sdk.SettingDef{
			{Key: "hook_url", Title: "Webhook URL", Type: "text", Help: "POST JSON: {event, payload, timestamp, title, message}"},
			{Key: "secret", Title: "签名密钥（可选）", Type: "password", Help: "放入请求头 X-ZJPanel-Secret"},
			{Key: "events", Title: "订阅事件（逗号分隔）", Type: "text", Default: "sitecheck.down,cert.issued,cert.expiring,cert.failed"},
		},
	}
}

func (p *Plugin) OnInit(ctx sdk.Context) error {
	p.ctx = ctx
	url, err := ctx.SettingGet("hook_url")
	if err == nil {
		p.url = url
	}
	secret, err := ctx.SettingGet("secret")
	if err == nil {
		p.secret = secret
	}
	events := "sitecheck.down,cert.issued,cert.expiring,cert.failed"
	if ev, err := ctx.SettingGet("events"); err == nil && ev != "" {
		events = ev
	}

	for _, topic := range splitCSV(events) {
		t := topic
		ctx.SubscribeEvent(t, func(payload map[string]any) {
			p.send(map[string]any{"event": t, "payload": payload})
		})
	}

	// MCP 工具（聚合到面板 /mcp，外部客户端可经 API Key 调用 send_notification）
	ctx.RegisterMCPTool("send_notification", "发送一条通知（经 notify-webhook 插件推送到配置的 Webhook）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string", "description": "通知标题"},
				"message": map[string]any{"type": "string", "description": "通知内容"},
			},
			"required": []string{"title", "message"},
		},
		func(args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			message, _ := args["message"].(string)
			return p.send(map[string]any{"event": "mcp.send_notification", "payload": map[string]any{"title": title, "message": message}})
		})

	ctx.Logf("初始化完成（hook: %s）", maskURL(p.url))
	return nil
}

func (p *Plugin) send(body map[string]any) (string, error) {
	if p.url == "" {
		return "", fmt.Errorf("未配置 hook_url（请在插件设置中填写）")
	}
	body["timestamp"] = time.Now().Format(time.RFC3339)
	if t, ok := body["payload"].(map[string]any); ok {
		if title, ok := t["title"].(string); ok {
			body["title"] = title
		}
		if msg, ok := t["message"].(string); ok {
			body["message"] = msg
		}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, p.url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ZJPanel-Plugin", "notify-webhook")
	if p.secret != "" {
		req.Header.Set("X-ZJPanel-Secret", p.secret)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		p.ctx.Logf("推送失败: %v", err)
		return "", err
	}
	defer resp.Body.Close()
	p.ctx.Logf("已推送 %s → HTTP %d", body["event"], resp.StatusCode)
	return fmt.Sprintf("已推送到 Webhook（HTTP %d）", resp.StatusCode), nil
}

func (p *Plugin) OnShutdown() {}

func splitCSV(s string) []string {
	var out []string
	for _, part := range splitString(s, ",") {
		trimmed := trimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func splitString(s, sep string) []string {
	var out []string
	start := 0
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			out = append(out, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	out = append(out, s[start:])
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

func maskURL(u string) string {
	if len(u) > 40 {
		return u[:37] + "..."
	}
	return u
}
