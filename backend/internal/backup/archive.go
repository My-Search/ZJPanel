package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// walkDir 递归收集目录下所有普通文件；单文件不可读不阻断整体打包，skipDirs 按目录名跳过。
// 符号链接/设备/套接字不收录：tar 无法原样还原它们，且可能指向目录之外。
func walkDir(root, archivePrefix string, skipDirs map[string]bool, files *[]collectedFile) error {
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
			return nil
		}
		if info.IsDir() {
			if skipDirs != nil && skipDirs[filepath.Base(p)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		*files = append(*files, collectedFile{
			src:     p,
			archive: filepath.ToSlash(filepath.Join(archivePrefix, rel)),
		})
		return nil
	})
}

// collect 按所选项目收集文件清单（不读取内容，仅列路径与大小）。
func (s *Service) collect(items []string) ([]collectedFile, error) {
	var files []collectedFile
	addDir := func(root, archivePrefix string) {
		_ = walkDir(root, archivePrefix, nil, &files)
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
			addDir(filepath.Join(s.cfg.DataDir, "certs"), "certs")
		case ItemSecurity:
			p := filepath.Join(s.cfg.DataDir, "security", "policy.json")
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				files = append(files, collectedFile{src: p, archive: "security/policy.json"})
			}
		case ItemPlugins:
			addDir(filepath.Join(s.cfg.DataDir, "plugins-data"), "plugins-data")
		case ItemNginx:
			for _, sub := range []string{"sites-available", "sites-enabled", "conf.d"} {
				addDir(filepath.Join(s.cfg.NginxPrefix, sub), filepath.Join("nginx", sub))
			}
		case ItemCompose:
			if err := s.collectCompose(&files); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// composeFileNames ComposeRoot 下一个目录顶层出现这些文件即视为「站点应用目录」。
var composeFileNames = map[string]bool{
	"docker-compose.yml": true, "docker-compose.yaml": true,
	"compose.yml": true, "compose.yaml": true,
}

// collectCompose 扫描 ComposeRoot 一级目录：凡顶层含 compose 文件的，整个目录递归打包。
// 整目录迁移意味着应用配置、挂载的本地文件与业务数据一并带走（Docker 命名数据卷除外）。
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
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !hasComposeFile(dir) {
			continue
		}
		_ = walkDir(dir, filepath.Join("compose", e.Name()), nil, files)
	}
	return nil
}

// hasComposeFile 判断目录顶层是否存在 compose 文件。
func hasComposeFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range entries {
		if !de.IsDir() && composeFileNames[de.Name()] {
			return true
		}
	}
	return false
}

// copyTarEntry 按开始打包时 Stat 的快照大小定长写入一个 tar 条目。
// 大目录打包动辄数分钟，目录里有正在被写的活文件（容器日志、站点数据库等）是常态：
//   - 文件变大：只写快照大小，超出部分截断。不设上限的 io.Copy 会写入超过头声明的字节数，
//     触发 archive/tar: write too long 令整个备份失败；
//   - 文件变小/被替换：不足部分补零，保持 tar 流结构完整（否则后续 WriteHeader 报
//     missed writing N bytes，或 Close 错误被吞后产出损坏的包）。
//
// 返回 changed 表示文件写入期间大小发生了变化（按快照截断/补零，内容可能不完整）。
func copyTarEntry(tw *tar.Writer, fh *os.File, size int64) (changed bool, err error) {
	n, err := io.Copy(tw, io.LimitReader(fh, size))
	if err != nil {
		return false, err
	}
	if n < size {
		// 变小：补零填满声明的 tar 大小
		var zero [32 * 1024]byte
		for rest := size - n; rest > 0; {
			w := rest
			if w > int64(len(zero)) {
				w = int64(len(zero))
			}
			if _, werr := tw.Write(zero[:w]); werr != nil {
				return true, werr
			}
			rest -= w
		}
		return true, nil
	}
	// 变大探测：LimitReader 在快照大小处截断，多读 1 字节判断文件仍在增长
	var probe [1]byte
	if m, _ := fh.Read(probe[:]); m > 0 {
		return true, nil
	}
	return false, nil
}

