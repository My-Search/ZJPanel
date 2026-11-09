package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/pkg"
)

// Service 版本检查与一键更新：
// 检查 = 拉取版本清单（GitHub releases/latest 或自定义 {version,url} JSON），semver 对比；
// 应用 = 经一次性 docker:cli 容器执行 `docker compose pull && up -d`（面板容器自身被替换也不中断）。

type Service struct {
	cfg *pkg.Config
}

func New(cfg *pkg.Config) *Service { return &Service{cfg: cfg} }

func CurrentVersion() string { return buildinfo.Version }

func (s *Service) Check() (map[string]any, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(s.cfg.UpdateURL)
	if err != nil {
		return nil, fmt.Errorf("版本清单拉取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("版本清单拉取失败: HTTP %d", resp.StatusCode)
	}
	current := CurrentVersion()
	latest, url, notes := "", "", ""
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if tn, ok := raw["tag_name"].(string); ok && tn != "" {
		// GitHub releases/latest
		latest = strings.TrimPrefix(tn, "v")
		notes, _ = raw["html_url"].(string)
	} else if v, ok := raw["version"].(string); ok {
		latest = strings.TrimPrefix(v, "v")
		url, _ = raw["downloadUrl"].(string)
		notes, _ = raw["notes"].(string)
	} else {
		return nil, fmt.Errorf("版本清单格式无法识别")
	}
	avail := semverLess(current, latest)
	return map[string]any{
		"current": current, "latest": latest, "updateAvailable": avail,
		"url": url, "notes": notes,
	}, nil
}

func semverLess(cur, latest string) bool {
	if cur == latest || latest == "" {
		return false
	}
	if strings.HasPrefix(cur, "0.0.0") || strings.Contains(cur, "dev") {
		return true // dev 版本永远提示可更新
	}
	c := splitSemver(cur)
	l := splitSemver(latest)
	for i := 0; i < 3; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

func splitSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// Apply 一键更新：启动一次性 updater 容器执行 compose pull + up -d。
func (s *Service) Apply() (string, error) {
	deployDir := os.Getenv("ZJPANEL_DEPLOY_DIR")
	if deployDir == "" {
		return "", fmt.Errorf("面板容器未挂载部署目录（ZJPANEL_DEPLOY_DIR），请在宿主机执行 update.sh 升级")
	}
	// docker CLI 可用性（面板容器内自带）
	if out, err := exec.Command("docker", "version", "--format", "{{.Client.Version}}").CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker CLI 不可用: %s", out)
	}
	// 幂等：清理可能残留的 updater 容器
	_ = exec.Command("docker", "rm", "-f", "zjpanel-updater").Run()
	script := "docker compose pull && docker compose up -d --remove-orphans"
	cmd := exec.Command("docker", "run", "-d", "--rm", "--name", "zjpanel-updater",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", deployDir+":/work",
		"-w", "/work",
		"docker:cli",
		"sh", "-c", script,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("启动更新容器失败: %w", err)
	}
	return "更新容器已启动（zjpanel-updater），拉取新镜像并重建面板后自动完成，稍后面板将自动切换到新版本。", nil
}
