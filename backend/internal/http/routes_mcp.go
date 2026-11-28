package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/agent"
	"zjpanel/backend/internal/backup"
	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/notify"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/plugin"
	sitePkg "zjpanel/backend/internal/site"
	"zjpanel/backend/internal/update"
	"zjpanel/backend/internal/waf"
	sdk "zjpanel/backend/sdk/plugin"
)

const pluginPasswordMask = "********"

// ===================== MCP（聚合端点 + API Key 管理） =====================

func (s *Server) registerMCPRoutes() {
	// /mcp：唯一对外 MCP 端点（API Key 鉴权；核心工具 + 插件工具聚合）
	// API Key 校验缓存：agent 客户端高频轮询时避免每请求全表扫；
	// last_used_at 至多每分钟同步一次。TTL 60 秒，删除 Key 时主动清空缓存保证即时吊销。
	type keyEntry struct{ validUntil, lastSync time.Time }
	var keyMu sync.Mutex
	keyCache := map[string]*keyEntry{}
	keyCacheCheck := func(hash string) (valid, needSync bool) {
		keyMu.Lock()
		defer keyMu.Unlock()
		now := pkg.Now()
		e, ok := keyCache[hash]
		if !ok || now.After(e.validUntil) {
			delete(keyCache, hash)
			return false, false
		}
		if now.Sub(e.lastSync) >= time.Minute {
			e.lastSync = now
			return true, true
		}
		return true, false
	}
	keyCachePut := func(hash string) {
		keyMu.Lock()
		defer keyMu.Unlock()
		keyCache[hash] = &keyEntry{validUntil: pkg.Now().Add(time.Minute), lastSync: pkg.Now()}
	}
	keyCacheClear := func() {
		keyMu.Lock()
		defer keyMu.Unlock()
		keyCache = map[string]*keyEntry{}
	}
	mcpAuth := func(c *gin.Context) {
		rateKey := "mcp:" + c.ClientIP()
		if !loginAllow(rateKey) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "尝试过于频繁，请稍后再试"})
			c.Abort()
			return
		}
		key := mcp.ValidateKey(c.Request)
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 API Key（X-Api-Key 或 Authorization: Bearer）"})
			c.Abort()
			return
		}
		keyHash := pkg.SHA256Hex(key)
		needSync := false
		if valid, sync := keyCacheCheck(keyHash); valid {
			needSync = sync
		} else {
			var keys []model.ApiKey
			s.db.Find(&keys)
			hit := false
			for _, k := range keys {
				if subtle.ConstantTimeCompare([]byte(keyHash), []byte(k.Hash)) == 1 {
					hit = true
					break
				}
			}
			if !hit {
				loginRecordFail(rateKey)
				c.JSON(http.StatusUnauthorized, gin.H{"error": "API Key 无效"})
				c.Abort()
				return
			}
			keyCachePut(keyHash)
			needSync = true
		}
		if needSync {
			now := pkg.Now()
			s.db.Model(&model.ApiKey{}).Where("hash = ?", keyHash).Update("last_used_at", &now)
		}
		c.Next()
	}
	s.r.POST("/mcp", mcpAuth, gin.WrapH(s.MCPSrv.Handler()))
	s.r.GET("/mcp", mcpAuth, func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "仅支持 POST（streamable http）"})
	})

	g := s.r.Group("/api/mcp", s.auth.Require())
	g.GET("/keys", func(c *gin.Context) {
		var keys []model.ApiKey
		s.db.Order("id").Find(&keys)
		c.JSON(http.StatusOK, keys)
	})
	g.POST("/keys", func(c *gin.Context) {
		var req struct{ Name string }
		if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "名称必填"})
			return
		}
		plain := "zjp_" + pkg.RandomHex(24)
		rec := model.ApiKey{Name: req.Name, Prefix: plain[:10], Hash: pkg.SHA256Hex(plain), CreatedAt: pkg.Now()}
		if err := s.db.Create(&rec).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "mcp.key.create", req.Name)
		c.JSON(http.StatusOK, gin.H{"id": rec.ID, "name": rec.Name, "key": plain, "note": "请立即保存，明文不再展示"})
	})
	g.DELETE("/keys/:id", func(c *gin.Context) {
		s.db.Delete(&model.ApiKey{}, idParam(c))
		keyCacheClear() // 吊销即时生效：清空校验缓存，后续请求全量回库比对
		s.audit(c, "mcp.key.delete", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.GET("/tools", func(c *gin.Context) {
		tools := []map[string]any{}
		for _, t := range s.MCPReg.List() {
			tools = append(tools, map[string]any{
				"name": t.Name, "description": t.Description, "owner": t.Owner,
			})
		}
		c.JSON(http.StatusOK, tools)
	})
}

// ===================== 更新 =====================

func (s *Server) registerUpdateRoutes() {
	g := s.r.Group("/api/update", s.auth.Require())
	g.GET("/current", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"version": update.CurrentVersion()})
	})
	g.POST("/check", func(c *gin.Context) {
		res, err := s.Updater.Check()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})
	g.POST("/apply", func(c *gin.Context) {
		msg, err := s.Updater.Apply()
		resp := gin.H{"ok": err == nil, "message": msg}
		if err != nil {
			resp["error"] = err.Error()
		}
		s.audit(c, "update.apply", msg)
		c.JSON(http.StatusOK, resp)
	})
}

