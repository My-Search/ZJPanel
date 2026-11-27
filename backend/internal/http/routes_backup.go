package httpapi

import (
	"fmt"
	"html"
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

	// ---- 云盘 OAuth（Google Drive / OneDrive）----

	// 回调地址：必须原样配置到 Google / Azure 的 OAuth 应用里。
	g.GET("/remote/redirect-uri", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"redirectUri": oauthRedirectURI(c)})
	})

	// 发起授权：返回授权页地址，由前端打开新窗口。
	g.POST("/oauth/start", func(c *gin.Context) {
		var req struct {
			Kind        string `json:"kind"`
			Return      string `json:"return"`
			RedirectURI string `json:"redirectUri"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Kind == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		redirect := req.RedirectURI
		if redirect == "" {
			redirect = oauthRedirectURI(c)
		}
		authURL, err := s.Backup.OAuthAuthorizeURL(req.Kind, redirect, req.Return)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.oauth.start", req.Kind)
		// ok 字段保持回传：老前端按 r.ok && r.url 判断，缺 ok 会被当成发起失败。
		c.JSON(http.StatusOK, gin.H{"ok": true, "url": authURL, "redirectUri": redirect})
	})

	// 授权回调：Google / Microsoft 会把浏览器重定向到这里（顶层导航带 Cookie，登录态可用）。
	// 校验一次性 state 后换取令牌，成功落库刷新令牌与账号。
	g.GET("/oauth/callback", func(c *gin.Context) {
		if e := c.Query("error"); e != "" {
			desc := c.Query("error_description")
			if desc == "" {
				desc = e
			}
			renderOAuthResult(c, false, "授权未完成", desc, "/")
			return
		}
		st, ok := backup.TakeOAuthState(c.Query("state"))
		if !ok {
			renderOAuthResult(c, false, "授权校验失败", "链接已过期或 state 不匹配，请回到设置页重新发起授权", "/")
			return
		}
		redirect := st.RedirectURI
		if redirect == "" {
			redirect = oauthRedirectURI(c)
		}
		account, err := s.Backup.CompleteOAuth(st.Kind, c.Query("code"), redirect)
		if err != nil {
			renderOAuthResult(c, false, "授权失败", err.Error(), st.ReturnPath)
			return
		}
		s.audit(c, "backup.oauth.callback", st.Kind)
		detail := "云盘已连接"
		if account != "" {
			detail = "已授权账号 " + account
		}
		renderOAuthResult(c, true, "授权成功", detail, st.ReturnPath)
	})

	// 撤销授权：清除刷新令牌，保留客户端凭据便于重新授权。
	g.POST("/oauth/clear", func(c *gin.Context) {
		if err := s.Backup.ClearOAuth(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "backup.oauth.clear", "")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// oauthRedirectURI 计算对外回调地址：优先代理透传的协议与主机，保证与用户浏览器地址一致。
func oauthRedirectURI(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := c.Request.Host
	if h := c.GetHeader("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host + "/api/backup/oauth/callback"
}

// renderOAuthResult 授权结果页：尝试自动关闭标签页，否则给出返回面板的链接。
func renderOAuthResult(c *gin.Context, ok bool, title, detail, returnPath string) {
	if returnPath == "" || !strings.HasPrefix(returnPath, "/") {
		returnPath = "/"
	}
	color := "#10b981"
	if !ok {
		color = "#ef4444"
	}
	page := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0b1220;
font-family:system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;color:#e2e8f0}
.card{width:min(420px,calc(100%% - 32px));padding:32px 28px;border-radius:14px;background:#151c2c;border:1px solid rgba(148,163,184,.18);
box-shadow:0 20px 50px rgba(2,6,23,.5);text-align:center}
.dot{width:44px;height:44px;margin:0 auto 16px;border-radius:50%%;display:grid;place-items:center;font-size:22px;color:#fff;background:%s}
h1{margin:0 0 8px;font-size:19px;font-weight:600}
p{margin:0;font-size:13px;line-height:1.7;color:#94a3b8;word-break:break-all}
a{display:inline-block;margin-top:18px;font-size:13px;color:#93c5fd;text-decoration:none}
</style></head><body><div class="card">
<div class="dot">%s</div><h1>%s</h1><p>%s</p>
<a href="%s">返回面板</a>
</div><script>setTimeout(function(){try{window.close()}catch(e){}},900)</script></body></html>`,
		html.EscapeString(title), color, map[bool]string{true: "✓", false: "!"}[ok],
		html.EscapeString(title), html.EscapeString(detail), html.EscapeString(returnPath))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}
