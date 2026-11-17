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
//
// 每个会话签发时记录当时用户的密码哈希指纹，并在每次请求时回查数据库
// 校验指纹：密码被修改（无论经面板 API 还是带外改库）后，旧会话立即失效，
// 防止"短暂 DB 写窗口换取 24h 持久会话"的驻留攻击。

type session struct {
	UserID   uint
	Username string
	PwFP     string // 签发时用户密码哈希的指纹
	Expires  time.Time
}

type Auth struct {
	mu       sync.Mutex
	sessions map[string]*session
	// pwFP 由 boot 注入：按用户 ID 返回当前密码哈希指纹（false=用户不存在）。
	pwFP func(uid uint) (string, bool)
}

const CookieName = "zjp_session"

// NewAuth pwFP 为密码指纹回查函数（绑定当前凭据）。
func NewAuth(pwFP func(uid uint) (string, bool)) *Auth {
	return &Auth{sessions: map[string]*session{}, pwFP: pwFP}
}

// Login 签发会话：记录当前密码哈希指纹。
func (a *Auth) Login(c *gin.Context, uid uint, username, pwHash string) {
	token := pkg.RandomHex(32)
	a.mu.Lock()
	a.sessions[token] = &session{UserID: uid, Username: username,
		PwFP: pkg.PwFingerprint(pwHash), Expires: time.Now().Add(24 * time.Hour)}
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
	s := a.sessions[ck.Value]
	if s == nil {
		a.mu.Unlock()
		return nil
	}
	if time.Now().After(s.Expires) {
		delete(a.sessions, ck.Value)
		a.mu.Unlock()
		return nil
	}
	uid, fp := s.UserID, s.PwFP
	a.mu.Unlock()
	// 回查当前密码指纹：哈希已变（改密码/带外篡改）或用户已删除即吊销。
	cur, ok := a.pwFP(uid)
	if !ok || cur != fp {
		a.mu.Lock()
		// 双检：期间可能恰好被重新登录刷新，避免误杀并发会话
		if cur2, ok2 := a.sessions[ck.Value]; ok2 && cur2.PwFP == fp {
			delete(a.sessions, ck.Value)
		}
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	s.Expires = time.Now().Add(24 * time.Hour) // 滑动续期
	a.mu.Unlock()
	return s
}

func (a *Auth) Current(c *gin.Context) *session { return a.current(c) }

// UpdateUsername 更新当前会话缓存的用户名（改用户名后无需重新登录）。
func (a *Auth) UpdateUsername(c *gin.Context, username string) {
	if s := a.current(c); s != nil {
		a.mu.Lock()
		s.Username = username
		a.mu.Unlock()
	}
}

// RevokeAllForUser 吊销指定用户的全部会话（改密码后调用）。
func (a *Auth) RevokeAllForUser(uid uint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, s := range a.sessions {
		if s.UserID == uid {
			delete(a.sessions, t)
		}
	}
}

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
