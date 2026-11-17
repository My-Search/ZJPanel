package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 目录浏览（导入向导的文件夹选择组件）：
// 列出指定目录的一级子目录并标记是否含 docker-compose.yml。
// 面板容器只可见挂载进来的目录（默认 /opt:/opt），容器路径边界即安全边界。

// BrowseDir 列出目录下的子目录（含 compose 检测标记）。
func (s *Service) BrowseDir(path string) (map[string]any, error) {
	if strings.TrimSpace(path) == "" {
		path = s.cfg.ComposeRoot
	}
	abs := filepath.Clean(path)
	if !isAbsPath(abs) {
		abs = filepath.Clean(filepath.Join(s.cfg.ComposeRoot, path))
	}
	if strings.Contains(abs, "..") {
		return nil, fmt.Errorf("非法路径")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("无法读取目录 %s: %w（确认该目录已挂载进容器）", abs, err)
	}
	out := []map[string]any{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(abs, e.Name())
		hasCompose := false
		if _, err := parseCompose(child); err == nil {
			hasCompose = true
		}
		out = append(out, map[string]any{
			"name": e.Name(), "path": child, "hasCompose": hasCompose,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	// 当前目录自身是否含 compose 文件（供「导入此目录」按钮状态，避免额外探测请求误弹错误）
	curHasCompose := false
	if _, err := parseCompose(abs); err == nil {
		curHasCompose = true
	}
	return map[string]any{
		"path": abs, "dirs": out, "hasCompose": curHasCompose,
	}, nil
}

// ParseDir 解析指定目录的 compose 服务（导入向导第二步用）。
func (s *Service) ParseDir(dir string) ([]ServiceInfo, string, error) {
	clean := filepath.Clean(dir)
	if !isAbsPath(clean) {
		return nil, "", fmt.Errorf("需要绝对路径")
	}
	if strings.Contains(clean, "..") {
		return nil, "", fmt.Errorf("非法路径")
	}
	if _, err := os.Stat(clean); err != nil {
		return nil, "", fmt.Errorf("目录不存在（确认已挂载进容器）: %s", clean)
	}
	f, err := parseCompose(clean)
	if err != nil {
		return nil, "", err
	}
	return servicesOf(f), firstNonEmpty(f.Name, filepath.Base(clean)), nil
}

// isAbsPath 跨平台绝对路径判断：Windows 下 "/opt"（容器内 Linux 路径）也视为绝对。
func isAbsPath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\")
}
