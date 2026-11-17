//go:build windows

package update

import (
	"log/slog"
	"os"
	"os/exec"
)

// restartSelf Windows：启动新进程后退出当前进程（旧 exe 已改名，映射句柄仍有效，可正常退出）。
func restartSelf(execPath string) {
	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		slog.Error("面板更新：重启失败，请手动启动面板", "err", err)
		os.Exit(3)
	}
	os.Exit(0)
}
