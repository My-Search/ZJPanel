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

// loadRemoteRaw 读取远程存储配置原样（敏感字段仍是密文），用于保存时沿用旧值。
func (s *Service) loadRemoteRaw() RemoteConfig {
	var v model.Setting
	if err := s.db.Where("key = ?", settingRemoteKey).First(&v).Error; err != nil {
		return RemoteConfig{Kind: StorageLocal}
	}
	var rc RemoteConfig
	if json.Unmarshal([]byte(v.Value), &rc) != nil {
		return RemoteConfig{Kind: StorageLocal}
	}
	return rc
}

// openCtx 解密单个敏感字段；解密失败（旧密钥/脏数据）按未设置处理，避免把密文当明文用。
func (s *Service) openCtx(ctx, v string) string {
	if v == "" || s.box == nil {
		return v
	}
	if p, err := s.box.OpenCtx(ctx, v); err == nil {
		return p
	}
	return ""
}

// loadRemote 读取远程存储配置，并解密密码 / 客户端密钥 / 刷新令牌。
func (s *Service) loadRemote() RemoteConfig {
	rc := s.loadRemoteRaw()
	rc.Password = s.openCtx(pkg.CtxBackupRemotePass, rc.Password)
	rc.ClientSecret = s.openCtx(pkg.CtxBackupRemoteSecret, rc.ClientSecret)
	rc.RefreshToken = s.openCtx(pkg.CtxBackupRemoteToken, rc.RefreshToken)
	return rc
}

// seal 加密敏感字段（空值保留空）。
func (s *Service) seal(ctx, v string) (string, error) {
	if v == "" || s.box == nil {
		return v, nil
	}
	return s.box.SealECtx(ctx, v)
}

// saveRemote 保存远程存储配置：三处敏感字段加密落库。
// 传空值表示「沿用已有密文」，避免前端不回传明文时把已保存的凭据清空。
func (s *Service) saveRemote(rc RemoteConfig) error {
	prev := s.loadRemoteRaw()
	store := rc
	// 选了远程通道就等于启用（前端无独立开关，避免上传时报「未启用远程存储」）
	store.Enabled = store.Kind != "" && store.Kind != StorageLocal

	if store.Password == "" {
		store.Password = prev.Password
	} else {
		enc, err := s.seal(pkg.CtxBackupRemotePass, store.Password)
		if err != nil {
			return err
		}
		store.Password = enc
	}

	if store.ClientSecret == "" {
		store.ClientSecret = prev.ClientSecret
	} else {
		enc, err := s.seal(pkg.CtxBackupRemoteSecret, store.ClientSecret)
		if err != nil {
			return err
		}
		store.ClientSecret = enc
	}

	if store.RefreshToken == "" {
		store.RefreshToken = prev.RefreshToken
	} else {
		enc, err := s.seal(pkg.CtxBackupRemoteToken, store.RefreshToken)
		if err != nil {
			return err
		}
		store.RefreshToken = enc
	}

	b, err := json.Marshal(store)
	if err != nil {
		return err
	}
	rec := model.Setting{Key: settingRemoteKey, Value: string(b)}
	return s.db.Save(&rec).Error
}

// RemoteView 向前端返回的远程配置（脱敏：敏感字段不回传明文，仅返回是否已设置）。
type RemoteView struct {
	RemoteConfig
	PasswordSet     bool `json:"passwordSet"`
	ClientSecretSet bool `json:"clientSecretSet"`
	Authorized      bool `json:"authorized"` // 云盘是否已完成 OAuth 授权
}

func (s *Service) RemoteConfigView() RemoteView {
	rc := s.loadRemote()
	view := RemoteView{
		RemoteConfig:    rc,
		PasswordSet:     rc.Password != "",
		ClientSecretSet: rc.ClientSecret != "",
		Authorized:      rc.RefreshToken != "",
	}
	// 不把明文/令牌带回前端。
	view.Password = ""
	view.ClientSecret = ""
	view.RefreshToken = ""
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
	// 传入配置通常来自前端（敏感字段为空）：用已保存的密文补齐，避免测试时缺凭据。
	prev := s.loadRemote()
	if rc.Password == "" {
		rc.Password = prev.Password
	}
	if rc.ClientSecret == "" {
		rc.ClientSecret = prev.ClientSecret
	}
	if rc.RefreshToken == "" {
		rc.RefreshToken = prev.RefreshToken
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
