//go:build windows

package service

// newScheduleBackend 在 Windows 上返回 nil。
//
// Windows 的自动更新由 **schtasks 注册的系统计划任务**负责（见 internal/cne
// 的 schedule_*.go）：任务由 Task Scheduler 拉起，应用关着也会跑。
// 再起一个进程内 goroutine 只会和它双跑、抢 cne 的写入锁，
// 所以这里刻意什么都不做。
func newScheduleBackend(*CneUpdateService) scheduleBackend {
	return nil
}
