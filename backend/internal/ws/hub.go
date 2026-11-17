package ws

import (
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// Hub 中心 WebSocket 广播器：按主题（sitecheck/metrics/logmap/cert-run 等）管理订阅。
// 终端与 docker exec 用独立专用 WS 端点（有输入/回放协议），不走 Hub。

type client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *client) send(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := readCtx()
	defer cancel()
	_ = c.conn.Write(ctx, websocket.MessageText, data)
}

type Hub struct {
	mu     sync.RWMutex
	topics map[string]map[*client]struct{}
}

func NewHub() *Hub {
	return &Hub{topics: map[string]map[*client]struct{}{}}
}

func (h *Hub) Register(topic string, conn *websocket.Conn) func() {
	c := &client{conn: conn}
	h.mu.Lock()
	if h.topics[topic] == nil {
		h.topics[topic] = map[*client]struct{}{}
	}
	h.topics[topic][c] = struct{}{}
	h.mu.Unlock()
	return func() { h.unregister(topic, c) }
}

func (h *Hub) unregister(topic string, c *client) {
	h.mu.Lock()
	delete(h.topics[topic], c)
	h.mu.Unlock()
}

func (h *Hub) Broadcast(topic string, data []byte) {
	h.mu.RLock()
	cs := make([]*client, 0, len(h.topics[topic]))
	for c := range h.topics[topic] {
		cs = append(cs, c)
	}
	h.mu.RUnlock()
	for _, c := range cs {
		c.send(data)
	}
	if len(cs) == 0 && data != nil {
		slog.Debug("ws 广播无订阅者", "topic", topic)
	}
}

func (h *Hub) Count(topic string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.topics[topic])
}
