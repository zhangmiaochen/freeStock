package service

import (
	"fmt"
	"strings"
	"time"
)

// 进程内调度器的**纯时间逻辑**。
//
// ⚠ 这些函数刻意放在不带 build tag 的文件里：
// 调度器本身是 Linux/NAS 专用的（schedule_local.go，`//go:build !windows`），
// 但"到点没有 / 下次什么时候"是最容易算错的部分，必须在**开发机（Windows）
// 上就能单测**。把时间计算与平台解耦，是这里唯一能验证的抓手。

// validClockLocal 校验 HH:MM。
//
// 与 cne.validClock 同口径 —— 两边分开定义是因为 cne 是底层包，
// 不该为了复用一个 5 行函数反向导出内部校验。
func validClockLocal(v string) bool {
	_, _, ok := parseClockLocal(v)
	return ok
}

// parseClockLocal 把 "HH:MM" 解析成时/分；非法返回 ok=false。
func parseClockLocal(v string) (hour, minute int, ok bool) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return 0, 0, false
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

// scheduledToday 返回"今天 at 点"的时刻（本地时区）。
func scheduledToday(at string, now time.Time) (time.Time, bool) {
	hour, minute, ok := parseClockLocal(at)
	if !ok {
		return time.Time{}, false
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, now.Location()), true
}

// nextRunText 返回界面显示用的"下次运行时间"：今天的计划时间；
// 若已过则顺延到明天。
//
// 格式与 Windows 的 schtasks "Next Run Time" 对齐（2026/11/10 17:30:00），
// 这样前端 `clockFromSchedule` 那类解析逻辑在两个平台上都能用。
func nextRunText(at string, now time.Time) (string, bool) {
	t, ok := scheduledToday(at, now)
	if !ok {
		return "", false
	}
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t.Format("2006/01/02 15:04:05"), true
}

// dueNow 判断某个任务"此刻该不该跑"。
//
// 三个条件同时满足才算到点：
//  1. 任务开关打开
//  2. 已过今天的计划时间
//  3. 今天还没成功发起过（lastRunDate != 今天）
//
// ⚠ 第 3 条是**防重复**的关键：ticker 每 30 秒看一次，
// 没有它就等于每 30 秒发一次更新。而"发起失败"（比如手动更新占着闸）
// 时不能写 lastRunDate，否则今天这次就被吞掉了 —— 这是调用方的责任，
// 本函数只回答"该不该"。
func dueNow(enabled bool, at string, lastRunDate string, now time.Time) bool {
	if !enabled {
		return false
	}
	scheduled, ok := scheduledToday(at, now)
	if !ok {
		return false
	}
	if now.Before(scheduled) {
		return false // 还没到点
	}
	return lastRunDate != now.Format("2006-01-02")
}
