package valuation

import (
	"math"
	"testing"
)

// 本文件钉住方案里最容易被"实现成看起来对"的几条硬要求：
//
//   - §7.2 / 验收 5：五档期限必须真正独立（参数不同、结果不同）
//   - §7.1 / 验收 6：同一信息不能被重复计分
//   - §5.6：安全边际与潜在收益口径不同，不能混用
//   - §7.3：风险约束不能静默修改原始估值

// makeScoreInput 造一个"数据齐备"的评分输入。
//
// 数值刻意选得像一家中等的制造业公司：PE 18 倍、ROE 12%、成长 10%、
// 质量良好 —— 这样各维度分数都在中间附近，任何"复制粘贴"的错误都容易暴露。
func makeScoreInput() ScoreInput {
	asOf := buildHealthyAsOf()
	company := CompanyValuation{
		Code: "600000", Name: "示例公司",
		IndustryCode: "BK0475", IndustryName: "示例行业",
		MarketCap: 5e10, FloatMarketCap: 4e10,
		PE: 18, PB: 2.2, PS: 2.0,
		NetProfit: 2.78e9, Revenue: 2.5e10, Equity: 2.27e10,
		ROE: 0.122, GrowthScore: 62, QualityScore: 74, DebtRatio: 0.45,
		RevenueYoY: 0.10, ProfitYoY: 0.08,
		SelfPercentile: 35, SelfAvailable: true,
	}
	industry := IndustryValuation{
		Code: "BK0475", Name: "示例行业",
		WeightedPE: 20, WeightedPB: 2.4, WeightedPS: 2.2,
		MedianPE: 18.5, MedianPB: 2.3, MedianPS: 2.1,
		PEPercentile: 42, PBPercentile: 45,
		Members: 40, LossMembers: 4, LossRatio: 0.10,
		ROEMedian: 0.11, RevenueYoYMed: 0.09, ProfitYoYMed: 0.07,
		Top3Weight: 0.28, PEExTop3: 17.8,
		Stage: "mature", StageNote: "成熟阶段",
		Available: true, Score: 62,
	}
	self := SelfValuation{
		Available: true,
		Current:   ValuationPoint{Date: 20261014, PE: 18, PB: 2.2, PS: 2.0, MarketCap: 5e10},
		CurrentPE: 18, CurrentPB: 2.2, CurrentPS: 2.0,
		PEPercentile3Y: 32, PEPercentile5Y: 35,
		PBPercentile3Y: 38, PBPercentile5Y: 40, PSPercentile5Y: 44,
		SampleDays3Y: 730, SampleDays5Y: 1220,
		Score: 62,
	}
	relative := RelativeValuation{
		Available: true, IndustryCode: "BK0475", IndustryName: "示例行业",
		PremiumVsWeighted: -0.10, PremiumVsMedian: -0.027,
		PEPercentileInIndustry: 45, PBPercentileInIndustry: 46,
		GrowthPercentileInIndustry: 58, QualityPercentileInIndustry: 66,
		ROEPercentileInIndustry: 55,
		Quadrant:                QuadrantCheapGood,
		QuadrantLabel:           "估值折价 + 基本面优于行业 → 可能是估值折价机会",
		QuadrantReason:          "相对行业中位数折价 2.7%，基本面行业分位 62",
		Comparables:             12,
		ComparableNote:          "同行样本 12 只",
		Score:                   58,
	}
	growth := Growth{
		Available: true, Score: 62,
		RevenueYoY: 0.10, ProfitYoY: 0.08, DeductedYoY: 0.07,
		RevenueCAGR3: 0.11, RevenueCAGR5: 0.09, ProfitCAGR3: 0.10,
		RevenueYoYSeries:   []float64{0.09, 0.12, 0.11, 0.10},
		ProfitYoYSeries:    []float64{0.07, 0.09, 0.08, 0.08},
		Consistency:        1.0,
		VolatilityOfGrowth: 0.012,
		OCFGrowth:          0.09,
		FCFPositive:        true,
		ROE:                0.122, IncrementalROIC: 0.14,
	}
	quality := Quality{
		Available: true, Score: 74,
		DeductedRatio: 0.94, CashContent: 1.15, DebtRatio: 0.45,
		DebtChange: 0.01, FinancingDependence: -0.01,
		MainBusinessRatio: 0.96, GrossMargin: 0.32, GrossMarginChange: 0.005,
	}
	confidence := Confidence{
		Score:   82,
		Level:   "high",
		Reasons: []string{"区间宽度、方法一致性、数据覆盖三项均正常，未扣分"},
	}
	params := DefaultParams()

	// 五档合理价值：期限越长，合理价值越高（成长在长期释放）
	fairByHorizon := map[string]FairValue{}
	for _, horizon := range Horizons {
		base := 24.0 + float64(horizon.TradingDays)/252*1.0
		fairByHorizon[horizon.Key] = FairValue{
			Available:    true,
			Price:        22.0,
			Shares:       1e9,
			Pessimistic:  base * 0.75,
			Base:         base,
			Optimistic:   base * 1.30,
			SafetyMargin: (base - 22.0) / base,
			Upside:       (base * 1.30 / 22.0) - 1,
			MethodsUsed:  []Method{MethodPE, MethodPB},
			Confidence:   confidence,
			ModelGrowth:  0.08,
		}
	}

	return ScoreInput{
		Code: "600000", Name: "示例公司",
		Industry: industry, Self: self, Relative: relative,
		Growth: growth, Quality: quality,
		AsOf: asOf, Company: company,
		Price: 22.0, Shares: 1e9,
		Volatility: 0.32, PaidLast12M: 0.6,
		PriceLength: 1200, PriceAdjusted: true,
		Confidence:    confidence,
		FairByHorizon: fairByHorizon,
		Params:        params,
	}
}

