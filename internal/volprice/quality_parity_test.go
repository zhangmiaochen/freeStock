package volprice

import (
	"strings"
	"testing"
)

// **行为质量 / 箱体结构 / 超跌修复 与原版 Python 的逐字段比对。**
//
// 黄金值由 `testdata/gen_quality_golden.py` 生成（直接 import 原版
// `volume_price_models`）。6 种形态 × 4 组超跌输入 × 8 组"候选阻断"输入，
// 覆盖 `_candidate_trade_block_reasons` 的**全部分支**。
func TestQualityHelpersParityWithOriginalPython(t *testing.T) {
	if len(goldenQualityCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_quality_golden.py")
	}
	for _, tc := range goldenQualityCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))
			feature := frame.Snapshot()

			quality := BehaviorQualityOf(feature)
			compareGoldenFloat(t, "behavior.score", quality.Score, tc.BehaviorScore)
			if quality.DeathVolumeRisk != tc.BehaviorDeath {
				t.Errorf("behavior.death_volume_risk：go %v，python %v",
					quality.DeathVolumeRisk, tc.BehaviorDeath)
			}
			if quality.VolumeBehavior != tc.VolumeBehavior {
				t.Errorf("behavior.volume_behavior：go %q，python %q",
					quality.VolumeBehavior, tc.VolumeBehavior)
			}
			if quality.AcceptanceQuality != tc.AcceptanceQuality {
				t.Errorf("behavior.acceptance_quality：go %q，python %q",
					quality.AcceptanceQuality, tc.AcceptanceQuality)
			}
			if quality.UpperShadowBehavior != tc.UpperShadowBehavior {
				t.Errorf("behavior.upper_shadow_behavior：go %q，python %q",
					quality.UpperShadowBehavior, tc.UpperShadowBehavior)
			}
			if got := BehaviorConditionText(feature); got != tc.BehaviorText {
				t.Errorf("behavior_condition_text：\n  go     %q\n  python %q", got, tc.BehaviorText)
			}
			compareGoldenFloat(t, "behavior_score_adjust", BehaviorScoreAdjust(feature), tc.ScoreAdjust)
			if got := BehaviorQualityOK(feature, 45.0); got != tc.QualityOK45 {
				t.Errorf("behavior_quality_ok(45)：go %v，python %v", got, tc.QualityOK45)
			}
			if got := strings.Join(BehaviorTradeBlockReasons(feature, 45.0), "|"); got != tc.BlockReasons45 {
				t.Errorf("behavior_trade_block_reasons(45)：\n  go     %q\n  python %q", got, tc.BlockReasons45)
			}

			window, ok := BoxWindowMetricsOf(frame)
			if ok != tc.BoxWindowOK {
				t.Errorf("box_window_metrics 可用性：go %v，python %v", ok, tc.BoxWindowOK)
			} else if ok {
				compareFloatMaps(t, "box_window_metrics", window.AsFloatMap(), tc.BoxWindow)
			}

			box := BoxStructureQualityOf(frame, feature)
			compareFloatMaps(t, "box_structure_quality_fast", box.AsFloatMap(), tc.BoxQuality)
			if got := box.BlockReasonsText(); got != tc.BoxReasons {
				t.Errorf("box block_reasons：\n  go     %q\n  python %q", got, tc.BoxReasons)
			}

			for _, rebound := range tc.Rebounds {
				label := "oversold_rebound[" + formatGoldenInputs(rebound) + "]"
				got := OversoldReboundQualityOf(frame, feature, rebound.Fall5, rebound.Panic, rebound.Rebound)
				compareFloatMaps(t, label, got.AsFloatMap(), rebound.Values)
				if reasons := got.BlockReasonsText(); reasons != rebound.Reasons {
					t.Errorf("%s block_reasons：\n  go     %q\n  python %q", label, reasons, rebound.Reasons)
				}
			}

			for _, block := range tc.Blocks {
				opts := CandidateBlockOptions{
					MinScore:                block.MinScore,
					ConfirmationHits:        block.Hits,
					ConfirmationMisses:      block.Misses,
					HardReasons:             block.Hard,
					SoftReasons:             block.Soft,
					MinProgress:             block.MinProgress,
					MaxSoftMisses:           block.MaxSoft,
					RequireStrongAcceptance: block.RequireStrong,
				}
				got := strings.Join(CandidateTradeBlockReasons(feature, opts), "|")
				if got != block.Want {
					t.Errorf("candidate_trade_block_reasons(hits=%d,misses=%d,soft=%d,hard=%d,min=%.0f,strong=%v)：\n  go     %q\n  python %q",
						len(block.Hits), len(block.Misses), len(block.Soft), len(block.Hard),
						block.MinScore, block.RequireStrong, got, block.Want)
				}
			}
		})
	}
}

func formatGoldenInputs(c goldenReboundCase) string {
	flag := func(v bool) string {
		if v {
			return "T"
		}
		return "F"
	}
	return "fall5=" + formatFixed(c.Fall5, 1) + ",panic=" + flag(c.Panic) + ",rebound=" + flag(c.Rebound)
}
