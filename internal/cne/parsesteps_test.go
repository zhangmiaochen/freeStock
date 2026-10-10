package cne

import (
	"strings"
	"testing"
)

// **用真实日志行做夹具** —— 取自用户实际跑手动更新时产生的
// `data/cnequity/logs/freestock-update-20261010-123916.log`。
//
// ⚠ 这里刻意**不自己编样例**：第一版解析器就是照方案文档里的示例
// （`hot_rank success 1.2s 100 rows`）写的，而真实日志完全不是那个样子，
// 结果对真实的每一条都匹配不上、卡片徽章一直是空的。
// 用真实行当夹具，才能保证解析器是照着现实写的。
var realLogLines = []string{
	`日志写到 D:\workspace\stockTool\CNEquity\data\cnequity\logs\cne-run-daily-20261010-123919.log`,
	`2026-10-10 12:39:19,571 INFO cnequity.orchestrator.engine: Skipping job daily:core: 2026-10-10 is not a trading day`,
	`调度组 core：skipped_non_trading_day`,
	`调度组 capital：skipped_non_trading_day`,
	`调度组 signals：skipped_non_trading_day`,
	`调度组 fundamentals：skipped_non_trading_day`,
	`调度组 macro_risk：skipped_non_trading_day`,
	`调度组 research：skipped_non_trading_day`,
	`调度组 derivatives：跳过（它要抓的数据集全部处于关闭状态）`,
	`调度组 intraday：跳过（它要抓的数据集全部处于关闭状态）`,
	`调度组 ticks：跳过（它要抓的数据集全部处于关闭状态）`,
	`2026-10-10 12:39:19,788 INFO cnequity.orchestrator.engine: Wave events:disclosures: ['announcement_index', 'compact'] (parallel=True)`,
	`2026-10-10 12:39:19,805 INFO cnequity.orchestrator.engine: Step announcement_index starting`,
	`2026-10-10 12:40:24,645 INFO cnequity.progress: still working: announcement_index 1m04s (no output for 1m04s)`,
	`2026-10-10 12:50:25,559 INFO cnequity.progress: still working: announcement_index 11m05s (no output for 1m00s)`,
	`2026-10-10 12:54:25,892 INFO cnequity.progress: still working: announcement_index 15m06s (no output for 1m00s)`,
}

// 解析器必须能从真实日志里认出**非交易日跳过的调度组**。
//
// 这条对用户最重要：它解释"为什么这次更新没碰行情数据"。
func TestParseStepsFindsSkippedGroups(t *testing.T) {
	steps := parseSteps(realLogLines)

	byName := map[string]StepInfo{}
	for _, s := range steps {
		byName[s.Name] = s
	}

	for _, group := range []string{"core", "capital", "signals", "fundamentals", "macro_risk", "research"} {
		s, ok := byName[group]
		if !ok {
			t.Errorf("**没认出跳过的调度组 %s** —— 用户就看不出这次为什么没更新行情", group)
			continue
		}
		if s.State != "skipped" {
			t.Errorf("%s 的状态应为 skipped，实际 %q", group, s.State)
		}
		if !strings.Contains(s.Rows, "non_trading_day") {
			t.Errorf("%s 应带上跳过原因 non_trading_day，实际 %q", group, s.Rows)
		}
	}

	// `跳过（原因）` 那种写法也要认，并保留中文原因
	for _, group := range []string{"derivatives", "intraday", "ticks"} {
		s, ok := byName[group]
		if !ok {
			t.Errorf("没认出调度组 %s", group)
			continue
		}
		if s.State != "skipped" {
			t.Errorf("%s 应为 skipped，实际 %q", group, s.State)
		}
		if !strings.Contains(s.Rows, "关闭") {
			t.Errorf("%s 应保留中文原因，实际 %q", group, s.Rows)
		}
	}
	t.Logf("认出 %d 个调度组，其中 skipped %d 个", len(steps), countState(steps, "skipped"))
}

// 正在跑的步骤与它的已用时必须能读出来 —— 这是"卡住没卡住"的唯一依据。
func TestParseStepsFindsRunningStepWithElapsed(t *testing.T) {
	steps := parseSteps(realLogLines)
	var ann *StepInfo
	for i := range steps {
		if steps[i].Name == "announcement_index" {
			ann = &steps[i]
			break
		}
	}
	if ann == nil {
		t.Fatal("没认出正在跑的 announcement_index")
	}
	if ann.State != "running" {
		t.Errorf("应为 running，实际 %q", ann.State)
	}
	// 同名取最后一次心跳 → 应是 15m06s（最新那条）
	if ann.Elapsed != "15m06s" {
		t.Errorf("已用时应取最新心跳 15m06s，实际 %q", ann.Elapsed)
	}
	t.Logf("正在跑：%s  已用 %s", ann.Name, ann.Elapsed)
}

// 时间戳前缀不能干扰识别（行首是 "2026-10-10 12:39:19,805"）。
func TestParseStepsIgnoresTimestampPrefix(t *testing.T) {
	withTS := parseSteps([]string{
		`2026-10-10 12:39:19,805 INFO cnequity.orchestrator.engine: Step announcement_index starting`,
	})
	withoutTS := parseSteps([]string{
		`Step announcement_index starting`,
	})
	if len(withTS) != 1 || len(withoutTS) != 1 {
		t.Fatalf("两种写法都应各解析出 1 条，实际 %d / %d", len(withTS), len(withoutTS))
	}
	if withTS[0].Name != withoutTS[0].Name || withTS[0].State != withoutTS[0].State {
		t.Errorf("带/不带时间戳的结果应一致：%+v vs %+v", withTS[0], withoutTS[0])
	}
}

// 不能把日志里的无关行当成步骤 —— 否则徽章会塞满噪音。
func TestParseStepsIgnoresNoise(t *testing.T) {
	noise := []string{
		`日志写到 D:\workspace\stockTool\CNEquity\data\cnequity\logs\cne-run-daily-20261010-123919.log`,
		`2026-10-10 12:39:19,571 INFO cnequity.orchestrator.engine: Skipping job daily:core: 2026-10-10 is not a trading day`,
		``,
		`   `,
	}
	steps := parseSteps(noise)
	for _, s := range steps {
		if strings.Contains(s.Name, "日志写到") || strings.Contains(s.Name, "Skipping") {
			t.Errorf("把无关行当成步骤了：%+v", s)
		}
	}
	t.Logf("噪音行解析出 %d 条（应当很少）", len(steps))
}

// 已经 success 的步骤不该被后面的 running 心跳覆盖回 running。
func TestParseStepsSuccessNotDowngraded(t *testing.T) {
	steps := parseSteps([]string{
		`Step compact success 12.3s`,
		`still working: compact 1m00s (no output for 1m00s)`,
	})
	if len(steps) != 1 {
		t.Fatalf("应为 1 条，实际 %d", len(steps))
	}
	if steps[0].State != "success" {
		t.Errorf("success 不该被 running 覆盖，实际 %q", steps[0].State)
	}
}

func countState(steps []StepInfo, state string) int {
	n := 0
	for _, s := range steps {
		if s.State == state {
			n++
		}
	}
	return n
}
