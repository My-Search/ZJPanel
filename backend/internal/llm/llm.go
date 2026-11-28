package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// OpenAI 兼容 chat completions 客户端（手写最小实现，与 internal/mcp 的手写哲学一致）。
// DeepSeek / Qwen / GLM / Kimi / OpenRouter / one-api / Ollama 等均兼容该协议；
// 非流式调用：一轮一响应；流式调用（ChatStream）：SSE 增量回调，正文/思考过程逐段推送。

// Config 模型接入配置（APIKey 由调用方解密后传入，本包不落盘）。
type Config struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// Check 配置完整性校验。
func (c Config) Check() error {
	if strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("请先在设置中配置模型（BaseURL 与模型名必填）")
	}
	return nil
}

// Tool function calling 的工具定义（Schema 即 JSON Schema，直接来自 MCP 注册表）。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

// ToolCall 模型发起的一次工具调用请求。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串（原样透传）
}

// Message 对话消息。Role: system | user | assistant | tool；
// assistant 消息可携带 ToolCalls；tool 消息用 ToolCallID 对应被调用的工具。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
}

// ---- wire 格式（OpenAI 协议字段） ----

type wireFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function wireFunc `json:"function"`
}

type wireMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolDef struct {
	Type     string       `json:"type"`
	Function wireToolFunc `json:"function"`
}

type wireToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMsg     `json:"messages"`
	Tools       []wireToolDef `json:"tools,omitempty"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ---- 流式 wire 格式（SSE data 块） ----

type wireToolCallDelta struct {
	Index    int      `json:"index"`
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function wireFunc `json:"function"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string              `json:"content"`
			Reasoning string              `json:"reasoning_content"` // GLM/DeepSeek 等思考模型
			ToolCalls []wireToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Client OpenAI 兼容客户端。
type Client struct {
	http       *http.Client // 非流式：整体 120s 超时
	streamHTTP *http.Client // 流式：不限整体时长，生命周期由 ctx 控制
}

func NewClient() *Client {
	return &Client{
		http:       &http.Client{Timeout: 120 * time.Second},
		streamHTTP: &http.Client{},
	}
}

// ErrContextLength 上下文超限：调用方应压缩上下文后重试（可用 errors.Is 判定）。
var ErrContextLength = errors.New("上下文超限")

