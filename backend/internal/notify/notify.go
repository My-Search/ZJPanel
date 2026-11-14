// Package notify 统一通知服务：站内信（DB 持久化）+ WS 实时广播 + 按消息类型订阅的邮件发送。
// 生产者两类：① 面板事件总线主题（Start 中订阅并渲染标题/级别）；
// ② 核心模块与插件直接调用 Send（插件经 SDK 的 ctx.Notify）。
// 渠道：站内信必发；邮件按 notify.subs 订阅开关发给单一收件人；webhook 渠道由 notify-webhook 插件独立承担。
package notify

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

// model.Setting 中的键（全局 KV 表）
const (
	keySMTPHost      = "notify.smtp.host"
	keySMTPPort      = "notify.smtp.port"
	keySMTPUser      = "notify.smtp.user"
	keySMTPPass      = "notify.smtp.pass"      // SecretBox 密文
	keySMTPFromAddr  = "notify.smtp.from_addr" // 发件信封地址（留空回落 SMTP 用户）
	keySMTPFromAlias = "notify.smtp.alias"     // 发件人显示别名（默认 ZJPanel）
	keySMTPFrom      = "notify.smtp.from"      // 旧版键：兼容迁移（邮箱→地址，非邮箱→别名）
	keyRcpt          = "notify.rcpt"           // 订阅收件邮箱
	keySubs          = "notify.subs"           // JSON: map[type]bool，邮件订阅开关
)

// BuiltinTypes 内置消息类型（订阅设置 UI 展示；插件可自定义类型，出现后由 DISTINCT 补充进列表）。
// 注：插件启停事件不入站内信（由管理员自己触发，噪音大），事件仍经 bus 供 notify-webhook 插件订阅。
var BuiltinTypes = []string{
	"cert.issued", "cert.expiring", "cert.failed",
	"sitecheck.down", "sitecheck.up",
	"cron.failed", "panel.update",
}

// Input 一次通知的内容；Level 为 info|warn|error（空取 info）。
type Input struct {
	Type  string
	Level string
	Title string
	Body  string
}

type Service struct {
	db  *gorm.DB
	box *pkg.SecretBox
	hub *ws.Hub
	bus *bus.Bus

	seen sync.Map // 防抖：type|title -> 上次发送时间（仅 bus 自动事件）
}

func New(db *gorm.DB, box *pkg.SecretBox, hub *ws.Hub, eventBus *bus.Bus) *Service {
	return &Service{db: db, box: box, hub: hub, bus: eventBus}
}

// Send 记录站内信 → WS 广播 → 按订阅发邮件（异步，失败只记日志）。
// 邮件配置每次现读，改设置即时生效，无需重启。
func (s *Service) Send(source string, in Input) {
	if in.Type == "" || in.Title == "" {
		return
	}
	if in.Level == "" {
		in.Level = "info"
	}
	n := model.Notification{
		Type: in.Type, Level: in.Level, Title: in.Title, Body: in.Body,
		Source: source, CreatedAt: time.Now(),
	}
	if err := s.db.Create(&n).Error; err != nil {
		slog.Error("通知落库失败", "err", err)
		return
	}
	if b, err := json.Marshal(n); err == nil {
		s.hub.Broadcast("notify", b)
	}
	s.emailIfSubscribed(in.Type, in.Title, in.Body)
}

// List 站内信列表（新→旧）。
func (s *Service) List(limit int, unreadOnly bool) []model.Notification {
	if limit < 1 || limit > 500 {
		limit = 50
	}
	q := s.db.Order("id DESC").Limit(limit)
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	out := []model.Notification{}
	_ = q.Find(&out).Error
	return out
}

// Unread 未读计数。
func (s *Service) Unread() int64 {
	var n int64
	_ = s.db.Model(&model.Notification{}).Where("read_at IS NULL").Count(&n).Error
	return n
}

// MarkRead 标记已读；ids 为空且 all=true 时全部已读。
func (s *Service) MarkRead(ids []uint, all bool) {
	q := s.db.Model(&model.Notification{}).Where("read_at IS NULL")
	if !all && len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	} else if !all {
		return
	}
	_ = q.Update("read_at", time.Now()).Error
}

// Prune 清理 30 天前的通知。
func (s *Service) Prune() {
	if err := s.db.Where("created_at < ?", time.Now().AddDate(0, 0, -30)).Delete(&model.Notification{}).Error; err != nil {
		slog.Error("通知清理失败", "err", err)
	}
}

