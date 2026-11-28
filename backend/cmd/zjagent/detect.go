// 主机主动防御：常驻攻击检测（日志 tail + 滑动窗口计数 + 本地自动封禁 + 事件上报）。
//
// 框架设计：一个检测面（sourceDef）= 日志行流 + 行处理，SSH 爆破是首个实例；
// 后续新增检测面（MySQL/Redis 等服务日志）注册一个 sourceDef 即可。
// 策略由面板经 defense_policy 指令下发，持久化在 defense.json，断网按最后策略工作；
// 事件走反向通道 type=event，断线写 spool 由 flushSpool 补传（上限与其一致）。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ---- 防御策略（面板下发 + 本地持久化，JSON 结构与面板 agent.HostDefensePolicy 保持一致） ----

type defensePolicy struct {
	Enabled    bool     `json:"enabled"`
	SSH        sshRules `json:"ssh"`
	Whitelist  []string `json:"whitelist"`  // IP 或 CIDR，永不封禁
	BanPrivate bool     `json:"banPrivate"` // 是否允许封私网段（内网横向移动场景才开启）
}

type sshRules struct {
	WindowSecs int  `json:"windowSecs"` // 滑动窗口秒数
	MaxFails   int  `json:"maxFails"`   // 窗口内失败次数阈值
	BanSeconds int  `json:"banSeconds"` // 封禁时长
	AutoBan    bool `json:"autoBan"`    // 达阈值本地自动封禁
}

func defaultDefensePolicy() defensePolicy {
	return defensePolicy{
		Enabled: true,
		SSH:     sshRules{WindowSecs: 600, MaxFails: 10, BanSeconds: 3600, AutoBan: true},
	}
}

const defensePath = stateDir + "/defense.json"

var (
	polMu     sync.Mutex
	polCur    defensePolicy
	polLoaded bool
)

// currentPolicy 返回当前生效策略；首次调用时从磁盘加载（默认值兜底）。
func currentPolicy() defensePolicy {
	polMu.Lock()
	defer polMu.Unlock()
	if !polLoaded {
		polCur = loadDefensePolicy()
		polLoaded = true
	}
	return polCur
}

func loadDefensePolicy() defensePolicy {
	pol := defaultDefensePolicy()
	if b, err := os.ReadFile(defensePath); err == nil {
		_ = json.Unmarshal(b, &pol)
	}
	return pol
}

