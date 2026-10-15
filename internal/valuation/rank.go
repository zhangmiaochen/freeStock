package valuation

import (
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// 排名与筛选（方案 §9）
//
// 三条硬要求：
//
//  1. **按期限独立排名**（五档各一套）；
//  2. **排名必须使用一致的评估日期、模型版本与数据截止时间** —— 所以
//     RankResult 里带着 Snapshot，界面必须显示它；
//  3. **被风险规则阻断或无法可靠评估的标的单独标记，不与正常评估标的混合排序**
//     —— 这是最容易做错的一条：把评级为"暂无法可靠评估"的标的按分数排进榜，
//     会让榜上出现"数据缺失但分很高"的荒谬结果。
// ---------------------------------------------------------------------------

// RankRow 是排名榜上的一行。
type RankRow struct {
	Rank int    `json:"rank"`
	Code string `json:"code"`
	Name string `json:"name"`

	IndustryCode string `json:"industryCode"`
	IndustryName string `json:"industryName"`

	// Score 是最终综合分。
	Score float64 `json:"score"`
	// RawScore 是风险约束前的分（分开展示，方案 §7.3）。
	RawScore float64 `json:"rawScore"`
	Rating   Rating  `json:"rating"`
	// RatingCapped 表示评级被风险约束封顶。
	RatingCapped bool `json:"ratingCapped"`

	// ScorePercentile 是全市场分位（0~100）。
	ScorePercentile float64 `json:"scorePercentile"`
	// IndustryRank 是行业内排名（1 起）。
	IndustryRank int `json:"industryRank"`
	// IndustrySize 是该行业参与排名的标的数。
	IndustrySize int `json:"industrySize"`

	Price          float64 `json:"price"`
	MarketCap      float64 `json:"marketCap"`
	FloatMarketCap float64 `json:"floatMarketCap"`

	// 估值四层的摘要
	CurrentPE          float64 `json:"currentPE"`
	SelfPercentile     float64 `json:"selfPercentile"`
	IndustryWeightedPE float64 `json:"industryWeightedPE"`
	IndustryPercentile float64 `json:"industryPercentile"`
	PremiumVsMedian    float64 `json:"premiumVsMedian"`

	// 成长与质量
	GrowthScore  float64 `json:"growthScore"`
	QualityScore float64 `json:"qualityScore"`
	ROE          float64 `json:"roe"`

	// 安全边际与收益空间
	FairBase     float64 `json:"fairBase"`
	SafetyMargin float64 `json:"safetyMargin"`
	Upside       float64 `json:"upside"`

	// 该期限的收益情景
	ExpectedReturn  float64 `json:"expectedReturn"`
	PessimisticLoss float64 `json:"pessimisticLoss"`
	ReturnRiskRatio float64 `json:"returnRiskRatio"`
	TargetPrice     float64 `json:"targetPrice"`

	// 该期限的估值分位（用于"估值性价比排名"）
	SelfPercentileForHorizon float64 `json:"selfPercentileForHorizon"`

	// Confidence 是数据可信度。
	Confidence Confidence `json:"confidence"`

	// RiskFlags 是该期限命中的风险（含标题，便于榜上直接显示）。
	RiskFlags []RiskFlag `json:"riskFlags"`

	// KeyStrengths 是关键优势（最长 3 条）。
	KeyStrengths []string `json:"keyStrengths"`
	// MainRisks 是主要风险（最长 3 条）。
	MainRisks []string `json:"mainRisks"`
}

// ExcludedRow 是被排除出正常排名的标的。
type ExcludedRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Rating Rating `json:"rating"`
	// Reason 是排除原因（方案要求"单独标记"并说明）。
	Reason string `json:"reason"`
	// Score 仍然给出（供参考），但明确标注它没有参与排名。
	Score float64 `json:"score"`
	// RawScore 是约束前的分。
	RawScore  float64    `json:"rawScore"`
	RiskFlags []RiskFlag `json:"riskFlags"`
}

// HorizonRanking 是一档期限的排名结果。
type HorizonRanking struct {
	Horizon Horizon `json:"horizon"`
	// Rows 是按综合分降序的正常评估标的。
	Rows []RankRow `json:"rows"`
	// Excluded 是被排除的标的（方案 §9：单独标记，不混排）。
	Excluded []ExcludedRow `json:"excluded"`
	// Scored 是参与排名的标的数。
	Scored int `json:"scored"`
	// ExcludedCount 是被排除数。
	ExcludedCount int `json:"excludedCount"`
	// Distribution 是分数分布（用于总览页的直方图）。
	Distribution ScoreDistribution `json:"distribution"`
}

