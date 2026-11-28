// 主机防御：账号 / 持久化 / 暴露面基线审计，以及 nftables 主机层封禁。
//
// 设计约束：
//   - 只读审计绝不修改系统；封禁只操作独立表 inet zjpanel，不碰用户既有防火墙规则；
//   - 封禁项带 timeout，到期自动解封，避免永久锁死；
//   - 仅接受 IPv4（IPv6 单独建模后再开放），非法地址直接拒绝。
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type banItem struct {
	IP      string `json:"ip"`
	Expires string `json:"expires,omitempty"`
}

type fwInfo struct {
	Backend string `json:"backend"` // nftables | ufw | firewalld | iptables | none
	Active  bool   `json:"active"`
	Rules   int    `json:"rules"`
}

func auditPayload(findings []finding) (bool, string) {
	b, _ := json.Marshal(map[string]any{"findings": findings, "checkedAt": time.Now()})
	return true, string(b)
}

func addFinding(list *[]finding, key, value, risk, advice string) {
	*list = append(*list, finding{Key: key, Value: value, Risk: risk, Advice: advice})
}

// ---- 账号基线 ----

func accountsAudit() (bool, string) {
	findings := []finding{}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return false, "读取 /etc/passwd 失败: " + err.Error()
	}
	uid0 := []string{}
	loginUsers := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		name, uid, shell := f[0], f[3], f[6]
		if uid == "0" && name != "root" {
			uid0 = append(uid0, name)
		}
		switch shell {
		case "/sbin/nologin", "/usr/sbin/nologin", "/bin/false", "/usr/bin/false", "":
		default:
			if uid != "0" {
				loginUsers = append(loginUsers, name)
			}
		}
	}
	if len(uid0) > 0 {
		addFinding(&findings, "UID 0 账号", "root + "+strings.Join(uid0, ", "), "critical", "除 root 外的 UID 0 账号等同超级用户，确认来源并删除")
	} else {
		addFinding(&findings, "UID 0 账号", "仅 root", "ok", "")
	}
	addFinding(&findings, "可登录账号", fmt.Sprintf("%d 个：%s", len(loginUsers), strings.Join(loginUsers, ", ")), "ok", "离职/临时账号应及时删除或锁定")

	// /etc/shadow：权限与空密码
	if st, err := os.Stat("/etc/shadow"); err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			addFinding(&findings, "/etc/shadow 权限", st.Mode().Perm().String(), "critical", "chmod 640 /etc/shadow（root:shadow）")
		} else {
			addFinding(&findings, "/etc/shadow 权限", st.Mode().Perm().String(), "ok", "")
		}
	}
	empty := []string{}
	if sb, err := os.ReadFile("/etc/shadow"); err == nil {
		for _, line := range strings.Split(string(sb), "\n") {
			f := strings.Split(line, ":")
			if len(f) >= 2 && f[1] == "" {
				empty = append(empty, f[0])
			}
		}
	}
	if len(empty) > 0 {
		addFinding(&findings, "空密码账号", strings.Join(empty, ", "), "critical", "立即设置密码或锁定账号（passwd -l）")
	} else {
		addFinding(&findings, "空密码账号", "无", "ok", "")
	}

	// sudoers NOPASSWD
	nopass := []string{}
	files := []string{"/etc/sudoers"}
	if more, _ := filepath.Glob("/etc/sudoers.d/*"); len(more) > 0 {
		files = append(files, more...)
	}
	for _, f := range files {
		fb, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(fb), "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if strings.Contains(t, "NOPASSWD") {
				nopass = append(nopass, filepath.Base(f))
				break
			}
		}
	}
	if len(nopass) > 0 {
		addFinding(&findings, "sudo 免密", strings.Join(nopass, ", "), "warn", "确认 NOPASSWD 仅授予必要账号")
	} else {
		addFinding(&findings, "sudo 免密", "无", "ok", "")
	}

	// authorized_keys：权限过宽 = 任何人可加公钥
	badKeys := []string{}
	checked := 0
	checkKey := func(path string) {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			return
		}
		checked++
		if st.Mode().Perm()&0o022 != 0 {
			badKeys = append(badKeys, path+" "+st.Mode().Perm().String())
		}
	}
	checkKey("/root/.ssh/authorized_keys")
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		home := f[5]
		if home != "" && home != "/" && home != "/nonexistent" {
			checkKey(filepath.Join(home, ".ssh", "authorized_keys"))
		}
	}
	if len(badKeys) > 0 {
		addFinding(&findings, "authorized_keys 权限", strings.Join(badKeys, "; "), "critical", "改为 600，且目录 700，防止他人追加公钥")
	} else {
		addFinding(&findings, "authorized_keys 权限", fmt.Sprintf("%d 个文件正常", checked), "ok", "")
	}

	return auditPayload(findings)
}

