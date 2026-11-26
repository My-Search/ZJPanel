package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"zjpanel/backend/internal/pkg"
)

// ScriptParams 安装脚本参数。PanelURL 必须是目标机能访问到的面板地址
// （域名或 IP:端口），否则 Agent 回连会失败。
type ScriptParams struct {
	PanelURL string // 例：https://panel.example.com 或 http://1.2.3.4:9000
	Token    string // 一次性引导 token
	Version  string
	SHA256   string // 二进制摘要（可选，脚本会校验）
}

// InstallScript 生成幂等的安装脚本。
// 幂等语义：已装且版本一致 → 只做配置校正与启动；版本不同 → 走升级（双槽切换）；
// 未装 → 全新安装。任何一步失败都明确退出，不留「装了一半」的状态。
func InstallScript(p ScriptParams) string {
	panel := strings.TrimRight(p.PanelURL, "/")
	if panel == "" {
		panel = "http://127.0.0.1:9000"
	}
	shaCheck := ""
	if p.SHA256 != "" {
		shaCheck = fmt.Sprintf(`
  want=%q
  got=$(sha256sum "$tmp" | awk '{print $1}')
  [ "$want" = "$got" ] || { echo "[zjagent] 二进制校验失败"; exit 12; }`, p.SHA256)
	}
	return fmt.Sprintf(`#!/bin/sh
# ZJPanel 主机 Agent 安装脚本（由面板生成，可审阅）
set -eu

PANEL=%q
TOKEN=%q
VERSION=%q
DIR=/usr/local/lib/zjagent
CONF=/etc/zjagent
STATE=/var/lib/zjagent

say() { echo "[zjagent] $*"; }
fail() { echo "[zjagent] 失败: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "需要 root 权限（请用 root 或 sudo 执行）"
command -v systemctl >/dev/null 2>&1 || fail "需要 systemd（当前环境不支持）"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "不支持的架构: $(uname -m)" ;;
esac

DL="$PANEL/api/agent/bin/$ARCH?token=$TOKEN"
TMP=$(mktemp /tmp/zjagent.XXXXXX)
trap 'rm -f "$TMP"' EXIT
say "下载 Agent ($ARCH)"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$DL" -o "$TMP" || fail "下载失败：目标机无法访问 $PANEL"
elif command -v wget >/dev/null 2>&1; then
  wget -q "$DL" -O "$TMP" || fail "下载失败：目标机无法访问 $PANEL"
else
  fail "缺少 curl 或 wget"
fi
%s

mkdir -p "$DIR/versions/$VERSION" "$CONF" "$STATE"
install -m 0755 "$TMP" "$DIR/versions/$VERSION/zjagent"
ln -sfn "$DIR/versions/$VERSION/zjagent" /usr/local/bin/zjagent

cat > "$CONF/agent.json" <<EOF
{"panel":"$PANEL","token":"$TOKEN","version":"$VERSION"}
EOF
chmod 600 "$CONF/agent.json"

cat > /etc/systemd/system/zjagent.service <<EOF
[Unit]
Description=ZJPanel Host Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/zjagent run --config /etc/zjagent/agent.json
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable zjagent >/dev/null 2>&1 || true
systemctl restart zjagent
sleep 1
systemctl is-active --quiet zjagent || fail "服务未启动，请查看 journalctl -u zjagent -n 50"
say "安装完成，版本 $VERSION，面板 $PANEL"
`, panel, p.Token, p.Version, shaCheck)
}

// UninstallScript 本机卸载脚本：停服务、删 unit、删二进制与配置，可选保留状态目录。
// 该脚本也可由管理员直接在目标机执行：zjagent uninstall --purge
func UninstallScript(purge bool) string {
	state := ""
	if purge {
		state = `rm -rf /var/lib/zjagent`
	}
	return fmt.Sprintf(`#!/bin/sh
set -eu
say() { echo "[zjagent] $*"; }
systemctl stop zjagent 2>/dev/null || true
systemctl disable zjagent 2>/dev/null || true
rm -f /etc/systemd/system/zjagent.service
systemctl daemon-reload 2>/dev/null || true
rm -rf /usr/local/lib/zjagent
rm -f /usr/local/bin/zjagent
rm -rf /etc/zjagent
%s
say "已卸载 ZJPanel Agent"
`, state)
}

// PanelWS 由面板 HTTP 地址推导 Agent 的 WebSocket 端点。
func PanelWS(panelHTTP string) string {
	p := strings.TrimRight(panelHTTP, "/")
	switch {
	case strings.HasPrefix(p, "https://"):
		p = "wss://" + strings.TrimPrefix(p, "https://")
	case strings.HasPrefix(p, "http://"):
		p = "ws://" + strings.TrimPrefix(p, "http://")
	default:
		p = "ws://" + p
	}
	return p + "/api/agent/ws"
}

// BinaryPath 返回内置 Agent 二进制的路径（按架构）。
func BinaryPath(cfg *pkg.Config, arch string) string {
	for _, dir := range []string{filepath.Join(cfg.AppDir, "agent"), filepath.Join(cfg.DataDir, "agent")} {
		p := filepath.Join(dir, "zjagent-linux-"+arch)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// FileSHA256 计算文件摘要（安装脚本校验用）。
func FileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RunScript 通过 SSH 执行安装脚本并返回合并输出。
// 只用于「引导」这一件事；常驻控制走 WebSocket，不复用交互式终端。
func RunScript(client *ssh.Client, script string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(script)
	if err != nil {
		return string(out), fmt.Errorf("远程执行失败: %w", err)
	}
	return string(out), nil
}