// buildHealthyAsOf 造一个数据齐备的财务时点视图。
func buildHealthyAsOf() AsOf {
	asOf := AsOf{
		EvalDate:     20261014,
		LatestPeriod: "2026Q2",
		Items: map[string]float64{
			ItemRevenue:          2.5e10,
			ItemOperatingCost:    1.7e10,
			ItemOperatingProfit:  3.4e9,
			ItemTotalProfit:      3.5e9,
			ItemNetProfit:        2.78e9,
			ItemNetProfitDeduct:  2.61e9,
			ItemIncomeTax:        0.7e9,
			ItemTotalAssets:      5.0e10,
			ItemTotalLiabilities: 2.25e10,
			ItemTotalEquity:      2.27e10,
			ItemRoe:              0.121,
			ItemGrossMargin:      32.0,
			ItemNetCashOperate:   3.2e9,
			ItemNetCashFinance:   -0.5e9,
			ItemCapex:            1.4e9,
		},
		Quarters: 20,
	}
	asOf.TTM = TTMValues{
		Revenue: 2.5e10, OperatingCost: 1.7e10, OperatingProfit: 3.4e9,
		NetProfit: 2.78e9, NetProfitDeduct: 2.61e9,
		CashOperate: 3.2e9, Capex: 1.4e9, FreeCashFlow: 1.8e9,
		CashOperateQuarters: 4, CapexQuarters: 4,
	}
	return asOf
}

// TestHorizonsUseDifferentParameters 钉住"五档权重不同"这一要求本身。
//
// 这是"不允许简单复制同一个评分"的**配置层**保证：
// 如果五档权重完全相同，即使代码里循环了五遍，出来的也是同一个分。
func TestHorizonsUseDifferentParameters(t *testing.T) {
	params := DefaultParams().Sanitize()

	// 每档权重之和必须归一化到 100
	for _, horizon := range Horizons {
		weights := params.WeightsFor(horizon.Key)
		sum := 0.0
		for _, dimension := range ScoreDimensions {
			if _, ok := weights[dimension]; !ok {
				t.Fatalf("%s 期限缺少维度 %s 的权重", horizon.Key, dimension)
			}
			sum += weights[dimension]
		}
		if math.Abs(sum-100) > 0.01 {
			t.Errorf("%s 期限权重之和 = %.2f，应归一化到 100", horizon.Key, sum)
		}
	}

	// 五档权重表两两不得相同
	keys := HorizonKeys()
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			left, right := params.WeightsFor(keys[i]), params.WeightsFor(keys[j])
			same := true
			for _, dimension := range ScoreDimensions {
				if math.Abs(left[dimension]-right[dimension]) > 1e-9 {
					same = false
					break
				}
			}
			if same {
				t.Errorf("%s 与 %s 的权重表完全相同 —— 方案明确禁止把同一套权重套到所有期限", keys[i], keys[j])
			}
		}
	}

	// 长期限的成长权重必须高于短期限（这是权重的设计意图，不是巧合）
	if params.WeightsFor("36m")[DimGrowth] <= params.WeightsFor("1m")[DimGrowth] {
		t.Error("36 个月的成长权重应高于 1 个月 —— 期限越长，价值越由公司成长决定")
	}
	// 短期限的估值与安全边际权重必须高于长期限
	if params.WeightsFor("1m")[DimSelfValuation] <= params.WeightsFor("36m")[DimSelfValuation] {
		t.Error("1 个月的自身估值权重应高于 36 个月 —— 短期价格主要由估值波动驱动")
	}
	if params.WeightsFor("1m")[DimMargin] <= params.WeightsFor("36m")[DimMargin] {
		t.Error("1 个月的安全边际权重应高于 36 个月")
	}
}

// TestHorizonWeightsNotAllSameForScenarioProb 钉住情景概率也按期限不同。
func TestScenarioProbDependsOnHorizon(t *testing.T) {
	params := DefaultParams().Sanitize()
	for _, horizon := range Horizons {
		prob := params.ScenarioProbFor(horizon.Key)
		sum := prob[0] + prob[1] + prob[2]
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("%s 期限情景概率之和 = %.4f，应为 1", horizon.Key, sum)
		}
	}
	if params.ScenarioProbFor("36m")[1] <= params.ScenarioProbFor("1m")[1] {
		t.Error("36 个月的基准情景概率应不低于 1 个月（长期中枢回归更明显）")
	}
}