// ---- 持久化基线 ----

var suspiciousRe = regexp.MustCompile(`(curl|wget|base64|/dev/tcp|bash -c|sh -c|nc |ncat|socat|/tmp/|/dev/shm)`)

func persistenceAudit() (bool, string) {
	findings := []finding{}
	files := []string{"/etc/crontab", "/etc/rc.local", "/etc/ld.so.preload"}
	for _, g := range []string{"/etc/cron.d/*", "/var/spool/cron/*", "/var/spool/cron/crontabs/*", "/etc/profile.d/*.sh"} {
		more, _ := filepath.Glob(g)
		files = append(files, more...)
	}
	suspicious := []string{}
	cronFiles := 0
	for _, f := range files {
		fb, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(f, "cron") {
			cronFiles++
		}
		for _, line := range strings.Split(string(fb), "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if suspiciousRe.MatchString(t) && !strings.Contains(f, "ld.so.preload") {
				suspicious = append(suspicious, filepath.Base(f)+": "+t)
			}
		}
	}
	addFinding(&findings, "计划任务文件", fmt.Sprintf("%d 个", cronFiles), "ok", "定期核对 crontab 来源")
	if len(suspicious) > 0 {
		addFinding(&findings, "可疑计划任务/启动项", strings.Join(suspicious, " | "), "critical", "含下载/编码/临时目录执行的定时任务高度可疑，逐条确认")
	} else {
		addFinding(&findings, "可疑计划任务/启动项", "无", "ok", "")
	}

	// ld.so.preload 是经典用户态 rootkit 落点
	if _, err := os.Stat("/etc/ld.so.preload"); err == nil {
		addFinding(&findings, "ld.so.preload", "存在", "critical", "确认预加载库来源，非业务需要应立即删除")
	} else {
		addFinding(&findings, "ld.so.preload", "不存在", "ok", "")
	}

	// systemd：近 7 天新增/修改的 unit + ExecStart 可疑命令
	units, _ := filepath.Glob("/etc/systemd/system/*.service")
	recent := []string{}
	badUnits := []string{}
	now := time.Now()
	for _, f := range units {
		st, err := os.Stat(f)
		if err == nil && st.ModTime().After(now.Add(-7*24*time.Hour)) {
			recent = append(recent, filepath.Base(f))
		}
		fb, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(fb), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "ExecStart=") && suspiciousRe.MatchString(t) {
				badUnits = append(badUnits, filepath.Base(f))
				break
			}
		}
	}
	addFinding(&findings, "systemd 单元", fmt.Sprintf("%d 个", len(units)), "ok", "")
	if len(recent) > 0 {
		sort.Strings(recent)
		addFinding(&findings, "近 7 天变更单元", strings.Join(recent, ", "), "warn", "新增服务需确认来源与二进制路径")
	}
	if len(badUnits) > 0 {
		addFinding(&findings, "可疑 systemd 单元", strings.Join(badUnits, ", "), "critical", "ExecStart 含下载/临时目录执行，立即核查")
	}

	return auditPayload(findings)
}

// ---- Webshell 特征扫描 ----

var webRoots = []string{"/var/www", "/usr/share/nginx/html", "/www", "/home", "/opt"}

var webExts = map[string]bool{
	".php": true, ".phtml": true, ".php5": true, ".php7": true,
	".jsp": true, ".jspx": true, ".asp": true, ".aspx": true, ".ashx": true,
}

var webSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

type webPattern struct {
	re    *regexp.Regexp
	label string
	risk  string
}

var webPatterns = []webPattern{
	{regexp.MustCompile(`eval\s*\(\s*\$_(POST|GET|REQUEST|COOKIE|FILES)`), "eval 执行请求参数", "critical"},
	{regexp.MustCompile(`assert\s*\(\s*\$_(POST|GET|REQUEST|COOKIE)`), "assert 执行请求参数", "critical"},
	{regexp.MustCompile(`(system|exec|shell_exec|passthru|popen|proc_open)\s*\(\s*\$_(POST|GET|REQUEST)`), "命令执行请求参数", "critical"},
	{regexp.MustCompile(`base64_decode\s*\(\s*\$_`), "base64 解码请求参数", "critical"},
	{regexp.MustCompile(`preg_replace\s*\(\s*['"][^'"]*e['"]`), "preg_replace /e 修饰符", "warn"},
	{regexp.MustCompile(`Runtime\s*\.\s*getRuntime\s*\(\s*\)\s*\.\s*exec`), "Java Runtime.exec", "warn"},
	{regexp.MustCompile(`ProcessStartInfo`), ".NET ProcessStartInfo", "warn"},
}

