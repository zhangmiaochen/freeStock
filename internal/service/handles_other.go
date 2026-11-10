//go:build !windows

package service

// openHandleCount 在非 Windows 平台不适用，返回 -1。
//
// ⚠ 这个文件必须存在：`openHandleCount` 被 backtest.go / portfolio.go
// 这类**跨平台**文件调用，Windows 实现在 handles_windows.go 里
// （文件名后缀已隐含 build tag），Linux/NAS 上必须有个同签名的实现，
// 否则 `GOOS=linux go build -tags server` 会报 undefined。
//
// 用 /proc/<pid>/fd 也能数，但那是 Linux 专有且容器里可能受限；
// 这里刻意返回 -1（"不适用"），而不是编一个数字 —— 日志里看到 -1
// 就说明这条诊断信息在当前平台没有意义。
func openHandleCount() int {
	return -1
}
