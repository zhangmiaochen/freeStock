package service

import (
	"context"
	"os"
	"sort"
	"testing"

	"freestock/internal/config"
	"freestock/internal/score"
)

// ⚠ **等价性硬验证**：`DailyBars`（新）必须与
// `barsFromQuotes(DailyHistory(...))`（旧）**逐字段完全一致**。
//
// 为什么必须做：评分里 `TurnRankPct = RankPct(全市场换手率, 本股换手率)`
// 用到了换手率，而 `score.Bar` 原本没有这个字段 —— 一旦漏掉，
// 换手率全变 0，评分结果就会**静默变化**。这正是用户反复强调不能发生的事。
//
// 只在本机数据湖可用时跑。
func TestDailyBarsMatchesDailyHistory(t *testing.T) {
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	if _, err := svc.stock.SetDataSource("cnlake", root); err != nil {
		t.Skipf("切到数据湖失败：%v", err)
	}
	provider, source := svc.stock.snapshot()
	if source.Kind != config.KindCnLake {
		t.Skipf("数据源不是数据湖（%s）", source.Kind)
	}

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil {
		t.Skipf("拿不到股票列表：%v", err)
	}
	codes := make([]string, 0, 512)
	for _, item := range universe.Stocks {
		if item.HasQuote {
			codes = append(codes, item.Code)
		}
	}
	sort.Strings(codes)
	// 取一只大盘股 + 若干随机个股，覆盖面够
	sample := []string{"600519", "000001", "300750"}
	for _, code := range codes {
		if len(sample) >= 25 {
			break
		}
		dup := false
		for _, existing := range sample {
			if existing == code {
				dup = true
				break
			}
		}
		if !dup {
			sample = append(sample, code)
		}
	}

	latest := int(universe.TradeDate)
	start, end := shiftDays(t, latest, -320), latest
	ctx := context.Background()

	checked, withTurnover := 0, 0
	for _, code := range sample {
		oldQuotes, err := provider.DailyHistory(ctx, code, start, end)
		if err != nil {
			t.Fatalf("%s DailyHistory 失败：%v", code, err)
		}
		oldBars := barsFromQuotes(oldQuotes)

		newBars, err := provider.DailyBars(ctx, code, start, end)
		if err != nil {
			t.Fatalf("%s DailyBars 失败：%v", code, err)
		}

		if len(oldBars) != len(newBars) {
			t.Errorf("%s 行数不一致：旧 %d，新 %d", code, len(oldBars), len(newBars))
			continue
		}
		for i := range oldBars {
			o, n := oldBars[i], newBars[i]
			if o.Date != n.Date || o.Open != n.Open || o.High != n.High ||
				o.Low != n.Low || o.Close != n.Close ||
				o.Volume != n.Volume || o.Amount != n.Amount || o.Turnover != n.Turnover {
				t.Errorf("%s 第 %d 根不一致：\n  旧 %+v\n  新 %+v", code, i, o, n)
				break
			}
			if n.Turnover > 0 {
				withTurnover++
			}
		}
		checked++

		// 指标也必须一致（评分真正依赖的就是它）
		oldInd, okOld := score.ComputeIndicators(oldBars)
		newInd, okNew := score.ComputeIndicators(newBars)
		if okOld != okNew {
			t.Errorf("%s ComputeIndicators 可用性不一致：旧 %v，新 %v", code, okOld, okNew)
			continue
		}
		if okOld && oldInd != newInd {
			t.Errorf("%s 指标不一致：\n  旧 %+v\n  新 %+v", code, oldInd, newInd)
		}
	}

	if checked == 0 {
		t.Fatal("一只都没比对上")
	}
	t.Logf("逐字段比对通过：%d 只（窗口 %d~%d），其中 %d 根有非零换手率",
		checked, start, end, withTurnover)
	if withTurnover == 0 {
		t.Error("**换手率全是 0** —— 说明 Turnover 没算出来，评分结果会变")
	}
}
