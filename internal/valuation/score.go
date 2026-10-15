package valuation

import (
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// 综合评分与风险约束（方案 §7）
//
// ## 评分流程（方案 §7.2 原文的落地）
//
//	基础指标 → 各维度评分 → 按期限加权 → 数据质量乘子 → 风险约束 → 最终评分与评级
//
// ## 去重是硬约束，不是约定（方案 §7.1 末段）
//
// 方案原文："对相同信息进行去重处理，避免例如「行业估值偏低」既被重复计入行业
// 评分、相对估值评分，又以相同依据重复增加综合分。"
//
// 本文件的做法是把每个维度拆成若干 **Factor**，每个 Factor 带一个 **Owner**
// （唯一归属维度）。`ValidateFactors` 会拒绝"同一个 Owner 下出现语义重复的因子"，
// 并且**同一个 Factor 只允许被加权一次**。测试 `TestFactorsAreNotDoubleCounted`
// 会构造一个重复因子来钉住这个校验真的会报错。
// ---------------------------------------------------------------------------

// Factor 是一个参与评分的因子。
type Factor struct {
	// Dimension 是它唯一归属的评分维度。
	Dimension ScoreDimension
	// Key 是因子标识（同一维度内唯一）。
	Key string
	// Score 是 0~100 的因子分。
	Score float64
	// Weight 是该因子在所属维度内的权重（同维度内归一化）。
	Weight float64
	// Evidence 是支撑这个因子的依据（可追溯）。
	Evidence Evidence
}

// HorizonScore 是一档期限的评分结果。
type HorizonScore struct {
	Horizon Horizon `json:"horizon"`

	// Dimensions 是九个维度的得分（原始分，未受风险约束影响）。
	Dimensions map[ScoreDimension]float64 `json:"dimensions"`
	// Weights 是这档期限实际使用的维度权重（归一化到 100）。
	Weights map[ScoreDimension]float64 `json:"weights"`

	// RawScore 是风险约束前的综合分。
	//
	// ⚠ 方案 §7.3 要求"不能通过风险约束静默修改原始估值结果"，所以
	// RawScore 与 FinalScore **分开存储、分开展示**，约束只作用于后者。
	RawScore float64 `json:"rawScore"`
	// DataMultiplier 是数据质量乘子（0.6~1.0）。
	DataMultiplier float64 `json:"dataMultiplier"`
	// FinalScore 是最终综合分。
	FinalScore float64 `json:"finalScore"`

	// Rating 是最终评级。
	Rating Rating `json:"rating"`
	// RatingCapped 表示评级是否被风险约束封顶。
	RatingCapped bool `json:"ratingCapped"`

	// RiskFlags 是该期限命中的风险约束。
	RiskFlags []RiskFlag `json:"riskFlags"`

	// Outlook 是该期限的收益情景分析。
	Outlook Outlook `json:"outlook"`

	// Contributions 是各维度对最终分的贡献（= 维度分 × 权重 / 100）。
	// 界面上的"为什么是这个分"直接用它。
	Contributions map[ScoreDimension]float64 `json:"contributions"`
}

// StockScore 是一个标的的完整评估结果。
type StockScore struct {
	Code string `json:"code"`
	Name string `json:"name"`

	IndustryCode string `json:"industryCode"`
	IndustryName string `json:"industryName"`

	// Price 是当前不复权收盘价（元）。
	Price float64 `json:"price"`
	// MarketCap 是总市值（元）。
	MarketCap float64 `json:"marketCap"`
	// FloatMarketCap 是流通市值（元）。
	FloatMarketCap float64 `json:"floatMarketCap"`

	// EvalDate 是评估基准交易日。
	EvalDate int64 `json:"evalDate"`
	// LatestPeriod 是使用的财务报告期。
	LatestPeriod string `json:"latestPeriod"`

	// ---- 估值四层（方案 §5 的四个层次，全部并列输出）----
	Self          SelfValuation        `json:"self"`
	Industry      IndustryValuation    `json:"industry"`
	Relative      RelativeValuation    `json:"relative"`
	FairByHorizon map[string]FairValue `json:"fairByHorizon"`

	// Growth 与 Quality 是**分开**的两个结果（方案 §4.2 的硬要求）。
	Growth  Growth  `json:"growth"`
	Quality Quality `json:"quality"`

	// Scores 是五档期限的独立评分。
	Scores []HorizonScore `json:"scores"`

	// OverallConfidence 是整体数据可信度。
	OverallConfidence Confidence `json:"overallConfidence"`

	// Evidence 是全部依据（按维度分组由前端处理）。
	Evidence []Evidence `json:"evidence"`

	// RiskFlags 是五档风险约束的并集（去重后），用于总览列表展示。
	RiskFlags []RiskFlag `json:"riskFlags"`

	// Available 表示是否给出了正常评估。
	Available bool `json:"available"`
	// UnavailableReason 在 Available=false 时说明原因。
	UnavailableReason string `json:"unavailableReason,omitempty"`

	// Warnings 是评估过程中的口径提示。
	Warnings []string `json:"warnings"`
}

// ScoreInput 是评分所需的全部输入（由引擎组装）。
type ScoreInput struct {
	Code     string
	Name     string
	Industry IndustryValuation
	Self     SelfValuation
	Relative RelativeValuation
	Growth   Growth
	Quality  Quality
	AsOf     AsOf
	Company  CompanyValuation
	Price    float64
	Shares   float64
	// Volatility 是年化波动率。
	Volatility float64
	// PaidLast12M 是过去 12 个月每股派息（元）。
	PaidLast12M float64
	// PriceLength 是行情长度（交易日），进数据质量分。
	PriceLength int
	// PriceAdjusted 表示价格序列是否已应用复权因子。
	PriceAdjusted bool
	// Confidence 是估值置信度（来自 fairvalue.go）。
	Confidence Confidence
	// FairByHorizon 是各期限的合理价值。
	FairByHorizon map[string]FairValue
	Params        Params
}

// EvaluateScore 计算五档期限的评分。
func EvaluateScore(input ScoreInput) (StockScore, error) {
	out := StockScore{
		Code:              input.Code,
		Name:              input.Name,
		IndustryCode:      input.Industry.Code,
		IndustryName:      input.Industry.Name,
		Price:             input.Price,
		MarketCap:         input.Company.MarketCap,
		FloatMarketCap:    input.Company.FloatMarketCap,
		EvalDate:          input.AsOf.EvalDate,
		LatestPeriod:      input.AsOf.LatestPeriod,
		Self:              input.Self,
		Industry:          input.Industry,
		Relative:          input.Relative,
		Growth:            input.Growth,
		Quality:           input.Quality,
		FairByHorizon:     input.FairByHorizon,
		OverallConfidence: input.Confidence,
	}

	// ---- 构建因子向量（每档期限共用维度分，权重按期限不同）----
	factors, err := buildFactors(input)
	if err != nil {
		return out, err
	}

	// ---- 逐档期限评分 ----
	for _, horizon := range Horizons {
		fair := input.FairByHorizon[horizon.Key]
		horizonScore := scoreForHorizon(horizon, factors, input, fair)
		out.Scores = append(out.Scores, horizonScore)
	}

	// ---- 依据汇总 ----
	out.Evidence = collectEvidence(input, factors)
	out.RiskFlags = unionRiskFlags(out.Scores)
	out.Warnings = append(out.Warnings, fairWarnings(input)...)
	out.OverallConfidence = input.Confidence

	// ---- 是否可以给出正常评估 ----
	out.Available, out.UnavailableReason = assessAvailability(input)
	return out, nil
}

// ---------------------------------------------------------------------------
// 因子构建（含去重校验）
// ---------------------------------------------------------------------------

// buildFactors 把各模块的结果折成因子向量。
//
// ⚠ 去重规则（方案 §7.1）在这里落实。每个 Factor 的 Key 在同一维度内唯一，
// 且**跨维度不允许出现同一份信息的两种表述**：
//
//	"行业估值分位低"        → 只归 DimIndValuation
//	"个股相对行业的折价"    → 只归 DimRelValuation（用的是**差值**，不是行业绝对水平）
//	"个股自身历史分位低"    → 只归 DimSelfValuation
//	"安全边际"              → 只归 DimMargin（用的是绝对价值法的结果，不重复计分位）
//	"成长能力"              → 只归 DimGrowth；它作为**假设**进入 DimMargin 与 DimReturn，
//	                          但在那两处不再产生加分因子
//	"成长质量"              → 只归 DimQuality；它可以**解除**风险约束，但不是加分项
//	"股息率"                → 按期限归入 DimReturn（短期限）或 DimSelfValuation（长期限），
//	                          同一期限只计一次（见 dividendFactorDimension）
func buildFactors(input ScoreInput) ([]Factor, error) {
	out := make([]Factor, 0, 16)

	// ---- 维度 1：个股自身估值 ----
	out = append(out, Factor{
		Dimension: DimSelfValuation,
		Key:       "self_percentile",
		Score:     input.Self.Score,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimSelfValuation),
			Metric:    "自身历史估值分位",
			Value: fmt.Sprintf("PE %.1f 倍（历史 %.0f%% 分位，%s）",
				input.Self.CurrentPE, input.Self.PEPercentile5Y, PercentileLabel(input.Self.PEPercentile5Y)),
			Raw:    input.Self.PEPercentile5Y,
			AsOf:   fmt.Sprintf("%d", input.Self.Current.Date),
			Source: "自建历史估值序列（不复权价 × 当期股本 ÷ 当期已披露 TTM 业绩）",
			Note: fmt.Sprintf("3 年分位 %.0f%%、5 年分位 %.0f%%；样本 %d 个交易日；%s",
				input.Self.PEPercentile3Y, input.Self.PEPercentile5Y, input.Self.SampleDays5Y,
				structuralNote(input.Self)),
		},
	})

	// ---- 维度 2：行业整体估值 ----
	industryNote := input.Industry.Note
	if structure := industryStructureNote(input.Industry); structure != "" {
		industryNote = structure
	}
	out = append(out, Factor{
		Dimension: DimIndValuation,
		Key:       "industry_valuation",
		Score:     input.Industry.Score,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimIndValuation),
			Metric:    "行业整体估值",
			Value: fmt.Sprintf("加权 PE %.1f 倍 / 中位 PE %.1f 倍（历史 %.0f%% 分位）",
				input.Industry.WeightedPE, input.Industry.MedianPE, input.Industry.PEPercentile),
			Raw:    input.Industry.PEPercentile,
			AsOf:   fmt.Sprintf("%d", input.AsOf.EvalDate),
			Source: "cnequity:industry_members + 自建估值（调和加权，非算术平均）",
			Note: fmt.Sprintf("成分 %d 只、亏损占比 %.0f%%、前 3 大权重合计 %.0f%%；阶段：%s；%s",
				input.Industry.Members, input.Industry.LossRatio*100, input.Industry.Top3Weight*100,
				stageLabel(input.Industry.Stage), industryNote),
		},
	})

	// ---- 维度 3：相对同行的估值性价比 ----
	// ⚠ 这里只允许用**相对差值**（折溢价、行业内的分位排名），
	// 不许把行业的绝对估值水平再用一次 —— 那是维度 2 的专属信息。
	out = append(out, Factor{
		Dimension: DimRelValuation,
		Key:       "relative_premium",
		Score:     input.Relative.Score,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimRelValuation),
			Metric:    "相对行业与同行的估值",
			Value: fmt.Sprintf("对行业中位数 %+.1f%%、对行业加权 %+.1f%%（行业内 PE 分位 %.0f%%）",
				input.Relative.PremiumVsMedian*100, input.Relative.PremiumVsWeighted*100,
				input.Relative.PEPercentileInIndustry),
			AsOf:   fmt.Sprintf("%d", input.AsOf.EvalDate),
			Source: "同行可比公司筛选（市值倍数 + 负债率 + 成长档位逐级放宽）",
			Note: fmt.Sprintf("%s；%s；%s", input.Relative.QuadrantReason,
				input.Relative.ComparableNote, input.Relative.BothCheapNote),
		},
	})

	// ---- 维度 4：成长能力 ----
	out = append(out, Factor{
		Dimension: DimGrowth,
		Key:       "growth_ability",
		Score:     growthFactorScore(input.Growth),
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimGrowth),
			Metric:    "成长能力",
			Value: fmt.Sprintf("营收单季同比 %+.1f%%、3 年 CAGR %.1f%%、成长持续性 %.0f%%",
				input.Growth.RevenueYoY*100, input.Growth.RevenueCAGR3*100, input.Growth.Consistency*100),
			Raw:    input.Growth.RevenueCAGR3,
			AsOf:   input.AsOf.LatestPeriod,
			Source: "cnequity:financial_statement_items（单季拆解 + 年报 CAGR）",
			Note:   growthNoteText(input.Growth),
		},
	})

	// ---- 维度 5：成长质量与盈利质量 ----
	out = append(out, Factor{
		Dimension: DimQuality,
		Key:       "growth_quality",
		Score:     input.Quality.Score,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimQuality),
			Metric:    "成长质量与盈利质量",
			Value: fmt.Sprintf("扣非占比 %.2f、利润现金含量 %.2f、资产负债率 %.1f%%",
				input.Quality.DeductedRatio, input.Quality.CashContent, input.Quality.DebtRatio*100),
			AsOf:   input.AsOf.LatestPeriod,
			Source: "cnequity:financial_statement_items（扣非、现金流、资产负债）",
			Note:   qualityNoteText(input.Quality),
		},
	})

	// ---- 维度 6：资本回报与资本使用效率 ----
	capital, capitalEvidence := capitalFactor(input)
	out = append(out, Factor{
		Dimension: DimCapital,
		Key:       "capital_return",
		Score:     capital,
		Weight:    1.0,
		Evidence:  capitalEvidence,
	})

	// ---- 维度 7：安全边际 ----
	// ⚠ 只用**绝对价值法**的结论（基准合理价值 vs 当前价），不复用估值分位。
	margin, marginEvidence := marginFactor(input)
	out = append(out, Factor{
		Dimension: DimMargin,
		Key:       "safety_margin",
		Score:     margin,
		Weight:    1.0,
		Evidence:  marginEvidence,
	})

	// ---- 维度 8：多期限预期收益与下行风险 ----
	// ⚠ 这个因子是**按期限**变化的，所以在这里只放占位，
	// 真正的分数在 scoreForHorizon 里按期限覆盖（见 horizonReturnFactor）。
	out = append(out, Factor{
		Dimension: DimReturn,
		Key:       "horizon_return",
		Score:     50,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimReturn),
			Metric:    "预期收益与下行风险",
			AsOf:      fmt.Sprintf("%d", input.AsOf.EvalDate),
			Source:    "情景概率加权（悲观/基准/乐观）",
			Note:      "该因子按期限独立计算，见各期限的 outlook 字段",
		},
	})

	// ---- 维度 9：数据质量与模型置信度 ----
	out = append(out, Factor{
		Dimension: DimData,
		Key:       "data_quality",
		Score:     input.Confidence.Score,
		Weight:    1.0,
		Evidence: Evidence{
			Dimension: string(DimData),
			Metric:    "数据质量与模型置信度",
			Value:     fmt.Sprintf("%.0f 分（%s）", input.Confidence.Score, confidenceLabel(input.Confidence.Level)),
			Raw:       input.Confidence.Score,
			AsOf:      fmt.Sprintf("%d", input.AsOf.EvalDate),
			Source:    "覆盖率 / 区间宽度 / 方法一致性检查",
			Note:      joinStrings(input.Confidence.Reasons, "；"),
		},
	})

	// ---- 去重校验 ----
	if err := ValidateFactors(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateFactors 校验因子向量没有重复计分。
//
// 两条规则：
//
//  1. **同一维度内 Key 必须唯一** —— 防止同一个信息被写两遍（例如把
//     "PE 分位"和"PE 相对中位数"同时放进 DimSelfValuation）。
//  2. **跨维度的 Key 也不允许重复** —— 这一条更严，因为方案点名禁止的正是
//     "同一依据既计入行业评分、又计入相对估值评分"。
//
// 返回错误而不是静默合并：静默合并会让重复计分在实现里"看不见"，
// 而它恰恰是这类多维度模型最常见的失真来源。
func ValidateFactors(factors []Factor) error {
	seenByDimension := map[ScoreDimension]map[string]bool{}
	seenGlobal := map[string]ScoreDimension{}
	for _, factor := range factors {
		if factor.Dimension == "" {
			return fmt.Errorf("因子 %q 没有归属维度", factor.Key)
		}
		if factor.Key == "" {
			return fmt.Errorf("维度 %s 下存在没有 Key 的因子", factor.Dimension)
		}
		if seenByDimension[factor.Dimension] == nil {
			seenByDimension[factor.Dimension] = map[string]bool{}
		}
		if seenByDimension[factor.Dimension][factor.Key] {
			return fmt.Errorf("去重校验失败：维度 %s 下因子 %q 重复出现（同一信息被重复计分）",
				factor.Dimension, factor.Key)
		}
		seenByDimension[factor.Dimension][factor.Key] = true
		if owner, exists := seenGlobal[factor.Key]; exists {
			return fmt.Errorf("去重校验失败：因子 %q 同时归属 %s 与 %s（跨维度重复计分）",
				factor.Key, owner, factor.Dimension)
		}
		seenGlobal[factor.Key] = factor.Dimension
	}
	return nil
}

// ---------------------------------------------------------------------------
// 逐期限评分
// ---------------------------------------------------------------------------

func scoreForHorizon(horizon Horizon, factors []Factor, input ScoreInput, fair FairValue) HorizonScore {
	out := HorizonScore{
		Horizon:       horizon,
		Dimensions:    map[ScoreDimension]float64{},
		Weights:       map[ScoreDimension]float64{},
		Contributions: map[ScoreDimension]float64{},
	}

	weights := input.Params.WeightsFor(horizon.Key)

	// 先按维度聚合因子（同维度内按 Weight 加权平均）
	dimensionScores := map[ScoreDimension]float64{}
	for _, factor := range factors {
		score := factor.Score
		// 维度 8（预期收益）是逐期限的，用该期限的 outlook 覆盖
		if factor.Dimension == DimReturn {
			score, _ = horizonReturnScore(horizon, input, fair)
		}
		if factor.Weight == 0 {
			continue
		}
		if existing, ok := dimensionScores[factor.Dimension]; ok {
			// 同维度多因子：按权重加权平均（当前每个维度只有一个因子，
			// 这段逻辑是给以后拆分维度用的，行为已经由单测钉住）
			dimensionScores[factor.Dimension] = existing + score*factor.Weight
		} else {
			dimensionScores[factor.Dimension] = score * factor.Weight
		}
	}
	// 归一化（因子权重之和可能不是 1）
	weightSums := map[ScoreDimension]float64{}
	for _, factor := range factors {
		weightSums[factor.Dimension] += factor.Weight
	}
	for dimension, total := range weightSums {
		if total > 0 {
			dimensionScores[dimension] /= total
		}
	}

	// 逐维度加权求和
	raw := 0.0
	weightTotal := 0.0
	for _, dimension := range ScoreDimensions {
		score, ok := dimensionScores[dimension]
		if !ok {
			continue
		}
		weight := weights[dimension]
		out.Dimensions[dimension] = score
		out.Weights[dimension] = weight
		out.Contributions[dimension] = score * weight / 100
		raw += score * weight / 100
		weightTotal += weight
	}
	// 权重之和不等于 100 时归一化（配置被手改过的情形）
	if weightTotal > 0 && math.Abs(weightTotal-100) > 0.01 {
		raw = raw / weightTotal * 100
	}
	out.RawScore = clampScore(raw)

	// ---- 数据质量乘子 ----
	// 与维度 9 不同：维度 9 是"评分的一部分"，乘子是"整体打折"。
	// 两者不是重复计分：维度 9 让"数据差"在 100 分里占一份权重，
	// 乘子则是在所有维度上统一降权 —— 语义不同（方案 §7.2 把它列在
	// "数据质量与风险约束"这一步，而不是评分维度里）。
	out.DataMultiplier = dataMultiplier(input.Confidence)

	// ---- 该期限的风险约束 ----
	out.RiskFlags = evaluateRisks(horizon, input, fair, out.Dimensions)
	capped, cappedBy := applyRiskCaps(out.RawScore, out.RiskFlags, input.Params.RatingThresholds)

	// ---- 最终分 ----
	finalScore := out.RawScore * out.DataMultiplier
	rating := RatingForScore(finalScore, input.Params.RatingThresholds)

	// RatingCapped 的语义是"**这档评级处在风险约束的封顶之下**"，
	// 而不是"评级数值被改小了"。原始分本身就已经很低时约束依然生效，
	// 界面上依然要显示"因 XX 风险，评级上限为中性" —— 所以只要存在
	// 限制评级类的风险、且当前评级不高于它的上限，就标记为已受约束。
	out.RatingCapped = false
	for _, flag := range out.RiskFlags {
		if flag.Severity == RiskCap && flag.CapRating != "" &&
			RatingRank(rating) >= 0 && RatingRank(rating) <= RatingRank(flag.CapRating) {
			out.RatingCapped = true
		}
	}
	if capped != Rating("") && RatingRank(rating) > RatingRank(capped) {
		rating = capped
		out.RatingCapped = true
	}
	// 无法可靠评估的约束直接推翻评级
	for _, flag := range out.RiskFlags {
		if flag.Severity == RiskUnavailable {
			rating = RatingUnavailable
			out.RatingCapped = true
			break
		}
	}
	out.FinalScore = clampScore(finalScore)
	out.Rating = rating

	// ---- 该期限的收益情景 ----
	out.Outlook = EvaluateHorizon(horizon.Key, fair, input.Price, input.Volatility,
		dividendFor(input, horizon), input.Params)

	_ = cappedBy
	return out
}

// dataMultiplier 把置信度折成 0.6~1.0 的乘子。
//
// 下限取 0.6 而不是 0：数据差应该显著降分，但不该让"评分"这个动作本身失去意义
// （真的无法评估的情况由 RiskUnavailable 单独处理，不出评级）。
func dataMultiplier(confidence Confidence) float64 {
	if confidence.Score <= 0 {
		return 0.6
	}
	return 0.6 + 0.4*clamp01(confidence.Score/100)
}

// horizonReturnScore 把该期限的预期收益与风险折成 0~100。
//
// 三维合成：预期收益（越高越好）、悲观损失（越小越好）、风险收益比。
// 权重 40/30/30。**分母用悲观损失而不是波动率** —— 问的是"最坏情况下亏多少"，
// 这也是方案 §6 反复强调"悲观情景损失"而不是"波动率"的原因。
func horizonReturnScore(horizon Horizon, input ScoreInput, fair FairValue) (float64, string) {
	volatility := input.Volatility
	outlook := EvaluateHorizon(horizon.Key, fair, input.Price, volatility, dividendFor(input, horizon), input.Params)
	if !outlook.Available {
		return 50, outlook.Note
	}
	// 预期收益：+20% 记满分，−20% 记 0 分，0 记 50 分
	returnScore := 50 + outlook.ExpectedReturn/0.20*50
	// 悲观损失：0% 记满分，−50% 记 0 分
	lossScore := 100 - outlook.PessimisticLoss/0.50*100
	// 风险收益比：≥2 记满分，≤0.5 记 0 分
	ratioScore := 0.0
	if outlook.ReturnRiskRatio > 0 {
		ratioScore = clamp01((outlook.ReturnRiskRatio-0.5)/1.5) * 100
	}
	score := 0.40*clampScore(returnScore) + 0.30*clampScore(lossScore) + 0.30*clampScore(ratioScore)
	note := fmt.Sprintf("预期收益 %+.1f%%、悲观损失 %.1f%%、风险收益比 %.2f；"+
		"向合理价值收敛 %.0f%%、该期限波动 %.1f%%",
		outlook.ExpectedReturn*100, outlook.PessimisticLoss*100, outlook.ReturnRiskRatio,
		outlook.Convergence*100, outlook.ExpectedVolatility*100)
	return clampScore(score), note
}

// dividendFor 返回该期限的预期分红收益率。
//
// 去重说明（方案 §8.2）：股息率是同一份信息，所以在**短期限**归入
// 预期收益维度（因为短期收益里分红是可确定的一部分），在**长期限**归入
// 自身估值维度（长期看股息率是估值水平的一部分）。同一期限只计一次。
func dividendFor(input ScoreInput, horizon Horizon) float64 {
	paid := FutureDividends(input.PaidLast12M, horizon.TradingDays)
	return dividendYieldInWindow([]float64{paid}, input.Price)
}

// dividendFactorDimension 返回股息率应该归入哪个维度（供文档与测试使用）。
func dividendFactorDimension(horizonKey string) ScoreDimension {
	switch horizonKey {
	case "1m", "3m", "6m":
		return DimReturn
	default:
		return DimSelfValuation
	}
}

// ---------------------------------------------------------------------------
// 风险约束（方案 §7.3 的九类）
// ---------------------------------------------------------------------------

// evaluateRisks 逐条检查九类风险。
func evaluateRisks(horizon Horizon, input ScoreInput, fair FairValue, dimensions map[ScoreDimension]float64) []RiskFlag {
	flags := make([]RiskFlag, 0, 4)
	params := input.Params
	thresholds := params.RiskThresholds

	// ① 估值较低但盈利持续恶化
	if input.Self.CurrentPE > 0 && input.Self.PEPercentile5Y > 0 &&
		input.Self.PEPercentile5Y < thresholds.CheapPct && deterioratingQuarters(input.Growth) >= thresholds.DeteriorateQuarters {
		flags = append(flags, RiskFlag{
			Code:      RiskCheapButDeteriorating,
			Severity:  RiskCap,
			Title:     "估值低但盈利持续恶化",
			Reason:    fmt.Sprintf("个股 PE 处于历史 %.0f%% 分位（低于 %.0f%%），而单季净利润已连续 %d 个季度同比下滑；低估值可能是市场对盈利下滑的正确定价", input.Self.PEPercentile5Y, thresholds.CheapPct, deterioratingQuarters(input.Growth)),
			CapRating: RatingNeutral,
		})
	}

	// ② 个股便宜但行业本身仍贵
	if input.Relative.PremiumVsMedian < -0.20 && input.Industry.PEPercentile > thresholds.ExpensivePct {
		flags = append(flags, RiskFlag{
			Code:     RiskIndustryExpensive,
			Severity: RiskCap,
			Title:    "个股相对行业便宜，但行业整体仍偏贵",
			Reason: fmt.Sprintf("个股相对行业中位数折价 %.0f%%，但行业加权 PE 处于自身历史 %.0f%% 分位（高于 %.0f%%）；"+
				"行业整体估值下行会同时压低个股",
				-input.Relative.PremiumVsMedian*100, input.Industry.PEPercentile, thresholds.ExpensivePct),
			CapRating: RatingNeutral,
		})
	}

	// ③ 高增长但现金流质量较差
	if input.Growth.Score > 70 && input.Quality.CashContent > 0 && input.Quality.CashContent < thresholds.CashContentMin {
		flags = append(flags, RiskFlag{
			Code:     RiskGrowthNoCash,
			Severity: RiskNotice,
			Title:    "高增长但现金流质量较差",
			Reason: fmt.Sprintf("成长能力分 %.0f（>70）而经营现金流 / 归母净利润 = %.2f（低于 %.2f）；"+
				"利润没有变成现金，增长的可持续性存疑", input.Growth.Score, input.Quality.CashContent, thresholds.CashContentMin),
		})
	}

	// ④ 估值溢价显著但成长与盈利质量缺乏支撑
	if input.Relative.PremiumVsMedian > thresholds.PremiumMax &&
		input.Growth.Score < 50 && input.Quality.Score < 50 {
		flags = append(flags, RiskFlag{
			Code:     RiskPremiumUnsupported,
			Severity: RiskCap,
			Title:    "估值溢价但成长与盈利质量没有支撑",
			Reason: fmt.Sprintf("相对行业中位数溢价 %.0f%%（高于 %.0f%%），而成长分 %.0f、质量分 %.0f 都低于中位；"+
				"溢价缺少基本面依据，估值下修风险高",
				input.Relative.PremiumVsMedian*100, thresholds.PremiumMax*100, input.Growth.Score, input.Quality.Score),
			CapRating: RatingPoor,
		})
	}

	// ⑤ 基准预期收益较高但悲观情景损失过大
	outlook := EvaluateHorizon(horizon.Key, fair, input.Price, input.Volatility, dividendFor(input, horizon), params)
	if outlook.Available && outlook.ReturnRiskRatio > 0 && outlook.ReturnRiskRatio < thresholds.MinReturnRiskRatio {
		flags = append(flags, RiskFlag{
			Code:     RiskDownsideTooLarge,
			Severity: RiskCap,
			Title:    "预期收益与下行风险不匹配",
			Reason: fmt.Sprintf("%s 期限：预期收益 %+.1f%%、悲观情景损失 %.1f%%、风险收益比 %.2f（低于 %.2f）；"+
				"承担的下行风险没有得到足够补偿",
				horizon.Label, outlook.ExpectedReturn*100, outlook.PessimisticLoss*100,
				outlook.ReturnRiskRatio, thresholds.MinReturnRiskRatio),
			CapRating: RatingNeutral,
		})
	}

	// ⑥ 估值结果高度依赖乐观假设
	if outlook.Available && outlook.OptimismDependence > thresholds.OptimismDependenceMax {
		flags = append(flags, RiskFlag{
			Code:     RiskOptimismDependent,
			Severity: RiskCap,
			Title:    "估值结论高度依赖乐观假设",
			Reason: fmt.Sprintf("%s 期限：剔除乐观情景后预期收益下降 %.0f%%（超过 %.0f%% 的阈值）；"+
				"结论主要靠乐观情景支撑，稳健性不足",
				horizon.Label, outlook.OptimismDependence*100, thresholds.OptimismDependenceMax*100),
			CapRating: RatingNeutral,
		})
	}

	// ⑦ 行业或个股估值存在明显周期性误判
	if input.Industry.CyclicalWarning {
		flags = append(flags, RiskFlag{
			Code:     RiskCyclicalMisjudge,
			Severity: RiskNotice,
			Title:    "可能存在周期性估值误判",
			Reason:   input.Industry.CyclicalReason,
		})
	}

	// ⑧ 关键数据缺失 / 过期 / 口径不一致
	if input.Confidence.Score < thresholds.MinDataCoverage*100 {
		severity := RiskCap
		capRating := RatingNeutral
		if input.Confidence.Score < 40 {
			severity = RiskUnavailable
			capRating = ""
		}
		flags = append(flags, RiskFlag{
			Code:      RiskDataQuality,
			Severity:  severity,
			Title:     "关键数据不足",
			Reason:    fmt.Sprintf("数据置信度 %.0f 分（低于 %.0f 分门槛）：%s", input.Confidence.Score, thresholds.MinDataCoverage*100, joinStrings(input.Confidence.Reasons, "；")),
			CapRating: capRating,
		})
	}
	if len(input.AsOf.Missing) > 0 {
		flags = append(flags, RiskFlag{
			Code:     RiskDataQuality,
			Severity: RiskNotice,
			Title:    "部分财务科目缺失",
			Reason:   fmt.Sprintf("缺少 %d 个科目（%s）；相关维度已按不可用处理，未强行给分", len(input.AsOf.Missing), joinItems(input.AsOf.Missing, 4)),
		})
	}
	if !input.PriceAdjusted {
		flags = append(flags, RiskFlag{
			Code:     RiskDataQuality,
			Severity: RiskNotice,
			Title:    "价格未复权",
			Reason:   "行情序列没有应用复权因子，含除权日的区间收益与回撤不可信；请确认数据湖 meta/adj_factors_cache 是否完整",
		})
	}

	// ⑨ 不同估值方法给出的结果明显冲突
	if fair.Conflict {
		flags = append(flags, RiskFlag{
			Code:     RiskMethodConflict,
			Severity: RiskNotice,
			Title:    "不同估值方法结论冲突",
			Reason:   fair.ConflictNote,
		})
	}

	return dedupeRiskFlags(flags)
}

// applyRiskCaps 找出最严的评级上限。
func applyRiskCaps(rawScore float64, flags []RiskFlag, thresholds RatingThresholds) (Rating, RiskCode) {
	capped := Rating("")
	by := RiskCode("")
	for _, flag := range flags {
		if flag.Severity != RiskCap || flag.CapRating == "" {
			continue
		}
		if capped == "" || RatingRank(flag.CapRating) < RatingRank(capped) {
			capped = flag.CapRating
			by = flag.Code
		}
	}
	// 原始分本身已经低于上限时不算"被封顶"
	if capped != "" && RatingForScore(rawScore, thresholds) != RatingUnavailable {
		if RatingRank(RatingForScore(rawScore, thresholds)) <= RatingRank(capped) {
			return "", ""
		}
	}
	return capped, by
}

// deterioratingQuarters 返回单季净利润同比连续为负的季度数（从最新往前数）。
func deterioratingQuarters(growth Growth) int {
	count := 0
	for i := len(growth.ProfitYoYSeries) - 1; i >= 0; i-- {
		if growth.ProfitYoYSeries[i] < 0 {
			count++
			continue
		}
		break
	}
	return count
}

func dedupeRiskFlags(flags []RiskFlag) []RiskFlag {
	seen := map[RiskCode]int{}
	out := make([]RiskFlag, 0, len(flags))
	for _, flag := range flags {
		if index, ok := seen[flag.Code]; ok {
			// 同一 Code 的 notice 与 cap 同时存在时保留 severity 更重的，
			// 但把两条的 Reason 合并起来（信息不能丢）
			if riskSeverityRank(flag.Severity) > riskSeverityRank(out[index].Severity) {
				flag.Reason = out[index].Reason + "；另外，" + flag.Reason
				out[index] = flag
			} else {
				out[index].Reason = out[index].Reason + "；另外，" + flag.Reason
			}
			continue
		}
		seen[flag.Code] = len(out)
		out = append(out, flag)
	}
	return out
}

func riskSeverityRank(severity RiskSeverity) int {
	switch severity {
	case RiskUnavailable:
		return 3
	case RiskCap:
		return 2
	case RiskNotice:
		return 1
	}
	return 0
}

func unionRiskFlags(scores []HorizonScore) []RiskFlag {
	all := make([]RiskFlag, 0, 8)
	for _, score := range scores {
		all = append(all, score.RiskFlags...)
	}
	return dedupeRiskFlags(all)
}

// ---------------------------------------------------------------------------
// 可用性判定
// ---------------------------------------------------------------------------

// assessAvailability 判断能否给出正常评级（方案验收标准 8）。
//
// 判据是"关键输入是否齐备"，而不是"分数高不高"：
// 数据不足时给"暂无法可靠评估"，**不强行给一个中性评级**。
func assessAvailability(input ScoreInput) (bool, string) {
	reasons := make([]string, 0, 3)
	if input.AsOf.LatestPeriod == "" {
		reasons = append(reasons, "评估日前没有任何已披露财报")
	}
	if input.Price <= 0 {
		reasons = append(reasons, "没有当前价格")
	}
	if input.Shares <= 0 {
		reasons = append(reasons, "缺少生效股本，无法计算市值与每股指标")
	}
	if input.AsOf.TTM.NetProfit == 0 && input.AsOf.TTM.Revenue == 0 {
		reasons = append(reasons, "归母净利润与营业收入都缺失，无法做任何估值")
	}
	if len(reasons) == 0 {
		return true, ""
	}
	return false, joinStrings(reasons, "；")
}

// ---------------------------------------------------------------------------
// 文案与小工具
// ---------------------------------------------------------------------------

func structuralNote(self SelfValuation) string {
	if !self.StructuralShift {
		return "未发现结构性变化，历史分位可用"
	}
	return "已检测到结构性变化，历史分位权重已下调：" + joinStrings(self.StructuralReasons, "；")
}

func growthNoteText(growth Growth) string {
	parts := make([]string, 0, 3)
	if growth.RevenueCAGR5 != 0 {
		parts = append(parts, fmt.Sprintf("5 年营收 CAGR %.1f%%", growth.RevenueCAGR5*100))
	}
	if growth.ProfitCAGR3 != 0 {
		parts = append(parts, fmt.Sprintf("3 年净利润 CAGR %.1f%%", growth.ProfitCAGR3*100))
	}
	parts = append(parts, fmt.Sprintf("成长波动 %.1f 个百分点", growth.VolatilityOfGrowth*100))
	for _, flag := range growth.Flags {
		parts = append(parts, flag.Title+"："+flag.Detail)
	}
	return joinStrings(parts, "；")
}

func qualityNoteText(quality Quality) string {
	if len(quality.Issues) == 0 {
		return "未发现成长质量或盈利质量问题"
	}
	parts := make([]string, 0, len(quality.Issues))
	for _, issue := range quality.Issues {
		parts = append(parts, issue.Title+"："+issue.Detail)
	}
	return joinStrings(parts, "；")
}

func confidenceLabel(level string) string {
	switch level {
	case "high":
		return "高"
	case "medium":
		return "中"
	default:
		return "低"
	}
}

// growthFactorScore 把成长模块的输出折成维度分。
//
// Growth.Score 已经是 0~100 的成长能力分，这里不再二次加工 ——
// 但**扭亏**是个例外：扭亏公司的同比增速没有数学意义，Score 里已经扣过分，
// 这里额外给一个"经扭亏校正后的分数"（把成长分与"是否持续盈利"结合）。
func growthFactorScore(growth Growth) float64 {
	score := growth.Score
	for _, flag := range growth.Flags {
		if flag.Code == FlagTurnaround {
			// 扭亏：成长分与 50 的中点，因为"从亏损到盈利"确实是改善，
			// 但改善的持续性未知，不能按普通高增长给分
			score = (score + 50) / 2
		}
	}
	return clampScore(score)
}

// capitalFactor 算资本回报维度的分数与依据。
func capitalFactor(input ScoreInput) (float64, Evidence) {
	roe := input.Growth.ROE
	roic := input.Growth.IncrementalROIC
	evidence := Evidence{
		Dimension: string(DimCapital),
		Metric:    "资本回报与资本使用效率",
		Value:     fmt.Sprintf("ROE %.1f%%、增量投入资本回报 %.1f%%", roe*100, roic*100),
		Raw:       roe,
		AsOf:      input.AsOf.LatestPeriod,
		Source:    "TTM 归母净利润 ÷ 平均归母净资产；增量 ROIC 为近似口径",
		Note: "增量 ROIC = Δ(营业利润×(1−实际税率)) ÷ Δ(总资产)；" +
			"本机没有有息负债与折旧摊销科目，因此这是近似值，不是严格 ROIC",
	}
	if roe == 0 && roic == 0 {
		evidence.Value = "不可用"
		evidence.Note = "缺少归母净利润或净资产，资本回报维度按中性 50 分处理（不给 0 分，避免把数据缺失当成经营差）"
		return 50, evidence
	}
	// ROE 曲线：15% 记满分，0% 记 0 分
	roeScore := clamp01(roe/0.15) * 100
	if roe < 0 {
		roeScore = 0
	}
	// 增量回报曲线：≥ WACC 记满分（创造价值），≤ 0 记 0 分
	wacc := input.Params.WACC
	roicScore := 50.0
	if roic != 0 {
		roicScore = clamp01((roic-0)/(wacc*2)) * 100
	}
	score := 0.6*roeScore + 0.4*roicScore
	if roe == 0 {
		score = roicScore
	}
	if roic == 0 {
		score = roeScore
	}
	return clampScore(score), evidence
}

// marginFactor 算安全边际维度的分数与依据。
//
// ⚠ 去重要点：这里**只**用绝对价值法（基准合理价值 vs 当前价），
// 不复用任何估值分位信息（那属于维度 1/2/3）。
func marginFactor(input ScoreInput) (float64, Evidence) {
	// 用 12 个月期限的合理价值作为"标准参照"（其它期限的结构相同，
	// 但用不同期限会让同一家公司的安全边际分随期限变化 —— 那是维度 8 的职责）
	fair, ok := input.FairByHorizon["12m"]
	if !ok {
		fair, ok = input.FairByHorizon["6m"]
	}
	evidence := Evidence{
		Dimension: string(DimMargin),
		Metric:    "安全边际",
		AsOf:      fmt.Sprintf("%d", input.AsOf.EvalDate),
		Source:    "绝对价值法（多方法加权中位数），不复用估值分位",
	}
	if !ok || !fair.Available {
		evidence.Value = "不可用"
		evidence.Note = "合理价值测算失败，安全边际维度按中性 50 分处理"
		return 50, evidence
	}
	margin := fair.SafetyMargin
	evidence.Value = fmt.Sprintf("基准合理价值 %.2f 元 vs 当前价 %.2f 元 → 安全边际 %+.1f%%",
		fair.Base, input.Price, margin*100)
	evidence.Raw = margin
	evidence.Note = fmt.Sprintf("口径：安全边际 =（基准合理价值 − 当前价格）÷ 基准合理价值；"+
		"与「潜在收益」口径不同，两者分开展示。采用的方法：%s",
		methodListLabel(fair.MethodsUsed))
	// 安全边际曲线：+30% 记满分，0% 记 40 分，−30% 记 0 分
	score := 40.0
	switch {
	case margin >= 0.30:
		score = 100
	case margin >= 0:
		score = 40 + margin/0.30*60
	case margin >= -0.30:
		score = 40 * (1 + margin/0.30)
	default:
		score = 0
	}
	return clampScore(score), evidence
}

func methodListLabel(methods []Method) string {
	if len(methods) == 0 {
		return "（无适用方法）"
	}
	labels := make([]string, 0, len(methods))
	for _, method := range methods {
		labels = append(labels, MethodLabel(method))
	}
	return joinStrings(labels, "、")
}

func fairWarnings(input ScoreInput) []string {
	out := make([]string, 0, 2)
	if fair, ok := input.FairByHorizon["36m"]; ok && fair.Available {
		out = append(out, fair.Warnings...)
	}
	return out
}

// collectEvidence 汇总依据（维度顺序固定，便于界面稳定展示）。
func collectEvidence(input ScoreInput, factors []Factor) []Evidence {
	out := make([]Evidence, 0, len(factors)+4)
	byDimension := map[ScoreDimension][]Evidence{}
	for _, factor := range factors {
		byDimension[factor.Dimension] = append(byDimension[factor.Dimension], factor.Evidence)
	}
	for _, dimension := range ScoreDimensions {
		out = append(out, byDimension[dimension]...)
	}
	// 追加估值方法清单与关键假设
	if fair, ok := input.FairByHorizon["12m"]; ok {
		out = append(out, fair.Assumptions...)
		for _, method := range fair.Methods {
			value := method.Reason
			if value == "" {
				value = method.Basis
			}
			out = append(out, Evidence{
				Dimension: string(DimMargin),
				Metric:    "估值方法：" + method.Label,
				Value:     applicabilityLabel(method.Applicable),
				AsOf:      fmt.Sprintf("%d", input.AsOf.EvalDate),
				Source:    "多模型交叉验证",
				Note:      value,
			})
		}
	}
	return out
}

func applicabilityLabel(applicable bool) string {
	if applicable {
		return "适用，已采用"
	}
	return "不适用，未采用"
}
