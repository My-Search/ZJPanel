//go:build windows

package backup

import "golang.org/x/sys/windows"

// diskFreeImpl GetDiskFreeSpaceEx 的 available 值：调用者实际可用空间（含配额语义）。
func diskFreeImpl(path string) int64 {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return -1
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return -1
	}
	return int64(free)
}
