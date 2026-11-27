package targets

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	certdns "zjpanel/backend/internal/cert/dns"
	"zjpanel/backend/internal/nginx"
	"zjpanel/backend/internal/pkg"
)

// DeployTargetRegistry 部署目标注册表（certd 的 deployTo 插件思路）。
// 内置：nginx_site（本地 nginx 证书写入+reload）、ssh_upload（SFTP 上传+前后置脚本）、local_script。
// 插件可经 SDK 注册扩展目标。

type DeployArgs struct {
	TaskID   uint
	Domains  []string
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
	Config   map[string]any
}

type TargetDef struct {
	Type   string                                `json:"type"`
	Title  string                                `json:"title"`
	Fields []certdns.FieldDef                    `json:"fields"`
	Runner func(args DeployArgs) (string, error) `json:"-"`
	Owner  string                                `json:"owner"` // builtin | 插件 id
}

type TargetRegistry struct {
	mu sync.RWMutex
	m  map[string]*TargetDef
}

func NewTargetRegistry() *TargetRegistry {
	return &TargetRegistry{m: map[string]*TargetDef{}}
}

func (r *TargetRegistry) Register(def *TargetDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[def.Type] = def
}

func (r *TargetRegistry) UnregisterByOwner(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.m {
		if v.Owner == owner {
			delete(r.m, k)
		}
	}
}

func (r *TargetRegistry) List() []*TargetDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*TargetDef, 0, len(r.m))
	for _, v := range r.m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func (r *TargetRegistry) Get(typ string) (*TargetDef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[typ]
	if !ok {
		return nil, fmt.Errorf("部署目标类型不存在: %s", typ)
	}
	return p, nil
}

// RegisterBuiltins 内置部署目标。
// reloadFn: nginx reload 回调（由 boot 注入 site 服务，避免循环依赖）。
// sshDialer: terminal.DialHost 回调。
func RegisterBuiltins(r *TargetRegistry, cfg *pkg.Config, reloadFn func() (string, error), sshDialer func(hostID uint) (*ssh.Client, error)) {
	r.Register(&TargetDef{
		Type: "nginx_site", Title: "部署到本机 Nginx 站点",
		Fields: []certdns.FieldDef{
			{Key: "domain", Title: "目标域名", Type: "text", Help: "默认取证书主域名"},
		},
		Owner: "builtin",
		Runner: func(args DeployArgs) (string, error) {
			domain := strings.TrimSpace(str2str(args.Config["domain"]))
			if domain == "" {
				domain = mainDomain(args.Domains)
			}
			if domain == "" {
				return "", fmt.Errorf("无法确定目标域名")
			}
			if err := nginx.ValidateName(domain); err != nil {
				return "", fmt.Errorf("目标域名非法: %w", err)
			}
			dir := filepath.Join(cfg.NginxPrefix, "ssl", domain)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), args.CertPEM, 0o644); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), args.KeyPEM, 0o600); err != nil {
				return "", err
			}
			if out, err := reloadFn(); err != nil {
				return "", fmt.Errorf("证书已写入 %s，但 nginx 重载失败: %s", dir, out)
			}
			return fmt.Sprintf("证书已写入 %s 并重载 nginx", dir), nil
		},
	})
	r.Register(&TargetDef{
		Type: "ssh_upload", Title: "上传到 SSH 主机",
		Fields: []certdns.FieldDef{
			{Key: "hostId", Title: "SSH 主机", Type: "select", OptionsSource: "hosts", Required: true, Help: "仅显示「主机」页中配置了 SSH 密码/私钥的主机；证书经 SFTP 上传到该主机。内置本机请改用「部署到本机 Nginx 站点」"},
			{Key: "certPath", Title: "证书（公钥 fullchain）远程路径", Type: "text", Default: "/etc/nginx/ssl/{domain}/fullchain.pem", Help: "远程主机上证书文件的完整路径，{domain} 自动替换为证书主域名；上级目录不存在会自动创建"},
			{Key: "keyPath", Title: "私钥远程路径", Type: "text", Default: "/etc/nginx/ssl/{domain}/privkey.pem", Help: "远程主机上私钥文件的完整路径，{domain} 自动替换为证书主域名；上传后建议权限 600"},
			{Key: "preScript", Title: "上传前命令（可选）", Type: "textarea", Help: "上传证书前在目标主机执行，可用环境变量 CERT_DOMAIN / CERT_PATH / CERT_KEY_PATH"},
			{Key: "postScript", Title: "上传后命令（如 nginx -s reload）", Type: "textarea", Default: "nginx -s reload", Help: "上传完成后在目标主机执行，常用于重载 Web 服务"},
		},
		Owner: "builtin",
		Runner: func(args DeployArgs) (string, error) {
			hostID := anyToUint(args.Config["hostId"])
			if hostID == 0 {
				return "", fmt.Errorf("请选择 SSH 主机")
			}
			client, err := sshDialer(hostID)
			if err != nil {
				return "", err
			}
			defer client.Close()
			certPath := strOr(str2str(args.Config["certPath"]), defaultCertPath(args.Domains, "fullchain.pem"))
			keyPath := strOr(str2str(args.Config["keyPath"]), defaultCertPath(args.Domains, "privkey.pem"))
			certPath = applyDomainPlaceholder(certPath, args.Domains)
			keyPath = applyDomainPlaceholder(keyPath, args.Domains)
			envs := []string{
				"CERT_DOMAIN=" + strings.Join(args.Domains, ","),
				"CERT_PATH=" + certPath,
				"CERT_KEY_PATH=" + keyPath,
				"CERT_EXPIRES=" + args.NotAfter.Format("2006-01-02"),
			}
			// 前置命令
			if pre := strings.TrimSpace(str2str(args.Config["preScript"])); pre != "" {
				if out, err := sshRun(client, pre, envs); err != nil {
					return "", fmt.Errorf("前置命令失败: %s", out)
				}
			}
			// SFTP 上传
			scli, err := sftp.NewClient(client)
			if err != nil {
				return "", fmt.Errorf("SFTP 连接失败: %w", err)
			}
			defer scli.Close()
			for path, data := range map[string][]byte{certPath: args.CertPEM, keyPath: args.KeyPEM} {
				if err := sftpWrite(scli, path, data); err != nil {
					return "", fmt.Errorf("上传 %s 失败: %w", path, err)
				}
			}
			// 后置命令
			post := strings.TrimSpace(str2str(args.Config["postScript"]))
			if post == "" {
				post = "nginx -s reload"
			}
			out, err := sshRun(client, post, envs)
			if err != nil {
				return "", fmt.Errorf("后置命令失败: %s", out)
			}
			return fmt.Sprintf("已上传到 %s:%s / %s 并执行后置命令", sshHostLabel(client), certPath, keyPath), nil
		},
	})
	r.Register(&TargetDef{
		Type: "local_script", Title: "执行脚本 / 命令（本机或 SSH 主机）",
		Fields: []certdns.FieldDef{
			{Key: "hostId", Title: "目标主机（留空 = 面板本机）", Type: "select", OptionsSource: "hosts", Required: false, Help: "选择 SSH 主机后通过 SSH 在该主机执行；留空则在面板所在主机执行"},
			{Key: "script", Title: "执行命令 / 脚本内容", Type: "textarea", Required: true, Help: "本机用 sh -c 执行；选择 SSH 主机后通过 SSH 执行。环境变量: CERT_DOMAIN / CERT_PATH / CERT_KEY_PATH / CERT_EXPIRES"},
		},
		Owner: "builtin",
		Runner: func(args DeployArgs) (string, error) {
			script := strings.TrimSpace(str2str(args.Config["script"]))
			if script == "" {
				return "", fmt.Errorf("命令/脚本内容必填")
			}
			dir := filepath.Join(cfg.DataDir, "certs", fmt.Sprintf("%d", args.TaskID))
			envs := []string{
				"CERT_DOMAIN=" + strings.Join(args.Domains, ","),
				"CERT_PATH=" + filepath.Join(dir, "fullchain.pem"),
				"CERT_KEY_PATH=" + filepath.Join(dir, "privkey.pem"),
				"CERT_EXPIRES=" + args.NotAfter.Format("2006-01-02"),
			}
			// 选择了 SSH 主机：复用主机凭据拨号，在远程主机执行命令
			if hostID := anyToUint(args.Config["hostId"]); hostID > 0 {
				client, err := sshDialer(hostID)
				if err != nil {
					return "", err
				}
				defer client.Close()
				out, err := sshRun(client, script, envs)
				if err != nil {
					return "", fmt.Errorf("远程命令执行失败: %s", out)
				}
				return fmt.Sprintf("已在 %s 执行命令: %s", sshHostLabel(client), strings.TrimSpace(out)), nil
			}
			// 留空：在面板所在主机用 sh -c 执行
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), envs...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("脚本失败: %s", out)
			}
			return "脚本执行成功: " + strings.TrimSpace(string(out)), nil
		},
	})
}

