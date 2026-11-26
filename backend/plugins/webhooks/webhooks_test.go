package webhooks

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signHex(secret string, body []byte, use256 bool) string {
	if use256 {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return "sha1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookRequest(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "s3cr3t"
	valid1 := signHex(secret, body, false)
	valid256 := signHex(secret, body, true)

	// 1. 不带任何签名头必须被拒绝（回归：此前空签名头绕过校验）
	if verifyWebhookRequest(secret, body, "", "") {
		t.Fatal("缺少签名头时应拒绝请求")
	}
	// 2. 空密钥必须被拒绝
	if verifyWebhookRequest("", body, valid1, valid256) {
		t.Fatal("空密钥时应拒绝请求")
	}
	// 3. 合法 sha1 或 sha256 应通过
	if !verifyWebhookRequest(secret, body, valid1, "") {
		t.Fatal("合法 sha1 签名应通过")
	}
	if !verifyWebhookRequest(secret, body, "", valid256) {
		t.Fatal("合法 sha256 签名应通过")
	}
	if !verifyWebhookRequest(secret, body, valid1, valid256) {
		t.Fatal("同时带合法 sha1 与 sha256 签名应通过")
	}
	// 4. 篡改任一签名头必须被拒绝
	if verifyWebhookRequest(secret, body, "sha1=deadbeef", "") {
		t.Fatal("错误 sha1 签名应被拒绝")
	}
	if verifyWebhookRequest(secret, body, valid1, "sha256=deadbeef") {
		t.Fatal("错误 sha256 签名应被拒绝")
	}
}

func TestSplitUTF8(t *testing.T) {
	// 中文按 3 字节编码：把“你好”从“好”的首字节后截断，应只输出完整的“你”，余下字节续到下块。
	whole, rest := splitUTF8([]byte("你\xe5"))
	if string(whole) != "你" || len(rest) != 1 {
		t.Fatalf("截断多字节字符时应保留完整部分，got whole=%q rest=%v", whole, rest)
	}
	// 余下字节与新块拼起来后应还原完整字符（“好”余下的两个续字节 \xa5\xbd）。
	next := append(append([]byte{}, rest...), []byte("\xa5\xbd")...)
	if whole2, rest2 := splitUTF8(next); string(whole2) != "好" || len(rest2) != 0 {
		t.Fatalf("补齐后应输出完整字符，got whole=%q rest=%v", whole2, rest2)
	}
	// 纯 ASCII 原样返回。
	if whole, rest := splitUTF8([]byte("ok\n")); string(whole) != "ok\n" || len(rest) != 0 {
		t.Fatalf("ASCII 应原样返回，got whole=%q rest=%v", whole, rest)
	}
	// 中间存在非法字节（非截断）时不强行拆分，交给 JSON 编码兜底。
	if whole, rest := splitUTF8([]byte("ok\xffok")); string(whole) != "ok\xffok" || len(rest) != 0 {
		t.Fatalf("非尾部非法字节应原样返回，got whole=%q rest=%v", whole, rest)
	}
}

func TestRunStreamSSE(t *testing.T) {
	p := New().(*Plugin)
	run := &liveRun{
		RunID: "r1", HookID: "h", StartAt: "2026-01-01 00:00:00",
		status: "running", subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
	}
	p.runs[run.RunID] = run
	p.appendOut(run, []byte("hello "))

	req := httptest.NewRequest(http.MethodGet, "/runs/r1/stream", nil)
	req.Header.Set("X-Plugin-Path", "/runs/r1/stream")
	rec := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		p.apiRunStream(rec, req)
		close(finished)
	}()

	time.Sleep(50 * time.Millisecond)
	p.appendOut(run, []byte("世界"))
	p.finishRun(run, "success", 0)

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

func TestApiLogsIncludesRunning(t *testing.T) {
	p := New().(*Plugin)
	run := &liveRun{
		RunID: "r2", HookID: "h", StartAt: "2026-01-01 00:00:00",
		status: "running", subs: map[chan struct{}]struct{}{}, done: make(chan struct{}),
	}
	p.runs[run.RunID] = run
	p.runOrder = []string{run.RunID}
	p.logs["h"] = []RunLog{{RunID: "r0", HookID: "h", StartAt: "2026-01-01 00:00:00", Status: "success", Output: "done"}}

	req := httptest.NewRequest(http.MethodGet, "/logs/h", nil)
	req.Header.Set("X-Plugin-Path", "/logs/h")
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
