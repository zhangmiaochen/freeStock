package service

import (
	"testing"
	"time"
)

// 时区固定成 UTC+8（与部署环境 Asia/Shanghai 同偏移），
// 避免测试机时区不同导致"到点没到点"判断飘。
func fixedZone(t *testing.T) *time.Location {
	t.Helper()
	loc := time.FixedZone("CST", 8*3600)
	return loc
}

// validClockLocal 必须跟 cne.validClock 一个口径 —— 非法值会让
// 调度器永久不触发（而不是报错），所以这里把边界钉死。
func TestValidClockLocal(t *testing.T) {
	good := []string{"00:00", "17:30", "23:59", "9:05", " 8:00 "}
	bad := []string{"", "1730", "24:00", "12:60", "-1:00", "abc", "12:34:56", "12", "12:xx"}
	for _, v := range good {
		if !validClockLocal(v) {
			t.Errorf("validClockLocal(%q) 应为 true", v)
		}
	}
	for _, v := range bad {
		if validClockLocal(v) {
			t.Errorf("validClockLocal(%q) 应为 false", v)
		}
	}
}

// dueNow 是"到点没有"的唯一判据。四种情形必须都对：
// 未开启 / 没到点 / 今天已跑 / 已过点且今天没跑。
func TestDueNow(t *testing.T) {
	loc := fixedZone(t)

	cases := []struct {
		name        string
		enabled     bool
		at          string
		lastRunDate string
		now         time.Time
		want        bool
		why         string
	}{
		{
			name:    "未开启不跑",
			enabled: false, at: "17:30", now: time.Date(2026, 11, 10, 18, 0, 0, 0, loc),
			want: false, why: "开关关着",
		},
		{
			name:    "还没到点不跑",
			enabled: true, at: "17:30", now: time.Date(2026, 11, 10, 17, 29, 59, 0, loc),
			want: false, why: "差 1 秒",
		},
		{
			name:    "刚好到点要跑",
			enabled: true, at: "17:30", now: time.Date(2026, 11, 10, 17, 30, 0, 0, loc),
			want: true, why: "边界：等于计划时间就算到点",
		},
		{
			name:    "过点未跑要补跑",
			enabled: true, at: "17:30", now: time.Date(2026, 11, 10, 23, 0, 0, 0, loc),
			want: true, why: "应用重启后补跑",
		},
		{
			name:    "今天已跑不重复",
			enabled: true, at: "17:30", lastRunDate: "2026-11-10",
			now:  time.Date(2026, 11, 10, 20, 0, 0, 0, loc),
			want: false, why: "防每 30 秒重复触发",
		},
		{
			name:    "昨天跑过今天还要跑",
			enabled: true, at: "17:30", lastRunDate: "2026-11-09",
			now:  time.Date(2026, 11, 10, 17, 45, 0, 0, loc),
			want: true, why: "跨天必须重置",
		},
		{
			name:    "非法时间永不触发",
			enabled: true, at: "25:00", now: time.Date(2026, 11, 10, 23, 0, 0, 0, loc),
			want: false, why: "配置脏数据不该造成误跑",
		},
	}
	for _, c := range cases {
		if got := dueNow(c.enabled, c.at, c.lastRunDate, c.now); got != c.want {
			t.Errorf("%s: dueNow = %v，期望 %v（%s）", c.name, got, c.want, c.why)
		}
	}
}

// nextRunText 是界面上的"下次运行时间"，格式要与 Windows 的
// schtasks 一致（前端按同一套解析）。
func TestNextRunText(t *testing.T) {
	loc := fixedZone(t)

	// 17:30 还没到 → 今天
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, loc)
	got, ok := nextRunText("17:30", now)
	if !ok || got != "2026/11/10 17:30:00" {
		t.Errorf("nextRunText = %q,%v，期望 2026/11/10 17:30:00", got, ok)
	}

	// 已经过了 → 顺延到明天
	now = time.Date(2026, 11, 10, 18, 0, 0, 0, loc)
	got, ok = nextRunText("17:30", now)
	if !ok || got != "2026/11/11 17:30:00" {
		t.Errorf("nextRunText = %q,%v，期望 2026/11/11 17:30:00", got, ok)
	}

	// 非法输入不编时间
	if _, ok := nextRunText("bad", now); ok {
		t.Error("非法时间不该返回 ok")
	}
}

// scheduledToday 必须落在"同一天"，跨月/跨年不能算错。
func TestScheduledTodayAcrossBoundaries(t *testing.T) {
	loc := fixedZone(t)
	for _, now := range []time.Time{
		time.Date(2026, 1, 1, 0, 1, 0, 0, loc),    // 年初
		time.Date(2026, 12, 31, 23, 0, 0, 0, loc), // 年末
		time.Date(2028, 2, 29, 12, 0, 0, 0, loc),  // 闰日
	} {
		got, ok := scheduledToday("17:30", now)
		if !ok {
			t.Fatalf("scheduledToday(%v) 失败", now)
		}
		gy, gm, gd := got.Date()
		ny, nm, nd := now.Date()
		if gy != ny || gm != nm || gd != nd {
			t.Errorf("日期漂移：now=%v got=%v", now, got)
		}
		if got.Hour() != 17 || got.Minute() != 30 {
			t.Errorf("时分不对：%v", got)
		}
	}
}

// 关键回归：调度时间必须按**本地时区**解释。
//
// 容器里如果没装 tzdata、TZ 没设对，time.Now() 会是 UTC ——
// 「17:30」实际在北京时间次日 01:30 才触发，而且不报任何错。
func TestDueNowUsesLocalZone(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	utc := time.UTC

	// 北京 18:00 = UTC 10:00，此时北京 17:30 已过、UTC 17:30 未到
	moment := time.Date(2026, 11, 10, 10, 0, 0, 0, utc)

	if !dueNow(true, "17:30", "", moment.In(cst)) {
		t.Error("按 CST 解释时 17:30 应该已到点")
	}
	if dueNow(true, "17:30", "", moment.In(utc)) {
		t.Error("按 UTC 解释时 17:30 尚未到点 —— 说明判断依赖时区（这正是 tzdata 必须存在的原因）")
	}
}
