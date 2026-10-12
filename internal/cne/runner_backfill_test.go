package cne

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **回归**：补历史区间必须发出 `cne backfill daily_bars --start X --end Y`，
// 而不是 `cne run daily ...`。
//
// 这两条是**不同的子命令**：`run daily` 是日更 pipeline，只认单日 `--trade-date`；
// 补历史要用 `backfill`。这正是"手动更新只能补交易日、不能补区间"的根本原因 ——
// 不是前端缺输入框，而是这条命令路径以前根本不存在。
//
// 参数拼错不会报错，只会安静地补不到数据（或补错范围），所以必须钉死。
func TestDefaultArgsForBackfillUsesBackfillSubcommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("建 configs 目录失败：%v", err)
	}
	cfg := filepath.Join(dir, "configs", "cnequity.toml")
	if err := os.WriteFile(cfg, []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	r := NewRunner(Options{ProjectDir: dir})
	args, err := r.defaultArgs(KindBackfill,
		[]string{"daily_bars", "--start", "2021-01-01", "--end", "2023-10-08"})
	if err != nil {
		t.Fatalf("拼参数失败：%v", err)
	}

	want := []string{
		"backfill", "daily_bars",
		"--start", "2021-01-01", "--end", "2023-10-08",
		"--config", cfg,
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("backfill 参数拼错：\n  实际 %v\n  期望 %v", args, want)
	}
	// 反向断言：绝不能混进日更 pipeline 的写法
	for _, bad := range []string{"run", "daily", "--stale-only", "--trade-date"} {
		for _, got := range args {
			if got == bad {
				t.Errorf("backfill 参数里不该出现 %q：%v", bad, args)
			}
		}
	}
}

// 另外三种类型**必须仍然**走 `run daily` —— 改 backfill 时很容易把它们带偏。
func TestDefaultArgsForDailyKindsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("建 configs 目录失败：%v", err)
	}
	cfg := filepath.Join(dir, "configs", "cnequity.toml")
	if err := os.WriteFile(cfg, []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	r := NewRunner(Options{ProjectDir: dir})

	cases := []struct {
		kind  string
		extra []string
		want  []string
	}{
		{KindFull, nil, []string{"run", "daily", "--config", cfg}},
		{KindStale, nil, []string{"run", "daily", "--stale-only", "--config", cfg}},
		{KindDate, []string{"--trade-date", "2026-10-09"},
			[]string{"run", "daily", "--trade-date", "2026-10-09", "--config", cfg}},
	}
	for _, tc := range cases {
		args, err := r.defaultArgs(tc.kind, tc.extra)
		if err != nil {
			t.Fatalf("%s 拼参数失败：%v", tc.kind, err)
		}
		if strings.Join(args, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s 参数变了：\n  实际 %v\n  期望 %v", tc.kind, args, tc.want)
		}
	}
}

// StartBackfill 的参数校验：数据集白名单 / 空值 / 起止颠倒 必须在**起进程之前**被拒。
//
// 这些都不该走到 executable()/foreignRunning()（那会给出误导性的
// "已有 cne 进程在运行"），所以 ProjectDir 故意留空。
func TestStartBackfillRejectsBadRangeBeforeSpawning(t *testing.T) {
	r := NewRunner(Options{}) // 没有 ProjectDir：校验通过就会在这里失败

	if _, err := r.StartBackfill("daily_bars", "", "2023-10-08"); err == nil ||
		!strings.Contains(err.Error(), "起止日期") {
		t.Errorf("空起点应被拒且说明原因，实际 %v", err)
	}
	if _, err := r.StartBackfill("daily_bars", "2021-01-01", "  "); err == nil ||
		!strings.Contains(err.Error(), "起止日期") {
		t.Errorf("空终点应被拒且说明原因，实际 %v", err)
	}
	if _, err := r.StartBackfill("daily_bars", "2023-10-08", "2021-01-01"); err == nil ||
		!strings.Contains(err.Error(), "晚于") {
		t.Errorf("起点晚于终点应被拒，实际 %v", err)
	}
	// 数据集名拼错必须在起进程前就被白名单挡住（cne 自己的报错很难懂）
	if _, err := r.StartBackfill("daily_bar", "2021-01-01", "2023-10-08"); err == nil ||
		!strings.Contains(err.Error(), "不支持回补数据集") {
		t.Errorf("拼错的数据集名应被白名单拒绝，实际 %v", err)
	}
}

// **回归**：指数日线必须能独立回补。
//
// 实机踩过的坑：只补 `daily_bars` 之后，回填仍然只有 **728** 个交易日 ——
// 因为交易日历取自 `index_bars` 里的基准指数，而那个数据集没被补。
// 所以"数据集必须可选"这条不变量要钉住。
func TestStartBackfillAcceptsIndexBars(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("建 configs 目录失败：%v", err)
	}
	cfg := filepath.Join(dir, "configs", "cnequity.toml")
	if err := os.WriteFile(cfg, []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	r := NewRunner(Options{ProjectDir: dir})

	args, err := r.defaultArgs(KindBackfill,
		[]string{"index_bars", "--start", "2021-01-01", "--end", "2023-10-08"})
	if err != nil {
		t.Fatalf("拼参数失败：%v", err)
	}
	want := []string{
		"backfill", "index_bars",
		"--start", "2021-01-01", "--end", "2023-10-08",
		"--config", cfg,
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("index_bars 回补参数拼错：\n  实际 %v\n  期望 %v", args, want)
	}
	// 白名单必须同时含个股与指数
	for _, name := range []string{"daily_bars", "index_bars"} {
		if !IsBackfillDataset(name) {
			t.Errorf("白名单缺少 %q：%v", name, BackfillDatasetNames())
		}
	}
}
