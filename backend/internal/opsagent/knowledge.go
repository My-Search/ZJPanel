package opsagent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"zjpanel/backend/internal/model"
)

// 运维知识库：人工维护的操作规范/手册/约定（区别于智能体自己积累的记忆）。
// 管理端走设置页 CRUD；Agent 侧提供 kb_search / kb_read 两个内建只读工具。
// Persistent=true 的条目为「常驻知识」：buildContext 时全量注入系统提示词，
// 每次会话（含事件与巡检会话）自动携带；注入量有双重上限保护，避免常驻条目过多撑爆上下文。

const (
	kbContentMax         = 100_000 // 单条内容字符上限（保存时校验）
	kbPersistentMaxN     = 30      // 常驻注入条数上限
	kbPersistentMaxChars = 20_000  // 常驻注入总字符上限（超出部分不注入，Agent 仍可用工具查询）
	kbReadMaxChars       = 8_000   // kb_read 单次返回字符上限
)

// ListKnowledge 知识库条目列表（管理端）：常驻优先，其次按更新时间倒序。
func (s *Service) ListKnowledge(query string) []model.OpsAgentKnowledge {
	tx := s.db.Model(&model.OpsAgentKnowledge{})
	if q := strings.TrimSpace(query); q != "" {
		like := "%" + q + "%"
		tx = tx.Where("title LIKE ? OR tags LIKE ? OR content LIKE ?", like, like, like)
	}
	var list []model.OpsAgentKnowledge
	tx.Order("persistent desc, updated_at desc").Find(&list)
	return list
}

// SaveKnowledge 新建（id=0）/ 更新知识条目。
func (s *Service) SaveKnowledge(k model.OpsAgentKnowledge) error {
	k.Title = strings.TrimSpace(k.Title)
	k.Tags = strings.TrimSpace(k.Tags)
	k.Content = strings.TrimSpace(k.Content)
	if k.Title == "" {
		return fmt.Errorf("标题必填")
	}
	if k.Content == "" {
		return fmt.Errorf("内容必填")
	}
	if utf8.RuneCountInString(k.Content) > kbContentMax {
		return fmt.Errorf("内容过长（上限 %d 字符），请拆分为多条", kbContentMax)
	}
	if k.ID == 0 {
		return s.db.Create(&k).Error
	}
	var row model.OpsAgentKnowledge
	if err := s.db.First(&row, k.ID).Error; err != nil {
		return fmt.Errorf("知识条目不存在")
	}
	row.Title, row.Tags, row.Content, row.Persistent = k.Title, k.Tags, k.Content, k.Persistent
	return s.db.Save(&row).Error
}

// DeleteKnowledge 删除知识条目。
func (s *Service) DeleteKnowledge(id uint) error {
	return s.db.Delete(&model.OpsAgentKnowledge{}, id).Error
}

// persistentKnowledge 常驻知识条目（系统提示词注入用）。
func (s *Service) persistentKnowledge() []model.OpsAgentKnowledge {
	var list []model.OpsAgentKnowledge
	s.db.Where("persistent = ?", true).Order("updated_at desc").Limit(kbPersistentMaxN).Find(&list)
	return list
}

// ---- 内建工具实现 ----

// toolKBSearch 关键词检索知识库（标题/标签/内容 LIKE），返回条目摘要；全文用 kb_read。
func (s *Service) toolKBSearch(args map[string]any) string {
	q, _ := args["query"].(string)
	limit := int(numOf(args["limit"]))
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	tx := s.db.Model(&model.OpsAgentKnowledge{})
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + q + "%"
		tx = tx.Where("title LIKE ? OR tags LIKE ? OR content LIKE ?", like, like, like)
	}
	var list []model.OpsAgentKnowledge
	if err := tx.Order("persistent desc, updated_at desc").Limit(limit).Find(&list).Error; err != nil {
		return "错误: " + err.Error()
	}
	if len(list) == 0 {
		return "（知识库无匹配条目）"
	}
	out := make([]string, 0, len(list))
	for _, k := range list {
		flag := ""
		if k.Persistent {
			flag = " [常驻]"
		}
		out = append(out, fmt.Sprintf("#%d %s%s\n标签: %s\n摘要: %s（kb_read(id) 查看全文）", k.ID, k.Title, flag, k.Tags, trunc(k.Content, 200)))
	}
	return strings.Join(out, "\n\n")
}

// toolKBRead 按 id 读取知识库条目全文。
func (s *Service) toolKBRead(args map[string]any) string {
	id := numOf(args["id"])
	if id <= 0 {
		return "错误: id 必填"
	}
	var k model.OpsAgentKnowledge
	if err := s.db.First(&k, id).Error; err != nil {
		return "错误: 知识条目不存在"
	}
	head := k.Title
	if k.Tags != "" {
		head += "（标签: " + k.Tags + "）"
	}
	return head + "\n\n" + trunc(k.Content, kbReadMaxChars)
}