// TestHorizonsAreNotCopies 是验收标准 5 的直接检验：
// **五档期限的评分不能完全相同**。
func TestHorizonsAreNotCopies(t *testing.T) {
	input := makeScoreInput()
	score, err := EvaluateScore(input)
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if len(score.Scores) != 5 {
		t.Fatalf("应产出 5 档期限，实际 %d", len(score.Scores))
	}

	// ① 各档的充分性：分数、目标价、预期收益、悲观损失都必须有差异
	for i := 0; i < len(score.Scores); i++ {
		for j := i + 1; j < len(score.Scores); j++ {
			left, right := score.Scores[i], score.Scores[j]
			if !left.Outlook.Available || !right.Outlook.Available {
				t.Fatalf("%s 或 %s 的收益情景不可用，无法检验独立性", left.Horizon.Key, right.Horizon.Key)
			}
			if nearlyEqual(left.FinalScore, right.FinalScore, 1e-9) &&
				nearlyEqual(left.Outlook.TargetPrice, right.Outlook.TargetPrice, 1e-9) {
				t.Errorf("%s 与 %s 的评分与目标价完全相同 —— 五档没有独立计算",
					left.Horizon.Key, right.Horizon.Key)
			}
			if nearlyEqual(left.Outlook.Convergence, right.Outlook.Convergence, 1e-9) {
				t.Errorf("%s 与 %s 的向合理价值收敛比例相同", left.Horizon.Key, right.Horizon.Key)
			}
		}
	}

	// ② 收敛比例必须单调递增（期限越长，越接近合理价值）
	previous := -1.0
	for _, item := range score.Scores {
		if item.Outlook.Convergence <= previous {
			t.Errorf("%s 的收敛比例 %.3f 未大于上一档 %.3f", item.Horizon.Key, item.Outlook.Convergence, previous)
		}
		previous = item.Outlook.Convergence
	}

	// ③ 该期限内的波动幅度必须随期限递增（√T 律）
	previous = -1.0
	for _, item := range score.Scores {
		if item.Outlook.ExpectedVolatility <= previous {
			t.Errorf("%s 的期限波动 %.4f 未大于上一档 %.4f", item.Horizon.Key, item.Outlook.ExpectedVolatility, previous)
		}
		previous = item.Outlook.ExpectedVolatility
	}

	// ④ 短期目标价必须低于长期目标价（合理价值随成长释放，收敛比例也更高）
	short := score.Scores[0].Outlook.TargetPrice
	long := score.Scores[len(score.Scores)-1].Outlook.TargetPrice
	if long <= short {
		t.Errorf("36 个月目标价 %.2f 应高于 1 个月目标价 %.2f", long, short)
	}
}

// TestFactorsAreNotDoubleCounted 钉住去重校验**真的会报错**。
//
// 这是验收标准 6 的核心：同一份信息不能在两个维度里各加一次分。
// 只写一条"我们做了去重"的注释是没有意义的 —— 必须有会失败的校验。
func TestFactorsAreNotDoubleCounted(t *testing.T) {
	// ① 同维度内重复 Key
	duplicated := []Factor{
		{Dimension: DimSelfValuation, Key: "pe_percentile", Score: 70, Weight: 1},
		{Dimension: DimSelfValuation, Key: "pe_percentile", Score: 70, Weight: 1},
	}
	if err := ValidateFactors(duplicated); err == nil {
		t.Error("同维度下重复的因子 Key 应当被拒绝（同一信息被重复计分）")
	}
	// ② 跨维度重复 Key —— 方案点名的情形：
	// "行业估值偏低既被重复计入行业评分、相对估值评分"
	crossDimension := []Factor{
		{Dimension: DimIndValuation, Key: "industry_cheap", Score: 80, Weight: 1},
		{Dimension: DimRelValuation, Key: "industry_cheap", Score: 80, Weight: 1},
	}
	if err := ValidateFactors(crossDimension); err == nil {
		t.Error("跨维度重复的因子 Key 应当被拒绝（同一依据被重复增加综合分）")
	}

	// ③ 归属为空
	if err := ValidateFactors([]Factor{{Key: "orphan", Score: 50, Weight: 1}}); err == nil {
		t.Error("没有归属维度的因子应当被拒绝")
	}

	// ④ 正常因子向量必须通过
	input := makeScoreInput()
	factors, err := buildFactors(input)
	if err != nil {
		t.Fatalf("正常因子向量不应报错: %v", err)
	}
	if err := ValidateFactors(factors); err != nil {
		t.Errorf("正常因子向量应通过去重校验: %v", err)
	}
	// 每个维度都必须恰好有一个因子（当前设计），且 Key 全局唯一
	byDimension := map[ScoreDimension]int{}
	for _, factor := range factors {
		byDimension[factor.Dimension]++
	}
	for _, dimension := range ScoreDimensions {
		if byDimension[dimension] != 1 {
			t.Errorf("维度 %s 有 %d 个因子，预期恰好 1 个（多因子需显式评估是否重复计分）",
				dimension, byDimension[dimension])
		}
	}
}

