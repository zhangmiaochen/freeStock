package service

import (
	"testing"
	"time"

	"freestock/internal/cnlake"
)

// TestImpliedExpectationRealData 在真实数据上看"市场隐含预期"是否合理。
//
// 这条用例的作用是**验收这一轮新机制**：它必须能区分出"市场要求远高于自身能力"
// 与"市场要求低于自身能力"的股票，而不是所有股票都给同一个结论
// （上一轮实测过 `impliedYears` 因定义问题对任何输入都给"≥20 年"，
//
//	那种"所有输入同一输出"的指标是没有用的）。
func TestImpliedExpectationRealData(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	started := time.Now()
	report, err := svc.GetValuationReport(150, true)
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	t.Logf("评估耗时 %s", time.Since(started).Round(time.Millisecond))

	if len(report.Rankings) == 0 {
		t.Fatal("没有排名结果")
	}

	verdicts := map[string]int{}
	fulfillments := map[string]int{}
	durations := []float64{}
	withImplied := 0
	overAndLow := 0

	for _, row := range report.Rankings[3].Rows { // 12 个月档
		detail, err := svc.GetStockValuation(row.Code)
		if err != nil || detail == nil || !detail.Available {
			continue
		}
		implied := detail.Stock.FairByHorizon["12m"].Implied
		if !implied.Available {
			continue
		}
		withImplied++
		verdicts[implied.Verdict]++
		fulfillments[implied.Fulfillment]++
		durations = append(durations, implied.ImpliedYears)
		if implied.Verdict == "over" && (implied.Fulfillment == "low" || implied.Fulfillment == "medium") {
			overAndLow++
		}
		if withImplied <= 8 {
			t.Logf("%s %s | 隐含增速 %.1f%% 需持续 %.1f 年 | 自身 %.1f%%（%s）| gap %+.1f%% | 兑现 %s | 落空 %+.1f%% | %s",
				row.Code, row.Name,
				implied.ImpliedGrowth*100, implied.ImpliedYears,
				implied.AchievableGrowth*100, implied.AchievableBasis,
				implied.GrowthGap*100, implied.Fulfillment,
				implied.DownsideIfMiss*100, implied.Verdict)
		}
	}

	if withImplied == 0 {
		t.Fatal("没有任何标的算出隐含预期 —— 新机制没生效（或全部因现金流为负被跳过）")
	}
	t.Logf("可反解 %d 只；结论分布 over/fair/under = %d/%d/%d；兑现分布 high/medium/low = %d/%d/%d",
		withImplied, verdicts["over"], verdicts["fair"], verdicts["under"],
		fulfillments["high"], fulfillments["medium"], fulfillments["low"])
	t.Logf("隐含持续时间：%d 个样本，全部落在 [%.1f, %.1f] 年",
		len(durations), minOf(durations), maxOf(durations))
	t.Logf("其中 over 且兑现非高 的有 %d 只（会被新风险码命中）", overAndLow)

	// ① 结论必须有区分度：不能全部落进同一档
	if len(verdicts) < 2 {
		t.Errorf("结论只有一个档位（%v）—— 这个机制没有区分力", verdicts)
	}
	// ② 持续时间必须有区分度（这是上一轮踩过的坑）
	if maxOf(durations) <= minOf(durations) {
		t.Errorf("隐含持续时间对所有样本都是 %.1f 年 —— 定义又退化成常数了", maxOf(durations))
	}
}

func minOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	out := values[0]
	for _, v := range values {
		if v < out {
			out = v
		}
	}
	return out
}

func maxOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	out := values[0]
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}
