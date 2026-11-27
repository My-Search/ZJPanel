package backup

import (
	"fmt"
	"strings"
)

// Provider 远程存储抽象：上传/下载/列举/删除备份包。
// 实现：WebDAV、Google Drive、OneDrive（后两者走 OAuth，见 oauth.go）。
type Provider interface {
	// Upload 把本地文件上传到远程目录，返回远程对象名。
	Upload(localPath, remoteName string) error
	// Download 把远程对象写入本地目标路径。
	Download(remoteName, localPath string) error
	// Delete 删除远程对象。
	Delete(remoteName string) error
	// List 列举远程已存在的备份对象名。
	List() ([]string, error)
	// Test 连通性校验（保存配置前调用）。
	Test() error
}

// NewProvider 按配置构造 Provider；未启用或本地存储返回 nil。
func NewProvider(rc RemoteConfig) (Provider, error) {
	switch rc.Kind {
	case StorageWebDAV:
		return newWebDAV(rc), nil
	case StorageGoogleDrive:
		return newGDrive(rc), nil
	case StorageOneDrive:
		return newOneDrive(rc), nil
	default:
		return nil, nil
	}
}

// dirSegments 把远程目录配置拆成逐级目录名；空值回退到默认目录，剔除 . / .. 等危险段。
func dirSegments(remoteDir, def string) []string {
	d := strings.Trim(strings.TrimSpace(remoteDir), "/")
	if d == "" {
		d = def
	}
	var out []string
	for _, p := range strings.Split(d, "/") {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{def}
	}
	return out
}

// nopProvider 占位（本地存储场景）。
type nopProvider struct{}

var _ Provider = nopProvider{}

func (nopProvider) Upload(string, string) error   { return nil }
func (nopProvider) Download(string, string) error { return nil }
func (nopProvider) Delete(string) error           { return nil }
func (nopProvider) List() ([]string, error)       { return nil, nil }
func (nopProvider) Test() error                   { return nil }

// String 便于日志输出通道类型。
func (rc RemoteConfig) String() string {
	if rc.Kind == "" {
		return StorageLocal
	}
	return fmt.Sprintf("%s(%s)", rc.Kind, rc.RemoteDir)
}