type webHit struct {
	path  string
	label string
	risk  string
}

func webshellAudit() (bool, string) {
	findings := []finding{}
	hits := []webHit{}
	scanned := 0
	truncated := false
	deadline := time.Now().Add(20 * time.Second)

	for _, root := range webRoots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 权限不足等直接跳过
			}
			if time.Now().After(deadline) || scanned >= 30000 || len(hits) >= 20 {
				truncated = true
				return filepath.SkipAll
			}
			if d.IsDir() {
				if webSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !webExts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			scanned++
			for _, p := range webPatterns {
				if p.re.Match(b) {
					hits = append(hits, webHit{path: path, label: p.label, risk: p.risk})
					break
				}
			}
			return nil
		})
	}

	value := fmt.Sprintf("已扫描 %d 个脚本文件", scanned)
	if truncated {
		value += "（达到时间/数量上限，结果可能不完整）"
	}
	addFinding(&findings, "脚本文件扫描", value, "ok", "覆盖 /var/www、nginx 默认目录、/home、/opt")

	if len(hits) == 0 {
		addFinding(&findings, "疑似 Webshell", "未发现", "ok", "")
		return auditPayload(findings)
	}
	crit, warn := 0, 0
	for _, h := range hits {
		if h.risk == "critical" {
			crit++
		} else {
			warn++
		}
	}
	risk := "warn"
	if crit > 0 {
		risk = "critical"
	}
	addFinding(&findings, "命中统计", fmt.Sprintf("%d 个文件（高危 %d / 可疑 %d）", len(hits), crit, warn), risk, "逐条人工复核，确认后隔离并排查上传入口")
	for _, h := range hits {
		key := "可疑脚本特征"
		if h.risk == "critical" {
			key = "疑似 Webshell"
		}
		addFinding(&findings, key, h.path, h.risk, "命中 "+h.label+"，确认来源与访问日志")
	}
	return auditPayload(findings)
}

// ---- 暴露面基线 ----

var riskyPorts = map[int]struct {
	Name string
	Risk string
}{
	21:    {"FTP", "warn"},
	23:    {"Telnet", "critical"},
	2375:  {"Docker API（未加密）", "critical"},
	3306:  {"MySQL", "critical"},
	5432:  {"PostgreSQL", "critical"},
	6379:  {"Redis", "critical"},
	9200:  {"Elasticsearch", "critical"},
	11211: {"Memcached", "critical"},
	27017: {"MongoDB", "critical"},
	5900:  {"VNC", "warn"},
}

// isLoopbackAddr 判断监听地址是否仅本机可达；0.0.0.0、私网 IP、公网 IP 都算对外/局域网暴露。
func isLoopbackAddr(addr string) bool {
	if addr == "0.0.0.0" || addr == "" {
		return false
	}
	if strings.HasPrefix(addr, "v6:") {
		// collectPorts 对 IPv6 只保留后 8 位，::1 的尾段是 00000001
		return addr == "v6:00000001"
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

func exposureAudit() (bool, string) {
	findings := []finding{}
	ports := collectPorts()
	public := []string{}
	risky := []string{}
	for _, p := range ports {
		if isLoopbackAddr(p.Addr) {
			continue
		}
		public = append(public, fmt.Sprintf("%d/%s", p.Port, p.Proto))
		if meta, ok := riskyPorts[p.Port]; ok {
			risky = append(risky, fmt.Sprintf("%d %s", p.Port, meta.Name))
			addFinding(&findings,
				fmt.Sprintf("高危端口 %d", p.Port),
				fmt.Sprintf("%s（%s）", meta.Name, p.Addr),
				meta.Risk,
				"仅监听内网地址或用防火墙限制来源；数据库/缓存类不应直接暴露公网",
			)
		}
	}
	if len(public) == 0 {
		addFinding(&findings, "对外监听端口", "无", "ok", "")
	} else {
		sort.Strings(public)
		risk := "ok"
		if len(risky) > 0 {
			risk = "warn"
		}
		addFinding(&findings, "对外监听端口", strings.Join(public, ", "), risk, "只保留业务必需端口，其余绑定 127.0.0.1")
	}

	fw := detectFirewall()
	if !fw.Active {
		addFinding(&findings, "主机防火墙", "未检测到生效的防火墙", "warn", "启用 nftables/ufw/firewalld，仅放行 22/80/443 等必要端口")
	} else {
		addFinding(&findings, "主机防火墙", fmt.Sprintf("%s（%d 条规则）", fw.Backend, fw.Rules), "ok", "")
	}

	return auditPayload(findings)
}

func detectFirewall() fwInfo {
	if out, err := exec.Command("nft", "list", "ruleset").Output(); err == nil {
		rules := strings.Count(string(out), "rule ")
		return fwInfo{Backend: "nftables", Active: rules > 0, Rules: rules}
	}
	if out, err := exec.Command("ufw", "status").Output(); err == nil {
		active := strings.Contains(string(out), "Status: active")
		r := 0
		if active {
			r = len(strings.Split(strings.TrimSpace(string(out)), "\n")) - 4
			if r < 0 {
				r = 0
			}
		}
		return fwInfo{Backend: "ufw", Active: active, Rules: r}
	}
	if out, err := exec.Command("firewall-cmd", "--state").Output(); err == nil && strings.Contains(string(out), "running") {
		return fwInfo{Backend: "firewalld", Active: true}
	}
	if out, err := exec.Command("iptables", "-S").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		rules := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "-A ") {
				rules++
			}
		}
		return fwInfo{Backend: "iptables", Active: rules > 0, Rules: rules}
	}
	return fwInfo{Backend: "none"}
}

