package config

import "testing"

// UpdatePrefs.Sanitize 必须"永不报错，只纠正"（与 KlinePrefs 同一约定）。
//
// 计划任务对时间格式敏感：非法时间会让**任务注册失败**，
// 所以这里宁可回落默认值，也不能把非法值传下去。
func TestUpdatePrefsSanitizeClock(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"17:30", "17:30"},
		{"7:5", "07:05"},
		{"1730", "17:30"},   // 无冒号的紧凑写法
		{" 9:05 ", "09:05"}, // 带空格
		{"", "17:30"},       // 空 → 回落
		{"25:00", "17:30"},  // 小时越界
		{"12:99", "17:30"},  // 分钟越界
		{"abc", "17:30"},    // 非数字
		{"1:2:3", "17:30"},  // 段数不对
		{"-1:00", "17:30"},  // 负数
	}
	for _, c := range cases {
		p := UpdatePrefs{DailyTime: c.in}.Sanitize()
		if p.DailyTime != c.want {
			t.Errorf("Sanitize(%q) = %q，期望 %q", c.in, p.DailyTime, c.want)
		}
	}
	// StaleTime 走同一套规则
	if got := (UpdatePrefs{StaleTime: "2100"}).Sanitize().StaleTime; got != "21:00" {
		t.Errorf("StaleTime \"2100\" 应为 21:00，实际 %q", got)
	}
}

// 两个自动开关**默认必须关** —— 注册计划任务会改动用户机器。
func TestDefaultUpdatePrefsKeepsAutomationOff(t *testing.T) {
	def := DefaultUpdatePrefs()
	if def.AutoDaily || def.AutoStale {
		t.Errorf("自动更新默认必须是关闭的（会动用户的计划任务），实际 autoDaily=%v autoStale=%v",
			def.AutoDaily, def.AutoStale)
	}
	if def.DailyTime == "" || def.StaleTime == "" {
		t.Error("默认时间不应为空，否则计划任务注册会失败")
	}
	// 空值经过 Sanitize 后仍然是关闭的（不能因为纠正而"意外打开"）
	s := UpdatePrefs{}.Sanitize()
	if s.AutoDaily || s.AutoStale {
		t.Error("Sanitize 不应把自动开关打开")
	}
	if s.ProjectDir == "" {
		t.Error("ProjectDir 为空时应回落默认值")
	}
}

// 分组要去重去空。
func TestUpdatePrefsSanitizeGroups(t *testing.T) {
	p := UpdatePrefs{Groups: []string{"daily", "", "daily", " stale ", "stale"}}.Sanitize()
	if len(p.Groups) != 2 {
		t.Fatalf("应去重去空后剩 2 个，实际 %v", p.Groups)
	}
	if p.Groups[0] != "daily" || p.Groups[1] != "stale" {
		t.Errorf("分组内容不对：%v", p.Groups)
	}
}

// 读写往返。
func TestUpdatePrefsRoundTrip(t *testing.T) {
	store := NewStoreAt(t.TempDir() + "/config.json")

	if _, saved := store.UpdatePrefs(); saved {
		t.Error("新配置文件不应报告为已保存")
	}

	want := store.SetUpdatePrefs(UpdatePrefs{
		AutoDaily: true, DailyTime: "18:05",
		ProjectDir: `D:\somewhere`,
	})
	got, saved := store.UpdatePrefs()
	if !saved {
		t.Fatal("保存后应报告为已保存")
	}
	if got.AutoDaily != want.AutoDaily || got.DailyTime != want.DailyTime ||
		got.ProjectDir != want.ProjectDir {
		t.Errorf("往返不一致：写入 %+v，读回 %+v", want, got)
	}
	t.Logf("往返成功：autoDaily=%v dailyTime=%s projectDir=%s",
		got.AutoDaily, got.DailyTime, got.ProjectDir)
}
