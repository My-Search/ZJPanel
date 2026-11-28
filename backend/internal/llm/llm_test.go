package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httptest 假 OpenAI 服务：校验请求组装（模型/消息/工具/auth），返回固定 tool_calls 响应。
func TestChatRequestAssemblyAndToolCalls(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"panel_info","arguments":"{}"}}]}}]}`))
	}))
	defer srv.Close()

	c := NewClient()
	msgs := []Message{
		{Role: "system", Content: "你是运维助手"},
		{Role: "user", Content: "面板什么状态"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_9", Name: "list_sites", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "call_9", Content: "[]"},
	}
	out, err := c.Chat(context.Background(), Config{
		BaseURL: srv.URL, APIKey: "sk-test", Model: "test-model",
	}, msgs, []Tool{{Name: "panel_info", Description: "面板概览", Schema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("端点归一错误: %s", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("鉴权头错误: %s", gotAuth)
	}
	if gotBody.Model != "test-model" || len(gotBody.Messages) != 4 {
		t.Fatalf("请求组装错误: model=%s msgs=%d", gotBody.Model, len(gotBody.Messages))
	}
	if gotBody.Messages[2].ToolCalls[0].Function.Name != "list_sites" {
		t.Fatalf("assistant 工具调用回传丢失")
	}
	if gotBody.Messages[3].ToolCallID != "call_9" {
		t.Fatalf("tool 消息 tool_call_id 丢失")
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Function.Name != "panel_info" {
		t.Fatalf("工具表组装错误")
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "panel_info" || out.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool_calls 解析错误: %+v", out.ToolCalls)
	}
}

func TestEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":              "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/":             "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1":           "https://api.deepseek.com/v1/chat/completions",
		"http://127.0.0.1:11434/v1":             "http://127.0.0.1:11434/v1/chat/completions",
		"https://open.bigmodel.cn/api/paas/v4":  "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		"https://x.example/api/v1/chat/completions": "https://x.example/api/v1/chat/completions",
	}
	for in, want := range cases {
		if got := endpoint(in); got != want {
			t.Errorf("endpoint(%s) = %s, want %s", in, got, want)
		}
	}
}

// 429 触发一次重试后成功；400 直接失败不重试。
func TestRetryOn429(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer srv.Close()

	c := NewClient()
	out, err := c.Chat(context.Background(), Config{BaseURL: srv.URL, Model: "m"},
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || out.Content != "OK" || n != 2 {
		t.Fatalf("429 重试失败: n=%d out=%+v err=%v", n, out, err)
	}
}
