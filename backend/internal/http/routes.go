package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/site"
	"zjpanel/backend/internal/terminal"
)

// RegisterRoutes 模块路由装配（boot 在注入全部服务后调用一次）。
func (s *Server) RegisterRoutes() {
	s.registerNginxRoutes()
	s.registerSiteRoutes()
	s.registerComposeRoutes()
	s.registerSshRoutes()
	s.registerTerminalRoutes()
	s.registerDockerRoutes()
	s.registerSettingsRoutes()
	s.registerMetricsRoutes()
	s.registerLogmapRoutes()
	s.registerCertRoutes()
	s.registerMCPRoutes()
	s.registerUpdateRoutes()
	s.registerPluginRoutes()
	s.registerNotifyRoutes()
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
		items := []map[string]any{}
		for _, it := range list {
			m := map[string]any{
				"id": it.ID, "domain": it.Domain, "type": it.Type, "upstream": it.Upstream,
				"root": it.Root, "ssl": it.SSL, "advanced": it.Advanced,
				"maintenance": it.Maintenance, "remark": it.Remark,
				"composeProjectId": it.ComposeProjectID, "certTaskId": it.CertTaskID,
				"createdAt": it.CreatedAt,
			}
			if st, ok := states[it.Domain]; ok {
				m["enabled"], m["maintenanceFile"] = st.Enabled, st.Maintenance
			}
			if info, ok := checks[it.Domain]; ok {
				m["check"] = info
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
	// 解析指定目录的 compose 服务
	g.GET("/parse", func(c *gin.Context) {
		services, name, err := s.Compose.ParseDir(c.Query("dir"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"services": services, "name": name})
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
	g.POST("/:id/action", func(c *gin.Context) {
		var req struct{ Action string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := s.Compose.Action(idParam(c), req.Action)
		resp := gin.H{"ok": err == nil, "output": out}
		if err != nil {
			resp["error"] = err.Error()
		}
		s.audit(c, "compose.action", c.Param("id")+"→"+req.Action)
		c.JSON(http.StatusOK, resp)
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
}

// ===================== SSH 主机管理（终端用） =====================

func (s *Server) registerSshRoutes() {
	g := s.r.Group("/api/ssh", s.auth.Require())
	g.GET("/hosts", func(c *gin.Context) {
		var list []model.SshHost
		s.db.Order("id").Find(&list)
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
			h.PasswordEnc = s.box.Seal(req.Password)
		}
		if req.PrivateKey != "" {
			h.PrivateKeyEnc = s.box.Seal(req.PrivateKey)
		}
		if req.Passphrase != "" {
			h.PassphraseEnc = s.box.Seal(req.Passphrase)
		}
		if err := s.db.Save(&h).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "ssh.host.save", h.Host)
		c.JSON(http.StatusOK, gin.H{"ok": true, "id": h.ID})
	})
	g.DELETE("/hosts/:id", func(c *gin.Context) {
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
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
	})
}

// ===================== 设置 / 审计 =====================

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
