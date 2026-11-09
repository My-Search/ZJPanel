package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/pkg"
)

// Auth 基于 HttpOnly Cookie 的内存会话（单管理员面板足够）。
// 终端/WS 端点同样走 Cookie 鉴权；MCP 走独立 API Key，不经过这里。

type session struct {
	UserID   uint
	Username string
	Expires  time.Time
}

type Auth struct {
	mu       sync.Mutex
	sessions map[string]*session
}

const CookieName = "zjp_session"

func NewAuth() *Auth {
	return &Auth{sessions: map[string]*session{}}
}

func (a *Auth) Login(c *gin.Context, uid uint, username string) {
	token := pkg.RandomHex(32)
	a.mu.Lock()
	a.sessions[token] = &session{UserID: uid, Username: username, Expires: time.Now().Add(24 * time.Hour)}
	a.mu.Unlock()
	http.SetCookie(c.Writer, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 86400,
	})
}

func (a *Auth) Logout(c *gin.Context) {
	if ck, err := c.Request.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, ck.Value)
		a.mu.Unlock()
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func (a *Auth) current(c *gin.Context) *session {
	ck, err := c.Request.Cookie(CookieName)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[ck.Value]
	if s == nil {
		return nil
	}
	if time.Now().After(s.Expires) {
		delete(a.sessions, ck.Value)
		return nil
	}
	s.Expires = time.Now().Add(24 * time.Hour) // 滑动续期
	return s
}

func (a *Auth) Current(c *gin.Context) *session { return a.current(c) }

// Require 登录中间件；命中后注入 session 供 handler 使用。
func (a *Auth) Require() gin.HandlerFunc {
	return func(c *gin.Context) {
		s := a.current(c)
		if s == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			return
		}
		c.Set("session", s)
		c.Next()
	}
}
