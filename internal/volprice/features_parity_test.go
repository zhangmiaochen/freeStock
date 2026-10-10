package volprice

import (
	"math"
	"testing"
)

// **量价特征层与原版 pandas 实现的 1:1 比对。**
//
// 黄金值由 `testdata/gen_pandas_golden.py` 生成 —— 那个脚本**直接 import 原项目的
// `volume_price_features`**，在同一份 70 根 K 线上跑出全部派生列。
// 所以这个用例失败就意味着"我的 Go 实现与原版算出来的数不一样"，
// 而不是"我按自己的理解写了个期望值"。
//
// 覆盖到的边界：
//   - `rolling(w, min_periods=m)` 的窗口截断（前 w-1 根）与 min_periods 门槛
//     （例如 rolling(60, min_periods=45) 在第 45 根才出第一个非 NaN）
//   - `shift(59)` / `shift(21)` 造成的整段缺失
//   - 退化 K 线：第 30 根一字板（high == low → rng 为 NaN → close_pos 填 0.5）
//     与第 45 根十字星（open == close → body 为 0 → 走 body_floor 分支）
//   - `volume_decay` 的两级 mask、`three_day_drift` 的布尔链
func TestFeatureParityWithOriginalPandas(t *testing.T) {
	if len(goldenBars) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_pandas_golden.py")
	}
	bars := make([]Bar, 0, len(goldenBars))
	for _, item := range goldenBars {
		bars = append(bars, Bar{
			Date: item.Date, Open: item.Open, High: item.High,
			Low: item.Low, Close: item.Close, Volume: item.Volume,
		})
	}
	frame := AttachVectorBarsColumns(bars)
	if frame.Len() != len(goldenBars) {
		t.Fatalf("K 线根数不一致：go %d，pandas %d", frame.Len(), len(goldenBars))
	}
	numeric, boolean, text := frame.Columns()

	for column, want := range goldenNumeric {
		got, ok := numeric[column]
		if !ok {
			t.Errorf("列 %s 在 Go 侧不存在", column)
			continue
		}
		compareNumericSeries(t, column, got, want)
	}
	for column, want := range goldenBool {
		got, ok := boolean[column]
		if !ok {
			t.Errorf("列 %s 在 Go 侧不存在", column)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("列 %s 长度不一致：go %d，pandas %d", column, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("列 %s 第 %d 行：go %v，pandas %v", column, i, got[i], want[i])
				break
			}
		}
	}
	for column, want := range goldenText {
		got, ok := text[column]
		if !ok {
			t.Errorf("列 %s 在 Go 侧不存在", column)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("列 %s 长度不一致：go %d，pandas %d", column, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("列 %s 第 %d 行：go %q，pandas %q", column, i, got[i], want[i])
				break
			}
		}
	}
}

// compareNumericSeries 做 NaN 感知的逐元素比较。
//
// 容差 1e-9：Go 与 pandas 的求和顺序不同（例如 rolling mean 的累加次序），
// 末位可能有舍入差。容差之外一律算不一致。
func compareNumericSeries(t *testing.T, column string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("列 %s 长度不一致：go %d，pandas %d", column, len(got), len(want))
		return
	}
	for i := range want {
		gotNaN, wantNaN := math.IsNaN(got[i]), math.IsNaN(want[i])
		if gotNaN || wantNaN {
			if gotNaN != wantNaN {
				t.Errorf("列 %s 第 %d 行：go %v，pandas %v（NaN 位置不一致）",
					column, i, got[i], want[i])
				return
			}
			continue
		}
		if diff := math.Abs(got[i] - want[i]); diff > 1e-9 {
			t.Errorf("列 %s 第 %d 行：go %.12f，pandas %.12f（差 %.3g）",
				column, i, got[i], want[i], diff)
			return
		}
	}
}

// `calculate_feature_snapshot_prepared` 的最后一根快照也要逐字段一致。
func TestFeatureSnapshotParityWithOriginalPandas(t *testing.T) {
	bars := make([]Bar, 0, len(goldenBars))
	for _, item := range goldenBars {
		bars = append(bars, Bar{
			Date: item.Date, Open: item.Open, High: item.High,
			Low: item.Low, Close: item.Close, Volume: item.Volume,
		})
	}
	got := AttachVectorBarsColumns(bars).Snapshot()
	want := goldenSnapshot

	if got.TradeDate != want.TradeDate {
		t.Errorf("TradeDate：go %q，pandas %q", got.TradeDate, want.TradeDate)
	}
	compareFloat := func(name string, a, b float64) {
		t.Helper()
		if math.IsNaN(a) || math.IsNaN(b) {
			if math.IsNaN(a) != math.IsNaN(b) {
				t.Errorf("%s：go %v，pandas %v", name, a, b)
			}
			return
		}
		if diff := math.Abs(a - b); diff > 1e-9 {
			t.Errorf("%s：go %.12f，pandas %.12f", name, a, b)
		}
	}
	compareFloat("Close", got.Close, want.Close)
	compareFloat("TrendScore", got.TrendScore, want.TrendScore)
	compareFloat("VolumeDecayScore", got.VolumeDecayScore, want.VolumeDecayScore)
	compareFloat("BehaviorQualityScore", got.BehaviorQualityScore, want.BehaviorQualityScore)
	compareFloat("PlatformRangePct", optValue(got.PlatformRangePct), want.PlatformRangePct)
	compareFloat("VolumeRatio20", optValue(got.VolumeRatio20), want.VolumeRatio20)
	compareFloat("PullbackRatio", optValue(got.PullbackRatio), want.PullbackRatio)
	compareFloat("SupportPrice", optValue(got.SupportPrice), want.SupportPrice)
	compareFloat("LowerShadowRatio", optValue(got.LowerShadowRatio), want.LowerShadowRatio)
	compareFloat("UpperShadowRatio", optValue(got.UpperShadowRatio), want.UpperShadowRatio)
	compareFloat("PlatformHigh", optValue(got.PlatformHigh), want.PlatformHigh)
	compareFloat("PlatformLow", optValue(got.PlatformLow), want.PlatformLow)

	compareBool := func(name string, a, b bool) {
		t.Helper()
		if a != b {
			t.Errorf("%s：go %v，pandas %v", name, a, b)
		}
	}
	compareBool("HigherHigh", got.HigherHigh, want.HigherHigh)
	compareBool("HigherLow", got.HigherLow, want.HigherLow)
	compareBool("SupportHold", got.SupportHold, want.SupportHold)
	compareBool("BreakoutFlag", got.BreakoutFlag, want.BreakoutFlag)
	compareBool("PanicVolumeFlag", got.PanicVolumeFlag, want.PanicVolumeFlag)
	compareBool("ReversalFlag", got.ReversalFlag, want.ReversalFlag)
	compareBool("DeathVolumeRisk", got.DeathVolumeRisk, want.DeathVolumeRisk)

	compareText := func(name, a, b string) {
		t.Helper()
		if a != b {
			t.Errorf("%s：go %q，pandas %q", name, a, b)
		}
	}
	compareText("VolumeBehavior", got.VolumeBehavior, want.VolumeBehavior)
	compareText("AcceptanceQuality", got.AcceptanceQuality, want.AcceptanceQuality)
	compareText("UpperShadowBehavior", got.UpperShadowBehavior, want.UpperShadowBehavior)
}

func optValue(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}
