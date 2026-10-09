package service

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"freestock/internal/portfolio"
)

// **真实端到端回测**：回填 → 建组合 → 跑回测 → 读结果。
//
// 这条路径之前只有引擎的单测覆盖，"用真实数据从头跑到尾"一直没验证过。
// 它要打本机 free-stockdb（127.0.0.1:7899），耗时较长 ——
// 用短区间 + 小 limit 控制规模。
//
// 跑法：go test ./internal/service/ -run TestBacktestEndToEnd -v -timeout 30m
func TestBacktestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实回测（要打数据源、耗时较长）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// ① 先确认有评分能力：拿最新交易日
	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用，跳过：%v", err)
	}
	latest := int(universe.TradeDate)
	t.Logf("最新交易日 %d，股票 %d 只", latest, len(universe.Stocks))

	// ② 短区间回填
	//
	// ⚠ 规模刻意压到最小：回填 N 天会把每只股票的历史**重复拉 N 次**
	//（`ScoreStocks` 每天都要重新取一遍约 250 个交易日的窗口），
	// 所以「天数 × 只数」的成本是**乘性**的，必须两边都压小。
	//
	// ⚠ 日期算术不能在 YYYYMMDD 上直接减天数 ——
	// `20261008 - 12 = 20260996` 是非法日期，再"回落"会得到 20260100，
	// 区间就变成 9 个月了（我踩过）。用 time 正常算。
	latestTime, err := time.Parse("20060102", fmt.Sprintf("%d", latest))
	if err != nil {
		t.Fatalf("解析最新交易日失败：%v", err)
	}
	start, err := strconv.Atoi(latestTime.AddDate(0, 0, -16).Format("20060102"))
	if err != nil {
		t.Fatalf("算回填起点失败：%v", err)
	}
	const limit = 20
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil {
		t.Fatalf("历史回填失败：%v", err)
	}
	t.Logf("回填 %d~%d：交易日 %d，成功 %d，跳过 %d，耗时 %s（本步 %s）",
		start, latest, bf.Days, bf.Scored, len(bf.Skipped), bf.Elapsed,
		time.Since(began).Round(time.Second))
	if bf.Scored == 0 {
		t.Skipf("区间内没有可回填的交易日，跳过（跳过原因：%v）", bf.Skipped)
	}

	// ③ 建组合，并**直接从评分表里挑**几只当初始持仓
	//
	// 不用自动组合：熊市 + 小候选池（limit=20）下质量门槛很严，
	// 它可能一只都选不出来 —— 那是策略的正确行为，但会让回测没有持仓可动。
	// 这一步只是"验证回测通路"，所以直接拿评分最高的几只。
	store, err := svc.db()
	if err != nil {
		t.Fatalf("取组合库失败：%v", err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "回测端到端测试", Type: portfolio.TypeSemiAuto,
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(3),
		MaxStockCount:       intPtr(5),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	bounded := bf.Dates
	if len(bounded) < 2 {
		t.Skipf("回填出的交易日只有 %d 天，跑不了回测", len(bounded))
	}
	seed, err := store.LoadStrength(int64(bounded[len(bounded)-1]), portfolio.StrategyTrendGrowth, 3)
	if err != nil {
		t.Fatalf("读评分失败：%v", err)
	}
	if len(seed) == 0 {
		t.Skip("评分表里没有可用的股票，跳过")
	}
	for _, row := range seed {
		if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: row.Code, Name: row.Name,
			EntryPrice: row.Close, EntryDate: fmt.Sprintf("%d", bounded[0]),
			Layers: intPtr(1),
		}); err != nil {
			t.Logf("补持仓 %s 失败（跳过）：%v", row.Code, err)
		}
	}
	detail0, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("读组合失败：%v", err)
	}
	t.Logf("组合 %s：初始持仓 %d 只", created.ID, len(detail0.Positions))
	if len(detail0.Positions) == 0 {
		t.Skip("一只持仓都没补上，跳过回测")
	}

	// ④ 跑回测
	dates := bounded
	runBegan := time.Now()
	result, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID:    created.ID,
		StartDate:      dates[0],
		EndDate:        dates[len(dates)-1],
		InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	t.Logf("回测完成 %s：耗时 %s", result.RunID, time.Since(runBegan).Round(time.Second))
	t.Logf("指标：总收益 %.2f%% / 年化 %.2f%% / 回撤 %.2f%% / 夏普 %.2f / 成交 %d 笔 / 交易日 %d",
		result.Metrics.TotalReturn, result.Metrics.AnnualReturn,
		result.Metrics.MaxDrawdown, result.Metrics.SharpeRatio,
		result.Metrics.TotalTrades, result.Metrics.TradingDays)
	if result.Metrics.AnnualReturnWarning != "" {
		t.Logf("提示：%s", result.Metrics.AnnualReturnWarning)
	}

	// ⑤ 读回来，验证三张表都落了数据
	payload, err := svc.GetBacktestPayload(result.RunID)
	if err != nil {
		t.Fatalf("读回测结果失败：%v", err)
	}
	if len(payload.Daily) != result.Metrics.TradingDays {
		t.Errorf("净值曲线应有 %d 天，实际 %d", result.Metrics.TradingDays, len(payload.Daily))
	}
	if len(payload.Daily) == 0 {
		t.Fatal("净值曲线为空 —— 回测根本没跑起来")
	}

	// 净值曲线的首末值必须自洽
	first, last := payload.Daily[0], payload.Daily[len(payload.Daily)-1]
	if first.TotalValue <= 0 {
		t.Errorf("首日净值应 > 0，实际 %.2f", first.TotalValue)
	}
	wantReturn := (last.TotalValue/100000 - 1) * 100
	if diff := wantReturn - last.CumulativeReturn; diff > 0.01 || diff < -0.01 {
		t.Errorf("末日累计收益应为 %.4f%%，实际 %.4f%%", wantReturn, last.CumulativeReturn)
	}
	t.Logf("净值：%s %.2f → %s %.2f（累计 %.2f%%）；成交明细 %d 笔",
		first.TradeDate, first.TotalValue, last.TradeDate, last.TotalValue,
		last.CumulativeReturn, len(payload.Trades))

	// 成交明细的买卖必须成对出现在合理位置
	for _, trade := range payload.Trades {
		if trade.TradeDate < payload.Run.StartDate || trade.TradeDate > payload.Run.EndDate {
			t.Errorf("成交日期 %s 落在回测区间之外", trade.TradeDate)
		}
		if trade.Action != "buy" && trade.Action != "sell" {
			t.Errorf("未知成交方向 %q", trade.Action)
		}
		if trade.Shares <= 0 {
			t.Errorf("%s %s 股数应 > 0，实际 %d", trade.TradeDate, trade.Code, trade.Shares)
		}
	}
}