// isContextLengthError 按各厂商报错关键词识别上下文超限（OpenAI/DeepSeek/Qwen/GLM/Ollama 等）。
func isContextLengthError(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{
		"context length", "context_length", "maximum context", "context window",
		"too many tokens", "token limit", "exceeds the model", "reduce the length",
		"上下文长度", "超上下文", "上下文超限", "输入超限", "长度超出",
	} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// endpoint 归一：已带版本段（/v1、/v4 等，如 GLM 的 /api/paas/v4）或以 /api 结尾时
// 原样追加 /chat/completions；否则补 /v1（DeepSeek/Qwen 等官方地址的常规写法）。
var versionSuffix = regexp.MustCompile(`(/v[0-9]+[a-z]*|/api)$`)

func endpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return base
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if versionSuffix.MatchString(base) {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// Chat 单轮对话调用：发送全部消息与工具表，返回助手的下一条消息（可能带工具调用）。
//
// 重试策略：最多重试 MaxRetries 次（429/5xx/网络错误），指数退避（2s 起、30s 封顶，
// 带抖动——不连续轰炸）；401/404 等客户端错误直接失败不浪费重试。
// 上下文超限错误（ErrContextLength）原样返回给调用方，由其压缩后重试，不在本层盲目重试。
func (c *Client) Chat(ctx context.Context, cfg Config, msgs []Message, tools []Tool) (Message, error) {
	if err := cfg.Check(); err != nil {
		return Message{}, err
	}
	req := chatRequest{Model: cfg.Model, Temperature: 0.3}
	for _, m := range msgs {
		wm := wireMsg{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID: tc.ID, Type: "function", Function: wireFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		req.Messages = append(req.Messages, wm)
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, wireToolDef{Type: "function", Function: wireToolFunc{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}

	const maxRetries = 10
	backoff := 2 * time.Second
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避 + 抖动：不连续重试，且随时响应停止/取消
			delay := backoff + time.Duration(rand.Int63n(int64(backoff/4+time.Second)))
			if backoff < 30*time.Second {
				backoff *= 2
			}
			select {
			case <-ctx.Done():
				return Message{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		msg, retryable, err := c.callOnce(ctx, cfg, req)
		if err == nil {
			return msg, nil
		}
		lastErr = err
		if !retryable {
			return Message{}, err
		}
	}
	return Message{}, lastErr
}

func (c *Client) callOnce(ctx context.Context, cfg Config, req chatRequest) (Message, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, false, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Message{}, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Message{}, false, fmt.Errorf("请求模型服务失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Message{}, false, fmt.Errorf("读取模型响应失败: %w", err)
	}
	var cr chatResponse
	_ = json.Unmarshal(data, &cr)
	if resp.StatusCode >= 400 {
		msg := ""
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		// 上下文超限：不同厂商报错措辞不一，按关键词识别（400/413）。
		// 不做普通重试——调用方应压缩上下文后重试。
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge {
			if isContextLengthError(msg) {
				return Message{}, false, fmt.Errorf("%w: %s", ErrContextLength, msg)
			}
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return Message{}, retryable, fmt.Errorf("模型服务返回 %d: %s", resp.StatusCode, msg)
	}
	if cr.Error != nil {
		return Message{}, false, fmt.Errorf("模型服务错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Message{}, false, fmt.Errorf("模型返回空响应")
	}
	m := cr.Choices[0].Message
	out := Message{Role: "assistant", Content: m.Content}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return Message{}, false, fmt.Errorf("模型返回空响应")
	}
	return out, false, nil
}

// Ping 连通性测试：发一句固定问候，能返回任意内容即视为配置可用。
func (c *Client) Ping(ctx context.Context, cfg Config) error {
	_, err := c.Chat(ctx, cfg, []Message{{Role: "user", Content: "回复“OK”两个字母即可"}}, nil)
	return err
}

// ===================== 流式调用 =====================

// StreamDelta 流式增量：Kind 为 "content"（正文）或 "reasoning"（思考过程）。
type StreamDelta struct {
	Kind string
	Text string
}

// ChatStream 流式单轮对话：语义与 Chat 一致，但正文/思考过程按增量经 onDelta 回调推送，
// 工具调用仍在结束时一次性返回（流式中按 index 碎片累积）。
//
// 重试策略与 Chat 相同（429/5xx/网络错误指数退避），但仅在收到任何增量之前才允许重试——
// 已向调用方推送过内容后中断的，返回已累积的 Message 与错误，由调用方决定如何收尾。
// 首个增量前因 4xx 失败（部分网关不支持 stream 参数）自动降级为非流式 Chat 兜底一次。
// onDelta 返回 error 时立即中止并原样返回该错误。
func (c *Client) ChatStream(ctx context.Context, cfg Config, msgs []Message, tools []Tool, onDelta func(StreamDelta) error) (Message, error) {
	if err := cfg.Check(); err != nil {
		return Message{}, err
	}
	req := chatRequest{Model: cfg.Model, Temperature: 0.3, Stream: true}
	for _, m := range msgs {
		wm := wireMsg{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID: tc.ID, Type: "function", Function: wireFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		req.Messages = append(req.Messages, wm)
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, wireToolDef{Type: "function", Function: wireToolFunc{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}

	const maxRetries = 10
	backoff := 2 * time.Second
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff + time.Duration(rand.Int63n(int64(backoff/4+time.Second)))
			if backoff < 30*time.Second {
				backoff *= 2
			}
			select {
			case <-ctx.Done():
				return Message{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		msg, emitted, retryable, err := c.callOnceStream(ctx, cfg, req, onDelta)
		if err == nil {
			return msg, nil
		}
		if emitted {
			// 已推送过增量：不可重试（重放会重复输出），交调用方收尾
			return msg, err
		}
		lastErr = err
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		if !retryable {
			// 4xx：可能是网关不支持 stream，降级非流式兜底一次
			if fb, ferr := c.Chat(ctx, cfg, msgs, tools); ferr == nil {
				if onDelta != nil {
					_ = onDelta(StreamDelta{Kind: "content", Text: fb.Content}) // 降级路径一次性补齐
				}
				return fb, nil
			} else {
				return Message{}, ferr
			}
		}
	}
	return Message{}, lastErr
}

// callOnceStream 发起一次流式请求并消费 SSE。
// 返回 emitted 标记是否已向 onDelta 推送过内容；出错时携带已累积的部分结果。
func (c *Client) callOnceStream(ctx context.Context, cfg Config, req chatRequest, onDelta func(StreamDelta) error) (Message, bool, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, false, false, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Message{}, false, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := c.streamHTTP.Do(httpReq)
	if err != nil {
		return Message{}, false, true, fmt.Errorf("请求模型服务失败: %w", err)
	}
	defer resp.Body.Close()

	out := Message{Role: "assistant"}
	emitted := false
	emit := func(kind, text string) error {
		if text == "" || onDelta == nil {
			return nil
		}
		emitted = true
		return onDelta(StreamDelta{Kind: kind, Text: text})
	}
	streamErr := func(format string, a ...any) (Message, bool, bool, error) {
		return out, emitted, false, fmt.Errorf(format, a...)
	}

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		var cr chatResponse
		_ = json.Unmarshal(data, &cr)
		msg := ""
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge {
			if isContextLengthError(msg) {
				return Message{}, false, false, fmt.Errorf("%w: %s", ErrContextLength, msg)
			}
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return Message{}, false, retryable, fmt.Errorf("模型服务返回 %d: %s", resp.StatusCode, msg)
	}

	// 逐行消费 SSE：data: {...} / data: [DONE]
	calls := map[int]*ToolCall{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			return streamErr("模型服务错误: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			d := ch.Delta
			if d.Reasoning != "" {
				if err := emit("reasoning", d.Reasoning); err != nil {
					return out, emitted, false, err
				}
			}
			if d.Content != "" {
				out.Content += d.Content
				if err := emit("content", d.Content); err != nil {
					return out, emitted, false, err
				}
			}
			for _, tcd := range d.ToolCalls {
				tc := calls[tcd.Index]
				if tc == nil {
					tc = &ToolCall{}
					calls[tcd.Index] = tc
				}
				if tcd.ID != "" {
					tc.ID = tcd.ID
				}
				if tcd.Function.Name != "" {
					tc.Name = tcd.Function.Name
				}
				tc.Arguments += tcd.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		// 网络中断/ctx 取消：已累积内容随结果返回（emitted 决定是否可重试）
		return out, emitted, true, fmt.Errorf("读取模型流式响应失败: %w", err)
	}
	for i := 0; i < len(calls); i++ {
		if tc := calls[i]; tc != nil && tc.Name != "" {
			out.ToolCalls = append(out.ToolCalls, *tc)
		}
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return Message{}, emitted, false, fmt.Errorf("模型返回空响应")
	}
	return out, emitted, false, nil
}
