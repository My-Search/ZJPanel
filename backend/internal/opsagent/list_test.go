package opsagent

import (
	"testing"
	"time"

	"zjpanel/backend/internal/model"
)

func mustCreateSession(t *testing.T, s *Service, title, status string, viewed *time.Time) *model.OpsAgentSession {
	t.Helper()
	sess := &model.OpsAgentSession{Title: title, Status: status, ViewedAt: viewed}
	if err := s.db.Create(sess).Error; err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return sess
}

// stamp / setViewed 用裸 SQL 改 updated_at / viewed_at（绕开 GORM 自动维护），精确控制时间线。
func stamp(t *testing.T, s *Service, id uint, at time.Time) {
	t.Helper()
	if err := s.db.Exec("UPDATE ops_agent_sessions SET updated_at = ? WHERE id = ?", at, id).Error; err != nil {
		t.Fatalf("改 updated_at 失败: %v", err)
	}
}

func setViewed(t *testing.T, s *Service, id uint, at time.Time) {
	t.Helper()
	if err := s.db.Exec("UPDATE ops_agent_sessions SET viewed_at = ? WHERE id = ?", at, id).Error; err != nil {
		t.Fatalf("改 viewed_at 失败: %v", err)
	}
}

func ids(list []model.OpsAgentSession) []uint {
	out := make([]uint, 0, len(list))
	for _, it := range list {
		out = append(out, it.ID)
	}
	return out
}

func assertOrder(t *testing.T, want []uint, got []uint) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("长度 = %d, 期望 %d（got=%v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 位 = %d, 期望 %d（完整顺序 %v）", i, got[i], want[i], got)
		}
	}
}

// 三组排序：进行中（running/waiting_confirm）→ 已结束未查看 → 已结束已查看，组内按 updated_at 倒序。
func TestListSessionsGroupOrder(t *testing.T) {
	s := &Service{db: testDB(t)}
	base := time.Now().Add(-24 * time.Hour)

	a := mustCreateSession(t, s, "排查运行中", "running", nil)                     // 组0
	b := mustCreateSession(t, s, "待确认", "waiting_confirm", nil)                 // 组0
	c := mustCreateSession(t, s, "完成没看过", "idle", nil)                        // 组1（从未打开）
	d := mustCreateSession(t, s, "出错没看过", "error", ptrTime(base))             // 组1（看完之后又出错）
	f := mustCreateSession(t, s, "中断没看过", "interrupted", nil)                  // 组1
	e := mustCreateSession(t, s, "完成已看", "idle", nil)                         // 组2
	g := mustCreateSession(t, s, "较早已看", "idle", nil)                         // 组2

	stamp(t, s, a.ID, base.Add(10*time.Hour))
	stamp(t, s, b.ID, base.Add(9*time.Hour))
	stamp(t, s, c.ID, base.Add(8*time.Hour))
	stamp(t, s, d.ID, base.Add(7*time.Hour)) // viewed(base) < updated(base+7h) → 未读
	stamp(t, s, f.ID, base.Add(5*time.Hour))
	stamp(t, s, e.ID, base.Add(6*time.Hour))
	setViewed(t, s, e.ID, base.Add(7*time.Hour)) // viewed > updated → 已读
	stamp(t, s, g.ID, base.Add(4*time.Hour))
	setViewed(t, s, g.ID, base.Add(5*time.Hour))

	items, total := s.ListSessions(20, 0, "")
	if total != 7 {
		t.Fatalf("total = %d, 期望 7", total)
	}
	// 组内倒序：组0 a,b；组1 c,d,f；组2 e,g
	assertOrder(t, []uint{a.ID, b.ID, c.ID, d.ID, f.ID, e.ID, g.ID}, ids(items))
}

// MarkSessionViewed 之后会话从「未查看」组移入「已查看」组。
func TestMarkSessionViewed(t *testing.T) {
	s := &Service{db: testDB(t)}
	base := time.Now().Add(-24 * time.Hour)

	r := mustCreateSession(t, s, "还在跑", "running", nil)
	a := mustCreateSession(t, s, "完成后未看", "idle", nil)
	b := mustCreateSession(t, s, "已看过", "idle", nil)
	stamp(t, s, r.ID, base.Add(4*time.Hour))
	stamp(t, s, a.ID, base.Add(1*time.Hour))
	stamp(t, s, b.ID, base.Add(2*time.Hour))
	setViewed(t, s, b.ID, base.Add(3*time.Hour))

	items, _ := s.ListSessions(20, 0, "")
	assertOrder(t, []uint{r.ID, a.ID, b.ID}, ids(items))

	if err := s.MarkSessionViewed(a.ID); err != nil {
		t.Fatalf("标记已查看失败: %v", err)
	}
	// 标记后 a 移入已读组（updated_at 不被触碰，组内仍按原 updated_at 排序 → 落在 b 之后）
	items, _ = s.ListSessions(20, 0, "")
	assertOrder(t, []uint{r.ID, b.ID, a.ID}, ids(items))
	var reloaded model.OpsAgentSession
	if err := s.db.First(&reloaded, a.ID).Error; err != nil || reloaded.ViewedAt == nil {
		t.Fatalf("标记后 viewed_at 应写入 (err=%v)", err)
	}
}

// 关键词搜索：命中标题或消息内容；total 为命中总数；分页切片正确。
func TestListSessionsSearchAndPaging(t *testing.T) {
	s := &Service{db: testDB(t)}
	a := mustCreateSession(t, s, "Webshell 扫描", "idle", nil)
	b := mustCreateSession(t, s, "日常巡检", "idle", nil)
	c := mustCreateSession(t, s, "事件分析", "idle", nil)
	// b 的标题不含关键词，但消息内容命中 → 也应被搜出
	if err := s.db.Create(&model.OpsAgentMessage{SessionID: b.ID, Role: "user", Content: "分析一下最近的 webshell 告警"}).Error; err != nil {
		t.Fatalf("创建消息失败: %v", err)
	}

	items, total := s.ListSessions(20, 0, "Webshell")
	if total != 2 {
		t.Fatalf("total = %d, 期望 2", total)
	}
	hit := map[uint]bool{}
	for _, it := range items {
		hit[it.ID] = true
	}
	if !hit[a.ID] || !hit[b.ID] {
		t.Fatalf("应命中标题会话 %d 与消息内容会话 %d, 实际 %v", a.ID, b.ID, hit)
	}

	if _, total = s.ListSessions(20, 0, "不存在关键词"); total != 0 {
		t.Fatalf("total = %d, 期望 0", total)
	}

	// 分页：3 条会话按 updated_at 倒序 a,b,c，limit=2 offset=1 → b,c
	for i, sess := range []*model.OpsAgentSession{a, b, c} {
		stamp(t, s, sess.ID, time.Now().Add(time.Duration(-i)*time.Hour))
	}
	items, total = s.ListSessions(2, 1, "")
	if total != 3 || len(items) != 2 {
		t.Fatalf("total=%d len=%d, 期望 3/2", total, len(items))
	}
	assertOrder(t, []uint{b.ID, c.ID}, ids(items))
}

func ptrTime(v time.Time) *time.Time { return &v }
