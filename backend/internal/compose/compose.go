package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/site"
	"zjpanel/backend/internal/ws"
)

// Service docker-compose 目录扫描与「导入为网站」：
// 扫描 ComposeRoot 下含 docker-compose.yml 的一级目录，解析服务与发布端口；
// 导入时生成反代站点并持久化映射（项目目录保持原位，重扫描可再同步）。

type Service struct {
	cfg    *pkg.Config
	db     *gorm.DB
	site   *site.Service
	hub    *ws.Hub
	jobsMu sync.Mutex
	jobs   map[uint]*ActionJob // 项目最近一次操作任务（jobs.go）
}

func New(cfg *pkg.Config, db *gorm.DB, siteSvc *site.Service, hub *ws.Hub) *Service {
	return &Service{cfg: cfg, db: db, site: siteSvc, hub: hub, jobs: map[uint]*ActionJob{}}
}

type ServiceInfo struct {
	Name          string   `json:"name"`
	Image         string   `json:"image"`
	ContainerName string   `json:"containerName"`
	Ports         []string `json:"ports"` // 已发布端口 "8080:80"
}

// AppStatusChange 「一切安好」批量对账中发生状态变化的应用。
type AppStatusChange struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	From  string `json:"from"` // 正常 | 停用
	To    string `json:"to"`
	Sites int    `json:"sites"` // 该应用关联网站数
}

// AllGoodResult 「一切安好」执行结果：探测覆盖数与发生变化的引用。
type AllGoodResult struct {
	CheckedSites     int               `json:"checkedSites"`
	Apps             int               `json:"apps"` // 有网站、参与判定的应用数
	NormalToDisabled int               `json:"normalToDisabled"`
	DisabledToNormal int               `json:"disabledToNormal"`
	SkippedNoSites   int               `json:"skippedNoSites"`
	SkippedUnknown   int               `json:"skippedUnknown"` // 有站点未拿到本次探测结果，跳过不改
	Changes          []AppStatusChange `json:"changes"`
}

type ProjectInfo struct {
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	Services []ServiceInfo `json:"services"`
	Imported bool          `json:"imported"`
	SiteID   *uint         `json:"siteId"`
	ID       uint          `json:"id"`
	HasDecl  bool          `json:"hasDecl"` // 存在 zjpanel.json（声明式导入）
	Decl     *DeclFile     `json:"decl,omitempty"`
	// 应用运维状态：停用的应用视为人工关停，其站点掉线不告警、不计入安全中心。
	Disabled   bool       `json:"disabled"`
	DisabledAt *time.Time `json:"disabledAt,omitempty"`
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

// servicesOf 归一化端口条目（字符串 "8080:80" 或映射 {published,target}）。
// 按服务名排序：compose 文件里 services 是 map，Go 遍历顺序随机；不排序会导致
// 同一份文件每次扫描产出不同 JSON，既让 SyncDB 的「无变化不写库」失效，UI 服务顺序也会跳。
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

// Scan 扫描根目录（一级），返回项目列表（附带已有导入记录）。
func (s *Service) Scan() ([]ProjectInfo, error) {
	var out []ProjectInfo
	entries, err := os.ReadDir(s.cfg.ComposeRoot)
	if err != nil {
		return out, nil
	}
	// 一次取出全部记录再按目录索引：目录多时不再每个项目查一次库。
	// 查询失败按「没有任何导入记录」处理（与原先逐目录 First 失败的语义一致）。
	recs := s.recordsByDir()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.ComposeRoot, e.Name())
		// 已从面板删除（hidden）的项目不再出现在列表，目录本身保持原样
		if rec, ok := recs[dir]; ok && rec.Hidden {
			continue
		}
		f, err := parseCompose(dir)
		if err != nil {
			continue
		}
		pi := ProjectInfo{Name: firstNonEmpty(f.Name, e.Name()), Dir: dir, Services: servicesOf(f)}
		if decl, derr := s.ReadDecl(dir); derr == nil && decl != nil {
			pi.HasDecl, pi.Decl = true, decl
		}
		if rec, ok := recs[dir]; ok {
			pi.ID, pi.Imported, pi.SiteID = rec.ID, rec.Imported, rec.SiteID
			pi.Disabled, pi.DisabledAt = rec.Disabled, rec.DisabledAt
		}
		out = append(out, pi)
	}
	return out, nil
}

