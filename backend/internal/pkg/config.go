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
	Port          int    // 面板 HTTP 端口
	NginxPrefix   string // nginx 配置根（/etc/nginx）
	NginxLogsDir  string // nginx 日志目录
	NginxHTMLDir  string // nginx 默认页目录
	NginxBin      string // nginx 可执行文件（默认 "nginx"，走 PATH）
	ComposeRoot   string // docker-compose 项目扫描根目录
	UpdateURL     string // 版本清单 URL（检查更新）
	ChallengePort int    // ACME HTTP-01 本地挑战端口（nginx 将 /.well-known/acme-challenge 转发到此）
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
		Port:          envInt("ZJPANEL_PORT", 9000),
		NginxPrefix:   env("ZJPANEL_NGINX_PREFIX", "/etc/nginx"),
		NginxLogsDir:  env("ZJPANEL_NGINX_LOGS", "/var/log/nginx"),
		NginxHTMLDir:  env("ZJPANEL_NGINX_HTML", "/usr/share/nginx/html"),
		NginxBin:      env("ZJPANEL_NGINX_BIN", "nginx"),
		ComposeRoot:   env("ZJPANEL_COMPOSE_ROOT", "/compose"),
		UpdateURL:     env("ZJPANEL_UPDATE_URL", "https://api.github.com/repos/My-Search/ZJPanel/releases/latest"),
		ChallengePort: envInt("ZJPANEL_CHALLENGE_PORT", 9100),
	}
	if _, err := os.Stat("/proc/version"); err != nil && os.Getenv("ZJPANEL_DATA") == "" {
		dev := "./dev-data"
		_ = os.MkdirAll(dev, 0o755)
		cfg.DataDir = dev
		cfg.NginxPrefix = dev + "/nginx"
		cfg.NginxLogsDir = dev + "/nginx/logs"
		cfg.NginxHTMLDir = dev + "/nginx/html"
		cfg.ComposeRoot = dev + "/compose"
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
