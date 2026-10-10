//go:build !windows

package cne

import "errors"

// 非 Windows 平台没有 schtasks。本项目主要面向 Windows 桌面端，
// 这些分支只保证能编译，并给出明确的"不支持"而不是静默成功。

const (
	TaskDaily = "cne-daily-update"
	TaskStale = "cne-daily-stale"
)

type TaskInfo struct {
	Name                    string `json:"name"`
	Registered              bool   `json:"registered"`
	NextRun                 string `json:"nextRun"`
	Status                  string `json:"status"`
	LastResult              string `json:"lastResult"`
	LastRun                 string `json:"lastRun"`
	Command                 string `json:"command"`
	Enabled                 bool   `json:"enabled"`
	ScheduledState          string `json:"scheduledState"`
	StartWhenAvailable      bool   `json:"startWhenAvailable"`
	StartWhenAvailableKnown bool   `json:"startWhenAvailableKnown"`
}

type ScheduleState struct {
	Daily       TaskInfo `json:"daily"`
	Stale       TaskInfo `json:"stale"`
	WrapperPath string   `json:"wrapperPath"`
	Summary     string   `json:"summary"`
	Note        string   `json:"note"`
}

func Query(name string) (*TaskInfo, error) {
	return &TaskInfo{Name: name}, nil
}

func QuerySchedule() (*ScheduleState, error) {
	return &ScheduleState{
		Summary: "当前平台不支持计划任务（只有 Windows 有 schtasks）",
		Note:    "自动更新在本平台不可用",
	}, nil
}

func CreateTask(name, scriptPath, at string) error {
	return errors.New("当前平台不支持计划任务")
}

func ChangeTaskTime(name, at string) error {
	return errors.New("当前平台不支持计划任务")
}

func DeleteTask(name string) error {
	return errors.New("当前平台不支持计划任务")
}
