package terminal

import (
	"encoding/json"
	"fmt"
	"log/slog"
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
// SSH 会话因网络抖动断开后并不消失：保留会话并由后台自动重连，直到用户手动关闭。
type Session struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // ssh | local
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`

	HostID uint // ssh 会话来源主机（断线重连时凭此取回凭证）

	mu        sync.Mutex
	buf       *RingBuf
	pty       ptyHandle
	stats     *StatsCollector
	clients   map[Client]struct{}
	lastInput time.Time // 最近一次用户键入（区分主动 exit 与网络断开）
	cols      int
	rows      int
	down      bool // SSH 通道当前断开、重连循环进行中（前端标签状态）
	reconn    bool // 重连循环是否在跑（防止重复启动）
	closed    bool
}

func newSession(kind, label string, p ptyHandle) *Session {
	return &Session{
		ID: pkg.RandomHex(8), Kind: kind, Label: label,
		CreatedAt: time.Now(), buf: NewRingBuf(1024 * 1024),
		pty: p, clients: map[Client]struct{}{}, lastInput: time.Now(),
		cols: 80, rows: 24,
	}
}

// Info 会话摘要（前端恢复标签；ssh 会话带 hostId 供文件面板使用）。
func (s *Session) Info() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"id": s.ID, "kind": s.Kind, "label": s.Label, "createdAt": s.CreatedAt,
		"hostId": s.HostID,
	}
}

func (s *Session) broadcast(data []byte) {
	s.mu.Lock()
	s.buf.Write(data)
	cs := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.WriteBinary(data)
	}
}

func (s *Session) notifyClosed() {
	s.mu.Lock()
	s.closed = true
	cs := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.WriteText(`{"type":"closed"}`)
	}
}

// notifyEvent 向在线客户端推送 JSON 事件（前端更新标签断连状态）。
func (s *Session) notifyEvent(typ string) {
	s.mu.Lock()
	cs := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": typ, "sessionId": s.ID})
	for _, c := range cs {
		_ = c.WriteText(string(b))
	}
}

// AttachResult 回放结果：Offset 为会话输出流的当前末尾偏移，Replay 为需写入客户端的回放字节，
// Gap 表示客户端的 since 已失效（数据已滚出环形缓冲），只能全量回放并要求客户端重置画面。
type AttachResult struct {
	Offset uint64
	Replay []byte
	Gap    bool
}

// Attach 注册客户端连接并返回回放。since 为客户端已消费的流偏移：
// 仍在缓冲范围内时只回放增量（刷新恢复/断线重连无缝续传），失效时回退全量并置 Gap。
// 注册与读回放在同一锁内，保证与 broadcast 的缓冲写入+客户端快照原子交错——
// 客户端要么在回放里拿到数据、要么在广播帧里拿到，不会重复也不会丢失。
func (s *Session) Attach(c Client, since uint64) AttachResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c] = struct{}{}
	end := s.buf.End()
	if since > 0 && since >= s.buf.Start() && since <= end {
		replay, _ := s.buf.ReadFrom(since)
		return AttachResult{Offset: end, Replay: replay}
	}
	return AttachResult{Offset: end, Replay: s.buf.ReadAll(), Gap: since > 0}
}

// RedrawDance 触发一次 PTY 尺寸抖动（rows-1 → rows）：内核/远端 sshd 向前台进程组发 SIGWINCH，
// 让 vim/htop/ink 类 TUI 整屏重画。用于全量回放（无快照恢复或增量失效）后清掉回放残影。
// 双次抖动+稍长间隔使更多 TUI 应用可靠收到 SIGWINCH 并完成重画。
func (s *Session) RedrawDance() {
	s.mu.Lock()
	p, cols, rows, down := s.pty, s.cols, s.rows, s.down
	s.mu.Unlock()
	if p == nil || down || rows <= 1 {
		return
	}
	// 第一次抖动：rows-1 → rows（部分 TUI 只响应尺寸变化方向为增大的 SIGWINCH）
	_ = p.Resize(cols, rows-1)
	time.AfterFunc(300*time.Millisecond, func() {
		s.mu.Lock()
		same := s.pty == p // 重连循环可能已换新 pty
		s.mu.Unlock()
		if !same {
			return
		}
		_ = p.Resize(cols, rows)
		// 第二次抖动：cols-1 → cols（覆盖只响应列变化的 TUI，如某些版本的 less/man）
		time.AfterFunc(300*time.Millisecond, func() {
			s.mu.Lock()
			same2 := s.pty == p
			s.mu.Unlock()
			if !same2 {
				return
			}
			_ = p.Resize(cols-1, rows)
			time.AfterFunc(200*time.Millisecond, func() {
				s.mu.Lock()
				same3 := s.pty == p
				s.mu.Unlock()
				if same3 {
					_ = p.Resize(cols, rows)
				}
			})
		})
	})
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
	s.lastInput = time.Now()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return ErrClosed
	}
	return p.Write(data)
}

func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	if cols > 0 {
		s.cols = cols
	}
	if rows > 0 {
		s.rows = rows
	}
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

// viewSize 最近一次终端尺寸（重连后按原尺寸重建 PTY）。
func (s *Session) viewSize() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Manager 会话注册表：多会话并发；会话常驻直到用户手动关闭，
// SSH 会话断线后自动重连而非销毁。
type Manager struct {
	db       *gorm.DB
	box      *pkg.SecretBox
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewManager(db *gorm.DB, box *pkg.SecretBox) *Manager {
	return &Manager{db: db, box: box, sessions: map[string]*Session{}}
}

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
	sess := newSession("ssh", fmt.Sprintf("%s@%s", host.Username, host.Host), nil)
	sess.HostID = hostID
	var ph ptyHandle
	p, err := newSSHPty(client, 80, 24,
		func(out []byte) { sess.broadcast(out) },
		func() { m.ptyGoneIfCurrent(sess, ph) })
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	ph = p
	sess.mu.Lock()
	sess.pty = p
	sess.mu.Unlock()
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	m.EnableStats(sess) // 底部状态栏：采集远程机器资源
	return sess, nil
}

// CreateLocal 本地（容器内）终端。
func (m *Manager) CreateLocal() (*Session, error) {
	sess := newSession("local", "本地终端", nil)
	var ph ptyHandle
	p, err := newLocalPty(80, 24,
		func(out []byte) { sess.broadcast(out) },
		func() { m.ptyGoneIfCurrent(sess, ph) })
	if err != nil {
		return nil, err
	}
	ph = p
	sess.mu.Lock()
	sess.pty = p
	sess.mu.Unlock()
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	m.EnableStats(sess) // 底部状态栏：采集面板宿主机资源
	return sess, nil
}

// ptyGoneIfCurrent PTY 通道结束回调：仅当结束的是「当前」通道时才处理。
// 重连成功换用新通道后，旧通道 Close 仍会触发一次它的 onClose——
// 不能让它误判为会话再次断开（否则会无限重连换掉活连接）。
func (m *Manager) ptyGoneIfCurrent(sess *Session, p ptyHandle) {
	sess.mu.Lock()
	cur := sess.pty
	sess.mu.Unlock()
	if cur != p {
		return
	}
	m.ptyGone(sess)
}

// ptyGone PTY 通道结束：本地 shell 退出，或 SSH 通道因网络断开/远端 exit 结束。
// 用户刚键入过（主动 exit）→ 正常关闭移除；SSH 网络断开 → 保留会话并后台重连。
func (m *Manager) ptyGone(sess *Session) {
	// SSH 通道结束（网络断开/远端重启）一律转入后台重连：会话常驻是面板承诺。
	// 旧的 recentInput(3s) 启发式会把「刚敲完命令就遇上网络抖动」误判为用户 exit
	// 而直接销毁会话，shell 状态丢失。exit 后重连得到的是新 shell，标签可手动关闭。
	if sess.Kind == "ssh" && !sess.isClosed() {
		m.markDisconnected(sess)
		return
	}
	sess.Close()
	m.remove(sess.ID)
}

// markDisconnected SSH 网络断开：会话保留（常驻直到手动关闭），后台周期重连。
func (m *Manager) markDisconnected(sess *Session) {
	sess.mu.Lock()
	if sess.closed || sess.reconn {
		sess.mu.Unlock()
		return
	}
	sess.reconn, sess.down = true, true
	sess.mu.Unlock()
	sess.broadcast([]byte("\r\n\x1b[33m[SSH 连接断开，后台自动重连中…]\x1b[0m\r\n"))
	sess.notifyEvent("disconnected")
	slog.Info("ssh 会话断开，转入后台重连", "session", sess.ID, "label", sess.Label)
	go m.reconnectLoop(sess)
}

// reconnectLoop 周期重试直到连上或会话被手动关闭（间隔 3s 起步、最长 15s）。
func (m *Manager) reconnectLoop(sess *Session) {
	defer func() {
		sess.mu.Lock()
		sess.reconn = false
		sess.mu.Unlock()
	}()
	delay := 3 * time.Second
	for {
		time.Sleep(delay)
		if sess.isClosed() {
			return
		}
		var host model.SshHost
		if err := m.db.First(&host, sess.HostID).Error; err != nil {
			sess.broadcast([]byte("\r\n\x1b[33m[SSH 主机已被删除，停止重连；可手动关闭此会话]\x1b[0m\r\n"))
			return
		}
		client, err := dialSSH(host, m.box)
		if err != nil {
			if delay < 15*time.Second {
				delay *= 2
			}
			continue
		}
		cols, rows := sess.viewSize()
		var ph ptyHandle
		p, err := newSSHPty(client, cols, rows,
			func(out []byte) { sess.broadcast(out) },
			func() { m.ptyGoneIfCurrent(sess, ph) })
		if err != nil {
			_ = client.Close()
			continue
		}
		ph = p
		sess.mu.Lock()
		if sess.closed { // 重连成功瞬间被手动关闭：直接丢弃新连接
			sess.mu.Unlock()
			p.Close()
			return
		}
		old := sess.pty
		sess.pty = p
		sess.down = false
		sess.mu.Unlock()
		if old != nil {
			old.Close() // 旧通道已死，兜底释放
		}
		sess.StopStats()           // 旧采集器绑定的是失效连接
		m.EnableStats(sess)        // 在新连接上重启状态栏采集
		sess.broadcast([]byte("\r\n\x1b[32m[已重新连接 " + time.Now().Format("15:04:05") + "]\x1b[0m\r\n"))
		sess.notifyEvent("reconnected")
		slog.Info("ssh 会话重连成功", "session", sess.ID, "label", sess.Label)
		return
	}
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
		s.mu.Lock()
		down := s.down
		s.mu.Unlock()
		out = append(out, map[string]any{
			"id": s.ID, "kind": s.Kind, "label": s.Label, "hostId": s.HostID,
			"createdAt": s.CreatedAt, "clients": s.ClientCount(),
			"disconnected": down,
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
