package logmap

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"zjpanel/backend/internal/pkg"
)

// mkLogLine 生成一条 zjp 格式 access log。
func mkLogLine(ip, path string, ts time.Time) string {
	return fmt.Sprintf(`%s - - [%s] "GET %s HTTP/1.1" 200 123 "-" "ua" srv=a.example.com`,
		ip, ts.Format("02/Jan/2006:15:04:05 -0700"), path)
}

// ringCount 读取已解析总条数（含历史条目）。
func (s *Service) ringCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// TestTailLogsIncremental 增量解析管线回归（v1.0.7 空闲优化引入 stat 跳过后必须保持）：
// 首扫整份日志 → 未变化的文件不重复解析 → 追加新行后必须解析并进入实时流。
func TestTailLogsIncremental(t *testing.T) {
	dir := t.TempDir()
	s := New(&pkg.Config{DataDir: t.TempDir(), NginxLogsDir: dir})
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	drain := func() {
		for {
			select {
			case <-ch:
			case <-time.After(100 * time.Millisecond):
				return
			}
		}
	}
	drain()

	log := filepath.Join(dir, "access.log")
	base := time.Now().Add(-time.Minute)
	if err := os.WriteFile(log, []byte(mkLogLine("1.1.1.1", "/a", base)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.tailLogs()
	if n := s.ringCount(); n != 1 {
		t.Fatalf("首扫应解析 1 条, got %d", n)
	}
	// 未变化（size/mtime 均未动）：不得重复解析
	s.tailLogs()
	if n := s.ringCount(); n != 1 {
		t.Fatalf("未变化文件被重复解析: count=%d", n)
	}
	// 追加一行（size/mtime 变化）：必须解析出新条目并实时推送给订阅者
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(mkLogLine("2.2.2.2", "/b", time.Now()) + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s.tailLogs()
	if n := s.ringCount(); n != 2 {
		t.Fatalf("追加后应解析出第 2 条, got %d（stat 跳过逻辑误吞新行？）", n)
	}
	select {
	case e := <-ch:
		if e.IP != "2.2.2.2" || e.Path != "/b" {
			t.Fatalf("实时流条目错误: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("追加的新请求未进入实时流")
	}
	// 轮转（文件变小）：游标重置、重新解析
	if err := os.WriteFile(log, []byte(mkLogLine("3.3.3.3", "/c", time.Now())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.tailLogs()
	if n := s.ringCount(); n != 3 {
		t.Fatalf("轮转后应重新解析, got %d", n)
	}
	select {
	case e := <-ch:
		if e.IP != "3.3.3.3" {
			t.Fatalf("轮转后实时流条目错误: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("轮转后的新请求未进入实时流")
	}
}
