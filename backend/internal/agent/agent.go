// Package agent 主机 Agent 控制面：反向 WebSocket 注册表、token 签发、指令队列与指标缓存。
//
// 设计要点：
//   - 检测与执行在宿主 Agent 侧，面板只做策略、审批与审计；
//   - Agent 出站连接面板，不需要在目标机开放入站端口；
//   - 指令带唯一 ID + ack，重复下发由 Agent 侧按 ID 去重（至少一次语义）。
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

// Proto 面板与 Agent 的协议版本。任一侧不匹配时拒绝连接并提示升级。
const Proto = "1"

// ProtocolOK 协议兼容判断（当前只有一代，后续按主版本号比较）。
func ProtocolOK(v string) bool { return v == Proto || v == "" }

// Metrics Agent 上报的主机指标。
type Metrics struct {
	T            time.Time `json:"t"`
	Load1        float64   `json:"load1"`
	Load5        float64   `json:"load5"`
	Load15       float64   `json:"load15"`
	MemTotal     uint64    `json:"memTotal"`
	MemAvailable uint64    `json:"memAvailable"`
	DiskTotal    uint64    `json:"diskTotal"`
	DiskUsed     uint64    `json:"diskUsed"`
	Uptime       uint64    `json:"uptime"`
	Conns        int       `json:"conns"`
	SSHSessions  int       `json:"sshSessions"`
	CPU          float64   `json:"cpu"` // 0~100，由 /proc/stat 两次采样差值计算（Agent 侧）
	Ports        []Port    `json:"ports"`
}

// Port 监听端口。
type Port struct {
	Proto   string `json:"proto"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
}

// Event 主机侧事件（SSH 爆破、登录成功等）。
type Event struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	HostID   uint      `json:"hostId"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	IP       string    `json:"ip,omitempty"`
	User     string    `json:"user,omitempty"`
	Detail   string    `json:"detail"`
	Count    int       `json:"count"`
}

type conn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (c *conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.c.Write(ctx, websocket.MessageText, b)
}

type bootEntry struct {
	hostID  uint
	expires time.Time
}

type result struct {
	OK     bool
	Output string
	Error  string
}

// Service 控制面。所有对 DB 的写入都在这里，HTTP 层只做参数校验与审计。
type Service struct {
	cfg *pkg.Config
	db  *gorm.DB
	box *pkg.SecretBox
	hub *ws.Hub

	mu      sync.Mutex
	conns   map[uint]*conn
	boot    map[string]bootEntry
	metrics map[uint]*Metrics
	events  map[uint][]Event
	alive   map[uint]time.Time // 最近一次 hello/heartbeat，用于识别半开连接
	waiters map[string]chan result
	evSeq   int64

	// 自动升级去重：同一主机同一时刻只跑一次检测/下发
	upgrading map[uint]bool
	// 自动升级连续失败次数（内存态，重启清零）：用于指数退避拉长冷却窗口
	upgradeFails map[uint]int
}

func New(cfg *pkg.Config, db *gorm.DB, box *pkg.SecretBox, hub *ws.Hub) *Service {
	return &Service{
		cfg: cfg, db: db, box: box, hub: hub,
		conns:        map[uint]*conn{},
		boot:         map[string]bootEntry{},
		metrics:      map[uint]*Metrics{},
		events:       map[uint][]Event{},
		alive:        map[uint]time.Time{},
		waiters:      map[string]chan result{},
		upgrading:    map[uint]bool{},
		upgradeFails: map[uint]int{},
	}
}

// EnsureLocalHost 内置「本机」伪主机：承接面板容器内的本地 Shell，
// 让「主机」成为唯一入口；它没有 Agent，也不能被卸载/删除。
func (s *Service) EnsureLocalHost() {
	var h model.SshHost
	if err := s.db.Where("local = ?", true).First(&h).Error; err == nil {
		return
	}
	h = model.SshHost{
		Name: "本机", Host: "127.0.0.1", Port: 22, Username: "root",
		AuthType: "local", Local: true, AgentStatus: "local", AgentVersion: "内置",
		Claimed: true,
	}
	if err := s.db.Create(&h).Error; err != nil {
		slog.Warn("本机伪主机初始化失败", "err", err)
	}
}

