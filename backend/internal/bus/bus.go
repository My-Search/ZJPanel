package bus

import "sync"

// Bus 轻量事件总线：核心模块发布事件（站点创建/下线、证书签发/到期、站点探测离线等），
// 插件通过 SDK 订阅（如 notify-webhook）。同步分发，handler 需自行恢复 panic。

type Handler func(payload map[string]any)

type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

func New() *Bus { return &Bus{handlers: map[string][]Handler{}} }

func (b *Bus) Subscribe(topic string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], h)
}

func (b *Bus) Publish(topic string, payload map[string]any) {
	b.mu.RLock()
	hs := make([]Handler, len(b.handlers[topic]))
	copy(hs, b.handlers[topic])
	b.mu.RUnlock()
	if payload == nil {
		payload = map[string]any{}
	}
	for _, h := range hs {
		func() {
			defer func() { _ = recover() }()
			h(payload)
		}()
	}
}