// emitStep 进度回调安全触发（nil 回调直接跳过：单测/无推送场景）。
func emitStep(step func(string), format string, args ...any) {
	if step != nil {
		step(fmt.Sprintf(format, args...))
	}
}

// Build 按所选项目生成备份包，返回包信息（文件名、大小、清单、SHA256）。
// step 可为 nil：收集/打包/哈希各阶段会带进度说明，供任务日志实时展示。
func (s *Service) Build(items []string, step func(string)) (rec *BackupRecord, retErr error) {
	items = NormalizeItems(items)
	files, err := s.collect(items)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.backupsDir, 0o755); err != nil {
		return nil, err
	}
	emitStep(step, "已收集 %d 个文件，开始打包（磁盘剩余 %s）", len(files), humanFreeBytes(diskFreeBytes(s.backupsDir)))

	now := time.Now()
	name := backupName(now)
	path := filepath.Join(s.backupsDir, name)
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	// 失败不留半截包：List() 会把备份目录里的 *.tar.gz 当记录展示，
	// 残包既占空间又会显示成「0 项」的坏记录。
	defer func() {
		out.Close()
		if retErr != nil {
			_ = os.Remove(path)
		}
	}()

	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	var manifestFiles []ManifestFile
	var changedFiles []string // 打包期间发生大小变化的活文件
	hash := sha256.New()
	total := int64(0)

	for i, f := range files {
		// 磁盘水位检查：剩余空间跌破最低水位立即失败，半截包由 retErr 清理删除，
		// 避免打包把宿主机磁盘占满（查询失败按未知处理，不阻断打包）。
		free := diskFreeBytes(s.backupsDir)
		if free >= 0 && free < minFreeBytes {
			return nil, fmt.Errorf("磁盘剩余空间不足（仅 %s，低于 %s 水位），已中止打包", humanBytes(free), humanBytes(minFreeBytes))
		}
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
			return nil, fmt.Errorf("写入 %s 头失败: %w", f.src, err)
		}
		changed, err := copyTarEntry(tw, fh, st.Size())
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("写入 %s 失败: %w", f.src, err)
		}
		if changed {
			changedFiles = append(changedFiles, f.src)
		}
		manifestFiles = append(manifestFiles, ManifestFile{
			Source: f.src, Archive: f.archive, Size: st.Size(),
		})
		total += st.Size()
		// 大目录打包可能持续数分钟，定期报进度避免前台看起来无响应。
		if len(files) > 400 && (i+1)%200 == 0 {
			emitStep(step, "打包进度 %d/%d 文件（累计 %s，磁盘剩余 %s）", i+1, len(files), humanBytes(total), humanFreeBytes(free))
		}
	}

	// 活文件提示：截断/补零不影响包结构，恢复端拿到的是开始打包那一刻的快照。
	if len(changedFiles) > 0 {
		sample := changedFiles
		more := ""
		if len(sample) > 3 {
			sample = sample[:3]
			more = " 等"
		}
		emitStep(step, "%d 个文件在打包期间发生变化，已按快照大小写入（日志/数据库等活文件属正常现象）：%s%s",
			len(changedFiles), strings.Join(sample, "、"), more)
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

	// Close 负责 flush gzip 尾部与 tar 填充，出错说明包损坏，交由失败清理删除。
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("收尾 tar 流失败: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("收尾 gzip 流失败: %w", err)
	}
	if err := out.Close(); err != nil {
		return nil, fmt.Errorf("关闭备份包失败: %w", err)
	}

	// 启用加密：整包流式加密为 .enc（age 格式）并删除明文，云端只落加密包。
	cc := s.loadCrypt()
	finalName := name
	if cc.Enabled {
		if cc.Password == "" {
			_ = os.Remove(path)
			return nil, errors.New("已启用备份包加密但未配置密码：请到备份设置填写加密密码")
		}
		// 加密要再写一份约等大的 .enc：空间不够提前失败，避免加密中途 ENOSPC 留下满盘。
		if pst, err := os.Stat(path); err == nil {
			if free := diskFreeBytes(s.backupsDir); free >= 0 && free-pst.Size() < minFreeBytes {
				return nil, fmt.Errorf("磁盘剩余空间不足以生成加密包（需约 %s，仅剩 %s）", humanBytes(pst.Size()), humanFreeBytes(free))
			}
		}
		emitStep(step, "备份包加密（age 流式）…")
		if err := func() error {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()
			return encryptTo(path+".enc", src, cc.Password)
		}(); err != nil {
			_ = os.Remove(path)
			_ = os.Remove(path + ".enc")
			return nil, err
		}
		_ = os.Remove(path)
		finalName = name + ".enc"
	}
	manifest.Encrypted = cc.Enabled
	finalPath := filepath.Join(s.backupsDir, finalName)

	// 计算整包 SHA256 与最终大小（加密包按最终产物计算）。
	emitStep(step, "计算整包 SHA256…")
	if hf, err := os.Open(finalPath); err == nil {
		if _, err := io.Copy(hash, hf); err == nil {
			_ = hf.Close()
		}
	}
	finalStat, _ := os.Stat(finalPath)
	rec = &BackupRecord{
		Name:         finalName,
		Path:         finalPath,
		Size:         0,
		CreatedAt:    now,
		Items:        items,
		FileCount:    len(manifestFiles),
		Uncompressed: total,
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
		Encrypted:    cc.Enabled,
	}
	if finalStat != nil {
		rec.Size = finalStat.Size()
	}
	// 旁落清单 json，便于列表不拆包展示。
	_ = writeManifest(s.backupsDir, finalName, manifest)
	return rec, nil
}

