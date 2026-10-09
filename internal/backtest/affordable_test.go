package backtest

import (
	"testing"
	"time"
)

// ⚠ 回归测试：`affordableBuyShares` 曾经是个**死循环**。
//
// 原实现是 `shares -= max(1, int(excess / price))`，我写成了 `max0(...)`。
// 而 `excess` 只是"手续费那点差额"，通常远小于股价 → `int(excess/price)` 恒为 0
// → `shares` 永不减少 → 死循环。`shares < 0` 那道保险也永远不触发。
//
// 后果：回测只要真正下单就卡死。
func TestAffordableBuySharesTerminates(t *testing.T) {
	cfg := Config{
		TotalLayers: 10, MaxLayersPerStock: 3,
		CommissionRate: 0.00025, MinCommission: 5.0,
	}.withDefaults()
	engine := New(cfg, nil, nil, nil)

	cases := []struct {
		name      string
		available float64
		price     float64
		want      int
	}{
		// 1000 元买 10 元的股票：100 股要 1000 + 5(最低佣金) = 1005 > 1000
		// → 应回落到 99 股：990 + 5 = 995 ≤ 1000
		{"刚好差一点佣金", 1000, 10, 99},
		// 资金远大于一手，正常成交（佣金按万分之 2.5）
		{"资金充足", 100000, 10, 9997},
		// 买不起一手
		{"买不起", 5, 10, 0},
		// 边界：正好够
		{"价格为零", 1000, 0, 0},
		{"资金为零", 0, 10, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan int, 1)
			go func() { done <- engine.affordableBuyShares(tc.available, tc.price) }()

			select {
			case got := <-done:
				if got != tc.want {
					t.Errorf("available=%.0f price=%.0f：应为 %d 股，实际 %d",
						tc.available, tc.price, tc.want, got)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("available=%.0f price=%.0f 超过 3 秒没返回 —— 死循环回归了",
					tc.available, tc.price)
			}
		})
	}
}

// 买得起的股数必须满足"含佣金总成本 ≤ 可用资金"，且再多一股就买不起。
func TestAffordableBuySharesIsMaximal(t *testing.T) {
	cfg := Config{
		TotalLayers: 10, MaxLayersPerStock: 3,
		CommissionRate: 0.00025, MinCommission: 5.0,
	}.withDefaults()
	engine := New(cfg, nil, nil, nil)

	cost := func(shares int, price float64) float64 {
		amount := float64(shares) * price
		return amount + maxf(amount*cfg.CommissionRate, cfg.MinCommission)
	}

	for _, price := range []float64{3.7, 10, 25.5, 1888.8} {
		for _, available := range []float64{500, 1000, 3333, 100000} {
			shares := engine.affordableBuyShares(available, price)
			if shares <= 0 {
				continue
			}
			if got := cost(shares, price); got > available+1e-9 {
				t.Errorf("price=%.2f available=%.0f：算出 %d 股，总成本 %.2f 超了可用资金",
					price, available, shares, got)
			}
			// 再多一股就该买不起（否则说明算少了）
			if next := cost(shares+1, price); next <= available+1e-9 {
				t.Errorf("price=%.2f available=%.0f：算出 %d 股，但 %d 股也买得起（%.2f ≤ %.0f）",
					price, available, shares, shares+1, next, available)
			}
		}
	}
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
