package opsagent

import (
	"strings"
	"testing"

	"zjpanel/backend/internal/llm"
	"zjpanel/backend/internal/model"
)

func TestEstimateTextTokens(t *testing.T) {
	cn := estimateTextTokens("检查主机安全状态")      // 8 个非 ASCII → 8+1（非空下限）= 9
	en := estimateTextTokens("check disk usage") // 17 ASCII → 17/4+1 = 5
	if cn != 9 {
		t.Errorf("中文估算=%d 期望 9", cn)
	}
	if en != 5 {
		t.Errorf("英文估算=%d 期望 5", en)
	}
	if estimateTextTokens("") != 0 {
		t.Error("空文本应为 0")
	}
}

// 切点必须落在 user/event 上且不超目标；配对 assistant(tool_calls)+tool 不能被切断。
func TestPickCompactBoundary(t *testing.T) {
	mk := func(role, content string) model.OpsAgentMessage {
		return model.OpsAgentMessage{Role: role, Content: content}
	}
	rows := []model.OpsAgentMessage{
		mk("user", strings.Repeat("早", 200)), // ~201
		mk("assistant", "收到"),                // ~11
		mk("tool", strings.Repeat("t", 300)),  // ~84
		mk("assistant", "查看站点"),              // ~35（带 toolCalls）
		mk("user", strings.Repeat("中", 1600)), // ~1601 ← 合法切点（累积 ~1930）
		mk("assistant", "结论"),
		mk("user", "最新问题一"),
		mk("assistant", "完成"),
	}
	rows[3].Meta = metaToolCallsJSON([]llm.ToolCall{{ID: "c1", Name: "list_sites"}})

	// user 行（索引 4）处得到边界 5：token 收益够、序列化体积在预算内、尾部剩 3 行
	b := pickCompactBoundary(rows)
	if b != 5 {
		t.Fatalf("边界=5 期望，实际 %d", b)
	}
	// 巨长单条消息：在它自身（user 行）即形成切点——只压缩这一条，收益最大、丢失最小
	giant := []model.OpsAgentMessage{
		mk("user", strings.Repeat("巨", 20000)), // 估算 ~20000 tok
		mk("assistant", "收到"),
		mk("tool", "结果"),
		mk("user", strings.Repeat("后", 100)),
		mk("assistant", "结论"),
	}
	if got := pickCompactBoundary(giant); got != 1 {
		t.Fatalf("巨长行切点=1 期望，实际 %d", got)
	}
	// 行数太少 → 0
	if got := pickCompactBoundary(rows[:2]); got != 0 {
		t.Fatalf("不足 4 行应无边界，实际 %d", got)
	}
}

// 压缩后的 buildContext：summary 行替代标记范围内的原始行，范围外照常。
func TestBuildContextSkipsSummarizedRange(t *testing.T) {
	db := testDB(t)
	s := &Service{db: db}
	sess := model.OpsAgentSession{Title: "t", DefenseMode: "observe"}
	db.Create(&sess)
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "user", Content: "第一条（将被压缩）"})       // id=1
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "assistant", Content: "好的"})               // id=2
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "summary", Content: "摘要：用户问过 X",
		Meta: `{"toId":2,"count":2}`})                                                                        // id=3
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "user", Content: "最新问题"})                  // id=4

	msgs := s.buildContext(&sess, nil, nil)
	// system + 摘要(system) + 最新问题 = 3；id 1/2 被跳过
	if len(msgs) != 3 {
		t.Fatalf("消息数应 3，实际 %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "system" || !strings.Contains(msgs[1].Content, "2 条") || !strings.Contains(msgs[1].Content, "用户问过 X") {
		t.Fatalf("摘要行渲染错误: %+v", msgs[1])
	}
	if msgs[2].Content != "最新问题" {
		t.Fatalf("范围外的最新消息应保留: %+v", msgs[2])
	}
	// 原始行仍在库中（绝不删除）
	var cnt int64
	db.Model(&model.OpsAgentMessage{}).Where("session_id = ? AND role <> 'summary'", sess.ID).Count(&cnt)
	if cnt != 3 {
		t.Fatalf("原始消息应保留 3 条（含摘要外全部），实际 %d", cnt)
	}
}
