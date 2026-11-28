package model

import (
	"encoding/json"
	"strings"
	"time"
)

type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;uniqueIndex" json:"username"`
	Password  string    `json:"-"` // bcrypt
	CreatedAt time.Time `json:"createdAt"`
}

type Setting struct {
	Key   string `gorm:"primaryKey;size:128" json:"key"`
	Value string `json:"value"`
}

// ApiKey MCP 访问密钥：仅存哈希与前缀，明文只在创建时展示一次。
type ApiKey struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:64" json:"name"`
	Prefix     string     `gorm:"size:16;index" json:"prefix"`
	Hash       string     `gorm:"size:64;uniqueIndex" json:"-"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// SshHost 主机：既是 SSH 终端的目标，也是 Agent 管理的宿主。
// 一张表承载两种形态（未装 Agent / 已装 Agent），避免「终端主机」与「受管主机」两套清单。
type SshHost struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Name          string    `gorm:"size:64" json:"name"`
	Host          string    `gorm:"size:255" json:"host"`
	Port          int       `json:"port"`
	Username      string    `gorm:"size:64" json:"username"`
	AuthType      string    `gorm:"size:16" json:"authType"` // password | key
	PasswordEnc   string    `json:"-"`
	PrivateKeyEnc string    `json:"-"` // AES-GCM
	PassphraseEnc string    `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`

	// ---- 主机分组与 Agent 状态 ----
	Group         string     `gorm:"size:64" json:"group"`
	Tags          string     `gorm:"size:255" json:"tags"` // 逗号分隔
	Local         bool       `json:"local"`                // 内置「本机」伪主机：永不卸载、无 SSH。注意：旧库加列前的历史行此值为 NULL（启动迁移会归一为 0），新增 SQL 过滤须兼容 `OR local IS NULL`
	AgentID       string     `gorm:"size:64;index" json:"agentId"`
	AgentStatus   string     `gorm:"size:16" json:"agentStatus"` // none|pending|online|offline|uninstalled
	AgentVersion  string     `gorm:"size:32" json:"agentVersion"`
	AgentProto    string     `gorm:"size:16" json:"agentProto"`
	AgentLastSeen *time.Time `json:"agentLastSeen"`
	RemoteIP      string     `gorm:"size:64" json:"remoteIp"`
	OSArch        string     `gorm:"size:64" json:"osArch"`
	TokenHash     string     `gorm:"size:64;index" json:"-"` // sha256(host token)，明文只在 Agent 配置里
	Claimed       bool       `json:"claimed"`                // 命令安装的主机：回连后等待人工认领
}

// AgentCommand 一次下发给 Agent 的指令（审计 + 幂等 + 超时重试）。
type AgentCommand struct {
	ID        string     `gorm:"primaryKey;size:32" json:"id"`
	HostID    uint       `gorm:"index" json:"hostId"`
	Kind      string     `gorm:"size:32" json:"kind"`
	Params    string     `gorm:"type:text" json:"params"`
	Status    string     `gorm:"size:16" json:"status"` // queued|sent|ok|failed|timeout
	Output    string     `gorm:"type:text" json:"output"`
	Error     string     `gorm:"type:text" json:"error"`
	Attempts  int        `json:"attempts"`
	CreatedAt time.Time  `json:"createdAt"`
	AckedAt   *time.Time `json:"ackedAt"`
}

// HostCheck 主机防御检查的最近结果（按主机 + 检查类型唯一），
// 页面刷新/面板重启后仍能展示上一次检查，而不是退回「未检查」。
type HostCheck struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	HostID    uint      `gorm:"uniqueIndex:idx_host_check" json:"hostId"`
	Kind      string    `gorm:"size:32;uniqueIndex:idx_host_check" json:"kind"`
	CheckedAt time.Time `json:"checkedAt"`
	OK        bool      `json:"ok"`
	Result    string    `gorm:"type:text" json:"result"` // Agent 返回的 JSON
	Error     string    `gorm:"type:text" json:"error"`
}

// QuickCmd 终端页常用命令（点击即发送到当前 SSH 会话执行）。
type QuickCmd struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128" json:"name"`
	Command   string    `gorm:"type:text" json:"command"`
	Color     string    `gorm:"size:16" json:"color"` // chip 背景色（可空）
	Usage     int       `json:"usage"`
	CreatedAt time.Time `json:"createdAt"`
}

