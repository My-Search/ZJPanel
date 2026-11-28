package opsagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/llm"
	"zjpanel/backend/internal/mcp"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/notify"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

// AI 运维智能体控制面：会话在后端 goroutine 运行（前端只是视图），
// 页面刷新/关闭不影响执行；消息逐条落库并经 WS 主题 opsagent 推送。
//
// 工具层复用面板 MCP 注册表（含插件工具），按三级防御模式过滤/拦截处置类操作
// （模式是会话级属性：创建时指定、空闲可切换；事件会话用全局默认，巡检任务各自指定）：
//   observe 仅观察（默认）：工具表剔除处置工具，只能查询+通知+给建议
//   confirm 执行前确认  ：处置调用挂起等待用户确认后执行
//   auto    自动处置    ：直接执行（底层工具自带审计）
// 另有内建工具：长期记忆（跨会话）与 host_exec（SSH 执行，设置显式开启）。

const (
	wsTopic              = "opsagent"
	settingLLMKey        = "opsagent.llm"
	settingAgentKey      = "opsagent.config"
	maxConcurrent        = 5      // 并发 runner 上限
	maxCtxMessages       = 400    // 送入模型的最大消息条数（安全兜底；常规控制靠上下文压缩）
	defaultContextTokens = 300000 // 模型上下文窗口默认值（设置页可改）
	eventDedupeEvery     = 10 * time.Minute
	streamFlushWindow    = 100 * time.Millisecond // 流式增量合并窗口（delta 帧节流）
	streamPersistEvery   = 2 * time.Second        // 流式中途落库间隔（断线/重启后可恢复已生成部分）
)

type Service struct {
	db     *gorm.DB
	box    *pkg.SecretBox
	hub    *ws.Hub
	bus    *bus.Bus
	reg    *mcp.Registry
	notify *notify.Service
	llm    *llm.Client

	mu        sync.Mutex
	runs      map[uint]*sessionRun
	lastEvent sync.Map // 事件去重 key -> time.Time
	throttle  sync.Map // 通知节流 key -> time.Time

	sched          CronScheduler // 共享 cron（定时巡检），boot 注入
	inspectMu      sync.Mutex
	inspectEntries map[string]any // 巡检任务 ID -> cron entry
}

// sessionRun 一个运行中的会话执行器：cancel 用于手动停止，confirmCh 用于确认门控。
type sessionRun struct {
	cancel    context.CancelFunc
	confirmCh chan bool
	mu        sync.Mutex
	pending   *pendingAction
}

type pendingAction struct {
	CallID string
	RowID  uint
}

func New(db *gorm.DB, box *pkg.SecretBox, hub *ws.Hub, eventBus *bus.Bus, reg *mcp.Registry, notifySvc *notify.Service) *Service {
	return &Service{
		db: db, box: box, hub: hub, bus: eventBus, reg: reg, notify: notifySvc,
		llm: llm.NewClient(), runs: map[uint]*sessionRun{},
		inspectEntries: map[string]any{},
	}
}

// Start 启动：清理重启残留 + 订阅事件主题（按配置门控，改配置无需重订阅）。
func (s *Service) Start() {
	// 面板重启会杀掉 runner：残留 running/waiting_confirm 会话标记中断（可发消息续聊），
	// 残留的待确认工具行在下次构建上下文时按「未执行」补答案，保证协议完整。
	if err := s.db.Model(&model.OpsAgentSession{}).
		Where("status IN ?", []string{"running", "waiting_confirm"}).
		Updates(map[string]any{"status": "interrupted", "error": "面板重启，会话中断；发送消息可继续"}).Error; err != nil {
		slog.Warn("智能体会话中断标记失败", "err", err)
	}
	for _, e := range EventCatalog {
		topic := e.Topic
		s.bus.Subscribe(topic, func(payload map[string]any) { s.handleEvent(topic, payload) })
	}
}

// ===================== 配置 =====================

type llmSetting struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"` // SecretBox 密文，明文不落盘
	Model   string `json:"model"`
	// ContextTokens 模型上下文窗口（tokens）：用于估算上下文占用与压缩触发，默认 300k。
	ContextTokens int `json:"contextTokens"`
}

// AgentConfig 全局配置：事件订阅 + SSH 执行总开关 + 事件会话的防御模式。
// 防御模式是会话级属性（会话创建时确定、可在空闲时切换）：
//   - 手动会话：工作台创建时选择（SettingsAiTab 不再提供全局模式开关）
//   - 巡检会话：由巡检任务自带的 defenseMode 决定
//   - 事件会话：自动创建、无法逐个选择——普通事件沿用 DefenseMode，
//     攻击类事件（host.security/security.attack）时效性强，单独取 AttackMode 默认
type AgentConfig struct {
	DefenseMode string `json:"defenseMode"` // 普通事件会话默认防御模式：observe | confirm | auto
	// AttackMode 攻击事件会话默认防御模式（独立于 DefenseMode：攻击响应要求及时性，默认 auto）
	AttackMode   string          `json:"attackMode"`
	EventEnabled bool            `json:"eventEnabled"`
	Events       map[string]bool `json:"events"`
	AllowSSHExec bool            `json:"allowSSHExec"`
	// SessionRetentionDays 会话保留天数：到期会话由全局定时清理任务删除（含消息），0=永久（默认）。
	SessionRetentionDays int `json:"sessionRetentionDays"`
}

