package cronjobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSplitUTF8(t *testing.T) {
	whole, rest := splitUTF8([]byte("你\xe5"))
	if string(whole) != "你" || len(rest) != 1 {
		t.Fatalf("截断多字节字符时应保留完整部分，got whole=%q rest=%v", whole, rest)
	}
	next := append(append([]byte{}, rest...), []byte("\xa5\xbd")...)
	if whole2, rest2 := splitUTF8(next); string(whole2) != "好" || len(rest2) != 0 {
		t.Fatalf("补齐后应输出完整字符，got whole=%q rest=%v", whole2, rest2)
	}
	if whole, rest := splitUTF8([]byte("ok\n")); string(whole) != "ok\n" || len(rest) != 0 {
		t.Fatalf("ASCII 应原样返回，got whole=%q rest=%v", whole, rest)
	}
}

func TestLogsIncludesRunning(t *testing.T) {
	p := New().(*Plugin)
	run := &liveRun{
		RunID: "r1", TaskID: "t", StartAt: "2026-01-01 00:00:00", started: time.Now(),
		status: "running", subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
	}
	p.runs[run.RunID] = run
	p.runOrder = []string{run.RunID}
	p.logs["t"] = []RunLog{{RunID: "r0", TaskID: "t", Status: "success", Output: "done"}}

	req := httptest.NewRequest(http.MethodGet, "/logs/t", nil)
	req.Header.Set("X-Plugin-Path", "/logs/t")
	rec := httptest.NewRecorder()
	p.apiLogs(rec, req)

	var got []RunLog
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析日志响应失败: %v", err)
	}
	if len(got) != 2 || got[0].Status != "running" || got[1].Status != "success" {
		t.Fatalf("进行中的执行应排在历史之前，got %+v", got)
	}
}

func TestRunStreamChunksLargeOutput(t *testing.T) {
	p := New().(*Plugin)
	run := &liveRun{
		RunID: "r3", TaskID: "t", StartAt: "2026-01-01 00:00:00", started: time.Now(),
		status: "success", closed: true, subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
		buf: []byte(strings.Repeat("0123456789", 10000)), // 100KB，必须分多条 SSE 事件
	}
	p.runs[run.RunID] = run

	req := httptest.NewRequest(http.MethodGet, "/runs/r3/stream", nil)
	req.Header.Set("X-Plugin-Path", "/runs/r3/stream")
	rec := httptest.NewRecorder()
	p.apiRunStream(rec, req)

	body := rec.Body.String()
	if got := strings.Count(body, `"type":"out"`); got < 2 {
		t.Fatalf("大输出应拆成多条事件，got %d：%s", got, body[:200])
	}
	if !strings.Contains(body, `"type":"done"`) {
		t.Fatalf("大输出结束后应发送 done 事件：%s", body[len(body)-200:])
	}
}

func TestRunStreamSSE(t *testing.T) {
	p := New().(*Plugin)
	run := &liveRun{
		RunID: "r2", TaskID: "t", StartAt: "2026-01-01 00:00:00", started: time.Now(),
		status: "running", subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
	}
	p.runs[run.RunID] = run
	p.appendOut(run, []byte("hello "))

	req := httptest.NewRequest(http.MethodGet, "/runs/r2/stream", nil)
	req.Header.Set("X-Plugin-Path", "/runs/r2/stream")
	rec := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		p.apiRunStream(rec, req)
		close(finished)
	}()

	time.Sleep(50 * time.Millisecond)
	p.appendOut(run, []byte("世界"))
	p.finishRun(run, "success", 0, false)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("执行结束后 SSE 应立即收尾并返回")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type 应为 text/event-stream，got %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`"type":"out"`, "hello", "世界", `"type":"done"`, `"exitCode":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE 响应缺少 %q：%s", want, body)
		}
	}
}