// TestSafetyMarginAndUpsideUseDifferentFormulas 钉住方案 §5.6：
// "安全边际与潜在收益必须分别计算，两者口径不同，不能混用"。
func TestSafetyMarginAndUpsideUseDifferentFormulas(t *testing.T) {
	price := 100.0
	base := 125.0
	optimistic := 150.0

	wantSafety := (base - price) / base // (125−100)/125 = 20%
	wantUpside := (optimistic - price) / price

	if math.Abs(wantSafety-0.20) > 1e-9 {
		t.Fatalf("测试设定错误：安全边际应为 20%%，实际 %.4f", wantSafety)
	}
	if math.Abs(wantUpside-0.50) > 1e-9 {
		t.Fatalf("测试设定错误：潜在收益应为 50%%，实际 %.4f", wantUpside)
	}
	if nearlyEqual(wantSafety, wantUpside, 1e-9) {
		t.Fatal("安全边际与潜在收益在本例中不应相等（否则测试没有区分力）")
	}

	// 用真实的 fairvalue 入口验证两个字段确实按不同公式填
	input := makeScoreInput()
	input.Price = price
	fair := EvaluateFairValue(FairValueInput{
		Company: input.Company, AsOf: input.AsOf,
		Price: price, Shares: 1e9,
		Growth: input.Growth, Quality: input.Quality,
		Industry: input.Industry, IndustryAvailable: true,
		Horizon: Horizons[3], Params: input.Params,
	})
	if !fair.Available {
		t.Fatalf("合理价值测算失败: %s", fair.Note)
	}
	wantSafetyMargin := (fair.Base - price) / fair.Base
	wantUpsideValue := (fair.Optimistic - price) / price
	if math.Abs(fair.SafetyMargin-wantSafetyMargin) > 1e-9 {
		t.Errorf("安全边际 = %.6f，应为 (基准 %.2f − 价格 %.2f) ÷ 基准 = %.6f",
			fair.SafetyMargin, fair.Base, price, wantSafetyMargin)
	}
	if math.Abs(fair.Upside-wantUpsideValue) > 1e-9 {
		t.Errorf("潜在收益 = %.6f，应为 (乐观 %.2f − 价格 %.2f) ÷ 价格 = %.6f",
			fair.Upside, fair.Optimistic, price, wantUpsideValue)
	}
}

// TestRiskConstraintDoesNotSilentlyChangeRawScore 钉住方案 §7.3：
// "不能通过风险约束静默修改原始估值结果" —— 原始分与最终分必须分开。
func TestRiskConstraintDoesNotSilentlyChangeRawScore(t *testing.T) {
	input := makeScoreInput()
	// 构造一个必然触发风险约束的场景：
	// 个股便宜（分位 12%）但盈利连续下滑 → RiskCheapButDeteriorating
	input.Self.PEPercentile5Y = 12
	input.Growth.ProfitYoYSeries = []float64{0.10, -0.05, -0.08, -0.12}
	input.Growth.RevenueYoYSeries = []float64{0.10, -0.02, -0.05, -0.09}

	score, err := EvaluateScore(input)
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	oneMonth := score.Scores[0]

	// 必须命中"估值低但盈利持续恶化"
	found := false
	for _, flag := range oneMonth.RiskFlags {
		if flag.Code == RiskCheapButDeteriorating {
			found = true
			if flag.Reason == "" {
				t.Error("风险约束必须给出具体原因（不能只降分不说明问题）")
			}
		}
	}
	if !found {
		t.Fatal("应命中「估值低但盈利持续恶化」风险约束")
	}

	// RawScore 与 FinalScore 必须同时存在且都被填上
	if oneMonth.RawScore <= 0 {
		t.Error("原始分必须被保留（不能只留约束后的分）")
	}
	if oneMonth.FinalScore <= 0 {
		t.Error("最终分必须被填上")
	}
	// 原始分不能因为风险约束而被改写：它只反映维度加权结果
	recomputedRaw := 0.0
	for _, dimension := range ScoreDimensions {
		recomputedRaw += oneMonth.Contributions[dimension]
	}
	if math.Abs(recomputedRaw-oneMonth.RawScore) > 0.01 {
		t.Errorf("原始分 %.4f 与各维度贡献之和 %.4f 不一致 —— 说明约束改写了原始分",
			oneMonth.RawScore, recomputedRaw)
	}
	// 评级确实被封顶了
	if !oneMonth.RatingCapped {
		t.Error("命中限制评级类风险时 RatingCapped 应为 true")
	}
	if RatingRank(oneMonth.Rating) > RatingRank(RatingNeutral) {
		t.Errorf("评级应被封顶到中性或更低，实际 %s", oneMonth.Rating)
	}
}

