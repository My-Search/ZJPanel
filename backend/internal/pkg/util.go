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
	"strings"
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

// PwFingerprint 密码哈希指纹（用于会话与当前凭据绑定，不落任何明文/完整哈希）。
func PwFingerprint(hash string) string {
	return SHA256Hex("zjpanel-pwfp:"+hash)[:16]
}

func BcryptHash(s string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	return string(b)
}

func BcryptVerify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ---- AES-GCM（凭证/私钥加密）----

// AesGCMEncrypt legacy 格式：base64(nonce‖ct)，无 AAD。仅保留用于解密历史密文，
// 新写入一律走 AesGCMEncryptAAD（v2 格式）。
func AesGCMEncrypt(key []byte, plain string) (string, error) {
	out, err := gcmSeal(key, []byte(plain), nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func AesGCMDecrypt(key []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	plain, err := gcmOpen(key, raw, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// v2 密文前缀（带 AAD 的字段绑定格式）。
const v2Prefix = "v2:"

// AAD 字段上下文（各加密字段的全局唯一标识；迁移与读写两侧必须一致）。
const (
	CtxSSHPassword   = "ssh_hosts.password_enc"
	CtxSSHPrivateKey = "ssh_hosts.private_key_enc"
	CtxSSHPassphrase = "ssh_hosts.passphrase_enc"
	CtxAcmeKey       = "acme_accounts.key_enc"
	CtxDnsConfig     = "dns_credentials.config_enc"
	CtxSMTPPass      = "settings.notify.smtp.pass"
	CtxMySQLConnPwd  = "mysqladmin.connection.password"
)

// AesGCMEncryptAAD v2 格式："v2:" + base64(nonce‖ct)，AAD 绑定字段上下文，
// 密文不可跨字段/跨用途互换而不被发现。AAD = "zjpanel:" + ctx。
func AesGCMEncryptAAD(key []byte, ctx, plain string) (string, error) {
	out, err := gcmSeal(key, []byte(plain), aadOf(ctx))
	if err != nil {
		return "", err
	}
	return v2Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// AesGCMDecryptAAD 解密 v2 格式（AAD 必须与加密时一致）。
func AesGCMDecryptAAD(key []byte, ctx, enc string) (string, error) {
	if !strings.HasPrefix(enc, v2Prefix) {
		return "", errors.New("not a v2 ciphertext")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, v2Prefix))
	if err != nil {
		return "", err
	}
	plain, err := gcmOpen(key, raw, aadOf(ctx))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func aadOf(ctx string) []byte { return []byte("zjpanel:" + ctx) }

func gcmBlock(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func gcmSeal(key []byte, plain, aad []byte) ([]byte, error) {
	gcm, err := gcmBlock(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, aad), nil
}

func gcmOpen(key []byte, raw, aad []byte) ([]byte, error) {
	gcm, err := gcmBlock(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], aad)
}

// EnsureSecretKey 加载/创建面板 32 字节主密钥（凭证加密用）。
// 优先级：env ZJPANEL_SECRET_KEY（64 位 hex）> <dataDir>/secret.key 文件。
// 密钥文件存在但内容无效时直接报错拒绝启动（绝不静默重建——旧密文会全部不可解）。
func EnsureSecretKey(dataDir string) ([]byte, error) {
	if k := os.Getenv("ZJPANEL_SECRET_KEY"); k != "" {
		b, err := hex.DecodeString(strings.TrimSpace(k))
		if err != nil || len(b) != 32 {
			return nil, errors.New("ZJPANEL_SECRET_KEY 必须是 64 位 hex（32 字节）")
		}
		return b, nil
	}
	p := filepath.Join(dataDir, "secret.key")
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		trimmed := strings.TrimSpace(string(b))
		if k, err := hex.DecodeString(trimmed); err == nil && len(k) == 32 {
			return k, nil
		}
		return nil, fmt.Errorf("密钥文件 %s 内容无效（应为 64 位 hex），已拒绝启动以免静默重建导致既有密文全部不可解", p)
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
// 写入一律 v2（AAD 绑定字段上下文）；读取兼容 legacy（无前缀）与 v2。

type SecretBox struct {
	key []byte
}

func NewSecretBox(key []byte) *SecretBox { return &SecretBox{key: key} }

// SealCtx 以字段上下文加密（v2 格式；ctx 如 "ssh_hosts.password_enc"）。
func (s *SecretBox) SealCtx(ctx, plain string) string {
	enc, err := AesGCMEncryptAAD(s.key, ctx, plain)
	if err != nil {
		panic(err)
	}
	return enc
}

// SealECtx 同 SealCtx，但返回错误。
func (s *SecretBox) SealECtx(ctx, plain string) (string, error) {
	return AesGCMEncryptAAD(s.key, ctx, plain)
}

// OpenCtx 解密：v2 前缀走 AAD 通道，否则按 legacy 无 AAD 解密。
func (s *SecretBox) OpenCtx(ctx, enc string) (string, error) {
	if strings.HasPrefix(enc, v2Prefix) {
		return AesGCMDecryptAAD(s.key, ctx, enc)
	}
	return AesGCMDecrypt(s.key, enc)
}

func Now() time.Time { return time.Now() }

func TimePtr(t time.Time) *time.Time { return &t }
