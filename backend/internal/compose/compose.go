package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/site"
)

// Service docker-compose 目录扫描与「导入为网站」：
// 扫描 ComposeRoot 下含 docker-compose.yml 的一级目录，解析服务与发布端口；
// 导入时生成反代站点并持久化映射（项目目录保持原位，重扫描可再同步）。

type Service struct {
	cfg  *pkg.Config
	db   *gorm.DB
	site *site.Service
}

func New(cfg *pkg.Config, db *gorm.DB, siteSvc *site.Service) *Service {
	return &Service{cfg: cfg, db: db, site: siteSvc}
}

type ServiceInfo struct {
	Name          string   `json:"name"`
	Image         string   `json:"image"`
	ContainerName string   `json:"containerName"`
	Ports         []string `json:"ports"` // 已发布端口 "8080:80"
}

type ProjectInfo struct {
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	Services []ServiceInfo `json:"services"`
	Imported bool          `json:"imported"`
	SiteID   *uint         `json:"siteId"`
	ID       uint          `json:"id"`
	HasDecl  bool          `json:"hasDecl"`  // 存在 zjpanel.json（声明式导入）
	Decl     *DeclFile     `json:"decl,omitempty"`
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image         string      `yaml:"image"`
	ContainerName string      `yaml:"container_name"`
	Ports         []yaml.Node `yaml:"ports"`
}

var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

func composeFileName(dir string) string {
	for _, n := range composeNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return n
		}
	}
	return "docker-compose.yml"
}

// parseCompose 最小化解析 compose 文件（项目名/服务/发布端口），不依赖完整 compose-go。
func parseCompose(dir string) (*composeFile, error) {
	for _, name := range composeNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var f composeFile
		if err := yaml.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s 解析失败: %w", name, err)
		}
		return &f, nil
	}
	return nil, fmt.Errorf("目录中未找到 docker-compose.yml")
}

// servicesOf 归一化端口条目（字符串 "8080:80" 或映射 {published,target}）
func servicesOf(f *composeFile) []ServiceInfo {
	var out []ServiceInfo
	for name, sv := range f.Services {
		info := ServiceInfo{Name: name, Image: sv.Image, ContainerName: sv.ContainerName}
		for _, pn := range sv.Ports {
			if pn.Kind == yaml.ScalarNode {
				info.Ports = append(info.Ports, pn.Value)
				continue
			}
			if pn.Kind == yaml.MappingNode {
				var pm struct {
					Published string `yaml:"published"`
					Target    string `yaml:"target"`
				}
				if err := pn.Decode(&pm); err == nil && pm.Published != "" {
					info.Ports = append(info.Ports, pm.Published+":"+pm.Target)
				}
			}
		}
		out = append(out, info)
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Scan 扫描根目录（一级），返回项目列表并同步 DB。
func (s *Service) Scan() ([]ProjectInfo, error) {
	var out []ProjectInfo
	entries, err := os.ReadDir(s.cfg.ComposeRoot)
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.ComposeRoot, e.Name())
		f, err := parseCompose(dir)
		if err != nil {
			continue
		}
		pi := ProjectInfo{Name: firstNonEmpty(f.Name, e.Name()), Dir: dir, Services: servicesOf(f)}
		if decl, derr := s.ReadDecl(dir); derr == nil && decl != nil {
			pi.HasDecl, pi.Decl = true, decl
		}
		var rec model.ComposeProject
		if err := s.db.Where("dir = ?", dir).First(&rec).Error; err == nil {
			pi.ID, pi.Imported, pi.SiteID = rec.ID, rec.Imported, rec.SiteID
		}
		out = append(out, pi)
	}
	return out, nil
}

// SyncDB 扫描并入库：新目录创建记录，已有记录刷新服务解析结果。
func (s *Service) SyncDB() ([]ProjectInfo, error) {
	list, err := s.Scan()
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		var rec model.ComposeProject
		if err := s.db.Where("dir = ?", p.Dir).First(&rec).Error; err != nil {
			rec = model.ComposeProject{Name: p.Name, Dir: p.Dir, Services: mustJSON(p.Services)}
			if err := s.db.Create(&rec).Error; err != nil {
				return nil, err
			}
		} else {
			s.db.Model(&rec).Updates(map[string]any{"services": mustJSON(p.Services), "name": p.Name})
		}
	}
	return list, nil
}

// ListProjects DB 项目记录（含存在性核对）。
func (s *Service) ListProjects() ([]map[string]any, error) {
	var recs []model.ComposeProject
	if err := s.db.Order("id").Find(&recs).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		_, statErr := os.Stat(r.Dir)
		out = append(out, map[string]any{
			"id": r.ID, "name": r.Name, "dir": r.Dir,
			"services": json.RawMessage(func() string {
				if r.Services == "" {
					return "[]"
				}
				return r.Services
			}()),
			"exists": statErr == nil, "imported": r.Imported,
			"siteId": r.SiteID, "createTime": r.CreatedAt,
		})
	}
	return out, nil
}

