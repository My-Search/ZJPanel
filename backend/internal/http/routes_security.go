package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/security"
)

// registerSecurityRoutes 安全中心 API：态势快照 / 事件流 / 策略 / 封禁处置 / 预演。
func (s *Server) registerSecurityRoutes() {
	g := s.r.Group("/api/security", s.auth.Require())

	g.GET("/overview", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Security.Latest())
	})

	g.GET("/events", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		c.JSON(http.StatusOK, s.Security.RecentEvents(limit))
	})

	g.GET("/policy", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Security.PolicyCopy())
	})

	g.PUT("/policy", func(c *gin.Context) {
		var p security.Policy
		if err := c.ShouldBindJSON(&p); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Security.SetPolicy(p); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "security.policy.save", fmt.Sprintf("enabled=%v observe=%v autoBan=%v cc=%v/%d",
			p.Enabled, p.Observe, p.AutoBan, p.CCEnabled, p.CCRate))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/block/ip", func(c *gin.Context) {
		var req struct {
			Value   string `json:"value"`
			Minutes int    `json:"minutes"`
			Reason  string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 IP/CIDR"})
			return
		}
		if err := s.Security.BlockIP(req.Value, req.Minutes, req.Reason, false); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "security.block.ip", fmt.Sprintf("%s minutes=%d reason=%s", req.Value, req.Minutes, req.Reason))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/unblock/ip", func(c *gin.Context) {
		var req struct {
			Value string `json:"value"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 IP/CIDR"})
			return
		}
		if err := s.Security.UnblockIP(req.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "security.unblock.ip", req.Value)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/block/country", func(c *gin.Context) {
		var req struct {
			Value   string `json:"value"`
			Minutes int    `json:"minutes"`
			Reason  string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少国家码"})
			return
		}
		if err := s.Security.BlockCountry(req.Value, req.Minutes, req.Reason, false); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "security.block.country", fmt.Sprintf("%s minutes=%d reason=%s",
			req.Value, req.Minutes, req.Reason))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/unblock/country", func(c *gin.Context) {
		var req struct {
			Value string `json:"value"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少国家码"})
			return
		}
		if err := s.Security.UnblockCountry(req.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "security.unblock.country", req.Value)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.POST("/dryrun", func(c *gin.Context) {
		var req struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		res, err := s.Security.DryRun(req.Kind, req.Value)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})
}
