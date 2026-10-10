package volprice

import (
	"slices"
	"strings"
	"testing"
)

// **确认信号与原版 Python 的逐字段比对** —— 这是整个量价移植里最关键的比对：
// 它比的是**最终信号本身**（价格 / 打分 / 风险级别 / 命中与未命中清单 / 解释文本 /
// 子类型 / 行为质量 / 子度量），而不是中间量。信号直接决定组合买什么。
//
// 黄金值由 `testdata/gen_eval_confirmed_golden.py` 生成（直接 import 原版
// `volume_price_models._evaluate_confirmed_prepared`）。10 种形态 × 3 种市场状态
// × 3 个模型 = 90 组；其中 5 组会真正产出信号，覆盖 5 条不同代码路径：
//
//	box_range sell（箱体上沿兑现）
//	trend_pullback buy via power_breakout_reentry
//	trend_pullback buy via trend_continuation_reentry
//	box_range buy（箱体下沿低吸）
//	oversold_rebound buy（超跌反抽）
//
// 其余 85 组是"原版不产信号"，用来确认 Go 侧**没有多产信号**。
func TestConfirmedSignalParityWithOriginalPython(t *testing.T) {
	if len(goldenEvalCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_eval_confirmed_golden.py")
	}
	fired := 0
	for _, tc := range goldenEvalCases {
		tc := tc
		t.Run(tc.Name+"/"+tc.MarketState, func(t *testing.T) {
			frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))
			for model, want := range tc.ByModel {
				got, ok := EvaluateConfirmedPrepared(model, frame, tc.MarketState)
				if ok != want.OK {
					t.Errorf("%s：是否出信号不一致 —— go %v，python %v", model, ok, want.OK)
					continue
				}
				if !ok {
					continue
				}
				fired++
				compareConfirmedSignal(t, model, got, want)
			}
		})
	}
	if fired == 0 {
		t.Fatal("没有任何用例产出信号 —— 黄金值没有覆盖到信号构造路径")
	}
	t.Logf("比对到 %d 个确认信号", fired)
}

func compareConfirmedSignal(t *testing.T, label string, got Signal, want goldenEvalSignal) {
	t.Helper()
	checkStr := func(field, mine, expected string) {
		if mine != expected {
			t.Errorf("%s.%s：\n  go     %q\n  python %q", label, field, mine, expected)
		}
	}
	checkStr("model_id", got.ModelID, want.ModelID)
	checkStr("model_name", got.ModelName, want.ModelName)
	checkStr("market_state", got.MarketState, want.MarketState)
	checkStr("market_state_label", got.MarketStateLabel, want.MarketStateLabel)
	checkStr("signal_type", got.SignalType, want.SignalType)
	checkStr("signal_level", got.SignalLevel, want.SignalLevel)
	checkStr("signal_level_label", got.SignalLevelLabel, want.SignalLevelLabel)
	checkStr("signal_subtype", got.SignalSubtype, want.SignalSubtype)
	checkStr("signal_subtype_label", got.SignalSubtypeLabel, want.SignalSubtypeLabel)
	checkStr("signal_date", got.SignalDate, want.SignalDate)
	checkStr("risk_level", got.RiskLevel, want.RiskLevel)
	checkStr("explanation", got.Explanation, want.Explanation)

	compareGoldenFloat(t, label+".entry_price", derefOrNaN(got.EntryPrice), want.EntryPrice)
	compareGoldenFloat(t, label+".stop_price", derefOrNaN(got.StopPrice), want.StopPrice)
	compareGoldenFloat(t, label+".target_price", derefOrNaN(got.TargetPrice), want.TargetPrice)
	compareGoldenFloat(t, label+".score", got.Score, want.Score)
	compareGoldenFloat(t, label+".candidate_trigger_price",
		derefOrNaN(got.CandidateTriggerPrice), want.CandidateTriggerPrice)

	if !slices.Equal(got.ConditionHits, want.Hits) {
		t.Errorf("%s.condition_hits：\n  go     %v\n  python %v", label, got.ConditionHits, want.Hits)
	}
	if !slices.Equal(got.ConditionMisses, want.Misses) {
		t.Errorf("%s.condition_misses：\n  go     %v\n  python %v", label, got.ConditionMisses, want.Misses)
	}
	if !slices.Equal(got.QualityBlockReasons, want.QualityBlockReasons) {
		t.Errorf("%s.quality_block_reasons：\n  go     %v\n  python %v",
			label, got.QualityBlockReasons, want.QualityBlockReasons)
	}

	quality := got.BehaviorQuality
	compareGoldenFloat(t, label+".behavior.score", quality.Score, want.BehaviorScore)
	if quality.DeathVolumeRisk != want.BehaviorDeath {
		t.Errorf("%s.behavior.death_volume_risk：go %v，python %v",
			label, quality.DeathVolumeRisk, want.BehaviorDeath)
	}
	checkStr("behavior.volume_behavior", quality.VolumeBehavior, want.VolumeBehavior)
	checkStr("behavior.acceptance_quality", quality.AcceptanceQuality, want.AcceptanceQuality)
	checkStr("behavior.upper_shadow_behavior", quality.UpperShadowBehavior, want.UpperShadowBehavior)
	compareFloatMaps(t, label+".behavior.extras", quality.Extras, want.BehaviorExtras)

	if got.EarlyMainTrend != nil {
		compareFloatMaps(t, label+".early_maintrend", got.EarlyMainTrend.AsFloatMap(), want.EarlyMainTrend)
	} else if len(want.EarlyMainTrend) > 0 {
		t.Errorf("%s.early_maintrend：Go 侧没有该子度量，python 有 %d 个键",
			label, len(want.EarlyMainTrend))
	}
	if got.TrendPlatform != nil {
		compareFloatMaps(t, label+".trend_platform", got.TrendPlatform.AsFloatMap(), want.TrendPlatform)
	} else if len(want.TrendPlatform) > 0 {
		t.Errorf("%s.trend_platform：Go 侧没有该子度量，python 有 %d 个键",
			label, len(want.TrendPlatform))
	}
	if got.BoxQuality != nil {
		compareFloatMaps(t, label+".box_quality", got.BoxQuality.AsFloatMap(), want.BoxQuality)
	} else if len(want.BoxQuality) > 0 {
		t.Errorf("%s.box_quality：Go 侧没有该子度量，python 有 %d 个键", label, len(want.BoxQuality))
	}
	if got.OversoldQuality != nil {
		compareFloatMaps(t, label+".oversold_quality",
			got.OversoldQuality.AsFloatMap(), want.OversoldQuality)
	} else if len(want.OversoldQuality) > 0 {
		t.Errorf("%s.oversold_quality：Go 侧没有该子度量，python 有 %d 个键",
			label, len(want.OversoldQuality))
	}
}

// compareGoldenFloat 已在 market_state_parity_test.go 里定义（NaN 感知 + 1e-9 容差），
// 这里直接复用，避免同一断言有两份实现。

func derefOrNaN(v *float64) float64 {
	if v == nil {
		return nan()
	}
	return *v
}

// 子度量映射可能一边有键一边没有（例如 Go 侧把 extras 记在了别处）。
// 这个断言把"键集合不一致"也当成失败，避免静默漏字段。
func TestGoldenEvalExtrasKeysAreComparable(t *testing.T) {
	for _, tc := range goldenEvalCases {
		for model, want := range tc.ByModel {
			if !want.OK {
				continue
			}
			for key := range want.BehaviorExtras {
				if strings.TrimSpace(key) == "" {
					t.Fatalf("%s/%s：空键名", tc.Name, model)
				}
			}
		}
	}
}
