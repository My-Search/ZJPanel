package waf

import (
	"net/url"
	"regexp"
	"strings"

	libinjection "github.com/corazawaf/libinjection-go"
)

// scan 一次请求的检测视图：原始串 + 多轮解码串 + 查询串的值列表。
// 只在需要时才解析/解码，避免为「无参数 GET 静态请求」付多余成本。
type scan struct {
	rawPath  string // 解码后的路径（不含查询串）
	rawQuery string // 原始查询串
	pathD    string // 路径（解码 3 层）
	queryD   string // 查询串（解码 3 层）
	ua       string
	referer  string
	values   []string // 查询串解码后的参数值
	parsed   bool
}

func newScan(req Request) *scan {
	path, query := splitPath(req.Path)
	return &scan{
		rawPath:  path,
		rawQuery: query,
		pathD:    decodeLayers(path, 3),
		queryD:   decodeLayers(query, 3),
		ua:       req.UA,
		referer:  decodeLayers(req.Referer, 2),
	}
}

// queryValues 解码后的参数值（解析失败时整串作为一个值，异常格式本身就值得按单值判）。
func (s *scan) queryValues() []string {
	if s.parsed {
		return s.values
	}
	s.parsed = true
	if s.queryD == "" {
		return nil
	}
	v, err := url.ParseQuery(s.queryD)
	if err != nil || len(v) == 0 {
		s.values = []string{s.queryD}
		return s.values
	}
	for _, vals := range v {
		for _, one := range vals {
			if one != "" {
				s.values = append(s.values, one)
			}
		}
	}
	// 参数名也可能是注入点（?id'=1），把键也带进值列表一起判
	for k := range v {
		if strings.ContainsAny(k, "'\"<>()") {
			s.values = append(s.values, k)
		}
	}
	return s.values
}

// builtinRules 内置规则表。ID 稳定（落盘文件按 ID 覆盖开关/动作），新增规则只追加。
func builtinRules() []Rule {
	return []Rule{
		{ID: "sqli", Name: "SQL 注入", Category: CatSQLi, Severity: SevHigh, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "libinjection 词法判定 + 高危 SQL 关键字兜底，覆盖查询串参数值"},
		{ID: "xss", Name: "XSS 跨站脚本", Category: CatXSS, Severity: SevHigh, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "对查询串参数值做 libinjection 判定，并要求含可执行上下文信号（标签/事件/伪协议）"},
		{ID: "rce", Name: "命令/代码执行", Category: CatRCE, Severity: SevHigh, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "命令拼接、jndi 注入、Runtime.exec、exec/system 等函数调用特征"},
		{ID: "lfi", Name: "路径穿越/文件读取", Category: CatLFI, Severity: SevHigh, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "../ 穿越序列、/etc/passwd、WEB-INF 等任意文件读取特征"},
		{ID: "webshell", Name: "Webshell 访问", Category: CatWebshell, Severity: SevHigh, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "脚本扩展名 + cmd/exec/eval 等控制参数的组合特征"},
		{ID: "scanner", Name: "扫描器 UA", Category: CatScanner, Severity: SevMedium, Action: ActionBlock, Enabled: true, Builtin: true,
			Desc: "sqlmap/nikto/nuclei/fscan 等已知攻击工具 User-Agent（不含 curl/wget 等通用客户端）"},
		{ID: "sensitive", Name: "敏感路径探测", Category: CatSensitive, Severity: SevMedium, Action: ActionObserve, Enabled: true, Builtin: true,
			Desc: ".env/.git/config、phpmyadmin、actuator 等泄漏面探测；默认只观察，误伤面大"},
	}
}

// ---- SQL 注入 ----

// sqlMeta 高危 SQL 片段：libinjection 认不出的纯 DDL/配置语句靠这一层兜底。
var sqlMeta = regexp.MustCompile(`(?i)(?:union\s+(?:all\s+)?select|information_schema|sleep\s*\(\s*\d|benchmark\s*\(|xp_cmdshell|sp_configure|waitfor\s+delay|into\s+(?:out|dump)file|load_file\s*\(|updatexml\s*\(|extractvalue\s*\(|(?:or|and)\s+\d+\s*=\s*\d+|concat\s*\(|char\s*\(\s*\d+|0x[0-9a-f]{8,})`)

// sqlSignal SQLi 第二信号：libinjection 命中后仍要求出现引号/注释/SQL 结构词，
// 压掉「1<2 and 3>2」这类散文比较被词法引擎误判的情况（SamWaf 同样采用多信号判定）。
var sqlSignal = regexp.MustCompile(`(?i)(?:['"]|--|/\*|;\s*(?:drop|select|insert|update|delete)|union\s+(?:all\s+)?select|select\s+[^=]{1,80}\s+from|insert\s+into|update\s+[^=]{1,60}\s+set|delete\s+from|drop\s+table|sleep\s*\(|benchmark\s*\(|information_schema|xp_cmdshell|waitfor\s+delay|into\s+(?:out|dump)file|load_file\s*\()`)

func checkSQLi(s *scan) (string, bool) {
	vals := s.queryValues()
	for _, v := range vals {
		if len(v) < 3 {
			continue
		}
		if ok, fp := libinjection.IsSQLi(v); ok {
			if sqlSignal.MatchString(v) || strings.Contains(v, "=") {
				return clip(v+" "+fp, 160), true
			}
		}
	}
	if s.queryD != "" && sqlMeta.MatchString(s.queryD) && strings.ContainsAny(s.queryD, "'\"=()") {
		return clip(s.queryD, 160), true
	}
	return "", false
}

// ---- XSS ----

