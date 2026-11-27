package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/waf"
)

// registerWafRoutes WAF 规则引擎 API：配置 / 规则 / 命中日志 / 规则测试。
func (s *Server) registerWafRoutes() {
	if s.WAF == nil {
		return
	}
	g := s.r.Group("/api/waf", s.auth.Require())

	g.GET("/view", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"config": s.WAF.View(), "stats": s.WAF.Stat()})
	})

	g.PUT("/settings", func(c *gin.Context) {
		var st waf.Settings
		if err := c.ShouldBindJSON(&st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.WAF.SetSettings(st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "waf.settings.save", "enabled="+strconv.FormatBool(st.Enabled)+" mode="+st.BlockMode)
		c.JSON(http.StatusOK, gin.H{"ok": true, "config": s.WAF.View()})
	})

	g.PUT("/rule", func(c *gin.Context) {
		var req struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
			Action  string `json:"action"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少规则 ID"})
			return
		}
		if err := s.WAF.SetRule(req.ID, req.Enabled, req.Action); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "waf.rule.update", req.ID+" enabled="+strconv.FormatBool(req.Enabled)+" action="+req.Action)
		c.JSON(http.StatusOK, gin.H{"ok": true, "config": s.WAF.View()})
	})

	g.POST("/rule", func(c *gin.Context) {
		var req struct {
			Name     string   `json:"name"`
			Severity string   `json:"severity"`
			Action   string   `json:"action"`
			Scope    []string `json:"scope"`
			Pattern  string   `json:"pattern"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		r, err := s.WAF.AddRule(waf.Rule{
			Name: req.Name, Severity: req.Severity, Action: req.Action,
			Scope: req.Scope, Pattern: req.Pattern,
		})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "waf.rule.add", r.ID+" "+r.Name)
		c.JSON(http.StatusOK, gin.H{"ok": true, "rule": r, "config": s.WAF.View()})
	})

	g.DELETE("/rule/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := s.WAF.DeleteRule(id); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "waf.rule.delete", id)
		c.JSON(http.StatusOK, gin.H{"ok": true, "config": s.WAF.View()})
	})

	g.GET("/hits", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		c.JSON(http.StatusOK, s.WAF.Hits(waf.HitFilter{
			IP: c.Query("ip"), Category: c.Query("category"),
			RuleID: c.Query("ruleId"), Site: c.Query("site"), Limit: limit,
		}))
	})

	g.GET("/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.WAF.Stat())
	})

	// 规则测试：手工构造一条请求跑一遍规则，用于上线前验证/排障（不做封禁）。
	g.POST("/test", func(c *gin.Context) {
		var req struct {
			Method  string `json:"method"`
			Path    string `json:"path"`
			UA      string `json:"ua"`
			Referer string `json:"referer"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 path"})
			return
		}
		hits := s.WAF.Test(waf.Request{
			Method: req.Method, Path: req.Path, UA: req.UA,
			Referer: req.Referer, Site: "test",
		})
		c.JSON(http.StatusOK, gin.H{"hits": hits})
	})
}
