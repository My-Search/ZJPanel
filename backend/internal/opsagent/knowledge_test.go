package opsagent

import (
	"strings"
	"testing"

	"zjpanel/backend/internal/model"
)

// 知识库 CRUD 校验 + kb_search / kb_read 工具行为。
func TestKnowledgeCRUDAndTools(t *testing.T) {
	db := testDB(t)
	s := &Service{db: db}

	if err := s.SaveKnowledge(model.OpsAgentKnowledge{Title: "  ", Content: "x"}); err == nil {
		t.Fatal("空标题应被拒绝")
	}
	if err := s.SaveKnowledge(model.OpsAgentKnowledge{Title: "规范", Content: "  "}); err == nil {
		t.Fatal("空内容应被拒绝")
	}
	if err := s.SaveKnowledge(model.OpsAgentKnowledge{Title: "nginx 规范", Tags: "nginx,规范", Content: "改配置必须 nginx -t 通过后再 reload。", Persistent: true}); err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if err := s.SaveKnowledge(model.OpsAgentKnowledge{ID: 1, Title: "nginx 规范 v2", Tags: "nginx", Content: "更新后的规范内容。", Persistent: true}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	var row model.OpsAgentKnowledge
	db.First(&row, 1)
	if row.Title != "nginx 规范 v2" || row.Content != "更新后的规范内容。" {
		t.Fatalf("更新未落库: %+v", row)
	}
	if err := s.SaveKnowledge(model.OpsAgentKnowledge{ID: 99, Title: "x", Content: "y"}); err == nil {
		t.Fatal("更新不存在的条目应报错")
	}

	// kb_search：命中标题，常驻条目带标记
	if got := s.toolKBSearch(map[string]any{"query": "nginx"}); !strings.Contains(got, "#1") || !strings.Contains(got, "[常驻]") {
		t.Fatalf("kb_search 未命中: %s", got)
	}
	// kb_search：无命中
	if got := s.toolKBSearch(map[string]any{"query": "不存在"}); !strings.Contains(got, "无匹配") {
		t.Fatalf("kb_search 空结果异常: %s", got)
	}
	// kb_read：全文（数字参数兼容 float64，与 JSON 反序列化口径一致）
	if got := s.toolKBRead(map[string]any{"id": float64(1)}); !strings.Contains(got, "更新后的规范内容") {
		t.Fatalf("kb_read 内容错误: %s", got)
	}
	if got := s.toolKBRead(map[string]any{"id": float64(99)}); !strings.Contains(got, "不存在") {
		t.Fatalf("kb_read 未报不存在: %s", got)
	}

	// 删除后检索不到
	if err := s.DeleteKnowledge(1); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got := s.toolKBSearch(map[string]any{"query": "nginx"}); !strings.Contains(got, "无匹配") {
		t.Fatalf("删除后 kb_search 仍命中: %s", got)
	}
}

// 常驻知识注入系统提示词：每次会话全量携带；总量超限截断并提示可用工具查询；非常驻条目不注入。
func TestPersistentKnowledgeInjection(t *testing.T) {
	db := testDB(t)
	s := &Service{db: db}
	sess := model.OpsAgentSession{Title: "t", Status: "idle", DefenseMode: "observe"}

	// 非常驻条目不注入，无常驻知识时系统提示词不含常驻段落
	db.Create(&model.OpsAgentKnowledge{Title: "普通条目", Content: "非常驻内容", Persistent: false})
	prompt := s.buildContext(&sess, nil, nil)[0].Content
	if strings.Contains(prompt, "# 常驻知识") {
		t.Fatal("无常驻知识时不应出现常驻段落")
	}

	db.Create(&model.OpsAgentKnowledge{Title: "重启规范", Content: "reload 前必须 nginx -t", Persistent: true})
	prompt = s.buildContext(&sess, nil, nil)[0].Content
	if !strings.Contains(prompt, "# 常驻知识") || !strings.Contains(prompt, "reload 前必须 nginx -t") {
		t.Fatalf("常驻知识未注入: %s", prompt)
	}

	// 超总量上限：只注入能放下的部分并给出截断说明
	big := strings.Repeat("长", 6000)
	for i := 0; i < 5; i++ {
		db.Create(&model.OpsAgentKnowledge{Title: "大条目", Content: big, Persistent: true})
	}
	prompt = s.buildContext(&sess, nil, nil)[0].Content
	if !strings.Contains(prompt, "kb_search") {
		t.Fatal("常驻知识超限未提示截断")
	}
	if n := strings.Count(prompt, "长"); n > kbPersistentMaxChars {
		t.Fatalf("常驻注入超出上限: %d > %d", n, kbPersistentMaxChars)
	}
}
