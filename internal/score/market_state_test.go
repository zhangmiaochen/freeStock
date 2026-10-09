package score

import (
	"math"
	"testing"
)

// F52 边界：`len < 40` 时前段窗口的取法。
//
// 原实现 volume_price_market_state.py:286-291：
//
//	if len(grouped) >= 40: prev = grouped.iloc[-40:-20]
//	else:                  prev = grouped.head(max(1, len(grouped) - 20))
//
// 此前我的成交额分支前面挂着 `if len(amounts) >= 2*recent` ——
// **不足 40 天时整段跳过，amount_growth 恒为 0**，
// 而原实现照样用 head 那段算。市场状态因此在样本不足时判定偏移。
func TestMarketStateShortHistoryAmountGrowth(t *testing.T) {
	// 30 天，成交额递增 → 前段（第 5..9 天）明显小于近 5 天
	days := mkMarketDays(30, 0.2, 0.6, 0)
	for i := range days {
		days[i].TotalAmount = float64(1_000_000_000 + i*100_000_000)
	}
	got := BuildMarketState(days)

	if got.AmountGrowth == 0 {
		t.Fatalf("样本 30 天（<40）时成交额增速不该是 0，实际 %+v", got)
	}
	// 手算复核：prev = head(10) → tail(5) = 第 5..9 天；recent = tail(20) → tail(5) = 第 25..29 天
	want := func(from, to int) float64 {
		sum := 0.0
		for i := from; i < to; i++ {
			sum += days[i].TotalAmount
		}
		return sum / float64(to-from)
	}
	recentAmt := want(25, 30)
	prevAmt := want(5, 10)
	expected := (recentAmt/prevAmt - 1) * 100
	if math.Abs(got.AmountGrowth-expected) > 0.01 {
		t.Errorf("成交额增速应为 %.4f，实际 %.4f", expected, got.AmountGrowth)
	}
}

// 恰好 20 天时 `head(max(1, 0))` = `head(1)` = **第 1 行**（不是退化成近段）。
func TestMarketStateExactlyTwentyDays(t *testing.T) {
	days := mkMarketDays(20, 0.3, 0.6, 0)
	for i := range days {
		days[i].TotalAmount = float64(1_000_000_000 + i*50_000_000)
	}
	got := BuildMarketState(days)

	// 前段 = 第 0 天；近 5 日 = 第 15..19 天
	recentAmt := 0.0
	for i := 15; i < 20; i++ {
		recentAmt += days[i].TotalAmount
	}
	recentAmt /= 5
	prevAmt := days[0].TotalAmount
	want := (recentAmt/prevAmt - 1) * 100
	if math.Abs(got.AmountGrowth-want) > 0.01 {
		t.Errorf("20 天时前段应取第 1 行：期望 %.4f，实际 %.4f", want, got.AmountGrowth)
	}
	// 少于 20 天 → 直接判震荡并给出理由
	short := BuildMarketState(mkMarketDays(10, 1.0, 0.8, 1e12))
	if short.State != StateRange {
		t.Errorf("不足 20 天应判震荡，实际 %s", short.State)
	}
}
