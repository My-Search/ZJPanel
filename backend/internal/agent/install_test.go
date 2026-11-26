package agent

import (
	"strings"
	"testing"
)

func TestPanelWS(t *testing.T) {
	cases := map[string]string{
		"https://panel.example.com": "wss://panel.example.com/api/agent/ws",
		"http://1.2.3.4:9000/":      "ws://1.2.3.4:9000/api/agent/ws",
		"panel.example.com":         "ws://panel.example.com/api/agent/ws",
	}
	for in, want := range cases {
		if got := PanelWS(in); got != want {
			t.Fatalf("PanelWS(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestInstallScriptIsIdempotentAndGuarded(t *testing.T) {
	s := InstallScript(ScriptParams{
		PanelURL: "https://panel.example.com", Token: "tok123", Version: "1.2.3", SHA256: "abc",
	})
	for _, want := range []string{
		"set -eu",
		"id -u",                  // 必须 root 才继续
		"systemctl",              // 依赖 systemd
		"sha256sum",              // 校验二进制
		"versions/$VERSION",     // 双槽版本目录
		"ln -sfn",                // symlink 原子切换
		"/etc/systemd/system/zjagent.service",
		"https://panel.example.com",
		"tok123",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("安装脚本缺少关键片段 %q", want)
		}
	}
}

func TestProtocolOK(t *testing.T) {
	if !ProtocolOK(Proto) || !ProtocolOK("") {
		t.Fatal("当前协议版本应被接受")
	}
	if ProtocolOK("0") {
		t.Fatal("不兼容协议不应被接受")
	}
}

func TestUninstallScriptPurge(t *testing.T) {
	if !strings.Contains(UninstallScript(true), "/var/lib/zjagent") {
		t.Fatal("--purge 应清理状态目录")
	}
	if strings.Contains(UninstallScript(false), "rm -rf /var/lib/zjagent") {
		t.Fatal("默认卸载不应删除状态目录")
	}
}