// Import 导入 compose 项目的一个服务为反代站点（目录任意，只需已挂载进容器且含 compose 文件）。
func (s *Service) Import(dir, serviceName, port, domain string, ssl bool) (*model.Site, error) {
	clean := filepath.Clean(dir)
	if !isAbsPath(clean) {
		return nil, fmt.Errorf("需要绝对路径（宿主机目录经挂载映射进容器，默认 /opt:/opt）")
	}
	if strings.Contains(clean, "..") {
		return nil, fmt.Errorf("非法路径")
	}
	if _, err := os.Stat(clean); err != nil {
		return nil, fmt.Errorf("目录不存在（确认已挂载进容器）: %s", clean)
	}
	f, err := parseCompose(clean)
	if err != nil {
		return nil, err
	}
	if port == "" {
		return nil, fmt.Errorf("必须选择要反代的端口")
	}
	services := servicesOf(f)
	okName, okPort := false, false
	for _, sv := range services {
		if sv.Name == serviceName {
			okName = true
		}
		for _, p := range sv.Ports {
			if p == port || strings.HasPrefix(p, port+":") || strings.HasSuffix(p, ":"+port) {
				okPort = true
			}
		}
	}
	if !okName && !okPort {
		return nil, fmt.Errorf("服务 %q 或端口 %q 不在该 compose 项目中", serviceName, port)
	}
	upstream := "127.0.0.1:" + port

	var rec model.ComposeProject
	if err := s.db.Where("dir = ?", clean).First(&rec).Error; err != nil {
		rec = model.ComposeProject{Name: firstNonEmpty(f.Name, filepath.Base(clean)), Dir: clean, Services: mustJSON(services)}
		if err := s.db.Create(&rec).Error; err != nil {
			return nil, err
		}
	}
	siteRec, err := s.site.Create(site.SaveReq{
		Domain: domain, Type: "proxy", Upstream: upstream, SSL: ssl,
		ComposeProjectID: &rec.ID, Remark: "compose: " + serviceName,
	})
	if err != nil {
		return nil, err
	}
	s.db.Model(&rec).Updates(map[string]any{"imported": true, "site_id": siteRec.ID})
	// 兼容导入：写出 zjpanel.json（下次导入/迁移直接读目录）
	_ = s.WriteDecl(rec.ID)
	return siteRec, nil
}

// Action 项目生命周期：up/down/restart/logs（docker compose CLI）
func (s *Service) Action(id uint, action string) (string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, id).Error; err != nil {
		return "", fmt.Errorf("项目不存在")
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		return "", fmt.Errorf("项目目录不存在: %s", rec.Dir)
	}
	file := filepath.Join(rec.Dir, composeFileName(rec.Dir))
	var args []string
	switch action {
	case "up":
		args = []string{"compose", "-f", file, "up", "-d"}
	case "down":
		args = []string{"compose", "-f", file, "down"}
	case "restart":
		args = []string{"compose", "-f", file, "restart"}
	case "logs":
		args = []string{"compose", "-f", file, "logs", "--tail", "100"}
	default:
		return "", fmt.Errorf("未知操作: %s", action)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = rec.Dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Refresh 重解析项目服务；若已导入站点且原 upstream 端口失效，自动更新为第一个可用端口。
func (s *Service) Refresh(id uint) (string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, id).Error; err != nil {
		return "", fmt.Errorf("项目不存在")
	}
	f, err := parseCompose(rec.Dir)
	if err != nil {
		return "", err
	}
	services := servicesOf(f)
	s.db.Model(&rec).Updates(map[string]any{"services": mustJSON(services)})
	if !rec.Imported || rec.SiteID == nil {
		return "已刷新服务解析", nil
	}
	var siteRec model.Site
	if err := s.db.First(&siteRec, *rec.SiteID).Error; err != nil {
		return "已刷新服务解析（关联站点丢失）", nil
	}
	// upstream 端口是否仍可用
	oldPort := strings.TrimPrefix(siteRec.Upstream, "127.0.0.1:")
	still := false
	for _, sv := range services {
		for _, p := range sv.Ports {
			if p == oldPort || strings.HasPrefix(p, oldPort+":") || strings.HasSuffix(p, ":"+oldPort) {
				still = true
			}
		}
	}
	if still {
		return "已刷新服务解析", nil
	}
	newPort := ""
	for _, sv := range services {
		for _, p := range sv.Ports {
			parts := strings.Split(p, ":")
			if len(parts) == 2 && parts[0] != "" {
				newPort = parts[0]
				break
			}
		}
		if newPort != "" {
			break
		}
	}
	if newPort == "" {
		return "已刷新，但项目不再发布任何端口，站点 upstream 未变更", nil
	}
	siteRec.Upstream = "127.0.0.1:" + newPort
	if err := s.db.Save(&siteRec).Error; err != nil {
		return "", err
	}
	content := s.site.GenerateProxyConf(siteRec.Domain, siteRec.Upstream, siteRec.SSL)
	if err := os.WriteFile(s.cfg.NginxPrefix+"/sites-available/"+siteRec.Domain+".conf", []byte(content), 0o644); err != nil {
		return "", err
	}
	if _, err := s.site.ReloadNginx(); err != nil {
		return "", fmt.Errorf("站点 conf 已更新，但 nginx 重载失败: %w", err)
	}
	return fmt.Sprintf("已刷新：upstream 变更为 %s，站点 conf 已重新生成", siteRec.Upstream), nil
}
