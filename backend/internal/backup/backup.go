package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"

	"gorm.io/gorm"
)

// Setting 键：远程存储配置。
const settingRemoteKey = "backup.remote"

// 备份可选项（前端勾选；全选即完整迁移）。
// 「配置级 + 应用目录」：打包面板数据库、nginx/证书/策略配置，以及 Compose 应用目录整目录
// （编排文件、配置与挂载的本地数据）；不含 Docker 命名数据卷与站点上传的静态大文件。
const (
	ItemDatabase = "database" // SQLite 数据库（站点/主机/证书任务/插件设置/审计等全部记录）
	ItemCerts    = "certs"    // DataDir/certs 下的 ACME 证书与私钥
	ItemSecurity = "security" // 安全中心 / WAF 策略（policy.json、封禁与白名单）
	ItemPlugins  = "plugins"  // 插件运行数据 DataDir/plugins-data
	ItemNginx    = "nginx"    // nginx 站点配置（sites-available / sites-enabled / conf.d）
	ItemCompose  = "compose"  // ComposeRoot 下各应用目录的完整文件（整目录递归）
)

// AllItems 全部可备份项（完整迁移默认全选）。
var AllItems = []string{ItemDatabase, ItemCerts, ItemSecurity, ItemPlugins, ItemNginx, ItemCompose}

// ItemMeta 备份项的展示元信息（前端渲染勾选清单用）。
type ItemMeta struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Desc    string `json:"desc"`
	Default bool   `json:"default"`
}

// ItemMetas 返回备份项清单（含中文标签与说明）。
func ItemMetas() []ItemMeta {
	return []ItemMeta{
		{ItemDatabase, "面板数据库", "SQLite 内的站点、主机、证书任务、插件设置、审计日志等全部记录", true},
		{ItemCerts, "证书与私钥", "ACME 签发的 fullchain.pem / privkey.pem", true},
		{ItemSecurity, "安全 / WAF 策略", "封禁名单、白名单、CC 限速与 WAF 规则配置", true},
		{ItemPlugins, "插件数据", "各插件在 DataDir/plugins-data 下的运行数据", true},
		{ItemNginx, "Nginx 站点配置", "sites-available、sites-enabled、conf.d 下的站点配置文件", true},
		{ItemCompose, "Compose 项目目录（整目录）", "含 docker-compose.yml 的应用目录整体打包：编排文件、配置与挂载的本地数据（不含 Docker 命名卷）", true},
	}
}

// Manifest 备份包清单：记录打包时间、版本、所选项目与包内文件清单。
type Manifest struct {
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
	PanelVer  string         `json:"panelVersion"`
	Items     []string       `json:"items"`
	Files     []ManifestFile `json:"files"`
}

// ManifestFile 包内单个文件的来源与归档路径。
type ManifestFile struct {
	Source  string `json:"source"`  // 宿主机原始绝对路径
	Archive string `json:"archive"` // 包内相对路径（tar 内位置）
	Size    int64  `json:"size"`
}

// StorageKind 远程存储类型。
const (
	StorageLocal  = "local"
	StorageWebDAV = "webdav"
	// 以下两类为 OAuth 体系，预留 Provider 位，后续接入。
	StorageGoogleDrive = "googledrive"
	StorageOneDrive    = "onedrive"
)

// RemoteConfig 远程存储配置（持久化到 Setting 表，凭据用 SecretBox 加密落库）。
type RemoteConfig struct {
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind"` // local | webdav | googledrive | onedrive
	// WebDAV
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// OAuth 云盘（Google Drive / OneDrive）
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"` // SecretBox 加密
	RefreshToken string `json:"refreshToken"` // SecretBox 加密
	Account      string `json:"account"`      // 已授权账号（邮箱/显示名），仅展示用
	RemoteDir    string `json:"remoteDir"`
}

// IsOAuth 是否为 OAuth 云盘通道。
func (rc RemoteConfig) IsOAuth() bool {
	return rc.Kind == StorageGoogleDrive || rc.Kind == StorageOneDrive
}

// Service 备份与恢复服务。
type Service struct {
	cfg *pkg.Config
	db  *gorm.DB
	box *pkg.SecretBox
	// backupsDir 本地备份包存放目录（DataDir/backups）。
	backupsDir string

	hub *ws.Hub // WS 进度广播（可为 nil：单测/无推送场景静默降级）
	bus *bus.Bus

	// 任务注册表与全局互斥：同一时刻只允许一个备份任务。
	jobsMu   sync.Mutex
	jobs     []*BackupJob // 新→旧
	activeMu sync.Mutex
	active   bool

	// 定时备份调度器（boot 注入共享 cron；可为 nil，保存配置仍生效仅不调度）。
	schedMu sync.Mutex
	sched   Scheduler
	schedID any
}

func New(cfg *pkg.Config, db *gorm.DB, box *pkg.SecretBox, hub *ws.Hub, eventBus *bus.Bus) *Service {
	return &Service{
		cfg:        cfg,
		db:         db,
		box:        box,
		backupsDir: filepath.Join(cfg.DataDir, "backups"),
		hub:        hub,
		bus:        eventBus,
	}
}

// BackupsDir 本地备份目录。
func (s *Service) BackupsDir() string { return s.backupsDir }

// NormalizeItems 校验并归一勾选项目：未知项剔除，空列表回退为全选。
func NormalizeItems(items []string) []string {
	valid := map[string]bool{}
	for _, it := range AllItems {
		valid[it] = true
	}
	out := make([]string, 0, len(AllItems))
	seen := map[string]bool{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if valid[it] && !seen[it] {
			out = append(out, it)
			seen[it] = true
		}
	}
	if len(out) == 0 {
		out = append(out, AllItems...)
	}
	sort.Strings(out)
	return out
}

// backupName 生成备份包文件名：zjpanel-backup-20060102-150405.tar.gz
func backupName(now time.Time) string {
	return fmt.Sprintf("zjpanel-backup-%s.tar.gz", now.Format("20060102-150405"))
}

// writeManifest 写清单到备份目录旁的 .json（便于不拆包即可查看内容）。
func writeManifest(dir, name string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	meta := strings.TrimSuffix(name, ".tar.gz") + ".json"
	return os.WriteFile(filepath.Join(dir, meta), b, 0o644)
}
