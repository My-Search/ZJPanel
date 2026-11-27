// zjagent 主机 Agent：单静态二进制，子命令 install/run/uninstall/version/doctor/update。
//
// 设计边界：
//   - 只提供结构化动作（采集、审计、卸载、升级），永不提供通用 exec；
//   - 出站连接面板，不在目标机开放入站端口；
//   - 断线时事件进本地 spool，恢复后补传；指令按 ID 去重，重复下发回放上次结果。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"zjpanel/backend/internal/agent"
	"zjpanel/backend/internal/buildinfo"
)

type config struct {
	Panel   string `json:"panel"`
	Token   string `json:"token"`
	Version string `json:"version"`
}

type cmdMsg struct {
	Type   string         `json:"type"`
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params"`
}

type client struct {
	cfg     config
	conn    *websocket.Conn
	writeMu sync.Mutex
	seenMu  sync.Mutex
	seen    map[string]string // cmdID -> 上次结果（去重）
	host    string
	agentID string
}

const (
	confPath  = "/etc/zjagent/agent.json"
	stateDir  = "/var/lib/zjagent"
	spoolPath = stateDir + "/events.jsonl"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "run":
		run()
	case "install":
		fmt.Println("请使用面板生成的安装脚本（curl -fsSL ... | sh），或在面板「主机」页点安装")
	case "uninstall":
		purge := hasFlag(os.Args[2:], "--purge")
		script := agent.UninstallScript(purge)
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			os.Exit(1)
		}
	case "version":
		fmt.Printf("zjagent %s (proto %s, %s/%s)\n", buildinfo.Version, agent.Proto, runtime.GOOS, runtime.GOARCH)
	case "doctor":
		doctor()
	case "audit":
		auditCLI()
	case "update":
		fmt.Println("请由面板下发升级指令（主机页 → 升级），或重新执行安装脚本")
	default:
		usage()
	}
}

func usage() {
	fmt.Println("用法: zjagent <run|install|uninstall|version|doctor|audit|update>")
	fmt.Println("  run        前台运行（systemd 调用）")
	fmt.Println("  uninstall  卸载（--purge 同时清理状态目录）")
	fmt.Println("  audit      <ssh|accounts|persistence|exposure|webshell|firewall> 本地只读检查，输出 JSON")
}

// auditCLI 让运维可在目标机直接跑只读检查（与面板下发同一套实现）。
func auditCLI() {
	if len(os.Args) < 3 {
		fmt.Println("用法: zjagent audit <ssh|accounts|persistence|exposure|webshell|firewall>")
		return
	}
	cmds := map[string]string{
		"ssh": "ssh_audit", "accounts": "accounts_audit", "persistence": "persistence_audit",
		"exposure": "exposure_audit", "webshell": "webshell_audit", "firewall": "firewall_status",
	}
	cmd, ok := cmds[os.Args[2]]
	if !ok {
		fmt.Println("未知检查类型: " + os.Args[2])
		os.Exit(1)
	}
	c := &client{}
	ok2, out := c.exec(cmd, nil)
	if !ok2 {
		fmt.Println(out)
		os.Exit(1)
	}
	fmt.Println(out)
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func loadConfig() config {
	var c config
	b, err := os.ReadFile(confPath)
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	// 环境变量优先，便于容器/调试场景
	if v := os.Getenv("ZJAGENT_PANEL"); v != "" {
		c.Panel = v
	}
	if v := os.Getenv("ZJAGENT_TOKEN"); v != "" {
		c.Token = v
	}
	if c.Version == "" {
		c.Version = buildinfo.Version
	}
	return c
}

func saveConfig(c config) {
	_ = os.MkdirAll(filepath.Dir(confPath), 0o755)
	b, _ := json.Marshal(c)
	_ = os.WriteFile(confPath, b, 0o600)
}

func run() {
	cfg := loadConfig()
	if cfg.Panel == "" {
		fmt.Fprintln(os.Stderr, "缺少面板地址：请检查 /etc/zjagent/agent.json")
		os.Exit(1)
	}
	_ = os.MkdirAll(stateDir, 0o755)
	c := &client{cfg: cfg, seen: map[string]string{}}
	c.host, _ = os.Hostname()
	c.agentID = readOrCreateID()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c.loop(ctx)
}

func readOrCreateID() string {
	p := stateDir + "/agent.id"
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	_ = os.WriteFile(p, []byte(id), 0o600)
	return id
}

// loop 建立连接 → 握手 → 收发；断开按退避重连，永不退出（交给 systemd 兜底）。
func (c *client) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.connect(ctx)
		if err != nil {
			logf("连接失败: %v（%s 后重试）", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *client) connect(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	url := wsURL(c.cfg.Panel)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return err
	}
	if c.conn != nil {
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}
	c.conn = conn
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		c.conn = nil
	}()

	skew := int64(0)
	if t, err := httpGetInt(c.cfg.Panel); err == nil {
		skew = t
	}
	c.send(map[string]any{
		"type": "hello", "token": c.cfg.Token, "agentId": c.agentID,
		"version": buildinfo.Version, "proto": agent.Proto,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": c.host,
		"clockSkewMs": skew,
	})
	logf("已连接 %s", url)

	// 心跳与指标：15s 一次
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(15 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				m := collect()
				c.send(map[string]any{"type": "heartbeat", "metrics": m})
			}
		}
	}()
	defer func() { <-done }()

	// 补传断网期间的事件
	c.flushSpool()

	for {
		readCtx, rc := context.WithTimeout(ctx, 90*time.Second)
		mt, data, err := conn.Read(readCtx)
		rc()
		if err != nil {
			return err
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch s(msg["type"]) {
		case "welcome":
			if tok := s(msg["token"]); tok != "" && tok != c.cfg.Token {
				c.cfg.Token = tok
				saveConfig(c.cfg)
				logf("已换取长期 token")
			}
		case "cmd":
			var cm cmdMsg
			_ = json.Unmarshal(data, &cm)
			go c.handleCmd(cm)
		}
	}
}

