//go:build !linux && !darwin

package main

// 非 Linux 平台仅用于本机开发编译，Agent 的实际运行目标是 Linux。
func diskUsageOS(string) (total, used uint64) { return 0, 0 }

func isRoot() bool { return false }
