package plugin

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"zjpanel/backend/internal/pkg"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

func sortStrings(list []string) { sort.Strings(list) }

// unzipPlugin 解压插件 zip 到临时目录，校验 manifest 后移动到 plugins/<id>/。
func unzipPlugin(data []byte, root string) (string, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("zip 解析失败: %w", err)
	}
	tmp := filepath.Join(root, fmt.Sprintf(".tmp-%s", pkg.RandomHex(4)))
	_ = os.MkdirAll(tmp, 0o755)
	defer func() {
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
	}()
	// 定位根前缀（首个条目的第一段路径）
	prefix := ""
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		parts := strings.SplitN(filepath.ToSlash(f.Name), "/", 2)
		if len(parts) == 2 {
			prefix = parts[0] + "/"
		}
		break
	}
	var manifestData []byte
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(f.Name)
		if !strings.HasPrefix(name, "/") && strings.Contains(name, "..") {
			return "", "", fmt.Errorf("zip 中含非法路径: %s", name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.HasPrefix(rel, ".") {
			continue
		}
		dest := filepath.Join(tmp, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(dest), 0o755)
		rc, err := f.Open()
		if err != nil {
			return "", "", err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", "", err
		}
		if rel == "plugin.yaml" {
			manifestData = data
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return "", "", err
		}
	}
	if manifestData == nil {
		return "", "", fmt.Errorf("zip 中缺少 plugin.yaml")
	}
	// 校验 id
	mf, err := parseManifestBytes(manifestData)
	if err != nil {
		return "", "", err
	}
	final := filepath.Join(root, mf.ID)
	if _, err := os.Stat(final); err == nil {
		return "", "", fmt.Errorf("插件目录已存在: %s", mf.ID)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", "", err
	}
	tmp = "" // 已移动，避免 defer 删除
	return final, mf.ID, nil
}
