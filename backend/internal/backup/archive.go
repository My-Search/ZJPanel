package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zjpanel/backend/internal/buildinfo"
)

// collectedFile 待打包文件：宿主机绝对路径 + 归档内相对路径。
type collectedFile struct {
	src     string
	archive string
}

// collect 按所选项目收集文件清单（不读取内容，仅列路径与大小）。
func (s *Service) collect(items []string) ([]collectedFile, error) {
	var files []collectedFile
	addDir := func(root, archivePrefix string, skipDirs map[string]bool) error {
		root = filepath.Clean(root)
		st, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录尚未生成：跳过，不算错误
			}
			return err
		}
		if !st.IsDir() {
			return nil
		}
		return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // 单文件不可读不阻断整体打包
			}
			if info.IsDir() {
				if skipDirs != nil && skipDirs[filepath.Base(p)] {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			archive := filepath.ToSlash(filepath.Join(archivePrefix, rel))
			files = append(files, collectedFile{src: p, archive: archive})
			return nil
		})
	}

	for _, it := range items {
		switch it {
		case ItemDatabase:
			// SQLite 主库 + WAL/SHM 一并打包，避免恢复时丢未 checkpoint 的事务。
			for _, ext := range []string{"", "-wal", "-shm"} {
				p := filepath.Join(s.cfg.DataDir, "zjpanel.db"+ext)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					files = append(files, collectedFile{src: p, archive: "db/" + filepath.Base(p)})
				}
			}
		case ItemCerts:
			_ = addDir(filepath.Join(s.cfg.DataDir, "certs"), "certs", nil)
		case ItemSecurity:
			p := filepath.Join(s.cfg.DataDir, "security", "policy.json")
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				files = append(files, collectedFile{src: p, archive: "security/policy.json"})
			}
		case ItemPlugins:
			_ = addDir(filepath.Join(s.cfg.DataDir, "plugins-data"), "plugins-data", nil)
		case ItemNginx:
			for _, sub := range []string{"sites-available", "sites-enabled", "conf.d"} {
				_ = addDir(filepath.Join(s.cfg.NginxPrefix, sub), filepath.Join("nginx", sub), nil)
			}
		case ItemCompose:
			if err := s.collectCompose(&files); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// collectCompose 扫描 ComposeRoot 一级目录，收录各项目的 compose 文件与 zjpanel.json 声明。
func (s *Service) collectCompose(files *[]collectedFile) error {
	root := s.cfg.ComposeRoot
	st, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !st.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	composeNames := []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		// 收录该目录下的 compose 文件与声明文件（仅顶层，不递归应用内部数据目录）。
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if de.IsDir() {
				continue
			}
			name := de.Name()
			isCompose := false
			for _, cn := range composeNames {
				if name == cn {
					isCompose = true
					break
				}
			}
			if !isCompose && name != "zjpanel.json" {
				continue
			}
			src := filepath.Join(dir, name)
			archive := filepath.ToSlash(filepath.Join("compose", e.Name(), name))
			*files = append(*files, collectedFile{src: src, archive: archive})
		}
	}
	return nil
}