// Site 站点（nginx vhost 的面板元数据；配置实体在 sites-available/<domain>.conf）
type Site struct {
	ID               uint   `gorm:"primaryKey" json:"id"`
	Domain           string `gorm:"size:255;uniqueIndex" json:"domain"`
	Type             string `gorm:"size:16" json:"type"`      // proxy | static
	Upstream         string `gorm:"size:255" json:"upstream"` // 127.0.0.1:port（proxy）
	Root             string `gorm:"size:255" json:"root"`     // 静态目录（static）
	SSL              bool   `json:"ssl"`
	CertTaskID       *uint  `json:"certTaskId"`
	// 手动指定证书路径（成对填写才生效）；空 = 自动约定 <NginxPrefix>/ssl/<域名>/。
	// 「从证书仓库选择」不落库：保存时一次性复制到约定目录，续期由部署目标覆盖同目录。
	SSLCertPath string `gorm:"size:255" json:"sslCertPath"`
	SSLKeyPath  string `gorm:"size:255" json:"sslKeyPath"`
	Advanced         bool   `json:"advanced"` // 高级模式：整文件 raw 编辑
	Maintenance      bool   `json:"maintenance"`
	MaintenanceHTML  string `json:"maintenanceHtml"` // 站点级覆盖；空则用全局模板
	ComposeProjectID *uint  `json:"composeProjectId"`
	Remark           string `gorm:"size:255" json:"remark"`
	// 最近一次探测结果缓存：进程重启后站点列表仍能显示上次状态，探测服务启动后覆盖。
	CheckStatus    string     `gorm:"size:16" json:"checkStatus,omitempty"`
	CheckCode      int        `json:"checkCode,omitempty"`
	CheckLatencyMs int64      `json:"checkLatencyMs,omitempty"`
	CheckError     string     `gorm:"size:255" json:"checkError,omitempty"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// ComposeProject 导入映射：项目目录保持原位，面板仅记录解析结果与关联站点。
type ComposeProject struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:128;index" json:"name"`
	Dir      string `gorm:"size:512;uniqueIndex" json:"dir"` // docker-compose.yml 所在目录
	Services string `gorm:"type:text" json:"services"`       // JSON: [{name,image,ports:[]}]
	SiteID   *uint  `json:"siteId"`
	Imported bool   `json:"imported"`
	Hidden   bool   `gorm:"index;default:false" json:"hidden"` // 已从面板删除：扫描/列表跳过，重新导入或「恢复」后可见
	// 应用运维状态：正常（默认）/ 停用。停用表示应用是人工关停的——其关联网站掉线不告警、
	// 不计入安全中心态势；探测仍按常规频率进行，只是结果被标记为「不告警」。
	Disabled   bool       `gorm:"index;default:false" json:"disabled"`
	DisabledAt *time.Time `json:"disabledAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// AcmeAccount ACME 账户（certd 的 Access 概念）
type AcmeAccount struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Email     string    `gorm:"size:128" json:"email"`
	Type      string    `gorm:"size:16" json:"type"` // staging | prod | custom
	DirURL    string    `gorm:"size:255" json:"dirUrl"`
	Reg       string    `gorm:"size:255" json:"-"` // ACME 注册 URI（空=未注册）
	KeyEnc    string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

// DnsCredential DNS 提供商凭证
type DnsCredential struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64" json:"name"`
	Provider  string    `gorm:"size:64" json:"provider"` // 注册表中的 provider name
	ConfigEnc string    `gorm:"type:text" json:"-"`      // JSON（AES-GCM）
	CreatedAt time.Time `json:"createdAt"`
}

// CertTask 证书申请任务（certd 式管线定义）
type CertTask struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:128" json:"name"`
	Domains         string    `gorm:"type:text" json:"domains"` // JSON array，支持 *.example.com
	Challenge       string    `gorm:"size:16" json:"challenge"` // http | dns
	AcmeAccountID   uint      `json:"acmeAccountId"`
	DnsCredentialID *uint     `json:"dnsCredentialId"`
	RenewDays       int       `json:"renewDays"` // 临近到期 N 天内续期
	CronSpec        string    `gorm:"size:64;column:cron_spec" json:"cronSpec"`
	Enabled         bool      `json:"enabled"`
	DeployTargets   string    `gorm:"type:text" json:"deployTargets"` // JSON: [{type, config}]
	LastRunID       *uint     `json:"lastRunId"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (t *CertTask) DomainList() []string {
	var out []string
	if t.Domains == "" {
		return out
	}
	if err := json.Unmarshal([]byte(t.Domains), &out); err == nil {
		return out
	}
	return strings.Split(t.Domains, ",")
}

func (t *CertTask) RenewDaysOrDefault() int {
	if t.RenewDays <= 0 {
		return 20
	}
	return t.RenewDays
}

// CertInfo 证书仓库
type CertInfo struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TaskID    *uint     `gorm:"index" json:"taskId"`
	Domains   string    `gorm:"type:text" json:"domains"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	Status    string    `gorm:"size:16" json:"status"` // active | inactive
	CertPath  string    `gorm:"size:512" json:"certPath"`
	KeyPath   string    `gorm:"size:512" json:"keyPath"`
	IssuedAt  time.Time `json:"issuedAt"`
}

