package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

// ===================== 终端常用命令 CRUD =====================

func (s *Server) registerQuickCmdRoutes() {
	g := s.r.Group("/api/quickcmds", s.auth.Require())
	g.GET("", func(c *gin.Context) {
		var list []model.QuickCmd
		s.db.Order("usage DESC, id ASC").Find(&list)
		c.JSON(http.StatusOK, list)
	})
	g.POST("", func(c *gin.Context) {
		var req struct {
			Name    string `json:"name"`
			Command string `json:"command"`
			Color   string `json:"color"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Command == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "command 必填"})
			return
		}
		if len(req.Name) > 128 {
			req.Name = req.Name[:128]
		}
		qc := model.QuickCmd{Name: req.Name, Command: req.Command, Color: req.Color}
		if err := s.db.Create(&qc).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "id": qc.ID})
	})
	g.PUT("/cmds/:id", func(c *gin.Context) {
		var req struct {
			Name    string `json:"name"`
			Command string `json:"command"`
			Color   string `json:"color"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Command == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "command 必填"})
			return
		}
		qc := model.QuickCmd{ID: idParam(c)}
		if err := s.db.First(&qc, qc.ID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "命令不存在"})
			return
		}
		qc.Name, qc.Command, qc.Color = req.Name, req.Command, req.Color
		if err := s.db.Save(&qc).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.DELETE("/cmds/:id", func(c *gin.Context) {
		s.db.Delete(&model.QuickCmd{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/cmds/:id/use", func(c *gin.Context) {
		res := s.db.Model(&model.QuickCmd{}).Where("id = ?", idParam(c)).
			Update("usage", gorm.Expr("usage + 1"))
		if res.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": res.Error.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
