package httpapi

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"sync"
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
				longCache := strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "map/")
				if longCache {
					// 带 hash 的静态资源（vite 产物）与地图数据可长期缓存：
					// 内容变更时文件名/hash 必变，长缓存安全。地图此前每次访问都重下 ~1MB，
					// 手机网络下「地图迟迟不出来」的根因之一。
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
					// 大体积文本资源（JS/CSS/world.json）懒 gzip：内容随二进制固定，
					// 每文件只压一次并缓存；world.json ~1MB 压后 ~200KB，首访加载显著变快。
					if acceptsGzip(c.Request.Header.Get("Accept-Encoding")) {
						if gz, ok := gzipCached(sub, name); ok {
							c.Header("Content-Encoding", "gzip")
							c.Header("Vary", "Accept-Encoding")
							// 传原始文件名让 ServeContent 给出正确的 Content-Type
							http.ServeContent(c.Writer, c.Request, name, time.Now(), bytes.NewReader(gz))
							return
						}
					}
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

// gzCache 嵌入式静态资源的 gzip 结果缓存（name → 压缩字节；nil = 压不小或失败，回退明文）。
var gzCache sync.Map

// gzipCached 返回 name 的 gzip 压缩内容；首次请求时压缩一次，之后直接命中缓存。
func gzipCached(sub fs.FS, name string) ([]byte, bool) {
	if v, ok := gzCache.Load(name); ok {
		b, _ := v.([]byte)
		return b, len(b) > 0
	}
	b, err := fs.ReadFile(sub, name)
	if err != nil {
		gzCache.Store(name, []byte(nil))
		return nil, false
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil || w.Close() != nil {
		gzCache.Store(name, []byte(nil))
		return nil, false
	}
	out := buf.Bytes()
	if len(out) >= len(b) { // 已压缩内容（图片/字体等）压不小，回退明文
		gzCache.Store(name, []byte(nil))
		return nil, false
	}
	gzCache.Store(name, out)
	return out, true
}

// acceptsGzip 宽松判断客户端是否接受 gzip（主流浏览器与 HTTP 客户端均会携带）。
func acceptsGzip(header string) bool {
	return strings.Contains(header, "gzip")
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