// TestUnavailableWhenKeyDataMissing 钉住验收标准 8：
// 数据不足时给"暂无法可靠评估"，而不是强行给一个中性评级。
func TestUnavailableWhenKeyDataMissing(t *testing.T) {
	input := makeScoreInput()
	input.AsOf = AsOf{} // 没有任何财报
	input.AsOf.TTM = TTMValues{}
	input.Shares = 0

	score, err := EvaluateScore(input)
	if err != nil {
		t.Fatalf("评分不应报错（应返回不可评估状态）: %v", err)
	}
	if score.Available {
		t.Error("关键数据缺失时 Available 应为 false")
	}
	if score.UnavailableReason == "" {
		t.Error("不可评估时必须说明原因")
	}
}

// TestPercentileIsLowWhenValueIsLow 钉住分位口径。
func TestPercentileIsLowWhenValueIsLow(t *testing.T) {
	samples := make([]float64, 0, 200)
	for i := 1; i <= 200; i++ {
		samples = append(samples, float64(i))
	}
	low, ok := Percentile(samples, 10, 120)
	if !ok || low > 5 {
		t.Errorf("值 10 在 1~200 的样本里分位应接近 4.75，实际 %.2f (ok=%v)", low, ok)
	}
	mid, _ := Percentile(samples, 100, 120)
	if math.Abs(mid-50) > 1 {
		t.Errorf("中位值分位应约 50，实际 %.2f", mid)
	}
	high, _ := Percentile(samples, 195, 120)
	if high < 95 {
		t.Errorf("高值分位应 > 95，实际 %.2f", high)
	}
	// 样本不足必须返回 false，而不是给一个不可靠的分位
	if _, ok := Percentile(samples[:10], 5, 120); ok {
		t.Error("样本不足时应返回 false")
	}
}

// TestFairPEDoesNotRewardGrowthAlone 钉住方案 §5.4：
// "不能简单把高增长率直接转换成高估值" —— 同等增速下，质量差的公司目标 PE 必须更低。
//
// ⚠ 本轮口径变了：目标 PE 现在**锚定市场实际倍数**（见 fairPE）。
// 这条用例的作用相应改成两件事：
//  1. 同一锚下，质量差的拿到更低的目标倍数（原有的相对关系不变）；
//  2. 目标倍数**不许脱离锚** —— 这是修掉"银行被给出 20 倍目标 PE"的关键约束。
func TestFairPEDoesNotRewardGrowthAlone(t *testing.T) {
	const anchor = 20.0
	highQuality, _ := fairPE(100, 100, anchor)
	lowQuality, _ := fairPE(100, 0, anchor)
	if lowQuality >= highQuality {
		t.Errorf("同样成长分 100，质量分 0 的目标 PE（%.1f）必须低于质量分 100 的（%.1f）",
			lowQuality, highQuality)
	}
	if highQuality > 40 {
		t.Errorf("目标 PE 上限应为 40 倍，实际 %.1f —— 无上限会让高增长公司拿到不切实际的估值", highQuality)
	}

	// 关键约束：相对锚的偏离必须被夹在 ±25% 内。
	// 没有这一条，低估值行业（银行 PE 5 倍）会被套上成长股的倍数。
	worst, _ := fairPE(100, 100, 5)
	if worst > 5*1.25+1e-9 {
		t.Errorf("锚 5 倍时最好的公司也只能给到 6.25 倍，实际 %.2f —— "+
			"目标倍数脱离了市场锚，会把低估值行业判成严重低估", worst)
	}
	// 上界方向：上限是**相对锚**的（2 倍），不是固定 40 倍。
	//
	// 为什么不用固定上限：高估值行业（半导体设备 PE 中位 60~80 倍）里的公司会被
	// 夹到 40 倍，于是"模型 40 倍 vs 市场 68 倍"被判成方法冲突 —— 那只是上限太低，
	// 不是估值分歧。相对上限让上限随行业水平浮动。
	best, _ := fairPE(100, 100, 50)
	if best != 62.5 {
		t.Errorf("锚 50 倍时最好的公司应给到 62.5（锚的 1.25 倍，未触 2 倍上限），实际 %.2f", best)
	}
	// 触到相对上限的情况：锚 100 倍时上限是 200 倍，但绝对上限 120 倍先生效
	capped, _ := fairPE(100, 100, 120)
	if capped != 120 {
		t.Errorf("锚 120 倍时目标倍数应被绝对上限夹到 120，实际 %.2f", capped)
	}
	// 锚在合理范围内时不该触发上限
	mid, _ := fairPE(100, 100, 24)
	if mid <= 24 || mid > 30 {
		t.Errorf("锚 24 倍、基本面最好的公司应给到 24~30 倍之间，实际 %.2f", mid)
	}

	// 拿不到锚时必须退回默认区间（不能返回 0，否则所有方法一起失效）
	fallback, note := fairPE(0, 0, 0)
	if fallback <= 0 {
		t.Fatalf("拿不到市场锚时应退回默认区间，实际 %.2f", fallback)
	}
	if note == "" {
		t.Error("走降级路径时必须给出说明（界面要显示为什么没做市场锚定）")
	}
}

