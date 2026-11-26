//go:build linux || darwin

package main

import "syscall"

// diskUsageOS 取文件系统容量（Linux 目标机实际路径）。
func diskUsageOS(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return total, total - free
}

func isRoot() bool { return syscall.Geteuid() == 0 }
