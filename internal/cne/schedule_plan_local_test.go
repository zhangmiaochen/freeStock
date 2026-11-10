//go:build !windows

package cne

import (
	"strings"
	"testing"
)

// fakeLocalScheduler 是最小的 LocalScheduler 实现，用来测"计划任务预览"。
type fakeLocalScheduler struct {
	daily taskStub
	stale taskStub
	// applied 记录 ApplyRecommendedSettings 被调用了几次（有的路径会用）
	applied int
}

type taskStub struct {
	enabled bool
	at      string
}

func (f *fakeLocalScheduler) stub(name string) taskStub {
	if name == TaskStale {
		return f.stale
	}
	return f.daily
}

func (f *fakeLocalScheduler) Query(name string) (*TaskInfo, error) {
	s := f.stub(name)
	info := &TaskInfo{
		Name:                    name,
		Registered:              true,
		StartWhenAvailable:      true,
		StartWhenAvailableKnown: true,
		Enabled:                 s.enabled,
		ConfiguredTime:          s.at,
		Status:                  "Ready",
	}
	if !s.enabled {
		info.Status = "Disabled"
	}
	return info, nil
}

func (f *fakeLocalScheduler) QuerySchedule() (*ScheduleState, error) {
	d, err := f.Query(TaskDaily)
	if err != nil {
		return nil, err
	}
	s, err := f.Query(TaskStale)
	if err != nil {
		return nil, err
	}
	return &ScheduleState{Daily: *d, Stale: *s}, nil
}

func (f *fakeLocalScheduler) CreateTask(name, scriptPath, at string) error {
	return nil
}
func (f *fakeLocalScheduler) ChangeTaskTime(name, at string) error { return nil }
func (f *fakeLocalScheduler) DeleteTask(name string) error         { return nil }
func (f *fakeLocalScheduler) SetTaskEnabled(name string, enabled bool) error {
	return nil
}
func (f *fakeLocalScheduler) TaskEnabled(name string) (bool, error) {
	return f.stub(name).enabled, nil
}
func (f *fakeLocalScheduler) ApplyRecommendedSettings(name string) error {
	f.applied++
	return nil
}
func (f *fakeLocalScheduler) StartWhenAvailable(name string) (bool, bool) {
	return true, true
}

// withFakeScheduler 临时替换全局调度器，结束后恢复（避免污染其它用例）。
func withFakeScheduler(t *testing.T, f *fakeLocalScheduler) {
	t.Helper()
	prev := localScheduler
	t.Cleanup(func() { localScheduler = prev })
	RegisterLocalScheduler(f)
}

// 任务**禁用**时（自动更新关着），预览也要能拿到"当前设置的时间"。
//
// 之前只在启用时才填 NextRun，于是禁用状态下打开设置页会报
// "读不到当前运行时间，为避免误改而不动它" —— 明明时间就在配置里，
// 用户完全看不懂这句话。
func TestPreviewScheduleUsesConfiguredTimeWhenDisabled(t *testing.T) {
	withFakeScheduler(t, &fakeLocalScheduler{
		daily: taskStub{enabled: false, at: "17:30"},
		stale: taskStub{enabled: false, at: "21:00"},
	})

	plan, err := PreviewSchedule("17:30", "21:00")
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("时间没变时不该有告警，实际：%v", plan.Warnings)
	}
	if plan.HasChanges {
		t.Error("时间没变时不该有改动")
	}
	for _, c := range plan.Changes {
		if c.Action != "keep" {
			t.Errorf("%s 应为 keep，实际 %s（%s）", c.Task, c.Action, c.Reason)
		}
		if c.From != c.To {
			t.Errorf("%s 当前时间应读得到：from=%q to=%q", c.Task, c.From, c.To)
		}
	}
}

// 禁用状态下改时间：要能算出 17:30 → 18:00，并给出该平台真正的动作提示。
func TestPreviewScheduleChangesTimeWhenDisabled(t *testing.T) {
	withFakeScheduler(t, &fakeLocalScheduler{
		daily: taskStub{enabled: false, at: "17:30"},
		stale: taskStub{enabled: false, at: "21:00"},
	})

	plan, err := PreviewSchedule("18:00", "21:00")
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	if !plan.HasChanges {
		t.Fatal("时间变了应当有改动")
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("不该有告警：%v", plan.Warnings)
	}

	var changed []ScheduleChange
	for _, c := range plan.Changes {
		if c.Action == "change" {
			changed = append(changed, c)
		}
	}
	if len(changed) != 1 || changed[0].Task != TaskDaily {
		t.Fatalf("应只有主日更需要改，实际 %v", changed)
	}
	if changed[0].From != "17:30" || changed[0].To != "18:00" {
		t.Errorf("应显示 17:30 → 18:00，实际 %q → %q", changed[0].From, changed[0].To)
	}

	if len(plan.Commands) != 1 {
		t.Fatalf("应给出一条动作提示，实际 %v", plan.Commands)
	}
	// Linux/NAS 上不是 schtasks，而是写回 config.json
	if strings.Contains(plan.Commands[0], "schtasks") {
		t.Errorf("Linux 上的提示不该是 schtasks 命令：%q", plan.Commands[0])
	}
}