// Restore 从备份包恢复所选项目（数据库/配置/声明写回对应位置）。
// 返回恢复的文件清单与跳过项；调用方应在低峰期执行，必要时重启面板/重载 nginx。
// step 可为 nil：恢复进度定期上报。按内容头自动识别 age 加密包并解密（明文包透传兼容）。
func (s *Service) Restore(path string, items []string, step func(string)) (*RestoreResult, error) {
	items = NormalizeItems(items)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	src, err := s.decryptReader(br)
	if err != nil {
		return nil, err
	}
	gr, err := gzip.NewReader(src)
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
		if len(res.Restored)%100 == 0 {
			emitStep(step, "已写回 %d 个文件…", len(res.Restored))
		}
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
		return safeJoin(filepath.Join(s.cfg.DataDir, "certs"), strings.TrimPrefix(archive, "certs/"))
	case archive == "security/policy.json":
		return filepath.Join(s.cfg.DataDir, "security", "policy.json"), nil
	case strings.HasPrefix(archive, "plugins-data/"):
		return safeJoin(filepath.Join(s.cfg.DataDir, "plugins-data"), strings.TrimPrefix(archive, "plugins-data/"))
	case strings.HasPrefix(archive, "nginx/"):
		return safeJoin(s.cfg.NginxPrefix, strings.TrimPrefix(archive, "nginx/"))
	case strings.HasPrefix(archive, "compose/"):
		return safeJoin(s.cfg.ComposeRoot, strings.TrimPrefix(archive, "compose/"))
	}
	return "", fmt.Errorf("未知归档路径: %s", archive)
}

// safeJoin 把包内相对路径拼到目标根目录，拒绝 .. 越界（备份包可能来自外部，不能直接信任）。
func safeJoin(base, rel string) (string, error) {
	rel = filepath.FromSlash(strings.TrimPrefix(rel, "/"))
	if rel == "" {
		return "", fmt.Errorf("非法归档路径: %s", rel)
	}
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		if seg == ".." {
			return "", fmt.Errorf("非法归档路径: %s", rel)
		}
	}
	dest := filepath.Join(base, rel)
	cleanBase := filepath.Clean(base)
	if dest != cleanBase && !strings.HasPrefix(dest, cleanBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("归档路径越界: %s", rel)
	}
	return dest, nil
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
