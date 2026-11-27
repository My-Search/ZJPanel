package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"zjpanel/backend/internal/model"
)

// AuditCommands 主机防御检查类型 → Agent 指令。
var AuditCommands = map[string]string{
	"ssh":         "ssh_audit",
	"accounts":    "accounts_audit",
	"persistence": "persistence_audit",
	"exposure":    "exposure_audit",
	"webshell":    "webshell_audit",
}

// AuditKinds 稳定的检查顺序：定时任务和「全部检查」都按这个顺序串行，避免同时扫盘。
var AuditKinds = []string{"ssh", "accounts", "persistence", "exposure", "webshell"}

// CheckState 一次防御检查的持久化视图；Result 为 Agent 返回的 JSON。
type CheckState struct {
	Kind      string    `json:"kind"`
	CheckedAt time.Time `json:"checkedAt"`
	OK        bool      `json:"ok"`
	Result    any       `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Audit 执行一次防御检查并落库。成功/失败都保存，页面刷新后仍能看到
// 上一次检查的时间与失败原因，而不是退回「未检查」。
func (s *Service) Audit(hostID uint, kind string) (any, error) {
	cmd, ok := AuditCommands[kind]
	if !ok {
		return nil, fmt.Errorf("未知检查类型: %s", kind)
	}
	res, err := s.Exec(hostID, cmd, nil, 45*time.Second)
	if err != nil {
		s.saveCheck(hostID, kind, false, "", err.Error())
		return nil, err
	}
	if !res.OK {
		msg := res.Error
		if msg == "" {
			msg = res.Output
		}
		if msg == "" {
			msg = "Agent 返回失败"
		}
		s.saveCheck(hostID, kind, false, "", msg)
		return nil, fmt.Errorf("%s", msg)
	}

	var payload any
	if err := json.Unmarshal([]byte(res.Output), &payload); err != nil {
		// 审计输出理论上都是 JSON；极端情况下保留原始文本，避免结果丢失。
		payload = map[string]any{"output": res.Output}
	}
	s.saveCheck(hostID, kind, true, res.Output, "")
	return payload, nil
}

func (s *Service) saveCheck(hostID uint, kind string, ok bool, result, errMsg string) {
	rec := model.HostCheck{HostID: hostID, Kind: kind}
	upd := map[string]any{
		"checked_at": time.Now(), "ok": ok, "result": result, "error": errMsg,
	}
	if err := s.db.Where("host_id = ? AND kind = ?", hostID, kind).
		Assign(upd).FirstOrCreate(&rec).Error; err != nil {
		slog.Warn("主机防御检查结果保存失败", "host", hostID, "kind", kind, "err", err)
	}
}

// LatestChecks 最近一次各类型检查结果，按 kind 索引。
func (s *Service) LatestChecks(hostID uint) map[string]CheckState {
	var rows []model.HostCheck
	s.db.Where("host_id = ?", hostID).Find(&rows)
	out := make(map[string]CheckState, len(rows))
	for _, r := range rows {
		var payload any
		if r.Result != "" {
			_ = json.Unmarshal([]byte(r.Result), &payload)
		}
		out[r.Kind] = CheckState{
			Kind: r.Kind, CheckedAt: r.CheckedAt, OK: r.OK,
			Result: payload, Error: r.Error,
		}
	}
	return out
}

// StartAuditScheduler 定时对所有在线主机跑五类只读检查。
// 首次延迟 5 分钟，给面板重启后的 Agent 回连留时间；之后按 every 循环。
func (s *Service) StartAuditScheduler(every time.Duration) {
	if every <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		<-timer.C
		for {
			s.auditAllOnline()
			time.Sleep(every)
		}
	}()
}

func (s *Service) auditAllOnline() {
	var hosts []model.SshHost
	if err := s.db.Where("local = ?", false).Find(&hosts).Error; err != nil {
		slog.Warn("定时主机防御检查读取主机失败", "err", err)
		return
	}
	ran := 0
	for _, h := range hosts {
		if !s.Online(h.ID) {
			continue
		}
		ran++
		for _, kind := range AuditKinds {
			if _, err := s.Audit(h.ID, kind); err != nil {
				slog.Warn("定时主机防御检查失败", "host", h.ID, "kind", kind, "err", err)
			}
		}
	}
	slog.Info("定时主机防御检查完成", "hosts", ran)
}
