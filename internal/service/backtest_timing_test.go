package service

import (
	"os"
	"strconv"
	"testing"
	"time"

	"freestock/internal/portfolio"
)

// 第 4 项：回测到底慢在哪一段。
//
// 候选池只有几十只时看不出问题（一次批量就取完），所以要放到**几百只**的规模。
// 用 FREESTOCK_TIMING=1 开启（回填 + 回测合计约 1~2 分钟）。
func TestBacktestPhaseTiming(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("计时（约 1~2 分钟）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用：%v", err)
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -12)

	// ① 回填
	//
	// 允许用环境变量放大规模 —— 用来验证"广度闸门需要接近全市场的样本"
	// 这一判断（广度判据是"评分≥70 的只数 ≥20"，样本太小就永远凑不够）。
	limit := 300
	if v := os.Getenv("FREESTOCK_BACKFILL_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	backfillElapsed := time.Since(began)
	if bf.Scored == 0 {
		t.Skipf("回填没出数据：%v", bf.Skipped)
	}
	t.Logf("① 回填 %d 天 × %d 只：%s（%.0f ms/天·只）",
		bf.Days, limit, backfillElapsed.Round(time.Millisecond),
		float64(backfillElapsed.Milliseconds())/float64(bf.Days*limit))

	// ② 组合 + 持仓
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "阶段计时", Type: portfolio.TypeSemiAuto,
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(5),
		MaxStockCount:       intPtr(8),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	dates := bf.Dates
	seed, err := store.LoadStrength(int64(dates[len(dates)-1]), portfolio.StrategyTrendGrowth, 5)
	if err != nil || len(seed) == 0 {
		t.Skipf("评分表没有可用股票：%v", err)
	}
	for _, row := range seed {
		_, _ = svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: row.Code, Name: row.Name,
			EntryPrice: row.Close, EntryDate: "20260101", Layers: intPtr(1),
		})
	}

	// ③ 回测（阶段日志由 CreateBacktest 自己打）
	began = time.Now()
	result, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID: created.ID, StartDate: dates[0], EndDate: dates[len(dates)-1],
		InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	elapsed := time.Since(began)

	// ④ 读回结果（这一步也走 DB，顺带计时）
	began = time.Now()
	payload, err := svc.GetBacktestPayload(result.RunID)
	if err != nil {
		t.Fatalf("读回测结果失败：%v", err)
	}
	readElapsed := time.Since(began)

	t.Logf("② 回测（%d 天）：%s", len(dates), elapsed.Round(time.Millisecond))
	t.Logf("③ 读回结果：%s（净值 %d 天，成交 %d 笔）",
		readElapsed.Round(time.Millisecond), len(payload.Daily), len(payload.Trades))
	t.Logf("指标：总收益 %.2f%%，成交 %d 笔", result.Metrics.TotalReturn, result.Metrics.TotalTrades)

	// ---- 以下是**回归断言**，不只是计时 ----
	//
	// 用户报的"回测一笔成交都没有、净值一直等于初始资金"，
	// 根因是**回填范围远小于全市场**导致广度闸门删光了买入信号。
	// 所以这里在**大范围回填**（足以让广度闸门放行）的前提下，
	// 明确断言"必须成交、净值必须会动"。
	if limit < 100 {
		t.Skipf("本次只回填了 %d 只（<100），广度闸门不会启用，不适合断言成交", limit)
	}

	// 广度闸门放行与否不用在这里断言 —— **"有成交"本身就是它放行的证明**：
	// 闸门一旦判定"广度不足"，会把所有 add/replace_in 行删掉，绝不可能成交。
	if result.Metrics.TotalTrades == 0 {
		t.Errorf("回填了 %d 只却仍然零成交 —— 广度仍被误判，或还有别的闸门在拦", limit)
	}
	if len(payload.Trades) != result.Metrics.TotalTrades {
		t.Errorf("成交笔数不一致：payload %d，metrics %d", len(payload.Trades), result.Metrics.TotalTrades)
	}

	// 净值曲线必须自洽：净值 = 现金 + 持仓市值
	for _, d := range payload.Daily {
		if diff := d.TotalValue - (d.Cash + d.PositionValue); diff > 0.01 || diff < -0.01 {
			t.Errorf("%s 净值 %.2f ≠ 现金 %.2f + 市值 %.2f", d.TradeDate, d.TotalValue, d.Cash, d.PositionValue)
		}
	}
	// 累计收益必须与净值对得上
	last := payload.Daily[len(payload.Daily)-1]
	initial := 100000.0
	wantReturn := (last.TotalValue/initial - 1) * 100
	if diff := wantReturn - last.CumulativeReturn; diff > 0.01 || diff < -0.01 {
		t.Errorf("末日累计收益应为 %.4f%%，实际 %.4f%%", wantReturn, last.CumulativeReturn)
	}
	// 成交必须合法
	for _, trade := range payload.Trades {
		if trade.TradeDate < payload.Run.StartDate || trade.TradeDate > payload.Run.EndDate {
			t.Errorf("成交日期 %s 落在区间外", trade.TradeDate)
		}
		if trade.Action != "buy" && trade.Action != "sell" {
			t.Errorf("未知方向 %q", trade.Action)
		}
		if trade.Shares <= 0 {
			t.Errorf("%s %s 股数应 > 0，实际 %d", trade.TradeDate, trade.Code, trade.Shares)
		}
	}
	t.Logf("④ 断言通过：广度放行、成交 %d 笔、净值自洽（末值 %.2f，累计 %.2f%%）",
		len(payload.Trades), last.TotalValue, last.CumulativeReturn)
}
