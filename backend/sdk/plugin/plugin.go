// Package plugin 是 ZJPanel 的插件 SDK（稳定接口）。
// 外部插件以 Go 源码形态安装（plugin.yaml + main.go），由面板内嵌解释器加载；
// 插件通过 Context 声明能力：HTTP 路由、菜单、设置、cron、MCP 工具、事件订阅、
// 证书 DNS 提供商与部署目标。所有 MCP 工具聚合到面板唯一 /mcp 端点对外提供。
package plugin

import (
	"context"
	"net/http"
)

// Plugin 插件入口接口。
type Plugin interface {
	Meta() Meta
	OnInit(ctx Context) error
	OnShutdown()
}

// Meta 插件元数据（plugin.yaml 同构；内置插件在代码里声明）。
type Meta struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Permissions []string     `json:"permissions,omitempty"` // 预留：http/fs/exec/docker/nginx
	Menu        []Menu       `json:"menu,omitempty"`
	Settings    []SettingDef `json:"settings,omitempty"`
}

// Menu 菜单贡献：前端侧边栏项，页面为 /plugin-assets/<id>/（iframe 承载）。
type Menu struct {
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	Path  string `json:"path"` // 相对插件页内路由，如 / 或 /config
}

// SettingDef 声明式设置项（前端自动渲染表单，值存面板 DB）。
type SettingDef struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Type    string   `json:"type"` // text | textarea | password | select | switch
	Default string   `json:"default,omitempty"`
	Help    string   `json:"help,omitempty"`
	Options []string `json:"options,omitempty"` // select 用
}

// FieldDef 表单字段（DNS 提供商凭证 / 部署目标参数共用）。
type FieldDef struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Type     string `json:"type"` // text | password | select
	Default  string `json:"default,omitempty"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required"`
}

// DNSProvider 插件实现的最小 DNS 挑战接口（与 ACME 库对齐）。
type DNSProvider interface {
	Present(ctx context.Context, domain, token, keyAuth string) error
	CleanUp(ctx context.Context, domain, token, keyAuth string) error
}

// DeployArgs 部署目标入参。
type DeployArgs struct {
	TaskID   uint
	Domains  []string
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter string // 2006-01-02
	Config   map[string]any
}

// Context 插件运行时上下文：全部扩展点从这里声明。
type Context interface {
	Logf(format string, args ...any)
	DataDir() string // 插件私有数据目录（面板创建）

	SettingGet(key string) (string, error)
	SettingSet(key, value string) error

	// RegisterRoute 注册插件 API（自动挂在 /api/plugin/<id>/ 前缀下）。
	RegisterRoute(method, path string, h http.HandlerFunc)
	// RegisterCron 注册定时任务（标准 5 段 cron）。
	RegisterCron(spec string, fn func())
	// RegisterMCPTool 声明 MCP 工具（聚合到面板唯一 /mcp；名称自动加 <pluginId>_ 前缀）。
	RegisterMCPTool(name, description string, inputSchema map[string]any, handler func(args map[string]any) (string, error))
	// SubscribeEvent 订阅面板事件（sitecheck.down / sitecheck.up / cert.issued / cert.expiring / cert.failed / site.created 等）。
	SubscribeEvent(topic string, fn func(payload map[string]any))
	// RegisterDNSProvider 注册证书 DNS 提供商（出现在申请向导中）。
	RegisterDNSProvider(name, title string, fields []FieldDef, factory func(cfg map[string]string) (DNSProvider, error))
	// RegisterDeployTarget 注册证书部署目标。
	RegisterDeployTarget(typ, title string, fields []FieldDef, runner func(args DeployArgs) (string, error))
}
