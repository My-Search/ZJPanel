package pkg

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config 面板运行配置：全部来自环境变量，容器部署时由 docker-compose/entrypoint 注入。
// 服务层只依赖本包，不依赖 boot，避免循环引用。
type Config struct {
	DataDir       string // 面板数据目录（SQLite/证书/插件/geoip）
	AppDir        string // 程序安装目录（内置插件前端资产 /app/plugin-assets）
	Port          int    // 面板 HTTP 端口
	NginxPrefix   string // nginx 配置根（/etc/nginx）
	NginxLogsDir  string // nginx 日志目录
	NginxHTMLDir  string // nginx 默认页目录
	NginxBin      string // nginx 可执行文件（默认 "nginx"，走 PATH）
	ComposeRoot   string // docker-compose 项目扫描根目录
	UpdateURL     string // 版本清单 URL（检查更新）
	DeployDir     string // 面板自身 compose 部署目录（宿主机路径），容器自更新据此改写镜像 tag
	ChallengePort int    // ACME HTTP-01 本地挑战端口（nginx 将 /.well-known/acme-challenge 转发到此）
	TrustedProxies []string // 可信反代 CIDR：仅来自这些地址的 X-Forwarded-For 才参与 ClientIP 解析
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimRight(v, "/\\")
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return def
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			return n
		}
	}
	return def
}

// LoadConfig 读取配置；无环境变量且非 Linux 时回退到本地 dev 目录（Windows 开发用）。
func LoadConfig() *Config {
	cfg := &Config{
		DataDir:       env("ZJPANEL_DATA", "/data"),
		AppDir:        env("ZJPANEL_APP_DIR", "/app"),
		Port:          envInt("ZJPANEL_PORT", 9000),
		NginxPrefix:   env("ZJPANEL_NGINX_PREFIX", "/etc/nginx"),
		NginxLogsDir:  env("ZJPANEL_NGINX_LOGS", "/var/log/nginx"),
		NginxHTMLDir:  env("ZJPANEL_NGINX_HTML", "/usr/share/nginx/html"),
		NginxBin:      env("ZJPANEL_NGINX_BIN", "nginx"),
		ComposeRoot:   env("ZJPANEL_COMPOSE_ROOT", "/compose"),
		UpdateURL:     env("ZJPANEL_UPDATE_URL", "https://api.github.com/repos/My-Search/ZJPanel/releases/latest"),
		DeployDir:    env("ZJPANEL_DEPLOY_DIR", ""),
		ChallengePort: envInt("ZJPANEL_CHALLENGE_PORT", 9100),
	}
	// 可信代理：默认仅本机回环（单容器部署中同容器 nginx 经 127.0.0.1 反代面板）。
	// 特殊值 "none" = 完全不信任任何代理头；多值逗号分隔 CIDR。
	switch tp := env("ZJPANEL_TRUSTED_PROXIES", "127.0.0.1/8,::1/128"); strings.ToLower(strings.TrimSpace(tp)) {
	case "none":
		cfg.TrustedProxies = nil
	default:
		for _, s := range strings.Split(tp, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.TrustedProxies = append(cfg.TrustedProxies, s)
			}
		}
	}
	if _, err := os.Stat("/proc/version"); err != nil && os.Getenv("ZJPANEL_DATA") == "" {
		dev := "./dev-data"
		_ = os.MkdirAll(dev, 0o755)
		cfg.DataDir = dev
		cfg.NginxPrefix = dev + "/nginx"
		cfg.NginxLogsDir = dev + "/nginx/logs"
		cfg.NginxHTMLDir = dev + "/nginx/html"
		cfg.ComposeRoot = dev + "/compose"
		// AppDir dev 回退：从 backend/ 目录运行，仓库根的 plugin-assets/ 即内置插件前端资产
		cfg.AppDir = "../"
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return cfg
}

func (c *Config) EnsureDirs() {
	for _, d := range []string{
		c.DataDir,
		filepath.Join(c.DataDir, "certs"),
		filepath.Join(c.DataDir, "plugins"),
		filepath.Join(c.DataDir, "geoip"),
		filepath.Join(c.DataDir, "nginx-backup"),
		c.NginxPrefix,
		c.NginxLogsDir,
		c.NginxHTMLDir,
		filepath.Join(c.NginxPrefix, "sites-available"),
		filepath.Join(c.NginxPrefix, "sites-enabled"),
		filepath.Join(c.NginxPrefix, "conf.d"),
	} {
		_ = os.MkdirAll(d, 0o755)
	}
}
