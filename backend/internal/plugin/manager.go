package plugin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/cert/dns"
	"zjpanel/backend/internal/cert/targets"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	sdk "zjpanel/backend/sdk/plugin"
)

// Manager 插件运行时：内置注册 + 外部目录（Yaegi 加载）+ 扩展点接线。
// 生命周期：enable → Meta/OnInit（声明路由/cron/MCP/事件/证书注册项）→ disable → OnShutdown 全量回收。

type routeEntry struct {
	Method  string
	Path    string
	Handler http.HandlerFunc
}

type runtime struct {
	id           string
	p            sdk.Plugin
	ctx          *ctxImpl
	routes       []routeEntry
	publicRoutes []routeEntry // 免鉴权公开路由（/api/pub/plugin/<id>/）
	cronIDs      []any
	mcpNames     []string
	manifest     *Manifest
}

type Manager struct {
	cfg   *pkg.Config
	db    *gorm.DB
	box   *pkg.SecretBox
	bus   *bus.Bus
	mcp   *mcp.Registry
	dns   *certdns.DNSRegistry
	tgt   *targets.TargetRegistry
	addCron func(spec string, fn func()) any
	rmCron  func(id any)

	mu      sync.RWMutex
	running map[string]*runtime
	builtins map[string]func() sdk.Plugin
}

func New(cfg *pkg.Config, db *gorm.DB, box *pkg.SecretBox, b *bus.Bus, mcpReg *mcp.Registry,
	dnsReg *certdns.DNSRegistry, tgtReg *targets.TargetRegistry,
	addCron func(string, func()) any, rmCron func(any)) *Manager {
	return &Manager{
		cfg: cfg, db: db, box: box, bus: b, mcp: mcpReg, dns: dnsReg, tgt: tgtReg,
		addCron: addCron, rmCron: rmCron,
		running: map[string]*runtime{}, builtins: map[string]func() sdk.Plugin{},
	}
}

// RegisterBuiltin 注册编译内置插件（与外部插件同一接口）。
func (m *Manager) RegisterBuiltin(p sdk.Plugin) {
	meta := p.Meta()
	m.builtins[meta.ID] = func() sdk.Plugin { return p }
}

// ---- Manifest ----

func loadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		return nil, fmt.Errorf("缺少 plugin.yaml")
	}
	return parseManifestBytes(b)
}

// ---- 生命周期 ----

// StartAll 启动时恢复已启用插件。
func (m *Manager) StartAll() {
	// 内置插件开箱即用：无 DB 记录时自动创建并启用（用户手动停用过则尊重其选择）
	for id := range m.builtins {
		var cnt int64
		m.db.Model(&model.PluginRecord{}).Where("plugin_id = ?", id).Count(&cnt)
		if cnt == 0 {
			if err := m.Enable(id); err != nil {
				slog.Error("内置插件启用失败", "id", id, "err", err)
			}
		}
	}
	var recs []model.PluginRecord
	m.db.Where("enabled = ?", true).Find(&recs)
	for _, r := range recs {
		if err := m.Enable(r.PluginID); err != nil {
			slog.Error("插件启动失败", "id", r.PluginID, "err", err)
			m.db.Model(&r).Update("enabled", false)
		}
	}
	slog.Info("插件管理器就绪", "内置", len(m.builtins))
}

// Enable 启用插件：内置直接构造；外部从 DataDir/plugins/<id>/main.go 经 Yaegi 加载。
func (m *Manager) Enable(id string) error {
	m.mu.Lock()
	if _, ok := m.running[id]; ok {
		m.mu.Unlock()
		return fmt.Errorf("插件已在运行")
	}
	m.mu.Unlock()

	var (
		p   sdk.Plugin
		mf  *Manifest
		err error
	)
	if ctor, ok := m.builtins[id]; ok {
		p = ctor()
		meta := p.Meta()
		mf = &Manifest{ID: meta.ID, Name: meta.Name, Version: meta.Version, Description: meta.Description, Permissions: meta.Permissions}
	} else {
		dir := m.extDir(id)
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("插件目录不存在: %s", dir)
		}
		mf, err = loadManifestDir(dir)
		if err != nil {
			return err
		}
		p, err = m.loadExternal(id, dir)
		if err != nil {
			return fmt.Errorf("插件加载失败: %w", err)
		}
	}

	rt := &runtime{id: id, p: p, manifest: mf, ctx: &ctxImpl{mgr: m, id: id, rt: nil}}
	rt.ctx.rt = rt
	if err := safeInit(p, rt.ctx); err != nil {
		return fmt.Errorf("OnInit 失败: %w", err)
	}

	m.mu.Lock()
	m.running[id] = rt
	m.mu.Unlock()

	// DB 记录
	var rec model.PluginRecord
	err = m.db.Where("plugin_id = ?", id).First(&rec).Error
	if err != nil {
		rec = model.PluginRecord{PluginID: id, Name: mf.Name, Version: mf.Version, Enabled: true, Builtin: m.isBuiltin(id), Manifest: ""}
		m.db.Create(&rec)
	} else {
		m.db.Model(&rec).Updates(map[string]any{"enabled": true, "name": mf.Name, "version": mf.Version})
	}
	slog.Info("插件已启用", "id", id, "version", mf.Version)
	m.bus.Publish("plugin.enabled", map[string]any{"id": id})
	return nil
}

