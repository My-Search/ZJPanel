package backup

// minFreeBytes 打包期间备份目录所在卷的最低剩余空间水位（500MB）。
// 打包中跌破水位立即判定备份失败并删除半截包，避免把宿主机磁盘占满。
const minFreeBytes = int64(500) << 20

// diskFreeBytes 查询 path 所在卷对当前用户可用的剩余空间（字节）。
// 平台实现在 diskfree_unix.go / diskfree_windows.go；
// 包级变量以便单测注入任意水位验证中止与清理逻辑。
var diskFreeBytes = diskFreeImpl

// humanFreeBytes 磁盘剩余空间展示：查询失败显示「未知」而不阻断打包流程。
func humanFreeBytes(n int64) string {
	if n < 0 {
		return "未知"
	}
	return humanBytes(n)
}
