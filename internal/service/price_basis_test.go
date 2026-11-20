package service

import (
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// P2：计划/候选行里的价格是**复权价**，展示与资金折算必须换算成**原始价**。
//
// 用真实数据湖验证：茅台（600519）后复权价约为原始价的 8~9 倍，
// 换算后 Close 必须等于原始收盘价（与券商一致），而不是复权价。
func TestConvertPlanPricesToRaw(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") {
		t.Skip("数据湖不可用，跳过")
	}
	if _, err := svc.stock.SetDataSource("cnlake", lake.Root()); err != nil {
		t.Skipf("切湖失败：%v", err)
	}
	tradeDate := svc.latestTradeDate()
	if tradeDate <= 0 {
		t.Skip("拿不到最新交易日")
	}
	raw := svc.rawClosesAt([]string{"600519"}, tradeDate)
	rawClose := raw["600519"]
	if rawClose <= 0 {
		t.Skip("数据湖里没有 600519 的当日原始价")
	}

	// 故意给一个"复权口径"的价格（原始价 × 8.9）
	adjusted := rawClose * 8.9
	result := &PlanResult{
		StrengthDate: tradeDate,
		Candidates: []score.Row{{
			Code: "600519", Name: "贵州茅台",
			Close: adjusted, Open: adjusted, High: adjusted, Low: adjusted,
			Ma20: adjusted, // 指标保持复权口径，不能被换算
		}},
		Holdings: []score.Row{{
			Code: "600519", Name: "贵州茅台", Close: adjusted,
			Open: adjusted, High: adjusted, Low: adjusted,
		}},
	}
	svc.convertPlanPricesToRaw(result)

	got := result.Candidates[0]
	if diff := got.Close - rawClose; diff > rawClose*0.02 || diff < -rawClose*0.02 {
		t.Fatalf("候选行 Close=%.2f，应换算成原始价 %.2f（复权价 %.2f）", got.Close, rawClose, adjusted)
	}
	if got.Ma20 != adjusted {
		t.Errorf("均线属于指标，必须保持复权口径：Ma20=%.2f，期望 %.2f", got.Ma20, adjusted)
	}
	if h := result.Holdings[0]; h.Close < rawClose*0.98 || h.Close > rawClose*1.02 {
		t.Errorf("持仓行 Close=%.2f，应换算成原始价 %.2f", h.Close, rawClose)
	}
}

// P2：建仓价缺省时必须是**原始价**（与券商一致），不能取评分表的复权价。
func TestAddPositionDefaultsToRawClose(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") {
		t.Skip("数据湖不可用，跳过")
	}
	if _, err := svc.stock.SetDataSource("cnlake", lake.Root()); err != nil {
		t.Skipf("切湖失败：%v", err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "口径测试", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatal(err)
	}
	pos, err := svc.AddPosition(created.ID, portfolio.PositionInput{Code: "600519", Layers: intPtr(1)})
	if err != nil {
		t.Skipf("建仓失败（可能没有行情）：%v", err)
	}
	tradeDate := svc.latestTradeDate()
	raw := svc.rawClosesAt([]string{"600519"}, tradeDate)["600519"]
	if raw <= 0 {
		t.Skip("拿不到原始价")
	}
	if ratio := pos.EntryPrice / raw; ratio < 0.8 || ratio > 1.25 {
		t.Fatalf("建仓价 %.2f 与原始收盘 %.2f 偏差 %.2f 倍 —— 可能取到了复权价",
			pos.EntryPrice, raw, ratio)
	}
}
