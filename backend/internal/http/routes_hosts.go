package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/agent"
	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/terminal"
)

// hostListStatus 让列表里的 agentStatus 与 online 布尔值保持同一语义：
// 连接已断时，即使 DB 还留着上一次 online，也必须返回 offline，否则前端标题
// 会显示「在线」、防御页却按 online=false 禁用全部检查。
func hostListStatus(local, online bool, stored string) string {
	switch {
	case local:
		return "local"
	case online:
		return "online"
	case stored == "online":
		return "offline"
	case stored == "":
		return "none"
	default:
		return stored
	}
}

// registerHostRoutes 主机（终端 + Agent）与 Agent 侧端点。
// 一条主机记录同时服务「SSH 终端」和「受管主机」，不再有第二套清单。
func (s *Server) registerHostRoutes() {
	g := s.r.Group("/api/hosts", s.auth.Require())

	list := func(c *gin.Context) {
		var hosts []model.SshHost
		s.db.Order("local desc, id").Find(&hosts)
		items := make([]map[string]any, 0, len(hosts))
		for _, h := range hosts {
			online := h.Local || s.Agent.Online(h.ID)
			status := hostListStatus(h.Local, online, h.AgentStatus)
			items = append(items, map[string]any{
				"id": h.ID, "name": h.Name, "host": h.Host, "port": h.Port,
				"username": h.Username, "authType": h.AuthType, "local": h.Local,
				"group": h.Group, "tags": h.Tags, "claimed": h.Claimed,
				"online": online,
				"agentStatus": status, "agentVersion": h.AgentVersion, "agentProto": h.AgentProto,
				"osArch": h.OSArch, "remoteIp": h.RemoteIP, "lastSeen": h.AgentLastSeen,
				"outdated":     h.AgentStatus != "none" && h.AgentStatus != "local" && h.AgentVersion != "" && h.AgentVersion != buildinfo.Version,
				"panelVersion": buildinfo.Version,
			})
		}
		c.JSON(http.StatusOK, items)
	}
	g.GET("", list)

	// 创建/更新主机。install=true 时立即通过 SSH 注入 Agent。
	g.POST("", func(c *gin.Context) {
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
			Group      string `json:"group"`
			Tags       string `json:"tags"`
			Install    bool   `json:"install"`
			PanelURL   string `json:"panelUrl"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Host == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "host 必填"})
			return
		}
		if req.Port <= 0 {
			req.Port = 22
		}
		if req.AuthType != "password" && req.AuthType != "key" {
			req.AuthType = "password"
		}
		h := model.SshHost{ID: req.ID}
		if req.ID > 0 {
			if err := s.db.First(&h, req.ID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "主机不存在"})
				return
			}
		}
		if h.Local {
			// 内置本机只允许改名字/分组，不参与 SSH 凭据逻辑
			h.Name, h.Group, h.Tags = req.Name, req.Group, req.Tags
			if err := s.db.Save(&h).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "id": h.ID})
			return
		}
		h.Name, h.Host, h.Port, h.Username, h.AuthType = req.Name, req.Host, req.Port, req.Username, req.AuthType
		h.Group, h.Tags = req.Group, req.Tags
		if h.Name == "" {
			h.Name = req.Host
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
		if h.AgentStatus == "" {
			h.AgentStatus = "none"
		}
		if err := s.db.Save(&h).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "host.save", fmt.Sprintf("%s(%s)", h.Name, h.Host))

		resp := gin.H{"ok": true, "id": h.ID}
		if req.Install {
			out, err := s.installAgent(h.ID, orPanelURL(c, req.PanelURL))
			resp["output"] = out
			if err != nil {
				resp["ok"] = false
				resp["error"] = err.Error()
			}
		}
		c.JSON(http.StatusOK, resp)
	})

	g.DELETE("/:id", func(c *gin.Context) {
		id := idParam(c)
		var h model.SshHost
		if err := s.db.First(&h, id).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "主机不存在"})
			return
		}
		if h.Local {
			c.JSON(http.StatusBadRequest, gin.H{"error": "内置本机不可删除"})
			return
		}
		if s.Agent.Online(id) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Agent 仍在线：请先卸载 Agent，确认后再删除记录"})
			return
		}
		if err := s.db.Delete(&model.SshHost{}, id).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "host.delete", fmt.Sprintf("%s(%s)", h.Name, h.Host))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/:id/test", func(c *gin.Context) {
		client, err := terminal.DialHostByID(s.db, s.box, idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		_ = client.Close()
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 认领：命令安装的主机回连后，补名字并转正。
	g.POST("/:id/claim", func(c *gin.Context) {
		var req struct{ Name string }
		_ = c.ShouldBindJSON(&req)
		if err := s.Agent.Claim(idParam(c), req.Name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "host.claim", req.Name)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 通过 SSH 注入 Agent（幂等：重复执行只会校正配置并重启）。
	g.POST("/:id/agent/install", func(c *gin.Context) {
		var req struct{ PanelUrl string }
		_ = c.ShouldBindJSON(&req)
		out, err := s.installAgent(idParam(c), orPanelURL(c, req.PanelUrl))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "output": out, "error": err.Error()})
			return
		}
		s.audit(c, "host.agent.install", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": out})
	})

	g.POST("/:id/agent/uninstall", func(c *gin.Context) {
		id := idParam(c)
		res, err := s.Agent.Exec(id, "uninstall", nil, 20*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		// Agent 先回 ack 再自删：等连接真正断开才算卸载成功，否则不删记录、不标已卸载。
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && s.Agent.Online(id) {
			time.Sleep(500 * time.Millisecond)
		}
		if s.Agent.Online(id) {
			c.JSON(http.StatusOK, gin.H{
				"ok": true, "output": res.Output, "confirmed": false,
				"warning": "卸载指令已下发，但 Agent 仍在线：请查看目标机 /tmp/zjagent-uninstall.log，或在该机执行 zjagent uninstall --purge 后再删除记录",
			})
			return
		}
		s.Agent.MarkUninstalled(id)
		s.audit(c, "host.agent.uninstall", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output, "confirmed": true})
	})

	g.POST("/:id/agent/upgrade", func(c *gin.Context) {
		var h model.SshHost
		if err := s.db.First(&h, idParam(c)).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "主机不存在"})
			return
		}
		arch := "amd64"
		if strings.Contains(h.OSArch, "arm64") || strings.Contains(h.OSArch, "aarch64") {
			arch = "arm64"
		}
		bin := agent.BinaryPath(s.cfg, arch)
		if bin == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "面板未内置该架构的 Agent 二进制"})
			return
		}
		res, err := s.Agent.Exec(idParam(c), "upgrade", map[string]any{
			"path": "/api/agent/bin/" + arch, "sha256": agent.FileSHA256(bin), "version": buildinfo.Version,
		}, 60*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		s.audit(c, "host.agent.upgrade", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
	})

	g.GET("/:id/metrics", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"metrics": s.Agent.LatestMetrics(idParam(c)), "online": s.Agent.Online(idParam(c))})
	})

	g.GET("/:id/events", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		c.JSON(http.StatusOK, s.Agent.RecentEvents(idParam(c), limit))
	})

	g.GET("/:id/commands", func(c *gin.Context) {
		var cmds []model.AgentCommand
		s.db.Where("host_id = ?", idParam(c)).Order("created_at desc").Limit(30).Find(&cmds)
		c.JSON(http.StatusOK, cmds)
	})

	// 主机防御检查：只读基线审计（不改系统配置）。
	// ssh=SSH 基线 accounts=账号 persistence=持久化 exposure=暴露面 webshell=后门特征
	g.POST("/:id/check/:kind", func(c *gin.Context) {
		kind := c.Param("kind")
		cmds := map[string]string{
			"ssh":         "ssh_audit",
			"accounts":    "accounts_audit",
			"persistence": "persistence_audit",
			"exposure":    "exposure_audit",
			"webshell":    "webshell_audit",
		}
		cmd, ok := cmds[kind]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知检查类型: " + kind})
			return
		}
		res, err := s.Agent.Exec(idParam(c), cmd, nil, 45*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		var payload any
		_ = json.Unmarshal([]byte(res.Output), &payload)
		s.audit(c, "host.check."+kind, c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"kind": kind, "result": payload})
	})

	// 主机防火墙状态与封禁列表（nftables 独立表 inet zjpanel）。
	g.GET("/:id/firewall", func(c *gin.Context) {
		res, err := s.Agent.Exec(idParam(c), "firewall_status", nil, 30*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		var payload any
		_ = json.Unmarshal([]byte(res.Output), &payload)
		c.JSON(http.StatusOK, payload)
	})

	// 下发封禁：仅 IPv4，带 TTL，到期自动解封。
	g.POST("/:id/bans", func(c *gin.Context) {
		var req struct {
			IP      string `json:"ip"`
			Seconds int    `json:"seconds"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		res, err := s.Agent.Exec(idParam(c), "ban_ip", map[string]any{
			"ip": req.IP, "seconds": req.Seconds,
		}, 30*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		s.audit(c, "host.ban."+req.IP, c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
	})

	g.DELETE("/:id/bans/:ip", func(c *gin.Context) {
		res, err := s.Agent.Exec(idParam(c), "unban_ip", map[string]any{
			"ip": c.Param("ip"),
		}, 30*time.Second)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !res.OK {
			c.JSON(http.StatusBadRequest, gin.H{"error": res.Output})
			return
		}
		s.audit(c, "host.unban."+c.Param("ip"), c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
	})

	// ---- Agent 侧端点（token 鉴权，不走登录态） ----

	s.r.GET("/api/agent/time", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ts": time.Now().Unix()})
	})

	// 安装脚本：由 token 定位主机；脚本可被用户审阅后执行。
	s.r.GET("/api/agent/install.sh", func(c *gin.Context) {
		tok := c.Query("token")
		hostID, _, ok := s.Agent.ResolveToken(tok)
		if !ok {
			c.String(http.StatusForbidden, "invalid token")
			return
		}
		script := agent.InstallScript(agent.ScriptParams{
			PanelURL: orPanelURL(c, c.Query("panel")), Token: tok, Version: buildinfo.Version,
		})
		c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
		c.String(http.StatusOK, script)
		_ = hostID
	})

	// Agent 二进制：token 鉴权；SHA 由面板即时计算，升级指令里带过去校验。
	s.r.GET("/api/agent/bin/:arch", func(c *gin.Context) {
		tok := c.Query("token")
		if _, _, ok := s.Agent.ResolveToken(tok); !ok {
			c.String(http.StatusForbidden, "invalid token")
			return
		}
		arch := c.Param("arch")
		if arch != "amd64" && arch != "arm64" {
			c.String(http.StatusBadRequest, "unsupported arch")
			return
		}
		p := agent.BinaryPath(s.cfg, arch)
		if p == "" {
			c.String(http.StatusServiceUnavailable, "该架构 Agent 未内置（开发模式请先构建 cmd/zjagent 并放到 data/agent/）")
			return
		}
		c.Header("Content-Type", "application/octet-stream")
		c.Header("X-Agent-SHA256", agent.FileSHA256(p))
		c.File(p)
	})

	// 反向连接：Agent 出站连面板。Origin 校验不适用（非浏览器），凭据在首帧 hello 里，
	// 不走 query（避免 token 落进 nginx/面板访问日志）。
	s.r.GET("/api/agent/ws", func(c *gin.Context) {
		// 兼容带 query token 的旧式调用（如脚本自检）；否则等待首帧 hello 认证。
		var hostID uint
		var bootstrap, haveTok bool
		if tok := c.Query("token"); tok != "" {
			var ok bool
			hostID, bootstrap, ok = s.Agent.ResolveToken(tok)
			if !ok {
				c.String(http.StatusForbidden, "invalid token")
				return
			}
			haveTok = true
		}
		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		if haveTok {
			s.Agent.Serve(hostID, conn, bootstrap)
			return
		}
		s.Agent.ServeAuth(conn)
	})

	// 命令安装：先建一条「待认领」记录，再给一行命令。
	s.r.POST("/api/agent/register", s.auth.Require(), func(c *gin.Context) {
		var req struct {
			Name     string `json:"name"`
			Group    string `json:"group"`
			PanelURL string `json:"panelUrl"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if req.Name == "" {
			req.Name = "待认领主机"
		}
		h := model.SshHost{Name: req.Name, AuthType: "none", Group: req.Group, AgentStatus: "pending", Claimed: false}
		if err := s.db.Create(&h).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		tok := s.Agent.IssueBootstrap(h.ID)
		base := orPanelURL(c, req.PanelURL)
		cmd := fmt.Sprintf(`curl -fsSL "%s/api/agent/install.sh?token=%s" | sh`, base, tok)
		s.audit(c, "host.agent.register", fmt.Sprintf("%d", h.ID))
		c.JSON(http.StatusOK, gin.H{"ok": true, "id": h.ID, "token": tok, "command": cmd, "scriptUrl": base + "/api/agent/install.sh?token=" + tok})
	})
}

// installAgent 通过 SSH 注入 Agent：探测架构 → 生成脚本 → 执行 → 输出回显。
func (s *Server) installAgent(hostID uint, panelURL string) (string, error) {
	var h model.SshHost
	if err := s.db.First(&h, hostID).Error; err != nil {
		return "", fmt.Errorf("主机不存在")
	}
	if h.Local {
		return "", fmt.Errorf("内置本机不需要安装 Agent")
	}
	client, err := terminal.DialHostByID(s.db, s.box, hostID)
	if err != nil {
		return "", err
	}
	defer client.Close()

	// 探测架构：脚本虽能自检，但 SHA 校验需要面板先知道下哪个包
	archOut, err := agent.RunScript(client, `uname -m`)
	if err != nil {
		return archOut, err
	}
	arch := "amd64"
	if strings.Contains(archOut, "arm64") || strings.Contains(archOut, "aarch64") {
		arch = "arm64"
	}
	bin := agent.BinaryPath(s.cfg, arch)
	if bin == "" {
		return "", fmt.Errorf("面板未内置 %s 架构的 Agent 二进制（请用正式镜像，或先构建 cmd/zjagent）", arch)
	}
	tok := s.Agent.IssueBootstrap(hostID)
	script := agent.InstallScript(agent.ScriptParams{
		PanelURL: panelURL, Token: tok, Version: buildinfo.Version, SHA256: agent.FileSHA256(bin),
	})
	out, err := agent.RunScript(client, script)
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).
		Updates(map[string]any{"agent_status": "pending", "claimed": true})
	return out, err
}

// orPanelURL 推断面板地址：显式参数 > 请求来源。Agent 需要从目标机访问到这个地址。
func orPanelURL(c *gin.Context, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimRight(strings.TrimSpace(explicit), "/")
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
