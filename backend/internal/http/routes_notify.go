package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerNotifyRoutes 统一通知：站内信列表/已读 + 通知设置（SMTP / 类型订阅）+ 测试发送。
func (s *Server) registerNotifyRoutes() {
	g := s.r.Group("/api/notify", s.auth.Require())

	// 站内信列表（新→旧，Limit 模式照审计日志惯例）
	g.GET("/list", func(c *gin.Context) {
		limit := intParam(c.DefaultQuery("limit", "50"))
		unread := c.Query("unread") == "1"
		c.JSON(http.StatusOK, gin.H{"list": s.Notify.List(limit, unread), "unread": s.Notify.Unread()})
	})

	g.GET("/unread", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"unread": s.Notify.Unread()})
	})

	// 标记已读：{ids:[..]} 或 {all:true}
	g.POST("/read", func(c *gin.Context) {
		var req struct {
			IDs []uint `json:"ids"`
			All bool   `json:"all"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		s.Notify.MarkRead(req.IDs, req.All)
		c.JSON(http.StatusOK, gin.H{"ok": true, "unread": s.Notify.Unread()})
	})

	// 通知设置（SMTP + 收件邮箱 + 类型订阅）
	g.GET("/settings", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Notify.Settings())
	})

	g.PUT("/settings", func(c *gin.Context) {
		var req struct {
			SMTPHost      string          `json:"smtpHost"`
			SMTPPort      int             `json:"smtpPort"`
			SMTPUser      string          `json:"smtpUser"`
			SMTPPass      string          `json:"smtpPass"`      // 留空沿用旧密码
			SMTPFrom      string          `json:"smtpFrom"`      // 发件邮箱（留空回落 SMTP 用户）
			SMTPFromAlias string          `json:"smtpFromAlias"` // 发件人别名（留空默认 ZJPanel）
			Rcpt          string          `json:"rcpt"`
			Subs          map[string]bool `json:"subs"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if err := s.Notify.SaveSettings(req.SMTPHost, req.SMTPPort, req.SMTPUser, req.SMTPPass,
			req.SMTPFrom, req.SMTPFromAlias, req.Rcpt, req.Subs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "notify.settings", "更新通知设置")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 发送测试邮件（验证 SMTP 配置）
	g.POST("/test", func(c *gin.Context) {
		if err := s.Notify.SendTest(); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
