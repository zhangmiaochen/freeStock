package chanlunassist_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"freestock/internal/backtest"
	"freestock/internal/chanlun"
	"freestock/internal/chanlunassist"
	"freestock/internal/score"
)

// 这段测试用**受控合成行情**验证「叠加层确实改变了净值与回撤」，
// 以及「没有信号时结果与基线逐字段一致」。
//
// 它不是业绩证据：价格路径是按 test case 设计的。真实数据结论见
// docs/缠论辅助组合回测_结果.md 与 internal/service 下的真实数据实验。

type scriptedProvider struct {
	byDate map[int]chanlunassist.Snapshot
}

func (p scriptedProvider) Signals(code string, asOf int) chanlunassist.Snapshot {
	if snapshot, ok := p.byDate[asOf]; ok {
		return snapshot
	}
	return chanlunassist.Snapshot{}
}

func signal(t chanlun.SignalType, date int) *chanlunassist.Signal {
	return &chanlunassist.Signal{
		Type: t, TypeCN: chanlun.SignalTypeCN(t), State: chanlun.StateConfirmed,
		Score: 80, ConfirmDate: date, ConfirmIndex: 0, Price: 10, AgeBars: 0,
	}
}

// 造一段「先跌、再涨、最后崩」的日线，返回 bars 与交易日列表。
func crashPathBars(code string) ([]backtest.PriceBar, []string) {
	legs := []struct {
		days int
		from float64
		to   float64
	}{
		{10, 10.0, 9.0}, // 回调
		{29, 9.0, 15.0}, // 主升
		{21, 15.0, 7.0}, // 崩塌
	}
	var closes []float64
	closes = append(closes, 10.0)
	for _, leg := range legs {
		for i := 1; i <= leg.days; i++ {
			closes = append(closes, leg.from+(leg.to-leg.from)*float64(i)/float64(leg.days))
		}
	}
	bars := make([]backtest.PriceBar, 0, len(closes))
	dates := make([]string, 0, len(closes))
	prev := closes[0]
	for i, closePrice := range closes {
		date := 20260101 + i
		open := prev
		if i == 0 {
			open = closePrice
		}
		bars = append(bars, backtest.PriceBar{
			Date: itoa(date), Code: code, Name: code,
			Open: round2(open), Close: round2(closePrice),
		})
		dates = append(dates, itoa(date))
		prev = closePrice
	}
	return bars, dates
}

func itoa(v int) string {
	// 交易日只用于排序与相等比较，不必是严格日历日期。
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// 基线计划：第一天建仓 1 层，之后一直持有（不做任何主动减仓）。
func holdPlan() backtest.PlanBuilder {
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		if len(held) == 0 {
			if day.Date != "20260101" {
				return score.RotationPlan{}
			}
			return score.RotationPlan{Rows: []score.RotationRow{{
				Code: "600000", Name: "600000", Action: score.ActionAdd,
				ActionLabel: score.ActionLabel(score.ActionAdd),
				Score:       80, RankNo: 1, TargetLayers: 1, LayersDelta: 1,
				Reason: "新增建仓",
			}}}
		}
		rows := make([]score.RotationRow, 0, len(held))
		for _, row := range held {
			rows = append(rows, score.RotationRow{
				Code: row.Code, Name: row.Name, Action: score.ActionKeep,
				ActionLabel: score.ActionLabel(score.ActionKeep),
				Score:       row.TotalScore, SourceType: "holding",
				CurrentLayers: row.PositionLayers, TargetLayers: row.PositionLayers,
				Reason: "继续持有",
			})
		}
		return score.RotationPlan{Rows: rows}
	}
}

func runEngine(t *testing.T, buildPlan backtest.PlanBuilder) *backtest.Result {
	t.Helper()
	bars, dates := crashPathBars("600000")
	days := make([]backtest.StrengthDay, 0, len(dates))
	for _, date := range dates {
		days = append(days, backtest.StrengthDay{
			Date: date, MarketState: score.StateRange,
			Rows: []score.Row{{
				Code: "600000", Name: "600000", Market: "sh-main",
				TradeDate: mustAtoi(date), TotalScore: 80, Close: 10, Open: 10,
				Ma5: 10, Ma20: 10, Ma60: 10, Amount: 1e9,
			}},
		})
	}
	cfg := backtest.Config{
		StartDate: dates[0], EndDate: dates[len(dates)-1],
		TargetPositionCount: 1, MaxStockCount: 5, TotalLayers: 10,
		MaxLayersPerStock: 4, InitialCapital: 100000,
	}
	engine := backtest.New(cfg, days, bars, nil)
	result, err := engine.Run(buildPlan)
	if err != nil {
		t.Fatalf("回测执行失败：%v", err)
	}
	return result
}

func mustAtoi(s string) int64 {
	var v int64
	for _, ch := range s {
		v = v*10 + int64(ch-'0')
	}
	return v
}

// 带信号的回测：main 里包一层 Apply。
func runWithAssist(t *testing.T, provider chanlunassist.Provider, cfg chanlunassist.Config) *backtest.Result {
	t.Helper()
	state := chanlunassist.NewState()
	base := holdPlan()
	wrapped := func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := base(held, day)
		return chanlunassist.Apply(plan, held, int(mustAtoi(day.Date)), provider, cfg, state)
	}
	return runEngine(t, wrapped)
}

