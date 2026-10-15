package service

import (
	"encoding/json"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// 本文件是**性能基线**，不是功能测试。
//
// 为什么留着一组"只打日志、断言很少"的用例：多期限性价比评估的耗时结构
// 与直觉相反，我先后猜错过两次（以为是"逐只评估"、以为是"序列化"），
// 量了才知道是"加载数据湖"。留着它们，下次谁改动这部分都能一眼看出
// 钱花在哪、有没有退化。
//
// 数据湖不可用时全部 skip。

// TestProfileValuationCost 量清楚"慢在哪"：后端各阶段 vs 传给前端的字节数。
//
// 结论（2026-10-15 实测）：后端 28.5 秒里 21 秒是"加载数据湖"；
// JSON 只有 2.7 MB、序列化 9 毫秒 —— **不是传输问题**。
func TestProfileValuationCost(t *testing.T) {
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
	t.Logf("=== 后端总计 %s ===", time.Since(started).Round(time.Millisecond))

	var sum int64
	for _, phase := range report.PhaseTimings {
		t.Logf("   %-18s %6d ms", phase.Name, phase.MS)
		sum += phase.MS
	}
	t.Logf("   阶段合计 %d ms（其余为组装与返回）", sum)

	t.Logf("=== 结果规模 ===")
	for _, ranking := range report.Rankings {
		t.Logf("    %-8s rows=%-5d excluded=%-4d", ranking.Horizon.Key, len(ranking.Rows), len(ranking.Excluded))
	}
	t.Logf("  行业 %d 个 / 指数 %d 个 / 行业分布 %d 行 / notes %d 条",
		len(report.Industries), len(report.Indices), len(report.IndustryDistribution), len(report.Notes))

	encode := time.Now()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	t.Logf("=== 序列化 %.2f MB，耗时 %s ===",
		float64(len(payload))/1024/1024, time.Since(encode).Round(time.Millisecond))

	parts := map[string]any{
		"rankings":             report.Rankings,
		"industries":           report.Industries,
		"indices":              report.Indices,
		"industryDistribution": report.IndustryDistribution,
		"notes":                report.Notes,
	}
	for name, part := range parts {
		raw, _ := json.Marshal(part)
		t.Logf("  %-20s %8.0f KB  (%.1f%%)", name, float64(len(raw))/1024,
			float64(len(raw))/float64(len(payload))*100)
	}
	if len(report.Rankings) > 0 && len(report.Rankings[0].Rows) > 0 {
		raw, _ := json.Marshal(report.Rankings[0].Rows[0])
		t.Logf("  单个 RankRow %.2f KB —— 5 档 × N 行就是它乘出来的", float64(len(raw))/1024)
	}
	_ = valuation.ModelVersion
}

// TestProfileLoadPhases 量"加载数据湖"的一次性成本，以及缓存命中后的成本。
//
// ⚠ 第一次必然是 15~22 秒（703 万行行情 + 652 万行财报 + 8000 个复权因子文件），
// 这是评估所需数据量的下限，缓存改变不了它。缓存改变的是**后续每一次**：
// 改评估范围、切回页面、点重新评估都走缓存。
func TestProfileLoadPhases(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	started := time.Now()
	data, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	t.Logf("① 冷读（一次性）%s", time.Since(started).Round(time.Millisecond))
	t.Logf("   行情标的 %d，财报标的 %d，股本标的 %d",
		len(data.bars), len(data.reports), len(data.shares))

	started = time.Now()
	second, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("第二次失败: %v", err)
	}
	cost := time.Since(started)
	t.Logf("② 缓存命中 %s（同一份数据 %v）", cost.Round(time.Microsecond), second == data)
	if second != data {
		t.Error("第二次应命中数据集缓存并返回同一个对象")
	}
	if cost > 500*time.Millisecond {
		t.Errorf("第二次花了 %s —— 数据集缓存没命中", cost)
	}
}