func safeInit(p sdk.Plugin, ctx sdk.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.OnInit(ctx)
}

// Disable 停用插件：OnShutdown + 回收全部扩展点。
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	rt, ok := m.running[id]
	if ok {
		delete(m.running, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("插件未运行")
	}
	func() {
		defer func() { _ = recover() }()
		rt.p.OnShutdown()
	}()
	for _, cid := range rt.cronIDs {
		m.rmCron(cid)
	}
	m.mcp.UnregisterByOwner(id)
	m.dns.UnregisterByOwner(id)
	m.tgt.UnregisterByOwner(id)
	m.db.Model(&model.PluginRecord{}).Where("plugin_id = ?", id).Update("enabled", false)
	slog.Info("插件已停用", "id", id)
	m.bus.Publish("plugin.disabled", map[string]any{"id": id})
	return nil
}

func (m *Manager) isBuiltin(id string) bool {
	_, ok := m.builtins[id]
	return ok
}

func (m *Manager) extDir(id string) string {
	return filepath.Join(m.cfg.DataDir, "plugins", id)
}

// ---- 列表 / 安装 / 卸载 ----

type PluginInfo struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description"`
	Enabled     bool          `json:"enabled"`
	Builtin     bool          `json:"builtin"`
	Running     bool          `json:"running"`
	Menu        []sdk.Menu    `json:"menu,omitempty"`
	Settings    []sdk.SettingDef `json:"settings,omitempty"`
}

// List 插件清单（DB 记录 ∪ 内置 ∪ 目录）。
func (m *Manager) List() []PluginInfo {
	m.mu.RLock()
	running := map[string]*runtime{}
	for k, v := range m.running {
		running[k] = v
	}
	m.mu.RUnlock()

	out := []PluginInfo{}
	seen := map[string]bool{}
	add := func(meta sdk.Meta, builtin, enabled, isRunning bool) {
		if seen[meta.ID] {
			return
		}
		seen[meta.ID] = true
		out = append(out, PluginInfo{
			ID: meta.ID, Name: meta.Name, Version: meta.Version, Description: meta.Description,
			Enabled: enabled, Builtin: builtin, Running: isRunning,
			Menu: meta.Menu, Settings: meta.Settings,
		})
	}
	// 内置（含未启用的）
	for id, ctor := range m.builtins {
		var enabled bool
		m.db.Model(&model.PluginRecord{}).Select("enabled").Where("plugin_id = ?", id).Scan(&enabled)
		meta := ctor().Meta()
		_, isRunning := running[id]
		add(meta, true, enabled, isRunning)
	}
	// 外部目录
	entries, _ := os.ReadDir(m.extRoot())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if seen[e.Name()] {
			continue
		}
		mf, err := loadManifest(m.extDir(e.Name()))
		if err != nil {
			continue
		}
		var enabled bool
		m.db.Model(&model.PluginRecord{}).Select("enabled").Where("plugin_id = ?", e.Name()).Scan(&enabled)
		_, isRunning := running[e.Name()]
		add(sdk.Meta{ID: mf.ID, Name: mf.Name, Version: mf.Version, Description: mf.Description}, false, enabled, isRunning)
	}
	return out
}

func (m *Manager) extRoot() string {
	return filepath.Join(m.cfg.DataDir, "plugins")
}

// InstallZip 安装外部插件 zip（plugin.yaml + main.go + 可选 dist/）。
func (m *Manager) InstallZip(data []byte) (string, error) {
	dir, id, err := unzipPlugin(data, m.extRoot())
	if err != nil {
		return "", err
	}
	if _, err := loadManifest(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("缺少 main.go")
	}
	var rec model.PluginRecord
	if err := m.db.Where("plugin_id = ?", id).First(&rec).Error; err == nil {
		return id, fmt.Errorf("插件 %s 已存在（版本 %s），请先卸载或直接启用", id, rec.Version)
	}
	m.db.Create(&model.PluginRecord{PluginID: id, Name: id, Version: "", Enabled: false, Builtin: false})
	return id, nil
}

// Uninstall 卸载外部插件（先停用再删目录）。
func (m *Manager) Uninstall(id string) error {
	if m.isBuiltin(id) {
		return fmt.Errorf("内置插件不可卸载")
	}
	_ = m.Disable(id)
	return os.RemoveAll(m.extDir(id))
}