// TestGrowthDampeningFollowsQualityAndHorizon 钉住增速衰减的两个方向。
func TestGrowthDampeningFollowsQualityAndHorizon(t *testing.T) {
	// 质量越高，衰减越小（增速越可持续）
	if growthDampening(80, "12m") <= growthDampening(40, "12m") {
		t.Error("高质量公司的增速衰减应小于低质量公司")
	}
	// 期限越长，衰减越强
	if growthDampening(70, "36m") >= growthDampening(70, "3m") {
		t.Error("36 个月的增速衰减应强于 3 个月")
	}
	// 衰减系数必须在 (0, 1] 内
	for _, quality := range []float64{0, 40, 60, 80, 100} {
		for _, key := range HorizonKeys() {
			value := growthDampening(quality, key)
			if value <= 0 || value > 1 {
				t.Errorf("growthDampening(%.0f, %s) = %.3f，应在 (0,1] 内", quality, key, value)
			}
		}
	}
}

// TestWeightedPEIsNotArithmeticMean 钉住方案 §5.2：
// "行业整体估值不能简单采用所有公司估值倍数的算术平均"。
func TestWeightedPEIsNotArithmeticMean(t *testing.T) {
	// 一家巨头（市值 9000 亿、盈利 900 亿 → PE 10 倍）
	// 一家小公司（市值 100 亿、盈利 1 亿 → PE 100 倍）
	// 算术平均 = 55 倍；调和加权 = 9100 / 901 ≈ 10.1 倍
	companies := []CompanyValuation{
		{Code: "A", MarketCap: 900e9, FloatMarketCap: 900e9, NetProfit: 90e9, Equity: 900e9, Revenue: 900e9},
		{Code: "B", MarketCap: 10e9, FloatMarketCap: 10e9, NetProfit: 0.1e9, Equity: 5e9, Revenue: 2e9},
	}
	pe, _, _, ok := weightedPE(companies, true)
	if !ok {
		t.Fatal("加权 PE 应可算出")
	}
	arithmetic := (10.0 + 100.0) / 2
	if math.Abs(pe-arithmetic) < 1 {
		t.Errorf("加权 PE %.2f 太接近算术平均 %.2f —— 说明用错了口径", pe, arithmetic)
	}
	// 正确口径：总市值 ÷ 总盈利（把两个巨头的市值加总，再除以盈利总和）
	const (
		wantCap    = 910e9
		wantProfit = 90.1e9
	)
	want := wantCap / wantProfit
	if math.Abs(pe-want) > 1e-6 {
		t.Errorf("加权 PE = %.6f，应为 总市值 %.0f ÷ 总盈利 %.0f = %.6f", pe, wantCap, wantProfit, want)
	}
	// 交叉验证：这个值也不是"按市值加权各公司 PE"（那会得到 ~10.0999… 附近但语义不同），
	// 真正要钉住的是"分母是盈利总和，不是 PE 的加权"
	sumProfit := 0.0
	for _, company := range companies {
		sumProfit += company.NetProfit
	}
	if math.Abs(sumProfit-wantProfit) > 1 {
		t.Errorf("总盈利 = %.0f，应为 %.0f", sumProfit, wantProfit)
	}
}

// TestIndustryKeepsWeightedMedianAndPercentileSeparate 钉住方案 §5.2 的三者分列。
func TestIndustryKeepsWeightedMedianAndPercentileSeparate(t *testing.T) {
	companies := make([]CompanyValuation, 0, 30)
	for i := 0; i < 30; i++ {
		// 倍数随市值递增：这样"市值加权"与"中位数"必然不同，
		// 测试才真的有区分力（如果所有公司倍数相同，两者当然相等，
		// 那样的用例对"用错口径"完全无感）
		pe := 10 + float64(i)*0.8
		pb := 1 + float64(i)*0.08
		ps := 1 + float64(i)*0.04
		cap := float64(100+i*10) * 1e8
		companies = append(companies, CompanyValuation{
			Code:           string(rune('A' + i)),
			MarketCap:      cap,
			FloatMarketCap: cap,
			PE:             pe,
			PB:             pb,
			PS:             ps,
			NetProfit:      cap / pe,
			Equity:         cap / pb,
			Revenue:        cap / ps,
			ROE:            0.10,
		})
	}
	params := DefaultParams()
	// 造一段历史，让分位能算出来
	history := make([]float64, 0, 200)
	for i := 0; i < 200; i++ {
		history = append(history, 15+float64(i)*0.05)
	}
	industry := AggregateIndustry("BK0001", "测试行业", companies, history, params)
	if !industry.Available {
		t.Fatalf("行业聚合应可用: %s", industry.Note)
	}
	if industry.WeightedPE <= 0 || industry.MedianPE <= 0 {
		t.Fatal("加权 PE 与中位 PE 都应有值")
	}
	if industry.PEPercentile <= 0 {
		t.Error("有 200 个历史样本时行业 PE 分位应能算出")
	}
	// 三者必须是三个独立字段，不能相互覆盖（这里断言它们确实不同）
	if math.Abs(industry.WeightedPE-industry.MedianPE) < 1e-9 {
		t.Error("加权 PE 与中位 PE 不应是同一个值（方案要求分别保留）")
	}
	// 分位是 0~100 的另一个量纲
	if industry.PEPercentile > 100 {
		t.Errorf("分位应在 0~100 内，实际 %.2f", industry.PEPercentile)
	}
}

