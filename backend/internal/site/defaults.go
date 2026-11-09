package site

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zjpanel/backend/internal/pkg"
)

//go:embed pages/404.html
var default404 string

//go:embed pages/50x.html
var default50x string

//go:embed pages/index.html
var defaultIndex string

//go:embed pages/maintenance.html
var defaultMaintenance string

// EnsureDefaults 首次启动写入默认页（404/50x/首页）与全局维护页模板（可被 UI 编辑）。
func EnsureDefaults(cfg *pkg.Config) {
	writeIfMissing := func(path, content string) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, []byte(content), 0o644)
		}
	}
	writeIfMissing(filepath.Join(cfg.NginxHTMLDir, "404.html"), default404)
	writeIfMissing(filepath.Join(cfg.NginxHTMLDir, "50x.html"), default50x)
	writeIfMissing(filepath.Join(cfg.NginxHTMLDir, "index.html"), defaultIndex)
	writeIfMissing(filepath.Join(cfg.DataDir, "maintenance.html"), defaultMaintenance)
}

// GlobalMaintenanceTemplate 读取全局维护页模板。
func GlobalMaintenanceTemplate(cfg *pkg.Config) (string, error) {
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "maintenance.html"))
	if err != nil {
		return defaultMaintenance, nil
	}
	return string(b), nil
}

// SaveGlobalMaintenanceTemplate 保存全局维护页模板。
func SaveGlobalMaintenanceTemplate(cfg *pkg.Config, content string) error {
	return os.WriteFile(filepath.Join(cfg.DataDir, "maintenance.html"), []byte(content), 0o644)
}

// RenderMaintenance 渲染维护页：站点级覆盖优先，否则全局模板。变量：{{domain}} {{time}} {{message}}。
func RenderMaintenance(cfg *pkg.Config, siteOverride, domain, message string) string {
	tpl := ""
	if strings.TrimSpace(siteOverride) != "" {
		tpl = siteOverride
	} else {
		tpl, _ = GlobalMaintenanceTemplate(cfg)
	}
	msg := message
	if msg == "" {
		msg = "站点正在维护，请稍后访问"
	}
	r := strings.NewReplacer(
		"{{domain}}", domain,
		"{{time}}", time.Now().Format("2006-01-02 15:04:05"),
		"{{message}}", msg,
	)
	return r.Replace(tpl)
}

var _ = fmt.Sprintf // 保持 fmt 引用
