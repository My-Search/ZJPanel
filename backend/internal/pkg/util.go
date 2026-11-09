package pkg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func RandomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func BcryptHash(s string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	return string(b)
}

func BcryptVerify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ---- AES-GCM（凭证/私钥加密）----

func AesGCMEncrypt(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func AesGCMDecrypt(key []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// EnsureSecretKey 加载/创建面板 32 字节主密钥（凭证加密用）。
func EnsureSecretKey(dataDir string) ([]byte, error) {
	p := filepath.Join(dataDir, "secret.key")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 64 {
		if k, err := hex.DecodeString(string(b)[:64]); err == nil {
			return k, nil
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	h := hex.EncodeToString(k)
	if err := os.WriteFile(p, []byte(h), 0o600); err != nil {
		return nil, fmt.Errorf("写入 secret.key 失败: %w", err)
	}
	return k, nil
}

// ---- 加密凭证存取（面板级，任何持有 SecretBox 的服务复用）----

type SecretBox struct {
	key []byte
}

func NewSecretBox(key []byte) *SecretBox { return &SecretBox{key: key} }

func (s *SecretBox) Seal(plain string) string {
	enc, err := AesGCMEncrypt(s.key, plain)
	if err != nil {
		panic(err)
	}
	return enc
}

// SealE 同 Seal，但返回错误（供不希望 panic 的调用方使用）。
func (s *SecretBox) SealE(plain string) (string, error) {
	return AesGCMEncrypt(s.key, plain)
}

func (s *SecretBox) Open(enc string) (string, error) {
	return AesGCMDecrypt(s.key, enc)
}

func Now() time.Time { return time.Now() }

func TimePtr(t time.Time) *time.Time { return &t }
