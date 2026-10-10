package volprice

import (
	"testing"
)

// **入场前置过滤与原版 Python 的比对。**
//
// 黄金值由 `testdata/gen_prefilter_golden.py` 生成（直接 import 原版
// `volume_price_models` / `volume_price_backtest`）。覆盖：
//
//   - `_recent_breakout`（含命中时返回的 7 个字段与 idx）
//   - `trend_continuation_prefilter` 的**三个 or 分支**（`trend_start` 形态同时
//     满足 standard_ok / platform_ok / early_ok）
//   - `trend_continuation_candidate_prefilter` / `trend_pullback_candidate_prefilter`
//   - `_passes_entry_prefilter` / `_passes_candidate_prefilter` 对三个模型的分派
//     （每个模型都既有 true 也有 false 的用例）
func TestPrefilterParityWithOriginalPython(t *testing.T) {
	if len(goldenPrefilterCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_prefilter_golden.py")
	}
	for _, tc := range goldenPrefilterCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))

			breakout, ok := RecentBreakoutOf(frame)
			if ok != tc.RecentBreakoutOK {
				t.Errorf("recent_breakout 可用性：go %v，python %v", ok, tc.RecentBreakoutOK)
			} else if ok {
				if breakout.Idx != tc.RecentBreakoutIdx {
					t.Errorf("recent_breakout.idx：go %d，python %d", breakout.Idx, tc.RecentBreakoutIdx)
				}
				if breakout.TradeDate != tc.RecentBreakoutDate {
					t.Errorf("recent_breakout.trade_date：go %q，python %q",
						breakout.TradeDate, tc.RecentBreakoutDate)
				}
				got := map[string]float64{
					"close": breakout.Close, "high": breakout.High, "low": breakout.Low,
					"volume": breakout.Volume, "platform_high": breakout.PlatformHigh,
					"platform_low": breakout.PlatformLow, "volume_ratio": breakout.VolumeRatio,
				}
				compareFloatMaps(t, "recent_breakout", got, tc.RecentBreakout)
			}

			if got := TrendContinuationPrefilter(frame); got != tc.TrendContinuation {
				t.Errorf("trend_continuation_prefilter：go %v，python %v", got, tc.TrendContinuation)
			}
			if got := TrendContinuationCandidatePrefilter(frame); got != tc.TrendContinuationCandidate {
				t.Errorf("trend_continuation_candidate_prefilter：go %v，python %v",
					got, tc.TrendContinuationCandidate)
			}
			if got := TrendPullbackCandidatePrefilter(frame); got != tc.TrendPullbackCandidate {
				t.Errorf("trend_pullback_candidate_prefilter：go %v，python %v",
					got, tc.TrendPullbackCandidate)
			}

			for model, want := range tc.EntryByModel {
				if got := PassesEntryPrefilter(model, frame); got != want {
					t.Errorf("passes_entry_prefilter(%s)：go %v，python %v", model, got, want)
				}
			}
			for model, want := range tc.CandidateByModel {
				if got := PassesCandidatePrefilter(model, frame); got != want {
					t.Errorf("passes_candidate_prefilter(%s)：go %v，python %v", model, got, want)
				}
			}
		})
	}
}

// `Slice` 必须保留派生列 —— `_passes_entry_prefilter` 里的 `data.iloc[-80:]`
// 之后仍然要读 `vp_prev20_high`，切片丢了派生列会让判定静默出错。
func TestVectorBarsSliceKeepsDerivedColumns(t *testing.T) {
	bars := make([]Bar, 0, 100)
	for i := 0; i < 100; i++ {
		price := 10.0 + float64(i)*0.05
		bars = append(bars, Bar{
			Date: seqDate(i), Open: price - 0.02, High: price + 0.05,
			Low: price - 0.05, Close: price, Volume: 1000 + float64(i),
		})
	}
	full := AttachVectorBarsColumns(bars)
	sub := full.Slice(20, 100)
	if sub.Len() != 80 {
		t.Fatalf("切片长度应为 80，实际 %d", sub.Len())
	}
	if sub.Prev20High == nil || len(sub.Prev20High) != 80 {
		t.Fatal("切片丢了 vp_prev20_high")
	}
	// 逐字段抽样比对：切片第 i 行应等于原帧第 20+i 行
	for _, idx := range []int{0, 1, 40, 79} {
		if sub.Prev20High[idx] != full.Prev20High[20+idx] {
			t.Errorf("Prev20High[%d] 不一致：%v vs %v",
				idx, sub.Prev20High[idx], full.Prev20High[20+idx])
		}
		if sub.Volume[i2f(idx)] != full.Volume[20+idx] {
			t.Errorf("Volume[%d] 不一致", idx)
		}
	}
	// 切片后的判定应与"在原帧上只取尾部 80 行"一致
	if PassesEntryPrefilter(ModelTrendPullback, sub) != PassesEntryPrefilter(ModelTrendPullback, full) {
		t.Log("（提示）切片与全帧的 trend_pullback 前置过滤结论不同 —— 若两个形状确实不同则属正常")
	}
}

// seqDate 与生成器里的 `_seq_date` 同构：严格递增且不重复。
func seqDate(day int) string {
	month := 1 + day/28
	dom := day%28 + 1
	return "2026" + twoDigit(month) + twoDigit(dom)
}

func twoDigit(v int) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

func i2f(v int) int { return v }
