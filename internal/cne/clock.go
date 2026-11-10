package cne

import (
	"fmt"
	"strings"
	"time"
)

// validClock 校验 HH:MM。
//
// ⚠ 这个函数**必须放在不带 build tag 的文件里**：
// `schedule_plan.go`（预览/应用改动）在 Windows 和非 Windows 上都编译，
// 而它要用 validClock 校验用户输入。之前它只在 schedule_windows.go 里，
// 导致 `GOOS=linux go build -tags server` 直接报 undefined: validClock。
//
// 计划任务（Windows schtasks）对时间格式敏感，非法值会让注册失败；
// 进程内调度器（Linux/NAS）同样只认 HH:MM，所以校验口径两边一致。
func validClock(v string) bool {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return false
	}
	hour, minute := 0, 0
	if _, err := fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return false
	}
	return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
}

// parseClock 把 "HH:MM" 解析成当天的时/分。
//
// 只在 validClock 通过后调用；解析失败返回 ok=false，
// 调用方据此跳过，而不是拿 0 点当作"到点了"。
func parseClock(v string) (hour, minute int, ok bool) {
	v = strings.TrimSpace(v)
	if !validClock(v) {
		return 0, 0, false
	}
	parts := strings.SplitN(v, ":", 2)
	if _, err := fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return 0, 0, false
	}
	return hour, minute, true
}

// clockTimeOn 返回 base 这一天（本地时区）的 hour:minute 时刻。
func clockTimeOn(base time.Time, hour, minute int) time.Time {
	y, m, d := base.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, base.Location())
}
