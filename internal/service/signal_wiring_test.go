package service

import (
	"path/filepath"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **组合计划真的把 `signal_history` 的信号吃进去了。**
//
// 这是"信号被消费"的服务层证据：`buildCandidatePoolFrom` 在范围过滤之后、
// 广度统计之前调用 attach（对应 portfolio_service.py:2469），
// 于是候选行上会带出 `active_signal_*` / `signal_age`。
//
// 没有 stockService 时日历会退化到"行日期去重排序"，仍然能完成匹配 ——
// 这一点也顺带验证了退路生效（生产环境有日历，走的是交易日历分支）。
func TestPlanConsumesSignalHistory(t *testing.T) {
	store, err := portfolio.Open(filepath.Join(t.TempDir(), "pf.db"))
	if err != nil {
		t.Fatalf("打开组合库失败：%v", err)
	}
	defer store.Close()

	const tradeDate int64 = 20261009
	strategyID := score.StrategyTrendGrowth

	all := []portfolio.StrengthRow{
		scopeRow("600519", "sh-main", "白酒", 80),
		scopeRow("601318", "sh-main", "保险", 70),
		scopeRow("300750", "chinext", "电池", 90),
	}
	for i := range all {
		all[i].TradeDate = tradeDate
	}
	if err := store.SaveStrength(tradeDate, strategyID, all); err != nil {
		t.Fatalf("写入评分失败：%v", err)
	}

	// 只给 600519 一条 buy 信号；另外两只没有信号，应当保持默认值。
	//
	// ⚠ `ts_code` 用**原项目的 tushare 格式**（带 .SH 后缀）：信号表存的就是这个格式
	//（与 Python 侧可直接对照），join 时由 `LoadSignalCandidates` 归一化成裸码。
	if err := store.SaveSignalHistory([]portfolio.SignalHistoryRow{{
		TradeDate: tradeDate, Code: "600519.SH", Name: "600519名",
		ModelID: "trend_pullback", ModelName: "趋势缩量回踩",
		MarketState: "bull", MarketStateLabel: "牛市",
		SignalType: "buy", SignalLevel: "confirmed", SignalLevelLabel: "确认",
		Score: 88.0, RiskLevel: "low", CreatedAt: "t", UpdatedAt: "t",
	}}); err != nil {
		t.Fatalf("写入信号失败：%v", err)
	}

	svc := &PortfolioService{}
	item := portfolio.Portfolio{
		ID: "pf_test", StrategyID: strategyID,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType:      portfolio.ScopeAll,
		ScopeValueJSON: `{"markets":["main"]}`,
	}
	cfg := score.BuildStrategyConfig(strategyID, score.StateRange, 5, 10, 3, "all")

	rows, universeScores := svc.buildCandidatePoolFrom(store, all, item, cfg, nil)
	if len(universeScores) == 0 {
		t.Fatal("广度分数不应为空")
	}

	byCode := map[string]score.Row{}
	for _, row := range rows {
		byCode[row.Code] = row
	}
	withSignal, ok := byCode["600519"]
	if !ok {
		t.Fatalf("600519 应当留在候选池里（实际候选 %d 只：%v）", len(rows), keysOf(byCode))
	}
	if withSignal.SignalType != "buy" {
		t.Errorf("600519 的 active_signal_type 应为 buy，实际 %q", withSignal.SignalType)
	}
	if withSignal.SignalScore != 88.0 {
		t.Errorf("active_signal_score 应为 88，实际 %v", withSignal.SignalScore)
	}
	if withSignal.SignalAge != 0 {
		t.Errorf("同日信号的 signal_age 应为 0，实际 %d", withSignal.SignalAge)
	}
	if withSignal.SignalModelID != "trend_pullback" {
		t.Errorf("active_signal_model_id 未回填：%q", withSignal.SignalModelID)
	}
	if withSignal.SignalLevel != "confirmed" || withSignal.SignalLevelLabel != "确认" {
		t.Errorf("active_signal_level 未回填：%q / %q", withSignal.SignalLevel, withSignal.SignalLevelLabel)
	}
	if withSignal.SignalTradeDate != tradeDate {
		t.Errorf("signal_trade_date 应为 %d，实际 %d", tradeDate, withSignal.SignalTradeDate)
	}

	// 没有信号的票必须保持"无信号"的默认值（age=999），否则会被误判成"有新鲜信号"。
	for code, row := range byCode {
		if code == "600519" {
			continue
		}
		if row.SignalType != "" || row.SignalAge != 999 || row.SignalScore != 0 {
			t.Errorf("%s 没有信号，应保持默认值，实际 type=%q age=%d score=%v",
				code, row.SignalType, row.SignalAge, row.SignalScore)
		}
	}
}

func keysOf(rows map[string]score.Row) []string {
	out := make([]string, 0, len(rows))
	for key := range rows {
		out = append(out, key)
	}
	return out
}