// ScoreDistribution 是评分分布的统计量。
type ScoreDistribution struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P10    float64 `json:"p10"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	// Histogram 是 [0,10) [10,20) … [90,100] 共 10 个桶的计数。
	Histogram []int `json:"histogram"`
	// RatingCounts 是各评级的数量。
	RatingCounts map[Rating]int `json:"ratingCounts"`
}

// RankInput 是参与排名的一个标的（一档期限一行）。
type RankInput struct {
	Code string
	Name string
	// Score 是该标的在该期限的评分结果（只取对应的那一档）。
	Score HorizonScore
	Stock StockScore
}

// BuildRanking 构造一档期限的排名。
func BuildRanking(horizon Horizon, inputs []RankInput) HorizonRanking {
	out := HorizonRanking{Horizon: horizon}

	// ---- 先分层：正常评估 vs 被排除 ----
	normal := make([]RankInput, 0, len(inputs))
	for _, input := range inputs {
		// 排除条件（方案 §9）：评级为"暂无法可靠评估"，或整体不可评估
		if input.Score.Rating == RatingUnavailable || !input.Stock.Available {
			reason := input.Stock.UnavailableReason
			if reason == "" {
				reason = "风险约束判定为无法可靠评估"
				for _, flag := range input.Score.RiskFlags {
					if flag.Severity == RiskUnavailable {
						reason = flag.Title + "：" + flag.Reason
					}
				}
			}
			out.Excluded = append(out.Excluded, ExcludedRow{
				Code:      input.Code,
				Name:      input.Name,
				Rating:    RatingUnavailable,
				Reason:    reason,
				Score:     input.Score.FinalScore,
				RawScore:  input.Score.RawScore,
				RiskFlags: input.Score.RiskFlags,
			})
			continue
		}
		normal = append(normal, input)
	}

	// ---- 排序：分数降序；同分按代码升序（保证结果稳定可复现）----
	sort.Slice(normal, func(i, j int) bool {
		if normal[i].Score.FinalScore != normal[j].Score.FinalScore {
			return normal[i].Score.FinalScore > normal[j].Score.FinalScore
		}
		return normal[i].Code < normal[j].Code
	})

	// ---- 全市场分位 ----
	scores := make([]float64, 0, len(normal))
	for _, input := range normal {
		scores = append(scores, input.Score.FinalScore)
	}
	out.Distribution = BuildDistribution(scores)

	// ---- 行业内排名 ----
	industryGroups := map[string][]string{} // 行业码 -> 该行业内的代码（已按分数降序）
	for _, input := range normal {
		key := input.Stock.IndustryCode
		if key == "" {
			key = "未分类"
		}
		industryGroups[key] = append(industryGroups[key], input.Code)
	}
	industryRank := map[string]int{}
	industrySize := map[string]int{}
	for key, codes := range industryGroups {
		industrySize[key] = len(codes)
		for index, code := range codes {
			industryRank[code] = index + 1
		}
	}

	// ---- 组装 ----
	out.Rows = make([]RankRow, 0, len(normal))
	for index, input := range normal {
		stock := input.Stock
		score := input.Score
		fair := stock.FairByHorizon[horizon.Key]

		key := stock.IndustryCode
		if key == "" {
			key = "未分类"
		}
		percentile, _ := Percentile(scores, score.FinalScore, 1)

		row := RankRow{
			Rank:                     index + 1,
			Code:                     input.Code,
			Name:                     input.Name,
			IndustryCode:             stock.IndustryCode,
			IndustryName:             stock.IndustryName,
			Score:                    score.FinalScore,
			RawScore:                 score.RawScore,
			Rating:                   score.Rating,
			RatingCapped:             score.RatingCapped,
			ScorePercentile:          percentile,
			IndustryRank:             industryRank[input.Code],
			IndustrySize:             industrySize[key],
			Price:                    stock.Price,
			MarketCap:                stock.MarketCap,
			FloatMarketCap:           stock.FloatMarketCap,
			CurrentPE:                stock.Self.CurrentPE,
			SelfPercentile:           stock.Self.PEPercentile5Y,
			IndustryWeightedPE:       stock.Industry.WeightedPE,
			IndustryPercentile:       stock.Industry.PEPercentile,
			PremiumVsMedian:          stock.Relative.PremiumVsMedian,
			GrowthScore:              stock.Growth.Score,
			QualityScore:             stock.Quality.Score,
			ROE:                      stock.Growth.ROE,
			FairBase:                 fair.Base,
			SafetyMargin:             fair.SafetyMargin,
			Upside:                   fair.Upside,
			ExpectedReturn:           score.Outlook.ExpectedReturn,
			PessimisticLoss:          score.Outlook.PessimisticLoss,
			ReturnRiskRatio:          score.Outlook.ReturnRiskRatio,
			TargetPrice:              score.Outlook.TargetPrice,
			SelfPercentileForHorizon: stock.Self.PEPercentile5Y,
			Confidence:               stock.OverallConfidence,
			RiskFlags:                score.RiskFlags,
		}
		row.KeyStrengths = keyStrengths(stock, score)
		row.MainRisks = mainRisks(stock, score)
		out.Rows = append(out.Rows, row)
	}
	out.Scored = len(out.Rows)
	out.ExcludedCount = len(out.Excluded)
	return out
}

// BuildDistribution 统计分数分布。
func BuildDistribution(scores []float64) ScoreDistribution {
	out := ScoreDistribution{Histogram: make([]int, 10), RatingCounts: map[Rating]int{}}
	if len(scores) == 0 {
		return out
	}
	sorted := sortedCopy(scores)
	out.Mean = 0
	for _, score := range scores {
		out.Mean += score
		index := int(score / 10)
		if index > 9 {
			index = 9
		}
		if index < 0 {
			index = 0
		}
		out.Histogram[index]++
	}
	out.Mean /= float64(len(scores))
	out.Median = Quantile(sorted, 0.5)
	out.P10 = Quantile(sorted, 0.10)
	out.P25 = Quantile(sorted, 0.25)
	out.P75 = Quantile(sorted, 0.75)
	out.P90 = Quantile(sorted, 0.90)
	// 评级分布（用默认阈值，因为分布是描述性的）
	thresholds := DefaultRatingThresholds()
	for _, score := range scores {
		out.RatingCounts[RatingForScore(score, thresholds)]++
	}
	return out
}

// keyStrengths 提取关键优势（最多 3 条，每条都带数值）。
func keyStrengths(stock StockScore, score HorizonScore) []string {
	out := make([]string, 0, 4)
	if stock.Self.PEPercentile5Y > 0 && stock.Self.PEPercentile5Y < 35 {
		out = append(out, fmt.Sprintf("自身估值处于历史 %.0f%% 分位（%s）",
			stock.Self.PEPercentile5Y, PercentileLabel(stock.Self.PEPercentile5Y)))
	}
	if stock.Relative.PremiumVsMedian < -0.05 {
		out = append(out, fmt.Sprintf("相对行业中位数折价 %.0f%%（同行 %d 只）",
			-stock.Relative.PremiumVsMedian*100, stock.Relative.Comparables))
	}
	if stock.Relative.Quadrant == QuadrantCheapGood {
		out = append(out, "估值折价且基本面优于行业（方案认可的唯一高性价比组合）")
	}
	if stock.Growth.Score >= 65 {
		out = append(out, fmt.Sprintf("成长能力分 %.0f（营收 3 年 CAGR %.1f%%）",
			stock.Growth.Score, stock.Growth.RevenueCAGR3*100))
	}
	if stock.Quality.Score >= 70 {
		out = append(out, fmt.Sprintf("成长质量分 %.0f（扣非占比 %.2f、现金含量 %.2f）",
			stock.Quality.Score, stock.Quality.DeductedRatio, stock.Quality.CashContent))
	}
	if stock.Growth.ROE > 0.15 {
		out = append(out, fmt.Sprintf("ROE %.1f%%（资本回报优秀）", stock.Growth.ROE*100))
	}
	if score.Outlook.Available && score.Outlook.ReturnRiskRatio >= 1.5 {
		out = append(out, fmt.Sprintf("风险收益比 %.2f（预期 %+.1f%% / 悲观损失 %.1f%%）",
			score.Outlook.ReturnRiskRatio, score.Outlook.ExpectedReturn*100, score.Outlook.PessimisticLoss*100))
	}
	if fair := stock.FairByHorizon[score.Horizon.Key]; fair.Available && fair.SafetyMargin > 0.15 {
		out = append(out, fmt.Sprintf("安全边际 %.1f%%（基准合理价值 %.2f 元）",
			fair.SafetyMargin*100, fair.Base))
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// mainRisks 提取主要风险（最多 3 条）。
func mainRisks(stock StockScore, score HorizonScore) []string {
	out := make([]string, 0, 4)
	for _, flag := range score.RiskFlags {
		if flag.Severity == RiskNotice {
			continue // 一般预警放后面
		}
		out = append(out, flag.Title+"："+flag.Reason)
	}
	if stock.Quality.Score < 45 {
		out = append(out, fmt.Sprintf("成长质量分仅 %.0f，盈利质量存在明显问题", stock.Quality.Score))
	}
	if stock.Growth.Score < 40 {
		out = append(out, fmt.Sprintf("成长能力分仅 %.0f（营收单季同比 %+.1f%%）",
			stock.Growth.Score, stock.Growth.RevenueYoY*100))
	}
	if stock.Industry.PEPercentile > 70 {
		out = append(out, fmt.Sprintf("所属行业估值处于历史 %.0f%% 分位（偏贵），行业下行会压制个股",
			stock.Industry.PEPercentile))
	}
	for _, flag := range score.RiskFlags {
		if flag.Severity == RiskNotice {
			out = append(out, flag.Title+"："+flag.Reason)
		}
	}
	if len(out) == 0 {
		out = append(out, "未识别出显著风险，但请以置信度与合理价值区间为准")
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// ---------------------------------------------------------------------------
// 筛选
// ---------------------------------------------------------------------------

// ScreenFilter 是排名结果的筛选条件（方案 §9：按期限、行业、证券类型、
// 成长质量、估值区间、风险状态及数据置信度筛选）。
type ScreenFilter struct {
	// Industry 为空表示不限行业。
	Industry string `json:"industry"`
	// MinScore / MaxScore 是综合分区间（0 表示不限）。
	MinScore float64 `json:"minScore"`
	MaxScore float64 `json:"maxScore"`
	// MaxSelfPercentile 是自身估值分位上限（0 表示不限）。
	MaxSelfPercentile float64 `json:"maxSelfPercentile"`
	// MinSelfPercentile / MaxSelfPercentileRange 是估值区间筛选。
	MinSelfPercentile float64 `json:"minSelfPercentile"`
	// MinGrowthScore / MinQualityScore 是成长与质量分下限。
	MinGrowthScore  float64 `json:"minGrowthScore"`
	MinQualityScore float64 `json:"minQualityScore"`
	// Ratings 是限定的评级集合（空表示不限）。
	Ratings []Rating `json:"ratings"`
	// MinConfidence 是置信度下限。
	MinConfidence float64 `json:"minConfidence"`
	// OnlyNoCap 为真时只保留评级未被风险约束封顶的标的。
	OnlyNoCap bool `json:"onlyNoCap"`
	// MinMarketCap / MaxMarketCap 是市值区间（元，0 表示不限）。
	MinMarketCap float64 `json:"minMarketCap"`
	MaxMarketCap float64 `json:"maxMarketCap"`
	// RiskCodes 是必须命中的风险类型（空表示不限）。
	RiskCodes []RiskCode `json:"riskCodes"`
	// ExcludeRiskCodes 是必须排除的风险类型。
	ExcludeRiskCodes []RiskCode `json:"excludeRiskCodes"`
	// Limit 是返回条数上限（0 表示不限）。
	Limit int `json:"limit"`
}

// ApplyFilter 按条件筛出排名行。
func ApplyFilter(rows []RankRow, filter ScreenFilter) []RankRow {
	out := make([]RankRow, 0, len(rows))
	ratingSet := map[Rating]bool{}
	for _, rating := range filter.Ratings {
		ratingSet[rating] = true
	}
	riskSet := map[RiskCode]bool{}
	for _, code := range filter.RiskCodes {
		riskSet[code] = true
	}
	excludeRisk := map[RiskCode]bool{}
	for _, code := range filter.ExcludeRiskCodes {
		excludeRisk[code] = true
	}

	for _, row := range rows {
		if filter.Industry != "" && row.IndustryCode != filter.Industry && row.IndustryName != filter.Industry {
			continue
		}
		if filter.MinScore > 0 && row.Score < filter.MinScore {
			continue
		}
		if filter.MaxScore > 0 && row.Score > filter.MaxScore {
			continue
		}
		if filter.MaxSelfPercentile > 0 && (row.SelfPercentile <= 0 || row.SelfPercentile > filter.MaxSelfPercentile) {
			continue
		}
		if filter.MinSelfPercentile > 0 && row.SelfPercentile < filter.MinSelfPercentile {
			continue
		}
		if filter.MinGrowthScore > 0 && row.GrowthScore < filter.MinGrowthScore {
			continue
		}
		if filter.MinQualityScore > 0 && row.QualityScore < filter.MinQualityScore {
			continue
		}
		if len(ratingSet) > 0 && !ratingSet[row.Rating] {
			continue
		}
		if filter.MinConfidence > 0 && row.Confidence.Score < filter.MinConfidence {
			continue
		}
		if filter.OnlyNoCap && row.RatingCapped {
			continue
		}
		if filter.MinMarketCap > 0 && row.MarketCap < filter.MinMarketCap {
			continue
		}
		if filter.MaxMarketCap > 0 && row.MarketCap > filter.MaxMarketCap {
			continue
		}
		if len(riskSet) > 0 {
			hit := false
			for _, flag := range row.RiskFlags {
				if riskSet[flag.Code] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(excludeRisk) > 0 {
			blocked := false
			for _, flag := range row.RiskFlags {
				if excludeRisk[flag.Code] {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}
		out = append(out, row)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out
}
