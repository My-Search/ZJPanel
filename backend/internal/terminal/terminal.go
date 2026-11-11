package terminal

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// Client 由 HTTP 层实现：把会话输出写给一条 WebSocket 连接。
type Client interface {
	WriteBinary(b []byte) error
	WriteText(s string) error
}

// Session 一个常驻终端会话：PTY/SSH 通道存活于后端，前端刷新重连后回放缓冲继续。
type Session struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // ssh | local
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`

	mu      sync.Mutex
	buf     *RingBuf
	pty     ptyHandle
	stats   *StatsCollector
	clients map[Client]struct{}
	lastAct time.Time
	closed  bool
}

func newSession(kind, label string, p ptyHandle) *Session {
	s := &Session{
		ID: pkg.RandomHex(8), Kind: kind, Label: label,
		CreatedAt: time.Now(), buf: NewRingBuf(256 * 1024),
		pty: p, clients: map[Client]struct{}{}, lastAct: time.Now(),
	}
	return s
}

// Info 会话摘要（前端恢复标签）。
func (s *Session) Info() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"id": s.ID, "kind": s.Kind, "label": s.Label, "createdAt": s.CreatedAt,
	}
}

func (s *Session) broadcast(data []byte) {
	s.mu.Lock()
	s.buf.Write(data)
	cs := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.lastAct = time.Now()
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.WriteBinary(data)
	}
}

func (s *Session) notifyClosed() {
	s.mu.Lock()
	cs := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.closed = true
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.WriteText(`{"type":"closed"}`)
	}
}

// Attach 注册客户端连接并返回回放缓冲。
func (s *Session) Attach(c Client) []byte {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.lastAct = time.Now()
	replay := s.buf.ReadAll()
	s.mu.Unlock()
	return replay
}

func (s *Session) Detach(c Client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

func (s *Session) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *Session) Input(data []byte) error {
	s.mu.Lock()
	s.lastAct = time.Now()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return ErrClosed
	}
	return p.Write(data)
}

func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return ErrClosed
	}
	return p.Resize(cols, rows)
}

func (s *Session) Close() {
	s.mu.Lock()
	p, already := s.pty, s.closed
	s.closed = true
	s.mu.Unlock()
	if p != nil && !already {
		p.Close()
	}
	s.StopStats()
	s.notifyClosed()
}

// SetStats 挂载状态栏采集器（EnableStats 调用）。
func (s *Session) SetStats(c *StatsCollector) {
	s.mu.Lock()
	s.stats = c
	s.mu.Unlock()
}

// StopStats 停止并摘除状态栏采集器。
func (s *Session) StopStats() {
	s.mu.Lock()
	c := s.stats
	s.stats = nil
	s.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// Stats 最近一次机器资源采样（终端底部状态栏；未启用或尚未采到时 ok=false）。
func (s *Session) Stats() (HostStats, bool) {
	s.mu.Lock()
	c := s.stats
	s.mu.Unlock()
	if c == nil {
		return HostStats{}, false
	}
	return c.Last()
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAct
}

// Manager 会话注册表：多会话并发 + 空闲回收。
type Manager struct {
	db       *gorm.DB
	box      *pkg.SecretBox
	mu       sync.RWMutex
	sessions map[string]*Session
	stop     chan struct{}
	once     sync.Once
}

func NewManager(db *gorm.DB, box *pkg.SecretBox) *Manager {
	m := &Manager{db: db, box: box, sessions: map[string]*Session{}, stop: make(chan struct{})}
	go m.gcLoop()
	return m
}

func (m *Manager) gcLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			now := time.Now()
			for _, s := range m.list() {
				if s.ClientCount() == 0 && now.Sub(s.idleSince()) > 30*time.Minute && !s.isClosed() {
					s.Close()
				}
			}
		}
	}
}

func (m *Manager) Close() { m.once.Do(func() { close(m.stop) }) }

// CreateSSH 按 SSH 主机 ID 建立常驻会话。
func (m *Manager) CreateSSH(hostID uint) (*Session, error) {
	var host model.SshHost
	if err := m.db.First(&host, hostID).Error; err != nil {
		return nil, fmt.Errorf("SSH 主机不存在")
	}
	client, err := dialSSH(host, m.box)
	if err != nil {
		return nil, err
	}
	var sess *Session
	p, err := newSSHPty(client, 80, 24,
		func(out []byte) { sess.broadcast(out) },
		func() {
			sess.Close()
			m.remove(sess.ID)
		})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	sess = newSession("ssh", fmt.Sprintf("%s@%s", host.Username, host.Host), p)
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	m.EnableStats(sess) // 底部状态栏：采集远程机器资源
	return sess, nil
}

// CreateLocal 本地（容器内）终端。
func (m *Manager) CreateLocal() (*Session, error) {
	var sess *Session
	p, err := newLocalPty(80, 24,
		func(out []byte) { sess.broadcast(out) },
		func() {
			sess.Close()
			m.remove(sess.ID)
		})
	if err != nil {
		return nil, err
	}
	sess = newSession("local", "本地终端", p)
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	m.EnableStats(sess) // 底部状态栏：采集面板宿主机资源
	return sess, nil
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	sess := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if sess != nil {
		sess.StopStats() // 会话移除时停止状态栏采集
	}
}

func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

func (m *Manager) list() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// List 存活会话清单（刷新页面恢复标签用）。
func (m *Manager) List() []map[string]any {
	out := []map[string]any{}
	for _, s := range m.list() {
		out = append(out, map[string]any{
			"id": s.ID, "kind": s.Kind, "label": s.Label,
			"createdAt": s.CreatedAt, "clients": s.ClientCount(),
		})
	}
	return out
}

func (m *Manager) CloseSession(id string) bool {
	s := m.Get(id)
	if s == nil {
		return false
	}
	s.Close()
	m.remove(id)
	return true
}
