package agent

import (
	"os"
	"os/exec"
	"runtime"
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

func TestInstallScriptSHAUsesDefinedVar(t *testing.T) {
	s := InstallScript(ScriptParams{
		PanelURL: "http://1.2.3.4:9000", Token: "tok123", Version: "1.2.3", SHA256: "abc123",
	})
	// 回归：脚本 set -u，校验段曾误用未定义的小写 $tmp，导致 `tmp: unbound variable` + 退出 12。
	if strings.Contains(s, "$tmp") {
		t.Fatalf("校验段引用了未定义变量 $tmp:\n%s", s)
	}
	if !strings.Contains(s, `sha256sum "$TMP"`) {
		t.Fatalf("校验段应使用脚本内已定义的 $TMP")
	}
	if !strings.Contains(s, "abc123") {
		t.Fatalf("校验段应带上期望摘要")
	}
	// 有 shell 时做语法检查（Windows 开发机的 sh 可能是 WSL 转发，容易误判，直接跳过）。
	if runtime.GOOS == "windows" {
		t.Log("Windows 开发机跳过 sh 语法检查")
		return
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		if shell, err = exec.LookPath("bash"); err != nil {
			t.Log("本机无 sh/bash，跳过语法检查")
			return
		}
	}
	f := t.TempDir() + "/install.sh"
	if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(shell, "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("安装脚本语法错误: %v\n%s", err, out)
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
