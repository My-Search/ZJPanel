package site

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
)

// Service 站点管理：DB 元数据 + sites-available conf 生成落盘 + symlink 状态切换 + 维护页。

type Service struct {
	cfg *pkg.Config
	db  *gorm.DB
	ng  *nginx.Service
	// OnChange 站点变更钩子（create/update/delete/state 后调用；boot 装配为
	// compose 声明文件同步——面板侧变化自动写回 zjpanel.json）。
	OnChange func(site *model.Site)
}

func New(cfg *pkg.Config, db *gorm.DB, ng *nginx.Service) *Service {
	return &Service{cfg: cfg, db: db, ng: ng}
}

func (s *Service) notifyChange(site *model.Site) {
	if s.OnChange != nil {
		func() {
			defer func() { _ = recover() }()
			s.OnChange(site)
		}()
	}
}

// SSLDir 站点证书约定目录：/etc/nginx/ssl/<domain>/（证书部署目标写入这里）
func (s *Service) SSLDir(domain string) string {
	return filepath.Join(s.cfg.NginxPrefix, "ssl", domain)
}

func (s *Service) SSLCertPath(domain string) string  { return filepath.Join(s.SSLDir(domain), "fullchain.pem") }
func (s *Service) SSLKeyPath(domain string) string   { return filepath.Join(s.SSLDir(domain), "privkey.pem") }

// ---- conf 生成 ----

func (s *Service) challengeBlock() string {
	return fmt.Sprintf(`    location /.well-known/acme-challenge/ {
        proxy_pass http://127.0.0.1:%d;
        proxy_set_header Host $host;
    }`, s.cfg.ChallengePort)
}

func (s *Service) headersBlock() string {
	return `        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;`
}