// TestProfileSnapshotUniverse 量"全市场行业基准"那一步（应当只有几十毫秒）。
//
// 这里踩过一个坑值得留着：`snapshotUniverse` 曾经按"评估范围"给标的建
// 完整价格序列（1400 根 Bar），于是选「全市场」时会给**全部 5381 只**都建一遍，
// 实测 73ms → 4063ms。现在一律走轻量画像，这个用例就是它的性能哨兵。
func TestProfileSnapshotUniverse(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)
	params := valuation.DefaultParams()

	data, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	started := time.Now()
	universe := svc.snapshotUniverse(data, params)
	cost := time.Since(started)
	t.Logf("全市场行业基准 %s（%d 只 / %d 行业）",
		cost.Round(time.Millisecond), len(universe.byCode), len(universe.groups))
	if cost > 2*time.Second {
		t.Errorf("全市场行业基准花了 %s —— 是不是又给全部标的建价格序列了？", cost)
	}

	// 行业历史序列
	started = time.Now()
	history := svc.industryHistory(data, universe, params)
	t.Logf("行业历史序列重建 %s（%d 个行业有序列）",
		time.Since(started).Round(time.Millisecond), len(history))
}

// TestProfilePhaseDetail 在**真实流程**里细分"全市场行业基准"那 5 秒。
//
// 上一个用例已经证明 `buildSnapshotUniverse` 本身只要 ~150ms，
// 所以那 5 秒必然在别处 —— 这个用例把 `snapshotUniverse`、`industryHistory`、
// `buildIndustryAggregates` 各量一遍，把它们从"阶段"里拆出来。
func TestProfilePhaseDetail(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)
	params := valuation.DefaultParams()

	data, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	codes := svc.selectValuationCodes(data, 0)
	t.Logf("全市场候选 %d 只", len(codes))

	started := time.Now()
	universe := svc.snapshotUniverse(data, params)
	t.Logf("① snapshotUniverse      %8s（%d 只 / %d 行业）",
		time.Since(started).Round(time.Millisecond), len(universe.byCode), len(universe.groups))

	started = time.Now()
	history := svc.industryHistory(data, universe, params)
	t.Logf("② industryHistory       %8s（%d 个行业有序列）",
		time.Since(started).Round(time.Millisecond), len(history))

	// 第二次（应命中缓存）
	started = time.Now()
	_ = svc.industryHistory(data, universe, params)
	t.Logf("②b industryHistory 缓存 %8s", time.Since(started).Round(time.Microsecond))

	started = time.Now()
	industries, members := svc.buildIndustryAggregates(universe.byCode, universe.groups, history, len(codes), params)
	t.Logf("③ buildIndustryAggregates %8s（%d 个行业 / %d 组成分）",
		time.Since(started).Round(time.Millisecond), len(industries), len(members))

	// 逐只评估
	started = time.Now()
	evaluations := make([]stockEvaluation, 0, len(codes))
	for _, code := range codes {
		eval, ok := svc.evaluateOneStock(data, code, params, false)
		if !ok {
			continue
		}
		evaluations = append(evaluations, eval)
	}
	t.Logf("④ 逐只评估（串行）      %8s（%d 只）",
		time.Since(started).Round(time.Millisecond), len(evaluations))

	// 评分与排名
	started = time.Now()
	rankInputs := map[string][]valuation.RankInput{}
	for _, horizon := range valuation.Horizons {
		rankInputs[horizon.Key] = make([]valuation.RankInput, 0, len(evaluations))
	}
	for i := range evaluations {
		eval := &evaluations[i]
		industry := industries[eval.company.IndustryCode]
		industryMembers := members[eval.company.IndustryCode]
		peers, peerNote := valuation.SelectComparables(eval.company, industryMembers, params)
		relative := valuation.EvaluateRelativeValuation(eval.company, industry, industryMembers, peers, params)
		relative.ComparableNote = peerNote
		fair := svc.applyIndustry(*eval, industry, params)
		sharePoint, _ := eval.shares.Latest()
		score, scoreErr := valuation.EvaluateScore(valuation.ScoreInput{
			Code: eval.code, Name: eval.name, Industry: industry, Self: eval.self,
			Relative: relative, Growth: eval.growth, Quality: eval.quality, AsOf: eval.asOf,
			Company: eval.company, Price: eval.series.LastClose(), Shares: sharePoint.TotalShares,
			Volatility: eval.volatility, PaidLast12M: data.dividendPaid[eval.code],
			PriceLength: eval.series.Len(), PriceAdjusted: eval.series.Adjusted,
			Confidence: eval.confidence, FairByHorizon: fair, Params: params,
		})
		if scoreErr != nil {
			continue
		}
		for _, horizonScore := range score.Scores {
			rankInputs[horizonScore.Horizon.Key] = append(rankInputs[horizonScore.Horizon.Key],
				valuation.RankInput{Code: eval.code, Name: eval.name, Score: horizonScore, Stock: score})
		}
	}
	t.Logf("⑤ 评分（串行）          %8s", time.Since(started).Round(time.Millisecond))

	started = time.Now()
	for _, horizon := range valuation.Horizons {
		_ = valuation.BuildRanking(horizon, rankInputs[horizon.Key])
	}
	t.Logf("⑥ 排名                  %8s", time.Since(started).Round(time.Millisecond))
}

