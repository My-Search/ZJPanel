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

const (
	maxPluginFiles     = 500
	maxPluginFileBytes = 32 << 20 // 单文件上限 32MB
	maxPluginTotalSize = 64 << 20 // 解压总上限 64MB
)

// ValidID 校验插件 ID 合法性（防路径穿越）。
func ValidID(id string) bool {
	return nameRe.MatchString(id)
}

// unzipPlugin 解压插件 zip 到临时目录，校验 manifest 后移动到 plugins/<id>/。
func unzipPlugin(data []byte, root string) (string, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("zip 解析失败: %w", err)
	}
	if len(zr.File) > maxPluginFiles {
		return "", "", fmt.Errorf("zip 条目过多（>%d）", maxPluginFiles)
	}
	tmp := filepath.Join(root, fmt.Sprintf(".tmp-%s", pkg.RandomHex(4)))
	cleanTmp := filepath.Clean(tmp)
	_ = os.MkdirAll(cleanTmp, 0o755)
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
		if len(parts) == 2 && parts[0] != "" && !strings.Contains(parts[0], "..") {
			prefix = parts[0] + "/"
		}
		break
	}
	var manifestData []byte
	var totalBytes int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("zip 中禁止包含符号链接: %s", f.Name)
		}
		name := filepath.ToSlash(f.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return "", "", fmt.Errorf("zip 中含非法路径: %s", name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.HasPrefix(rel, ".") || strings.HasPrefix(rel, "/") {
			continue
		}
		dest := filepath.Clean(filepath.Join(cleanTmp, filepath.FromSlash(rel)))
		if !strings.HasPrefix(dest, cleanTmp+string(os.PathSeparator)) {
			return "", "", fmt.Errorf("zip 条目路径越界: %s", f.Name)
		}
		_ = os.MkdirAll(filepath.Dir(dest), 0o755)
		rc, err := f.Open()
		if err != nil {
			return "", "", err
		}
		entryData, err := io.ReadAll(io.LimitReader(rc, maxPluginFileBytes+1))
		_ = rc.Close()
		if err != nil {
			return "", "", err
		}
		if int64(len(entryData)) > maxPluginFileBytes {
			return "", "", fmt.Errorf("zip 单文件过大（>32MB）: %s", f.Name)
		}
		totalBytes += int64(len(entryData))
		if totalBytes > maxPluginTotalSize {
			return "", "", fmt.Errorf("zip 解压总体积过大（>64MB）")
		}
		if rel == "plugin.yaml" {
			manifestData = entryData
		}
		if err := os.WriteFile(dest, entryData, 0o644); err != nil {
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
