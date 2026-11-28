package backup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEncryptedBackupRoundTrip 加密往返：Build 产出 age 加密 .enc 包，恢复自动解密；
// 密码不符时恢复失败并给出可理解的错误。
func TestEncryptedBackupRoundTrip(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), testScheduleDB(t), nil, nil, nil)

	if err := os.MkdirAll(filepath.Join(data, "certs/1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "certs/1/fullchain.pem"), []byte("cert-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 启用加密（密码加密落库，脱敏回显只给 passwordSet）。
	if err := s.SaveCrypt(CryptConfig{Enabled: true, Password: "test-pass-123"}); err != nil {
		t.Fatalf("保存加密配置失败: %v", err)
	}
	if v := s.CryptView(); !v.Enabled || !v.PasswordSet {
		t.Fatalf("加密视图异常: %+v", v)
	}

	rec, err := s.Build([]string{ItemCerts}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !rec.Encrypted || !strings.HasSuffix(rec.Name, ".tar.gz.enc") {
		t.Fatalf("产物应为加密包: name=%s encrypted=%v", rec.Name, rec.Encrypted)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatalf("加密包不存在: %v", err)
	}
	// 明文 tar.gz 应已被删除。
	if _, err := os.Stat(filepath.Join(s.backupsDir, strings.TrimSuffix(rec.Name, ".enc"))); !os.IsNotExist(err) {
		t.Fatal("明文包未被删除")
	}
	// 文件头是 age 魔数（不再能直接 gunzip）。
	f, _ := os.Open(rec.Path)
	br := bufio.NewReader(f)
	if !isAgeFile(br) {
		t.Fatal("加密包缺少 age 格式头")
	}
	_ = f.Close()

	// 列表应能看到加密标记，且清单 json 用基础名寻址。
	list, err := s.List()
	if err != nil || len(list) != 1 || !list[0].Encrypted {
		t.Fatalf("列表未正确展示加密包: %+v err=%v", list, err)
	}

	// 正确密码恢复：内容一致。
	_ = os.RemoveAll(filepath.Join(data, "certs"))
	res, err := s.Restore(rec.Path, []string{ItemCerts}, nil)
	if err != nil {
		t.Fatalf("解密恢复失败: %v", err)
	}
	if len(res.Restored) == 0 {
		t.Fatal("恢复无文件")
	}
	b, err := os.ReadFile(filepath.Join(data, "certs/1/fullchain.pem"))
	if err != nil || string(b) != "cert-body" {
		t.Fatalf("恢复内容不符: %v", err)
	}

	// 密码不符：解密失败且报错可理解。
	if err := s.SaveCrypt(CryptConfig{Enabled: true, Password: "another-pass"}); err != nil {
		t.Fatalf("改密码失败: %v", err)
	}
	if _, err := s.Restore(rec.Path, []string{ItemCerts}, nil); err == nil || !strings.Contains(err.Error(), "解密失败") {
		t.Fatalf("密码错误应导致解密失败，实际: %v", err)
	}
}

// TestEncryptedWithoutPassword 启用加密但未配置密码：Build 直接报错而非产出明文包。
func TestEncryptedWithoutPassword(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), testScheduleDB(t), nil, nil, nil)
	if err := s.SaveCrypt(CryptConfig{Enabled: true}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := s.Build([]string{ItemCerts}, nil); err == nil || !strings.Contains(err.Error(), "未配置密码") {
		t.Fatalf("应拒绝无密码加密: %v", err)
	}
	entries, _ := os.ReadDir(s.backupsDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			t.Fatalf("不应产出任何备份包: %s", e.Name())
		}
	}
}
