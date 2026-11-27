package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zjpanel/backend/internal/pkg"

	"gorm.io/gorm"
)

// Setting 键：远程存储配置。
const settingRemoteKey = "backup.remote"

// 备份可选项（前端勾选；全选即完整配置迁移）。
// 注意：这里是「配置级迁移」——打包面板数据库、nginx/证书/策略配置与 compose 声明，
// 不含站点上传文件、Docker 数据卷与业务数据库导出（后续按需扩展）。
const (
	ItemDatabase   = "database"    // SQLite 数据库（站点/主机/证书任务/插件设置/审计等全部记录）
	ItemCerts      = "certs"      // DataDir/certs 下的 ACME 证书与私钥
	ItemSecurity   = "security"    // 安全中心 / WAF 策略（policy.json、封禁与白名单）
	ItemPlugins    = "plugins"     // 插件运行数据 DataDir/plugins-data
	ItemNginx      = "nginx"      // nginx 站点配置（sites-available / sites-enabled / conf.d）
	ItemCompose    = "compose"    // ComposeRoot 下各项目目录的 docker-compose.yml 与 zjpanel.json
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
		{ItemCompose, "Compose 项目目录", "各应用目录的 docker-compose.yml / .yaml 与 zjpanel.json 声明", true},
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
	Source string `json:"source"` // 宿主机原始绝对路径
	Archive string `json:"archive"` // 包内相对路径（tar 内位置）
	Size   int64  `json:"size"`
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
	Enabled  bool   `json:"enabled"`
	Kind     string `json:"kind"` // local | webdav | googledrive | onedrive
	// WebDAV
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// OAuth 预留
	OAuthToken string `json:"oauthToken,omitempty"`
	RemoteDir  string `json:"remoteDir"`
}

// Service 备份与恢复服务。
type Service struct {
	cfg *pkg.Config
	db  *gorm.DB
	box *pkg.SecretBox
	// backupsDir 本地备份包存放目录（DataDir/backups）。
	backupsDir string
}

func New(cfg *pkg.Config, db *gorm.DB, box *pkg.SecretBox) *Service {
	return &Service{
		cfg:        cfg,
		db:         db,
		box:        box,
		backupsDir: filepath.Join(cfg.DataDir, "backups"),
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
