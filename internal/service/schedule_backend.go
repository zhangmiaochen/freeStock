package service

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// scheduleBackend 是"自动更新由谁到点触发"的平台抽象。
//
//   - Windows：schtasks 注册系统计划任务，应用关着也能跑 —— 后端为 nil
//   - Linux / NAS / Docker：进程内调度器（schedule_local.go）
//
// 之所以做成后端而不是在 Windows 上也起一个 goroutine：两套同时存在必然双跑，
// 撞 cne 的 ingest 锁。所以**同一时刻只有一套生效**。
type scheduleBackend interface {
	start()
	stop()
}

// ServiceStartup 启动自动更新的调度后端。
//
// Windows 上后端为 nil（走 schtasks），这里什么都不做 —— 原来的行为不变。
func (s *CneUpdateService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.mu.Lock()
	if s.sched == nil {
		s.sched = newScheduleBackend(s)
	}
	sched := s.sched
	s.mu.Unlock()

	if sched != nil {
		sched.start()
	}
	return nil
}

// ServiceShutdown 停掉调度后端（Windows 上是空操作）。
func (s *CneUpdateService) ServiceShutdown() error {
	s.mu.Lock()
	sched := s.sched
	s.mu.Unlock()

	if sched != nil {
		sched.stop()
	}
	return nil
}