func (c *client) send(v any) error {
	b, _ := json.Marshal(v)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c.conn == nil {
		return fmt.Errorf("未连接")
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}

// handleCmd 指令串行处理（单机动作互斥，避免两把锁同时改 sshd 配置）。
func (c *client) handleCmd(cm cmdMsg) {
	c.seenMu.Lock()
	if prev, ok := c.seen[cm.ID]; ok {
		c.seenMu.Unlock()
		_ = c.send(map[string]any{"type": "cmdResult", "id": cm.ID, "kind": cm.Kind, "ok": true, "output": prev})
		return
	}
	c.seenMu.Unlock()

	ok, out := c.exec(cm.Kind, cm.Params)

	c.seenMu.Lock()
	if len(c.seen) > 500 { // 防无限增长：清空重建即可，重复指令会重新执行一次
		c.seen = map[string]string{}
	}
	c.seen[cm.ID] = out
	c.seenMu.Unlock()

	_ = c.send(map[string]any{"type": "cmdResult", "id": cm.ID, "kind": cm.Kind, "ok": ok, "output": out})
}

func (c *client) exec(kind string, p map[string]any) (bool, string) {
	switch kind {
	case "ping":
		return true, "pong"
	case "metrics":
		b, _ := json.Marshal(collect())
		return true, string(b)
	case "list_ports":
		b, _ := json.Marshal(collectPorts())
		return true, string(b)
	case "ssh_audit":
		return sshAudit()
	case "accounts_audit":
		return accountsAudit()
	case "persistence_audit":
		return persistenceAudit()
	case "exposure_audit":
		return exposureAudit()
	case "webshell_audit":
		return webshellAudit()
	case "firewall_status":
		return firewallStatus()
	case "ban_ip":
		return banIP(p)
	case "unban_ip":
		return unbanIP(p)
	case "uninstall":
		// 先回 ack，再由脱离会话的子进程完成卸载（当前进程会被 systemctl stop 杀掉）
		go func() {
			time.Sleep(2 * time.Second)
			_ = exec.Command("sh", "-c", "nohup /usr/local/bin/zjagent uninstall --purge >/tmp/zjagent-uninstall.log 2>&1 &").Start()
		}()
		purge := true
		_ = purge
		return true, "已开始卸载，服务将停止"
	case "upgrade":
		return c.upgrade(p)
	default:
		return false, "未知指令: " + kind
	}
}

// upgrade 双槽切换：下载 → 校验 → 落到 versions/<v> → 切 symlink → 由 systemd 重启。
// 失败不切 symlink，旧版本继续跑。
func (c *client) upgrade(p map[string]any) (bool, string) {
	path, _ := p["path"].(string)
	url, _ := p["url"].(string)
	sum, _ := p["sha256"].(string)
	ver, _ := p["version"].(string)
	if url == "" && path != "" {
		url = strings.TrimRight(c.cfg.Panel, "/") + path + "?token=" + c.cfg.Token
	}
	if url == "" || ver == "" {
		return false, "缺少 url/version"
	}
	tmp := "/tmp/zjagent-upgrade"
	if err := download(url, tmp); err != nil {
		return false, "下载失败: " + err.Error()
	}
	if sum != "" {
		if got := fileSum(tmp); got != sum {
			return false, "校验失败：期望 " + sum + "，实际 " + got
		}
	}
	dst := "/usr/local/lib/zjagent/versions/" + ver
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return false, err.Error()
	}
	if err := os.Rename(tmp, dst+"/zjagent"); err != nil {
		if err := copyFile(tmp, dst+"/zjagent"); err != nil {
			return false, err.Error()
		}
	}
	_ = os.Chmod(dst+"/zjagent", 0o755)
	if err := os.Symlink(dst+"/zjagent", "/usr/local/bin/zjagent.tmp"); err == nil {
		_ = os.Rename("/usr/local/bin/zjagent.tmp", "/usr/local/bin/zjagent")
	}
	go func() {
		time.Sleep(2 * time.Second)
		_ = exec.Command("systemctl", "restart", "zjagent").Run()
	}()
	return true, "已切换到 " + ver + "，即将重启"
}

