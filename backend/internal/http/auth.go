package httpapi

import (
	"net/http"
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
	s.r.Use(gin.Logger(), gin.Recovery())
	s.registerAuthRoutes()
	return s
}

func (s *Server) Engine() *gin.Engine { return s.r }

// ---- 登录失败限速（每 IP 每分钟 8 次）----

type failRecord struct {
	count int
	reset time.Time
}

var failMu sync.Mutex
var failMap = map[string]*failRecord{}

func loginRateLimit(ip string) bool {
	failMu.Lock()
	defer failMu.Unlock()
	fr := failMap[ip]
	if fr == nil || time.Now().After(fr.reset) {
		failMap[ip] = &failRecord{count: 1, reset: time.Now().Add(time.Minute)}
		return true
	}
	fr.count++
	return fr.count <= 8
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
	// 首次初始化管理员
	g.POST("/init", func(c *gin.Context) {
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
		s.auth.Login(c, u.ID, u.Username)
		s.audit(c, "auth.init", "初始化管理员 "+u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 登录
	g.POST("/login", func(c *gin.Context) {
		if !loginRateLimit(c.ClientIP()) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "尝试过于频繁，请稍后再试"})
			return
		}
		var req struct{ Username, Password string }
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		var u model.User
		if err := s.db.Where("username = ?", req.Username).First(&u).Error; err != nil || !pkg.BcryptVerify(u.Password, req.Password) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
			return
		}
		s.auth.Login(c, u.ID, u.Username)
		s.audit(c, "auth.login", u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true, "username": u.Username})
	})
	g.POST("/logout", func(c *gin.Context) {
		s.auth.Logout(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 修改密码（需登录）
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
		s.db.Model(&u).Update("password", pkg.BcryptHash(req.New))
		s.audit(c, "auth.change-password", u.Username)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// audit 记录审计日志。
func (s *Server) audit(c *gin.Context, action, detail string) {
	s.db.Create(&model.AuditLog{Time: timeNow(), Action: action, Detail: detail, IP: c.ClientIP()})
}

func timeNow() time.Time { return pkg.Now() }