// recordsByDir 一次查询全部 compose 项目记录，按目录建索引（避免扫描时 N+1 查询）。
func (s *Service) recordsByDir() map[string]*model.ComposeProject {
	var recs []model.ComposeProject
	if err := s.db.Find(&recs).Error; err != nil {
		return map[string]*model.ComposeProject{}
	}
	m := make(map[string]*model.ComposeProject, len(recs))
	for i := range recs {
		m[recs[i].Dir] = &recs[i]
	}
	return m
}

// SyncDB 扫描并入库：新目录创建记录，已有记录仅在解析结果变化时刷新。
// 应用页每次激活都会调用扫描，无变化时空写会白白占用 SQLite 写锁并放大 WAL。
func (s *Service) SyncDB() ([]ProjectInfo, error) {
	list, err := s.Scan()
	if err != nil {
		return nil, err
	}
	recs := s.recordsByDir()
	for _, p := range list {
		services := mustJSON(p.Services)
		if rec, ok := recs[p.Dir]; ok {
			if rec.Name == p.Name && rec.Services == services {
				continue
			}
			if err := s.db.Model(rec).Updates(map[string]any{"services": services, "name": p.Name}).Error; err != nil {
				return nil, err
			}
			continue
		}
		rec := &model.ComposeProject{Name: p.Name, Dir: p.Dir, Services: services}
		if err := s.db.Create(rec).Error; err != nil {
			return nil, err
		}
		recs[p.Dir] = rec
	}
	return list, nil
}

// ListProjects DB 项目记录（含存在性核对）。
func (s *Service) ListProjects() ([]map[string]any, error) {
	var recs []model.ComposeProject
	if err := s.db.Where("hidden = ?", false).Order("id").Find(&recs).Error; err != nil {
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
			"siteId": r.SiteID, "disabled": r.Disabled, "disabledAt": r.DisabledAt,
			"createTime": r.CreatedAt,
		})
	}
	return out, nil
}

// SetDisabled 设置应用运维状态：停用=人工关停，其关联网站掉线不告警、不计入安全中心。
// 只改声明，不碰容器与配置。
func (s *Service) SetDisabled(projectID uint, disabled bool) error {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return fmt.Errorf("项目不存在")
	}
	upd := map[string]any{"disabled": disabled}
	if disabled {
		upd["disabled_at"] = time.Now()
	} else {
		upd["disabled_at"] = nil
	}
	return s.db.Model(&rec).Updates(upd).Error
}