var reXSSExecSignal = regexp.MustCompile(`(?i)<[a-z!/]|on\w+\s*=|javascript:|srcdoc\s*=|data:text/html`)

func xssHit(v string) bool {
	if !strings.ContainsAny(v, "<>\"'()`/") {
		return false
	}
	dv := decodeEntities(v)
	return libinjection.IsXSS(dv) && reXSSExecSignal.MatchString(dv)
}

func checkXSS(s *scan) (string, bool) {
	for _, v := range s.queryValues() {
		if v == "" {
			continue
		}
		if xssHit(v) {
			return clip(v, 160), true
		}
	}
	if strings.Contains(strings.ToLower(s.pathD), "<") && xssHit(s.pathD) {
		return clip(s.pathD, 160), true
	}
	return "", false
}

// ---- 命令/代码执行 ----

var rcePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\$\{jndi:(?:ldap|rmi|dns|http)://`),
	regexp.MustCompile(`Runtime\s*\.\s*getRuntime\s*\(\s*\)\s*\.\s*exec`),
	regexp.MustCompile(`(?i)ProcessBuilder|ProcessImpl`),
	regexp.MustCompile(`(?i)(?:exec|system|passthru|shell_exec|popen|proc_open|pcntl_exec|assert|eval|create_function)\s*\(`),
	regexp.MustCompile(`(?i)(?:;|\||&&|\|\|)\s*(?:cat|ls|id|whoami|uname|wget|curl|bash|sh|nc|ncat|python\d?|perl|ruby|powershell|cmd)\s+[^\s]`),
	regexp.MustCompile(`(?:/bin/(?:sh|bash)|/usr/bin/(?:wget|curl))`),
}

func checkRCE(s *scan) (string, bool) {
	full := s.pathD
	if s.queryD != "" {
		full += "?" + s.queryD
	}
	for _, re := range rcePatterns {
		if loc := re.FindStringIndex(full); loc != nil {
			start := loc[0] - 20
			if start < 0 {
				start = 0
			}
			end := loc[1] + 20
			if end > len(full) {
				end = len(full)
			}
			return clip(full[start:end], 160), true
		}
	}
	return "", false
}

// ---- 路径穿越 / 任意文件读取 ----

var lfiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|[/\\])\.\.(?:[/\\]|$)`),
	regexp.MustCompile(`(?:^|[?&])\.\.(?:[?&]|$)`),
	regexp.MustCompile(`(?i)/etc/(?:passwd|shadow|group|hosts)`),
	regexp.MustCompile(`(?i)(?:WEB-INF|META-INF)/(?:web\.xml|application\.xml)`),
	regexp.MustCompile(`(?i)(?:proc/self/environ|/var/log/(?:auth|nginx|apache))`),
	regexp.MustCompile(`(?i)(?:^|[/\\])windows[/\\](?:win|system32)`),
}

func checkLFI(s *scan) (string, bool) {
	full := s.pathD
	if s.queryD != "" {
		full += "?" + s.queryD
	}
	for _, re := range lfiPatterns {
		if re.MatchString(full) {
			return clip(full, 160), true
		}
	}
	return "", false
}

// ---- Webshell ----

var (
	reScriptExt  = regexp.MustCompile(`(?i)\.(?:php\d?|phtml|jspx?|asp|aspx|ashx|asa|cgi|pl)$`)
	reShellParam = regexp.MustCompile(`(?i)(?:cmd|exec|shell|eval|assert|base64_decode|passthru|phpinfo)\s*=\s*[^\s&]`)
	reShellName  = regexp.MustCompile(`(?i)(?:/[^/]*?(?:shell|cmd|backdoor|c99|r57|webshell)[^/]*)\.(?:php\d?|jspx?|asp|aspx|ashx)\b`)
)

func checkWebshell(s *scan) (string, bool) {
	if reShellName.MatchString(s.pathD) {
		return clip(s.pathD, 160), true
	}
	if reScriptExt.MatchString(s.pathD) && reShellParam.MatchString(s.queryD) {
		return clip(s.rawPath+"?"+s.rawQuery, 160), true
	}
	return "", false
}

// ---- 扫描器 UA ----

var reScannerUA = regexp.MustCompile(`(?i)(?:sqlmap|nikto|nmap|masscan|zmap|dirbuster|dirb|gobuster|wfuzz|ffuf|wpscan|joomscan|droopescan|acunetix|w3af|netsparker|appscan|openvas|nessus|nuclei|zgrab|fscan|goby|vega|jaeles|xray|awvs|whatweb|skipfish|arachni|censys|shodan|qualys|hydra|medusa|wfuzz)`)

func checkScanner(s *scan) (string, bool) {
	if s.ua == "" {
		return "", false // 空 UA 有大量正常客户端，交给自定义规则处理，不默认封
	}
	if reScannerUA.MatchString(s.ua) {
		return clip(s.ua, 160), true
	}
	return "", false
}

// ---- 敏感路径 ----

var sensitivePaths = []string{
	"/.env", "/.git/", "/.git/config", "/.svn/", "/.ds_store", "/.aws/", "/.ssh/",
	"/wp-login.php", "/wp-admin", "/xmlrpc.php", "/wp-config",
	"/phpmyadmin", "/pma/", "/myadmin", "/adminer",
	"/actuator", "/console", "/manager/html", "/solr/", "/jolokia",
	"/config.php", "/config.json", "/backup", "/db.php", "/server-status",
	"/cgi-bin/", "/vendor/", "/.well-known/../",
}

func checkSensitive(s *scan) (string, bool) {
	lp := strings.ToLower(s.pathD)
	for _, p := range sensitivePaths {
		if strings.Contains(lp, p) {
			return clip(s.pathD, 160), true
		}
	}
	return "", false
}
