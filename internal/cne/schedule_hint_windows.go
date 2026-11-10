//go:build windows

package cne

import "fmt"

// changeTaskHint 返回"改这个任务的时间"在 Windows 上对应的原始命令。
//
// 界面的确认框里原样展示它 —— 让用户清楚自己机器上会发生什么。
func changeTaskHint(task, at string) string {
	return fmt.Sprintf(`schtasks /change /tn "%s" /st %s`, task, at)
}