// ---- SSH 辅助 ----

func sshRun(client *ssh.Client, script string, envs []string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	for _, e := range envs {
		if i := strings.Index(e, "="); i > 0 {
			_ = sess.Setenv(e[:i], e[i+1:])
		}
	}
	out, err := sess.CombinedOutput(script)
	return string(out), err
}

func sftpWrite(scli *sftp.Client, path string, data []byte) error {
	_ = scli.MkdirAll(filepath.Dir(path))
	f, err := scli.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func mainDomain(domains []string) string {
	for _, d := range domains {
		if !strings.HasPrefix(d, "*.") {
			return d
		}
	}
	if len(domains) > 0 {
		return strings.TrimPrefix(domains[0], "*.")
	}
	return ""
}

// defaultCertPath 远程证书/私钥的默认路径：/etc/nginx/ssl/<主域名>/<文件名>。
// 与内置 nginx_site 的 <NginxPrefix>/ssl/<域名>/ 目录约定保持一致。
func defaultCertPath(domains []string, name string) string {
	domain := strings.TrimSpace(mainDomain(domains))
	if domain == "" {
		return "/etc/nginx/ssl/" + name
	}
	return "/etc/nginx/ssl/" + domain + "/" + name
}

// applyDomainPlaceholder 把前端展示用默认值里的 {domain} 替换为证书主域名。
// 直接调用 API 建任务时也兼容该占位符，避免把字面量写进远程路径。
func applyDomainPlaceholder(path string, domains []string) string {
	domain := strings.TrimSpace(mainDomain(domains))
	if domain == "" || !strings.Contains(path, "{domain}") {
		return path
	}
	return strings.ReplaceAll(path, "{domain}", domain)
}

func str2str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func str2int(s string) int64 {
	n := int64(0)
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// anyToUint 兼容前端 select 提交的字符串 ID 与 JSON 数字（float64/int/int64/uint）。
func anyToUint(v any) uint {
	switch n := v.(type) {
	case nil:
		return 0
	case string:
		return uint(str2int(n))
	case float64:
		return uint(n)
	case int:
		return uint(n)
	case int64:
		return uint(n)
	case uint:
		return n
	default:
		return 0
	}
}

func strOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func sshHostLabel(client *ssh.Client) string {
	return client.RemoteAddr().String()
}
