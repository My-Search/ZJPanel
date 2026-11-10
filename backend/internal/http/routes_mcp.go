package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	sitePkg "zjpanel/backend/internal/site"
	"zjpanel/backend/internal/update"
)

// ===================== MCP（聚合端点 + API Key 管理） =====================

func (s *Server) registerMCPRoutes() {
	// /mcp：唯一对外 MCP 端点（API Key 鉴权；核心工具 + 插件工具聚合）
	mcpAuth := func(c *gin.Context) {
		key := mcp.ValidateKey(c.Request)
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 API Key（X-Api-Key 或 Authorization: Bearer）"})
			c.Abort()
			return
		}
		var keys []model.ApiKey
		s.db.Find(&keys)
		for _, k := range keys {
			if pkg.SHA256Hex(key) == k.Hash {
				now := pkg.Now()
				s.db.Model(&k).Update("last_used_at", &now)
				c.Next()
				return
			}
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "API Key 无效"})
		c.Abort()
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
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 zip 文件"})
			return
		}
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		id, err := s.Plugins.InstallZip(data)
		resp := gin.H{"ok": err == nil, "id": id}
		if err != nil {
			resp["error"] = err.Error()
		}
		s.audit(c, "plugin.install", id)
		c.JSON(http.StatusOK, resp)
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
		defs := []struct {
			Key, Title, Type, Default, Help string
			Options                         []string
		}{}
		values := map[string]string{}
		for _, p := range s.Plugins.List() {
			if p.ID != id {
				continue
			}
			for _, sd := range p.Settings {
				defs = append(defs, struct {
					Key, Title, Type, Default, Help string
					Options                         []string
				}{sd.Key, sd.Title, sd.Type, sd.Default, sd.Help, sd.Options})
				var rec model.PluginSetting
				if err := s.db.Where("plugin_id = ? AND key = ?", id, sd.Key).First(&rec).Error; err == nil {
					values[sd.Key] = rec.Value
				} else if sd.Default != "" {
					values[sd.Key] = sd.Default
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"settings": defs, "values": values})
	})
	g.PUT("/:id/settings", func(c *gin.Context) {
		id := c.Param("id")
		var req map[string]string
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		for k, v := range req {
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
	})

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

var _ = os.Getenv
var _ = time.Now

// registerMCPTools 注册核心 MCP 工具（与插件工具聚合在同一注册表，经唯一 /mcp 对外暴露）。
func (s *Server) registerMCPTools() {
	jstr := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	reg := func(name, desc string, schema map[string]any, h func(args map[string]any) (string, error)) {
		_ = s.MCPReg.Register(&mcp.Tool{Name: name, Description: desc, InputSchema: schema, Handler: h, Owner: "core"})
	}
	reg("panel_info", "获取面板信息（版本/站点数/容器数）",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(args map[string]any) (string, error) {
			var siteCnt int64
			s.db.Model(&model.Site{}).Count(&siteCnt)
			containers, _ := s.Docker.ContainersList()
			return jstr(map[string]any{"version": update.CurrentVersion(), "sites": siteCnt, "containers": len(containers)}), nil
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
	reg("create_proxy_site", "创建反向代理站点（写入 nginx conf 并启用）",
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
	reg("toggle_site", "切换站点状态（enable|disable|maintenance）",
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
	reg("container_action", "容器操作（start|stop|restart|remove）",
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
	reg("run_cert_task", "立即执行证书任务（申请+部署）",
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
	reg("compose_action", "compose 项目操作（up|down|restart）",
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
}
