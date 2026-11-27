package backup

import (
	"fmt"
)

// Provider 远程存储抽象：上传/下载/列举/删除备份包。
// 现有实现：WebDAV；Google Drive / OneDrive 为 OAuth 体系，预留接入位。
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
	case StorageGoogleDrive, StorageOneDrive:
		return nil, fmt.Errorf("该云盘通道尚未接入，当前仅支持 WebDAV")
	default:
		return nil, nil
	}
}

// nopProvider 占位（本地存储场景）。
type nopProvider struct{}

var _ Provider = nopProvider{}

func (nopProvider) Upload(string, string) error   { return nil }
func (nopProvider) Download(string, string) error { return nil }
func (nopProvider) Delete(string) error           { return nil }
func (nopProvider) List() ([]string, error)       { return nil, nil }
func (nopProvider) Test() error                   { return nil }