// Start 订阅面板事件主题（防抖 5 分钟）并启动每日过期清理。
func (s *Service) Start() {
	wrap := func(topic, level, title, body string) {
		// bus.Publish 是同步分发，防抖判断放在最前面避免重复落库
		if !s.dedup(topic + "|" + title) {
			return
		}
		s.Send("core", Input{Type: topic, Level: level, Title: title, Body: body})
	}
	s.bus.Subscribe("cert.failed", func(p map[string]any) {
		wrap("cert.failed", "error", "证书续期失败", fmt.Sprintf("任务 #%v：%v", p["taskId"], p["error"]))
	})
	s.bus.Subscribe("cert.expiring", func(p map[string]any) {
		wrap("cert.expiring", "warn", fmt.Sprintf("证书即将到期（剩 %v 天）", p["daysLeft"]), fmt.Sprintf("域名：%v", p["domains"]))
	})
	s.bus.Subscribe("cert.issued", func(p map[string]any) {
		wrap("cert.issued", "info", "证书签发成功", fmt.Sprintf("域名：%v，有效期至 %v", p["domains"], p["notAfter"]))
	})
	s.bus.Subscribe("sitecheck.down", func(p map[string]any) {
		wrap("sitecheck.down", "error", fmt.Sprintf("站点探测异常：%v", p["domain"]), fmt.Sprintf("%v", p["error"]))
	})
	s.bus.Subscribe("sitecheck.up", func(p map[string]any) {
		wrap("sitecheck.up", "info", fmt.Sprintf("站点探测恢复：%v", p["domain"]), "")
	})
	// plugin.enabled/disabled 不进站内信：由管理员自己在面板内触发，属噪音（事件仍供插件订阅）
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			s.Prune()
		}
	}()
}

// dedup 同一 type|title 5 分钟内只发一次（防站点探测抖动等刷屏）。
func (s *Service) dedup(key string) bool {
	now := time.Now()
	if v, ok := s.seen.Load(key); ok && now.Sub(v.(time.Time)) < 5*time.Minute {
		return false
	}
	s.seen.Store(key, now)
	return true
}

// ---- 邮件渠道 ----

type mailConfig struct {
	host, user, pass, fromAddr, fromAlias, rcpt string
	port                                        int
	subs                                        map[string]bool
}

// loadMailConfig 现读 Setting 表；未配 SMTP 或收件人时返回 nil（邮件渠道静默关闭）。
// fromAddr：发件信封地址（留空回落 SMTP 用户）；fromAlias：发件人显示别名（默认 ZJPanel）。
// 兼容旧数据：旧 notify.smtp.from 若为邮箱 → 当作发件地址；非邮箱（如旧误填的别名）→ 当作别名。
func (s *Service) loadMailConfig() *mailConfig {
	get := func(k string) string {
		var v model.Setting
		if err := s.db.Where("key = ?", k).First(&v).Error; err != nil {
			return ""
		}
		return v.Value
	}
	host, rcpt := get(keySMTPHost), get(keyRcpt)
	if host == "" || rcpt == "" {
		return nil
	}
	port := 465
	_, _ = fmt.Sscanf(get(keySMTPPort), "%d", &port)
	pass, _ := s.box.Open(get(keySMTPPass))
	var subs map[string]bool
	_ = json.Unmarshal([]byte(get(keySubs)), &subs)

	user := get(keySMTPUser)
	legacyFrom := get(keySMTPFrom)
	fromAddr, fromAlias := get(keySMTPFromAddr), get(keySMTPFromAlias)
	if fromAddr == "" && strings.Contains(legacyFrom, "@") {
		fromAddr = legacyFrom // 旧数据是邮箱
	}
	if fromAlias == "" && legacyFrom != "" && !strings.Contains(legacyFrom, "@") {
		fromAlias = legacyFrom // 旧数据是别名
	}
	if fromAlias == "" {
		fromAlias = "ZJPanel"
	}
	if fromAddr == "" {
		fromAddr = user
	}
	return &mailConfig{
		host: host, port: port, user: user, pass: pass,
		fromAddr: fromAddr, fromAlias: fromAlias, rcpt: rcpt, subs: subs,
	}
}

func (s *Service) emailIfSubscribed(nType, title, body string) {
	cfg := s.loadMailConfig()
	if cfg == nil || !cfg.subs[nType] {
		return
	}
	go func() {
		if err := pkg.SendMail(cfg.host, cfg.port, cfg.user, cfg.pass, cfg.fromAlias, cfg.fromAddr, cfg.rcpt,
			"[ZJPanel] "+title, body); err != nil {
			slog.Error("通知邮件发送失败", "type", nType, "err", err)
		}
	}()
}

