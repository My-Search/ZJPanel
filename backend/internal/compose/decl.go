package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

// DeclFile 声明式站点描述（zjpanel.json，随 compose 项目目录落盘）。
// 目录为事实来源：导入优先读它；面板侧变化自动写回；换机器/重装面板扫描目录即恢复。

const DeclFileName = "zjpanel.json"

type DeclSite struct {
	Domain      string `json:"domain"`
	Type        string `json:"type"` // proxy | static
	Service     string `json:"service,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Root        string `json:"root,omitempty"`
	SSL         bool   `json:"ssl"`
	Maintenance bool   `json:"maintenance"`
	Remark      string `json:"remark,omitempty"`
}

type DeclFile struct {
	Version   int        `json:"version"`
	Project   string     `json:"project"`
	Dir       string     `json:"dir"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Sites     []DeclSite `json:"sites"`
}

// declPath 声明文件路径。
func (s *Service) declPath(dir string) string {
	return filepath.Join(dir, DeclFileName)
}

// ReadDecl 读取项目的 zjpanel.json（不存在返回 nil）。
func (s *Service) ReadDecl(dir string) (*DeclFile, error) {
	b, err := os.ReadFile(s.declPath(dir))
	if err != nil {
		return nil, nil
	}
	var d DeclFile
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("zjpanel.json 解析失败: %w", err)
	}
	return &d, nil
}

// WriteDecl 把项目当前关联站点写出到 zjpanel.json（面板侧变化时同步）。
func (s *Service) WriteDecl(projectID uint) error {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return nil // 项目不存在（如已卸载），静默
	}
	var sites []model.Site
	if err := s.db.Where("compose_project_id = ?", projectID).Find(&sites).Error; err != nil {
		return err
	}
	decl := DeclFile{
		Version:   1,
		Project:   rec.Name,
		Dir:       rec.Dir,
		UpdatedAt: time.Now(),
		Sites:     []DeclSite{},
	}
	for _, st := range sites {
		decl.Sites = append(decl.Sites, DeclSite{
			Domain: st.Domain, Type: st.Type, Upstream: st.Upstream, Root: st.Root,
			SSL: st.SSL, Maintenance: st.Maintenance, Remark: st.Remark,
		})
	}
	sort.Slice(decl.Sites, func(i, j int) bool { return decl.Sites[i].Domain < decl.Sites[j].Domain })
	b, _ := json.MarshalIndent(decl, "", "  ")
	if err := os.WriteFile(s.declPath(rec.Dir), b, 0o644); err != nil {
		return fmt.Errorf("写出 %s 失败: %w", DeclFileName, err)
	}
	return nil
}

// WriteDeclForSite 站点变更后同步其所属项目的声明文件。
func (s *Service) WriteDeclForSite(site *model.Site) {
	if site == nil || site.ComposeProjectID == nil {
		return
	}
	_ = s.WriteDecl(*site.ComposeProjectID)
}

// SyncDecl 按声明文件同步站点（扫描/导入时调用）。
// 返回 (created, updated, orphans)：
//   - created/updated: 按 JSON 创建/更新的站点数
//   - orphans: 面板存在关联但 JSON 未声明的站点（仅提示，不自动删除）
func (s *Service) SyncDecl(projectID uint) (int, int, []string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return 0, 0, nil, fmt.Errorf("项目不存在")
	}
	decl, err := s.ReadDecl(rec.Dir)
	if err != nil {
		return 0, 0, nil, err
	}
	if decl == nil {
		return 0, 0, nil, nil
	}
	created, updated := 0, 0
	declared := map[string]bool{}
	for _, ds := range decl.Sites {
		declared[ds.Domain] = true
		var site model.Site
		err := s.db.Where("domain = ?", ds.Domain).First(&site).Error
		if err != nil {
			// 声明里有、面板没有 → 创建（声明恢复）
			site = model.Site{
				Domain: ds.Domain, Type: ds.Type, Upstream: ds.Upstream, Root: ds.Root,
				SSL: ds.SSL, Maintenance: ds.Maintenance, Remark: ds.Remark,
				ComposeProjectID: &rec.ID,
			}
			if site.Type == "" {
				site.Type = "proxy"
			}
			if err := s.db.Create(&site).Error; err != nil {
				return created, updated, nil, fmt.Errorf("恢复站点 %s 失败: %w", ds.Domain, err)
			}
			if !site.Advanced {
				content := s.site.GenerateFor(site)
				_ = os.WriteFile(filepath.Join(s.cfg.NginxPrefix, "sites-available", site.Domain+".conf"), []byte(content), 0o644)
			}
			if site.Maintenance {
				_ = s.site.WriteMaintenanceConf(&site)
			}
			created++
			continue
		}
		// 面板已有：以声明为准同步关键字段（仅当站点归属本项目时）
		if site.ComposeProjectID != nil && *site.ComposeProjectID == rec.ID {
			changed := site.Upstream != ds.Upstream || site.SSL != ds.SSL ||
				site.Type != ds.Type || site.Root != ds.Root || site.Maintenance != ds.Maintenance
			if changed {
				site.Type, site.Upstream, site.Root = ds.Type, ds.Upstream, ds.Root
				site.SSL, site.Maintenance = ds.SSL, ds.Maintenance
				if site.Type == "" {
					site.Type = "proxy"
				}
				if err := s.db.Save(&site).Error; err != nil {
					return created, updated, nil, err
				}
				if !site.Advanced {
					content := s.site.GenerateFor(site)
					_ = os.WriteFile(filepath.Join(s.cfg.NginxPrefix, "sites-available", site.Domain+".conf"), []byte(content), 0o644)
				}
				updated++
			}
		}
	}
	// 孤立站点：面板关联本项目但声明未提及
	var linked []model.Site
	s.db.Where("compose_project_id = ?", projectID).Find(&linked)
	orphans := []string{}
	for _, st := range linked {
		if !declared[st.Domain] {
			orphans = append(orphans, st.Domain)
		}
	}
	if created+updated > 0 {
		if _, err := s.site.ReloadNginx(); err != nil {
			return created, updated, orphans, fmt.Errorf("站点已同步，但 nginx 重载失败: %w", err)
		}
	}
	return created, updated, orphans, nil
}

// DeclJSON 项目声明文件原文（面板查看用）。
func (s *Service) DeclJSON(projectID uint) (string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return "", fmt.Errorf("项目不存在")
	}
	if d, err := s.ReadDecl(rec.Dir); err == nil && d != nil {
		b, _ := json.MarshalIndent(d, "", "  ")
		return string(b), nil
	}
	// 尚未写出：即时生成预览（不落盘）
	var sites []model.Site
	s.db.Where("compose_project_id = ?", projectID).Find(&sites)
	decl := DeclFile{Version: 1, Project: rec.Name, Dir: rec.Dir, UpdatedAt: time.Now(), Sites: []DeclSite{}}
	for _, st := range sites {
		decl.Sites = append(decl.Sites, DeclSite{Domain: st.Domain, Type: st.Type, Upstream: st.Upstream, Root: st.Root, SSL: st.SSL, Maintenance: st.Maintenance, Remark: st.Remark})
	}
	b, _ := json.MarshalIndent(decl, "", "  ")
	return string(b), nil
}

// WriteDeclIfMissing 兼容导入：导入完成后若无声明文件则写出。
func (s *Service) WriteDeclIfMissing(projectID uint) error {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return nil
	}
	if _, err := os.Stat(s.declPath(rec.Dir)); err == nil {
		return nil
	}
	return s.WriteDecl(projectID)
}

var _ = gorm.ErrRecordNotFound
