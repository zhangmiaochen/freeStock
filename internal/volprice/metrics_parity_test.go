package volprice

import (
	"math"
	"sort"
	"testing"
)

// **四个结构度量与原版 Python 的逐字段比对。**
//
// 黄金值由 `testdata/gen_metrics_golden.py` 生成（直接 import 原版
// `volume_price_models`）。四种形态各自压不同分支：温和上行+回踩、
// 放量突破+整理、长期平台+突破、单边下行。
//
// 比对是**双向**的：既查"我算错的键"，也查"键集合不一致"——
// 少一个字段就说明我的移植漏了一条判定（原实现这里是 30+ 个键的大字典）。
func TestStructuralMetricsParityWithOriginalPython(t *testing.T) {
	if len(goldenMetricsCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_metrics_golden.py")
	}
	for _, tc := range goldenMetricsCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))
			if frame.Len() != len(tc.Bars) {
				t.Fatalf("K 线根数不一致：go %d，python %d", frame.Len(), len(tc.Bars))
			}

			gotTC, okTC := TrendContinuationMetricsOf(frame)
			if okTC != tc.TrendContinuationOK {
				t.Errorf("trend_continuation 可用性：go %v，python %v", okTC, tc.TrendContinuationOK)
			} else if okTC {
				compareFloatMaps(t, "trend_continuation", gotTC.AsFloatMap(), tc.TrendContinuation)
			}

			gotPB, okPB := PowerBreakoutMetricsOf(frame)
			if okPB != tc.PowerBreakoutOK {
				t.Errorf("power_breakout 可用性：go %v，python %v", okPB, tc.PowerBreakoutOK)
			} else if okPB {
				compareFloatMaps(t, "power_breakout", gotPB.AsFloatMap(), tc.PowerBreakout)
				if gotPB.BreakoutDate != tc.PowerBreakoutDate {
					t.Errorf("power_breakout.breakout_date：go %q，python %q",
						gotPB.BreakoutDate, tc.PowerBreakoutDate)
				}
			}

			gotEM, okEM := EarlyMainTrendMetricsOf(frame)
			if okEM != tc.EarlyMainTrendOK {
				t.Errorf("early_maintrend 可用性：go %v，python %v", okEM, tc.EarlyMainTrendOK)
			} else if okEM {
				compareFloatMaps(t, "early_maintrend", gotEM.AsFloatMap(), tc.EarlyMainTrend)
			}

			gotTP, okTP := TrendPlatformMetricsOf(frame)
			if okTP != tc.TrendPlatformOK {
				t.Errorf("trend_platform 可用性：go %v，python %v", okTP, tc.TrendPlatformOK)
			} else if okTP {
				compareFloatMaps(t, "trend_platform", gotTP.AsFloatMap(), tc.TrendPlatform)
			}
		})
	}
}

func barsFromGolden(items []goldenBar) []Bar {
	out := make([]Bar, 0, len(items))
	for _, item := range items {
		out = append(out, Bar{
			Date: item.Date, Open: item.Open, High: item.High,
			Low: item.Low, Close: item.Close, Volume: item.Volume,
		})
	}
	return out
}

// compareFloatMaps 先比键集合，再逐键比数值（NaN 感知，容差 1e-9）。
func compareFloatMaps(t *testing.T, label string, got, want map[string]float64) {
	t.Helper()
	missing := make([]string, 0)
	for key := range want {
		if _, ok := got[key]; !ok {
			missing = append(missing, key)
		}
	}
	extra := make([]string, 0)
	for key := range got {
		if _, ok := want[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("%s：Go 侧缺少这些键（移植漏了判定）：%v", label, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%s：Go 侧多出这些键（原版没有）：%v", label, extra)
	}
	for key, expected := range want {
		mine, ok := got[key]
		if !ok {
			continue
		}
		gotNaN, wantNaN := math.IsNaN(mine), math.IsNaN(expected)
		if gotNaN || wantNaN {
			if gotNaN != wantNaN {
				t.Errorf("%s.%s：go %v，python %v", label, key, mine, expected)
			}
			continue
		}
		if diff := math.Abs(mine - expected); diff > 1e-9 {
			t.Errorf("%s.%s：go %.12f，python %.12f（差 %.3g）", label, key, mine, expected, diff)
		}
	}
}
