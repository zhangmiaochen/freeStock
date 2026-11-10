//go:build !windows

package cne

import "fmt"

// changeTaskHint 返回"改这个任务的时间"在 Linux / NAS 上真正会发生的事。
//
// 这里**不能**照抄 Windows 的 `schtasks /change`：那台机器上没有 schtasks，
// 用户看到一条不存在的命令会以为功能是坏的（或者以为自己要手动去跑）。
// 实际行为是：把时间写回 config.json，进程内调度器下一个 tick 就按新时间生效。
func changeTaskHint(task, at string) string {
	return fmt.Sprintf("写入 config.json：%s 的每日执行时间改为 %s（进程内调度器即时生效）", task, at)
}