// IssueBootstrap 生成一次性引导 token（30 分钟），绑定到指定主机。
// 该 token 只用于首次回连换取长期 host token，泄露面比长期凭据小。
func (s *Service) IssueBootstrap(hostID uint) string {
	tok := randomHex(20)
	s.mu.Lock()
	for k, v := range s.boot {
		if time.Now().After(v.expires) {
			delete(s.boot, k)
		}
	}
	s.boot[tok] = bootEntry{hostID: hostID, expires: time.Now().Add(30 * time.Minute)}
	s.mu.Unlock()
	return tok
}

// ResolveToken 判定 token 属于引导还是长期凭据，返回主机 ID。
func (s *Service) ResolveToken(tok string) (hostID uint, bootstrap bool, ok bool) {
	if tok == "" {
		return 0, false, false
	}
	h := hashToken(tok)
	s.mu.Lock()
	if e, found := s.boot[tok]; found && time.Now().Before(e.expires) {
		s.mu.Unlock()
		return e.hostID, true, true
	}
	s.mu.Unlock()
	var host model.SshHost
	if err := s.db.Where("token_hash = ?", h).First(&host).Error; err != nil {
		return 0, false, false
	}
	return host.ID, false, true
}

// Online 主机当前是否有活跃连接。
// 除连接存在外还要求最近 45s 内有 hello/heartbeat（心跳 15s 一次）：TCP 半开时
// conn 仍在，但心跳已停，不能继续显示「在线」。
func (s *Service) Online(hostID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[hostID]; !ok {
		return false
	}
	if t, ok := s.alive[hostID]; ok && time.Since(t) > 45*time.Second {
		return false
	}
	return true
}

// LatestMetrics 最近一次指标。
func (s *Service) LatestMetrics(hostID uint) *Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metrics[hostID]
}

// RecentEvents 最近事件（倒序）。
func (s *Service) RecentEvents(hostID uint, limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.events[hostID]
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	out := make([]Event, 0, limit)
	for i := len(list) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, list[i])
	}
	return out
}

// Serve 处理一条 Agent 长连接（阻塞到断开）。bootstrap=true 时签发长期 token。
// 调用方需已完成 token 认证（如带 query token 的场景）。
func (s *Service) Serve(hostID uint, c *websocket.Conn, bootstrap bool) {
	s.serve(hostID, c, bootstrap, "", nil)
}

// ServeAuth 接受尚未认证的连接：从首帧 hello 里取 token 完成认证再进入会话。
// 这样 token 不会出现在 URL/访问日志里，Agent 也只需在 hello 中携带凭据。
func (s *Service) ServeAuth(c *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mt, data, err := c.Read(ctx)
	if err != nil || mt != websocket.MessageText {
		_ = c.Close(websocket.StatusPolicyViolation, "no hello")
		return
	}
	var msg map[string]any
	if json.Unmarshal(data, &msg) != nil || str(msg["type"]) != "hello" {
		_ = c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	hostID, bootstrap, ok := s.ResolveToken(str(msg["token"]))
	if !ok {
		_ = c.Close(websocket.StatusPolicyViolation, "invalid token")
		return
	}
	s.serve(hostID, c, bootstrap, str(msg["token"]), msg)
}

func (s *Service) serve(hostID uint, c *websocket.Conn, bootstrap bool, bootTok string, first map[string]any) {
	defer c.Close(websocket.StatusNormalClosure, "")
	ac := &conn{c: c}

	s.mu.Lock()
	if old, ok := s.conns[hostID]; ok {
		old.mu.Lock()
		_ = old.c.Close(websocket.StatusGoingAway, "replaced by new connection")
		old.mu.Unlock()
	}
	s.conns[hostID] = ac
	s.mu.Unlock()

	// 服务端定时下发 tick：Agent 侧读超时 30s，面板重启/网络半开时能在一个
	// tick 周期内发现断连并重连，而不是等应用层心跳 90s 超时才恢复。
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := ac.send(map[string]any{"type": "tick"}); err != nil {
					return
				}
			}
		}
	}()

	defer func() {
		removed := false
		s.mu.Lock()
		if cur, ok := s.conns[hostID]; ok && cur == ac {
			delete(s.conns, hostID)
			removed = true
		}
		s.mu.Unlock()
		// 被新连接替换时旧 serve 也会走到这里，不能把新连接的状态标成离线
		if removed {
			s.markOffline(hostID)
		}
	}()

	tokenToSend := ""
	if bootstrap {
		tokenToSend = randomHex(32)
		s.mu.Lock()
		if bootTok != "" {
			delete(s.boot, bootTok)
		} else {
			delete(s.boot, tokenOf(s, hostID))
		}
		s.mu.Unlock()
		s.db.Model(&model.SshHost{}).Where("id = ?", hostID).Update("token_hash", hashToken(tokenToSend))
	}

	// 认证阶段已经读过首帧 hello，直接用它完成上线登记并下发长期 token。
	if first != nil {
		s.onHello(hostID, ac, first, tokenToSend)
		go s.flushQueued(hostID)
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		mt, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch str(msg["type"]) {
		case "hello":
			s.onHello(hostID, ac, msg, tokenToSend)
			go s.flushQueued(hostID)
		case "heartbeat":
			s.onHeartbeat(hostID, msg)
		case "metrics":
			s.onHeartbeat(hostID, msg)
		case "event":
			s.onEvent(hostID, msg)
		case "cmdResult":
			s.onResult(hostID, msg)
		}
	}
}