// GenerateProxyConf 生成反代站点 conf；ssl=true 时生成 443 server（80 跳转）。
func (s *Service) GenerateProxyConf(domain, upstream string, ssl bool) string {
	var b strings.Builder
	if !ssl {
		b.WriteString(fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

%s

    location / {
        proxy_pass http://%s;
%s
    }
}
`, domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, s.challengeBlock(), upstream, s.headersBlock()))
		return b.String()
	}
	cert, key := s.SSLCertPath(domain), s.SSLKeyPath(domain)
	fmt.Fprintf(&b, `server {
    listen 80;
    listen [::]:80;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

%s

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

    ssl_certificate     %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;

    location / {
        proxy_pass http://%s;
%s
    }
}
`, domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, s.challengeBlock(),
		domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, cert, key, upstream, s.headersBlock())
	return b.String()
}

// GenerateStaticConf 静态站点 conf（error_page 指向自定义默认页）
func (s *Service) GenerateStaticConf(domain, root string, ssl bool) string {
	var b strings.Builder
	if !ssl {
		fmt.Fprintf(&b, `server {
    listen 80;
    listen [::]:80;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

%s

    root %s;
    index index.html;

    location / {
        try_files $uri $uri/ =404;
    }

    error_page 404 /404.html;
    error_page 500 502 503 504 /50x.html;
    location = /404.html { root %s; }
    location = /50x.html { root %s; }
}
`, domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, s.challengeBlock(), root, s.cfg.NginxHTMLDir, s.cfg.NginxHTMLDir)
		return b.String()
	}
	cert, key := s.SSLCertPath(domain), s.SSLKeyPath(domain)
	fmt.Fprintf(&b, `server {
    listen 80;
    listen [::]:80;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

%s

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name %s;
    access_log %s/%s.access.log zjp;
    error_log  %s/%s.error.log;

    ssl_certificate     %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;

    root %s;
    index index.html;

    location / {
        try_files $uri $uri/ =404;
    }

    error_page 404 /404.html;
    error_page 500 502 503 504 /50x.html;
    location = /404.html { root %s; }
    location = /50x.html { root %s; }
}
`, domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, s.challengeBlock(),
		domain, s.cfg.NginxLogsDir, domain, s.cfg.NginxLogsDir, domain, cert, key, root, s.cfg.NginxHTMLDir, s.cfg.NginxHTMLDir)
	return b.String()
}

// GenerateMaintenanceConf 维护态专用 conf：全部请求代理到面板维护页渲染器。
func (s *Service) GenerateMaintenanceConf(domain string, ssl bool) string {
	serverBlock := func(listenLine, extra string) string {
		return fmt.Sprintf(`    listen %s;
    server_name %s;

%s
%s
    location / {
        proxy_pass http://127.0.0.1:%d/_maintenance/%s;
        proxy_set_header Host $host;
        proxy_set_header X-Maintenance-Domain %s;
    }
}
`, listenLine, domain, s.challengeBlock(), extra, s.cfg.Port, domain, domain)
	}
	if !ssl {
		// serverBlock 自带闭合 "}"，此处补 "server {" 开头（原实现多补了一个 "}" 导致 nginx -t 失败）
		return "server {\n" + serverBlock("80;\n    listen [::]:80", "")
	}
	cert, key := s.SSLCertPath(domain), s.SSLKeyPath(domain)
	sslExtra := fmt.Sprintf(`    ssl_certificate     %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;`, cert, key)
	var b strings.Builder
	fmt.Fprintf(&b, `server {
    listen 80;
    listen [::]:80;
    server_name %s;

%s

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
%s
`, domain, s.challengeBlock(), serverBlock("443 ssl;\n    listen [::]:443 ssl", sslExtra))
	return b.String()
}

// ---- CRUD ----

type SaveReq struct {
	Domain          string  `json:"domain"`
	Type            string  `json:"type"` // proxy | static
	Upstream        string  `json:"upstream"`
	Root            string  `json:"root"`
	SSL             bool    `json:"ssl"`
	MaintenanceHTML string  `json:"maintenanceHtml"`
	Remark          string  `json:"remark"`
	ComposeProjectID *uint  `json:"composeProjectId"`
}

func validateDomain(d string) error {
	if d == "" || len(d) > 253 || strings.ContainsAny(d, " /\\") || !strings.Contains(d, ".") {
		return fmt.Errorf("非法域名: %s", d)
	}
	return nil
}

// Create 新建站点：写 conf → 启用 → nginx -t → reload，失败整体回退。
func (s *Service) Create(req SaveReq) (*model.Site, error) {
	if err := validateDomain(req.Domain); err != nil {
		return nil, err
	}
	var cnt int64
	s.db.Model(&model.Site{}).Where("domain = ?", req.Domain).Count(&cnt)
	if cnt > 0 {
		return nil, fmt.Errorf("站点已存在: %s", req.Domain)
	}
	if req.Type == "proxy" && req.Upstream == "" {
		return nil, fmt.Errorf("反代目标不能为空")
	}
	if req.Type == "static" && req.Root == "" {
		req.Root = filepath.Join(s.cfg.NginxHTMLDir, "sites", req.Domain)
		_ = os.MkdirAll(req.Root, 0o755)
	}
	site := &model.Site{
		Domain: req.Domain, Type: req.Type, Upstream: req.Upstream, Root: req.Root,
		SSL: req.SSL, MaintenanceHTML: req.MaintenanceHTML, Remark: req.Remark,
		ComposeProjectID: req.ComposeProjectID,
	}
	if err := s.db.Create(site).Error; err != nil {
		return nil, err
	}
	if err := s.writeSiteConf(site); err != nil {
		s.db.Delete(site)
		return nil, err
	}
	if err := s.ng.EnableSite(site.Domain); err != nil {
		_ = os.Remove(s.ng.AvailablePath(site.Domain))
		s.db.Delete(site)
		return nil, err
	}
	if _, err := s.ng.Reload(); err != nil {
		s.rollback(site)
		return nil, err
	}
	s.notifyChange(site)
	return site, nil
}

func (s *Service) rollback(site *model.Site) {
	_ = s.ng.DisableSite(site.Domain)
	_ = os.Remove(s.ng.AvailablePath(site.Domain))
	s.db.Delete(site)
}

// writeSiteConf 按 site 高级模式/生成模式落盘 conf。
func (s *Service) writeSiteConf(site *model.Site) error {
	if site.Advanced {
		// 高级模式：conf 以文件为准，不覆盖
		if _, err := os.Stat(s.ng.AvailablePath(site.Domain)); err == nil {
			return nil
		}
	}
	content := s.generateForSite(site)
	if err := os.WriteFile(s.ng.AvailablePath(site.Domain), []byte(content), 0o644); err != nil {
		return err
	}
	// 维护开启时同步维护 conf
	if site.Maintenance {
		return s.WriteMaintenanceConf(site)
	}
	return nil
}

// ensureSiteSSL 站点开启 SSL 时，证书目录缺失则从匹配的泛域名证书目录回退复制。
// 约定：泛域名证书部署在 /etc/nginx/ssl/<主域>/，站点引用 /etc/nginx/ssl/<站点域名>/。
func (s *Service) ensureSiteSSL(site *model.Site) {
	dir := s.SSLDir(site.Domain)
	certPath := s.SSLCertPath(site.Domain)
	if _, err := os.Stat(certPath); err == nil {
		return // 已有证书
	}
	labels := strings.Split(site.Domain, ".")
	for i := 1; i < len(labels)-1; i++ {
		wildDir := filepath.Join(s.cfg.NginxPrefix, "ssl", strings.Join(labels[i:], "."))
		ci, err1 := os.Stat(filepath.Join(wildDir, "fullchain.pem"))
		ki, err2 := os.Stat(filepath.Join(wildDir, "privkey.pem"))
		if err1 == nil && err2 == nil && !ci.IsDir() && !ki.IsDir() {
			if err := os.MkdirAll(dir, 0o755); err == nil {
				data, _ := os.ReadFile(filepath.Join(wildDir, "fullchain.pem"))
				if len(data) > 0 {
					_ = os.WriteFile(certPath, data, 0o644)
				}
				kdata, _ := os.ReadFile(filepath.Join(wildDir, "privkey.pem"))
				if len(kdata) > 0 {
					_ = os.WriteFile(s.SSLKeyPath(site.Domain), kdata, 0o600)
				}
			}
			return
		}
	}
}

func (s *Service) generateForSite(site *model.Site) string {
	if site.Type == "static" {
		return s.GenerateStaticConf(site.Domain, site.Root, site.SSL)
	}
	return s.GenerateProxyConf(site.Domain, site.Upstream, site.SSL)
}

// Update 修改站点：非高级模式重新生成 conf；高级模式仅更新元数据。
func (s *Service) Update(id uint, req SaveReq) (*model.Site, error) {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return nil, fmt.Errorf("站点不存在")
	}
	if err := validateDomain(site.Domain); err != nil {
		return nil, err
	}
	site.Type = req.Type
	site.Upstream = req.Upstream
	site.Root = req.Root
	site.SSL = req.SSL
	site.MaintenanceHTML = req.MaintenanceHTML
	site.Remark = req.Remark
	// 开启 SSL 时若站点证书目录缺失，尝试从匹配的泛域名证书目录回退复制
	if site.SSL && !site.Advanced {
		s.ensureSiteSSL(&site)
	}
	if req.ComposeProjectID != nil {
		site.ComposeProjectID = req.ComposeProjectID
	}
	if err := s.db.Save(&site).Error; err != nil {
		return nil, err
	}
	if !site.Advanced {
		if err := s.writeSiteConf(&site); err != nil {
			return nil, err
		}
	}
	if _, err := s.ng.Reload(); err != nil {
		return nil, err
	}
	s.notifyChange(&site)
	return &site, nil
}

// Delete 删除站点：下线 + 删 conf + 删记录。
func (s *Service) Delete(id uint) error {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return fmt.Errorf("站点不存在")
	}
	_ = s.ng.SetMaintenance(site.Domain, false)
	_ = s.ng.DisableSite(site.Domain)
	_ = os.Remove(s.ng.AvailablePath(site.Domain))
	_, _ = s.ng.Reload()
	if err := s.db.Delete(&site).Error; err != nil {
		return err
	}
	s.notifyChange(&site)
	return nil
}

// SetState 切换站点状态（enable/disable/maintenance）
func (s *Service) SetState(id uint, state string) error {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return fmt.Errorf("站点不存在")
	}
	switch state {
	case "enable":
		site.Maintenance = false
		if res := s.db.Model(&site).Updates(map[string]any{"maintenance": false}); res.Error != nil {
			return res.Error
		}
		if err := s.ng.SetMaintenance(site.Domain, false); err != nil {
			return err
		}
		if err := s.ng.EnableSite(site.Domain); err != nil {
			return err
		}
	case "disable":
		site.Maintenance = false
		if res := s.db.Model(&site).Updates(map[string]any{"maintenance": false}); res.Error != nil {
			return res.Error
		}
		if err := s.ng.DisableSite(site.Domain); err != nil {
			return err
		}
	case "maintenance":
		if err := s.WriteMaintenanceConf(&site); err != nil {
			return err
		}
		site.Maintenance = true
		if err := s.db.Model(&site).Update("maintenance", true).Error; err != nil {
			return err
		}
		if err := s.ng.SetMaintenance(site.Domain, true); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知状态: %s", state)
	}
	_, err := s.ng.Reload()
	s.notifyChange(&site)
	return err
}

// WriteMaintenanceConf 渲染维护 conf 并落盘。
func (s *Service) WriteMaintenanceConf(site *model.Site) error {
	content := s.GenerateMaintenanceConf(site.Domain, site.SSL)
	return os.WriteFile(s.ng.MaintenancePath(site.Domain), []byte(content), 0o644)
}

// GetConf 读取站点 conf 原文（高级编辑用）。
func (s *Service) GetConf(id uint) (string, error) {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return "", fmt.Errorf("站点不存在")
	}
	b, err := os.ReadFile(s.ng.AvailablePath(site.Domain))
	if err != nil {
		if site.Advanced {
			return "", err
		}
		// 文件缺失时重新生成
		content := s.generateForSite(&site)
		return content, os.WriteFile(s.ng.AvailablePath(site.Domain), []byte(content), 0o644)
	}
	return string(b), nil
}

// SaveConf 高级模式保存 conf：写文件 → 测试 → reload（失败回滚）。
func (s *Service) SaveConf(id uint, content string) error {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return fmt.Errorf("站点不存在")
	}
	_, err := s.ng.SaveAndReload("nginx", "sites-available/"+site.Domain+".conf", content)
	if err == nil {
		s.db.Model(&site).Update("advanced", true)
	}
	return err
}

// SetAdvanced 切换高级模式；退出高级模式时按元数据重新生成 conf。
func (s *Service) SetAdvanced(id uint, advanced bool) error {
	var site model.Site
	if err := s.db.First(&site, id).Error; err != nil {
		return err
	}
	site.Advanced = advanced
	if err := s.db.Model(&site).Update("advanced", advanced).Error; err != nil {
		return err
	}
	if !advanced {
		if err := s.writeSiteConf(&site); err != nil {
			return err
		}
		if _, err := s.ng.Reload(); err != nil {
			return err
		}
	}
	return nil
}

// ReloadNginx 供 compose 等模块更新 conf 后统一重载。
func (s *Service) ReloadNginx() (string, error) { return s.ng.Reload() }

// GenerateFor 导出 conf 生成（compose 声明同步用）。
func (s *Service) GenerateFor(site model.Site) string { return s.generateForSite(&site) }

// List 全部站点。
func (s *Service) List() ([]model.Site, error) {
	var sites []model.Site
	err := s.db.Order("id").Find(&sites).Error
	return sites, err
}