// ---- 运行时接线：路由 / 菜单 / 资产 ----

// DispatchRoute gin 动态派发：/api/plugin/:id/*path。
func (m *Manager) DispatchRoute(w http.ResponseWriter, r *http.Request, id, path string) {
	m.mu.RLock()
	rt, ok := m.running[id]
	m.mu.RUnlock()
	if !ok {
		http.Error(w, "插件未启用: "+id, http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for _, re := range rt.routes {
		if re.Method != "*" && !strings.EqualFold(re.Method, r.Method) {
			continue
		}
		if matchRoute(re.Path, path) {
			re.Handler(w, r)
			return
		}
	}
	http.Error(w, "路由不存在", http.StatusNotFound)
}

// DispatchPublicRoute 免鉴权公开路由派发：/api/pub/plugin/:id/*path。
func (m *Manager) DispatchPublicRoute(w http.ResponseWriter, r *http.Request, id, path string) {
	m.mu.RLock()
	rt, ok := m.running[id]
	m.mu.RUnlock()
	if !ok {
		http.Error(w, "插件未启用: "+id, http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for _, re := range rt.publicRoutes {
		if re.Method != "*" && !strings.EqualFold(re.Method, r.Method) {
			continue
		}
		if matchRoute(re.Path, path) {
			re.Handler(w, r)
			return
		}
	}
	http.Error(w, "路由不存在", http.StatusNotFound)
}

func matchRoute(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// ServeAsset 插件静态资产（前端页面，iframe 承载）：/plugin-assets/<id>/<path>。
// 内置插件优先读源码树 assets/<id>/dist（随镜像分发，编译期内嵌目录），缺失再回退数据目录。
func (m *Manager) ServeAsset(w http.ResponseWriter, r *http.Request, id, path string) {
	roots := []string{filepath.Join(m.cfg.AppDir, "plugin-assets", id, "dist"), filepath.Join(m.extDir(id), "dist")}
	// 未启用也允许加载资产页面（页面内可提示）
	for _, root := range roots {
		full := filepath.Join(root, filepath.Clean("/"+path))
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, full)
			return
		}
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
	}
	http.Error(w, "插件页面不存在（dist/index.html 缺失）", http.StatusNotFound)
}

// MenuItems 已启用插件的菜单贡献（前端侧边栏渲染）。
func (m *Manager) MenuItems() []map[string]any {
	out := []map[string]any{}
	m.mu.RLock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	rts := map[string]*runtime{}
	for id, rt := range m.running {
		rts[id] = rt
	}
	m.mu.RUnlock()
	sortStrings(ids)
	for _, id := range ids {
		rt := rts[id]
		if rt.manifest == nil {
			continue
		}
		for _, mi := range rt.manifest.Menu {
			out = append(out, map[string]any{
				"pluginId": id, "title": mi.Title, "icon": mi.Icon, "path": mi.Path,
			})
		}
	}
	return out
}

// ---- Yaegi 加载 ----

func (m *Manager) loadExternal(id, dir string) (sdk.Plugin, error) {
	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		return nil, err
	}
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		return nil, err
	}
	if err := i.Use(sdkExports()); err != nil {
		return nil, err
	}
	if _, err := i.Eval(string(src)); err != nil {
		return nil, fmt.Errorf("main.go 解析失败: %w", err)
	}
	v, err := i.Eval("main.New")
	if err != nil {
		return nil, fmt.Errorf("找不到 main.New(): %w", err)
	}
	p, ok := v.Interface().(sdk.Plugin)
	if !ok {
		return nil, fmt.Errorf("main.New() 未返回 plugin.Plugin 实现")
	}
	return p, nil
}

// sdkExports 把 SDK 类型导出给解释器（Yaegi Exports 惯例：类型用 (*T)(nil) 标记）。
func sdkExports() interp.Exports {
	return interp.Exports{
		"zjpanel/backend/sdk/plugin": map[string]reflect.Value{
			"Plugin":      reflect.ValueOf((*sdk.Plugin)(nil)),
			"Meta":        reflect.ValueOf((*sdk.Meta)(nil)),
			"Menu":        reflect.ValueOf((*sdk.Menu)(nil)),
			"SettingDef":  reflect.ValueOf((*sdk.SettingDef)(nil)),
			"FieldDef":    reflect.ValueOf((*sdk.FieldDef)(nil)),
			"Context":     reflect.ValueOf((*sdk.Context)(nil)),
			"DNSProvider": reflect.ValueOf((*sdk.DNSProvider)(nil)),
			"DeployArgs":  reflect.ValueOf((*sdk.DeployArgs)(nil)),
		},
	}
}

var _ = json.Marshal
var _ = fmt.Sprintf
