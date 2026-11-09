package httpapi

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// RegisterStatic 挂载前端 SPA（go:embed）；非 /api /mcp 路径全部回退 index.html。
func RegisterStatic(r *gin.Engine) {
	sub, _ := fs.Sub(distFS, "dist")
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name == "" || strings.HasSuffix(name, "/") {
			name = "index.html"
		}
		// 精确文件存在 → 直接输出（避免 http.FileServer 对 index.html 的 301 规范化重定向）
		if b, err := fs.ReadFile(sub, name); err == nil {
			c.Header("Cache-Control", "no-cache")
			http.ServeContent(c.Writer, c.Request, name, time.Now(), bytes.NewReader(b))
			return
		}
		// SPA 路由回退
		if b, err := fs.ReadFile(sub, "index.html"); err == nil {
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", b)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
}
