package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"freestock/internal/cne"
	"freestock/internal/config"
)

func newCneUpdateService(t *testing.T) (*CneUpdateService, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "cnequity.toml"),
		[]byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	store.SetUpdatePrefs(config.UpdatePrefs{ProjectDir: dir})
	return NewCneUpdateService(store), dir
}

// kind 参数必须被严格校验 —— 不能把任意字符串拼进命令行。
func TestManualUpdateRejectsUnknownKind(t *testing.T) {
	svc, _ := newCneUpdateService(t)

	for _, bad := range []string{"rm -rf", "daily; whoami", "ingest", "unknown"} {
		if _, err := svc.ManualUpdate(bad, ""); err == nil {
			t.Errorf("kind=%q 应当被拒绝", bad)
		}
	}
	// 前端可能传 trade_date 这种写法，应当被接受为 date
	if _, err := svc.ManualUpdate("trade-date", ""); err == nil {
		t.Error("date 类型缺少日期时应当报错（不能静默当成普通日更）")
	} else if !strings.Contains(err.Error(), "日期") {
		t.Errorf("错误信息应说明缺日期，实际：%v", err)
	}
	t.Log("非法 kind 与缺日期都被正确拒绝")
}

// 未配置 ProjectDir 时，设置校验要给得出问题（而不是等到点更新才失败）。
func TestSetUpdatePrefsReportsUnusableProjectDir(t *testing.T) {
	store := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewCneUpdateService(store)

	// 先给一个能用的，确认没有 warning
	good := t.TempDir()
	if err := os.MkdirAll(filepath.Join(good, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(good, "configs", "cnequity.toml"), []byte("x"), 0o644)
	info, err := svc.SetUpdatePrefs(config.UpdatePrefs{ProjectDir: good})
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if len(info.Warnings) != 0 {
		t.Errorf("可用的工程目录不该有 warning，实际 %v", info.Warnings)
	}

	// 再给一个不存在的，应当有 warning（但保存仍然成功）
	info, err = svc.SetUpdatePrefs(config.UpdatePrefs{ProjectDir: `D:\definitely\not\here\xyz`})
	if err != nil {
		t.Fatalf("保存不该失败（用户可能想先填好设置）：%v", err)
	}
	if len(info.Warnings) == 0 {
		t.Error("工程目录不存在时应当给出 warning")
	}
	t.Logf("warning：%v", info.Warnings)
}

// 设置读写往返，并且 Runner 会用上新设置。
func TestUpdatePrefsRoundTripViaService(t *testing.T) {
	svc, dir := newCneUpdateService(t)

	// helper 里已经保存过一次工程目录，所以这里 Saved 本来就该是 true ——
	// 要看的是"还没保存过的默认值"，用另一份干净的 store 验。
	fresh := NewCneUpdateService(config.NewStoreAt(filepath.Join(t.TempDir(), "fresh.json")))
	freshInfo := fresh.GetUpdatePrefs()
	if freshInfo.Saved {
		t.Error("全新配置不应报告为已保存")
	}
	// 默认必须是"自动全关"
	if freshInfo.Prefs.AutoDaily || freshInfo.Prefs.AutoStale {
		t.Errorf("自动更新默认必须关闭，实际 autoDaily=%v autoStale=%v",
			freshInfo.Prefs.AutoDaily, freshInfo.Prefs.AutoStale)
	}
	info := svc.GetUpdatePrefs()
	if !info.Saved {
		t.Error("helper 已保存过，这里应为已保存")
	}

	if _, err := svc.SetUpdatePrefs(config.UpdatePrefs{
		AutoDaily: true, DailyTime: "18:05", ProjectDir: dir,
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	got := svc.GetUpdatePrefs()
	if !got.Saved || !got.Prefs.AutoDaily || got.Prefs.DailyTime != "18:05" {
		t.Errorf("往返不一致：%+v", got.Prefs)
	}
	if got.ConfigPath == "" {
		t.Error("应给出配置文件路径，便于用户核对")
	}
	t.Logf("往返成功：%+v", got.Prefs)
}

// 没有任务时状态应为空、取消应报错（不能静默成功）。
func TestStatusAndCancelWithNoJob(t *testing.T) {
	svc, _ := newCneUpdateService(t)

	st, err := svc.UpdateStatus()
	if err != nil {
		t.Fatalf("取状态失败：%v", err)
	}
	if st.Running {
		t.Error("没有任务时不应报告运行中")
	}
	if err := svc.CancelUpdate(); err == nil {
		t.Error("没有任务时取消应当报错，而不是装作成功")
	}
	t.Log("无任务时状态为空、取消报错")
}

// cne 类型常量与前端约定必须一致 —— 前端按字符串传 kind。
func TestCneKindConstants(t *testing.T) {
	if cne.KindFull != "full" || cne.KindStale != "stale" || cne.KindDate != "date" {
		t.Errorf("kind 常量改了就要同步前端：full=%q stale=%q date=%q",
			cne.KindFull, cne.KindStale, cne.KindDate)
	}
}
