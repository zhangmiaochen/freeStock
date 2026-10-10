package service

import (
	"context"
	"os"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// **复现组合回测页回填慢的根因**：历史回填是**逐天往前推**的。
//
// 第 1 天要 [D0-320, D0]，第 2 天要 [D0-319, D1]，第 3 天 [D0-318, D2] ……
// 每天请求的区间都**不一样**。如果批量缓存是"不覆盖就整段重灌 + 替换"，
// 就会变成**每天都把 320 天窗口重灌一遍** —— 每天 2 秒，30 天就 60 秒。
//
// 之前的测试用的是**固定窗口**，所以完全没暴露这个问题。
func TestBackfillShiftingWindowDoesNotRefill(t *testing.T) {
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	lake := cnlake.Open(root)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	provider := &lakeProvider{lake: lake}

	dates, err := lake.BarPartitionDates()
	if err != nil || len(dates) < 60 {
		t.Skipf("分区不足：%v", err)
	}

	// 取一批代码
	tail := dates[len(dates)-20:]
	rows, err := lake.DailyBarsInRange(nil, tail[0], tail[len(tail)-1])
	if err != nil {
		t.Fatalf("读区间失败：%v", err)
	}
	seen := map[string]bool{}
	codes := []string{}
	for _, row := range rows {
		code := cnlake.BareSymbol(row.Symbol)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
		if len(codes) >= 40 {
			break
		}
	}
	if len(codes) < 10 {
		t.Skipf("代码太少（%d）", len(codes))
	}

	// 逐天推移，每天对所有代码要一个 320 天的尾窗
	const windowDays = 320
	days := dates[len(dates)-10:] // 模拟回填 10 个交易日
	t.Logf("样本 %d 只 × %d 个交易日（每天推移 1 天，窗口 %d 天）", len(codes), len(days), windowDays)

	refills := 0
	began := time.Now()
	for _, day := range days {
		dateIdx := -1
		for i, d := range dates {
			if d == day {
				dateIdx = i
				break
			}
		}
		startIdx := dateIdx - windowDays
		if startIdx < 0 {
			startIdx = 0
		}
		start, end := int(dates[startIdx]), int(day)

		provider.bulkMu.Lock()
		before := provider.bulkTo
		provider.bulkMu.Unlock()

		for _, code := range codes {
			if _, err := provider.DailyHistory(context.Background(), code, start, end); err != nil {
				t.Fatalf("%s @ %d 失败：%v", code, day, err)
			}
		}

		provider.bulkMu.Lock()
		after := provider.bulkTo
		provider.bulkMu.Unlock()
		if after != before {
			refills++
		}
	}
	total := time.Since(began)

	t.Logf("① 共 %d 个交易日 × %d 只：%s", len(days), len(codes), total.Round(time.Millisecond))
	t.Logf("② 缓存区间被推进了 %d 次（最多 %d 次 —— 每往后一天只该补 1 个日分区）",
		refills, len(days))
	t.Logf("   每只每天平均 %s", (total / time.Duration(len(days)*len(codes))).Round(time.Microsecond))

	// ★ 首要断言：**不能每天都整段重灌**
	if refills > len(days)+1 {
		t.Errorf("缓存被推进 %d 次（>%d）—— 说明又变成了「每天重灌一遍」", refills, len(days)+1)
	}
	// 次要断言：总耗时不该退化到「每天 2 秒」
	if total > 15*time.Second {
		t.Errorf("%d 天 × %d 只用了 %s —— 按天重灌的话就是这个量级",
			len(days), len(codes), total.Round(time.Second))
	}
}
