package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zjpanel/backend/internal/cert"
	"zjpanel/backend/internal/compose"
	"zjpanel/backend/internal/docker"
	"zjpanel/backend/internal/logmap"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/metrics"
	"zjpanel/backend/internal/middleware"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/notify"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/plugin"
	"zjpanel/backend/internal/sftp"
	"zjpanel/backend/internal/site"
	"zjpanel/backend/internal/sitecheck"
	"zjpanel/backend/internal/terminal"
	"zjpanel/backend/internal/update"
	"zjpanel/backend/internal/ws"
)

// Server 面板 HTTP 服务：路由装配与各服务实例在此聚合。
type Server struct {
	cfg  *pkg.Config
	db   *gorm.DB
	auth *middleware.Auth
	hub  *ws.Hub
	box  *pkg.SecretBox
	r    *gin.Engine

	// 服务实例由 boot 装配后注入（RegisterRoutes 前必须全部就绪）
	Nginx   *nginx.Service
	Sites   *site.Service
	Check   *sitecheck.Service
	Compose *compose.Service
	Term    *terminal.Manager
	Sftp    *sftp.Manager
	Docker  *docker.Service
	Metrics *metrics.Service
	Logmap  *logmap.Service
	Cert    *cert.Service
	Updater *update.Service
	Plugins *plugin.Manager
	Notify  *notify.Service
	MCPReg  *mcp.Registry
	MCPSrv  *mcp.Server
}

func NewServer(cfg *pkg.Config, db *gorm.DB, auth *middleware.Auth, hub *ws.Hub, box *pkg.SecretBox) *Server {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{cfg: cfg, db: db, auth: auth, hub: hub, box: box, r: gin.New()}
	s.r.Use(gin.Logger(), gin.Recovery(), securityHeadersAndCSRF())
	// 仅信任显式配置的反代 CIDR：直连客户端伪造 X-Forwarded-For 无法影响
	// ClientIP()（限速键位与审计日志 IP 均依赖它），防止限速绕过与日志造假。
	if err := s.r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		slog.Warn("可信代理配置无效，回退为不信任任何代理", "err", err)
		_ = s.r.SetTrustedProxies(nil)
	}
	s.registerAuthRoutes()
	return s
}

// securityHeadersAndCSRF 注入基础安全响应头，并对携带 Origin 的状态变更请求执行同源校验
// （防御同 IP 不同端口或同父域兄弟子域利用 SameSite=Lax 发起的跨源表单/简单请求 CSRF）。
func securityHeadersAndCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")

		m := c.Request.Method
		if m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions {
			p := c.Request.URL.Path
			// 外部 webhook 回调与 API Key 鉴权的 /mcp 不依赖浏览器 Cookie，豁免 Origin 校验
			if !strings.HasPrefix(p, "/api/pub/") && !strings.HasPrefix(p, "/mcp") {
				if !isSameOrigin(c.Request) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "跨源请求被拒绝 (Origin mismatch)"})
					return
				}
			}
		}
		c.Next()
	}
}

// isSameOrigin 校验请求的 Origin 头（若存在）是否与当前 Host 同源；兼容本地 vite :5173 开发代理。
func isSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if isLoopbackHost(u.Hostname()) && isLoopbackHost(hostOnly(r.Host)) && u.Port() == "5173" {
		return true
	}
	return false
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func isLoopbackHost(h string) bool {
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1"
}

func (s *Server) Engine() *gin.Engine { return s.r }

// ---- 登录失败限速（每键每分钟 8 次，键位含真实 IP 与用户名两个维度）----

type failRecord struct {
	count int
	reset time.Time
}

var failMu sync.Mutex
var failMap = map[string]*failRecord{}
var initMu sync.Mutex

const (
	loginFailWindow = time.Minute
	loginFailMax    = 8
	maxFailRecords  = 10000
)

// loginAllow 查询 key 是否仍在限速窗口内放行；不计入失败，不消耗尝试次数。
// 顺带清理过期记录，防止 failMap 无限增长。
func loginAllow(key string) bool {
	failMu.Lock()
	defer failMu.Unlock()
	now := time.Now()
	for k, fr := range failMap {
		if now.After(fr.reset) {
			delete(failMap, k)
		}
	}
	fr := failMap[key]
	if fr == nil {
		return true
	}
	return fr.count < loginFailMax
}

// loginRecordFail 记录一次登录失败；超出窗口即重建记录。
func loginRecordFail(key string) {
	failMu.Lock()
	defer failMu.Unlock()
	now := time.Now()
	fr := failMap[key]
	if fr == nil || now.After(fr.reset) {
		if len(failMap) >= maxFailRecords {
			for k, v := range failMap {
				if now.After(v.reset) {
					delete(failMap, k)
				}
			}
			if len(failMap) >= maxFailRecords {
				return
			}
		}
		failMap[key] = &failRecord{count: 1, reset: now.Add(loginFailWindow)}
		return
	}
	fr.count++
}

