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
	if err := db.AutoMigrate(&model.OpsAgentSession{}, &model.OpsAgentMessage{}, &model.OpsAgentMemory{}, &model.OpsAgentKnowledge{}, &model.Setting{}, &model.Site{}, &model.AuditLog{}); err != nil {
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
	// 开了 SSH 执行也必须遵守会话防御模式：仅观察不暴露 host_exec
	if names(s.buildToolset("observe", true))["host_exec"] {
		t.Fatal("observe 模式不应包含 host_exec（即使 AllowSSHExec 已开启）")
	}

	auto := names(s.buildToolset("auto", true))
	if !auto["host_ban_ip"] || !auto["host_exec"] {
		t.Fatal("auto 模式应包含处置工具与 host_exec")
	}

	// observe 二次拦截：dispatch 对处置类 MCP 工具与 host_exec 直接拒绝
	if got := s.dispatch(&model.OpsAgentSession{DefenseMode: "observe"}, llmCall("c1", "host_ban_ip", "{}")); got != observeBlocked {
		t.Fatalf("observe 拦截失效: %s", got)
	}
	if got := s.dispatch(&model.OpsAgentSession{DefenseMode: "observe"}, llmCall("c2", "host_exec", `{"hostId":1,"command":"id"}`)); got != observeBlocked {
		t.Fatalf("observe 模式 host_exec 未拦截: %s", got)
	}
}

// 防御模式是会话级属性：创建时显式指定，非法/空回落默认；空闲可切换，运行中锁定。
func TestSessionDefenseMode(t *testing.T) {
	db := testDB(t)
	s := &Service{db: db}

	sess, err := s.CreateSession("t", "manual", nil, "user", "", ModeConfirm)
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if sess.DefenseMode != ModeConfirm {
		t.Fatalf("会话应记录显式模式 confirm，实际 %s", sess.DefenseMode)
	}
	fallback, _ := s.CreateSession("t2", "manual", nil, "user", "", "")
	if fallback.DefenseMode != ModeObserve {
		t.Fatalf("空模式应回落默认 observe，实际 %s", fallback.DefenseMode)
	}

	if err := s.SetDefenseMode(sess.ID, ModeAuto); err != nil {
		t.Fatalf("空闲会话切换模式失败: %v", err)
	}
	var got model.OpsAgentSession
	db.First(&got, sess.ID)
	if got.DefenseMode != ModeAuto {
		t.Fatalf("模式未落库: %s", got.DefenseMode)
	}

	db.Model(&model.OpsAgentSession{}).Where("id = ?", sess.ID).Update("status", "running")
	if err := s.SetDefenseMode(sess.ID, ModeObserve); err == nil {
		t.Fatal("运行中会话不应允许切换防御模式")
	}
	if err := s.SetDefenseMode(sess.ID, "bad"); err == nil {
		t.Fatal("非法防御模式应被拒绝")
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
