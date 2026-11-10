//go:build !windows

package cne

// 非 Windows 平台（Linux / NAS / Docker）的设置读写。
//
// 与 schedule_other.go 一样，全部委托给 service 层注入的 LocalScheduler。
// 保持与 Windows 版**同名同签名**，这样 cneupdate.go 不需要按平台分叉。

// ApplyRecommendedSettings 让"错过也能补跑"生效。
//
// 进程内调度器天然补跑（应用重启后会补当天错过的那次），
// 所以这里只需要确认开关状态，不需要像 Windows 那样改 COM 设置。
func ApplyRecommendedSettings(taskName string) error {
	if localScheduler == nil {
		return errNoLocalScheduler{}
	}
	return localScheduler.ApplyRecommendedSettings(taskName)
}

// SetTaskEnabled 启用/禁用一个任务。
//
// 这就是界面上"自动更新开关"的实现：关掉之后到点不会跑。
func SetTaskEnabled(taskName string, enabled bool) error {
	if localScheduler == nil {
		return errNoLocalScheduler{}
	}
	return localScheduler.SetTaskEnabled(taskName, enabled)
}

// TaskEnabled 判断任务当前是启用还是禁用。
func TaskEnabled(taskName string) (bool, error) {
	if localScheduler == nil {
		return false, errNoLocalScheduler{}
	}
	return localScheduler.TaskEnabled(taskName)
}

// StartWhenAvailable 读回"错过计划时间后是否补跑"。
//
// 进程内调度器恒为 (true, true)：只要应用重新运行起来，
// 今天错过的那次会被补跑。第二个返回值表示"结论是否确定"。
func StartWhenAvailable(taskName string) (bool, bool) {
	if localScheduler == nil {
		return false, false
	}
	return localScheduler.StartWhenAvailable(taskName)
}