// ---- nftables 主机层封禁（独立表，不动用户规则） ----

const zjTable = "zjpanel"

func nftRun(args ...string) (string, error) {
	out, err := exec.Command("nft", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func ensureZJTable() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return fmt.Errorf("未安装 nftables：请先安装（Debian/Ubuntu: apt install -y nftables）")
	}
	if _, err := nftRun("list", "table", "inet", zjTable); err == nil {
		return nil
	}
	script := `table inet zjpanel {
  set banned { type ipv4_addr; flags timeout; }
  chain input { type filter hook input priority 0; policy accept; ip saddr @banned drop; }
}
`
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, e2 := nftRun("list", "table", "inet", zjTable); e2 == nil {
			return nil
		}
		return fmt.Errorf("初始化 nftables 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func listBans() []banItem {
	out, err := nftRun("list", "set", "inet", zjTable, "banned")
	if err != nil {
		return []banItem{}
	}
	return parseBans(out)
}

// parseBans 解析 `nft list set` 输出中的 elements 行。
func parseBans(out string) []banItem {
	re := regexp.MustCompile(`([0-9]{1,3}(?:\.[0-9]{1,3}){3})(?: expires ([0-9a-z]+))?`)
	items := []banItem{}
	seen := map[string]bool{}
	// 只解析 elements = { ... } 行，避免把表头其它地址误当封禁项
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "elements") {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			ip := m[1]
			if seen[ip] {
				continue
			}
			seen[ip] = true
			items = append(items, banItem{IP: ip, Expires: m[2]})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].IP < items[j].IP })
	return items
}

func firewallStatus() (bool, string) {
	fw := detectFirewall()
	b, _ := json.Marshal(map[string]any{
		"backend": fw.Backend, "active": fw.Active, "rules": fw.Rules,
		"bans": listBans(), "checkedAt": time.Now(),
	})
	return true, string(b)
}

func banIP(p map[string]any) (bool, string) {
	ip := str(p["ip"])
	secs := intOf(p["seconds"], 3600)
	if ip == "" {
		return false, "缺少 IP"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return false, "仅支持合法 IPv4 地址"
	}
	ip = parsed.To4().String()
	// 白名单保护：回环/面板地址/私网（未开启 banPrivate 时）/策略白名单永不封禁，
	// 手动 API 与 AI 处置两条路径都经过这里
	if ipProtected(ip, currentPolicy()) {
		return false, ip + " 在保护名单内（回环/面板/私网/白名单），未封禁"
	}
	if secs < 60 {
		secs = 60
	}
	if secs > 7*24*3600 {
		secs = 7 * 24 * 3600
	}
	if err := ensureZJTable(); err != nil {
		return false, err.Error()
	}
	out, err := nftRun("add", "element", "inet", zjTable, "banned", "{", ip, "timeout", fmt.Sprintf("%ds", secs), "}")
	if err != nil {
		if strings.Contains(out, "already exists") || strings.Contains(out, "File exists") {
			return true, fmt.Sprintf("%s 已在封禁列表中", ip)
		}
		return false, "封禁失败: " + out
	}
	return true, fmt.Sprintf("已封禁 %s，%d 秒后自动解封", ip, secs)
}

func unbanIP(p map[string]any) (bool, string) {
	ip := str(p["ip"])
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return false, "仅支持合法 IPv4 地址"
	}
	ip = parsed.To4().String()
	out, err := nftRun("delete", "element", "inet", zjTable, "banned", ip)
	if err != nil {
		if strings.Contains(out, "No such file") || strings.Contains(out, "does not exist") {
			return true, ip + " 不在封禁列表中"
		}
		return false, "解封失败: " + out
	}
	return true, "已解封 " + ip
}

// str / intOf 是 agent 消息里常用的小工具（防御模块复用）。
func str(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intOf(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}
