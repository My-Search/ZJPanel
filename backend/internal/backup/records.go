package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupRecord 本地备份包的描述信息（列表展示用）。
type BackupRecord struct {
	Name         string    `json:"name"`
	Path         string    `json:"-"`
	Size         int64     `json:"size"`
	Uncompressed int64     `json:"uncompressed"`
	CreatedAt    time.Time `json:"createdAt"`
	Items        []string  `json:"items"`
	FileCount    int       `json:"fileCount"`
	SHA256       string    `json:"sha256"`
	Encrypted    bool      `json:"encrypted"` // age 加密包（.tar.gz.enc）
	// Remote 标记该包是否已上传到远程存储。
	Remote bool `json:"remote"`
}

// RestoreResult 一次恢复操作的结果。
type RestoreResult struct {
	Items    []string `json:"items"`
	Restored []string `json:"restored"`
	Skipped  []string `json:"skipped"`
	Errors   []string `json:"errors"`
}

// List 扫描本地备份目录，返回按时间倒序的备份记录。
func (s *Service) List() ([]BackupRecord, error) {
	entries, err := os.ReadDir(s.backupsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []BackupRecord
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		// 兼容两种产物：明文 .tar.gz 与 age 加密 .tar.gz.enc
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.gz.enc") {
			continue
		}
		rec := BackupRecord{Name: name, Path: filepath.Join(s.backupsDir, name)}
		if st, err := e.Info(); err == nil {
			rec.Size = st.Size()
			rec.CreatedAt = st.ModTime()
		}
		rec.Encrypted = strings.HasSuffix(name, ".enc")
		// 旁落清单 json 补充项目/文件数/哈希（加密与否共用基础名）。
		meta := backupBaseName(name) + ".json"
		if mb, err := os.ReadFile(filepath.Join(s.backupsDir, meta)); err == nil {
			var m Manifest
			if json.Unmarshal(mb, &m) == nil {
				rec.Items = m.Items
				rec.FileCount = len(m.Files)
				for _, f := range m.Files {
					rec.Uncompressed += f.Size
				}
				if rec.CreatedAt.IsZero() {
					rec.CreatedAt = m.CreatedAt
				}
			}
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	return records, nil
}

// Delete 删除本地备份包（明文/加密）及其清单 json。
func (s *Service) Delete(name string) error {
	if !isSafeName(name) {
		return os.ErrInvalid
	}
	base := backupBaseName(name)
	for _, p := range []string{
		filepath.Join(s.backupsDir, base+".tar.gz"),
		filepath.Join(s.backupsDir, base+".tar.gz.enc"),
		filepath.Join(s.backupsDir, base+".json"),
	} {
		_ = os.Remove(p)
	}
	return nil
}

// OpenPath 校验备份名安全并返回本地绝对路径（不存在返回错误）。
func (s *Service) OpenPath(name string) (string, error) {
	if !isSafeName(name) {
		return "", os.ErrInvalid
	}
	p := filepath.Join(s.backupsDir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// isSafeName 防止路径穿越：只允许文件名，不含分隔符与 ..。
func isSafeName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return true
}
