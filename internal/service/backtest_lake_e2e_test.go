package service

import (
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **不依赖 stockdb 的端到端回测**：用本地数据湖当数据源。
//
// 为什么能这么测：
//   - 广度闸门在 `universe_count < 100` 时是 **disabled（直接放行）**，
//     所以用小股票池（80 只）就能绕开"样本不够导致不开仓"的问题 ——
//     这一步是原版行为，不是为测试开的后门。
//   - 湖的日线读取很慢（约 1.75s/只），但区间缓存让它**每只只取一次**，
//     80 只约 2 分钟，可以接受。
//
// 目的：验证"改完这几处之后，回测**真的会成交**、净值曲线合理"。
// 这是之前几轮一直缺的那一步 —— 之前受限于 stockdb 损坏。
func TestBacktestEndToEndOnLake(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要读数据湖，约 2~4 分钟）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") || !lake.HasDataset("instruments") {
		t.Skip("数据湖缺少 daily_bars / instruments，跳过")
	}
	if _, err := svc.stock.SetDataSource("cnlake", lake.Root()); err != nil {
		t.Skipf("切到数据湖失败：%v", err)
	}
	t.Logf("数据源：本地数据湖 %s", lake.Root())

	universe, err := svc.stock.GetStockUniverse(true)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据湖拿不到股票列表：%v", err)
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -10)

	// ★ 80 只 —— **故意低于广度闸门的 100 只启用线**，
	// 这样闸门不启用、不拦买入（原版同样行为）
	const limit = 80
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	t.Logf("① 回填：%d 个交易日，成功 %d 天，耗时 %s",
		bf.Days, bf.Scored, time.Since(began).Round(time.Second))
	if bf.Scored == 0 {
		t.Skipf("回填没产出数据：%v", bf.Skipped)
	}
	dates := bf.Dates
	if len(dates) < 2 {
		t.Skipf("交易日不足（%d）", len(dates))
	}

	// ② 建组合（空仓 —— 与原版回测一致，从纯现金开始）
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "湖上回测", Type: portfolio.TypeSemiAuto,
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(5),
		MaxStockCount:       intPtr(8),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	rows, err := store.LoadStrength(int64(dates[len(dates)-1]), score.StrategyTrendGrowth, 0)
	if err != nil || len(rows) == 0 {
		t.Skipf("评分表没有数据：%v", err)
	}
	t.Logf("② 组合 %s（空仓，从纯现金开始，与原版一致）", created.ID)

	// ③ 回测
	began = time.Now()
	result, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID: created.ID, StartDate: dates[0], EndDate: dates[len(dates)-1],
		InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	t.Logf("③ 回测完成：%s，%d 个交易日", time.Since(began).Round(time.Second), result.Metrics.TradingDays)

	payload, err := svc.GetBacktestPayload(result.RunID)
	if err != nil {
		t.Fatalf("读回测结果失败：%v", err)
	}
	m := result.Metrics
	t.Logf("指标：总收益 %.2f%% / 年化 %.2f%% / 回撤 %.2f%% / 夏普 %.2f / 成交 %d 笔",
		m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio, m.TotalTrades)
	t.Logf("相对基准：超额 %.2f%% / Beta %.2f / Alpha %.2f / 信息比率 %.2f",
		m.ExcessReturn, m.Beta, m.Alpha, m.InformationRatio)

	// ---- 关键断言：这次必须有成交 ----
	if len(payload.Daily) == 0 {
		t.Fatal("净值曲线为空")
	}
	if m.TotalTrades == 0 {
		t.Errorf("**仍然零成交** —— 修正没生效或还有别的闸门在拦（区间 %d~%d，%d 天）",
			dates[0], dates[len(dates)-1], len(dates))
		for _, d := range payload.Daily {
			t.Logf("  %s 净值 %.2f 现金 %.2f 市值 %.2f 持仓 %d",
				d.TradeDate, d.TotalValue, d.Cash, d.PositionValue, d.PositionCount)
		}
		return
	}

	// 成交必须落在区间内、方向合法、股数为正
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
	// 净值曲线的首末值必须自洽
	first := payload.Daily[0]
	last := payload.Daily[len(payload.Daily)-1]
	if first.TotalValue <= 0 {
		t.Errorf("首日净值应 > 0，实际 %.2f", first.TotalValue)
	}
	wantReturn := (last.TotalValue/100000 - 1) * 100
	if diff := wantReturn - last.CumulativeReturn; diff > 0.01 || diff < -0.01 {
		t.Errorf("末日累计收益应为 %.4f%%，实际 %.4f%%", wantReturn, last.CumulativeReturn)
	}
	// 净值 = 现金 + 持仓市值
	for _, d := range payload.Daily {
		if diff := d.TotalValue - (d.Cash + d.PositionValue); diff > 0.01 || diff < -0.01 {
			t.Errorf("%s 净值 %.2f ≠ 现金 %.2f + 市值 %.2f", d.TradeDate, d.TotalValue, d.Cash, d.PositionValue)
		}
	}
	t.Logf("④ 成交 %d 笔；净值 %s %.2f → %s %.2f（累计 %.2f%%）",
		len(payload.Trades), first.TradeDate, first.TotalValue,
		last.TradeDate, last.TotalValue, last.CumulativeReturn)
	for i, trade := range payload.Trades {
		if i >= 8 {
			t.Logf("  ... 共 %d 笔", len(payload.Trades))
			break
		}
		t.Logf("  %s %s %s %d 股 @ %.2f 佣金 %.2f",
			trade.TradeDate, trade.Action, trade.Code, trade.Shares, trade.Price, trade.Commission)
	}
}
