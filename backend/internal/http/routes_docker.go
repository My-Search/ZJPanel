package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerDockerRoutes Docker 管理 REST 路由（日志/拉取/exec 走专用 WS，见 ws.go）。
func (s *Server) registerDockerRoutes() {
	g := s.r.Group("/api/docker", s.auth.Require())
	g.GET("/info", func(c *gin.Context) {
		info, err := s.Docker.DockerInfo()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	})
	g.GET("/containers", func(c *gin.Context) {
		list, err := s.Docker.ContainersList()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	g.GET("/containers/stats", func(c *gin.Context) {
		stats, err := s.Docker.ContainersStats()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, stats)
	})
	g.POST("/containers/:id/action", func(c *gin.Context) {
		var req struct{ Action string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := s.Docker.ContainerAction(c.Param("id"), req.Action)
		resp := gin.H{"ok": err == nil}
		if out != "" {
			resp["output"] = out
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		s.audit(c, "docker.container."+req.Action, c.Param("id"))
		c.JSON(http.StatusOK, resp)
	})
	g.GET("/images", func(c *gin.Context) {
		list, err := s.Docker.ImagesList()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	g.DELETE("/images/*ref", func(c *gin.Context) {
		_, err := s.Docker.ImageRemove(c.Param("ref"), true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "docker.image.remove", c.Param("ref"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.GET("/networks", func(c *gin.Context) {
		list, err := s.Docker.NetworksList()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
	g.GET("/volumes", func(c *gin.Context) {
		list, err := s.Docker.VolumesList()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, list)
	})
}
