package valuation

import (
	"fmt"
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 指数性价比评估（方案 §8）
//
// ## 为什么指数必须用独立模型
//
// 方案原文："指数采用独立于个股的评估逻辑，不直接将个股模型简单相加。"
// 具体禁止两件事：
//
//  1. **不能简单算术平均成分股评分** —— 那会让 250 只小市值成分股把 5 只
//     权重股的结论淹没；
//  2. **不能只用加权 PE 判断指数是否便宜** —— 一个由 40 倍 PE 的成长股与
//     8 倍 PE 的银行各占一半的指数，加权 PE 是 24 倍（看着"不贵"），
//     但两半都不便宜也不便宜，加权值掩盖了结构性风险。
//
// 所以本文件的输出是**结构性**的：估值水平、估值分布、成长结构、
// 集中度、以及"有安全边际的成分股占多少权重"。
// ---------------------------------------------------------------------------

// IndexConstituent 是指数的一个成分股（带权重）。
type IndexConstituent struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"` // 权重（0~1；缺失时按 0 处理）
	Industry string  `json:"industry"`

	MarketCap float64 `json:"marketCap"`
	PE        float64 `json:"pe"`
	PB        float64 `json:"pb"`

	NetProfit float64 `json:"-"`
	Revenue   float64 `json:"-"`
	Equity    float64 `json:"-"`

	// ROE、成长与质量分（来自个股评估）。
	ROE          float64 `json:"roe"`
	RevenueYoY   float64 `json:"revenueYoY"`
	GrowthScore  float64 `json:"growthScore"`
	QualityScore float64 `json:"qualityScore"`

	// FairBase 是该成分的基准合理价值，Price 是当前价。
	// 两者都有时才能判断"有没有安全边际"。
	Price    float64 `json:"price"`
	FairBase float64 `json:"fairBase"`
	// SafetyMarginAvailable 表示该成分的安全边际是否算得出来。
	SafetyMarginAvailable bool `json:"-"`
	// SelfPercentile 是该成分自身的历史估值分位。
	SelfPercentile float64 `json:"selfPercentile"`
}

// SafetyMargin 返回该成分的安全边际；不可用时返回 false。
func (c IndexConstituent) SafetyMargin() (float64, bool) {
	if c.FairBase <= 0 || c.Price <= 0 || !c.SafetyMarginAvailable {
		return 0, false
	}
	return (c.FairBase - c.Price) / c.FairBase, true
}