// 二买在第一天（建仓日）出现、三卖在崩塌前出现：
// 期望「买得更多 + 提前退出」同时改善收益与回撤。
func TestOverlayImprovesReturnAndDrawdownOnControlledPath(t *testing.T) {
	_, dates := crashPathBars("600000")
	// 第 0 天出建仓计划（T+1 成交）；第 38 天是 15.0 的高点，出三卖。
	buyDay := mustAtoi(dates[0])
	sellDay := mustAtoi(dates[38])
	provider := scriptedProvider{byDate: map[int]chanlunassist.Snapshot{
		int(buyDay):  {Buy2: signal(chanlun.SignalBuy2, int(buyDay))},
		int(sellDay): {Sell3: signal(chanlun.SignalSell3, int(sellDay))},
	}}

	baseline := runEngine(t, holdPlan())
	overlay := runWithAssist(t, provider, chanlunassist.DefaultConfig())

	baseMetrics := backtest.CalculateMetrics(baseline.Snapshots, baseline.Trades, 100000)
	overMetrics := backtest.CalculateMetrics(overlay.Snapshots, overlay.Trades, 100000)

	if overMetrics.TotalReturn <= baseMetrics.TotalReturn {
		t.Errorf("叠加后总收益应更高：基线 %.2f%% / 叠加 %.2f%%",
			baseMetrics.TotalReturn, overMetrics.TotalReturn)
	}
	if overMetrics.MaxDrawdown >= baseMetrics.MaxDrawdown {
		t.Errorf("叠加后最大回撤应更小：基线 %.2f%% / 叠加 %.2f%%",
			baseMetrics.MaxDrawdown, overMetrics.MaxDrawdown)
	}

	// 加层：建仓应变成 2 层，且流水里有缠论留痕。
	buyLayers := 0
	for _, trade := range overlay.Trades {
		if trade.Action == "buy" {
			buyLayers += trade.Shares
			if !strings.Contains(trade.Reason, "缠论二买") {
				t.Errorf("买入原因应带缠论留痕，实际 %q", trade.Reason)
			}
		}
	}
	baseBuyLayers := 0
	for _, trade := range baseline.Trades {
		if trade.Action == "buy" {
			baseBuyLayers += trade.Shares
		}
	}
	if buyLayers <= baseBuyLayers {
		t.Errorf("叠加后建仓股数应更多：基线 %d / 叠加 %d", baseBuyLayers, buyLayers)
	}

	// 卖出：三卖应清掉 2 层，且发生在崩塌前。
	soldBeforeCrash := false
	for _, trade := range overlay.Trades {
		if trade.Action != "sell" {
			continue
		}
		if !strings.Contains(trade.Reason, "缠论三卖") {
			t.Errorf("卖出原因应带缠论留痕，实际 %q", trade.Reason)
		}
		if trade.TradeDate == itoa(int(sellDay)+1) {
			soldBeforeCrash = true
		}
	}
	if !soldBeforeCrash {
		t.Errorf("三卖应在信号次日（%d）成交，实际成交：%+v", sellDay+1, overlay.Trades)
	}
	// 清仓后净值应基本走平。
	last := overlay.Snapshots[len(overlay.Snapshots)-1]
	if last.PositionCount != 0 {
		t.Errorf("三卖减 2 层后应清仓（底仓 1 层 + 二买 1 层），实际仍持有 %d 只", last.PositionCount)
	}

	t.Logf("基线：收益 %.2f%% / 回撤 %.2f%% / 成交 %d 笔",
		baseMetrics.TotalReturn, baseMetrics.MaxDrawdown, len(baseline.Trades))
	t.Logf("叠加：收益 %.2f%% / 回撤 %.2f%% / 成交 %d 笔",
		overMetrics.TotalReturn, overMetrics.MaxDrawdown, len(overlay.Trades))
}

// 没有信号时，叠加层必须是严格的 no-op：净值与成交逐字段一致。
func TestOverlayWithoutSignalsEqualsBaseline(t *testing.T) {
	baseline := runEngine(t, holdPlan())
	overlay := runWithAssist(t, scriptedProvider{}, chanlunassist.DefaultConfig())

	if !reflect.DeepEqual(baseline.Snapshots, overlay.Snapshots) {
		t.Error("无信号时净值曲线应与基线完全一致")
	}
	if !reflect.DeepEqual(baseline.Trades, overlay.Trades) {
		t.Error("无信号时成交应与基线完全一致")
	}
}

// 只开卖出侧（sells_only）时，买入股数必须与基线相同，卖出更早。
func TestSellOnlyKeepsEntrySizeButExitsEarlier(t *testing.T) {
	_, dates := crashPathBars("600000")
	sellDay := int(mustAtoi(dates[38]))
	provider := scriptedProvider{byDate: map[int]chanlunassist.Snapshot{
		sellDay: {Sell3: signal(chanlun.SignalSell3, sellDay)},
	}}
	cfg := chanlunassist.DefaultConfig()
	cfg.Buy2ExtraLayers, cfg.Buy3ExtraLayers = 0, 0

	baseline := runEngine(t, holdPlan())
	sellsOnly := runWithAssist(t, provider, cfg)

	buyShares := func(trades []backtest.Trade) int {
		sum := 0
		for _, trade := range trades {
			if trade.Action == "buy" {
				sum += trade.Shares
			}
		}
		return sum
	}
	if buyShares(sellsOnly.Trades) != buyShares(baseline.Trades) {
		t.Errorf("只开卖出侧时买入股数应不变：%d vs %d",
			buyShares(sellsOnly.Trades), buyShares(baseline.Trades))
	}
	sold := false
	for _, trade := range sellsOnly.Trades {
		if trade.Action == "sell" {
			sold = true
		}
	}
	if !sold {
		t.Error("只开卖出侧时应在三卖后减仓")
	}
}