func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fileSum(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// ---- 采集 ----

func collect() agent.Metrics {
	m := agent.Metrics{T: time.Now(), Ports: collectPorts()}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fs := strings.Fields(string(b))
		if len(fs) >= 3 {
			m.Load1, _ = strconv.ParseFloat(fs[0], 64)
			m.Load5, _ = strconv.ParseFloat(fs[1], 64)
			m.Load15, _ = strconv.ParseFloat(fs[2], 64)
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				m.MemTotal = v * 1024
			case "MemAvailable:":
				m.MemAvailable = v * 1024
			}
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			u, _ := strconv.ParseFloat(f[0], 64)
			m.Uptime = uint64(u)
		}
	}
	m.Conns = countConns()
	m.SSHSessions = countSSH()
	m.DiskTotal, m.DiskUsed = diskUsage("/")
	m.CPU = cpuPercent()
	return m
}

func collectPorts() []agent.Port {
	ports := []agent.Port{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		proto := "tcp"
		if strings.HasSuffix(f, "tcp6") {
			proto = "tcp6"
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for _, line := range lines[1:] {
			fld := strings.Fields(line)
			if len(fld) < 4 || fld[3] != "0A" { // 0A = LISTEN
				continue
			}
			parts := strings.Split(fld[1], ":")
			if len(parts) != 2 {
				continue
			}
			port, _ := strconv.ParseInt(parts[1], 16, 32)
			addr := hexAddr(parts[0])
			if addr == "00000000000000000000000000000000" || addr == "00000000" {
				addr = "0.0.0.0"
			}
			ports = append(ports, agent.Port{Proto: proto, Addr: addr, Port: int(port)})
		}
	}
	// 同端口 IPv4/IPv6 双栈只保留一条，避免表格刷屏。
	// 若先出现的是回环地址、后出现的是非回环监听，优先保留后者（暴露面审计需要真实对外地址）。
	idx := map[int]int{}
	out := ports[:0]
	for _, p := range ports {
		if i, ok := idx[p.Port]; ok {
			if isLoopbackAddr(out[i].Addr) && !isLoopbackAddr(p.Addr) {
				out[i] = p
			}
			continue
		}
		idx[p.Port] = len(out)
		out = append(out, p)
	}
	return out
}

func hexAddr(h string) string {
	if strings.Contains(h, ":") || len(h) == 32 {
		// IPv6 只展示后 8 位，避免表格过长
		if len(h) >= 8 {
			return "v6:" + strings.ToLower(h[len(h)-8:])
		}
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return h
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

func countConns() int {
	n := 0
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if b, err := os.ReadFile(f); err == nil {
			n += len(strings.Split(string(b), "\n")) - 2
		}
	}
	return n
}

func countSSH() int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		cmdline := "/proc/" + e.Name() + "/cmdline"
		b, err := os.ReadFile(cmdline)
		if err != nil {
			continue
		}
		s := strings.ReplaceAll(string(b), "\x00", " ")
		if strings.HasPrefix(s, "sshd: ") || strings.Contains(s, "/sshd ") {
			if strings.Contains(s, "@") {
				n++
			}
		}
	}
	return n
}

func diskUsage(path string) (total, used uint64) { return diskUsageOS(path) }

// cpuPercent 用 /proc/stat 两次采样差值近似瞬时占用（首次返回 0）。
var (
	cpuMu   sync.Mutex
	cpuPrev struct{ idle, total uint64 }
)

func cpuPercent() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line := strings.Split(string(b), "\n")[0]
	f := strings.Fields(line)
	if len(f) < 8 {
		return 0
	}
	var idle, total uint64
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	cpuMu.Lock()
	defer cpuMu.Unlock()
	prev := cpuPrev
	cpuPrev = struct{ idle, total uint64 }{idle, total}
	if prev.total == 0 || total <= prev.total {
		return 0
	}
	dt := total - prev.total
	di := idle - prev.idle
	return float64(dt-di) * 100 / float64(dt)
}

// ---- SSH 基线审计（只读） ----

type finding struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Risk   string `json:"risk"` // ok | warn | critical
	Advice string `json:"advice"`
}

