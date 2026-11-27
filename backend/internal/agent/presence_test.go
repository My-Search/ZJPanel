package agent

import (
	"testing"
	"time"
)

func TestOnlineRequiresFreshHeartbeat(t *testing.T) {
	s := &Service{conns: map[uint]*conn{1: {}}, alive: map[uint]time.Time{}}

	if !s.Online(1) {
		t.Fatal("刚建立连接、尚未收到心跳时应视为在线")
	}

	s.alive[1] = time.Now().Add(-2 * time.Minute)
	if s.Online(1) {
		t.Fatal("超过 45s 没有心跳的 TCP 半开连接不应显示在线")
	}

	s.alive[1] = time.Now()
	delete(s.conns, 1)
	if s.Online(1) {
		t.Fatal("连接已移除后不应显示在线")
	}
}