// tokenOf 仅用于清理 bootstrap 表：按 hostID 反查（同一主机同时只允许一个引导 token）。
func tokenOf(s *Service, hostID uint) string {
	for k, v := range s.boot {
		if v.hostID == hostID {
			return k
		}
	}
	return ""
}

func (s *Service) onHello(hostID uint, ac *conn, msg map[string]any, newToken string) {
	version := str(msg["version"])
	proto := str(msg["proto"])
	name := str(msg["hostname"])
	osarch := str(msg["os"]) + "/" + str(msg["arch"])
	if !ProtocolOK(proto) {
		_ = ac.send(map[string]any{"type": "error", "message": "协议版本不兼容，请升级 Agent"})
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.alive[hostID] = now
	s.mu.Unlock()
	upd := map[string]any{
		"agent_id": str(msg["agentId"]), "agent_status": "online", "agent_version": version,
		"agent_proto": proto, "agent_last_seen": &now, "remote_ip": str(msg["remoteIp"]),
		"os_arch": osarch,
	}
	if name != "" {
		var cur model.SshHost
		if err := s.db.First(&cur, hostID).Error; err == nil && (cur.Name == "" || cur.Name == "待认领主机") {
			upd["name"] = name
		}
	}
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).Updates(upd)

	welcome := map[string]any{
		"type": "welcome", "hostId": hostID, "proto": Proto,
		"token": newToken, "version": version,
	}
	_ = ac.send(welcome)
	// 回连即检查：面板内置的 Agent 版本更新时立即自动升级（失败有冷却，不会刷屏重试）
	go s.maybeAutoUpgrade(hostID, version, str(msg["os"]), str(msg["arch"]))
	slog.Info("Agent 上线", "host", hostID, "version", version, "os", osarch)
	s.emitSystem(hostID, "Agent 上线", "info", name+" "+version)
}

func (s *Service) onHeartbeat(hostID uint, msg map[string]any) {
	now := time.Now()
	s.mu.Lock()
	s.alive[hostID] = now
	s.mu.Unlock()
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).
		Updates(map[string]any{"agent_status": "online", "agent_last_seen": &now})

	raw, _ := json.Marshal(msg["metrics"])
	var m Metrics
	if len(raw) > 0 && json.Unmarshal(raw, &m) == nil {
		m.T = now
		s.mu.Lock()
		s.metrics[hostID] = &m
		s.mu.Unlock()
	}
}

func (s *Service) onEvent(hostID uint, msg map[string]any) {
	raw, _ := json.Marshal(msg["event"])
	var e Event
	if json.Unmarshal(raw, &e) != nil {
		return
	}
	e.HostID = hostID
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.mu.Lock()
	s.evSeq++
	if e.ID == 0 {
		e.ID = s.evSeq
	}
	ring := append(s.events[hostID], e)
	if len(ring) > 200 {
		ring = ring[len(ring)-200:]
	}
	s.events[hostID] = ring
	s.mu.Unlock()
	// 复用面板 WS 主题，前端一台机器也能实时看到事件
	if b, err := json.Marshal(e); err == nil {
		s.hub.Broadcast("host-event", b)
	}
}

func (s *Service) onResult(hostID uint, msg map[string]any) {
	id := str(msg["id"])
	res := result{OK: boolOf(msg["ok"]), Output: str(msg["output"]), Error: str(msg["error"])}
	now := time.Now()
	status := "ok"
	if !res.OK {
		status = "failed"
	}
	_ = s.db.Model(&model.AgentCommand{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "output": res.Output, "error": res.Error, "acked_at": &now})
	// 指令结果也进事件流：卸载/加固这类动作必须留痕
	s.emitSystem(hostID, "指令完成："+str(msg["kind"]), "info", firstLine(res.Output+res.Error))

	s.mu.Lock()
	ch := s.waiters[id]
	delete(s.waiters, id)
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
	}
}

