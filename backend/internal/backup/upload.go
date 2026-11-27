package backup

import (
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
)

func copyFile(dest string, src io.Reader) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// RestoreUpload 把上传的备份包保存到临时文件，返回临时路径（用完调用 RemoveTemp）。
func (s *Service) RestoreUpload(file *multipart.FileHeader) (string, error) {
	if err := os.MkdirAll(s.backupsDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.backupsDir, "restore-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmp.Close()
	src, err := file.Open()
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	defer src.Close()
	if err := copyFile(tmp.Name(), src); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// RemoveTemp 删除临时恢复文件（忽略不存在）。
func (s *Service) RemoveTemp(path string) {
	if path == "" {
		return
	}
	// 仅允许删除备份目录下的临时文件，防路径穿越。
	if filepath.Dir(path) != s.backupsDir {
		return
	}
	_ = os.Remove(path)
}
