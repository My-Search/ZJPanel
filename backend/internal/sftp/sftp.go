package sftp

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/terminal"
)

// Manager 按主机缓存 SFTP 客户端：首次使用时拨号建立专用 SSH 连接（复用
// terminal.DialHostByID 的凭证体系），空闲超时自动关闭；操作失败可 Drop
// 失效缓存后重试（由路由层负责），终端会话断连不影响文件管理。
type Manager struct {
	db      *gorm.DB
	box     *pkg.SecretBox
	mu      sync.Mutex
	entries map[uint]*entry
	stop    chan struct{}
}

type entry struct {
	cli     *ssh.Client
	sft     *sftp.Client
	lastUse time.Time
	kaStop  chan struct{}
}

const idleTTL = 10 * time.Minute

func NewManager(db *gorm.DB, box *pkg.SecretBox) *Manager {
	m := &Manager{db: db, box: box, entries: map[uint]*entry{}, stop: make(chan struct{})}
	go m.gcLoop()
	return m
}

// Client 返回主机的 SFTP 客户端（懒建立，命中缓存即续期）。
func (m *Manager) Client(hostID uint) (*sftp.Client, error) {
	m.mu.Lock()
	if e, ok := m.entries[hostID]; ok {
		e.lastUse = time.Now()
		cli := e.sft
		m.mu.Unlock()
		return cli, nil
	}
	m.mu.Unlock()

	cli, err := terminal.DialHostByID(m.db, m.box, hostID)
	if err != nil {
		return nil, err
	}
	sft, err := sftp.NewClient(cli)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("SFTP 子系统建立失败: %w", err)
	}
	e := &entry{cli: cli, sft: sft, lastUse: time.Now(), kaStop: make(chan struct{})}
	go e.keepalive()

	m.mu.Lock()
	if old, ok := m.entries[hostID]; ok { // 并发首用：保留先到者，关闭多余的
		m.mu.Unlock()
		e.close()
		return old.sft, nil
	}
	m.entries[hostID] = e
	m.mu.Unlock()
	slog.Info("sftp 连接建立", "hostId", hostID)
	return e.sft, nil
}

// Drop 失效并关闭主机的缓存连接（操作报错后由路由层调用，下次 Client 重新拨号）。
func (m *Manager) Drop(hostID uint) {
	m.mu.Lock()
	e, ok := m.entries[hostID]
	delete(m.entries, hostID)
	m.mu.Unlock()
	if ok {
		e.close()
	}
}

func (m *Manager) Close() {
	select {
	case <-m.stop:
		return
	default:
	}
	close(m.stop)
	m.mu.Lock()
	es := make([]*entry, 0, len(m.entries))
	for id, e := range m.entries {
		delete(m.entries, id)
		es = append(es, e)
	}
	m.mu.Unlock()
	for _, e := range es {
		e.close()
	}
}

func (m *Manager) gcLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.mu.Lock()
			for id, e := range m.entries {
				if time.Since(e.lastUse) > idleTTL {
					delete(m.entries, id)
					e.close()
					slog.Info("sftp 连接空闲关闭", "hostId", id)
				}
			}
			m.mu.Unlock()
		}
	}
}

// keepalive 每 30s 发一次探测，保持 NAT 映射不老化；连接已死时静默失败，
// 下一次文件操作会报错并触发路由层的 Drop+重试。
func (e *entry) keepalive() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.kaStop:
			return
		case <-t.C:
			go func() {
				done := make(chan struct{})
				go func() { defer close(done); _, _, _ = e.cli.SendRequest("keepalive@openssh.com", true, nil) }()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
			}()
		}
	}
}

func (e *entry) close() {
	select {
	case <-e.kaStop:
		return
	default:
	}
	close(e.kaStop)
	_ = e.sft.Close()
	_ = e.cli.Close()
}