// TestSelectComparablesRelaxesProgressively 钉住同行筛选会逐级放宽，
// 且放宽后会在说明里如实写出"可比性下降"。
func TestSelectComparablesRelaxesProgressively(t *testing.T) {
	target := CompanyValuation{Code: "T", MarketCap: 100e8, DebtRatio: 0.40, GrowthScore: 60}
	// 一批"市值差距过大"的同行 → L1~L3 都筛不出，只能走 L4
	peers := []CompanyValuation{
		{Code: "P1", MarketCap: 5000e8, DebtRatio: 0.40, GrowthScore: 60, PE: 15},
		{Code: "P2", MarketCap: 6000e8, DebtRatio: 0.42, GrowthScore: 58, PE: 16},
		{Code: "P3", MarketCap: 7000e8, DebtRatio: 0.38, GrowthScore: 62, PE: 17},
		{Code: "P4", MarketCap: 8000e8, DebtRatio: 0.41, GrowthScore: 61, PE: 18},
		{Code: "P5", MarketCap: 9000e8, DebtRatio: 0.39, GrowthScore: 59, PE: 19},
	}
	picked, note := SelectComparables(target, peers, DefaultParams())
	if len(picked) != 5 {
		t.Fatalf("应放宽到 L4 拿到全部 5 只，实际 %d 只（%s）", len(picked), note)
	}
	if !contains(note, "L4") {
		t.Errorf("说明里应写出放宽到了哪一级（L4），实际：%s", note)
	}
	if !contains(note, "可比性下降") {
		t.Errorf("放宽到行业全体时应提示可比性下降，实际：%s", note)
	}
	// 空行业
	if picked, note := SelectComparables(target, []CompanyValuation{target}, DefaultParams()); picked != nil {
		t.Errorf("行业里只有自己时应返回空，实际 %d 只（%s）", len(picked), note)
	}
}

// TestCyclicalMethodOnlyForCyclicalIndustry 钉住方法选择是"按行业特征"的，
// 而不是对所有股票用同一套方法（验收标准 3）。
func TestCyclicalMethodOnlyForCyclicalIndustry(t *testing.T) {
	input := makeScoreInput()
	fairInput := FairValueInput{
		Company: input.Company, AsOf: input.AsOf,
		Price: 22, Shares: 1e9,
		Growth: input.Growth, Quality: input.Quality,
		Industry: input.Industry, IndustryAvailable: true,
		Horizon: Horizons[4], Params: input.Params,
	}

	// 成熟行业：不采用周期调整估值，且必须说明理由
	mature := EvaluateFairValue(fairInput)
	cyclicalMethod := findMethod(mature.Methods, MethodCyclicalAdj)
	if cyclicalMethod == nil {
		t.Fatal("方法清单里应始终包含周期调整估值的条目（即使不适用）")
	}
	if cyclicalMethod.Applicable {
		t.Error("成熟行业不应采用周期调整估值")
	}
	if !contains(cyclicalMethod.Reason, "成熟") {
		t.Errorf("不适用时必须说明原因（应提到行业阶段为成熟），实际：%s", cyclicalMethod.Reason)
	}

	// 周期行业：应当采用
	fairInput.Industry.Stage = "cyclical"
	cyclical := EvaluateFairValue(fairInput)
	cyclicalMethod = findMethod(cyclical.Methods, MethodCyclicalAdj)
	if cyclicalMethod == nil || !cyclicalMethod.Applicable {
		t.Errorf("周期波动行业应采用周期调整估值，实际适用=%v 原因=%s",
			cyclicalMethod != nil && cyclicalMethod.Applicable, cyclicalMethod.Reason)
	}
}