// ===================== 插件 =====================

func (s *Server) registerPluginRoutes() {
	g := s.r.Group("/api/plugins", s.auth.Require())
	g.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Plugins.List())
	})
	g.POST("/install", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<20)
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 zip 文件或文件过大（上限 32MB）"})
			return
		}
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		if err != nil || len(data) > 32<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取 zip 文件失败或超出 32MB 限制"})
			return
		}
		id, err := s.Plugins.InstallZip(data)
		resp := gin.H{"ok": err == nil, "id": id}
		if err != nil {
			resp["error"] = err.Error()
			s.audit(c, "plugin.install", fh.Filename+" 失败: "+err.Error()) // 失败也留痕，记录来源文件名与原因
		} else {
			s.audit(c, "plugin.install", id)
		}
		c.JSON(http.StatusOK, resp)
	})
	// 从 GitHub 安装：仓库地址（owner/repo）、归档 zip 或 Release zip 直链。
	g.POST("/install-github", func(c *gin.Context) {
		var req struct {
			URL string `json:"url"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.URL) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 GitHub 仓库地址"})
			return
		}
		// 失败同样留痕：detail 记来源 URL 与原因，便于排查仓库是否公开、地址是否有效。
		failDetail := req.URL + " 失败: "
		data, source, err := plugin.DownloadGitHubZip(req.URL)
		if err != nil {
			s.audit(c, "plugin.install.github", failDetail+err.Error())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		id, err := s.Plugins.InstallZip(data)
		if err != nil {
			s.audit(c, "plugin.install.github", failDetail+err.Error())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "plugin.install.github", id)
		c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "source": source})
	})
	g.POST("/:id/enable", func(c *gin.Context) {
		if err := s.Plugins.Enable(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "plugin.enable", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/:id/disable", func(c *gin.Context) {
		if err := s.Plugins.Disable(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "plugin.disable", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.DELETE("/:id", func(c *gin.Context) {
		if err := s.Plugins.Uninstall(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "plugin.uninstall", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.GET("/menu", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Plugins.MenuItems())
	})
	g.GET("/:id/settings", func(c *gin.Context) {
		id := c.Param("id")
		if !plugin.ValidID(id) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "非法插件 ID"})
			return
		}
		// 直接返回 sdk.SettingDef（自带小写 json tag）；password 类型仅返回掩码占位，不明文回显
		defs := []sdk.SettingDef{}
		values := map[string]string{}
		for _, p := range s.Plugins.List() {
			if p.ID != id {
				continue
			}
			for _, sd := range p.Settings {
				defs = append(defs, sd)
				var rec model.PluginSetting
				if err := s.db.Where("plugin_id = ? AND key = ?", id, sd.Key).First(&rec).Error; err == nil {
					if sd.Type == "password" && rec.Value != "" {
						values[sd.Key] = pluginPasswordMask
					} else {
						values[sd.Key] = rec.Value
					}
				} else if sd.Default != "" {
					values[sd.Key] = sd.Default
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"settings": defs, "values": values})
	})
	savePluginSettings := func(c *gin.Context) {
		id := c.Param("id")
		if !plugin.ValidID(id) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "非法插件 ID"})
			return
		}
		var req map[string]string
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pwdKeys := map[string]bool{}
		for _, p := range s.Plugins.List() {
			if p.ID == id {
				for _, sd := range p.Settings {
					if sd.Type == "password" {
						pwdKeys[sd.Key] = true
					}
				}
			}
		}
		for k, v := range req {
			if pwdKeys[k] {
				if v == pluginPasswordMask {
					continue // 未修改密码：保留既有密文
				}
				if v != "" && s.box != nil {
					v = s.box.SealCtx(pkg.CtxPluginSetting(id, k), v)
				}
			}
			var rec model.PluginSetting
			if err := s.db.Where("plugin_id = ? AND key = ?", id, k).First(&rec).Error; err != nil {
				s.db.Create(&model.PluginSetting{PluginID: id, Key: k, Value: v})
			} else {
				s.db.Model(&rec).Update("value", v)
			}
		}
		// 设置变更：重启插件以生效
		_ = s.Plugins.Disable(id)
		_ = s.Plugins.Enable(id)
		s.audit(c, "plugin.settings", id)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
	g.PUT("/:id/settings", savePluginSettings)
	g.POST("/:id/settings", savePluginSettings)

	// 插件 API 动态派发
	s.r.Any("/api/plugin/:id/*path", s.auth.Require(), func(c *gin.Context) {
		s.Plugins.DispatchRoute(c.Writer, c.Request, c.Param("id"), c.Param("path"))
	})
	// 插件免鉴权公开路由（外部服务回调，如 CI webhook；签名/令牌由插件自行校验）
	s.r.Any("/api/pub/plugin/:id/*path", func(c *gin.Context) {
		s.Plugins.DispatchPublicRoute(c.Writer, c.Request, c.Param("id"), c.Param("path"))
	})
	// 插件前端资产（iframe 承载；同源 cookie 鉴权）
	s.r.GET("/plugin-assets/:id/*path", s.auth.Require(), func(c *gin.Context) {
		s.Plugins.ServeAsset(c.Writer, c.Request, c.Param("id"), c.Param("path"))
	})
	s.r.GET("/plugin-assets/:id", s.auth.Require(), func(c *gin.Context) {
		s.Plugins.ServeAsset(c.Writer, c.Request, c.Param("id"), "/index.html")
	})
}

// registerMCPTools 注册核心 MCP 工具（与插件工具聚合在同一注册表，经唯一 /mcp 对外暴露）。
func (s *Server) registerMCPTools() {
	jstr := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	reg := func(name, desc string, schema map[string]any, h func(args map[string]any) (string, error)) {
		_ = s.MCPReg.Register(&mcp.Tool{Name: name, Description: desc, InputSchema: schema, Handler: h, Owner: "core"})
	}
	// regMut 处置类工具：AI 运维智能体的「仅观察/执行前确认」模式依赖此标注做过滤与拦截
	regMut := func(name, desc string, schema map[string]any, h func(args map[string]any) (string, error)) {
		_ = s.MCPReg.Register(&mcp.Tool{Name: name, Description: desc, InputSchema: schema, Handler: h, Owner: "core", Mutating: true})
	}
	reg("panel_notify", "发送一条面板统一通知（站内信必发；邮件按类型订阅设置发送）",
		map[string]any{"type": "object", "properties": map[string]any{
			"type":  map[string]any{"type": "string", "description": "消息类型，建议带前缀（如 ops.backup）"},
			"level": map[string]any{"type": "string", "enum": []string{"info", "warn", "error"}, "description": "级别，默认 info"},
			"title": map[string]any{"type": "string", "description": "标题"},
			"body":  map[string]any{"type": "string", "description": "正文"},
		}, "required": []string{"title"}},
		func(args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			if s.Notify == nil || strings.TrimSpace(title) == "" {
				return "", fmt.Errorf("title 必填")
			}
			nType, _ := args["type"].(string)
			if nType == "" {
				nType = "mcp.manual"
			}
			level, _ := args["level"].(string)
			body, _ := args["body"].(string)
			s.Notify.Send("mcp", notify.Input{Type: nType, Level: level, Title: title, Body: body})
			return jstr(map[string]any{"ok": true}), nil
		})
	reg("panel_info", "获取面板概览（版本/站点数/容器数/证书临期/安全态势/最近备份任务）。排查问题的首查工具",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			var siteCnt int64
			s.db.Model(&model.Site{}).Count(&siteCnt)
			containers, _ := s.Docker.ContainersList()
			out := map[string]any{
				"version":    update.CurrentVersion(),
				"sites":      siteCnt,
				"containers": len(containers),
			}
			// 证书临期：30 天内到期的任务数（与 list_cert_tasks 的 daysLeft 口径一致）
			var tasks []model.CertTask
			s.db.Find(&tasks)
			expiring := 0
			for _, t := range tasks {
				if info := s.Cert.LatestCert(t.ID); info != nil && time.Until(info.NotAfter).Hours() < 30*24 {
					expiring++
				}
			}
			out["certTasks"], out["certExpiringIn30d"] = len(tasks), expiring
			// 安全态势：健康级别与当前活跃的严重事件数（快照实时生成，无 DB 开销）
			snap := s.Security.Latest()
			out["securityLevel"], out["securityCriticalActive"] = snap.Level, snap.Active
			// 最近一次备份任务（可能为空 = 从未备份）
			if jb := s.Backup.LatestJob(); jb != nil {
				v := jb.View(0)
				out["lastBackup"] = map[string]any{"kind": v.Kind, "status": v.Status, "startedAt": v.StartedAt}
			}
			return jstr(out), nil
		})
	reg("list_sites", "列出全部站点（状态/延迟/维护开关）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			list, err := s.Sites.List()
			if err != nil {
				return "", err
			}
			checks := s.Check.All()
			out := []map[string]any{}
			for _, it := range list {
				m := map[string]any{"id": it.ID, "domain": it.Domain, "type": it.Type, "upstream": it.Upstream, "ssl": it.SSL, "maintenance": it.Maintenance}
				if ci, ok := checks[it.Domain]; ok {
					m["status"], m["latencyMs"] = ci.Status, ci.LatencyMs
				}
				out = append(out, m)
			}
			return jstr(out), nil
		})
	regMut("create_proxy_site", "创建反向代理站点（写入 nginx conf 并启用）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain":   map[string]any{"type": "string", "description": "站点域名"},
				"upstream": map[string]any{"type": "string", "description": "反代目标 127.0.0.1:端口"},
			},
			"required": []string{"domain", "upstream"},
		},
		func(args map[string]any) (string, error) {
			domain, _ := args["domain"].(string)
			upstream, _ := args["upstream"].(string)
			siteRec, err := s.Sites.Create(sitePkg.SaveReq{Domain: domain, Type: "proxy", Upstream: upstream})
			if err != nil {
				return "", err
			}
			return "站点已创建: " + siteRec.Domain + " → " + upstream, nil
		})
	regMut("toggle_site", "切换站点状态（enable|disable|maintenance）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"siteId": map[string]any{"type": "integer", "description": "站点 ID"},
				"state":  map[string]any{"type": "string", "description": "enable | disable | maintenance", "enum": []string{"enable", "disable", "maintenance"}},
			},
			"required": []string{"siteId", "state"},
		},
		func(args map[string]any) (string, error) {
			state, _ := args["state"].(string)
			if err := s.Sites.SetState(uint(numOf(args["siteId"])), state); err != nil {
				return "", err
			}
			return "站点状态已切换为 " + state, nil
		})
	reg("nginx_test_reload", "测试 nginx 配置并重载",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			out, err := s.Nginx.Reload()
			if err != nil {
				return out, err
			}
			return "nginx 配置测试通过并已重载", nil
		})
	reg("list_containers", "列出 Docker 容器",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			list, err := s.Docker.ContainersList()
			if err != nil {
				return "", err
			}
			return jstr(list), nil
		})
	regMut("container_action", "容器操作（start|stop|restart|remove）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"containerId": map[string]any{"type": "string"},
				"action":      map[string]any{"type": "string", "enum": []string{"start", "stop", "restart", "remove"}},
			},
			"required": []string{"containerId", "action"},
		},
		func(args map[string]any) (string, error) {
			cid, _ := args["containerId"].(string)
			action, _ := args["action"].(string)
			if _, err := s.Docker.ContainerAction(cid, action); err != nil {
				return "", err
			}
			return "容器 " + cid + " 已执行 " + action, nil
		})
	reg("container_recent_logs", "获取容器最近日志",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"containerId": map[string]any{"type": "string"},
				"tail":        map[string]any{"type": "integer", "description": "行数，默认 100"},
			},
			"required": []string{"containerId"},
		},
		func(args map[string]any) (string, error) {
			cid, _ := args["containerId"].(string)
			tail := "100"
			if t := int(numOf(args["tail"])); t > 0 {
				tail = fmt.Sprintf("%d", t)
			}
			r, tty, err := s.Docker.ContainerLogsHandle(cid, false, tail)
			if err != nil {
				return "", err
			}
			defer r.Close()
			var buf bytes.Buffer
			if tty {
				_, _ = io.Copy(&buf, r)
			} else {
				_, _ = stdcopy.StdCopy(&buf, &buf, r)
			}
			return buf.String(), nil
		})
	reg("get_metrics", "获取服务器实时指标",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			return jstr(s.Metrics.Snapshot()), nil
		})
	reg("list_cert_tasks", "列出证书任务与到期状态",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			var list []model.CertTask
			s.db.Order("id").Find(&list)
			out := []map[string]any{}
			for _, t := range list {
				m := map[string]any{"id": t.ID, "name": t.Name, "domains": t.DomainList(), "enabled": t.Enabled}
				if info := s.Cert.LatestCert(t.ID); info != nil {
					m["notAfter"] = info.NotAfter.Format("2006-01-02")
					m["daysLeft"] = int(time.Until(info.NotAfter).Hours() / 24)
				}
				out = append(out, m)
			}
			return jstr(out), nil
		})
	regMut("run_cert_task", "立即执行证书任务（申请+部署）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"taskId": map[string]any{"type": "integer"},
				"force":  map[string]any{"type": "boolean", "description": "未到期也强制续签"},
			},
			"required": []string{"taskId"},
		},
		func(args map[string]any) (string, error) {
			runID, err := s.Cert.RunTask(uint(numOf(args["taskId"])), args["force"] == true)
			if err != nil {
				return fmt.Sprintf("任务失败（runId=%d）", runID), err
			}
			return fmt.Sprintf("任务执行成功（runId=%d）", runID), nil
		})
	reg("compose_projects", "列出已导入的 docker-compose 项目",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			list, err := s.Compose.ListProjects()
			if err != nil {
				return "", err
			}
			return jstr(list), nil
		})
	regMut("compose_action", "compose 项目操作（up|down|restart）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"projectId": map[string]any{"type": "integer"},
				"action":    map[string]any{"type": "string", "enum": []string{"up", "down", "restart"}},
			},
			"required": []string{"projectId", "action"},
		},
		func(args map[string]any) (string, error) {
			action, _ := args["action"].(string)
			out, err := s.Compose.Action(uint(numOf(args["projectId"])), action)
			if err != nil {
				return out, err
			}
			return "compose " + action + " 完成", nil
		})
	// ---- 主机管理（与 HTTP API 共用同一套模型与审计；只读优先） ----
	reg("host_list", "列出全部主机及 Agent 状态（在线/离线/版本/是否落后）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			var hosts []model.SshHost
			s.db.Order("local desc, id").Find(&hosts)
			out := []map[string]any{}
			for _, h := range hosts {
				out = append(out, map[string]any{
					"id": h.ID, "name": h.Name, "host": h.Host, "port": h.Port,
					"local": h.Local, "group": h.Group,
					"agentStatus": h.AgentStatus, "agentVersion": h.AgentVersion,
					"online": s.Agent.Online(h.ID), "claimed": h.Claimed,
					"outdated": h.AgentVersion != "" && h.AgentVersion != buildinfo.Version,
					"lastSeen": h.AgentLastSeen,
				})
			}
			return jstr(out), nil
		})
	reg("host_metrics", "获取某台主机的实时指标（负载/内存/磁盘/连接/端口）",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"hostId": map[string]any{"type": "integer"}},
			"required":   []string{"hostId"},
		},
		func(args map[string]any) (string, error) {
			id := uint(numOf(args["hostId"]))
			m := s.Agent.LatestMetrics(id)
			if m == nil {
				return jstr(map[string]any{"online": s.Agent.Online(id), "metrics": nil}), nil
			}
			return jstr(map[string]any{"online": true, "metrics": m}), nil
		})
	reg("host_events", "获取某台主机最近的 Agent 事件（上线/指令/审计）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hostId": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer", "description": "默认 20"},
			},
			"required": []string{"hostId"},
		},
		func(args map[string]any) (string, error) {
			limit := int(numOf(args["limit"]))
			if limit <= 0 {
				limit = 20
			}
			return jstr(s.Agent.RecentEvents(uint(numOf(args["hostId"])), limit)), nil
		})
	reg("host_check", "对某台主机执行一次防御基线检查（只读审计，不改系统配置，结果落库）。kind: ssh=SSH 基线 accounts=账号 persistence=持久化 exposure=暴露面 webshell=后门特征",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hostId": map[string]any{"type": "integer", "description": "主机 ID"},
				"kind":   map[string]any{"type": "string", "enum": agent.AuditKinds, "description": "检查类型，默认 ssh"},
			},
			"required": []string{"hostId"},
		},
		func(args map[string]any) (string, error) {
			kind, _ := args["kind"].(string)
			if kind == "" {
				kind = "ssh"
			}
			payload, err := s.Agent.Audit(uint(numOf(args["hostId"])), kind)
			if err != nil {
				return "", err
			}
			return jstr(payload), nil
		})
	reg("host_firewall_status", "获取某台主机的防火墙状态与封禁列表（nftables 独立表 inet zjpanel）",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"hostId": map[string]any{"type": "integer", "description": "主机 ID"}},
			"required":   []string{"hostId"},
		},
		func(args map[string]any) (string, error) {
			res, err := s.Agent.Exec(uint(numOf(args["hostId"])), "firewall_status", nil, 30*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("%s", res.Output)
			}
			return res.Output, nil
		})
	regMut("host_ban_ip", "在某台主机的防火墙上封禁一个 IPv4（带 TTL，到期自动解封）",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hostId":  map[string]any{"type": "integer", "description": "主机 ID"},
				"ip":      map[string]any{"type": "string", "description": "要封禁的 IPv4 地址"},
				"seconds": map[string]any{"type": "integer", "description": "封禁时长秒数，默认 3600"},
			},
			"required": []string{"hostId", "ip"},
		},
		func(args map[string]any) (string, error) {
			id := uint(numOf(args["hostId"]))
			ip, _ := args["ip"].(string)
			seconds := int(numOf(args["seconds"]))
			if seconds <= 0 {
				seconds = 3600
			}
			res, err := s.Agent.Exec(id, "ban_ip", map[string]any{"ip": ip, "seconds": seconds}, 30*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("%s", res.Output)
			}
			s.auditMCP("host.ban."+ip, fmt.Sprintf("host=%d seconds=%d", id, seconds))
			return "已封禁 " + ip + "（" + fmt.Sprintf("%d", seconds) + " 秒后自动解封）", nil
		})
	regMut("host_unban_ip", "解除某台主机防火墙上的 IPv4 封禁",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hostId": map[string]any{"type": "integer", "description": "主机 ID"},
				"ip":     map[string]any{"type": "string", "description": "要解封的 IPv4 地址"},
			},
			"required": []string{"hostId", "ip"},
		},
		func(args map[string]any) (string, error) {
			id := uint(numOf(args["hostId"]))
			ip, _ := args["ip"].(string)
			res, err := s.Agent.Exec(id, "unban_ip", map[string]any{"ip": ip}, 30*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("%s", res.Output)
			}
			s.auditMCP("host.unban."+ip, fmt.Sprintf("host=%d", id))
			return "已解封 " + ip, nil
		})

	// ---- 安全中心（与 HTTP API 共用同一套 service 与数据；处置类操作留审计） ----
	reg("security_overview", "获取安全中心态势快照（RPS/错误率/健康分/Top IP 与国家/路径/活跃严重事件/生效策略）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			return jstr(s.Security.Latest()), nil
		})
	reg("security_events", "获取最近安全事件流（告警/自动封禁/严重命中等，新→旧）",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"limit": map[string]any{"type": "integer", "description": "条数，默认 50，上限 200"}},
		},
		func(args map[string]any) (string, error) {
			limit := int(numOf(args["limit"]))
			if limit <= 0 {
				limit = 50
			}
			if limit > 200 {
				limit = 200
			}
			return jstr(s.Security.RecentEvents(limit)), nil
		})
	regMut("security_block_ip", "在面板安全策略中封禁一个 IP 或 CIDR（下发 nginx，TTL 到期自动解封）。minutes 缺省 60，0 表示永久",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value":   map[string]any{"type": "string", "description": "IP 或 CIDR"},
				"minutes": map[string]any{"type": "integer", "description": "封禁时长（分钟），缺省 60；0=永久封禁"},
				"reason":  map[string]any{"type": "string", "description": "封禁原因（进审计与策略备注）"},
			},
			"required": []string{"value"},
		},
		func(args map[string]any) (string, error) {
			value, _ := args["value"].(string)
			minutes := int(numOf(args["minutes"]))
			if minutes < 0 {
				minutes = 60
			}
			reason, _ := args["reason"].(string)
			if err := s.Security.BlockIP(value, minutes, reason, false); err != nil {
				return "", err
			}
			s.auditMCP("security.block.ip", fmt.Sprintf("%s minutes=%d reason=%s", value, minutes, reason))
			if minutes > 0 {
				return "已封禁 " + value + "（" + fmt.Sprintf("%d 分钟", minutes) + "）", nil
			}
			return "已永久封禁 " + value, nil
		})
	regMut("security_unblock_ip", "解除面板安全策略中的 IP/CIDR 封禁",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string", "description": "IP 或 CIDR"}},
			"required":   []string{"value"},
		},
		func(args map[string]any) (string, error) {
			value, _ := args["value"].(string)
			if err := s.Security.UnblockIP(value); err != nil {
				return "", err
			}
			s.auditMCP("security.unblock.ip", value)
			return "已解封 " + value, nil
		})

	// ---- WAF（规则引擎只读视图；规则增改不暴露给 MCP） ----
	if s.WAF != nil {
		reg("waf_stats", "获取 WAF 统计（开关/模式/规则数/最近命中与近一小时 Top 命中 IP、规则）",
			map[string]any{"type": "object", "properties": map[string]any{}},
			func(args map[string]any) (string, error) {
				return jstr(s.WAF.Stat()), nil
			})
		reg("waf_recent_hits", "查询 WAF 最近命中日志（可按 IP/分类/规则/站点过滤）",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit":    map[string]any{"type": "integer", "description": "条数，默认 100，上限 500"},
					"ip":       map[string]any{"type": "string", "description": "按来源 IP 过滤"},
					"category": map[string]any{"type": "string", "description": "按攻击分类过滤（如 sqli/xss/rce）"},
					"ruleId":   map[string]any{"type": "string", "description": "按规则 ID 过滤"},
					"site":     map[string]any{"type": "string", "description": "按站点过滤"},
				},
			},
			func(args map[string]any) (string, error) {
				limit := int(numOf(args["limit"]))
				ip, _ := args["ip"].(string)
				category, _ := args["category"].(string)
				ruleID, _ := args["ruleId"].(string)
				siteName, _ := args["site"].(string)
				return jstr(s.WAF.Hits(waf.HitFilter{
					IP: ip, Category: category, RuleID: ruleID, Site: siteName, Limit: limit,
				})), nil
			})
	}

	// ---- 备份与云同步（查询与生成；恢复/删除不做 MCP 入口，危险写操作留给面板） ----
	reg("backup_items", "列出可备份项清单（backup_create 的 items 合法取值及说明）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			return jstr(map[string]any{"items": backup.ItemMetas()}), nil
		})
	reg("backup_list", "列出本地备份包（名称/大小/包含项/是否加密/是否已同步远程）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			list, err := s.Backup.List()
			if err != nil {
				return "", err
			}
			return jstr(map[string]any{"list": list}), nil
		})
	reg("backup_create", "生成一份备份包（异步任务，立即返回 jobId；items 为空表示完整配置迁移）。用 backup_status 查进度",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "要备份的项（合法取值见 backup_items）；缺省=完整备份",
				},
			},
		},
		func(args map[string]any) (string, error) {
			raw, _ := args["items"].([]any)
			items := make([]string, 0, len(raw))
			for _, v := range raw {
				if str, ok := v.(string); ok && str != "" {
					items = append(items, str)
				}
			}
			job, err := s.Backup.StartBuild(items, backup.TriggerManual)
			if err != nil {
				return "", err
			}
			s.auditMCP("backup.create", job.Target+" (via MCP)")
			return jstr(map[string]any{"ok": true, "jobId": job.ID, "hint": "调用 backup_status 查询进度"}), nil
		})
	reg("backup_status", "查询最近一次备份/上传/下载任务的状态与最后若干行日志",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			jb := s.Backup.LatestJob()
			if jb == nil {
				return jstr(map[string]any{"job": nil}), nil
			}
			v := jb.View(0)
			if len(v.Lines) > 10 {
				v.Lines = v.Lines[len(v.Lines)-10:] // 只回最后 10 行，避免长日志撑爆上下文
			}
			return jstr(map[string]any{"job": v}), nil
		})
	reg("backup_schedule", "查看定时备份配置与远程云存储状态（只读；远程凭据不回显）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			return jstr(map[string]any{"schedule": s.Backup.ScheduleView(), "remote": s.Backup.RemoteConfigView()}), nil
		})

	// ---- 版本更新（只读检查；apply 会中断面板与 MCP 会话，不暴露给 MCP） ----
	reg("update_check", "检查面板是否有新版本（只读；不执行更新）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			res, err := s.Updater.Check()
			if err != nil {
				return "", err
			}
			return jstr(res), nil
		})
}

// auditMCP MCP 工具的写操作留痕（无 HTTP 上下文，IP 记为 mcp 以区分来源）。
func (s *Server) auditMCP(action, detail string) {
	s.db.Create(&model.AuditLog{Time: timeNow(), Action: action, Detail: detail, IP: "mcp"})
}
