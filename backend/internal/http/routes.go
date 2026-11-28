package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/site"
	"zjpanel/backend/internal/terminal"
)

// RegisterRoutes 模块路由装配（boot 在注入全部服务后调用一次）。
func (s *Server) RegisterRoutes() {
	s.registerNginxRoutes()
	s.registerSiteRoutes()
	s.registerComposeRoutes()
	s.registerSshRoutes()
	s.registerHostRoutes()
	s.registerTerminalRoutes()
	s.registerSftpRoutes()
	s.registerFsRoutes()
	s.registerQuickCmdRoutes()
	s.registerDockerRoutes()
	s.registerSettingsRoutes()
	s.registerMetricsRoutes()
	s.registerLogmapRoutes()
	s.registerSecurityRoutes()
	s.registerWafRoutes()
	s.registerCertRoutes()
	s.registerMCPRoutes()
	s.registerUpdateRoutes()
	s.registerPluginRoutes()
	s.registerNotifyRoutes()
	s.registerBackupRoutes()
	s.registerOpsAgentRoutes()
	s.registerMCPTools()
	s.registerWSRoutes()
	s.registerMaintenanceRoute()
}

// ===================== Nginx =====================

func (s *Server) registerNginxRoutes() {
	g := s.r.Group("/api/nginx", s.auth.Require())
	g.GET("/files", func(c *gin.Context) {
		nodes, err := s.Nginx.ListTree(c.Query("root"), c.Query("path"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, nodes)
	})
	g.GET("/file", func(c *gin.Context) {
		content, err := s.Nginx.ReadFile(c.Query("root"), c.Query("path"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"content": content})
	})
	g.PUT("/file", func(c *gin.Context) {
		var req struct{ Root, Path, Content string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		var out string
		var err error
		if req.Root == "nginx" {
			out, err = s.Nginx.SaveAndReload(req.Root, req.Path, req.Content) // conf：测试→重载→失败回滚
		} else {
			err = s.Nginx.WriteFile(req.Root, req.Path, req.Content) // html 等静态文件直接写
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "output": out})
			return
		}
		s.audit(c, "nginx.file.save", req.Root+":"+req.Path)
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": out})
	})
	g.POST("/test", func(c *gin.Context) {
		out, err := s.Nginx.TestConfig()
		resp := gin.H{"ok": err == nil, "output": out}
		if err != nil {
			resp["error"] = err.Error()
		}
		c.JSON(http.StatusOK, resp)
	})
	g.POST("/reload", func(c *gin.Context) {
		out, err := s.Nginx.Reload()
		resp := gin.H{"ok": err == nil, "output": out}
		if err != nil {
			resp["error"] = err.Error()
		}
		s.audit(c, "nginx.reload", "")
		c.JSON(http.StatusOK, resp)
	})
	g.POST("/restart", func(c *gin.Context) {
		out, err := s.Nginx.Restart()
		resp := gin.H{"ok": err == nil, "output": out}
		if err != nil {
			resp["error"] = err.Error()
		}
		c.JSON(http.StatusOK, resp)
	})
	g.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"running": s.Nginx.Running()})
	})
}

// ===================== 站点 =====================

