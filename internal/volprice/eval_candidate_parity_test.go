package volprice

import (
	"slices"
	"testing"
)

// **候选信号与原版 Python 的逐字段比对。**
//
// 黄金值由 `testdata/gen_eval_candidate_golden.py` 生成（直接 import 原版
// `volume_price_models._evaluate_candidate_prepared`）。10 种形态 × 3 种市场状态
// × 3 个模型 = 90 组，其中 7 组会产出候选信号，覆盖三个候选入口：
//
//	trend_pullback candidate_buy via trend_continuation_reentry（3 组）
//	box_range candidate_buy（3 组）
//	oversold_rebound candidate_buy（1 组）
//
// 候选是"买点观察位"的主要来源，比确认信号多得多，所以这一层必须与确认层一样严。
func TestCandidateSignalParityWithOriginalPython(t *testing.T) {
	if len(goldenCandidateCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_eval_candidate_golden.py")
	}
	fired := 0
	for _, tc := range goldenCandidateCases {
		tc := tc
		t.Run(tc.Name+"/"+tc.MarketState, func(t *testing.T) {
			frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))
			for model, want := range tc.ByModel {
				got, ok := EvaluateCandidatePrepared(model, frame, tc.MarketState)
				if ok != want.OK {
					t.Errorf("%s：是否出候选不一致 —— go %v，python %v", model, ok, want.OK)
					continue
				}
				if !ok {
					continue
				}
				fired++
				compareCandidateSignal(t, model, got, want)
			}
		})
	}
	if fired == 0 {
		t.Fatal("没有任何用例产出候选信号 —— 黄金值没有覆盖到候选构造路径")
	}
	t.Logf("比对到 %d 个候选信号", fired)
}

func compareCandidateSignal(t *testing.T, label string, got Signal, want goldenCandidateSignal) {
	t.Helper()
	checkStr := func(field, mine, expected string) {
		if mine != expected {
			t.Errorf("%s.%s：\n  go     %q\n  python %q", label, field, mine, expected)
		}
	}
	checkStr("model_id", got.ModelID, want.ModelID)
	checkStr("market_state", got.MarketState, want.MarketState)
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

	// 候选专属字段
	if got.HitCount != want.HitCount {
		t.Errorf("%s.hit_count：go %d，python %d", label, got.HitCount, want.HitCount)
	}
	if got.TotalConditions != want.TotalConditions {
		t.Errorf("%s.total_conditions：go %d，python %d", label, got.TotalConditions, want.TotalConditions)
	}
	compareGoldenFloat(t, label+".hit_ratio", got.HitRatio, want.HitRatio)
	if got.ConfirmHitCount != want.ConfirmHitCount {
		t.Errorf("%s.confirmation_hit_count：go %d，python %d", label, got.ConfirmHitCount, want.ConfirmHitCount)
	}
	if got.ConfirmTotal != want.ConfirmTotal {
		t.Errorf("%s.confirmation_total_conditions：go %d，python %d", label, got.ConfirmTotal, want.ConfirmTotal)
	}
	compareGoldenFloat(t, label+".confirmation_progress", got.ConfirmProgress, want.ConfirmProgress)
	compareGoldenFloat(t, label+".loose_entry_score", got.LooseEntryScore, want.LooseEntryScore)
	if got.LooseEntryAllowed != want.LooseEntryAllowed {
		t.Errorf("%s.loose_entry_allowed：go %v，python %v", label, got.LooseEntryAllowed, want.LooseEntryAllowed)
	}
	compareGoldenFloat(t, label+".candidate_trigger_price",
		derefOrNaN(got.CandidateTriggerPrice), want.CandidateTriggerPrice)

	compareStringSlices(t, label+".condition_hits", got.ConditionHits, want.Hits)
	compareStringSlices(t, label+".condition_misses", got.ConditionMisses, want.Misses)
	compareStringSlices(t, label+".confirmation_hits", got.ConfirmationHits, want.ConfirmationHits)
	compareStringSlices(t, label+".confirmation_misses", got.ConfirmationMiss, want.ConfirmationMisses)
	compareStringSlices(t, label+".missing_core", got.MissingCore, want.MissingCore)
	compareStringSlices(t, label+".loose_entry_block_reasons", got.LooseBlockReasons, want.LooseBlockReasons)
	compareStringSlices(t, label+".quality_block_reasons", got.QualityBlockReasons, want.QualityBlockReasons)

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

	if got.TrendPlatform != nil {
		compareFloatMaps(t, label+".trend_platform", got.TrendPlatform.AsFloatMap(), want.TrendPlatform)
	} else if len(want.TrendPlatform) > 0 {
		t.Errorf("%s.trend_platform：Go 侧没有该子度量，python 有 %d 个键", label, len(want.TrendPlatform))
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
		t.Errorf("%s.oversold_quality：Go 侧没有该子度量，python 有 %d 个键", label, len(want.OversoldQuality))
	}
}

// compareStringSlices 把"两个都空"和"内容相同"都算通过（nil 与空切片等价）。
func compareStringSlices(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s：\n  go     %v\n  python %v", label, got, want)
	}
}

// 确认层与候选层不能互相污染：同一根 K 线、同一模型，两层各自的结论要独立成立。
func TestConfirmedAndCandidateLayersAreIndependent(t *testing.T) {
	for _, tc := range goldenCandidateCases {
		frame := AttachVectorBarsColumns(barsFromGolden(tc.Bars))
		for model := range tc.ByModel {
			confirmed, confirmedOK := EvaluateConfirmedPrepared(model, frame, tc.MarketState)
			candidate, candidateOK := EvaluateCandidatePrepared(model, frame, tc.MarketState)
			if confirmedOK && confirmed.SignalType != "buy" && confirmed.SignalType != "sell" {
				t.Errorf("%s/%s：确认层信号类型应为 buy 或 sell，实际 %q",
					tc.Name, model, confirmed.SignalType)
			}
			if candidateOK && candidate.SignalType != "candidate_buy" {
				t.Errorf("%s/%s：候选层信号类型应为 candidate_buy，实际 %q",
					tc.Name, model, candidate.SignalType)
			}
			if confirmedOK && candidateOK && confirmed.SignalLevel == candidate.SignalLevel {
				t.Errorf("%s/%s：两层信号的 level 不应相同（都是 %q）",
					tc.Name, model, confirmed.SignalLevel)
			}
		}
	}
}