// 防御模式取值。
const (
	ModeObserve = "observe" // 仅观察：剔除/拦截处置类工具
	ModeConfirm = "confirm" // 执行前确认：处置类调用挂起等待人工放行
	ModeAuto    = "auto"    // 自动处置：直接执行（底层工具自带审计）
)

func validMode(mode string) bool {
	switch mode {
	case ModeObserve, ModeConfirm, ModeAuto:
		return true
	}
	return false
}

// resolveMode 归一化会话防御模式：显式合法值优先；空值回落到全局默认（事件会话默认）；
// 非法值按最保守的「仅观察」处理，避免畸形参数意外拿到更高授权。
func (s *Service) resolveMode(mode string) string {
	if validMode(mode) {
		return mode
	}
	if mode != "" {
		return ModeObserve
	}
	return s.AgentConfig().DefenseMode
}

func (s *Service) loadLLMSetting() llmSetting {
	var st llmSetting
	var rec model.Setting
	if err := s.db.Where("key = ?", settingLLMKey).First(&rec).Error; err == nil {
		_ = json.Unmarshal([]byte(rec.Value), &st)
	}
	if st.ContextTokens <= 0 {
		st.ContextTokens = defaultContextTokens
	}
	return st
}

// LLMDetail 解密后的模型配置（仅服务端使用）。
func (s *Service) LLMDetail() (llm.Config, error) {
	st := s.loadLLMSetting()
	plain := ""
	if st.APIKey != "" {
		if p, err := s.box.OpenCtx(pkg.CtxLLMApiKey, st.APIKey); err == nil {
			plain = p
		}
	}
	cfg := llm.Config{BaseURL: st.BaseURL, APIKey: plain, Model: st.Model}
	if err := cfg.Check(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LLMView 脱敏视图（前端回显：只给 baseURL/model 与 apiKeySet）。
func (s *Service) LLMView() map[string]any {
	st := s.loadLLMSetting()
	_, err := s.LLMDetail()
	return map[string]any{
		"baseURL": st.BaseURL, "model": st.Model, "apiKeySet": st.APIKey != "",
		"contextTokens": st.ContextTokens,
		"ready":         err == nil, "hint": readyHint(err),
	}
}

func readyHint(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// SaveLLM 保存模型配置；apiKey 明文为空 = 沿用旧密文（前端密码框不回显明文的惯例）。
func (s *Service) SaveLLM(baseURL, apiKeyPlain, modelName string, contextTokens int) error {
	baseURL, modelName = strings.TrimSpace(baseURL), strings.TrimSpace(modelName)
	if baseURL == "" || modelName == "" {
		return fmt.Errorf("BaseURL 与模型名必填")
	}
	st := s.loadLLMSetting()
	st.BaseURL, st.Model = baseURL, modelName
	if contextTokens > 0 {
		if contextTokens < 4096 {
			contextTokens = 4096
		}
		st.ContextTokens = contextTokens
	}
	if apiKeyPlain != "" {
		sealed, err := s.box.SealECtx(pkg.CtxLLMApiKey, apiKeyPlain)
		if err != nil {
			return err
		}
		st.APIKey = sealed
	}
	b, _ := json.Marshal(st)
	return s.db.Save(&model.Setting{Key: settingLLMKey, Value: string(b)}).Error
}

// AgentConfig 载入智能体配置（带默认值：事件会话仅观察 + 攻击事件自动处置 + 事件全关 + SSH 执行关闭）。
func (s *Service) AgentConfig() AgentConfig {
	cfg := AgentConfig{DefenseMode: ModeObserve, AttackMode: ModeAuto, Events: map[string]bool{}}
	var rec model.Setting
	if err := s.db.Where("key = ?", settingAgentKey).First(&rec).Error; err == nil {
		_ = json.Unmarshal([]byte(rec.Value), &cfg)
	}
	if !validMode(cfg.DefenseMode) {
		cfg.DefenseMode = ModeObserve
	}
	if !validMode(cfg.AttackMode) {
		cfg.AttackMode = ModeAuto
	}
	if cfg.Events == nil {
		cfg.Events = map[string]bool{}
	}
	return cfg
}

func (s *Service) SaveAgentConfig(cfg AgentConfig) error {
	if !validMode(cfg.DefenseMode) {
		return fmt.Errorf("非法防御模式: %s", cfg.DefenseMode)
	}
	if cfg.AttackMode == "" {
		// 未提供攻击事件模式（不感知该字段的客户端）→ 沿用现值：既不拒绝保存，也不静默重置
		cfg.AttackMode = s.AgentConfig().AttackMode
	} else if !validMode(cfg.AttackMode) {
		return fmt.Errorf("非法攻击事件防御模式: %s", cfg.AttackMode)
	}
	if cfg.Events == nil {
		cfg.Events = map[string]bool{}
	}
	b, _ := json.Marshal(cfg)
	return s.db.Save(&model.Setting{Key: settingAgentKey, Value: string(b)}).Error
}

// ===================== 会话 =====================

// ListSessions 分页列出会话，三组排序（组内按 updated_at 倒序）：
//  0 = 进行中（running/waiting_confirm，需要用户关注）
//  1 = 已结束未查看（终态且 viewed_at 为空或早于 updated_at，即"跑完还没看过"）
//  2 = 已结束已查看
// query 非空时按标题或消息内容模糊匹配（只搜标题经常搜不到目标会话）。
func (s *Service) ListSessions(limit, offset int, query string) ([]model.OpsAgentSession, int64) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	q := strings.TrimSpace(query)
	filter := func(tx *gorm.DB) *gorm.DB {
		if q != "" {
			like := "%" + q + "%"
			return tx.Where("title LIKE ? OR EXISTS (SELECT 1 FROM ops_agent_messages m WHERE m.session_id = ops_agent_sessions.id AND m.content LIKE ?)", like, like)
		}
		return tx
	}
	var total int64
	if err := s.db.Model(&model.OpsAgentSession{}).Scopes(filter).Count(&total).Error; err != nil {
		slog.Warn("会话总数统计失败", "err", err)
	}
	list := []model.OpsAgentSession{}
	_ = s.db.Scopes(filter).Order(sessionListOrder).Offset(offset).Limit(limit).Find(&list).Error
	return list, total
}

// sessionListOrder 三组排序的 ORDER BY（与 ListSessions 的注释保持一致）。
const sessionListOrder = `CASE WHEN status IN ('running','waiting_confirm') THEN 0
	WHEN viewed_at IS NULL OR viewed_at < updated_at THEN 1
	ELSE 2 END, updated_at DESC`

// MarkSessionViewed 标记会话已查看（前端打开会话、或正查看的会话收到终态帧时调用）。
// 用 UpdateColumn 只写 viewed_at：GORM 的 Update 会顺带把 updated_at 刷成稍晚的当前时间，
// 反而让 viewed_at < updated_at 永远成立、会话永远判为未读；且标记已读也不应改变会话在已读组内的位置。
func (s *Service) MarkSessionViewed(id uint) error {
	return s.db.Model(&model.OpsAgentSession{}).Where("id = ?", id).UpdateColumn("viewed_at", time.Now()).Error
}

func (s *Service) GetSession(id uint) (*model.OpsAgentSession, error) {
	var sess model.OpsAgentSession
	if err := s.db.First(&sess, id).Error; err != nil {
		return nil, fmt.Errorf("会话不存在")
	}
	return &sess, nil
}

// Messages afterID 之后的消息（前端增量补帧）。
func (s *Service) Messages(sessionID, afterID uint, limit int) []model.OpsAgentMessage {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []model.OpsAgentMessage
	q := s.db.Where("session_id = ?", sessionID)
	if afterID > 0 {
		q = q.Where("id > ?", afterID)
	}
	q.Order("id asc").Limit(limit).Find(&rows)
	return rows
}

// CreateSession 建会话并写入首条消息（manual 或 event），随后由调用方 startRunner。
// defenseMode 为会话级授权级别快照：非法/为空时回落到全局默认（事件会话默认）。
func (s *Service) CreateSession(title, source string, hostID *uint, firstRole, firstContent, defenseMode string) (*model.OpsAgentSession, error) {
	sess := model.OpsAgentSession{
		Title: trunc(title, 80), Source: source, Status: "idle",
		DefenseMode: s.resolveMode(defenseMode), HostID: hostID,
	}
	if err := s.db.Create(&sess).Error; err != nil {
		return nil, err
	}
	if firstContent != "" {
		s.addMessage(sess.ID, firstRole, firstContent, "", "", "")
	}
	return &sess, nil
}

// SetDefenseMode 切换会话防御模式（会话级；运行中/待确认不可切换，避免跑一半改授权级别）。
func (s *Service) SetDefenseMode(sessionID uint, mode string) error {
	if !validMode(mode) {
		return fmt.Errorf("非法防御模式: %s", mode)
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess.Status == "running" || sess.Status == "waiting_confirm" {
		return fmt.Errorf("会话正在运行，请停止后再切换防御模式")
	}
	if sess.DefenseMode == mode {
		return nil
	}
	if err := s.db.Model(sess).Update("defense_mode", mode).Error; err != nil {
		return err
	}
	s.audit("opsagent.session.mode", fmt.Sprintf("session=%d mode=%s", sessionID, mode))
	s.broadcast(map[string]any{"type": "mode", "sessionId": sessionID, "defenseMode": mode})
	return nil
}

// SendMessage 用户追加消息并唤醒循环（idle/interrupted/error 会话）。
func (s *Service) SendMessage(sessionID uint, content string) error {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess.Status == "running" {
		return fmt.Errorf("会话正在运行中")
	}
	if sess.Status == "waiting_confirm" {
		return fmt.Errorf("有操作等待确认，请先确认或拒绝")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("消息不能为空")
	}
	s.addMessage(sessionID, "user", content, "", "", "")
	if sess.Status == "interrupted" || sess.Status == "error" {
		s.db.Model(sess).Updates(map[string]any{"error": ""})
	}
	return s.StartRunner(sess)
}

// StartRunner 启动会话执行器（并发上限保护）。
func (s *Service) StartRunner(sess *model.OpsAgentSession) error {
	s.mu.Lock()
	if _, ok := s.runs[sess.ID]; ok {
		s.mu.Unlock()
		return fmt.Errorf("会话正在运行中")
	}
	if len(s.runs) >= maxConcurrent {
		s.mu.Unlock()
		return fmt.Errorf("并发会话已达上限（%d），请稍后再试", maxConcurrent)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &sessionRun{cancel: cancel, confirmCh: make(chan bool, 1)}
	s.runs[sess.ID] = run
	s.mu.Unlock()
	s.setStatus(sess.ID, "running", "")
	go s.runLoop(sess.ID, ctx, run)
	return nil
}

// Confirm 确认/拒绝待执行操作（confirm 模式挂起的会话）。
func (s *Service) Confirm(sessionID uint, approve bool) error {
	s.mu.Lock()
	run := s.runs[sessionID]
	s.mu.Unlock()
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess.Status != "waiting_confirm" {
		return fmt.Errorf("会话不在等待确认状态")
	}
	if run == nil {
		// 无 runner（面板重启残留）：把待确认行标为未执行，会话转中断
		s.finalizePendingBySession(sessionID, "（面板重启，该操作未执行）")
		s.setStatus(sessionID, "interrupted", "面板重启，会话中断；发送消息可继续")
		return nil
	}
	select {
	case run.confirmCh <- approve:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("确认通道异常，请重试或停止会话")
	}
}

// Stop 停止运行中的会话。
func (s *Service) Stop(sessionID uint) error {
	s.mu.Lock()
	run := s.runs[sessionID]
	s.mu.Unlock()
	if run == nil {
		return fmt.Errorf("会话不在运行中")
	}
	run.cancel()
	return nil
}

func (s *Service) DeleteSession(sessionID uint) error {
	s.mu.Lock()
	running := s.runs[sessionID] != nil
	s.mu.Unlock()
	if running {
		return fmt.Errorf("会话正在运行，请先停止")
	}
	s.db.Where("session_id = ?", sessionID).Delete(&model.OpsAgentMessage{})
	return s.db.Delete(&model.OpsAgentSession{}, sessionID).Error
}

// RunningCount 运行中/待确认的会话数（顶栏角标）。
func (s *Service) RunningCount() int64 {
	var n int64
	s.db.Model(&model.OpsAgentSession{}).Where("status IN ?", []string{"running", "waiting_confirm"}).Count(&n)
	return n
}

// SessionContext 会话上下文用量估算（工作台显示）。
// 口径与实际请求一致：按 buildContext 输出（压缩摘要已替代对应原始段）估算，limit 为窗口配置。
func (s *Service) SessionContext(id uint) (int, int) {
	limit := s.contextLimit()
	sess, err := s.GetSession(id)
	if err != nil {
		return 0, limit
	}
	hosts, domains, _ := s.panelContext()
	return estimateMessagesTokens(s.buildContext(sess, hosts, domains)), limit
}

// ===================== 执行循环 =====================

// runLoop agentic 主循环：模型 ↔ 工具，逐轮落库推送；confirm 模式下处置调用挂起等待确认。
func (s *Service) runLoop(id uint, ctx context.Context, run *sessionRun) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("智能体会话异常退出", "session", id, "panic", r)
			s.finish(id, "error", fmt.Sprintf("内部异常: %v", r))
		}
		s.mu.Lock()
		delete(s.runs, id)
		s.mu.Unlock()
	}()
	// 不设轮数上限：循环由自然条件终止（模型不再调用工具 / 手动停止 / 出错），
	// 上下文增长靠阈值压缩 + maxCtxMessages 兜底。
	for {
		if ctx.Err() != nil {
			s.finish(id, "interrupted", "已手动停止")
			return
		}
		sess, err := s.GetSession(id)
		if err != nil {
			return
		}
		cfg, err := s.LLMDetail()
		if err != nil {
			s.fail(id, err)
			return
		}
		hosts, siteDomains, _ := s.panelContext()
		messages := s.buildContext(sess, hosts, siteDomains)
		limit := s.contextLimit()

		// 阈值压缩：估算占用超过窗口 75% 时先把早期上下文压成摘要（失败安全，不丢数据）
		if est := estimateMessagesTokens(messages); limit > 0 && est*100 > limit*compactionTriggerPct {
			if ok, cerr := s.compactContext(sess, ctx, cfg, s.sessionRows(id)); ok {
				messages = s.buildContext(sess, hosts, siteDomains)
			} else if cerr != nil {
				slog.Warn("上下文压缩失败，保持原上下文继续", "session", id, "err", cerr)
			}
		}
		tools := toLLMTools(s.buildToolset(sess.DefenseMode, s.AgentConfig().AllowSSHExec))

		// 流式调用：正文/思考增量经 sink 合并广播（delta 帧），定稿在调用结束后统一落库
		sink := newStreamSink(s, id)
		resp, err := s.llm.ChatStream(ctx, cfg, messages, tools, sink.delta)
		if errors.Is(err, llm.ErrContextLength) {
			// 模型报上下文超限：强制压缩（更激进的压缩目标）后重试一次；仍超限才真正失败
			if ok, cerr := s.compactContext(sess, ctx, cfg, s.sessionRows(id)); ok {
				messages = s.buildContext(sess, hosts, siteDomains)
				sink = newStreamSink(s, id)
				resp, err = s.llm.ChatStream(ctx, cfg, messages, tools, sink.delta)
			} else if cerr != nil {
				err = fmt.Errorf("上下文超限且自动压缩失败（%v），请手动精简会话或调大模型上下文窗口", cerr)
			}
		}
		if err != nil {
			// 已流出的部分内容先定稿保留（无增量时为空操作），再走中断/失败收尾
			sink.finish("")
			if ctx.Err() != nil {
				s.finish(id, "interrupted", "已手动停止")
				return
			}
			s.fail(id, err)
			return
		}
		// 广播当前上下文用量（composer 实时显示）
		s.broadcast(map[string]any{"type": "context", "sessionId": id,
			"tokens": estimateMessagesTokens(messages), "limit": limit})

		// 助手消息（可能带工具调用请求）定稿推送：流式行整行替换；纯工具调用（无正文增量）时补建行
		meta := ""
		if len(resp.ToolCalls) > 0 {
			meta = metaToolCallsJSON(resp.ToolCalls)
		}
		sink.finish(meta)

		if len(resp.ToolCalls) == 0 {
			s.finish(id, "idle", "")
			return
		}
		for _, call := range resp.ToolCalls {
			if stop := s.execCall(id, sess, ctx, run, call); stop {
				return
			}
		}
	}
}

// sessionRows 会话的消息行（时间正序，尾部安全截断；压缩与估算共用）。
func (s *Service) sessionRows(id uint) []model.OpsAgentMessage {
	var rows []model.OpsAgentMessage
	s.db.Where("session_id = ?", id).Order("id desc").Limit(maxCtxMessages).Find(&rows)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// contextLimit 模型上下文窗口（tokens），未配置时默认 300k。
func (s *Service) contextLimit() int {
	limit := s.loadLLMSetting().ContextTokens
	if limit <= 0 {
		return defaultContextTokens
	}
	return limit
}

// execCall 执行一次工具调用（含 confirm 门控）。返回 true 表示会话已终止（停止/异常）。
func (s *Service) execCall(id uint, sess *model.OpsAgentSession, ctx context.Context, run *sessionRun, call llm.ToolCall) bool {
	t := lookupTool(s, call.Name)
	if t == nil {
		s.addMessage(id, "tool", "工具不存在: "+call.Name, metaToolJSON(call.ID), call.Name, call.Arguments)
		return false
	}
	// confirm 模式：处置类操作挂起等待确认
	if needsConfirm(sess.DefenseMode, *t) {
		row := s.addMessage(id, "tool", "等待用户确认该操作…", metaPendingJSON(call.ID), call.Name, call.Arguments)
		s.setStatus(id, "waiting_confirm", "")
		run.mu.Lock()
		run.pending = &pendingAction{CallID: call.ID, RowID: row.ID}
		run.mu.Unlock()
		select {
		case approve := <-run.confirmCh:
			run.mu.Lock()
			run.pending = nil
			run.mu.Unlock()
			if !approve {
				s.updateMessage(row.ID, "用户拒绝了该操作，未执行。", metaToolJSON(call.ID))
				return false
			}
		case <-ctx.Done():
			s.updateMessage(row.ID, "（会话已停止，该操作未执行）", metaToolJSON(call.ID))
			s.finish(id, "interrupted", "已手动停止")
			return true
		}
		s.setStatus(id, "running", "")
	}
	result := s.dispatch(sess, call)
	s.addMessage(id, "tool", result, metaToolJSON(call.ID), call.Name, call.Arguments)
	return false
}

// fail 模型调用失败：助手消息留痕 + 会话置 error。
func (s *Service) fail(id uint, err error) {
	slog.Warn("智能体会话失败", "session", id, "err", err)
	s.addMessage(id, "assistant", "⚠ 调用模型失败："+err.Error(), "", "", "")
	s.finish(id, "error", err.Error())
}

// finish 终态转移并广播。
func (s *Service) finish(id uint, status, errMsg string) {
	s.db.Model(&model.OpsAgentSession{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "error": errMsg})
	s.broadcast(map[string]any{"type": "status", "sessionId": id, "status": status, "error": errMsg})
}

func (s *Service) setStatus(id uint, status, errMsg string) {
	updates := map[string]any{"status": status}
	if errMsg == "" {
		updates["error"] = ""
	} else {
		updates["error"] = errMsg
	}
	s.db.Model(&model.OpsAgentSession{}).Where("id = ?", id).Updates(updates)
	s.broadcast(map[string]any{"type": "status", "sessionId": id, "status": status, "error": errMsg})
}

// ===================== 消息持久化 =====================

func (s *Service) addMessage(sessionID uint, role, content, meta, toolName, toolArgs string) *model.OpsAgentMessage {
	return s.addMessageFull(sessionID, role, content, "", meta, toolName, toolArgs)
}

func (s *Service) addMessageFull(sessionID uint, role, content, reasoning, meta, toolName, toolArgs string) *model.OpsAgentMessage {
	row := model.OpsAgentMessage{
		SessionID: sessionID, Role: role, Content: content, Reasoning: reasoning, Meta: meta,
		ToolName: trunc(toolName, 128), ToolArgs: trunc(toolArgs, 2000),
	}
	if err := s.db.Create(&row).Error; err != nil {
		slog.Error("智能体会话消息写入失败", "session", sessionID, "err", err)
		return &row
	}
	s.broadcast(map[string]any{"type": "message", "sessionId": sessionID, "message": row})
	return &row
}

func (s *Service) updateMessage(id uint, content, meta string) {
	s.db.Model(&model.OpsAgentMessage{}).Where("id = ?", id).Updates(map[string]any{"content": content, "meta": meta})
	var row model.OpsAgentMessage
	if err := s.db.First(&row, id).Error; err == nil {
		s.broadcast(map[string]any{"type": "message", "sessionId": row.SessionID, "message": row})
	}
}

// updateAssistant 定稿流式助手行：完整正文/推理/工具调用 meta 一次性落库并广播整行替换。
func (s *Service) updateAssistant(id uint, content, reasoning, meta string) {
	s.db.Model(&model.OpsAgentMessage{}).Where("id = ?", id).
		Updates(map[string]any{"content": content, "reasoning": reasoning, "meta": meta})
	var row model.OpsAgentMessage
	if err := s.db.First(&row, id).Error; err == nil {
		s.broadcast(map[string]any{"type": "message", "sessionId": row.SessionID, "message": row})
	}
}

// streamSink 一轮流式模型响应的落库/广播聚合器（每个 LLM 轮一个）。
// 首个增量到达时才建 assistant 行（此前失败不留垃圾行），正文首段随行落库保证中途刷新可见；
// 增量按 streamFlushWindow 合并成 WS delta 帧广播，并按 streamPersistEvery 静默落库；
// finish 定稿：完整 content/reasoning/meta 一次性落库并广播 message 帧（整行替换语义）。
type streamSink struct {
	svc         *Service
	sessionID   uint
	mu          sync.Mutex
	rowID       uint
	content     strings.Builder
	reasoning   strings.Builder
	pendC       strings.Builder
	pendR       strings.Builder
	timer       *time.Timer
	lastPersist time.Time
}

func newStreamSink(svc *Service, sessionID uint) *streamSink {
	return &streamSink{svc: svc, sessionID: sessionID, lastPersist: time.Now()}
}

// delta 实现 llm.ChatStream 的增量回调；返回 nil 表示继续流式。
func (k *streamSink) delta(d llm.StreamDelta) error {
	kind, text := d.Kind, d.Text
	if text == "" {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.rowID == 0 {
		// 首个增量：建行（meta 带 streaming 标记供前端识别流式行）
		if kind == "content" {
			k.content.WriteString(text)
			if row := k.svc.addMessage(k.sessionID, "assistant", text, metaStreamingJSON(), "", ""); row.ID > 0 {
				k.rowID = row.ID
			}
			return nil
		}
		k.reasoning.WriteString(text)
		k.pendR.WriteString(text)
		if row := k.svc.addMessage(k.sessionID, "assistant", "", metaStreamingJSON(), "", ""); row.ID > 0 {
			k.rowID = row.ID
			k.timer = time.AfterFunc(streamFlushWindow, k.flush)
		}
		return nil
	}
	if kind == "content" {
		k.content.WriteString(text)
		k.pendC.WriteString(text)
	} else {
		k.reasoning.WriteString(text)
		k.pendR.WriteString(text)
	}
	if k.timer == nil {
		k.timer = time.AfterFunc(streamFlushWindow, k.flush)
	}
	return nil
}

// flush 把缓冲中的增量广播为 delta 帧；顺带按间隔静默落库（重启/断线后可恢复已生成部分）。
func (k *streamSink) flush() {
	k.mu.Lock()
	c, r := k.pendC.String(), k.pendR.String()
	k.pendC.Reset()
	k.pendR.Reset()
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	rowID := k.rowID
	content := k.content.String()
	reasoning := k.reasoning.String()
	persist := rowID > 0 && time.Since(k.lastPersist) >= streamPersistEvery && (c != "" || r != "")
	if persist {
		k.lastPersist = time.Now()
	}
	k.mu.Unlock()
	if rowID == 0 {
		return
	}
	if r != "" {
		k.svc.broadcast(map[string]any{"type": "delta", "sessionId": k.sessionID, "messageId": rowID, "path": "reasoning", "append": r})
	}
	if c != "" {
		k.svc.broadcast(map[string]any{"type": "delta", "sessionId": k.sessionID, "messageId": rowID, "path": "content", "append": c})
	}
	if persist {
		k.svc.db.Model(&model.OpsAgentMessage{}).Where("id = ?", rowID).
			Updates(map[string]any{"content": content, "reasoning": reasoning})
	}
}

// finish 定稿本轮流式行。rowID 为 0（未收到任何增量）时：有 meta（纯工具调用）或正文则补建行，否则空操作。
func (k *streamSink) finish(meta string) {
	k.flush()
	k.mu.Lock()
	rowID := k.rowID
	content := k.content.String()
	reasoning := k.reasoning.String()
	k.mu.Unlock()
	if rowID == 0 {
		if content != "" || meta != "" {
			k.svc.addMessageFull(k.sessionID, "assistant", content, reasoning, meta, "", "")
		}
		return
	}
	k.svc.updateAssistant(rowID, content, reasoning, meta)
}

func metaStreamingJSON() string { return `{"streaming":true}` }

// finalizePendingBySession 把会话中残留的 pending 工具行标为未执行（面板重启兜底）。
func (s *Service) finalizePendingBySession(sessionID uint, note string) {
	var rows []model.OpsAgentMessage
	s.db.Where("session_id = ? AND meta LIKE ?", sessionID, `%"pending":true%`).Find(&rows)
	for _, r := range rows {
		callID := metaString(r.Meta, "callId")
		s.updateMessage(r.ID, note, metaToolJSON(callID))
	}
}

func metaToolCallsJSON(calls []llm.ToolCall) string {
	b, _ := json.Marshal(map[string]any{"toolCalls": calls})
	return string(b)
}

// metaToolCalls 解析 assistant 行 Meta 里的工具调用列表。
func metaToolCalls(meta string) []llm.ToolCall {
	var m struct {
		ToolCalls []llm.ToolCall `json:"toolCalls"`
	}
	if json.Unmarshal([]byte(meta), &m) != nil {
		return nil
	}
	return m.ToolCalls
}

// metaInt 读 Meta 里的数值字段（json 数字 → float64）。
func metaInt(meta, key string) uint {
	var m map[string]any
	if json.Unmarshal([]byte(meta), &m) != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return uint(v)
	}
	return 0
}

func metaToolJSON(callID string) string {
	m, _ := json.Marshal(map[string]any{"callId": callID})
	return string(m)
}
func metaPendingJSON(callID string) string {
	m, _ := json.Marshal(map[string]any{"callId": callID, "pending": true})
	return string(m)
}

func metaString(meta, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(meta), &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// buildContext 从 DB 行重建模型对话上下文。
// 压缩是前缀语义：最新一条 summary 行覆盖其之前的全部消息（含更早的摘要），
// 原始行保留在库中不删除，仅不进上下文；pending 工具行补「未执行」答案保证协议完整。
func (s *Service) buildContext(sess *model.OpsAgentSession, hosts []model.SshHost, siteDomains []string) []llm.Message {
	var rows []model.OpsAgentMessage
	s.db.Where("session_id = ?", sess.ID).Order("id desc").Limit(maxCtxMessages).Find(&rows)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 { // 反转为时间正序
		rows[i], rows[j] = rows[j], rows[i]
	}
	// 预扫描：最新摘要的覆盖上界（prefix 压缩 ⇒ 覆盖其之前的全部行）
	lastSummaryIdx, maxToId := -1, uint(0)
	for i := range rows {
		if rows[i].Role == "summary" {
			if to := metaInt(rows[i].Meta, "toId"); to > maxToId {
				maxToId = to
			}
			lastSummaryIdx = i
		}
	}
	memories := s.relatedMemories(sess)
	out := []llm.Message{{Role: "system", Content: buildSystemPrompt(sess, hosts, siteDomains, memories, s.persistentKnowledge())}}
	for i := range rows {
		r := &rows[i]
		if r.ID <= maxToId && i != lastSummaryIdx {
			continue // 被最新摘要覆盖的原始行（含被吸收的更早摘要）
		}
		switch r.Role {
		case "summary":
			out = append(out, llm.Message{Role: "system", Content: fmt.Sprintf("【早期上下文摘要】以下摘自本会话更早的 %d 条消息（原始记录已归档）：\n%s", metaInt(r.Meta, "count"), r.Content)})
		case "user", "event":
			out = append(out, llm.Message{Role: "user", Content: r.Content})
		case "assistant":
			m := llm.Message{Role: "assistant", Content: r.Content}
			var meta struct {
				ToolCalls []llm.ToolCall `json:"toolCalls"`
			}
			if json.Unmarshal([]byte(r.Meta), &meta) == nil {
				m.ToolCalls = meta.ToolCalls
			}
			out = append(out, m)
		case "tool":
			content := r.Content
			if strings.Contains(r.Meta, `"pending":true`) {
				content = "（面板重启中断，该操作未执行）"
			}
			out = append(out, llm.Message{Role: "tool", ToolCallID: metaString(r.Meta, "callId"), Content: content})
		}
	}
	return out
}

// relatedMemories 注入系统提示的记忆：全局 + 会话绑定主机作用域，最多 20 条。
func (s *Service) relatedMemories(sess *model.OpsAgentSession) []model.OpsAgentMemory {
	ref := ""
	if sess.HostID != nil {
		ref = strconv.FormatUint(uint64(*sess.HostID), 10)
	}
	var list []model.OpsAgentMemory
	s.db.Where("scope = ? OR (scope = ? AND ref_id = ?)", "global", "host", ref).
		Order("updated_at desc").Limit(20).Find(&list)
	return list
}

// panelContext 面板上下文数据（主机清单 + 站点域名清单）。
func (s *Service) panelContext() ([]model.SshHost, []string, int64) {
	var hosts []model.SshHost
	s.db.Order("local desc, id").Limit(30).Find(&hosts)
	var domains []string
	var total int64
	s.db.Model(&model.Site{}).Count(&total)
	s.db.Model(&model.Site{}).Order("id").Limit(30).Pluck("domain", &domains)
	return hosts, domains, total
}

// ===================== 事件触发 =====================

// handleEvent 事件主题回调：按配置门控 + 去重 + 并发上限，命中即建会话并启动分析。
func (s *Service) handleEvent(topic string, payload map[string]any) {
	cfg := s.AgentConfig()
	if !cfg.EventEnabled || !cfg.Events[topic] {
		return
	}
	if _, err := s.LLMDetail(); err != nil {
		return // 未配置模型：静默跳过（通知体系照常工作）
	}
	key := topic + "|" + eventKey(payload)
	if v, ok := s.lastEvent.Load(key); ok {
		if time.Since(v.(time.Time)) < eventDedupeEvery {
			return
		}
	}
	s.lastEvent.Store(key, time.Now())
	if s.RunningCount() >= int64(maxConcurrent) {
		if s.notify != nil {
			s.notify.Send("opsagent", notify.Input{Type: "opsagent", Level: "warn",
				Title: "事件触发被限流", Body: fmt.Sprintf("事件 %s 触发时并发会话已达上限（%d），未创建分析会话。", topic, maxConcurrent)})
		}
		return
	}
	// 攻击类事件时效性强，防御模式单独取 AttackMode（默认 auto）；其余事件沿用 DefenseMode
	mode := cfg.DefenseMode
	if IsSecurityTopic(topic) {
		mode = cfg.AttackMode
	}
	// 攻击事件载荷带 hostId：把会话绑定到对应主机（AI 的 host_* 操作默认作用于它）
	var hostID *uint
	if hid, ok := payload["hostId"].(float64); ok && hid > 0 {
		id := uint(hid)
		hostID = &id
	}
	sess, err := s.CreateSession(eventTitle(topic, payload), "event:"+topic, hostID, "event", renderEvent(topic, payload), mode)
	if err != nil {
		slog.Error("事件触发会话创建失败", "topic", topic, "err", err)
		return
	}
	s.broadcast(map[string]any{"type": "session", "session": *sess})
	if err := s.StartRunner(sess); err != nil {
		s.finish(sess.ID, "error", err.Error())
	}
}

// MarkError 无 runner 可启动时的错误落库（如并发上限），供 HTTP 层调用。
func (s *Service) MarkError(sessionID uint, msg string) {
	s.finish(sessionID, "error", msg)
}

// TestLLM 模型连通性测试（设置页「测试连通」按钮）。
func (s *Service) TestLLM(ctx context.Context, cfg llm.Config) error {
	return s.llm.Ping(ctx, cfg)
}

// ===================== 记忆管理（API 用） =====================

func (s *Service) ListMemories(query string, limit int) []model.OpsAgentMemory {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	tx := s.db.Model(&model.OpsAgentMemory{})
	if q := strings.TrimSpace(query); q != "" {
		like := "%" + q + "%"
		tx = tx.Where("key LIKE ? OR content LIKE ?", like, like)
	}
	var list []model.OpsAgentMemory
	tx.Order("updated_at desc").Limit(limit).Find(&list)
	return list
}

func (s *Service) DeleteMemory(id uint) error {
	return s.db.Delete(&model.OpsAgentMemory{}, id).Error
}

// ===================== WS 广播 =====================

func (s *Service) broadcast(v map[string]any) {
	if s.hub == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.hub.Broadcast(wsTopic, b)
}