// ReconcileAllGood 「一切安好？」批量对账：按本次探测结果修正各应用的运维状态。
// 规则：应用只要有任意一个网站 online 就是「正常」；全部网站都 down（offline/error）则为「停用」。
// 只更新状态确实变化的应用，并返回变化清单（含方向），无网站/探测缺数据的应用跳过。
func (s *Service) ReconcileAllGood(statuses map[string]string) (AllGoodResult, error) {
	res := AllGoodResult{Changes: []AppStatusChange{}}
	var recs []model.ComposeProject
	if err := s.db.Where("hidden = ?", false).Order("id").Find(&recs).Error; err != nil {
		return res, err
	}
	var sites []model.Site
	if err := s.db.Where("compose_project_id IS NOT NULL").Find(&sites).Error; err != nil {
		return res, err
	}
	byProject := map[uint][]model.Site{}
	for _, st := range sites {
		if st.ComposeProjectID != nil {
			byProject[*st.ComposeProjectID] = append(byProject[*st.ComposeProjectID], st)
		}
	}

	now := time.Now()
	for _, rec := range recs {
		list := byProject[rec.ID]
		if len(list) == 0 {
			res.SkippedNoSites++
			continue
		}
		res.Apps++
		anyOnline, missing := false, false
		for _, st := range list {
			status, ok := statuses[st.Domain]
			if !ok {
				missing = true
				continue
			}
			if status == "online" {
				anyOnline = true
			}
		}
		if missing {
			res.SkippedUnknown++
			continue
		}
		wantDisabled := !anyOnline
		if rec.Disabled == wantDisabled {
			continue
		}
		upd := map[string]any{"disabled": wantDisabled}
		if wantDisabled {
			upd["disabled_at"] = now
		} else {
			upd["disabled_at"] = nil
		}
		if err := s.db.Model(&rec).Updates(upd).Error; err != nil {
			return res, err
		}
		change := AppStatusChange{ID: rec.ID, Name: rec.Name, Sites: len(list)}
		if wantDisabled {
			change.From, change.To = "正常", "停用"
			res.NormalToDisabled++
		} else {
			change.From, change.To = "停用", "正常"
			res.DisabledToNormal++
		}
		res.Changes = append(res.Changes, change)
	}
	return res, nil
}

// DisabledSiteDomains 停用应用关联网站域名集合（安全中心用于过滤其 5xx/攻击判定）。
func (s *Service) DisabledSiteDomains() map[string]bool {
	out := map[string]bool{}
	var domains []string
	err := s.db.Table("sites").Select("sites.domain").
		Joins("JOIN compose_projects ON sites.compose_project_id = compose_projects.id").
		Where("compose_projects.disabled = ?", true).Scan(&domains).Error
	if err != nil {
		slog.Warn("读取停用应用站点失败", "err", err)
		return out
	}
	for _, d := range domains {
		out[d] = true
	}
	return out
}

// HiddenProjects 已从面板删除的项目记录（「已删除」恢复入口用）。
// 目录仍存在才可恢复；目录已被用户删除的记录只保留在库里，不再展示。
func (s *Service) HiddenProjects() ([]map[string]any, error) {
	var recs []model.ComposeProject
	if err := s.db.Where("hidden = ?", true).Order("id").Find(&recs).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		_, statErr := os.Stat(r.Dir)
		out = append(out, map[string]any{
			"id": r.ID, "name": r.Name, "dir": r.Dir, "exists": statErr == nil,
		})
	}
	return out, nil
}

// Restore 恢复已删除的项目：仅解除 hidden 标记并回到列表，不重建站点（重新导入才建站）。
func (s *Service) Restore(projectID uint) error {
	var rec model.ComposeProject
	if err := s.db.First(&rec, projectID).Error; err != nil {
		return fmt.Errorf("项目不存在")
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		return fmt.Errorf("项目目录已不存在，无法恢复: %s", rec.Dir)
	}
	if err := s.db.Model(&rec).Update("hidden", false).Error; err != nil {
		return err
	}
	// 恢复后顺手按当前 compose 文件刷新服务解析（删除期间文件可能已改）
	if f, err := parseCompose(rec.Dir); err == nil {
		services := servicesOf(f)
		s.db.Model(&rec).Updates(map[string]any{
			"services": mustJSON(services),
			"name":     firstNonEmpty(f.Name, filepath.Base(rec.Dir)),
		})
	}
	return nil
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
	// 先解除删除标记：下面可能按声明恢复站点，失败时也应让项目回到列表（避免站点已恢复但项目仍隐藏）
	s.db.Model(&rec).Update("hidden", false)
	// 删除后重新导入：目录里保留着 zjpanel.json，先按声明恢复原有站点，再叠加本次新增；
	// 否则末尾 WriteDecl 只写新站点，会把旧声明覆盖（下次导入就没得恢复）。
	if decl, derr := s.ReadDecl(clean); derr == nil && decl != nil && len(decl.Sites) > 0 {
		if _, _, _, serr := s.SyncDecl(rec.ID); serr != nil {
			return nil, serr
		}
	}
	siteRec, err := s.site.Create(site.SaveReq{
		Domain: domain, Type: "proxy", Upstream: upstream, SSL: ssl,
		ComposeProjectID: &rec.ID, Remark: "compose: " + serviceName,
	})
	if err != nil {
		return nil, err
	}
	s.db.Model(&rec).Updates(map[string]any{"imported": true, "site_id": siteRec.ID, "hidden": false})
	// 兼容导入：写出 zjpanel.json（下次导入/迁移直接读目录）
	_ = s.WriteDecl(rec.ID)
	return siteRec, nil
}

