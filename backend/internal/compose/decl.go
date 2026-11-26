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
	sitePkg "zjpanel/backend/internal/site"
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

// applyImportedState 让 imported/site_id 跟随关联网站数量（无变化不写库，避免放大 SQLite 写）。
// site_id 保留原指向；原站点已不在关联集合里则退到第一个站点。
func (s *Service) applyImportedState(rec *model.ComposeProject, sites []model.Site) {
	imported := len(sites) > 0
	var siteID *uint
	if imported {
		siteID = &sites[0].ID
		for _, st := range sites {
			if rec.SiteID != nil && st.ID == *rec.SiteID {
				siteID = rec.SiteID
				break
			}
		}
	}
	changed := rec.Imported != imported
	if siteID == nil {
		changed = changed || rec.SiteID != nil
	} else {
		changed = changed || rec.SiteID == nil || *rec.SiteID != *siteID
	}
	if changed {
		s.db.Model(rec).Updates(map[string]any{"imported": imported, "site_id": siteID})
	}
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
	// 导入状态跟随关联网站数量：站点被逐个删完后项目必须回到「未导入」，
	// 否则 imported/site_id 只在导入时置位、永不回退，卡片会一直挂着「已导入」。
	s.applyImportedState(&rec, sites)
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
		if err := sitePkg.ValidateDomain(ds.Domain); err != nil {
			return created, updated, nil, fmt.Errorf("声明站点域名非法 %q: %w", ds.Domain, err)
		}
		typ := ds.Type
		if typ == "" {
			typ = "proxy"
		}
		if typ != "proxy" && typ != "static" {
			return created, updated, nil, fmt.Errorf("声明站点 %s 类型非法: %q", ds.Domain, ds.Type)
		}
		if typ == "proxy" {
			if err := sitePkg.ValidateUpstream(ds.Upstream); err != nil {
				return created, updated, nil, fmt.Errorf("声明站点 %s 反代目标非法: %w", ds.Domain, err)
			}
		} else {
			if err := sitePkg.ValidateRoot(ds.Root); err != nil {
				return created, updated, nil, fmt.Errorf("声明站点 %s 静态目录非法: %w", ds.Domain, err)
			}
		}
		declared[ds.Domain] = true
		var site model.Site
		err := s.db.Where("domain = ?", ds.Domain).First(&site).Error
		if err != nil {
			// 声明里有、面板没有 → 创建（声明恢复）
			site = model.Site{
				Domain: ds.Domain, Type: typ, Upstream: ds.Upstream, Root: ds.Root,
				SSL: ds.SSL, Maintenance: ds.Maintenance, Remark: ds.Remark,
				ComposeProjectID: &rec.ID,
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
				site.Type != typ || site.Root != ds.Root || site.Maintenance != ds.Maintenance
			if changed {
				site.Type, site.Upstream, site.Root = typ, ds.Upstream, ds.Root
				site.SSL, site.Maintenance = ds.SSL, ds.Maintenance
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
		// 声明恢复/同步后关联网站已变化：同步 imported/site_id（不重写声明，避免覆盖声明里
		// 归属其它项目的同名站点）
		s.applyImportedState(&rec, linked)
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

// Remove 删除项目：从面板列表移除并解除全部关联，运行中的容器不受影响。
//   - 删除全部关联网站（nginx conf、sites-enabled 软链、DB 记录）；
//   - 保留目录下的 zjpanel.json：下次导入按它把站点配置恢复回来管理（声明随目录迁移）；
//   - 项目标记 hidden：扫描/列表不再出现；项目目录、docker-compose.yml 与容器一律不动；
//   - 之后可经导入向导重新导入，或在「已删除」列表里恢复。
//
// 返回被删除的站点域名列表（前端确认后提示用）。
func (s *Service) Remove(projectID uint) ([]string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return nil, fmt.Errorf("项目不存在")
	}
	var sites []model.Site
	if err := s.db.Where("compose_project_id = ?", rec.ID).Find(&sites).Error; err != nil {
		return nil, err
	}
	// 先备份声明文件：删站点会经 OnChange 回写 zjpanel.json（删到最后一个会写成空声明），
	// 删除完成后必须还原，下次导入才能按原声明恢复站点。
	declPath := s.declPath(rec.Dir)
	declBackup, _ := os.ReadFile(declPath)
	removed := make([]string, 0, len(sites))
	for _, st := range sites {
		// site.Delete 会下线站点、删 conf 并重载 nginx，同时经 OnChange 回写声明
		if err := s.site.Delete(st.ID); err != nil {
			return removed, fmt.Errorf("删除站点 %s 失败: %w", st.Domain, err)
		}
		removed = append(removed, st.Domain)
	}
	if len(declBackup) > 0 {
		if err := os.WriteFile(declPath, declBackup, 0o644); err != nil {
			return removed, fmt.Errorf("还原 %s 失败: %w", DeclFileName, err)
		}
	} else {
		// 原本没有声明：删站点时 OnChange 可能写出空声明，清掉避免留下无站点的 zjpanel.json
		_ = os.Remove(declPath)
	}
	if err := s.db.Model(&rec).Updates(map[string]any{
		"hidden": true, "imported": false, "site_id": nil,
	}).Error; err != nil {
		return removed, err
	}
	return removed, nil
}

var _ = gorm.ErrRecordNotFound
