//go:build !windows

package service

import (
	"testing"

	"freestock/internal/cne"
	"freestock/internal/config"
)

// 「开启自动更新」在 Linux/NAS 上的完整往返。
//
// 这条链路和 Windows 完全不同：Windows 是去启停 schtasks 任务，
// Linux 是**把 autoDaily/autoStale 写进 config.json**，状态也从那里读回来。
// 之前没有针对它的测试，于是"界面显示已开启、实际配置还是 false"这类问题
// 只能靠用户发现。
func TestAutoUpdateSwitchRoundTripOnLinux(t *testing.T) {
	svc, _ := newCneUpdateService(t)
	// 注册进程内调度器（不 start，避免后台 tick 干扰）
	if be := newScheduleBackend(svc); be == nil {
		t.Fatal("Linux 上必须返回进程内调度器")
	}

	// 默认是关的
	st, err := svc.AutoUpdateStatus()
	if err != nil {
		t.Fatalf("读状态失败：%v", err)
	}
	if st.Enabled {
		t.Fatalf("默认应当是关闭的，实际 %+v", st)
	}

	// 打开
	st, err = svc.SetAutoUpdate(true)
	if err != nil {
		t.Fatalf("开启失败：%v", err)
	}
	if !st.Enabled || !st.DailyEnabled || !st.StaleEnabled {
		t.Errorf("开启后状态应为全开：%+v", st)
	}

	// 必须**落盘**（重启后还要是开的）
	prefs, _ := svc.store.UpdatePrefs()
	if !prefs.AutoDaily || !prefs.AutoStale {
		t.Errorf("开关没有写进配置：autoDaily=%v autoStale=%v",
			prefs.AutoDaily, prefs.AutoStale)
	}

	// 再读一次状态（模拟界面刷新）
	st, err = svc.AutoUpdateStatus()
	if err != nil {
		t.Fatalf("读状态失败：%v", err)
	}
	if !st.Enabled {
		t.Errorf("刷新后不应变回关闭：%+v", st)
	}

	// 关闭，同样要落盘
	st, err = svc.SetAutoUpdate(false)
	if err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	if st.Enabled {
		t.Errorf("关闭后状态应为关闭：%+v", st)
	}
	prefs, _ = svc.store.UpdatePrefs()
	if prefs.AutoDaily || prefs.AutoStale {
		t.Errorf("关闭没有写进配置：autoDaily=%v autoStale=%v",
			prefs.AutoDaily, prefs.AutoStale)
	}
}

// 改时间（ApplySchedule）只动时间，不会把自动更新开关一起打开。
//
// 用户常见的误解是"我改了时间，等于配好了自动更新"——两者确实是分开的，
// 这里把边界钉住。
func TestApplyScheduleDoesNotEnableAutoUpdate(t *testing.T) {
	svc, _ := newCneUpdateService(t)
	if be := newScheduleBackend(svc); be == nil {
		t.Fatal("Linux 上必须返回进程内调度器")
	}

	prefs, _ := svc.store.UpdatePrefs()
	prefs.DailyTime = "17:30"
	prefs.StaleTime = "21:00"
	svc.store.SetUpdatePrefs(prefs)

	plan, err := svc.PreviewSchedule("18:00", "21:30")
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	if !plan.HasChanges {
		t.Fatalf("时间变了应当有改动：%+v", plan)
	}
	if _, err := svc.ApplySchedule(plan, true); err != nil {
		t.Fatalf("应用失败：%v", err)
	}

	got, _ := svc.store.UpdatePrefs()
	if got.AutoDaily || got.AutoStale {
		t.Errorf("应用时间改动不应打开开关：autoDaily=%v autoStale=%v",
			got.AutoDaily, got.AutoStale)
	}
	if got.DailyTime != "18:00" || got.StaleTime != "21:30" {
		t.Errorf("时间没写进去：daily=%q stale=%q", got.DailyTime, got.StaleTime)
	}
}

// 确保 cne 包被引用（这些常量是跨平台约定的锚点）。
var _ = cne.TaskDaily
var _ = config.UpdatePrefs{}
