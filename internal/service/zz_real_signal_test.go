package service

import (
	"os"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **真实数据端到端：信号被生成、并且被消费。**
//
// 这是整个量价移植的收口验证 —— 前面所有分层比对（特征/度量/判定/回测/attach）
// 用的都是构造数据，这里走一遍真实链路：
//
//	真实日线 → RefreshSignals → signal_history
//	→ LoadSignalCandidates → AttachSignalCandidates → score.Row.active_signal_*
//	→ signal_bonus / 候选优先级 / 熊市新鲜度闸门
//
// 需要数据源与已物化的市场状态，所以用环境变量开启：
//
//	FREESTOCK_SIGNAL_E2E=1 go test ./internal/service/ -run TestRealSignalEndToEnd -v -count=1
//
// 可选参数：FREESTOCK_SIGNAL_CODES（默认 40）、FREESTOCK_SIGNAL_DATES（默认 5）
func TestRealSignalEndToEnd(t *testing.T) {
	if os.Getenv("FREESTOCK_SIGNAL_E2E") != "1" {
		t.Skip("真实信号端到端（要打数据源）：设 FREESTOCK_SIGNAL_E2E=1 才跑")
	}
	codeCount := abEnvInt("FREESTOCK_SIGNAL_CODES", 40)

	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	store := mustStore(t, svc)

	codes, _, err := svc.scoringUniverse(store, codeCount)
	if err != nil || len(codes) == 0 {
		t.Skipf("拿不到评分范围：%v", err)
	}
	latest, err := provider.LatestTradeDate(t.Context())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}

	// ---- ① 生成 ----
	result, genErr := svc.RefreshSignals(t.Context(), codes, 0, latest, SignalGenerateOptions{})
	if genErr != nil {
		t.Fatalf("信号生成失败：%v", genErr)
	}
	t.Logf("生成：股票 %d 只（跳过 %d）、信号 %d 条、市场状态天数 %d、耗时 %s",
		result.Codes, result.Skipped, result.Rows, result.MarketStateDays, result.Elapsed)
	t.Logf("按模型：%v", result.PerModel)

	// 没有市场状态缓存时，回测会退回逐日"个股上下文状态"。这与 Python 侧的
	// 状态口径不同（原版一定传 `build_market_state_cache` 的结果），所以信号
	// 数量会有偏差 —— 这是缺前置数据，不是移植缺陷，单独报出来。
	if result.MarketStateDays == 0 {
		t.Logf("注意：库内没有 market_state 数据，本次走的是逐日个股上下文状态，" +
			"信号数与 Python 侧口径会有偏差；建议先跑一次区间评分再复测")
	}
	stored, err := store.SignalHistoryCount()
	if err != nil {
		t.Fatalf("统计信号失败：%v", err)
	}
	if result.Rows == 0 {
		if result.MarketStateDays == 0 {
			t.Skip("没有市场状态且没生成信号 —— 缺前置数据，无法验证")
		}
		t.Fatalf("有 %d 天市场状态却没生成任何信号 —— 生产链可能断了", result.MarketStateDays)
	}
	if stored == 0 {
		t.Fatalf("生成报告写了 %d 条，但表里查不到", result.Rows)
	}
	t.Logf("signal_history 累计 %d 条", stored)

	// ---- ② 消费 ----
	// 取最近若干个交易日，构造"有信号"的评分行再走一遍 attach，
	// 断言信号真的贴到了行上。
	signalDates, err := store.SignalHistoryDates(0)
	if err != nil {
		t.Fatalf("取信号日期失败：%v", err)
	}
	if len(signalDates) == 0 {
		t.Skip("表里还没有信号日期")
	}
	// 找一个**既有信号又有评分**的交易日。评分表可能只回填了部分区间，
	// 所以逐日试；**一天都没有就现场补算一天** —— 否则这个测试会长期
	// 停在"缺前置数据"上，等于没验证消费链路。
	var all []portfolio.StrengthRow
	var maxDate int64
	for _, date := range signalDates {
		loaded, loadErr := store.LoadStrength(date, "", 0)
		if loadErr == nil && len(loaded) > 0 {
			all, maxDate = loaded, date
			break
		}
	}
	if len(all) == 0 {
		// 用信号里出现过的代码，在最近一个有信号的交易日现场算一次评分并落库
		if len(codes) > codeCount {
			codes = codes[:codeCount]
		}
		scoring := NewScoringService(svc.stock)
		scored, scoreErr := scoring.ScoreStocks(codes, int(signalDates[0]), 0, score.StrategyTrendGrowth)
		if scoreErr != nil || scored == nil || len(scored.Rows) == 0 {
			t.Skipf("信号日期（共 %d 天）里没有评分，现场补算也失败：%v", len(signalDates), scoreErr)
		}
		if err := svc.saveScoringResult(store, scored, score.StrategyTrendGrowth, true); err != nil {
			t.Skipf("现场补算评分落库失败：%v", err)
		}
		all, maxDate = nil, int64(scored.TradeDate)
		all, err = store.LoadStrength(maxDate, score.StrategyTrendGrowth, 0)
		if err != nil || len(all) == 0 {
			t.Skipf("现场补算后仍读不到评分：%v", err)
		}
		t.Logf("现场补算了 %d 只的评分（交易日 %d）以验证消费链路", len(all), maxDate)
	}
	t.Logf("消费验证用交易日 %d（评分 %d 行）", maxDate, len(all))

	rows := make([]score.Row, 0, len(all))
	dates := make([]int64, 0, len(all))
	for _, item := range all {
		rows = append(rows, strengthToRow(item))
		dates = append(dates, item.TradeDate)
	}
	attached := svc.attachSignalsToRows(store, rows, dates)

	fresh, ages := 0, map[int]int{}
	for _, row := range attached {
		if row.SignalType == "buy" || row.SignalType == "candidate_buy" {
			fresh++
			ages[row.SignalAge]++
		}
	}
	t.Logf("消费：%d 行里有 %d 行贴上了信号（signal_age 分布 %v）", len(attached), fresh, ages)

	candidates, _ := svc.buildCandidatePoolFrom(store, all, portfolio.Portfolio{
		ID: "pf_e2e", StrategyID: score.StrategyTrendGrowth,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType: portfolio.ScopeAll, ScopeValueJSON: `{"markets":["main","gem","star"]}`,
	}, score.BuildStrategyConfig(score.StrategyTrendGrowth, score.StateBull, 5, 10, 3, "all"), nil)
	withSignal := 0
	for _, row := range candidates {
		if row.SignalType != "" {
			withSignal++
		}
	}
	t.Logf("候选池 %d 只，其中 %d 只带信号", len(candidates), withSignal)
	if fresh == 0 {
		t.Fatalf("表里有信号（%d 条）却一行都没贴上 —— attach 链路可能断了", stored)
	}
}
