package terminal

import (
	"errors"
	"sync"
)

// RingBuf 固定容量环形缓冲：保存最近输出，刷新页面重连时回放。
// start/end 是会话输出流上的单调字节偏移：end 为已写入总量，start=end-size 为环内最老字节的偏移，
// 客户端凭已消费偏移（since）做增量续传。
type RingBuf struct {
	mu    sync.Mutex
	data  []byte
	cap   int
	size  int
	pos   int    // 下一个写位置
	start uint64 // 环内最老字节的流偏移
	end   uint64 // 已写入总字节数（下一字节偏移）
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
	r.end += uint64(len(p))
	for len(p) > 0 {
		n := copy(r.data[r.pos:], p)
		r.pos = (r.pos + n) % r.cap
		r.size = min(r.size+n, r.cap)
		p = p[n:]
	}
	r.start = r.end - uint64(r.size)
}

// snapshot 返回逻辑顺序的全量缓冲（旧→新）。调用方需持有 mu。
func (r *RingBuf) snapshot() []byte {
	if r.size == 0 {
		return nil
	}
	if r.size < r.cap {
		return r.data[:r.size]
	}
	out := make([]byte, 0, r.cap)
	out = append(out, r.data[r.pos:]...)
	out = append(out, r.data[:r.pos]...)
	return out
}

// ReadAll 返回缓冲内全部数据（旧→新）。
func (r *RingBuf) ReadAll() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.snapshot()...)
}

// ReadFrom 返回流偏移 since 之后的缓冲数据；since 不在 [Start,End] 区间（已滚出环形缓冲或超前）时 ok=false。
func (r *RingBuf) ReadFrom(since uint64) (out []byte, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if since < r.start || since > r.end {
		return nil, false
	}
	all := r.snapshot()
	off := int(since - r.start)
	return append([]byte(nil), all[off:]...), true
}

// Start 返回环内最老字节的流偏移。
func (r *RingBuf) Start() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start
}

// End 返回已写入总字节数（下一字节偏移）。
func (r *RingBuf) End() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.end
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