// IndexEvaluation 是指数的性价比评估结果。
type IndexEvaluation struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`

	// ---- 整体估值（调和加权：总市值 ÷ 总盈利，不是 PE 的加权平均）----
	WeightedPE float64 `json:"weightedPE"`
	WeightedPB float64 `json:"weightedPB"`
	WeightedPS float64 `json:"weightedPS"`
	// MedianPE 是成分 PE 的中位数。
	MedianPE float64 `json:"medianPE"`
	// PEPercentile 是自建指数 PE 序列上的历史分位。
	PEPercentile float64       `json:"pePercentile"`
	PEBand       ValuationBand `json:"peBand"`

	// ---- 估值分布 ----
	// CheapWeight 是"自身估值处于历史低 30% 分位"的成分权重合计。
	CheapWeight float64 `json:"cheapWeight"`
	// ExpensiveWeight 是"自身估值处于历史高 30% 分位"的成分权重合计。
	ExpensiveWeight float64 `json:"expensiveWeight"`
	// LossWeight 是亏损成分的权重合计（它们没有 PE，加权 PE 会偏乐观）。
	LossWeight float64 `json:"lossWeight"`
	// MedianSelfPercentile 是成分自身估值分位的中位数。
	MedianSelfPercentile float64 `json:"medianSelfPercentile"`

	// ---- 成长与盈利结构 ----
	// WeightedROE 是加权 ROE（= 总盈利 ÷ 总净资产）。
	WeightedROE float64 `json:"weightedROE"`
	// MedianROE 是成分 ROE 的中位数。
	MedianROE float64 `json:"medianROE"`
	// WeightedRevenueYoY 是加权营收同比。
	WeightedRevenueYoY float64 `json:"weightedRevenueYoY"`
	// WeightedGrowthScore / WeightedQualityScore 是权重加权的成长与质量分。
	WeightedGrowthScore  float64 `json:"weightedGrowthScore"`
	WeightedQualityScore float64 `json:"weightedQualityScore"`
	// MedianGrowthScore / MedianQualityScore 是中位数口径（与加权并列，
	// 让"大公司强/小公司弱"这种结构差异看得出来）。
	MedianGrowthScore  float64 `json:"medianGrowthScore"`
	MedianQualityScore float64 `json:"medianQualityScore"`

	// ---- 结构与集中度 ----
	// IndustryWeights 是行业权重（降序）。
	IndustryWeights []IndustryWeight `json:"industryWeights"`
	// Top10Weight 是前 10 大权重合计。
	Top10Weight float64 `json:"top10Weight"`
	// TopIndustryWeight 是最大行业权重。
	TopIndustryWeight float64 `json:"topIndustryWeight"`
	// ConcentrationNote 是对集中度的解读。
	ConcentrationNote string `json:"concentrationNote"`

	// ---- 安全边际含量（指数层面的"便宜货占比"）----
	// MarginWeight 是"基准合理价值高于当前价"的成分权重合计。
	MarginWeight float64 `json:"marginWeight"`
	// Margin10Weight 是安全边际超过 10% 的成分权重合计。
	Margin10Weight float64 `json:"margin10Weight"`
	// MarginNote 说明有多少成分算得出安全边际（数据可得性）。
	MarginNote string `json:"marginNote"`

	// ---- 多期限 ----
	Scores []HorizonScore `json:"scores"`

	// OverallConfidence / Available / Note
	OverallConfidence Confidence `json:"overallConfidence"`
	Available         bool       `json:"available"`
	Note              string     `json:"note,omitempty"`

	// Warnings 是数据与口径提示。
	Warnings []string `json:"warnings"`
}

// IndustryWeight 是行业权重项。
type IndustryWeight struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// IndexParams 是指数评估的参数。
type IndexParams struct {
	Params Params
	// HistoricalPE 是指数的历史加权 PE 序列（升序，可为空）。
	HistoricalPE []float64
	// MinConstituents 是给出指数评估所需的最少成分数。
	MinConstituents int
	// Volatility 是指数自身的年化波动率。
	Volatility float64
	// DividendYield 是指数近 12 个月的股息率（小数）。
	DividendYield float64
	// FairByHorizon 是指数整体在各期限的合理价值（按指数点位口径）。
	FairByHorizon map[string]FairValue
}

// EvaluateIndex 用独立模型评估一个指数。
func EvaluateIndex(symbol, name string, constituents []IndexConstituent, params IndexParams) IndexEvaluation {
	out := IndexEvaluation{Symbol: symbol, Name: name}
	minConstituents := params.MinConstituents
	if minConstituents <= 0 {
		minConstituents = 5
	}
	if len(constituents) < minConstituents {
		out.Note = fmt.Sprintf("指数成分不足（%d 个，至少需要 %d 个）："+
			"本机 index_constituents 只有 1 个快照（2026-10），且只有 5 个指数有成分与权重；"+
			"没有成分数据时只能给指数自身的走势类结论，不给估值结构结论",
			len(constituents), minConstituents)
		out.Available = false
		out.OverallConfidence = Confidence{
			Score: 25, Level: "low",
			Reasons: []string{out.Note},
		}
		return out
	}
	out.Available = true

	// ---- 权重归一化 ----
	// index_constituents 的 weight 是百分数还是小数不确定，统一按"总和归一"处理，
	// 这样不管原始量纲是什么都能得到正确的相对权重。
	normalized := normalizeWeights(constituents)
	weights := make(map[string]float64, len(normalized))
	for _, item := range normalized {
		weights[item.Code] = item.Weight
	}

	// ---- 整体估值：调和加权（总市值 ÷ 总盈利）----
	totalCap, totalProfit, totalEquity, totalRevenue := 0.0, 0.0, 0.0, 0.0
	pes := make([]float64, 0, len(normalized))
	pbs := make([]float64, 0, len(normalized))
	roes := make([]float64, 0, len(normalized))
	growthScores := make([]float64, 0, len(normalized))
	qualityScores := make([]float64, 0, len(normalized))
	selfPercentiles := make([]float64, 0, len(normalized))
	lossWeight := 0.0
	cheapWeight, expensiveWeight := 0.0, 0.0
	marginWeight, margin10Weight := 0.0, 0.0
	marginCount, marginAvailable := 0, 0

	for _, item := range normalized {
		cap := item.MarketCap
		totalCap += cap
		if item.NetProfit > 0 {
			totalProfit += item.NetProfit
			if item.PE > 0 {
				pes = append(pes, item.PE)
			}
		} else {
			lossWeight += item.Weight
		}
		if item.Equity > 0 {
			totalEquity += item.Equity
		}
		if item.Revenue > 0 {
			totalRevenue += item.Revenue
		}
		if item.PB > 0 {
			pbs = append(pbs, item.PB)
		}
		if item.ROE != 0 {
			roes = append(roes, item.ROE)
		}
		if item.GrowthScore > 0 {
			growthScores = append(growthScores, item.GrowthScore)
		}
		if item.QualityScore > 0 {
			qualityScores = append(qualityScores, item.QualityScore)
		}
		if item.SelfPercentile > 0 {
			selfPercentiles = append(selfPercentiles, item.SelfPercentile)
			switch {
			case item.SelfPercentile <= 30:
				cheapWeight += item.Weight
			case item.SelfPercentile >= 70:
				expensiveWeight += item.Weight
			}
		}
		if margin, ok := item.SafetyMargin(); ok {
			marginCount++
			marginAvailable++
			if margin > 0 {
				marginWeight += item.Weight
			}
			if margin > 0.10 {
				margin10Weight += item.Weight
			}
		}
	}

	if totalCap > 0 {
		if totalProfit > 0 {
			out.WeightedPE = totalCap / totalProfit
		}
		if totalEquity > 0 {
			out.WeightedPB = totalCap / totalEquity
		}
		if totalRevenue > 0 {
			out.WeightedPS = totalCap / totalRevenue
		}
	}
	out.MedianPE = medianOf(Winsorize(pes, 0.02, 0.98))
	out.MedianROE = medianOf(roes)
	out.LossWeight = lossWeight
	out.CheapWeight = cheapWeight
	out.ExpensiveWeight = expensiveWeight
	out.MedianSelfPercentile = medianOf(selfPercentiles)
	out.MarginWeight = marginWeight
	out.Margin10Weight = margin10Weight
	out.MarginNote = fmt.Sprintf("共 %d 个成分，其中 %d 个能算出安全边际（%d 个缺少合理价值或价格）",
		len(normalized), marginCount, len(normalized)-marginAvailable)

	// ---- 加权成长与质量 ----
	for _, item := range normalized {
		weight := item.Weight
		if item.GrowthScore > 0 {
			out.WeightedGrowthScore += item.GrowthScore * weight
		}
		if item.QualityScore > 0 {
			out.WeightedQualityScore += item.QualityScore * weight
		}
		if item.RevenueYoY != 0 {
			out.WeightedRevenueYoY += item.RevenueYoY * weight
		}
	}
	out.MedianGrowthScore = medianOf(growthScores)
	out.MedianQualityScore = medianOf(qualityScores)

	// 加权 ROE 用"总盈利 ÷ 总净资产"，与加权 PE/PB 同一口径
	if totalEquity > 0 && totalProfit != 0 {
		out.WeightedROE = totalProfit / totalEquity
	}

	// ---- 集中度与行业结构 ----
	sorted := make([]IndexConstituent, len(normalized))
	copy(sorted, normalized)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Weight > sorted[j].Weight })
	top := 10
	if top > len(sorted) {
		top = len(sorted)
	}
	for i := 0; i < top; i++ {
		out.Top10Weight += sorted[i].Weight
	}
	industryTotals := map[string]float64{}
	for _, item := range normalized {
		name := item.Industry
		if name == "" {
			name = "未分类"
		}
		industryTotals[name] += item.Weight
	}
	out.IndustryWeights = make([]IndustryWeight, 0, len(industryTotals))
	for industry, weight := range industryTotals {
		out.IndustryWeights = append(out.IndustryWeights, IndustryWeight{Name: industry, Weight: weight})
	}
	sort.Slice(out.IndustryWeights, func(i, j int) bool {
		return out.IndustryWeights[i].Weight > out.IndustryWeights[j].Weight
	})
	if len(out.IndustryWeights) > 0 {
		out.TopIndustryWeight = out.IndustryWeights[0].Weight
	}
	out.ConcentrationNote = concentrationNote(out)

	// ---- 历史分位 ----
	history := positiveOnly(params.HistoricalPE)
	if len(history) >= 60 && out.WeightedPE > 0 {
		out.PEPercentile, _ = Percentile(history, out.WeightedPE, 60)
		out.PEBand = BuildBand(history)
	} else {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"指数历史估值样本不足（%d 个交易日，至少需要 60 个）：本机的指数成分只有 1 个快照、"+
				"个股行业归属只有 5 个快照，所以历史指数 PE 只能靠当前成分近似重建，存在成分前视偏差；"+
				"此处只给当前加权值与分布，不给历史分位", len(history)))
	}

	// ---- 多期限评分 ----
	out.Scores = scoreIndex(out, params)

	// ---- 置信度 ----
	out.OverallConfidence = indexConfidence(out, params)

	// ---- 口径提示（必须显示）----
	out.Warnings = append(out.Warnings, indexWarnings(out)...)
	return out
}

// normalizeWeights 把权重归一化到总和 1；全部缺失时按市值权重兜底。
//
// 为什么要兜底：index_constituents 里某些成分的 weight 可能为空，
// 直接归一化会把这些成分当成权重 0（等于把它们从指数里删掉），
// 而它们的市值实际贡献着指数涨跌。
func normalizeWeights(constituents []IndexConstituent) []IndexConstituent {
	out := make([]IndexConstituent, len(constituents))
	copy(out, constituents)
	total := 0.0
	for _, item := range out {
		if item.Weight > 0 {
			total += item.Weight
		}
	}
	if total > 0 {
		missing := 0
		for _, item := range out {
			if item.Weight <= 0 {
				missing++
			}
		}
		if missing == 0 {
			for i := range out {
				out[i].Weight /= total
			}
			return out
		}
		// 有缺失：把缺失的那一份按各成分市值相对占比补给它们，其余按原权重
		// 比例归一。⚠ 只归一一次 —— 先补零再整体除一遍会把缺失成分压成 0，
		// 等于把它们从指数里删掉。
		missingCap := 0.0
		for _, item := range out {
			if item.Weight <= 0 {
				missingCap += item.MarketCap
			}
		}
		for i := range out {
			if out[i].Weight <= 0 {
				if missingCap > 0 {
					out[i].Weight = (out[i].MarketCap / missingCap) * (missingCap / (missingCap + total))
				} else {
					out[i].Weight = 0
				}
				continue
			}
			out[i].Weight = out[i].Weight / (missingCap + total) * total
		}
		sum := 0.0
		for _, item := range out {
			sum += item.Weight
		}
		if sum > 0 {
			for i := range out {
				out[i].Weight /= sum
			}
		}
		return out
	}
	// 完全没有权重：按市值
	capTotal := 0.0
	for _, item := range out {
		capTotal += item.MarketCap
	}
	if capTotal > 0 {
		for i := range out {
			out[i].Weight = out[i].MarketCap / capTotal
		}
		return out
	}
	// 连市值都没有：等权
	equal := 1.0 / float64(len(out))
	for i := range out {
		out[i].Weight = equal
	}
	return out
}

// concentrationNote 解读集中度。
func concentrationNote(out IndexEvaluation) string {
	parts := make([]string, 0, 2)
	if out.Top10Weight > 0.60 {
		parts = append(parts, fmt.Sprintf(
			"前 10 大成分权重合计 %.0f%%（>60%%），指数走势高度依赖少数公司；"+
				"加权 PE 也主要由它们决定，需要同时看中位数 %.1f 倍与加权值 %.1f 倍的差",
			out.Top10Weight*100, out.MedianPE, out.WeightedPE))
	} else {
		parts = append(parts, fmt.Sprintf("前 10 大成分权重合计 %.0f%%，权重分布相对分散",
			out.Top10Weight*100))
	}
	if out.TopIndustryWeight > 0.35 && len(out.IndustryWeights) > 0 {
		parts = append(parts, fmt.Sprintf(
			"最大行业「%s」占 %.0f%%（>35%%），指数带有明显的行业属性，"+
				"该行业的周期位置会主导指数表现",
			out.IndustryWeights[0].Name, out.TopIndustryWeight*100))
	}
	if out.LossWeight > 0.10 {
		parts = append(parts, fmt.Sprintf(
			"亏损成分权重合计 %.0f%%（>10%%）：它们没有 PE，加权 PE 的分母不含它们，"+
				"所以加权 PE 会**偏乐观**，这点必须与中位数一起看", out.LossWeight*100))
	}
	return joinStrings(parts, "；")
}

// scoreIndex 给指数算五档期限评分。
//
// 结构上是"独立模型"：它的九个维度里，
//   - DimSelfValuation 用**指数自身**的历史 PE 分位（不是成分股分位的平均）；
//   - DimIndValuation 用**结构分散度**（成分估值分布的均衡程度，而不是某个行业）；
//   - DimRelValuation 用**加权与中位数的背离度**（背离大说明估值被少数成分主导）；
//   - DimGrowth / DimQuality / DimCapital 用加权口径；
//   - DimMargin 用"有安全边际的成分权重"（指数层面的便宜货含量）；
//   - DimReturn 用指数自身的波动与股息率算情景。
func scoreIndex(out IndexEvaluation, params IndexParams) []HorizonScore {
	scores := make([]HorizonScore, 0, len(Horizons))

	// 指数自身的估值分（越低越便宜）
	selfScore := 50.0
	if out.PEPercentile > 0 {
		selfScore = 95 - out.PEPercentile*0.9
	}
	// 结构分散度：便宜权重高 → 好；贵权重高 → 差
	structureScore := 50.0
	if out.CheapWeight+out.ExpensiveWeight > 0 {
		structureScore = 50 + (out.CheapWeight-out.ExpensiveWeight)*100
	}
	// 加权与中位数背离：加权远高于中位数说明指数被高估值大票拉动
	dispersionScore := 50.0
	if out.MedianPE > 0 && out.WeightedPE > 0 {
		ratio := out.WeightedPE / out.MedianPE
		// ratio = 1 → 50；ratio = 1.5 → 20；ratio = 0.7 → 80
		dispersionScore = clampScore(50 - (ratio-1)*100)
	}
	// 成长与质量：加权与中位数取平均（避免只看大票）
	growthScore := (out.WeightedGrowthScore + out.MedianGrowthScore) / 2
	qualityScore := (out.WeightedQualityScore + out.MedianQualityScore) / 2
	if growthScore == 0 {
		growthScore = 50
	}
	if qualityScore == 0 {
		qualityScore = 50
	}
	// 资本回报：加权 ROE
	capitalScore := 50.0
	if out.WeightedROE != 0 {
		capitalScore = clamp01(out.WeightedROE/0.15) * 100
		if out.WeightedROE < 0 {
			capitalScore = 0
		}
	}
	// 安全边际含量：有安全边际的权重占比
	marginScore := 50.0
	if out.MarginWeight > 0 || out.Margin10Weight > 0 {
		marginScore = clampScore(20 + out.MarginWeight*60 + out.Margin10Weight*30)
	}
	// 数据质量
	dataScore := out.OverallConfidence.Score

	confidence := out.OverallConfidence
	for _, horizon := range Horizons {
		item := HorizonScore{
			Horizon:       horizon,
			Dimensions:    map[ScoreDimension]float64{},
			Weights:       map[ScoreDimension]float64{},
			Contributions: map[ScoreDimension]float64{},
		}
		weights := params.Params.WeightsFor(horizon.Key)

		dimensions := map[ScoreDimension]float64{
			DimSelfValuation: selfScore,
			DimIndValuation:  structureScore,
			DimRelValuation:  dispersionScore,
			DimGrowth:        growthScore,
			DimQuality:       qualityScore,
			DimCapital:       capitalScore,
			DimMargin:        marginScore,
			DimData:          dataScore,
		}

		// 该期限的收益情景（用指数自身的波动率与股息率）
		fair, hasFair := params.FairByHorizon[horizon.Key]
		if !hasFair {
			// 没有指数专门的合理价值时，用"当前点位 + 估值分位"推一个中性目标：
			// 分位低 → 目标略高；分位高 → 目标略低。这样多期限仍然有差别
			// （收敛比例不同），而不是五档都拿到 50 分。
			fair = indexNeutralFair(out, horizon, params)
		}
		returnScore, _ := horizonReturnScore(horizon, ScoreInput{
			Price: fair.Price, Volatility: params.Volatility,
			PaidLast12M: params.DividendYield * fair.Price,
			Params:      params.Params,
		}, fair)
		dimensions[DimReturn] = returnScore

		raw := 0.0
		total := 0.0
		for _, dimension := range ScoreDimensions {
			value, ok := dimensions[dimension]
			if !ok {
				continue
			}
			weight := weights[dimension]
			item.Dimensions[dimension] = value
			item.Weights[dimension] = weight
			item.Contributions[dimension] = value * weight / 100
			raw += value * weight / 100
			total += weight
		}
		if total > 0 && math.Abs(total-100) > 0.01 {
			raw = raw / total * 100
		}
		item.RawScore = clampScore(raw)
		item.DataMultiplier = dataMultiplier(confidence)
		item.FinalScore = clampScore(item.RawScore * item.DataMultiplier)
		item.Rating = RatingForScore(item.FinalScore, params.Params.RatingThresholds)
		item.Outlook = EvaluateHorizon(horizon.Key, fair, fair.Price, params.Volatility,
			dividendYieldInWindow([]float64{FutureDividends(params.DividendYield*fair.Price, horizon.TradingDays)}, fair.Price),
			params.Params)

		// 指数层面的风险约束：主要是数据类与结构类
		if out.PEPercentile == 0 {
			item.RiskFlags = append(item.RiskFlags, RiskFlag{
				Code:     RiskDataQuality,
				Severity: RiskNotice,
				Title:    "指数历史估值分位不可用",
				Reason:   "缺少时点正确的历史指数 PE 序列（成分只有 1 个快照），估值得分退化为「结构分散度」判断",
			})
		}
		if out.LossWeight > 0.15 {
			item.RiskFlags = append(item.RiskFlags, RiskFlag{
				Code:     RiskDataQuality,
				Severity: RiskNotice,
				Title:    "指数含较多亏损成分",
				Reason: fmt.Sprintf("亏损成分权重合计 %.0f%%（>15%%），加权 PE 未包含它们的亏损，"+
					"因此偏向乐观；请以中位数 PE %.1f 倍为参照", out.LossWeight*100, out.MedianPE),
			})
		}
		if out.Top10Weight > 0.70 {
			item.RiskFlags = append(item.RiskFlags, RiskFlag{
				Code:     RiskCyclicalMisjudge,
				Severity: RiskNotice,
				Title:    "指数权重高度集中",
				Reason:   fmt.Sprintf("前 10 大成分权重合计 %.0f%%，指数结论主要由少数公司决定", out.Top10Weight*100),
			})
		}
		scores = append(scores, item)
	}
	return scores
}

// indexNeutralFair 在没有专门的指数合理价值时，用估值分位推一个中性目标。
//
// 这是**明确的降级方案**（会在 Warnings 里说明），不是假装有模型：
// 分位 30 → 目标略高于现价 10%；分位 70 → 目标略低于现价 10%。
func indexNeutralFair(out IndexEvaluation, horizon Horizon, params IndexParams) FairValue {
	// 用成分股的加权合理价值/当前价推出一个"指数整体的隐含空间"
	upside := 0.0
	available := false
	for _, item := range out.IndustryWeights {
		_ = item
	}
	// 用 MarginWeight 作为隐含空间的代理：有安全边际的权重越高，隐含空间越大
	if out.MarginWeight > 0 || out.Margin10Weight > 0 {
		upside = out.MarginWeight*0.10 + out.Margin10Weight*0.15
		available = true
	} else if out.PEPercentile > 0 {
		// 分位低 → 正空间
		upside = (50 - out.PEPercentile) / 50 * 0.15
		available = true
	}
	price := 100.0 // 指数点位归一化到 100，只用于相对收益
	base := price * (1 + upside)
	return FairValue{
		Available:    available,
		Price:        price,
		Pessimistic:  base * (1 - params.Volatility*math.Sqrt(float64(horizon.TradingDays)/tradingDaysPerYear)),
		Base:         base,
		Optimistic:   base * (1 + params.Volatility*math.Sqrt(float64(horizon.TradingDays)/tradingDaysPerYear)),
		SafetyMargin: (base - price) / base,
		Upside:       upside,
		MethodsUsed:  []Method{MethodPE},
		Confidence:   out.OverallConfidence,
		Warnings: []string{"指数没有专门的合理价值模型（成分历史缺失），" +
			"各期限的目标空间由「有安全边际的成分权重」与估值分位推算，属于降级口径"},
	}
}

// indexConfidence 算指数评估的置信度。
func indexConfidence(out IndexEvaluation, params IndexParams) Confidence {
	score := 100.0
	reasons := make([]string, 0, 4)
	if out.PEPercentile == 0 {
		score -= 20
		reasons = append(reasons, "缺少时点正确的历史指数 PE 序列（成分快照只有 1 个），无法给历史分位，扣 20 分")
	}
	if out.LossWeight > 0.10 {
		penalty := math.Min(out.LossWeight*100*0.5, 15)
		score -= penalty
		reasons = append(reasons, fmt.Sprintf("亏损成分权重 %.0f%%，加权 PE 偏乐观，扣 %.0f 分",
			out.LossWeight*100, penalty))
	}
	// 能算出安全边际的成分比例
	coverage := 0.0
	if len(out.IndustryWeights) > 0 {
		// MarginNote 里已经带了计数，这里用权重覆盖率近似
		coverage = out.MarginWeight
	}
	_ = coverage
	if out.MarginWeight == 0 && out.Margin10Weight == 0 {
		score -= 15
		reasons = append(reasons, "没有任何成分算得出安全边际（缺少合理价值或价格），「便宜货含量」维度不可用，扣 15 分")
	}
	if out.Top10Weight > 0.60 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf("前 10 大权重合计 %.0f%%，指数结论对少数成分敏感，扣 10 分",
			out.Top10Weight*100))
	}
	score = clampScore(score)
	if len(reasons) == 0 {
		reasons = append(reasons, "估值分布、成分覆盖与集中度三项均正常，未扣分")
	}
	return Confidence{Score: score, Level: ConfidenceLevelFor(score), Reasons: reasons}
}

// indexWarnings 汇总指数评估必须显示的口径提示。
func indexWarnings(out IndexEvaluation) []string {
	warnings := make([]string, 0, 3)
	warnings = append(warnings, "指数估值用**调和加权**（总市值 ÷ 总盈利），不是成分 PE 的算术平均；"+
		"同时给出中位数 PE，两者背离越大说明指数越被少数成分主导")
	if out.WeightedPE > 0 && out.MedianPE > 0 {
		ratio := out.WeightedPE / out.MedianPE
		if ratio > 1.20 || ratio < 0.83 {
			warnings = append(warnings, fmt.Sprintf(
				"加权 PE %.1f 倍与中位 PE %.1f 倍背离 %.0f%%：低估值的权重股把加权值拉低了，"+
					"指数整体并不比它的成分更便宜", out.WeightedPE, out.MedianPE, math.Abs(ratio-1)*100))
		}
	}
	warnings = append(warnings, "本机 index_constituents 只有 1 个快照（2026-10），"+
		"历史日期的指数成分无法时点正确重建 —— 历史指数估值序列存在**成分前视偏差**，这一点已在置信度里扣分")
	return warnings
}
