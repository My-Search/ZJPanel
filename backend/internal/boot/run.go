package boot

import (
	"fmt"
	"log/slog"
	nethttp "net/http"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/ssh"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/cert"
	"zjpanel/backend/internal/cert/targets"
	"zjpanel/backend/internal/compose"
	"zjpanel/backend/internal/docker"
	httpapi "zjpanel/backend/internal/http"
	"zjpanel/backend/internal/logmap"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/metrics"
	"zjpanel/backend/internal/middleware"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/plugin"
	"zjpanel/backend/internal/site"
	"zjpanel/backend/internal/sitecheck"
	"zjpanel/backend/internal/terminal"
	"zjpanel/backend/internal/update"
	"zjpanel/backend/internal/ws"
	"zjpanel/backend/plugins/cronjobs"
	"zjpanel/backend/plugins/mysqladmin"
	"zjpanel/backend/plugins/notifywebhook"
	"zjpanel/backend/plugins/webhooks"
)

// cronWrap robfig/cron 适配（CronLike 接口；entryID 以 any 传递）。
type cronWrap struct {
	c  *cron.Cron
	mu sync.Mutex
}

func newCronWrap() *cronWrap {
	return &cronWrap{c: cron.New()}
}

func (w *cronWrap) AddFunc(spec string, cmd func()) any {
	id, _ := w.c.AddFunc(spec, cmd)
	return id
}

func (w *cronWrap) RemoveByID(id any) {
	if eid, ok := id.(cron.EntryID); ok {
		w.c.Remove(eid)
	}
}

// NextByID 查询任务下次执行时间（供证书任务列表展示）。
func (w *cronWrap) NextByID(id any) time.Time {
	if eid, ok := id.(cron.EntryID); ok {
		return w.c.Entry(eid).Next
	}
	return time.Time{}
}

func (w *cronWrap) Start() { w.c.Start() }
func (w *cronWrap) Stop()  { w.c.Stop() }

// Run 面板启动入口：装配全部服务并监听 HTTP。
func Run() {
	cfg := pkg.LoadConfig()
	cfg.EnsureDirs()

	key, err := pkg.EnsureSecretKey(cfg.DataDir)
	if err != nil {
		slog.Error("主密钥初始化失败", "err", err)
		return
	}
	box := pkg.NewSecretBox(key)

	db, err := OpenDB(cfg)
	if err != nil {
		slog.Error("数据库初始化失败", "err", err)
		return
	}
	site.EnsureDefaults(cfg)

	hub := ws.NewHub()
	eventBus := bus.New()

	// nginx / 站点 / 探测
	nginxSvc := nginx.New(cfg)
	siteSvc := site.New(cfg, db, nginxSvc)
	check := sitecheck.New(db, eventBus)
	check.Start(60 * time.Second)
	composeSvc := compose.New(cfg, db, siteSvc)
	// 站点变更 → 自动写回所属 compose 项目的 zjpanel.json（目录为事实来源）
	siteSvc.OnChange = func(st *model.Site) { composeSvc.WriteDeclForSite(st) }

	// 终端 / Docker
	termMgr := terminal.NewManager(db, box)
	dockerSvc := docker.New()

	// 指标 / 日志地图
	metricsSvc := metrics.New(db, hub)
	metricsSvc.Start()
	logmapSvc := logmap.New(cfg, hub)
	logmapSvc.Start()

	// 证书管线（certd 式）
	cronSvc := newCronWrap()
	certSvc := cert.New(cfg, db, box, eventBus, hub, cronSvc)
	certSvc.SetReloadFunc(siteSvc.ReloadNginx)
	certSvc.SetSSHDialer(func(hostID uint) (*ssh.Client, error) {
		return terminal.DialHostByID(db, box, hostID)
	})
	targets.RegisterBuiltins(certSvc.Targets, cfg, siteSvc.ReloadNginx,
		func(hostID uint) (*ssh.Client, error) { return terminal.DialHostByID(db, box, hostID) })
	certSvc.Start()

	// MCP 聚合注册表
	mcpReg := mcp.NewRegistry()
	mcpSrv := mcp.NewServer(mcpReg)

	// 插件运行时
	pluginMgr := plugin.New(cfg, db, box, eventBus, mcpReg, certSvc.DNS, certSvc.Targets,
		func(spec string, fn func()) any { return cronSvc.AddFunc(spec, fn) },
		func(id any) { cronSvc.RemoveByID(id) },
	)
	pluginMgr.RegisterBuiltin(notifywebhook.New())
	pluginMgr.RegisterBuiltin(webhooks.New())
	pluginMgr.RegisterBuiltin(mysqladmin.New())
	pluginMgr.RegisterBuiltin(cronjobs.New())
	pluginMgr.StartAll()

	updater := update.New(cfg)

	// HTTP 服务
	srv := httpapi.NewServer(cfg, db, middleware.NewAuth(), hub, box)
	srv.Nginx = nginxSvc
	srv.Sites = siteSvc
	srv.Check = check
	srv.Compose = composeSvc
	srv.Term = termMgr
	srv.Docker = dockerSvc
	srv.Metrics = metricsSvc
	srv.Logmap = logmapSvc
	srv.Cert = certSvc
	srv.Updater = updater
	srv.Plugins = pluginMgr
	srv.MCPReg = mcpReg
	srv.MCPSrv = mcpSrv
	httpapi.RegisterStatic(srv.Engine())
	srv.RegisterRoutes()

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("ZJPanel 启动完成", "addr", addr, "data", cfg.DataDir, "version", update.CurrentVersion())
	if err := nethttp.ListenAndServe(addr, srv.Engine()); err != nil {
		slog.Error("HTTP 服务退出", "err", err)
	}
}
