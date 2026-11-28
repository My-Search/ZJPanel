package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// OpenAI 兼容 chat completions 客户端（手写最小实现，与 internal/mcp 的手写哲学一致）。
// DeepSeek / Qwen / GLM / Kimi / OpenRouter / one-api / Ollama 等均兼容该协议；
// 非流式调用：一轮一响应，工具调用按轮落库推送，前端即可看到执行轨迹。

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
	Model       string    `json:"model"`
	Messages    []wireMsg `json:"messages"`
	Tools       []wireToolDef `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
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

// Client OpenAI 兼容客户端。
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 120 * time.Second}}
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

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ { // 429/5xx 一次重试
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Message{}, ctx.Err()
			case <-time.After(2 * time.Second):
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
	_, err := c.Chat(ctx, cfg, []Message{{Role: "user", Content: "回复“OK”两个字母即可"}} , nil)
	return err
}