// 自动建组合：预演与创建各跑一次，确认两者选出的持仓一致。
func TestAutoPortfolioPreviewMatchesCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	req := AutoPortfolioRequest{
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(3),
		MaxStockCount:       intPtr(5),
		TotalLayers:         10,
	}
	preview, err := svc.PreviewAutoPortfolio(req)
	if err != nil {
		t.Skipf("预演失败（可能还没刷新评分）：%v", err)
	}
	if preview.SelectedCount == 0 {
		t.Skip("预演没选出候选")
	}
	t.Logf("预演：%d 只，组合评分 %.2f（%s）",
		preview.SelectedCount, preview.Snapshot.PortfolioScore, preview.Snapshot.HealthLabel)

	codes := make([]string, 0, len(preview.SelectedPositions))
	for _, row := range preview.SelectedPositions {
		codes = append(codes, row.Code)
	}

	created, err := svc.CreateAutoPortfolio(req)
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	createdCodes := make([]string, 0, len(created.SelectedPositions))
	for _, row := range created.SelectedPositions {
		createdCodes = append(createdCodes, row.Code)
	}
	if len(codes) != len(createdCodes) {
		t.Fatalf("预演与创建的持仓数不同：%d vs %d", len(codes), len(createdCodes))
	}
	for i := range codes {
		if codes[i] != createdCodes[i] {
			t.Errorf("第 %d 只不同：预演 %s，创建 %s", i, codes[i], createdCodes[i])
		}
	}
	// 落库的持仓数应与预演一致
	detail, err := svc.GetPortfolioDetail(created.PortfolioID)
	if err != nil {
		t.Fatalf("读组合详情失败：%v", err)
	}
	if len(detail.Positions) != len(createdCodes) {
		t.Errorf("落库持仓 %d 只，预期 %d 只", len(detail.Positions), len(createdCodes))
	}
	t.Logf("组合 %s 落库 %d 只持仓，与预演一致", created.PortfolioID, len(detail.Positions))
}
