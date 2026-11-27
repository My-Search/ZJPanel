package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// loadRemote 读取远程存储配置；密码等敏感字段落库前加密，读出时解密。
func (s *Service) loadRemote() RemoteConfig {
	var v model.Setting
	if err := s.db.Where("key = ?", settingRemoteKey).First(&v).Error; err != nil {
		return RemoteConfig{Kind: StorageLocal}
	}
	var rc RemoteConfig
	if json.Unmarshal([]byte(v.Value), &rc) != nil {
		return RemoteConfig{Kind: StorageLocal}
	}
	// 解密已加密的密码（空则保留空）。
	if rc.Password != "" && s.box != nil {
		if p, err := s.box.OpenCtx(pkg.CtxBackupRemotePass, rc.Password); err == nil {
			rc.Password = p
		}
	}
	return rc
}

// saveRemote 保存远程存储配置；密码用 SecretBox 加密后落库。
func (s *Service) saveRemote(rc RemoteConfig) error {
	store := rc
	if store.Password != "" && s.box != nil {
		enc, err := s.box.SealECtx(pkg.CtxBackupRemotePass, store.Password)
		if err != nil {
			return err
		}
		store.Password = enc
	}
	b, err := json.Marshal(store)
	if err != nil {
		return err
	}
	rec := model.Setting{Key: settingRemoteKey, Value: string(b)}
	if err := s.db.Save(&rec).Error; err != nil {
		return err
	}
	return nil
}

// RemoteView 向前端返回的远程配置（脱敏：密码不回传明文，仅返回是否已设置）。
type RemoteView struct {
	RemoteConfig
	PasswordSet bool `json:"passwordSet"`
}

func (s *Service) RemoteConfigView() RemoteView {
	rc := s.loadRemote()
	view := RemoteView{RemoteConfig: rc, PasswordSet: rc.Password != ""}
	// 不把解密后的密码带回前端。
	view.Password = ""
	return view
}

// SaveRemote 保存远程存储配置（供路由调用；封装 saveRemote）。
func (s *Service) SaveRemote(rc RemoteConfig) error {
	return s.saveRemote(rc)
}

// UploadToRemote 把本地备份包上传到当前配置的远程存储。
func (s *Service) UploadToRemote(name string) error {
	rc := s.loadRemote()
	if !rc.Enabled || rc.Kind == StorageLocal || rc.Kind == "" {
		return errors.New("未启用远程存储")
	}
	p, err := s.OpenPath(name)
	if err != nil {
		return err
	}
	prov, err := NewProvider(rc)
	if err != nil {
		return err
	}
	if prov == nil {
		return errors.New("远程存储通道不可用")
	}
	return prov.Upload(p, name)
}

// DownloadFromRemote 从远程存储把备份包拉到本地备份目录。
func (s *Service) DownloadFromRemote(name string) error {
	rc := s.loadRemote()
	prov, err := NewProvider(rc)
	if err != nil {
		return err
	}
	if prov == nil {
		return errors.New("远程存储通道不可用")
	}
	local := filepath.Join(s.backupsDir, name)
	if err := os.MkdirAll(s.backupsDir, 0o755); err != nil {
		return err
	}
	return prov.Download(name, local)
}

// DeleteFromRemote 删除远程对象（忽略本地不存在）。
func (s *Service) DeleteFromRemote(name string) error {
	rc := s.loadRemote()
	prov, err := NewProvider(rc)
	if err != nil {
		return err
	}
	if prov == nil {
		return nil
	}
	return prov.Delete(name)
}

// TestRemote 校验当前（或传入的）远程配置连通性。
func (s *Service) TestRemote(rc RemoteConfig) error {
	if rc.Kind == StorageLocal || rc.Kind == "" {
		return nil
	}
	prov, err := NewProvider(rc)
	if err != nil {
		return err
	}
	if prov == nil {
		return fmt.Errorf("远程存储通道不可用")
	}
	return prov.Test()
}