// Build 按所选项目生成备份包，返回包信息（文件名、大小、清单、SHA256）。
func (s *Service) Build(items []string) (*BackupRecord, error) {
	items = NormalizeItems(items)
	files, err := s.collect(items)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.backupsDir, 0o755); err != nil {
		return nil, err
	}

	now := time.Now()
	name := backupName(now)
	path := filepath.Join(s.backupsDir, name)
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	var manifestFiles []ManifestFile
	hash := sha256.New()
	total := int64(0)

	for _, f := range files {
		st, err := os.Stat(f.src)
		if err != nil || st.IsDir() {
			continue
		}
		fh, err := os.Open(f.src)
		if err != nil {
			continue
		}
		hdr := &tar.Header{
			Name:    f.archive,
			Mode:    int64(st.Mode().Perm()),
			Size:    st.Size(),
			ModTime: st.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			fh.Close()
			return nil, err
		}
		if _, err := io.Copy(tw, fh); err != nil {
			fh.Close()
			return nil, err
		}
		fh.Close()
		manifestFiles = append(manifestFiles, ManifestFile{
			Source: f.src, Archive: f.archive, Size: st.Size(),
		})
		total += st.Size()
	}

	// 清单写入包内 _manifest.json
	manifest := &Manifest{
		Version:   1,
		CreatedAt: now,
		PanelVer:  buildinfo.Version,
		Items:     items,
		Files:     manifestFiles,
	}
	mb, _ := json.Marshal(manifest)
	if err := tw.WriteHeader(&tar.Header{
		Name: "_manifest.json", Mode: 0o644, Size: int64(len(mb)), ModTime: now,
	}); err == nil {
		_, _ = tw.Write(mb)
	}

	_ = tw.Close()
	_ = gw.Close()
	out.Close()

	// 计算整包 SHA256 与最终大小。
	if hf, err := os.Open(path); err == nil {
		if _, err := io.Copy(hash, hf); err == nil {
			_ = hf.Close()
		}
	}
	finalStat, _ := os.Stat(path)
	rec := &BackupRecord{
		Name:      name,
		Path:      path,
		Size:      0,
		CreatedAt: now,
		Items:     items,
		FileCount: len(manifestFiles),
		Uncompressed: total,
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
	}
	if finalStat != nil {
		rec.Size = finalStat.Size()
	}
	// 旁落清单 json，便于列表不拆包展示。
	_ = writeManifest(s.backupsDir, name, manifest)
	return rec, nil
}

// Restore 从备份包恢复所选项目（数据库/配置/声明写回对应位置）。
// 返回恢复的文件清单与跳过项；调用方应在低峰期执行，必要时重启面板/重载 nginx。
func (s *Service) Restore(path string, items []string) (*RestoreResult, error) {
	items = NormalizeItems(items)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("非有效的 gzip 备份包: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	res := &RestoreResult{Items: items}
	restoreSet := map[string]bool{}
	for _, it := range items {
		restoreSet[it] = true
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "_manifest.json" || hdr.FileInfo().IsDir() {
			continue
		}
		item := itemOfArchive(hdr.Name)
		if item != "" && !restoreSet[item] {
			res.Skipped = append(res.Skipped, hdr.Name)
			continue
		}
		dest, err := s.restoreTarget(hdr.Name)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		out.Close()
		res.Restored = append(res.Restored, hdr.Name)
	}
	return res, nil
}

// restoreTarget 把包内归档路径映射回宿主机目标路径。
func (s *Service) restoreTarget(archive string) (string, error) {
	archive = filepath.ToSlash(strings.TrimPrefix(archive, "/"))
	switch {
	case strings.HasPrefix(archive, "db/"):
		return filepath.Join(s.cfg.DataDir, filepath.Base(archive)), nil
	case strings.HasPrefix(archive, "certs/"):
		rel := strings.TrimPrefix(archive, "certs/")
		return filepath.Join(s.cfg.DataDir, "certs", rel), nil
	case archive == "security/policy.json":
		return filepath.Join(s.cfg.DataDir, "security", "policy.json"), nil
	case strings.HasPrefix(archive, "plugins-data/"):
		rel := strings.TrimPrefix(archive, "plugins-data/")
		return filepath.Join(s.cfg.DataDir, "plugins-data", rel), nil
	case strings.HasPrefix(archive, "nginx/"):
		rel := strings.TrimPrefix(archive, "nginx/")
		return filepath.Join(s.cfg.NginxPrefix, rel), nil
	case strings.HasPrefix(archive, "compose/"):
		rel := strings.TrimPrefix(archive, "compose/")
		return filepath.Join(s.cfg.ComposeRoot, rel), nil
	}
	return "", fmt.Errorf("未知归档路径: %s", archive)
}

// itemOfArchive 根据包内路径判断所属备份项。
func itemOfArchive(archive string) string {
	archive = filepath.ToSlash(archive)
	switch {
	case strings.HasPrefix(archive, "db/"):
		return ItemDatabase
	case strings.HasPrefix(archive, "certs/"):
		return ItemCerts
	case strings.HasPrefix(archive, "security/"):
		return ItemSecurity
	case strings.HasPrefix(archive, "plugins-data/"):
		return ItemPlugins
	case strings.HasPrefix(archive, "nginx/"):
		return ItemNginx
	case strings.HasPrefix(archive, "compose/"):
		return ItemCompose
	}
	return ""
}
