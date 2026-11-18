package webhooks

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func signHex(secret string, body []byte, use256 bool) string {
	if use256 {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return "sha1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookRequest(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "s3cr3t"
	valid1 := signHex(secret, body, false)
	valid256 := signHex(secret, body, true)

	// 1. 不带任何签名头必须被拒绝（回归：此前空签名头绕过校验）
	if verifyWebhookRequest(secret, body, "", "") {
		t.Fatal("缺少签名头时应拒绝请求")
	}
	// 2. 空密钥必须被拒绝
	if verifyWebhookRequest("", body, valid1, valid256) {
		t.Fatal("空密钥时应拒绝请求")
	}
	// 3. 合法 sha1 或 sha256 应通过
	if !verifyWebhookRequest(secret, body, valid1, "") {
		t.Fatal("合法 sha1 签名应通过")
	}
	if !verifyWebhookRequest(secret, body, "", valid256) {
		t.Fatal("合法 sha256 签名应通过")
	}
	if !verifyWebhookRequest(secret, body, valid1, valid256) {
		t.Fatal("同时带合法 sha1 与 sha256 签名应通过")
	}
	// 4. 篡改任一签名头必须被拒绝
	if verifyWebhookRequest(secret, body, "sha1=deadbeef", "") {
		t.Fatal("错误 sha1 签名应被拒绝")
	}
	if verifyWebhookRequest(secret, body, valid1, "sha256=deadbeef") {
		t.Fatal("错误 sha256 签名应被拒绝")
	}
}