// CertRun 一次任务执行（含各节点日志）
type CertRun struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TaskID     uint       `gorm:"index" json:"taskId"`
	Status     string     `gorm:"size:16" json:"status"` // running | success | error | skip
	Log        string     `gorm:"type:text" json:"log"`  // 追加式 JSONL/文本日志
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	CertInfoID *uint      `json:"certInfoId"`
}

// PluginRecord 已装插件
type PluginRecord struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	PluginID    string    `gorm:"size:64;uniqueIndex" json:"pluginId"`
	Name        string    `gorm:"size:128" json:"name"`
	Version     string    `gorm:"size:32" json:"version"`
	Enabled     bool      `json:"enabled"`
	Builtin     bool      `json:"builtin"`
	Manifest    string    `gorm:"type:text" json:"manifest"` // plugin.yaml 原文
	InstalledAt time.Time `json:"installedAt"`
}

// PluginSetting 插件独立设置（KV）
type PluginSetting struct {
	PluginID string `gorm:"primaryKey;size:64" json:"pluginId"`
	Key      string `gorm:"primaryKey;size:128" json:"key"`
	Value    string `gorm:"type:text" json:"value"`
}

// MetricSample 监控历史采样
type MetricSample struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	Time     time.Time `gorm:"index" json:"time"`
	CPU      float64   `json:"cpu"`
	MemUsed  uint64    `json:"memUsed"`
	MemTotal uint64    `json:"memTotal"`
	NetIn    uint64    `json:"netIn"`
	NetOut   uint64    `json:"netOut"`
	Load1    float64   `json:"load1"`
}

type AuditLog struct {
	ID     uint      `gorm:"primaryKey" json:"id"`
	Time   time.Time `json:"time"`
	Action string    `gorm:"size:128" json:"action"`
	Detail string    `gorm:"type:text" json:"detail"`
	IP     string    `gorm:"size:64" json:"ip"`
}

// Notification 统一通知（站内信）：Type 即消息类型（事件主题/插件自定义），ReadAt 为空表示未读。
type Notification struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Type      string     `gorm:"size:64;index" json:"type"`
	Level     string     `gorm:"size:16" json:"level"` // info | warn | error
	Title     string     `gorm:"size:255" json:"title"`
	Body      string     `gorm:"type:text" json:"body"`
	Source    string     `gorm:"size:64" json:"source"` // core 或插件 ID
	CreatedAt time.Time  `json:"createdAt" gorm:"index"` // 30 天清理的 DELETE 条件
	ReadAt    *time.Time `json:"readAt" gorm:"index"`    // 未读 COUNT 轮询的过滤列（无索引时全表扫）
}

// OpsAgentSession AI 运维会话。会话在后端 goroutine 里运行（前端只是视图），
// 页面刷新/关闭不影响执行；面板重启后 running 会话被标记 interrupted，可发消息续聊。
type OpsAgentSession struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Title       string    `gorm:"size:255" json:"title"`
	Source      string    `gorm:"size:64" json:"source"`       // manual | event:<topic>
	Status      string    `gorm:"size:16;index" json:"status"` // running|waiting_confirm|idle|interrupted|error
	DefenseMode string    `gorm:"size:16" json:"defenseMode"`  // 会话创建时的防御模式快照：observe|confirm|auto
	HostID      *uint     `json:"hostId"`                      // 可选：会话绑定的主机上下文
	Error       string    `gorm:"type:text" json:"error"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// OpsAgentMessage 会话消息。role=user|assistant|tool|event；tool 消息记录工具名/参数，
// Meta 存 JSON（confirm 模式下待确认动作的完整信息，前端据此渲染确认卡片）。
type OpsAgentMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID uint      `gorm:"index:idx_ops_msg_session" json:"sessionId"`
	Role      string    `gorm:"size:16;index:idx_ops_msg_session" json:"role"`
	Content   string    `gorm:"type:text" json:"content"`
	ToolName  string    `gorm:"size:128" json:"toolName,omitempty"`
	ToolArgs  string    `gorm:"type:text" json:"toolArgs,omitempty"`
	Meta      string    `gorm:"type:text" json:"meta,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// OpsAgentMemory 智能体持久记忆：跨会话生效。Scope=global 全局事实 / host 主机事实（RefID=主机ID）。
type OpsAgentMemory struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Scope     string    `gorm:"size:16;index" json:"scope"` // global | host
	RefID     string    `gorm:"size:32;index" json:"refId"` // host 作用域的主机 ID
	Key       string    `gorm:"size:255" json:"key"`
	Content   string    `gorm:"type:text" json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