// ---- 设置读写（供 HTTP 层调用） ----

// SettingsDTO 设置视图：pass 只回是否已配置（明文/密文均不出接口）。
type SettingsDTO struct {
	SMTPHost      string          `json:"smtpHost"`
	SMTPPort      int             `json:"smtpPort"`
	SMTPUser      string          `json:"smtpUser"`
	HasPass       bool            `json:"hasPass"`
	SMTPFrom      string          `json:"smtpFrom"`      // 发件信封地址
	SMTPFromAlias string          `json:"smtpFromAlias"` // 发件人显示别名
	Rcpt          string          `json:"rcpt"`
	Subs          map[string]bool `json:"subs"`
	Types         []string        `json:"types"`
}

// Settings 读当前配置与订阅（类型 = 内置 ∪ 库中出现过）。
func (s *Service) Settings() SettingsDTO {
	get := func(k string) string {
		var v model.Setting
		if err := s.db.Where("key = ?", k).First(&v).Error; err != nil {
			return ""
		}
		return v.Value
	}
	port := 465
	_, _ = fmt.Sscanf(get(keySMTPPort), "%d", &port)
	var subs map[string]bool
	_ = json.Unmarshal([]byte(get(keySubs)), &subs)
	if subs == nil {
		subs = map[string]bool{}
	}
	types := append([]string{}, BuiltinTypes...)
	var extra []string
	_ = s.db.Model(&model.Notification{}).Distinct().Pluck("type", &extra)
	for _, t := range extra {
		if t == "" {
			continue
		}
		found := false
		for _, b := range BuiltinTypes {
			if b == t {
				found = true
				break
			}
		}
		if !found {
			types = append(types, t)
		}
	}
	return SettingsDTO{
		SMTPHost: get(keySMTPHost), SMTPPort: port, SMTPUser: get(keySMTPUser),
		HasPass:  get(keySMTPPass) != "",
		SMTPFrom: get(keySMTPFromAddr), SMTPFromAlias: func() string {
			if v := get(keySMTPFromAlias); v != "" {
				return v
			}
			if legacy := get(keySMTPFrom); legacy != "" && !strings.Contains(legacy, "@") {
				return legacy // 旧数据别名迁移
			}
			return "ZJPanel"
		}(),
		Rcpt: get(keyRcpt), Subs: subs, Types: types,
	}
}

// SaveSettings 保存配置；pass 为空沿用旧密码（SecretBox 加密落库）；
// fromAddr 为空回落 SMTP 用户；fromAlias 为空默认 ZJPanel。
func (s *Service) SaveSettings(host string, port int, user, pass, fromAddr, fromAlias, rcpt string, subs map[string]bool) error {
	set := func(k, v string) error {
		var rec model.Setting
		if err := s.db.Where("key = ?", k).First(&rec).Error; err != nil {
			return s.db.Create(&model.Setting{Key: k, Value: v}).Error
		}
		rec.Value = v
		return s.db.Model(&rec).Update("value", v).Error
	}
	if port < 1 || port > 65535 {
		port = 465
	}
	if err := set(keySMTPHost, host); err != nil {
		return err
	}
	if err := set(keySMTPPort, fmt.Sprintf("%d", port)); err != nil {
		return err
	}
	if err := set(keySMTPUser, user); err != nil {
		return err
	}
	if pass != "" {
		enc, err := s.box.SealE(pass)
		if err != nil {
			return err
		}
		if err := set(keySMTPPass, enc); err != nil {
			return err
		}
	}
	if err := set(keySMTPFromAddr, fromAddr); err != nil {
		return err
	}
	if err := set(keySMTPFromAlias, fromAlias); err != nil {
		return err
	}
	if err := set(keyRcpt, rcpt); err != nil {
		return err
	}
	b, _ := json.Marshal(subs)
	return set(keySubs, string(b))
}

// SendTest 按当前配置发一封测试邮件。
func (s *Service) SendTest() error {
	cfg := s.loadMailConfig()
	if cfg == nil {
		return fmt.Errorf("请先填写 SMTP 服务器与收件邮箱")
	}
	return pkg.SendMail(cfg.host, cfg.port, cfg.user, cfg.pass, cfg.fromAlias, cfg.fromAddr, cfg.rcpt,
		"[ZJPanel] 测试邮件", "这是一封来自 ZJPanel 统一通知的测试邮件。收到它说明 SMTP 配置正确。")
}
