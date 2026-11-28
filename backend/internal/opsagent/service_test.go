package opsagent

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/llm"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
)

func llmCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: args}
}

func testDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.OpsAgentSession{}, &model.OpsAgentMessage{}, &model.OpsAgentMemory{}, &model.Setting{}, &model.Site{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// observe 模式必须剔除处置工具；auto 模式 + AllowSSHExec 才有 host_exec；内建记忆工具始终在场。
func TestBuildToolsetFiltering(t *testing.T) {
	reg := mcp.NewRegistry()
	_ = reg.Register(&mcp.Tool{Name: "list_sites", Owner: "core"})
	_ = reg.Register(&mcp.Tool{Name: "host_ban_ip", Owner: "core", Mutating: true})
	s := &Service{reg: reg}

	names := func(tools []agentTool) map[string]bool {
		m := map[string]bool{}
		for _, t := range tools {
			m[t.Name] = true
		}
		return m
	}

	observe := names(s.buildToolset("observe", false))
	if observe["host_ban_ip"] {
		t.Fatal("observe 模式不应包含处置工具 host_ban_ip")
	}
	if !observe["list_sites"] || !observe["memory_save"] || !observe["memory_search"] {
		t.Fatal("observe 模式缺少只读/记忆工具")
	}
	if observe["host_exec"] {
		t.Fatal("未开启 AllowSSHExec 不应包含 host_exec")
	}

	auto := names(s.buildToolset("auto", true))
	if !auto["host_ban_ip"] || !auto["host_exec"] {
		t.Fatal("auto 模式应包含处置工具与 host_exec")
	}

	// observe 二次拦截：dispatch 对处置工具直接拒绝
	if got := s.dispatch(&model.OpsAgentSession{DefenseMode: "observe"}, llmCall("c1", "host_ban_ip", "{}")); got != observeBlocked {
		t.Fatalf("observe 拦截失效: %s", got)
	}
}

// 构建上下文：pending 工具行必须补「未执行」答案，保证 assistant(tool_calls)→tool 协议完整。
func TestBuildContextPending(t *testing.T) {
	db := testDB(t)
	s := &Service{db: db}
	sess := model.OpsAgentSession{Title: "t", Status: "interrupted", DefenseMode: "observe"}
	db.Create(&sess)
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "assistant", Meta: metaToolCallsJSON([]llm.ToolCall{llmCall("call_1", "host_ban_ip", "{}")})})
	db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "tool", Content: "等待用户确认该操作…", Meta: metaPendingJSON("call_1"), ToolName: "host_ban_ip"})

	msgs := s.buildContext(&sess, nil, nil)
	if len(msgs) != 3 { // system + assistant + tool
		t.Fatalf("消息数错误: %d", len(msgs))
	}
	toolMsg := msgs[2]
	if toolMsg.Role != "tool" || toolMsg.Content == "等待用户确认该操作…" {
		t.Fatalf("pending 行未补答案: %+v", toolMsg)
	}
	if toolMsg.ToolCallID != "call_1" {
		t.Fatalf("callId 丢失: %+v", toolMsg)
	}
	assistant := msgs[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "host_ban_ip" {
		t.Fatalf("assistant toolCalls 重建失败: %+v", assistant)
	}
}
