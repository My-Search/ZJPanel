package opsagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"zjpanel/backend/internal/llm"
	"zjpanel/backend/internal/model"
)

// 上下文压缩：估算 token 超过阈值（模型上下文窗口 × 压缩触发比）或模型返回
// 上下文超限错误时，把较早的一段对话压缩成一条「摘要消息」。
//
// 失败安全（绝不丢上下文）：
//   - 原始消息永不删除，只被摘要标记范围（Meta.toId），构建上下文时用摘要替代该段；
//   - 摘要生成走与主对话相同的 llm.Chat（自带 ≤10 次指数退避重试）；
//   - 摘要最终失败则放弃本次压缩，保持原上下文原样（宁可失败不可丢失）。
//
// 切点安全：压缩边界只落在 user/event 消息上，保证 assistant(tool_calls)+tool
// 配对完整，不会切出悬空的工具结果。

// compactionTriggerPct 估算占用超过窗口该百分比时主动压缩（为回复与摘要留余量）。
const compactionTriggerPct = 75

// rowTokenOverhead 每条消息的协议固定开销估算（role/元信息等）。
const rowTokenOverhead = 8

// estimateTextTokens 轻量估算：非 ASCII 字符（中日韩等）≈1 token，ASCII ≈4 字符/token。
// 无 tokenizer 依赖的启发式，误差 ±20% 量级，足够做压缩触发判断。
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk, ascii := 0, 0
	for _, r := range text {
		if r > 127 {
			cjk++
		} else {
			ascii++
		}
	}
	return cjk + ascii/4 + 1
}

func estimateRowTokens(r *model.OpsAgentMessage) int {
	n := estimateTextTokens(r.Content) + estimateTextTokens(r.ToolArgs) + estimateTextTokens(r.ToolName)
	if r.Role == "assistant" && r.Meta != "" {
		n += estimateTextTokens(r.Meta)
	}
	return n + rowTokenOverhead
}

func estimateRowsTokens(rows []model.OpsAgentMessage) int {
	n := 0
	for i := range rows {
		n += estimateRowTokens(&rows[i])
	}
	return n
}

func estimateMessagesTokens(msgs []llm.Message) int {
	n := 0
	for i := range msgs {
		m := &msgs[i]
		n += estimateTextTokens(m.Content) + estimateTextTokens(m.ToolCallID) + rowTokenOverhead
		for _, tc := range m.ToolCalls {
			n += estimateTextTokens(tc.Name) + estimateTextTokens(tc.Arguments) + rowTokenOverhead
		}
	}
	return n
}

// pickCompactBoundary 在 rows 中找最大的可安全压缩前缀长度：
//   - 切点必须落在 user/event 行上（保证 assistant(tool_calls)+tool 配对完整）；
//   - 前缀 token 估算 ≥ minWorthTokens（值得一次摘要调用）；
//   - 前缀序列化体积 ≤ maxSummaryChars（摘要调用自身的输入预算）；
//   - 尾部至少留 3 行不压缩。
//
// 巨长单条消息不成障碍：序列化按行截断，预算按序列化后体积计。
// 返回 0 表示无可安全压缩的前缀。
func pickCompactBoundary(rows []model.OpsAgentMessage) int {
	if len(rows) < 4 {
		return 0
	}
	const (
		minWorthTokens = 1500
		maxSummaryChars = 100_000
	)
	worth, chars, boundary := 0, 0, 0
	for i := range rows {
		line := serializeRow(&rows[i])
		chars += len(line)
		worth += estimateRowTokens(&rows[i])
		if chars > maxSummaryChars {
			break
		}
		role := rows[i].Role
		if (role == "user" || role == "event") && worth >= minWorthTokens && len(rows)-i >= 3 {
			boundary = i + 1
		}
	}
	return boundary
}

// serializeRow 单行序列化（每行截断，控制摘要调用自身的体积）。
func serializeRow(r *model.OpsAgentMessage) string {
	line := ""
	switch r.Role {
	case "user":
		line = "[用户] " + r.Content
	case "event":
		line = "[事件] " + r.Content
	case "assistant":
		line = "[助手] " + r.Content
		if calls := metaToolCalls(r.Meta); len(calls) > 0 {
			names := make([]string, 0, len(calls))
			for _, c := range calls {
				names = append(names, c.Name)
			}
			line += "（调用工具: " + strings.Join(names, ", ") + "）"
		}
	case "tool":
		line = "[工具:" + r.ToolName + "] " + r.Content
	case "summary":
		line = "[既有摘要] " + r.Content
	}
	return trunc(strings.TrimSpace(line), 800)
}

// serializeRows 把待压缩消息序列化成摘要输入文本。
func serializeRows(rows []model.OpsAgentMessage, upto int) string {
	var b strings.Builder
	for i := 0; i < upto && i < len(rows); i++ {
		line := serializeRow(&rows[i])
		if line == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	out := b.String()
	if len(out) > 120_000 { // 摘要输入体积兜底（约 3 万 token 量级）
		out = out[len(out)-120_000:]
	}
	return out
}

// compactContext 执行一次压缩：序列化较早前缀 → LLM 摘要 → 插入摘要行（标记范围）。
// 成功返回 true；摘要失败返回 false 且不改动任何数据（原始消息全部保留）。
func (s *Service) compactContext(sess *model.OpsAgentSession, ctx context.Context, cfg llm.Config, rows []model.OpsAgentMessage) (bool, error) {
	boundary := pickCompactBoundary(rows)
	if boundary <= 0 {
		return false, fmt.Errorf("没有可安全压缩的对话段（切点必须落在用户/事件消息边界）")
	}
	text := serializeRows(rows, boundary)
	if strings.TrimSpace(text) == "" {
		return false, fmt.Errorf("待压缩内容为空")
	}
	prompt := "以下是一段 AI 运维智能体与用户的对话记录。请把它压缩成一份要点摘要，供后续对话作为历史上下文使用。必须保留：\n" +
		"1. 用户明确的指令、偏好与交代过的事项；\n" +
		"2. 已确认的关键事实（主机名/路径/域名/版本号/数值等）；\n" +
		"3. 已执行过的操作及其结果（工具名与关键参数）；\n" +
		"4. 未完成的事项与悬而未决的问题。\n" +
		"用简洁的条目输出，不要寒暄，总长不超过 600 字。\n\n对话记录：\n" + text

	resp, err := s.llm.Chat(ctx, cfg, []llm.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return false, err // 重试已由 llm.Chat 内部完成；仍失败 → 放弃压缩，不动数据
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return false, fmt.Errorf("摘要为空")
	}
	lastID := rows[boundary-1].ID
	meta, _ := json.Marshal(map[string]any{"toId": lastID, "count": boundary})
	s.addMessage(sess.ID, "summary", trunc(summary, 4000), string(meta), "", "")
	return true, nil
}
