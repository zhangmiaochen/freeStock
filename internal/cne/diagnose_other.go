//go:build !windows

package cne

// Diagnose 在非 Windows 平台没有意义。
func Diagnose() string {
	return "当前平台不支持计划任务（只有 Windows 有任务计划程序）"
}
