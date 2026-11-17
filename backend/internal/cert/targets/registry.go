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
	Type   string `json:"type"`
	Title  string `json:"title"`
	Fields []certdns.FieldDef
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
			{Key: "hostId", Title: "SSH 主机 ID", Type: "text", Required: true},
			{Key: "certPath", Title: "证书远程路径", Type: "text", Default: "/etc/nginx/ssl/fullchain.pem"},
			{Key: "keyPath", Title: "私钥远程路径", Type: "text", Default: "/etc/nginx/ssl/privkey.pem"},
			{Key: "preScript", Title: "前置命令", Type: "textarea"},
			{Key: "postScript", Title: "后置命令（如 nginx -s reload）", Type: "textarea", Default: "nginx -s reload"},
		},
		Owner: "builtin",
		Runner: func(args DeployArgs) (string, error) {
			hostID := uint(str2int(str2str(args.Config["hostId"])))
			if hostID == 0 {
				return "", fmt.Errorf("hostId 必填")
			}
			client, err := sshDialer(hostID)
			if err != nil {
				return "", err
			}
			defer client.Close()
			certPath := strOr(str2str(args.Config["certPath"]), "/etc/nginx/ssl/fullchain.pem")
			keyPath := strOr(str2str(args.Config["keyPath"]), "/etc/nginx/ssl/privkey.pem")
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
		Type: "local_script", Title: "执行本地脚本",
		Fields: []certdns.FieldDef{
			{Key: "script", Title: "脚本内容", Type: "textarea", Required: true, Help: "环境变量: CERT_DOMAIN / CERT_PATH / CERT_KEY_PATH / CERT_EXPIRES"},
		},
		Owner: "builtin",
		Runner: func(args DeployArgs) (string, error) {
			script := strings.TrimSpace(str2str(args.Config["script"]))
			if script == "" {
				return "", fmt.Errorf("script 必填")
			}
			dir := filepath.Join(cfg.DataDir, "certs", fmt.Sprintf("%d", args.TaskID))
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(),
				"CERT_DOMAIN="+strings.Join(args.Domains, ","),
				"CERT_PATH="+filepath.Join(dir, "fullchain.pem"),
				"CERT_KEY_PATH="+filepath.Join(dir, "privkey.pem"),
				"CERT_EXPIRES="+args.NotAfter.Format("2006-01-02"),
			)
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

func strOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func sshHostLabel(client *ssh.Client) string {
	return client.RemoteAddr().String()
}
