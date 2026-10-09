package backtest

import (
	"testing"

	"freestock/internal/score"
)

// ⚠ 回归测试：`holding_trade_days` 必须按**交易日**递增。
//
// 原实现 `_build_held_rows`（:215）：
//
//	holding_trade_days = max(0, current_index - buy_index + 1)   # 当天买算 1 天
//
// 我此前**完全没设这个字段** → 回测里恒为 0。而它被用在四处：
//   - 加层的天数门槛（`HoldingTradeDays < minHoldingDays` → 恒成立 → **加层永远被挡**）
//   - 急涨兑现（"买后短期急涨" → 对任何浮盈持仓**立刻触发**）
//   - 超跌修复失败退出
//
// 这些系统性偏差会直接让回测结果与原版对不上。
func TestHeldRowsHoldingTradeDays(t *testing.T) {
	cfg := Config{
		StartDate: "20260105", EndDate: "20260108",
		TargetPositionCount: 1, MaxStockCount: 1, TotalLayers: 1, MaxLayersPerStock: 1,
		InitialCapital: 100000,
		CommissionRate: 0.00025, MinCommission: 5,
	}
	bars := []PriceBar{
		bar("20260105", "600519", 100, 100),
		bar("20260106", "600519", 100, 100),
		bar("20260107", "600519", 100, 100),
		bar("20260108", "600519", 100, 100),
	}
	days := []StrengthDay{
		{Date: "20260105", MarketState: score.StateRange},
		{Date: "20260106", MarketState: score.StateRange},
		{Date: "20260107", MarketState: score.StateRange},
		{Date: "20260108", MarketState: score.StateRange},
	}

	// 记录每天计划构建器看到的"持仓天数"
	seen := map[string]int{}
	observed := map[string]bool{}
	builder := func(held []score.Row, day StrengthDay) score.RotationPlan {
		if len(held) > 0 {
			seen[day.Date] = held[0].HoldingTradeDays
			observed[day.Date] = true
		}
		if day.Date == "20260105" {
			return score.RotationPlan{Rows: []score.RotationRow{{
				Code: "600519", Name: "贵州茅台", Action: score.ActionAdd,
				TargetLayers: 1, LayersDelta: 1, RankNo: 1, Score: 80,
			}}}
		}
		// 之后几天给"继续持有"，避免卖出干扰
		return score.RotationPlan{Rows: []score.RotationRow{{
			Code: "600519", Name: "贵州茅台", Action: score.ActionKeep,
			TargetLayers: 1, CurrentLayers: 1, RankNo: 1, Score: 80,
		}}}
	}

	engine := New(cfg, days, bars, nil)
	if _, err := engine.Run(builder); err != nil {
		t.Fatalf("回测失败：%v", err)
	}

	// 1/5 收盘后才建仓，所以持仓是**从 1/6 起**被计划构建器看到：
	// 1/6 持有 1 个交易日、1/7 是 2、1/8 是 3
	want := map[string]int{"20260106": 1, "20260107": 2, "20260108": 3}
	for date, expected := range want {
		if !observed[date] {
			t.Errorf("%s 计划构建器没看到持仓（建仓可能没成功）", date)
			continue
		}
		if seen[date] != expected {
			t.Errorf("%s 的持有交易日数应为 %d，实际 %d", date, expected, seen[date])
		}
	}
	t.Logf("持有交易日数：1/6=%d 1/7=%d 1/8=%d（应为 1/2/3）",
		seen["20260106"], seen["20260107"], seen["20260108"])
}

// 建仓当天算 1 天，且**不随自然日间隔漂移**（用交易日序号，不是日历差）。
func TestHoldingTradeDaysUsesTradingDaysNotCalendar(t *testing.T) {
	cfg := Config{
		StartDate: "20260105", EndDate: "20260112",
		TargetPositionCount: 1, MaxStockCount: 1, TotalLayers: 1, MaxLayersPerStock: 1,
		InitialCapital: 100000, CommissionRate: 0.00025, MinCommission: 5,
	}
	// 1/5 之后隔了一个周末（1/10、1/11 不是交易日）再到 1/12
	bars := []PriceBar{
		bar("20260105", "600519", 100, 100),
		bar("20260106", "600519", 100, 100),
		bar("20260112", "600519", 100, 100),
	}
	days := []StrengthDay{
		{Date: "20260105", MarketState: score.StateRange},
		{Date: "20260106", MarketState: score.StateRange},
		{Date: "20260112", MarketState: score.StateRange},
	}

	seen := map[string]int{}
	builder := func(held []score.Row, day StrengthDay) score.RotationPlan {
		if len(held) > 0 {
			seen[day.Date] = held[0].HoldingTradeDays
		}
		if day.Date == "20260105" {
			return score.RotationPlan{Rows: []score.RotationRow{{
				Code: "600519", Name: "贵州茅台", Action: score.ActionAdd,
				TargetLayers: 1, LayersDelta: 1, RankNo: 1, Score: 80,
			}}}
		}
		return score.RotationPlan{Rows: []score.RotationRow{{
			Code: "600519", Name: "贵州茅台", Action: score.ActionKeep,
			TargetLayers: 1, CurrentLayers: 1, RankNo: 1, Score: 80,
		}}}
	}

	engine := New(cfg, days, bars, nil)
	if _, err := engine.Run(builder); err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	// 1/6 是买入后的第 1 个交易日；1/12 是第 2 个（中间那 6 个自然日不算）
	if seen["20260106"] != 1 {
		t.Errorf("1/6 应为 1 个交易日，实际 %d", seen["20260106"])
	}
	if seen["20260112"] != 2 {
		t.Errorf("1/12 应为 2 个交易日（不是自然日的 7 天），实际 %d", seen["20260112"])
	}
	t.Logf("1/6=%d，1/12=%d（按交易日，不按自然日）", seen["20260106"], seen["20260112"])
}