func sshAudit() (bool, string) {
	findings := []finding{}
	path := "/etc/ssh/sshd_config"
	b, err := os.ReadFile(path)
	if err != nil {
		return false, "读取 sshd_config 失败: " + err.Error()
	}
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 {
			kv[strings.ToLower(f[0])] = strings.ToLower(f[1])
		}
	}
	get := func(k, def string) string {
		if v, ok := kv[k]; ok {
			return v
		}
		return def
	}
	add := func(k, v, risk, advice string) {
		findings = append(findings, finding{Key: k, Value: v, Risk: risk, Advice: advice})
	}

	permitRoot := get("permitrootlogin", "prohibit-password")
	if permitRoot == "yes" {
		add("PermitRootLogin", permitRoot, "critical", "禁止 root 直接登录：PermitRootLogin no")
	} else {
		add("PermitRootLogin", permitRoot, "ok", "")
	}
	pwd := get("passwordauthentication", "yes")
	if pwd == "yes" {
		add("PasswordAuthentication", pwd, "warn", "建议改为密钥登录，关闭密码登录")
	} else {
		add("PasswordAuthentication", pwd, "ok", "")
	}
	maxAuth := get("maxauthtries", "6")
	if n, _ := strconv.Atoi(maxAuth); n > 4 {
		add("MaxAuthTries", maxAuth, "warn", "建议不超过 4")
	} else {
		add("MaxAuthTries", maxAuth, "ok", "")
	}
	port := get("port", "22")
	add("Port", port, "ok", "非默认端口可减少扫描噪音（可选）")
	empty := get("permitemptypasswords", "no")
	if empty == "yes" {
		add("PermitEmptyPasswords", empty, "critical", "立即关闭空密码登录")
	} else {
		add("PermitEmptyPasswords", empty, "ok", "")
	}
	proto := get("protocol", "2")
	if proto != "2" {
		add("Protocol", proto, "critical", "只允许 SSH 协议 2")
	} else {
		add("Protocol", proto, "ok", "")
	}

	pb, _ := json.Marshal(map[string]any{"findings": findings, "checkedAt": time.Now()})
	return true, string(pb)
}

// ---- spool ----

func (c *client) flushSpool() {
	f, err := os.Open(spoolPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() && n < 200 {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if c.send(map[string]any{"type": "event", "event": e}) == nil {
			n++
		}
	}
	_ = os.Remove(spoolPath)
}

func doctor() {
	fmt.Println("环境自检：")
	fmt.Printf("  版本: %s (proto %s)\n", buildinfo.Version, agent.Proto)
	fmt.Printf("  OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if !isRoot() {
		fmt.Println("  [!] 非 root：封禁与加固动作将不可用")
	} else {
		fmt.Println("  [ok] root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		fmt.Println("  [!] 未检测到 systemd")
	} else {
		fmt.Println("  [ok] systemd")
	}
	if _, err := os.Stat("/etc/ssh/sshd_config"); err == nil {
		fmt.Println("  [ok] sshd_config 可读")
	}
	for _, p := range []string{"/var/log/auth.log", "/var/log/secure"} {
		if _, err := os.Stat(p); err == nil {
			fmt.Println("  [ok] 日志源 " + p)
		}
	}
	cfg := loadConfig()
	if cfg.Panel == "" {
		fmt.Println("  [!] 未配置面板地址")
	} else {
		fmt.Println("  [ok] 面板 " + cfg.Panel)
	}
	// 时钟偏移（超过 30s 会让日志判定失准）
	if skew, err := httpGetInt(cfg.Panel); err == nil {
		if skew > 30 || skew < -30 {
			fmt.Printf("  [!] 时钟偏移 %d 秒，请校准 NTP\n", skew)
		} else {
			fmt.Println("  [ok] 时钟偏移正常")
		}
	}
}

// httpGetInt 取面板时间与本地时间的差值（秒）。
func httpGetInt(panel string) (int64, error) {
	p := strings.TrimRight(panel, "/")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", p+"/api/agent/time", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var v struct {
		Ts int64 `json:"ts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, err
	}
	return v.Ts - time.Now().Unix(), nil
}

func wsURL(panel string) string {
	p := strings.TrimRight(panel, "/")
	switch {
	case strings.HasPrefix(p, "https://"):
		p = "wss://" + strings.TrimPrefix(p, "https://")
	case strings.HasPrefix(p, "http://"):
		p = "ws://" + strings.TrimPrefix(p, "http://")
	default:
		p = "ws://" + p
	}
	return p + "/api/agent/ws"
}

func logf(format string, args ...any) {
	fmt.Printf("[zjagent] "+format+"\n", args...)
}

// s 宽松取字符串，避免 Agent 侧 JSON 类型差异导致 panic。
func s(v any) string {
	if x, ok := v.(string); ok {
		return x
	}
	return ""
}