// Exec 下发指令并等待 ack。主机离线时指令留在队列，连上后自动补发。
func (s *Service) Exec(hostID uint, kind string, params map[string]any, timeout time.Duration) (*result, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	id := randomHex(16)
	pb, _ := json.Marshal(params)
	cmd := model.AgentCommand{ID: id, HostID: hostID, Kind: kind, Params: string(pb), Status: "queued", CreatedAt: time.Now()}
	if err := s.db.Create(&cmd).Error; err != nil {
		return nil, err
	}
	ch := make(chan result, 1)
	s.mu.Lock()
	s.waiters[id] = ch
	ac, online := s.conns[hostID]
	s.mu.Unlock()

	if online {
		if err := ac.send(map[string]any{"type": "cmd", "id": id, "kind": kind, "params": params}); err != nil {
			slog.Warn("指令发送失败，留待重连补发", "host", hostID, "err", err)
		} else {
			_ = s.db.Model(&model.AgentCommand{}).Where("id = ?", id).Update("status", "sent")
		}
	}

	select {
	case res := <-ch:
		return &res, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.waiters, id)
		s.mu.Unlock()
		_ = s.db.Model(&model.AgentCommand{}).Where("id = ?", id).Update("status", "timeout")
		return nil, fmt.Errorf("等待 Agent 响应超时（%s）", timeout)
	}
}

// flushQueued 主机连上后补发排队指令。
func (s *Service) flushQueued(hostID uint) {
	var cmds []model.AgentCommand
	_ = s.db.Where("host_id = ? AND status = ?", hostID, "queued").Order("created_at").Limit(20).Find(&cmds).Error
	for _, cmd := range cmds {
		s.mu.Lock()
		ac, ok := s.conns[hostID]
		s.mu.Unlock()
		if !ok {
			return
		}
		var params map[string]any
		_ = json.Unmarshal([]byte(cmd.Params), &params)
		if err := ac.send(map[string]any{"type": "cmd", "id": cmd.ID, "kind": cmd.Kind, "params": params}); err == nil {
			_ = s.db.Model(&model.AgentCommand{}).Where("id = ?", cmd.ID).Update("status", "sent")
		}
	}
}

func (s *Service) markOffline(hostID uint) {
	s.mu.Lock()
	delete(s.alive, hostID)
	s.mu.Unlock()
	// 不改 agent_last_seen：保留最后一次真实心跳时间，前端显示「最后在线」才有意义
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).
		Update("agent_status", "offline")
	slog.Info("Agent 下线", "host", hostID)
	s.emitSystem(hostID, "Agent 下线", "warn", "连接已断开，等待 Agent 回连")
}

func (s *Service) emitSystem(hostID uint, kind, severity, detail string) {
	s.mu.Lock()
	s.evSeq++
	e := Event{ID: s.evSeq, Time: time.Now(), HostID: hostID, Kind: kind, Severity: severity, Detail: detail}
	ring := append(s.events[hostID], e)
	if len(ring) > 200 {
		ring = ring[len(ring)-200:]
	}
	s.events[hostID] = ring
	s.mu.Unlock()
	if b, err := json.Marshal(e); err == nil {
		s.hub.Broadcast("host-event", b)
	}
}

// MarkUninstalled 面板下发卸载并确认成功：只改状态，记录由用户显式删除。
func (s *Service) MarkUninstalled(hostID uint) {
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).
		Updates(map[string]any{"agent_status": "uninstalled", "agent_id": "", "token_hash": ""})
}

// Claim 认领命令安装的主机（补名字）。
func (s *Service) Claim(hostID uint, name string) error {
	var h model.SshHost
	if err := s.db.First(&h, hostID).Error; err != nil {
		return fmt.Errorf("主机不存在")
	}
	if name == "" {
		name = h.Host
	}
	return s.db.Model(&model.SshHost{}).Where("id = ?", hostID).Updates(map[string]any{"claimed": true, "name": name}).Error
}

// SetVersion 升级完成后回写版本（Agent 会在重连 hello 时再覆盖一次）。
func (s *Service) SetVersion(hostID uint, version string) {
	_ = s.db.Model(&model.SshHost{}).Where("id = ?", hostID).Update("agent_version", version)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// str/boolOf 宽松取值，避免 Agent 侧 JSON 类型差异导致 panic。
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOf(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}