func loginClearFail(ip, username string) {
	failMu.Lock()
	defer failMu.Unlock()
	delete(failMap, "ip:"+ip)
	delete(failMap, "ipu:"+ip+":"+strings.ToLower(username))
}

// loginRateLimit 按真实 IP 与 (IP, 用户名) 限速：拦截暴力破解，同时避免攻击者
// 跨 IP 高频故意输错管理员密码导致合法管理员自己的 IP 被全局锁死（DoS）。
func loginRateLimit(ip, username string) bool {
	return loginAllow("ip:"+ip) && loginAllow("ipu:"+ip+":"+strings.ToLower(username))
}

func (s *Server) registerAuthRoutes() {
	g := s.r.Group("/api/auth")
	// 状态查询
	g.GET("/status", func(c *gin.Context) {
		var cnt int64
		s.db.Model(&model.User{}).Count(&cnt)
		sess := s.auth.Current(c)
		c.JSON(http.StatusOK, gin.H{
			"initialized": cnt > 0,
			"loggedIn":    sess != nil,
			"username": func() any {
				if sess != nil {
					return sess.Username
				}
				return nil
			}(),
		})
	})
	// 首次初始化管理员（加锁防并发竞态重复初始化）
	g.POST("/init", func(c *gin.Context) {
		initMu.Lock()
		defer initMu.Unlock()
		var cnt int64
		s.db.Model(&model.User{}).Count(&cnt)
		if cnt > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "已初始化"})
			return
		}
		var req struct{ Username, Password string }
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Username) < 2 || len(req.Password) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "用户名≥2位，密码≥8位"})
			return
		}
		u := model.User{Username: req.Username, Password: pkg.BcryptHash(req.Password)}
		if err := s.db.Create(&u).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.auth.Login(c, u.ID, u.Username, u.Password)
		s.audit(c, "auth.init", "初始化管理员 "+u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 登录
	g.POST("/login", func(c *gin.Context) {
		var req struct{ Username, Password string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		ip := c.ClientIP()
		if !loginRateLimit(ip, req.Username) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "尝试过于频繁，请稍后再试"})
			return
		}
		var u model.User
		if err := s.db.Where("username = ?", req.Username).First(&u).Error; err != nil || !pkg.BcryptVerify(u.Password, req.Password) {
			loginRecordFail("ip:" + ip)
			loginRecordFail("ipu:" + ip + ":" + strings.ToLower(req.Username))
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
			return
		}
		loginClearFail(ip, req.Username)
		s.auth.Login(c, u.ID, u.Username, u.Password)
		s.audit(c, "auth.login", u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true, "username": u.Username})
	})
	g.POST("/logout", func(c *gin.Context) {
		s.auth.Logout(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 修改密码（需登录）：更新哈希后吊销该用户全部旧会话（旧凭据签发的
	// 会话即刻失效），并为当前设备重签新会话，避免改密码后自己被登出。
	protected := s.r.Group("/api", s.auth.Require())
	protected.POST("/auth/change-password", func(c *gin.Context) {
		var req struct{ Old, New string }
		if err := c.ShouldBindJSON(&req); err != nil || len(req.New) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "新密码≥8位"})
			return
		}
		sess := s.auth.Current(c)
		var u model.User
		if err := s.db.First(&u, sess.UserID).Error; err != nil || !pkg.BcryptVerify(u.Password, req.Old) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "原密码错误"})
			return
		}
		newHash := pkg.BcryptHash(req.New)
		if err := s.db.Model(&u).Update("password", newHash).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.auth.RevokeAllForUser(u.ID)
		s.auth.Login(c, u.ID, u.Username, newHash)
		s.audit(c, "auth.change-password", u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 修改用户名（需登录；需密码确认）
	protected.POST("/auth/change-username", func(c *gin.Context) {
		var req struct{ Username, Password string }
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Username) < 2 || len(req.Username) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "用户名需为2-64位"})
			return
		}
		sess := s.auth.Current(c)
		var u model.User
		if err := s.db.First(&u, sess.UserID).Error; err != nil || !pkg.BcryptVerify(u.Password, req.Password) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "密码错误"})
			return
		}
		var cnt int64
		s.db.Model(&model.User{}).Where("username = ? AND id <> ?", req.Username, u.ID).Count(&cnt)
		if cnt > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "用户名已存在"})
			return
		}
		old := u.Username
		if err := s.db.Model(&u).Update("username", req.Username).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.auth.UpdateUsername(c, req.Username)
		s.audit(c, "auth.change-username", old+" → "+req.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true, "username": req.Username})
	})
}

// audit 记录审计日志。
func (s *Server) audit(c *gin.Context, action, detail string) {
	s.db.Create(&model.AuditLog{Time: timeNow(), Action: action, Detail: detail, IP: c.ClientIP()})
}

func timeNow() time.Time { return pkg.Now() }