// TestProfileCachedPath 量用户实际感受到的四个路径。
//
// 这条用例是这次优化的**验收指标**：冷启动慢一次可以接受（评估要读全市场
// 5 年的行情与财报），但改范围、切回页面必须是瞬时。
func TestProfileCachedPath(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	cold := time.Now()
	first, err := svc.GetValuationReport(150, true)
	if err != nil {
		t.Fatalf("冷启动失败: %v", err)
	}
	coldCost := time.Since(cold)
	var coldLoad int64
	for _, phase := range first.PhaseTimings {
		if phase.Name == "加载数据湖" {
			coldLoad = phase.MS
		}
	}
	t.Logf("① 冷启动          %8s（其中加载数据湖 %d ms）", coldCost.Round(time.Millisecond), coldLoad)

	second := time.Now()
	if _, err := svc.GetValuationReport(150, true); err != nil {
		t.Fatalf("重算失败: %v", err)
	}
	warmCost := time.Since(second)
	t.Logf("② 同范围重算      %8s", warmCost.Round(time.Millisecond))

	third := time.Now()
	report, err := svc.GetValuationReport(80, true)
	if err != nil {
		t.Fatalf("改范围失败: %v", err)
	}
	rangeCost := time.Since(third)
	var rangeLoad int64
	for _, phase := range report.PhaseTimings {
		if phase.Name == "加载数据湖" {
			rangeLoad = phase.MS
		}
	}
	t.Logf("③ 改评估范围      %8s（其中加载数据湖 %d ms）", rangeCost.Round(time.Millisecond), rangeLoad)

	fourth := time.Now()
	if _, err := svc.GetValuationReport(80, false); err != nil {
		t.Fatalf("命中失败: %v", err)
	}
	hitCost := time.Since(fourth)
	t.Logf("④ 完全命中缓存    %8s", hitCost.Round(time.Millisecond))

	if rangeLoad > 200 {
		t.Errorf("改评估范围时仍在重读数据湖（%d ms）—— 数据集缓存没生效", rangeLoad)
	}
	if hitCost > 200*time.Millisecond {
		t.Errorf("命中报告缓存仍花了 %s —— 缓存没生效", hitCost)
	}
	if warmCost > coldCost/2 {
		t.Errorf("同范围重算 %s 应显著快于冷启动 %s —— 数据集缓存没生效", warmCost, coldCost)
	}
}
