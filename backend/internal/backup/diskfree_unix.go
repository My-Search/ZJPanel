//go:build !windows

package backup

import "syscall"

// diskFreeImpl Statfs 的 Bavail×Bsize：非特权进程实际可用的剩余空间（预留块除外）。
func diskFreeImpl(path string) int64 {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return -1
	}
	return int64(fs.Bavail) * int64(fs.Bsize)
}
