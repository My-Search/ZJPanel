package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/backup"
)

// registerBackupRoutes 备份与恢复：清单、生成、下载、删除、远程配置与上传/下载远程。
func (s *Server) registerBackupRoutes() {
	g := s.r.Group("/api/backup", s.auth.Require())

	// 可备份项清单（前端勾选渲染）
	g.GET("/items", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"items": backup.ItemMetas()})
	})

	// 本地备份列表
	g.GET("/list", func(c *gin.Context) {
		list, err := s.Backup.List()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"list": list})
	})

	// 生成备份包：{items:[...]}，空/全选即完整配置迁移
	g.POST("/create", func(c *gin.Context) {
		var req struct {
			Items []string `json:"items"`
		}
		_ = c.ShouldBindJSON(&req)
		rec, err := s.Backup.Build(req.Items)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.create", rec.Name)
		c.JSON(http.StatusOK, gin.H{"ok": true, "backup": rec})
	})

	// 下载本地备份包
	g.GET("/download", func(c *gin.Context) {
		name := c.Query("name")
		p, err := s.Backup.OpenPath(name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "备份包不存在"})
			return
		}
		c.FileAttachment(p, filepath.Base(p))
	})

	// 删除本地备份包
	g.DELETE("/:name", func(c *gin.Context) {
		name := c.Param("name")
		if err := s.Backup.Delete(name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.delete", name)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 恢复：上传备份包后按所选项目还原（本接口接收 multipart 文件）
	g.POST("/restore", func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请上传备份包"})
			return
		}
		items := strings.Split(c.PostForm("items"), ",")
		// 先落到临时文件再恢复，避免直接从 multipart 流写回系统目录。
		tmp, err := s.Backup.RestoreUpload(file)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		defer s.Backup.RemoveTemp(tmp)
		res, err := s.Backup.Restore(tmp, items)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.restore", file.Filename)
		c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
	})

	// 远程存储配置
	g.GET("/remote", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Backup.RemoteConfigView())
	})

	g.PUT("/remote", func(c *gin.Context) {
		var req backup.RemoteConfig
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if err := s.Backup.SaveRemote(req); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.remote", req.Kind)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 测试远程连通性（不保存）
	g.POST("/remote/test", func(c *gin.Context) {
		var req backup.RemoteConfig
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if err := s.Backup.TestRemote(req); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 上传本地备份到远程
	g.POST("/:name/upload-remote", func(c *gin.Context) {
		name := c.Param("name")
		if err := s.Backup.UploadToRemote(name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.upload-remote", name)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 从远程拉取到本地
	g.POST("/:name/download-remote", func(c *gin.Context) {
		name := c.Param("name")
		if err := s.Backup.DownloadFromRemote(name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
