package opsagent

import (
	"testing"
	"time"

	"zjpanel/backend/internal/model"
)

func saveRetentionDays(t *testing.T, s *Service, days int) {
	t.Helper()
	cfg := AgentConfig{DefenseMode: ModeObserve, Events: map[string]bool{}, SessionRetentionDays: days}
	if err := s.SaveAgentConfig(cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
}

// mkWithMessage 建一个带消息的会话（验证清理时消息级联删除）。
func mkWithMessage(t *testing.T, s *Service, title, status string) *model.OpsAgentSession {
	t.Helper()
	sess := mustCreateSession(t, s, title, status, nil)
	if err := s.db.Create(&model.OpsAgentMessage{SessionID: sess.ID, Role: "user", Content: title}).Error; err != nil {
		t.Fatalf("创建消息失败: %v", err)
	}
	return sess
}

func countSessions(t *testing.T, s *Service, id uint) int64 {
	t.Helper()
	var cnt int64
	if err := s.db.Model(&model.OpsAgentSession{}).Where("id = ?", id).Count(&cnt).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	return cnt
}

func countMessages(t *testing.T, s *Service, sessionID uint) int64 {
	t.Helper()
	var cnt int64
	if err := s.db.Model(&model.OpsAgentMessage{}).Where("session_id = ?", sessionID).Count(&cnt).Error; err != nil {
		t.Fatalf("查消息失败: %v", err)
	}
	return cnt
}

// 保留期清理：到期终态会话连同消息删除；到期但活跃（running/waiting_confirm）跳过；未到期跳过。
func TestSessionCleanerRetention(t *testing.T) {
	s := &Service{db: testDB(t)}
	saveRetentionDays(t, s, 7)
	base := time.Now()

	expired := mkWithMessage(t, s, "过期完成", "idle")
	expiredErr := mkWithMessage(t, s, "过期出错", "error")
	expiredRunning := mkWithMessage(t, s, "过期但还在跑", "running")
	expiredWait := mkWithMessage(t, s, "过期待确认", "waiting_confirm")
	recent := mkWithMessage(t, s, "未过期", "idle")
	stamp(t, s, expired.ID, base.Add(-10*24*time.Hour))
	stamp(t, s, expiredErr.ID, base.Add(-8*24*time.Hour))
	stamp(t, s, expiredRunning.ID, base.Add(-10*24*time.Hour))
	stamp(t, s, expiredWait.ID, base.Add(-9*24*time.Hour))
	stamp(t, s, recent.ID, base.Add(-2*24*time.Hour))

	n, err := s.SessionCleaner().Cleanup(time.Now())
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("清理条数 = %d, 期望 2", n)
	}
	if countSessions(t, s, expired.ID) != 0 || countMessages(t, s, expired.ID) != 0 {
		t.Fatalf("到期会话 %s 应连同消息删除", expired.Title)
	}
	if countSessions(t, s, expiredErr.ID) != 0 {
		t.Fatalf("到期出错会话应删除")
	}
	for _, sess := range []*model.OpsAgentSession{expiredRunning, expiredWait, recent} {
		if countSessions(t, s, sess.ID) != 1 {
			t.Fatalf("会话 %s 应保留", sess.Title)
		}
	}
}

// 保留 0 天 = 永久，不清理任何会话。
func TestSessionCleanerForever(t *testing.T) {
	s := &Service{db: testDB(t)}
	saveRetentionDays(t, s, 0)
	old := mkWithMessage(t, s, "很老的会话", "idle")
	stamp(t, s, old.ID, time.Now().Add(-365*24*time.Hour))
	n, err := s.SessionCleaner().Cleanup(time.Now())
	if err != nil || n != 0 {
		t.Fatalf("0 天应不清理, n=%d err=%v", n, err)
	}
	if countSessions(t, s, old.ID) != 1 {
		t.Fatalf("永久保留模式不应删除会话")
	}
}
