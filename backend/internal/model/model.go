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
}

// Site 站点（nginx vhost 的面板元数据；配置实体在 sites-available/<domain>.conf）
type Site struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Domain          string    `gorm:"size:255;uniqueIndex" json:"domain"`
	Type            string    `gorm:"size:16" json:"type"` // proxy | static
	Upstream        string    `gorm:"size:255" json:"upstream"`   // 127.0.0.1:port（proxy）
	Root            string    `gorm:"size:255" json:"root"`       // 静态目录（static）
	SSL             bool      `json:"ssl"`
	CertTaskID      *uint     `json:"certTaskId"`
	Advanced        bool      `json:"advanced"` // 高级模式：整文件 raw 编辑
	Maintenance     bool      `json:"maintenance"`
	MaintenanceHTML string    `json:"maintenanceHtml"` // 站点级覆盖；空则用全局模板
	ComposeProjectID *uint    `json:"composeProjectId"`
	Remark          string    `gorm:"size:255" json:"remark"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ComposeProject 导入映射：项目目录保持原位，面板仅记录解析结果与关联站点。
type ComposeProject struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128;index" json:"name"`
	Dir       string    `gorm:"size:512;uniqueIndex" json:"dir"` // docker-compose.yml 所在目录
	Services  string    `gorm:"type:text" json:"services"`       // JSON: [{name,image,ports:[]}]
	SiteID    *uint     `json:"siteId"`
	Imported  bool      `json:"imported"`
	CreatedAt time.Time `json:"createdAt"`
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
	ID       uint    `gorm:"primaryKey" json:"id"`
	Time     time.Time `gorm:"index" json:"time"`
	CPU      float64 `json:"cpu"`
	MemUsed  uint64  `json:"memUsed"`
	MemTotal uint64  `json:"memTotal"`
	NetIn    uint64  `json:"netIn"`
	NetOut   uint64  `json:"netOut"`
	Load1    float64 `json:"load1"`
}

type AuditLog struct {
	ID     uint      `gorm:"primaryKey" json:"id"`
	Time   time.Time `json:"time"`
	Action string    `gorm:"size:128" json:"action"`
	Detail string    `gorm:"type:text" json:"detail"`
	IP     string    `gorm:"size:64" json:"ip"`
}
