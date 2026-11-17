package plugin

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v5/challenge"

	"zjpanel/backend/internal/cert/dns"
	"zjpanel/backend/internal/cert/targets"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/notify"
	sdk "zjpanel/backend/sdk/plugin"
)

// ---- Context 实现（传给插件的运行时上下文）----

type ctxImpl struct {
	mgr *Manager
	id  string
	rt  *runtime
}

func (c *ctxImpl) Logf(format string, args ...any) {
	slog.Info("[plugin:" + c.id + "] " + fmt.Sprintf(format, args...))
}

func (c *ctxImpl) DataDir() string {
	d := filepath.Join(c.mgr.cfg.DataDir, "plugins-data", c.id)
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (c *ctxImpl) SettingGet(key string) (string, error) {
	var rec model.PluginSetting
	if err := c.mgr.db.Where("plugin_id = ? AND key = ?", c.id, key).First(&rec).Error; err != nil {
		return "", fmt.Errorf("设置不存在: %s", key)
	}
	return rec.Value, nil
}

func (c *ctxImpl) SettingSet(key, value string) error {
	var rec model.PluginSetting
	err := c.mgr.db.Where("plugin_id = ? AND key = ?", c.id, key).First(&rec).Error
	if err != nil {
		return c.mgr.db.Create(&model.PluginSetting{PluginID: c.id, Key: key, Value: value}).Error
	}
	return c.mgr.db.Model(&rec).Update("value", value).Error
}

func (c *ctxImpl) RegisterRoute(method, path string, h http.HandlerFunc) {
	if method == "" {
		method = "*"
	}
	c.rt.routes = append(c.rt.routes, routeEntry{Method: method, Path: path, Handler: h})
	c.Logf("注册路由 %s %s", method, path)
}

// RegisterPublicRoute 免鉴权公开路由：由 HTTP 层经 DispatchPublicRoute 派发，安全性由插件自校验。
func (c *ctxImpl) RegisterPublicRoute(method, path string, h http.HandlerFunc) {
	if method == "" {
		method = "*"
	}
	c.rt.publicRoutes = append(c.rt.publicRoutes, routeEntry{Method: method, Path: path, Handler: h})
	c.Logf("注册公开路由 %s %s", method, path)
}

func (c *ctxImpl) RegisterCron(spec string, fn func()) {
	id := c.mgr.addCron(spec, fn)
	c.rt.cronIDs = append(c.rt.cronIDs, id)
	c.Logf("注册定时任务 %s", spec)
}

func (c *ctxImpl) RegisterMCPTool(name, description string, inputSchema map[string]any, handler func(args map[string]any) (string, error)) {
	full := c.id + "_" + name
	if err := c.mgr.mcp.Register(&mcp.Tool{
		Name: full, Description: description, InputSchema: inputSchema, Handler: handler, Owner: c.id,
	}); err != nil {
		c.Logf("MCP 工具注册失败: %v", err)
		return
	}
	c.rt.mcpNames = append(c.rt.mcpNames, full)
	c.Logf("注册 MCP 工具 %s", full)
}

// Notify 发送统一通知（Source 记为插件 ID；服务未注入时为空操作）。
func (c *ctxImpl) Notify(nType, level, title, body string) {
	if c.mgr.notify == nil {
		return
	}
	c.mgr.notify.Send(c.id, notify.Input{Type: nType, Level: level, Title: title, Body: body})
}

func (c *ctxImpl) SubscribeEvent(topic string, fn func(payload map[string]any)) {
	c.mgr.bus.Subscribe(topic, fn)
	c.Logf("订阅事件 %s", topic)
}

// RegisterDNSProvider 插件 DNS 提供商 → cert 注册表（Present/CleanUp 与 lego challenge.Provider 同签名）。
func (c *ctxImpl) RegisterDNSProvider(name, title string, fields []sdk.FieldDef, factory func(cfg map[string]string) (sdk.DNSProvider, error)) {
	c.mgr.dns.Register(&certdns.ProviderDef{
		Name: name, Title: title, Fields: convertFields(fields), Owner: c.id,
		Factory: func(cfg map[string]string) (challenge.Provider, error) {
			p, err := factory(cfg)
			if err != nil {
				return nil, err
			}
			return p, nil // sdk.DNSProvider 与 challenge.Provider 方法集一致
		},
	})
	c.Logf("注册 DNS 提供商 %s", name)
}

// RegisterDeployTarget 插件部署目标 → cert 注册表。
func (c *ctxImpl) RegisterDeployTarget(typ, title string, fields []sdk.FieldDef, runner func(args sdk.DeployArgs) (string, error)) {
	c.mgr.tgt.Register(&targets.TargetDef{
		Type: typ, Title: title, Fields: convertFields(fields), Owner: c.id,
		Runner: func(args targets.DeployArgs) (string, error) {
			return runner(sdk.DeployArgs{
				TaskID: args.TaskID, Domains: args.Domains,
				CertPEM: args.CertPEM, KeyPEM: args.KeyPEM,
				NotAfter: args.NotAfter.Format("2006-01-02"),
				Config:   args.Config,
			})
		},
	})
	c.Logf("注册部署目标 %s", typ)
}

func convertFields(in []sdk.FieldDef) []certdns.FieldDef {
	out := make([]certdns.FieldDef, 0, len(in))
	for _, f := range in {
		out = append(out, certdns.FieldDef{Key: f.Key, Title: f.Title, Type: f.Type, Default: f.Default, Help: f.Help, Required: f.Required})
	}
	return out
}

var _ = time.Now