// applyPolicy 面板下发的新策略：钳位 → 落盘 → 立即生效（检测器每行日志实时读取，无需额外通知）。
func applyPolicy(p defensePolicy) defensePolicy {
	p.SSH.WindowSecs = clampInt(p.SSH.WindowSecs, 60, 24*3600)
	p.SSH.MaxFails = clampInt(p.SSH.MaxFails, 3, 1000)
	p.SSH.BanSeconds = clampInt(p.SSH.BanSeconds, 60, 7*24*3600)
	p.Whitelist = normalizeWhitelist(p.Whitelist)
	if b, err := json.Marshal(p); err == nil {
		_ = os.WriteFile(defensePath, b, 0o600)
	}
	polMu.Lock()
	polCur = p
	polLoaded = true
	polMu.Unlock()
	return p
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// defensePolicyCmd 面板下发的策略指令（exec kind=defense_policy）。
func defensePolicyCmd(p map[string]any) (bool, string) {
	b, _ := json.Marshal(p)
	var pol defensePolicy
	if err := json.Unmarshal(b, &pol); err != nil {
		return false, "策略格式错误: " + err.Error()
	}
	pol = applyPolicy(pol)
	return true, fmt.Sprintf("主动防御策略已生效（启用=%v，SSH：%ds 内 ≥%d 次失败封禁 %ds，自动封禁=%v，白名单 %d 条）",
		pol.Enabled, pol.SSH.WindowSecs, pol.SSH.MaxFails, pol.SSH.BanSeconds, pol.SSH.AutoBan, len(pol.Whitelist))
}

// normalizeWhitelist 去空、去重，保留 IP 与 CIDR 原样（解析失败在匹配时被忽略）。
func normalizeWhitelist(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, w := range list {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// ---- 白名单保护（封禁动作统一校验，防止误封回环/面板/内网/白名单地址） ----

// ipProtected 判断 IP 是否受保护不可封禁。
func ipProtected(ipStr string, pol defensePolicy) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false // 非法地址不归白名单管，由调用方的校验拒绝
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	if !pol.BanPrivate && ip.IsPrivate() {
		return true
	}
	for _, cidr := range protectedCIDRs(pol) {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// protectedCIDRs 构建受保护网段：策略白名单 + 面板地址（反向通道对端，封了会断 SSH 终端与审计）。
func protectedCIDRs(pol defensePolicy) []net.IPNet {
	out := parseWhitelist(pol.Whitelist)
	out = append(out, panelIPs()...)
	return out
}

func parseWhitelist(list []string) []net.IPNet {
	out := []net.IPNet{}
	for _, w := range list {
		if _, cidr, err := net.ParseCIDR(w); err == nil {
			out = append(out, *cidr)
			continue
		}
		if ip := net.ParseIP(w); ip != nil {
			out = append(out, singleIPNet(ip))
		}
	}
	return out
}

func singleIPNet(ip net.IP) net.IPNet {
	if ip4 := ip.To4(); ip4 != nil {
		return net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
	}
	return net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

var (
	panelMu        sync.Mutex
	panelCIDRs     []net.IPNet
	panelCachedFor string // 缓存对应的面板地址（地址变更立即失效，避免改配置后仍用旧解析）
	panelResolved  time.Time
)

// panelIPs 把面板地址解析为网段（IP 字面量直接用；域名解析缓存 10 分钟）。
func panelIPs() []net.IPNet {
	panelMu.Lock()
	defer panelMu.Unlock()
	panel := currentPanelAddr()
	if panel == "" {
		return nil
	}
	if panelCIDRs != nil && panelCachedFor == panel && time.Since(panelResolved) < 10*time.Minute {
		return panelCIDRs
	}
	panelCIDRs = resolveHostNets(panelHost(panel))
	panelCachedFor = panel
	panelResolved = time.Now() // 失败也计时，避免爆破场景下高频 DNS
	return panelCIDRs
}

// currentPanelAddr 取面板地址（测试与运行共用入口；client 未就绪时读配置文件）。
var panelAddrFn = func() string {
	return loadConfig().Panel
}

func currentPanelAddr() string { return panelAddrFn() }

// panelHost 从面板地址剥出主机名/IP（兼容 http(s)://、host:port、裸域名）。
func panelHost(panel string) string {
	h := panel
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	return strings.Trim(h, "[]")
}

func resolveHostNets(host string) []net.IPNet {
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPNet{singleIPNet(ip)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	out := []net.IPNet{}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, singleIPNet(ip))
		}
	}
	return out
}

// ---- 检测面 1：SSH 认证日志 ----

type linePat struct {
	re      *regexp.Regexp
	ipGrp   int
	userGrp int // <=0 表示无用户
}

// sshFailPats 常见 sshd 认证失败消息（兼容 auth.log 的 syslog 前缀与 journald 裸消息）。
var sshFailPats = []linePat{
	{regexp.MustCompile(`Failed password for (?:invalid user )?(\S+) from (\S+) port`), 2, 1},
	{regexp.MustCompile(`Invalid user (\S+) from (\S+)`), 2, 1},
	{regexp.MustCompile(`Too many authentication failures(?: for (\S+))? from (\S+)`), 2, 1},
	{regexp.MustCompile(`error: maximum authentication attempts exceeded(?: for \S+)? from (\S+)`), 1, 0},
	{regexp.MustCompile(`Connection closed by authenticating user \S+ (\S+) port`), 1, 0},
}

var sshAcceptRe = regexp.MustCompile(`Accepted (?:password|publickey) for (\S+) from (\S+)`)

// sshLogLines SSH 认证日志行流：优先 journald（单元名 Debian=ssh / RHEL=sshd 都能覆盖），
// 回退 tail 文件。
func sshLogLines(ctx context.Context) (<-chan string, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("仅支持 Linux")
	}
	if _, err := exec.LookPath("journalctl"); err == nil {
		cmd := exec.CommandContext(ctx, "journalctl", "-f", "-q", "-o", "cat", "-n", "0", "-u", "ssh", "-u", "sshd")
		if out, perr := cmd.StdoutPipe(); perr == nil {
			if serr := cmd.Start(); serr == nil {
				ch := make(chan string, 64)
				go func() {
					defer close(ch)
					defer cmd.Wait()
					sc := bufio.NewScanner(out)
					sc.Buffer(make([]byte, 64*1024), 1024*1024)
					for sc.Scan() {
						select {
						case ch <- sc.Text():
						case <-ctx.Done():
							return
						}
					}
				}()
				return ch, nil
			}
		}
		// journalctl 起不来（无日志权限等）落到文件 tail
	}
	return tailAuthFile(ctx)
}

// tailAuthFile tail 认证日志文件，处理轮转：inode 变化或被截断时重开。
func tailAuthFile(ctx context.Context) (<-chan string, error) {
	path := ""
	for _, p := range []string{"/var/log/auth.log", "/var/log/secure"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			path = p
			break
		}
	}
	if path == "" {
		return nil, fmt.Errorf("无 journalctl 且未找到 /var/log/auth.log 或 /var/log/secure")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for ctx.Err() == nil {
			for sc.Scan() {
				select {
				case ch <- sc.Text():
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			// 轮转检测：文件被改名/删除或截断则重开
			st, serr := os.Stat(path)
			cf, cerr := f.Stat()
			if serr != nil || cerr != nil || !os.SameFile(st, cf) {
				nf, oerr := os.Open(path)
				if oerr != nil {
					continue // 轮转间隙文件暂不可读，下一轮重试
				}
				_ = f.Close()
				f = nf
				sc = bufio.NewScanner(f)
				sc.Buffer(make([]byte, 64*1024), 1024*1024)
			}
		}
	}()
	return ch, nil
}

// ---- 攻击追踪：滑动窗口计数 + 同窗口聚合上报 ----

// breakinCorrelate 达阈值后多久内出现成功登录，判为"疑似爆破成功"。
const breakinCorrelate = 30 * time.Minute

type attackTracker struct {
	mu      sync.Mutex
	fails   map[string][]time.Time // ip -> 窗口内失败时间
	armed   map[string]time.Time   // ip -> 最近达阈值时间（爆破后成功登录关联）
	report  map[string]time.Time   // ip -> 上次上报时间（同一窗口只报一次）
	lastSev map[string]string      // ip -> 上次上报级别（双倍阈值升级 critical 用）
}

func newAttackTracker() *attackTracker {
	return &attackTracker{
		fails:   map[string][]time.Time{},
		armed:   map[string]time.Time{},
		report:  map[string]time.Time{},
		lastSev: map[string]string{},
	}
}

func (t *attackTracker) sshLine(line string, emit func(map[string]any)) {
	for _, p := range sshFailPats {
		if m := p.re.FindStringSubmatch(line); m != nil {
			user := ""
			if p.userGrp > 0 && p.userGrp < len(m) {
				user = m[p.userGrp]
			}
			t.observeFail(m[p.ipGrp], user, emit)
			return
		}
	}
	if m := sshAcceptRe.FindStringSubmatch(line); m != nil {
		t.observeAccept(m[2], m[1], emit)
	}
}

func (t *attackTracker) observeFail(ip, user string, emit func(map[string]any)) {
	if ip == "" {
		return
	}
	pol := currentPolicy()
	win := time.Duration(pol.SSH.WindowSecs) * time.Second
	if win <= 0 {
		win = 10 * time.Minute
	}
	now := time.Now()

	t.mu.Lock()
	arr := t.fails[ip][:0:0]
	for _, at := range t.fails[ip] {
		if now.Sub(at) < win {
			arr = append(arr, at)
		}
	}
	arr = append(arr, now)
	t.fails[ip] = arr
	cnt := len(arr)
	sev := "warning"
	if cnt >= pol.SSH.MaxFails*2 {
		sev = "critical"
	}
	// 同窗口只报一次；但升级为 critical 时允许再报一次（强度加大的攻击要立即让面板知道）
	shouldReport := cnt >= pol.SSH.MaxFails &&
		(now.Sub(t.report[ip]) >= win || (sev == "critical" && t.lastSev[ip] != "critical"))
	if shouldReport {
		t.report[ip] = now
		t.armed[ip] = now
		t.lastSev[ip] = sev
	}
	t.mu.Unlock()

	if !shouldReport {
		return
	}
	// 封禁与上报都在锁外（emit 走网络/spool，ban 走 nft 命令）
	action := "alert"
	protection := ""
	if ipProtected(ip, pol) {
		protection = "；该地址在保护名单内（回环/面板/私网/白名单），未封禁"
	} else if pol.Enabled && pol.SSH.AutoBan {
		if ok, msg := banIP(map[string]any{"ip": ip, "seconds": pol.SSH.BanSeconds}); ok {
			action = "banned"
			protection = "；" + msg
		} else {
			protection = "；自动封禁失败：" + msg
		}
	}
	emit(map[string]any{
		"kind":     "ssh_bruteforce",
		"severity": sev,
		"ip":       ip,
		"user":     user,
		"count":    cnt,
		"action":   action,
		"detail": fmt.Sprintf("%d 分钟内 %d 次 SSH 认证失败（最近尝试用户 %s）%s",
			int(win.Minutes()), cnt, user, protection),
	})
}

func (t *attackTracker) observeAccept(ip, user string, emit func(map[string]any)) {
	if ip == "" {
		return
	}
	now := time.Now()
	t.mu.Lock()
	armedAt, wasArmed := t.armed[ip]
	delete(t.armed, ip)
	cnt := len(t.fails[ip])
	t.fails[ip] = nil
	t.mu.Unlock()
	if !wasArmed || now.Sub(armedAt) > breakinCorrelate {
		return
	}
	emit(map[string]any{
		"kind":     "ssh_breakin",
		"severity": "critical",
		"ip":       ip,
		"user":     user,
		"count":    cnt,
		"action":   "alert",
		"detail": fmt.Sprintf("IP %s 近期达过爆破阈值，现已成功登录（用户 %s），疑似爆破成功，请立即核查该账号与登录来源",
			ip, user),
	})
}

// gc 清理过期状态，防长驻内存增长（窗口外的失败、过期 armed/report）。
func (t *attackTracker) gc() {
	now := time.Now()
	pol := currentPolicy()
	win := time.Duration(pol.SSH.WindowSecs) * time.Second
	if win <= 0 {
		win = 10 * time.Minute
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for ip, arr := range t.fails {
		keep := arr[:0:0]
		for _, at := range arr {
			if now.Sub(at) < win {
				keep = append(keep, at)
			}
		}
		if len(keep) == 0 {
			delete(t.fails, ip)
		} else {
			t.fails[ip] = keep
		}
	}
	for ip, at := range t.armed {
		if now.Sub(at) > breakinCorrelate {
			delete(t.armed, ip)
		}
	}
	for ip, at := range t.report {
		if now.Sub(at) > win*2 {
			delete(t.report, ip)
			delete(t.lastSev, ip)
		}
	}
}

// ---- 检测器主循环（框架：新增检测面 = 追加 defenseSources 一项） ----

type sourceDef struct {
	name   string
	start  func(ctx context.Context) (<-chan string, error)
	handle func(tr *attackTracker, line string, emit func(map[string]any))
}

var defenseSources = []sourceDef{
	{name: "ssh", start: sshLogLines, handle: (*attackTracker).sshLine},
}

// startDefense 常驻检测入口：run() 里随 ctx 启动，每面独立 goroutine，断源自动重连。
func startDefense(ctx context.Context, c *client) {
	tr := newAttackTracker()
	emit := func(ev map[string]any) { c.emitEvent(ev) }
	for _, src := range defenseSources {
		s := src
		go func() {
			for ctx.Err() == nil {
				lines, err := s.start(ctx)
				if err != nil {
					logf("检测器 %s：日志源不可用（%v），30s 后重试", s.name, err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(30 * time.Second):
					}
					continue
				}
				logf("检测器 %s：已启动", s.name)
				for line := range lines {
					s.handle(tr, line, emit)
				}
				logf("检测器 %s：日志源中断，重连", s.name)
			}
		}()
	}
	// 状态清理
	tk := time.NewTicker(5 * time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			tr.gc()
		}
	}
}

// ---- 事件上报：直发优先，断线写 spool（补传上限 200 条，这里同限裁剪） ----

const spoolMax = 200

func (c *client) emitEvent(ev map[string]any) {
	if _, ok := ev["time"]; !ok {
		ev["time"] = time.Now().Format(time.RFC3339)
	}
	if err := c.send(map[string]any{"type": "event", "event": ev}); err == nil {
		return
	}
	appendSpool(ev)
}

func appendSpool(ev map[string]any) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(spoolPath), 0o755)
	f, err := os.OpenFile(spoolPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	trimSpool()
}

// trimSpool 超上限时裁掉最旧一半；flushSpool 补传也只取 200 条，语义一致。
func trimSpool() {
	b, err := os.ReadFile(spoolPath)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) <= spoolMax {
		return
	}
	keep := lines[len(lines)-spoolMax/2:]
	_ = os.WriteFile(spoolPath, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}
