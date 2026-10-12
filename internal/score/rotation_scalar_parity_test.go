package score

import (
	"math"
	"testing"
)

// 与原版 `portfolio_rotation_logic` 的**纯标量 helper** 逐项比对。
//
// 黄金值由 `testdata/gen_rotation_scalar_golden.py` 跑原版产出（见
// `rotation_scalar_golden_test.go`）。这些函数不吃行、也不吃 policy，
// 输入就是几个数字/字符串 —— 正因为如此，"容差差一位、边界差一个"这类错
// 最容易藏在这里，而且靠读代码很难看出来。
//
// 覆盖关系：
//
//	_ma_pullback_touched            ↔ maPullbackTouched
//	_close_recovery_ratio           ↔ closeRecoveryRatio
//	_is_confirmed_pullback_rebound  ↔ isConfirmedPullbackRebound
//	_low_buy_drawdown_bounds        ↔ lowBuyDrawdownBounds
//	_low_buy_position_thresholds    ↔ lowBuyPositionThresholds
//	_low_buy_dynamic_target_count   ↔ LowBuyDynamicTargetCount
//	minimum_fill_layers             ↔ minimumFillLayers
//	overflow_position_limit_for_state   ↔ overflowPositionLimitForState
//	candidate_signal_max_age_for_state  ↔ signalMaxAgeForState
//	overflow_signal_max_age_for_state   ↔ overflowSignalMaxAgeForState
//	_market_state_label                 ↔ marketStateLabelOf

func TestRotationScalarParityWithOriginalPython(t *testing.T) {
	t.Run("均线回踩", func(t *testing.T) {
		for i, c := range goldenMaCases {
			if got := maPullbackTouched(c.PriceLow, c.PriceClose, c.MaValue); got != c.Touched {
				t.Errorf("第 %d 例 low=%v close=%v ma=%v：原版 %v，Go %v",
					i, c.PriceLow, c.PriceClose, c.MaValue, c.Touched, got)
			}
		}
	})

	t.Run("收盘收复比例", func(t *testing.T) {
		for i, c := range goldenRecoveryCases {
			got := closeRecoveryRatio(c.PriceHigh, c.PriceLow, c.PriceClose)
			if !almostEqual(got, c.Ratio, 1e-9) {
				t.Errorf("第 %d 例 high=%v low=%v close=%v：原版 %v，Go %v",
					i, c.PriceHigh, c.PriceLow, c.PriceClose, c.Ratio, got)
			}
		}
	})

	t.Run("回踩收复确认", func(t *testing.T) {
		for i, c := range goldenReboundCases {
			got := isConfirmedPullbackRebound(c.Open, c.High, c.Low, c.Close, c.PctChg, c.MaValue)
			if got != c.OK {
				t.Errorf("第 %d 例 %+v：原版 %v，Go %v", i, c, c.OK, got)
			}
		}
	})

	t.Run("低吸回撤档位", func(t *testing.T) {
		for _, c := range goldenDrawdownCases {
			low, high := lowBuyDrawdownBounds(c.ReadyCount)
			if !almostEqual(low, c.Low, 1e-9) || !almostEqual(high, c.High, 1e-9) {
				t.Errorf("ready=%d：原版 [%v, %v]，Go [%v, %v]",
					c.ReadyCount, c.Low, c.High, low, high)
			}
		}
	})

	t.Run("仓位档位", func(t *testing.T) {
		for mode, want := range goldenPositionThresholds {
			got := lowBuyPositionThresholds(mode)
			if len(got) != len(want) {
				t.Errorf("mode=%q：原版 %d 档，Go %d 档（%v vs %v）",
					mode, len(want), len(got), want, got)
				continue
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("mode=%q 第 %d 档：原版 %v，Go %v", mode, i, want[i], got[i])
				}
			}
		}
	})

	t.Run("低吸动态目标只数", func(t *testing.T) {
		for i, c := range goldenTargetCountCases {
			got := LowBuyDynamicTargetCount(c.ReadyCount, c.TargetPositionCount, c.MaxStockCount, c.PositionMode)
			if got != c.Want {
				t.Errorf("第 %d 例 ready=%d target=%d maxStock=%d mode=%q：原版 %d，Go %d",
					i, c.ReadyCount, c.TargetPositionCount, c.MaxStockCount, c.PositionMode, c.Want, got)
			}
		}
	})

	t.Run("层数下限", func(t *testing.T) {
		for total, want := range goldenMinimumFillLayers {
			if got := minimumFillLayers(total); got != want {
				t.Errorf("total_layers=%d：原版 %d，Go %d", total, want, got)
			}
		}
	})

	t.Run("溢出补位上限", func(t *testing.T) {
		for state, want := range goldenOverflowPositionLimit {
			if got := overflowPositionLimitForState(state); got != want {
				t.Errorf("state=%q：原版 %d，Go %d", state, want, got)
			}
		}
	})

	t.Run("候选信号年龄上限", func(t *testing.T) {
		for state, want := range goldenSignalMaxAge {
			if got := signalMaxAgeForState(state); got != want {
				t.Errorf("state=%q：原版 %d，Go %d", state, want, got)
			}
		}
	})

	t.Run("溢出信号年龄上限", func(t *testing.T) {
		for state, want := range goldenOverflowSignalMaxAge {
			if got := overflowSignalMaxAgeForState(state); got != want {
				t.Errorf("state=%q：原版 %d，Go %d", state, want, got)
			}
		}
	})

	t.Run("市场状态文案", func(t *testing.T) {
		for state, want := range goldenMarketStateLabels {
			if got := marketStateLabelOf(state); got != want {
				t.Errorf("state=%q：原版 %q，Go %q", state, want, got)
			}
		}
	})
}

// almostEqual 处理 NaN：两边都是 NaN 视为相等（原版会出现 0/0 的分母）。
func almostEqual(a, b, tol float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	if math.IsNaN(a) != math.IsNaN(b) {
		return false
	}
	return math.Abs(a-b) <= tol
}