// TestDCFOnlyForLongHorizon 钉住"DCF 只用于 36 个月"（方案 §6.1）。
func TestDCFOnlyForLongHorizon(t *testing.T) {
	input := makeScoreInput()
	for _, horizon := range Horizons {
		fair := EvaluateFairValue(FairValueInput{
			Company: input.Company, AsOf: input.AsOf,
			Price: 22, Shares: 1e9,
			Growth: input.Growth, Quality: input.Quality,
			Industry: input.Industry, IndustryAvailable: true,
			Horizon: horizon, Params: input.Params,
		})
		method := findMethod(fair.Methods, MethodDCF)
		if method == nil {
			t.Fatalf("%s 期限缺少 DCF 条目", horizon.Key)
		}
		if horizon.Key == "36m" {
			// 36 个月在数据齐备时应适用
			if !method.Applicable {
				t.Errorf("36 个月应采用 DCF，实际不适用：%s", method.Reason)
			}
		} else if method.Applicable {
			t.Errorf("%s 期限不应采用 DCF（会把长期结论硬套到短期）", horizon.Key)
		}
	}
}

// TestMethodConflictIsReportedNotAveraged 钉住方法冲突时解释原因、降置信度，
// 而不是机械取平均（方案 §5.5）。
func TestMethodConflictIsReportedNotAveraged(t *testing.T) {
	// 两个区间完全不重叠的方法
	methods := []MethodResult{
		{Method: MethodPE, Label: "PE", Applicable: true, FairValue: 40, Low: 36, High: 44},
		{Method: MethodPB, Label: "PB", Applicable: true, FairValue: 10, Low: 8, High: 12},
	}
	conflict, note := detectMethodConflict(methods)
	if !conflict {
		t.Fatal("区间完全不重叠应判定为方法冲突")
	}
	if !contains(note, "不取平均") {
		t.Errorf("冲突说明里必须写明不取平均的处理方式，实际：%s", note)
	}
	if !contains(note, "PE") || !contains(note, "PB") {
		t.Errorf("冲突说明必须点名是哪两种方法冲突，实际：%s", note)
	}

	// 区间有重叠时不算冲突
	overlapping := []MethodResult{
		{Method: MethodPE, Label: "PE", Applicable: true, FairValue: 25, Low: 20, High: 30},
		{Method: MethodPB, Label: "PB", Applicable: true, FairValue: 28, Low: 24, High: 32},
	}
	if conflict, _ := detectMethodConflict(overlapping); conflict {
		t.Error("区间有重叠时不应判定为冲突（那只是不确定性大）")
	}
}

// TestEffectiveSharesUseAsOfDate 钉住股本取"截至该日生效"的值。
//
// 取错方向（取之后的第一条）会让 2024 年的市值用上 2025 年增发后的股本。
func TestEffectiveSharesUseAsOfDate(t *testing.T) {
	timeline := NewShareTimeline([]SharePoint{
		{Date: 20200101, TotalShares: 100, FloatShares: 80},
		{Date: 20240101, TotalShares: 200, FloatShares: 160},
		{Date: 20260101, TotalShares: 250, FloatShares: 200},
	})
	cases := []struct {
		date int64
		want float64
	}{
		{20191231, 0},   // 早于第一条 → 取不到
		{20200101, 100}, // 恰好在变更日 → 用新值
		{20231231, 100}, // 变更前 → 用旧值
		{20240101, 200},
		{20250601, 200}, // 两次变更之间
		{20270101, 250},
	}
	for _, item := range cases {
		point, ok := timeline.At(item.date)
		if item.want == 0 {
			if ok {
				t.Errorf("日期 %d 早于第一条股本记录，应取不到，实际 %v", item.date, point)
			}
			continue
		}
		if !ok || point.TotalShares != item.want {
			t.Errorf("日期 %d 的生效股本应为 %.0f，实际 %.0f (ok=%v)", item.date, item.want, point.TotalShares, ok)
		}
	}
}

// TestMarketCapUsesRawCloseNotAdjusted 钉住第二条铁律：
// 市值必须用不复权价，复权价只用于收益。
func TestMarketCapUsesRawCloseNotAdjusted(t *testing.T) {
	// 一根每 1 股送 2 股后的行情：不复权价 111.42、复权因子 3.025
	bar := Bar{Date: 20250729, Close: 111.42, AdjFactor: 3.025}
	if nearlyEqual(bar.AdjustedClose(), bar.Close, 1e-9) {
		t.Fatal("测试设定错误：复权后价格应不同于原始价")
	}
	shares := SharePoint{TotalShares: 1e9, FloatShares: 1e9}

	total, _, ok := MarketCap(bar.Close, shares)
	if !ok {
		t.Fatal("市值应可算出")
	}
	wantTotal := 111.42 * 1e9
	if math.Abs(total-wantTotal) > 1 {
		t.Errorf("市值应用不复权价算：期望 %.0f，实际 %.0f", wantTotal, total)
	}
	// 若误用复权价，市值会被放大 3 倍以上 —— 明确断言这不是我们做的事
	wrong, _, _ := MarketCap(bar.AdjustedClose(), shares)
	if math.Abs(total-wrong) < 1 {
		t.Error("市值计算不能使用复权价")
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func nearlyEqual(a, b, tolerance float64) bool { return math.Abs(a-b) <= tolerance }

func findMethod(methods []MethodResult, method Method) *MethodResult {
	for i := range methods {
		if methods[i].Method == method {
			return &methods[i]
		}
	}
	return nil
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