// ImportDecl 按目录下的 zjpanel.json 恢复站点（删除/迁移后重新导入用）。
// 声明即事实来源：域名、类型、上游端口、SSL 均以声明为准，恢复后项目回到列表并标记已导入。
func (s *Service) ImportDecl(dir string) (int, int, error) {
	clean := filepath.Clean(dir)
	if !isAbsPath(clean) {
		return 0, 0, fmt.Errorf("需要绝对路径（宿主机目录经挂载映射进容器，默认 /opt:/opt）")
	}
	if strings.Contains(clean, "..") {
		return 0, 0, fmt.Errorf("非法路径")
	}
	if _, err := os.Stat(clean); err != nil {
		return 0, 0, fmt.Errorf("目录不存在（确认已挂载进容器）: %s", clean)
	}
	decl, err := s.ReadDecl(clean)
	if err != nil {
		return 0, 0, err
	}
	if decl == nil {
		return 0, 0, fmt.Errorf("目录下没有 %s，无法按声明导入", DeclFileName)
	}
	f, err := parseCompose(clean)
	if err != nil {
		return 0, 0, err
	}
	var rec model.ComposeProject
	if err := s.db.Where("dir = ?", clean).First(&rec).Error; err != nil {
		rec = model.ComposeProject{Name: firstNonEmpty(f.Name, filepath.Base(clean)), Dir: clean, Services: mustJSON(servicesOf(f))}
		if err := s.db.Create(&rec).Error; err != nil {
			return 0, 0, err
		}
	}
	// 解除删除标记：重新导入即回到列表
	if err := s.db.Model(&rec).Updates(map[string]any{"hidden": false}).Error; err != nil {
		return 0, 0, err
	}
	created, updated, _, err := s.SyncDecl(rec.ID)
	return created, updated, err
}

// actionArgs 构建 compose 生命周期命令参数（同步 Action 与异步 StartAction 共用）
func (s *Service) actionArgs(rec *model.ComposeProject, action string) ([]string, error) {
	if _, err := os.Stat(rec.Dir); err != nil {
		return nil, fmt.Errorf("项目目录不存在: %s", rec.Dir)
	}
	file := filepath.Join(rec.Dir, composeFileName(rec.Dir))
	switch action {
	case "up":
		return []string{"compose", "-f", file, "up", "-d"}, nil
	case "down":
		return []string{"compose", "-f", file, "down"}, nil
	case "restart":
		return []string{"compose", "-f", file, "restart"}, nil
	case "logs":
		return []string{"compose", "-f", file, "logs", "--tail", "100"}, nil
	default:
		return nil, fmt.Errorf("未知操作: %s", action)
	}
}

// Action 项目生命周期：up/down/restart/logs（docker compose CLI，同步执行；
// MCP 等内部调用走此路径，面板 UI 走 StartAction 异步实时输出）
func (s *Service) Action(id uint, action string) (string, error) {
	var rec model.ComposeProject
	if err := s.db.First(&rec, id).Error; err != nil {
		return "", fmt.Errorf("项目不存在")
	}
	args, err := s.actionArgs(&rec, action)
	if err != nil {
		return "", err
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
