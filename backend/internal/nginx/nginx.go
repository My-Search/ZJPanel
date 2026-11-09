package nginx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/pkg"
)

// Service 直接管理本机（容器内）nginx：配置文件树、站点可用/启用 symlink、nginx -t / reload。
// 与 nginx-ui 相同的模式：配置文件是唯一事实来源，面板保存后先测试再 reload，失败即回滚。

type Service struct {
	cfg *pkg.Config
	mu  sync.Mutex // 序列化 nginx -t / reload
}

func New(cfg *pkg.Config) *Service { return &Service{cfg: cfg} }

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateName 站点/文件名白名单，防路径穿越。
func ValidateName(name string) error {
	if name == "" || len(name) > 255 || !nameRe.MatchString(name) {
		return fmt.Errorf("非法名称: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("非法名称: %q", name)
	}
	return nil
}

func (s *Service) SitesAvailableDir() string { return filepath.Join(s.cfg.NginxPrefix, "sites-available") }
func (s *Service) SitesEnabledDir() string   { return filepath.Join(s.cfg.NginxPrefix, "sites-enabled") }

func (s *Service) AvailablePath(domain string) string {
	return filepath.Join(s.SitesAvailableDir(), domain+".conf")
}
func (s *Service) EnabledPath(domain string) string {
	return filepath.Join(s.SitesEnabledDir(), domain+".conf")
}
func (s *Service) MaintenancePath(domain string) string {
	return filepath.Join(s.SitesEnabledDir(), domain+"_maintenance.conf")
}

// ---- 命令 ----

func runCmd(bin string, args ...string) (string, error) {
	ctx, cancel := contextWithTimeout(20 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// nginxAvailable 检查 nginx 二进制是否可用（本地开发可能没有 nginx，此时跳过 test/reload）。
func (s *Service) nginxAvailable() bool {
	_, err := exec.LookPath(s.cfg.NginxBin)
	return err == nil
}

// TestConfig nginx -t
func (s *Service) TestConfig() (string, error) {
	if !s.nginxAvailable() {
		return "", fmt.Errorf("nginx 未安装（本地开发模式跳过测试）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := runCmd(s.cfg.NginxBin, "-t")
	return out, err
}

// Reload nginx -s reload（先测试，失败不动线上）
func (s *Service) Reload() (string, error) {
	if !s.nginxAvailable() {
		return "nginx 未安装，跳过 reload（本地开发模式）", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if out, err := runCmd(s.cfg.NginxBin, "-t"); err != nil {
		return out, fmt.Errorf("配置测试失败: %s", out)
	}
	return runCmd(s.cfg.NginxBin, "-s", "reload")
}

func (s *Service) Restart() (string, error) {
	if !s.nginxAvailable() {
		return "nginx 未安装，跳过 restart（本地开发模式）", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return runCmd(s.cfg.NginxBin, "-s", "reopen") // 容器内 nginx 由 entrypoint 常驻；reopen 重开日志
}

// Running 检查 nginx 进程是否存活
func (s *Service) Running() bool {
	out, err := runCmd("sh", "-c", "pidof nginx || pgrep -x nginx")
	return err == nil && strings.TrimSpace(out) != ""
}

// ---- 站点 conf 文件操作 ----

// EnableSite 创建 symlink 启用站点；maintenance 时先移除正式 symlink。
// Windows（本地开发）无 symlink 权限时回退为拷贝文件。
func (s *Service) EnableSite(domain string) error {
	if err := ValidateName(domain); err != nil {
		return err
	}
	src := s.AvailablePath(domain)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("站点配置不存在: %s", domain)
	}
	dst := s.EnabledPath(domain)
	_ = os.Remove(dst)
	_ = os.Remove(s.MaintenancePath(domain))
	if err := os.Symlink(src, dst); err != nil {
		b, rerr := os.ReadFile(src)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dst, b, 0o644)
	}
	return nil
}

func (s *Service) DisableSite(domain string) error {
	if err := ValidateName(domain); err != nil {
		return err
	}
	_ = os.Remove(s.EnabledPath(domain))
	return os.Remove(s.MaintenancePath(domain))
}

// SetMaintenance 维护态：移除正式 symlink，启用 _maintenance.conf（确保优先且唯一）。
func (s *Service) SetMaintenance(domain string, on bool) error {
	if err := ValidateName(domain); err != nil {
		return err
	}
	if on {
		_ = os.Remove(s.EnabledPath(domain))
		if _, err := os.Stat(s.MaintenancePath(domain)); err != nil {
			return fmt.Errorf("维护配置未生成")
		}
		return nil
	}
	_ = os.Remove(s.MaintenancePath(domain))
	if _, err := os.Stat(s.AvailablePath(domain)); err != nil {
		return nil // 站点已删除
	}
	return os.Symlink(s.AvailablePath(domain), s.EnabledPath(domain))
}

type SiteFileState struct {
	Domain      string `json:"domain"`
	Enabled     bool   `json:"enabled"`
	Maintenance bool   `json:"maintenance"`
}

// ListSiteStates 扫描 sites-available，返回各站点文件状态。
func (s *Service) ListSiteStates() []SiteFileState {
	var out []SiteFileState
	entries, err := os.ReadDir(s.SitesAvailableDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		domain := strings.TrimSuffix(e.Name(), ".conf")
		_, enabledErr := os.Lstat(s.EnabledPath(domain))
		_, maintErr := os.Stat(s.MaintenancePath(domain))
		out = append(out, SiteFileState{
			Domain:      domain,
			Enabled:     enabledErr == nil,
			Maintenance: maintErr == nil,
		})
	}
	return out
}

// ---- 通用配置文件树（nginx 主目录 + html 目录）----

type FileNode struct {
	Name string     `json:"name"`
	Path string     `json:"path"` // 相对 root 的路径
	Dir  bool       `json:"dir"`
	Root string     `json:"root"` // nginx | html
	Kids []FileNode `json:"kids,omitempty"`
}

func safeJoin(base, rel string) (string, error) {
	abs := filepath.Clean(filepath.Join(base, rel))
	if !strings.HasPrefix(abs, filepath.Clean(base)+string(os.PathSeparator)) && abs != filepath.Clean(base) {
		return "", fmt.Errorf("路径越界")
	}
	return abs, nil
}

// ListTree 列出目录树（两层懒加载：返回该目录直接子项）
func (s *Service) ListTree(root, rel string) ([]FileNode, error) {
	base := s.rootDir(root)
	if base == "" {
		return nil, fmt.Errorf("未知根: %s", root)
	}
	abs, err := safeJoin(base, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var nodes []FileNode
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childRel := rel
		if childRel == "" || childRel == "." {
			childRel = name
		} else {
			childRel = rel + "/" + name
		}
		nodes = append(nodes, FileNode{Name: name, Path: childRel, Dir: e.IsDir(), Root: root})
	}
	return nodes, nil
}

func (s *Service) rootDir(root string) string {
	switch root {
	case "nginx":
		return s.cfg.NginxPrefix
	case "html":
		return s.cfg.NginxHTMLDir
	case "logs":
		return s.cfg.NginxLogsDir
	}
	return ""
}

// ReadFile 读取配置文件内容（限 nginx/html/logs 根内）
func (s *Service) ReadFile(root, rel string) (string, error) {
	base := s.rootDir(root)
	if base == "" {
		return "", fmt.Errorf("未知根: %s", root)
	}
	abs, err := safeJoin(base, rel)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err == nil && fi.Size() > 2*1024*1024 {
		return "", fmt.Errorf("文件过大（>2MB）")
	}
	b, err := os.ReadFile(abs)
	return string(b), err
}

// WriteFile 备份后写入（备份到 DataDir/nginx-backup/<ts>/<root>/<name>）
func (s *Service) WriteFile(root, rel, content string) error {
	base := s.rootDir(root)
	if base == "" {
		return fmt.Errorf("未知根: %s", root)
	}
	if root == "logs" {
		return fmt.Errorf("日志目录只读")
	}
	abs, err := safeJoin(base, rel)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(abs), 0o755)
	if old, err := os.ReadFile(abs); err == nil {
		bak := filepath.Join(s.cfg.DataDir, "nginx-backup", time.Now().Format("20060102-150405")+"-"+filepath.Base(rel))
		_ = os.WriteFile(bak, old, 0o644)
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// SaveAndReload 写配置 → nginx -t → reload；测试失败则回滚。
func (s *Service) SaveAndReload(root, rel, content string) (string, error) {
	old := ""
	if b, err := s.ReadFile(root, rel); err == nil {
		old = b
	}
	if err := s.WriteFile(root, rel, content); err != nil {
		return "", err
	}
	if !s.nginxAvailable() {
		return "nginx 未安装，仅写入（本地开发模式）", nil
	}
	out, err := s.TestConfig()
	if err != nil {
		if old != "" {
			_ = s.WriteFile(root, rel, old) // 回滚
		} else {
			base := s.rootDir(root)
			if abs, jerr := safeJoin(base, rel); jerr == nil {
				_ = os.Remove(abs)
			}
		}
		return out, fmt.Errorf("nginx -t 未通过，已回滚: %s", strings.TrimSpace(errString(out)))
	}
	if out2, err2 := s.Reload(); err2 != nil {
		return out2, fmt.Errorf("reload 失败: %s", errString(out2))
	}
	return out, nil
}

func errString(s string) string {
	if s == "" {
		return "未知错误"
	}
	return s
}
