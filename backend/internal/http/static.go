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
// 配置了「安全入口」后，仅入口前缀下的页面导航返回 index.html，其余路径统一 404，
// 使域名根 / 与未知路径不暴露登录页；静态资源（assets/map/favicon 等）在文件命中分支直接输出，不受影响。
func (s *Server) RegisterStatic(r *gin.Engine) {
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
		// 非 index.html 的真实静态资源（assets/map/favicon 等）命中即直接输出，不受入口限制；
		// index.html 是 SPA 壳（根路径与前端路由回退都落到它），必须走下方入口校验与 base 注入，不能在此提前返回。
		if name != "index.html" {
			if b, err := fs.ReadFile(sub, name); err == nil {
				// 带 hash 的静态资源（vite 产物）与地图数据可长期缓存：
				// 内容变更时文件名/hash 必变，长缓存安全。地图此前每次访问都重下 ~1MB，
				// 手机网络下「地图迟迟不出来」的根因之一。
				if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "map/") {
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					c.Header("Cache-Control", "no-cache")
				}
				http.ServeContent(c.Writer, c.Request, name, time.Now(), bytes.NewReader(b))
				return
			}
		}

		// SPA 页面导航回退：安全入口校验。
		entry := s.EntryPath()
		if entry != "" && !underEntry(p, entry) {
			// 不返回 index.html、不重定向，统一 404，避免泄露入口是否存在。
			c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("404 page not found"))
			return
		}
		if b, err := fs.ReadFile(sub, "index.html"); err == nil {
			if entry != "" {
				b = injectEntryBase(b, entry)
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", b)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
}

// underEntry 判断请求路径是否位于安全入口前缀下（入口本身或其子路径）。
func underEntry(p, entry string) bool {
	return p == entry || strings.HasPrefix(p, entry+"/")
}

// injectEntryBase 在 index.html 的 <head> 后注入运行时 base，供前端 router/401 跳转使用。
// entry 已在保存时严格校验为 [A-Za-z0-9_/-]，无注入风险。
func injectEntryBase(html []byte, entry string) []byte {
	script := []byte(`<script>window.__ZJPANEL_BASE__=` + jsString(entry) + `;</script>`)
	idx := bytes.Index(html, []byte("<head>"))
	if idx < 0 {
		return html
	}
	insertAt := idx + len("<head>")
	out := make([]byte, 0, len(html)+len(script))
	out = append(out, html[:insertAt]...)
	out = append(out, script...)
	out = append(out, html[insertAt:]...)
	return out
}

// jsString 生成带引号的 JS 字符串字面量（仅含安全字符，仍做转义兜底）。
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
