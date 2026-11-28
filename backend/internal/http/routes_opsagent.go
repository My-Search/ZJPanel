package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/opsagent"
)

// registerOpsAgentRoutes AI 运维智能体：会话 CRUD / 消息与确认 / 模型与 Agent 配置 / 记忆管理。
// 会话的执行在后端 runner（页面刷新不中断）；前端经 WS 主题 opsagent 收消息与状态帧。
func (s *Server) registerOpsAgentRoutes() {
	g := s.r.Group("/api/opsagent", s.auth.Require())

	// ---- 会话 ----
	g.POST("/sessions", func(c *gin.Context) {
		var req struct {
			Message string `json:"message"`
			HostID  uint   `json:"hostId"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Message == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
			return
		}
		msg := req.Message
		title := msg
		if len(title) > 60 {
			title = title[:60]
		}
		var hostID *uint
		if req.HostID > 0 {
			hostID = &req.HostID
		}
		sess, err := s.OpsAgent.CreateSession(title, "manual", hostID, "user", msg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := s.OpsAgent.StartRunner(sess); err != nil {
			s.OpsAgent.MarkError(sess.ID, err.Error())
			c.JSON(http.StatusOK, gin.H{"ok": false, "session": sess, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "session": sess})
	})
	g.GET("/sessions", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.OpsAgent.ListSessions())
	})
	g.GET("/sessions/:id", func(c *gin.Context) {
		sess, err := s.OpsAgent.GetSession(idParam(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, sess)
	})
	g.GET("/sessions/:id/messages", func(c *gin.Context) {
		after, _ := strconv.Atoi(c.Query("afterId"))
		limit, _ := strconv.Atoi(c.Query("limit"))
		c.JSON(http.StatusOK, s.OpsAgent.Messages(idParam(c), uint(after), limit))
	})
	g.POST("/sessions/:id/messages", func(c *gin.Context) {
		var req struct {
			Message string `json:"message"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Message == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
			return
		}
		if err := s.OpsAgent.SendMessage(idParam(c), req.Message); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/sessions/:id/confirm", func(c *gin.Context) {
		var req struct {
			Approve bool `json:"approve"`
		}
		_ = c.ShouldBindJSON(&req)
		if err := s.OpsAgent.Confirm(idParam(c), req.Approve); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/sessions/:id/stop", func(c *gin.Context) {
		if err := s.OpsAgent.Stop(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.DELETE("/sessions/:id", func(c *gin.Context) {
		if err := s.OpsAgent.DeleteSession(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "opsagent.session.delete", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.GET("/running-count", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"count": s.OpsAgent.RunningCount()})
	})

	// ---- 配置（模型 + 防御模式 + 事件订阅） ----
	g.GET("/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"llm":    s.OpsAgent.LLMView(),
			"agent":  s.OpsAgent.AgentConfig(),
			"events": opsagent.EventCatalog,
		})
	})
	g.PUT("/config", func(c *gin.Context) {
		var req struct {
			LLM struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"` // 明文；空 = 沿用旧密文
				Model   string `json:"model"`
			} `json:"llm"`
			Agent opsagent.AgentConfig `json:"agent"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if req.LLM.BaseURL != "" || req.LLM.Model != "" || req.LLM.APIKey != "" {
			if err := s.OpsAgent.SaveLLM(req.LLM.BaseURL, req.LLM.APIKey, req.LLM.Model); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		// 局部保存：前端可能只传 llm（改模型）或只传 agent（改防御配置），未提供的一侧不动
		if req.Agent.DefenseMode != "" || req.Agent.EventEnabled || len(req.Agent.Events) > 0 || req.Agent.AllowSSHExec {
			if err := s.OpsAgent.SaveAgentConfig(req.Agent); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		s.audit(c, "opsagent.config.save",
			"mode="+req.Agent.DefenseMode+" eventEnabled="+strconv.FormatBool(req.Agent.EventEnabled)+
				" sshExec="+strconv.FormatBool(req.Agent.AllowSSHExec))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/config/test", func(c *gin.Context) {
		cfg, err := s.OpsAgent.LLMDetail()
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		if err := s.OpsAgent.TestLLM(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ---- 记忆管理 ----
	g.GET("/memories", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.OpsAgent.ListMemories(c.Query("query"), 0))
	})
	g.DELETE("/memories/:id", func(c *gin.Context) {
		if err := s.OpsAgent.DeleteMemory(idParam(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
