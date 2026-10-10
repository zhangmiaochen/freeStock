//go:build !windows

package cne

import "errors"

// 非 Windows 平台没有任务计划程序。给出明确的"不支持"，
// 而不是静默成功 —— 否则界面会显示"已开启自动更新"但其实什么都没做。

func ApplyRecommendedSettings(taskName string) error {
	return errors.New("当前平台不支持计划任务（只有 Windows 有任务计划程序）")
}

func SetTaskEnabled(taskName string, enabled bool) error {
	return errors.New("当前平台不支持计划任务")
}

func TaskEnabled(taskName string) (bool, error) {
	return false, errors.New("当前平台不支持计划任务")
}

func StartWhenAvailable(taskName string) (bool, bool) {
	return false, false
}
