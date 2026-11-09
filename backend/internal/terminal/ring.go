package terminal

import (
	"errors"
	"sync"
)

// RingBuf 固定容量环形缓冲：保存最近输出，刷新页面重连时回放。
type RingBuf struct {
	mu   sync.Mutex
	data []byte
	cap  int
	size int
	pos  int // 下一个写位置
}

func NewRingBuf(capacity int) *RingBuf {
	if capacity <= 0 {
		capacity = 256 * 1024
	}
	return &RingBuf{data: make([]byte, capacity), cap: capacity}
}

func (r *RingBuf) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(p) > 0 {
		n := copy(r.data[r.pos:], p)
		r.pos = (r.pos + n) % r.cap
		r.size = min(r.size+n, r.cap)
		p = p[n:]
	}
}

func (r *RingBuf) ReadAll() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size < r.cap {
		return append([]byte(nil), r.data[:r.size]...)
	}
	out := make([]byte, 0, r.cap)
	out = append(out, r.data[r.pos:]...)
	out = append(out, r.data[:r.pos]...)
	return out
}

func (r *RingBuf) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var ErrClosed = errors.New("terminal session closed")
