package backtest

import (
	"testing"
	"time"

	"freestock/internal/score"
)

// ⚠ 回归测试：**带真实手续费**跑通一次买入。
//
// 已有的 `TestEngineExecutesNextDayOpen` 为了"精确核对金额"把手续费关掉了
// （`MinCommission: 0`），于是它的数字恰好满足 `total ≤ available`，直接返回 ——
// **从没走到 `affordableBuyShares` 的回退分支**，也就没发现那里是个死循环。
//
// 这个测试用原实现的**真实默认值**（万分之 2.5、最低 5 元），
// 并且刻意选低价股 + 零头小的资金，逼出"佣金让总价刚好超预算"的情形。
//
// 附超时保护：死循环回归会直接失败，而不是把整个测试挂住。
func TestEngineBuyWithRealisticCommission(t *testing.T) {
	cfg := Config{
		StartDate: "20260105", EndDate: "20260106",
		TargetPositionCount: 1, MaxStockCount: 1, TotalLayers: 1, MaxLayersPerStock: 1,
		InitialCapital: 10000,
		// 原实现的默认值：佣金万分之 2.5、最低 5 元
		CommissionRate: 0.00025, MinCommission: 5,
		SlippagePct: 0,
	}
	// 10 元的股票、1 万本金、1 层 = 全仓：
	// shares = 1000，amount = 10000，commission = max(2.5, 5) = 5
	// → total = 10005 > 10000 → **必须走回退分支**（这里原来会死循环）
	bars := []PriceBar{
		bar("20260105", "000001", 10, 10),
		bar("20260106", "000001", 10, 10),
	}
	days := []StrengthDay{
		{Date: "20260105", MarketState: score.StateRange},
		{Date: "20260106", MarketState: score.StateRange},
	}
	plan := fixedPlan(map[string][]score.RotationRow{
		"20260105": {{
			Code: "000001", Name: "平安银行", Action: score.ActionAdd,
			TargetLayers: 1, LayersDelta: 1, RankNo: 1, Score: 80,
		}},
	})

	engine := New(cfg, days, bars, nil)
	type outcome struct {
		result *Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := engine.Run(plan)
		done <- outcome{res, err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("回测超过 5 秒没结束 —— 买入路径的死循环回归了")
	}
	if got.err != nil {
		t.Fatalf("回测失败：%v", got.err)
	}
	if len(got.result.Trades) != 1 {
		t.Fatalf("应有 1 笔成交，实际 %d", len(got.result.Trades))
	}
	trade := got.result.Trades[0]

	// 注意：`SlippagePct` 传 0 会被 `withDefaults` 补成原实现的默认值 0.1%，
	// 所以成交价是 10 × 1.001 = 10.01（四舍五入到 2 位），不是 10。
	//
	// 于是：shares = int(10000/10.01) = 999
	//       amount = 999 × 10.01 = 9999.99，commission = max(2.4999, 5) = 5
	//       total = 10004.99 > 10000 → **必须走回退分支**（这里原来会死循环）
	//       excess = 4.99，int(4.99/10.01) = 0 → 靠"至少减 1"才收敛到 998
	//       998 × 10.01 = 9989.98 + 5 = 9994.98 ≤ 10000 ✓
	if trade.Shares != 998 {
		t.Errorf("应回退到 998 股（佣金 5 元让它买不起 999 股），实际 %d", trade.Shares)
	}
	if trade.Commission != 5 {
		t.Errorf("佣金应取最低值 5 元，实际 %.4f", trade.Commission)
	}
	if trade.Amount+trade.Commission > cfg.InitialCapital+1e-9 {
		t.Errorf("总成本 %.2f 超过了本金 %.2f", trade.Amount+trade.Commission, cfg.InitialCapital)
	}
	// 含费成本价 = 总成本 / 股数
	wantCost := (trade.Amount + trade.Commission) / float64(trade.Shares)
	if trade.Shares > 0 {
		pos := got.result.Snapshots[len(got.result.Snapshots)-1]
		if pos.PositionCount != 1 {
			t.Errorf("末日应持有 1 只，实际 %d", pos.PositionCount)
		}
	}
	t.Logf("买入 %d 股 @ %.2f，金额 %.2f，佣金 %.2f，含费成本价 %.4f",
		trade.Shares, trade.Price, trade.Amount, trade.Commission, wantCost)
}
