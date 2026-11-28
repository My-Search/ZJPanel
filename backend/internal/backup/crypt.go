package backup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// 备份包加密：上传到云盘前把 tar.gz 流式加密为 age 格式，避免云盘侧泄露明文。
// 密码存 Setting（backup.crypt，SecretBox 加密落库），手动/定时备份自动加密、恢复自动解密；
// age 为公开格式，灾难恢复时可用 age 命令行 + 该密码在无面板环境下离线解包。

const settingCryptKey = "backup.crypt"

// ageHeaderPrefix age 文件格式头，用于按内容嗅探加密包（不依赖文件名后缀）。
const ageHeaderPrefix = "age-encryption.org/v1"

// CryptConfig 备份包加密配置（密码 SecretBox 加密落库）。
type CryptConfig struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"` // 读回脱敏；保存空值 = 沿用旧密码
}

// CryptView 返回前端的脱敏视图。
type CryptView struct {
	Enabled     bool `json:"enabled"`
	PasswordSet bool `json:"passwordSet"`
}

// loadCryptRaw 读原样（密码仍是密文）；db 未注入（单测）时按未启用处理。
func (s *Service) loadCryptRaw() CryptConfig {
	if s.db == nil {
		return CryptConfig{}
	}
	var v model.Setting
	if err := s.db.Where("key = ?", settingCryptKey).First(&v).Error; err != nil {
		return CryptConfig{}
	}
	var cc CryptConfig
	if json.Unmarshal([]byte(v.Value), &cc) != nil {
		return CryptConfig{}
	}
	return cc
}

// loadCrypt 读配置并解密密码。
func (s *Service) loadCrypt() CryptConfig {
	cc := s.loadCryptRaw()
	cc.Password = s.openCtx(pkg.CtxBackupCrypt, cc.Password)
	return cc
}

// CryptView 当前加密配置（脱敏）。
func (s *Service) CryptView() CryptView {
	cc := s.loadCrypt()
	return CryptView{Enabled: cc.Enabled, PasswordSet: cc.Password != ""}
}

// SaveCrypt 保存加密配置：密码非空则加密落库，空值沿用旧密码。
func (s *Service) SaveCrypt(cc CryptConfig) error {
	prev := s.loadCryptRaw()
	store := cc
	if store.Password == "" {
		store.Password = prev.Password
	} else {
		enc, err := s.seal(pkg.CtxBackupCrypt, store.Password)
		if err != nil {
			return err
		}
		store.Password = enc
	}
	b, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return s.db.Save(&model.Setting{Key: settingCryptKey, Value: string(b)}).Error
}

// encryptTo 把 src 流式加密写入 dest（age/scrypt 口令模式）。
func encryptTo(dest string, src io.Reader, password string) error {
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return fmt.Errorf("加密配置无效: %w", err)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	w, err := age.Encrypt(out, recipient)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		return err
	}
	return w.Close() // 关闭写入器才会写出尾部 MAC，必须先于 out.Close
}

// isAgeFile 按内容头嗅探是否 age 加密文件。
func isAgeFile(r *bufio.Reader) bool {
	head, _ := r.Peek(len(ageHeaderPrefix))
	return bytes.Equal(head, []byte(ageHeaderPrefix))
}

// decryptReader 若 r 指向 age 文件则返回解密流，否则原样返回（透明兼容旧明文包）。
func (s *Service) decryptReader(r *bufio.Reader) (io.Reader, error) {
	if !isAgeFile(r) {
		return r, nil
	}
	cc := s.loadCrypt()
	if cc.Password == "" {
		return nil, errors.New("备份包已加密：请先在备份设置中配置加密密码")
	}
	id, err := age.NewScryptIdentity(cc.Password)
	if err != nil {
		return nil, fmt.Errorf("解密配置无效: %w", err)
	}
	dr, err := age.Decrypt(r, id)
	if err != nil {
		return nil, fmt.Errorf("备份包解密失败（密码错误或文件损坏）: %w", err)
	}
	return dr, nil
}
