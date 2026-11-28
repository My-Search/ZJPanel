package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// ===================== 面板本机文件浏览（路径选择器） =====================

// registerFsRoutes 通用本机目录浏览 API，供前端文件/路径选择组件使用。
// 只读列目录与文件元信息，不读取文件内容；登录保护，与终端/SFTP 同一威胁模型。
func (s *Server) registerFsRoutes() {
	g := s.r.Group("/api/fs", s.auth.Require())
	g.GET("/browse", func(c *gin.Context) {
		p := strings.TrimSpace(c.Query("path"))
		if p == "" {
			p = "/"
		}
		p = filepath.Clean(p)
		dirEntries, err := os.ReadDir(p)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取目录: " + err.Error()})
			return
		}
		type entry struct {
			Name    string `json:"name"`
			IsDir   bool   `json:"isDir"`
			Size    int64  `json:"size"`
			ModTime string `json:"modTime"`
		}
		entries := make([]entry, 0, len(dirEntries))
		for _, de := range dirEntries {
			info, err := de.Info()
			if err != nil {
				continue // 竞态删除等场景直接跳过
			}
			entries = append(entries, entry{
				Name:    de.Name(),
				IsDir:   de.IsDir(),
				Size:    info.Size(),
				ModTime: info.ModTime().Format("2006-01-02 15:04"),
			})
		}
		// 目录在前，按名称排序
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		c.JSON(http.StatusOK, gin.H{"path": p, "entries": entries})
	})
}
