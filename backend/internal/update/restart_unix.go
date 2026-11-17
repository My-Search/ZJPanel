//go:build !windows

package update

import (
	"log/slog"
	"os"
	"syscall"
)

// restartSelf 以 exec 原位替换当前进程镜像（PID 不变）：
// 容器内面板为 PID 1，exec 后容器与 nginx 子进程均不受影响，面板即以新版本继续运行。
func restartSelf(execPath string) {
	if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
		// exec 失败极罕见（如权限被改）；退出后由 restart: unless-stopped 拉起——
		// 二进制已替换，容器重启即运行新版本。
		slog.Error("面板更新：exec 重启失败，转由容器重启策略拉起", "err", err)
		os.Exit(3)
	}
}