func (s *Server) registerSiteRoutes() {
	g := s.r.Group("/api/sites", s.auth.Require())
	g.GET("", func(c *gin.Context) {
		list, err := s.Sites.List()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		states := map[string]nginx.SiteFileState{}
		for _, st := range s.Nginx.ListSiteStates() {
			states[st.Domain] = st
		}
		checks := s.Check.All()
		// 停用应用：其关联网站掉线属人工关停，前端展示「停用」而不是故障。
		var disabledProjectIDs []uint
		_ = s.db.Model(&model.ComposeProject{}).Where("disabled = ?", true).Pluck("id", &disabledProjectIDs).Error
		disabled := make(map[uint]bool, len(disabledProjectIDs))
		for _, id := range disabledProjectIDs {
			disabled[id] = true
		}
		items := []map[string]any{}
		for _, it := range list {
			isDisabled := it.ComposeProjectID != nil && disabled[*it.ComposeProjectID]
			m := map[string]any{
				"id": it.ID, "domain": it.Domain, "type": it.Type, "upstream": it.Upstream,
				"root": it.Root, "ssl": it.SSL, "advanced": it.Advanced,
				"sslCertPath": it.SSLCertPath, "sslKeyPath": it.SSLKeyPath,
				"maintenance": it.Maintenance, "remark": it.Remark,
				"composeProjectId": it.ComposeProjectID, "certTaskId": it.CertTaskID,
				"disabled": isDisabled, "createdAt": it.CreatedAt,
			}
			if st, ok := states[it.Domain]; ok {
				m["enabled"], m["maintenanceFile"] = st.Enabled, st.Maintenance
			}
			if info, ok := checks[it.Domain]; ok {
				m["check"] = info
			} else if it.CheckStatus != "" {
				// 进程重启/探测尚未覆盖：回退到落库的上次结果，避免列表一片「检测中」
				chk := map[string]any{
					"domain": it.Domain, "status": it.CheckStatus, "code": it.CheckCode,
					"latencyMs": it.CheckLatencyMs, "error": it.CheckError,
				}
				if it.CheckedAt != nil {
					chk["checkedAt"] = *it.CheckedAt
				}
				m["check"] = chk
			}
			items = append(items, m)
		}
		c.JSON(http.StatusOK, items)
	})
	g.POST("", func(c *gin.Context) {
		var req site.SaveReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		siteRec, err := s.Sites.Create(req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "site.create", req.Domain)
		c.JSON(http.StatusOK, siteRec)
	})
	g.PUT("/:id", func(c *gin.Context) {
		var req site.SaveReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		siteRec, err := s.Sites.Update(idParam(c), req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "site.update", req.Domain)
		c.JSON(http.StatusOK, siteRec)
	})
	g.DELETE("/:id", func(c *gin.Context) {
		if err := s.Sites.Delete(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "site.delete", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/:id/state", func(c *gin.Context) {
		var req struct{ State string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Sites.SetState(idParam(c), req.State); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "site.state", c.Param("id")+"→"+req.State)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.GET("/:id/conf", func(c *gin.Context) {
		content, err := s.Sites.GetConf(idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"content": content})
	})
	g.PUT("/:id/conf", func(c *gin.Context) {
		var req struct{ Content string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Sites.SaveConf(idParam(c), req.Content); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "site.conf.save", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/:id/advanced", func(c *gin.Context) {
		var req struct{ Advanced bool }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Sites.SetAdvanced(idParam(c), req.Advanced); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 全局维护页模板
	g.GET("/maintenance-template", func(c *gin.Context) {
		tpl, err := site.GlobalMaintenanceTemplate(s.cfg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"content": tpl})
	})
	g.PUT("/maintenance-template", func(c *gin.Context) {
		var req struct{ Content string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := site.SaveGlobalMaintenanceTemplate(s.cfg, req.Content); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "maintenance.template.save", "")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// ===================== Compose =====================

func (s *Server) registerComposeRoutes() {
	g := s.r.Group("/api/compose", s.auth.Require())
	// 文件夹选择组件：浏览目录（导入向导）
	g.GET("/browse", func(c *gin.Context) {
		res, err := s.Compose.BrowseDir(c.Query("path"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})
	// 解析指定目录的 compose 服务（含 zjpanel.json 声明，供向导「按声明恢复」）
	g.GET("/parse", func(c *gin.Context) {
		services, name, decl, err := s.Compose.ParseDirWithDecl(c.Query("dir"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"services": services, "name": name, "decl": decl})
	})
	g.GET("/scan", func(c *gin.Context) {
		list, err := s.Compose.SyncDB()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	g.GET("/projects", func(c *gin.Context) {
		list, err := s.Compose.ListProjects()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	g.POST("/import", func(c *gin.Context) {
		var req struct {
			Dir, Service, Port, Domain string
			SSL                        bool
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		siteRec, err := s.Compose.Import(req.Dir, req.Service, req.Port, req.Domain, req.SSL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.import", req.Domain+" ← "+req.Dir)
		c.JSON(http.StatusOK, siteRec)
	})
	// 按目录下的 zjpanel.json 恢复站点（删除/迁移后重新导入；不覆盖声明）
	g.POST("/import-decl", func(c *gin.Context) {
		var req struct{ Dir string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		created, updated, err := s.Compose.ImportDecl(req.Dir)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.import-decl", fmt.Sprintf("%s 新建 %d 更新 %d", req.Dir, created, updated))
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "created": created, "updated": updated,
			"message": fmt.Sprintf("已按 zjpanel.json 恢复：新建 %d 个站点，更新 %d 个", created, updated),
		})
	})
	// 应用运维状态：正常/停用。停用是声明（不碰容器），其站点掉线不告警、不计入安全中心。
	g.POST("/:id/disabled", func(c *gin.Context) {
		var req struct{ Value bool }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Compose.SetDisabled(idParam(c), req.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.disabled", fmt.Sprintf("%s=%v", c.Param("id"), req.Value))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 「一切安好？」：先全量探测，再按结果批量修正应用状态——
	// 全部网站 down 的应用标记停用（掉线不告警），有网站 online 的应用恢复 normal。
	// 返回发生变化的清单与方向（正常→停用 / 停用→正常），无变化的应用不返回。
	g.POST("/all-good", func(c *gin.Context) {
		infos, err := s.Check.ProbeAllNow()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		statuses := make(map[string]string, len(infos))
		for domain, info := range infos {
			if info != nil {
				statuses[domain] = info.Status
			}
		}
		res, err := s.Compose.ReconcileAllGood(statuses)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		res.CheckedSites = len(infos)
		unchanged := res.Apps - len(res.Changes) - res.SkippedUnknown
		if unchanged < 0 {
			unchanged = 0
		}
		msg := fmt.Sprintf("已探测 %d 个站点，%d 个应用状态未变化", res.CheckedSites, unchanged)
		if len(res.Changes) > 0 {
			msg = fmt.Sprintf("已探测 %d 个站点：%d 个应用状态变更（正常→停用 %d，停用→正常 %d）",
				res.CheckedSites, len(res.Changes), res.NormalToDisabled, res.DisabledToNormal)
		}
		if res.SkippedNoSites > 0 {
			msg += fmt.Sprintf("；跳过 %d 个无网站应用", res.SkippedNoSites)
		}
		if res.SkippedUnknown > 0 {
			msg += fmt.Sprintf("；%d 个应用探测数据不全未改动", res.SkippedUnknown)
		}
		s.audit(c, "compose.all-good", fmt.Sprintf("sites=%d changes=%d", res.CheckedSites, len(res.Changes)))
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "checkedSites": res.CheckedSites, "apps": res.Apps,
			"normalToDisabled": res.NormalToDisabled, "disabledToNormal": res.DisabledToNormal,
			"skippedNoSites": res.SkippedNoSites, "skippedUnknown": res.SkippedUnknown,
			"changes": res.Changes, "message": msg,
		})
	})
	g.POST("/:id/action", func(c *gin.Context) {
		var req struct{ Action string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 异步启动：立即返回 jobId，实时输出走 WS 主题 compose-act-<id>，快照走 GET /:id/job
		job, err := s.Compose.StartAction(idParam(c), req.Action)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.action", c.Param("id")+"→"+req.Action)
		c.JSON(http.StatusOK, gin.H{"ok": true, "jobId": job.ID, "status": "running"})
	})
	// 操作任务快照：返回自全局行号 since 起的输出行（WS 断连补齐 / 页面刷新恢复实时视图）
	g.GET("/:id/job", func(c *gin.Context) {
		job := s.Compose.LatestJob(idParam(c))
		if job == nil {
			c.JSON(http.StatusOK, gin.H{"found": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"found": true, "job": job.View(intParam(c.DefaultQuery("since", "0")))})
	})
	g.POST("/:id/refresh", func(c *gin.Context) {
		msg, err := s.Compose.Refresh(idParam(c))
		resp := gin.H{"ok": err == nil, "message": msg}
		if err != nil {
			resp["error"] = err.Error()
		}
		c.JSON(http.StatusOK, resp)
	})
	// zjpanel.json：查看声明文件原文
	g.GET("/:id/decl", func(c *gin.Context) {
		content, err := s.Compose.DeclJSON(idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"content": content})
	})
	// 按声明同步站点（导入/迁移恢复；返回孤儿站点提示）
	g.POST("/:id/sync-decl", func(c *gin.Context) {
		created, updated, orphans, err := s.Compose.SyncDecl(idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.sync-decl", fmt.Sprintf("新建 %d 更新 %d", created, updated))
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "created": created, "updated": updated, "orphans": orphans,
			"message": fmt.Sprintf("声明同步完成：新建 %d 个站点，更新 %d 个", created, updated),
		})
	})
	// 立即写出声明文件
	g.POST("/:id/write-decl", func(c *gin.Context) {
		if err := s.Compose.WriteDecl(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 已从面板删除的项目（恢复入口）
	g.GET("/hidden", func(c *gin.Context) {
		list, err := s.Compose.HiddenProjects()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	// 恢复已删除的项目（仅回到列表，不重建站点）
	g.POST("/:id/restore", func(c *gin.Context) {
		if err := s.Compose.Restore(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "compose.restore", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 删除项目：删关联网站 + zjpanel.json，并从面板列表移除（目录与容器不动，可恢复/重新导入）
	g.DELETE("/:id", func(c *gin.Context) {
		removed, err := s.Compose.Remove(idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "removed": removed})
			return
		}
		s.audit(c, "compose.remove", fmt.Sprintf("删除站点 %d 个", len(removed)))
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "removed": removed,
			"message": fmt.Sprintf("已删除 %d 个关联网站，项目已从列表移除（容器照常运行，zjpanel.json 已保留）", len(removed)),
		})
	})
}

// ===================== SSH 主机管理（终端用） =====================

func (s *Server) registerSshRoutes() {
	g := s.r.Group("/api/ssh", s.auth.Require())
	g.GET("/hosts", func(c *gin.Context) {
		var list []model.SshHost
		// 内置「本机」只服务主机页的本地 Shell，不进终端页的 SSH 清单（它没有 SSH 凭据）。
		// OR local IS NULL 兼容早于 local 列的老库：迁移前这些老主机是 NULL，不能漏掉。
		s.db.Where("local = ? OR local IS NULL", false).Order("id").Find(&list)
		items := []map[string]any{}
		for _, h := range list {
			items = append(items, map[string]any{
				"id": h.ID, "name": h.Name, "host": h.Host, "port": h.Port,
				"username": h.Username, "authType": h.AuthType, "createdAt": h.CreatedAt,
			})
		}
		c.JSON(http.StatusOK, items)
	})
	g.POST("/hosts", func(c *gin.Context) {
		var req struct {
			ID         uint   `json:"id"`
			Name       string `json:"name"`
			Host       string `json:"host"`
			Port       int    `json:"port"`
			Username   string `json:"username"`
			AuthType   string `json:"authType"`
			Password   string `json:"password"`
			PrivateKey string `json:"privateKey"`
			Passphrase string `json:"passphrase"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Host == "" || req.Username == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "host/username 必填"})
			return
		}
		if req.Port <= 0 {
			req.Port = 22
		}
		if req.AuthType != "password" && req.AuthType != "key" {
			req.AuthType = "password"
		}
		if req.AuthType == "password" && req.Password == "" && req.ID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "密码必填"})
			return
		}
		if req.AuthType == "key" && req.PrivateKey == "" && req.ID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "私钥必填"})
			return
		}
		h := model.SshHost{ID: req.ID, Name: req.Name, Host: req.Host, Port: req.Port, Username: req.Username, AuthType: req.AuthType}
		if req.ID > 0 {
			if err := s.db.First(&h, req.ID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "主机不存在"})
				return
			}
		}
		if req.Password != "" {
			h.PasswordEnc = s.box.SealCtx(pkg.CtxSSHPassword, req.Password)
		}
		if req.PrivateKey != "" {
			h.PrivateKeyEnc = s.box.SealCtx(pkg.CtxSSHPrivateKey, req.PrivateKey)
		}
		if req.Passphrase != "" {
			h.PassphraseEnc = s.box.SealCtx(pkg.CtxSSHPassphrase, req.Passphrase)
		}
		if err := s.db.Save(&h).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "ssh.host.save", h.Host)
		c.JSON(http.StatusOK, gin.H{"ok": true, "id": h.ID})
	})
	g.DELETE("/hosts/:id", func(c *gin.Context) {
		var h model.SshHost
		if err := s.db.First(&h, idParam(c)).Error; err == nil && h.Local {
			c.JSON(http.StatusBadRequest, gin.H{"error": "内置本机不可删除"})
			return
		}
		s.db.Delete(&model.SshHost{}, idParam(c))
		s.audit(c, "ssh.host.delete", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 测试连接
	g.POST("/hosts/:id/test", func(c *gin.Context) {
		client, err := terminal.DialHostByID(s.db, s.box, idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		_ = client.Close()
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// ===================== 终端会话管理（刷新恢复标签用） =====================

func (s *Server) registerTerminalRoutes() {
	g := s.r.Group("/api/terminal", s.auth.Require())
	g.GET("/sessions", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Term.List())
	})
	g.DELETE("/sessions/:id", func(c *gin.Context) {
		s.Term.CloseSession(c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// ===================== 公共维护页渲染（nginx 反代至此，无需登录） =====================

func (s *Server) registerMaintenanceRoute() {
	s.r.GET("/_maintenance/:domain", func(c *gin.Context) {
		domain := c.Param("domain")
		override := ""
		var siteRec struct {
			MaintenanceHTML string
		}
		if err := s.db.Table("sites").Select("maintenance_html as maintenance_html").Where("domain = ?", domain).Scan(&siteRec).Error; err == nil {
			override = siteRec.MaintenanceHTML
		}
		html := site.RenderMaintenance(s.cfg, override, domain, c.Query("message"))
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
	})
}

// ===================== 设置 / 审计 =====================

// entryPathSettingKey 安全入口路径在 Setting 表中的键。
const entryPathSettingKey = "security.entry_path"

// EntryPath 读取当前安全入口（规范化为带前导 /、无尾斜杠；未配置返回空串）。
// 应急开关 ZJPANEL_ENTRY_OFF=1 时强制关闭（忘记入口、无法登录时可在部署环境设置后重启）。
func (s *Server) EntryPath() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("ZJPANEL_ENTRY_OFF"))); v == "1" || v == "true" || v == "yes" {
		return ""
	}
	var rec model.Setting
	if err := s.db.Where("key = ?", entryPathSettingKey).First(&rec).Error; err != nil {
		return ""
	}
	p, err := NormalizeEntryPath(rec.Value)
	if err != nil {
		return ""
	}
	return p
}

// NormalizeEntryPath 校验并规范化安全入口：空=关闭；必须 / 开头、3-64 位字母数字_-，
// 且不能与后端/静态保留前缀冲突。
func NormalizeEntryPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", nil
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" {
		return "", nil
	}
	if len(p) < 3 || len(p) > 64 {
		return "", fmt.Errorf("入口路径长度需在 3-64 个字符之间")
	}
	for _, r := range p[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", fmt.Errorf("入口路径仅支持字母、数字、下划线与连字符")
		}
	}
	reserved := []string{
		"/api", "/mcp", "/assets", "/map", "/plugin-assets", "/_maintenance",
		"/favicon", "/site.webmanifest", "/login", "/plugin",
	}
	for _, r := range reserved {
		if p == r || strings.HasPrefix(p, r+"/") {
			return "", fmt.Errorf("入口路径不能使用保留前缀 %s", r)
		}
	}
	return p, nil
}

func (s *Server) registerSettingsRoutes() {
	g := s.r.Group("/api/settings", s.auth.Require())
	g.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, map[string]any{
			"dataDir": s.cfg.DataDir, "nginxPrefix": s.cfg.NginxPrefix,
			"nginxLogsDir": s.cfg.NginxLogsDir, "nginxHtmlDir": s.cfg.NginxHTMLDir,
			"composeRoot": s.cfg.ComposeRoot, "challengePort": s.cfg.ChallengePort,
			"updateUrl": s.cfg.UpdateURL, "port": s.cfg.Port,
		})
	})
	g.GET("/audit", func(c *gin.Context) {
		var logs []model.AuditLog
		if err := s.db.Limit(200).Order("id DESC").Find(&logs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, logs)
	})

	// 安全入口：读取当前入口路径（未配置为空串，表示不启用）
	g.GET("/entry", func(c *gin.Context) {
		entry := s.EntryPath()
		c.JSON(http.StatusOK, gin.H{"entryPath": entry, "enabled": entry != ""})
	})

	// 安全入口：保存（空串=关闭，恢复根路径可直接访问）
	g.PUT("/entry", func(c *gin.Context) {
		var req struct {
			EntryPath string `json:"entryPath"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		entry, err := NormalizeEntryPath(req.EntryPath)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		rec := model.Setting{Key: entryPathSettingKey, Value: entry}
		if err := s.db.Save(&rec).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "settings.entry", entry)
		c.JSON(http.StatusOK, gin.H{"ok": true, "entryPath": entry, "enabled": entry != ""})
	})
}

func idParam(c *gin.Context) uint {
	var id uint
	for _, ch := range c.Param("id") {
		if ch < '0' || ch > '9' {
			return 0
		}
		id = id*10 + uint(ch-'0')
	}
	return id
}
