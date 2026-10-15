package valuation

import (
	"fmt"
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 成长能力 与 成长质量
//
// 方案 §4.2 明确要求"将成长速度与成长质量分开评价"，所以这里产出**两个独立
// 结构体**（Growth / Quality），各自有独立得分。它们最终进两个不同的评分维度
// （DimGrowth / DimQuality），不允许把质量当成成长的系数糊在一起。
//
// 方案 §4.1 还要求"不能只依据单年增长率判断成长能力"，必须识别：
//
//	低基数 / 并购扩张 / 周期反弹 / 一次性收益
//
// 这四种情形在下面分别有独立的判定与说明文案。
// ---------------------------------------------------------------------------

// Growth 是成长能力评估结果。
type Growth struct {
	// Score 是 0~100 的成长能力得分。
	Score float64 `json:"score"`
	// Available 表示是否有足够数据给出成长评分。
	Available bool `json:"available"`
	// Note 说明为何不可用。
	Note string `json:"note,omitempty"`

	// RevenueYoY 是最近单季营收同比。
	RevenueYoY float64 `json:"revenueYoY"`
	// ProfitYoY 是最近单季归母净利润同比。
	ProfitYoY float64 `json:"profitYoY"`
	// DeductedYoY 是最近单季扣非净利润同比。
	DeductedYoY float64 `json:"deductedYoY"`
	// RevenueCAGR3 / RevenueCAGR5 是营收 3/5 年复合增速（年报口径）。
	RevenueCAGR3 float64 `json:"revenueCAGR3"`
	RevenueCAGR5 float64 `json:"revenueCAGR5"`
	// ProfitCAGR3 是归母净利润 3 年复合增速。
	ProfitCAGR3 float64 `json:"profitCAGR3"`

	// RevenueYoYSeries 是最近 8 个单季的营收同比（升序），用于成长持续性。
	RevenueYoYSeries []float64 `json:"-"`
	// ProfitYoYSeries 是最近 8 个单季的净利润同比（升序）。
	ProfitYoYSeries []float64 `json:"-"`
	// Consistency 是同比为正的季度占比（成长持续性）。
	Consistency float64 `json:"consistency"`
	// VolatilityOfGrowth 是同比序列的标准差（越低越稳）。
	VolatilityOfGrowth float64 `json:"volatilityOfGrowth"`

	// OCFGrowth 是经营现金流 TTM 同比。
	OCFGrowth float64 `json:"ocfGrowth"`
	// FCFPositive 表示自由现金流为正。
	FCFPositive bool `json:"fcfPositive"`

	// ROE 与增量资本回报。
	ROE float64 `json:"roe"`
	// IncrementalROIC 是增量投入资本回报率的近似。
	IncrementalROIC float64 `json:"incrementalROIC"`

	// Flags 是识别出来的"需要警惕的成长形态"。
	Flags []GrowthFlag `json:"flags"`
}

// GrowthFlag 是识别出的特殊成长形态（方案 §4.1 的四类）。
type GrowthFlag struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// 成长形态标识。
const (
	FlagLowBase      = "low_base"     // 低基数
	FlagAcquisition  = "acquisition"  // 并购扩张
	FlagCyclical     = "cyclical"     // 周期反弹
	FlagOneOff       = "one_off"      // 一次性收益
	FlagTurnaround   = "turnaround"   // 扭亏
	FlagDecelerating = "decelerating" // 增长减速
)

// Quality 是成长质量与盈利质量评估结果。
//
// 与 Growth 完全分开：高增长可以伴随低质量，低增长也可以伴随高质量。
type Quality struct {
	// Score 是 0~100 的成长质量得分。
	Score float64 `json:"score"`
	// Available 表示是否有足够数据。
	Available bool `json:"available"`
	// Note 说明为何不可用。
	Note string `json:"note,omitempty"`

	// DeductedRatio 是扣非净利润 / 归母净利润（越高说明利润越"实"）。
	DeductedRatio float64 `json:"deductedRatio"`
	// CashContent 是经营现金流 / 归母净利润（利润的现金含量）。
	CashContent float64 `json:"cashContent"`
	// DebtRatio 是资产负债率。
	DebtRatio float64 `json:"debtRatio"`
	// DebtChange 是资产负债率相对上年同期的变化（正数=加杠杆）。
	DebtChange float64 `json:"debtChange"`
	// FinancingDependence 是筹资活动现金流净额 / 总资产（正数=持续净融资）。
	FinancingDependence float64 `json:"financingDependence"`
	// MainBusinessRatio 是营业利润 / 利润总额（越高说明利润越来自主营）。
	MainBusinessRatio float64 `json:"mainBusinessRatio"`
	// GrossMargin 与 GrossMarginChange 是毛利率及其同比变化。
	GrossMargin       float64 `json:"grossMargin"`
	GrossMarginChange float64 `json:"grossMarginChange"`

	// Issues 是质量问题的逐条说明（带具体数值）。
	Issues []QualityIssue `json:"issues"`
}

// QualityIssue 是一条成长/盈利质量问题。
type QualityIssue struct {
	Code     string  `json:"code"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	Severity float64 `json:"severity"` // 0~1，扣分权重
}

// 质量问题标识。
const (
	IssueLowDeducted     = "low_deducted"      // 扣非占比低：利润靠非经常性收益
	IssueWeakCash        = "weak_cash"         // 现金流不支持利润
	IssueHighDebt        = "high_debt"         // 依赖过度负债
	IssuePersistentFin   = "persistent_fin"    // 持续融资
	IssueMarginErosion   = "margin_erosion"    // 毛利率下滑
	IssueNotMainBusiness = "not_main_business" // 利润不来自主营
	IssueVolatileProfit  = "volatile_profit"   // 利润波动大
)

// GrowthInput 是成长评估的输入（由数据层组装）。
type GrowthInput struct {
	AsOf         AsOf
	Singles      []SingleQuarter
	PrevYearAsOf AsOf // 上年同期时点（用于算资产负债率变化等）
	Params       Params
	// IndustryMedianRevenueYoY / IndustryMedianProfitYoY 是行业同期中位数增速，
	// 用来识别"周期反弹"（自己涨、行业也在涨 → 大概率是行业性的）。
	IndustryMedianRevenueYoY float64
	IndustryMedianProfitYoY  float64
	HasIndustryMedian        bool
}

// EvaluateGrowth 计算成长能力与成长质量。
func EvaluateGrowth(input GrowthInput) (Growth, Quality) {
	return evaluateGrowth(input), evaluateQuality(input)
}

func evaluateGrowth(input GrowthInput) Growth {
	out := Growth{}
	asOf := input.AsOf
	if asOf.LatestPeriod == "" || len(asOf.Reports) == 0 {
		out.Note = "没有可用财报（评估日前无已披露报告期）"
		return out
	}

	// ---- 单季同比 ----
	out.RevenueYoYSeries = QuarterYoYSeries(input.Singles, ItemRevenue, 8)
	out.ProfitYoYSeries = QuarterYoYSeries(input.Singles, ItemNetProfit, 8)
	if len(out.RevenueYoYSeries) > 0 {
		out.RevenueYoY = out.RevenueYoYSeries[len(out.RevenueYoYSeries)-1]
	}
	if len(out.ProfitYoYSeries) > 0 {
		out.ProfitYoY = out.ProfitYoYSeries[len(out.ProfitYoYSeries)-1]
	}
	if value, ok := YoY(input.Singles, ItemNetProfitDeduct, 0); ok {
		out.DeductedYoY = value
	}
	// ---- 经营现金流同比（TTM 口径，因为现金流缺季不能拆单季同比）----
	if previous, ok := earlierAsOfReports(asOf, 4); ok {
		if now, okNow := ttmOf(asOf.Reports, ItemNetCashOperate); okNow {
			if before, okBefore := ttmOf(previous, ItemNetCashOperate); okBefore && before != 0 {
				out.OCFGrowth = now/before - 1
			}
		}
	}
	out.FCFPositive = asOf.TTM.FreeCashFlow > 0

	// ---- 多年 CAGR（年报口径）----
	revenueAnnuals := AnnualValues(asOf.Reports, ItemRevenue, 6)
	profitAnnuals := AnnualValues(asOf.Reports, ItemNetProfit, 4)
	if len(revenueAnnuals) >= 4 {
		if value, ok := CAGR(revenueAnnuals[len(revenueAnnuals)-4], revenueAnnuals[len(revenueAnnuals)-1], 3); ok {
			out.RevenueCAGR3 = value
		}
	}
	if len(revenueAnnuals) >= 6 {
		if value, ok := CAGR(revenueAnnuals[0], revenueAnnuals[len(revenueAnnuals)-1], 5); ok {
			out.RevenueCAGR5 = value
		}
	}
	if len(profitAnnuals) >= 4 {
		if value, ok := CAGR(profitAnnuals[len(profitAnnuals)-4], profitAnnuals[len(profitAnnuals)-1], 3); ok {
			out.ProfitCAGR3 = value
		}
	}

	// ---- 持续性 ----
	if len(out.RevenueYoYSeries) > 0 {
		positive := 0
		for _, value := range out.RevenueYoYSeries {
			if value > 0 {
				positive++
			}
		}
		out.Consistency = float64(positive) / float64(len(out.RevenueYoYSeries))
		out.VolatilityOfGrowth = stddev(out.RevenueYoYSeries)
	}

	// ---- 资本回报 ----
	out.ROE = returnOnEquity(input.AsOf, input.PrevYearAsOf)
	out.IncrementalROIC = incrementalROIC(input.AsOf, input.PrevYearAsOf)

	// ---- 成长形态识别 ----
	out.Flags = detectGrowthFlags(input, out)

	if len(out.RevenueYoYSeries) == 0 && out.RevenueCAGR3 == 0 && !out.FCFPositive {
		out.Note = "缺少足够的连续报告期，无法判断成长趋势"
		return out
	}
	out.Available = true
	out.Score = growthScore(out)
	return out
}

// detectGrowthFlags 识别方案 §4.1 要求的四类特殊形态（外加扭亏与减速）。
//
// 这些不是"扣分项"，而是**结论里必须点名的口径说明** —— 例如"高增长来自低基数"
// 会直接改变成长得分的解读，所以它既进 Flags 也参与打分。
func detectGrowthFlags(input GrowthInput, growth Growth) []GrowthFlag {
	flags := make([]GrowthFlag, 0, 3)

	// ① 低基数：上年同期几乎为零或为负，导致本期同比虚高
	if len(input.Singles) >= 5 {
		current := input.Singles[len(input.Singles)-1]
		previous := findByYearQuarter(input.Singles, current.Year-1, current.Quarter)
		if previous != nil {
			before := previous.Items[ItemNetProfit]
			now := current.Items[ItemNetProfit]
			if before <= 0 && now > 0 {
				flags = append(flags, GrowthFlag{
					Code:  FlagTurnaround,
					Title: "扭亏",
					Detail: fmt.Sprintf("去年同期净利润 %.2f 亿（≤0），本期 %.2f 亿；"+
						"增长率在此情形下没有数学意义，成长判断改用「是否持续盈利」这一判据",
						before/1e8, now/1e8),
				})
			} else if before > 0 && now > 0 && now/before > 1.5 && before/1e8 < 0.5 {
				flags = append(flags, GrowthFlag{
					Code:  FlagLowBase,
					Title: "低基数高增长",
					Detail: fmt.Sprintf("去年同期基数极低（%.2f 亿），本期同比 +%.1f%%；"+
						"该增速的参考价值低于绝对值", before/1e8, (now/before-1)*100),
				})
			}
		}
	}

	// ② 并购扩张：营收高增但经营现金流没跟上，且总资产明显跳升
	if growth.RevenueYoY > 0.25 && growth.OCFGrowth < growth.RevenueYoY-0.15 {
		flags = append(flags, GrowthFlag{
			Code:  FlagAcquisition,
			Title: "增长未获现金流印证",
			Detail: fmt.Sprintf("营收同比 +%.1f%% 而经营现金流同比 %+.1f%%；"+
				"并购扩张或放宽信用政策都可能造成这种背离", growth.RevenueYoY*100, growth.OCFGrowth*100),
		})
	}

	// ③ 周期反弹：自己与行业中位数同向且幅度接近
	if input.HasIndustryMedian && growth.RevenueYoY > 0.15 &&
		input.IndustryMedianRevenueYoY > 0.10 &&
		math.Abs(growth.RevenueYoY-input.IndustryMedianRevenueYoY) < 0.15 {
		flags = append(flags, GrowthFlag{
			Code:  FlagCyclical,
			Title: "可能是行业性反弹",
			Detail: fmt.Sprintf("个股营收同比 +%.1f%%、行业中位数 +%.1f%%，两者接近且同向；"+
				"该增长更像行业景气而非公司自身优势", growth.RevenueYoY*100, input.IndustryMedianRevenueYoY*100),
		})
	}

	// ④ 一次性收益：扣非同比明显弱于归母同比
	if growth.ProfitYoY-growth.DeductedYoY > 0.20 {
		flags = append(flags, GrowthFlag{
			Code:  FlagOneOff,
			Title: "增长含一次性收益",
			Detail: fmt.Sprintf("归母净利润同比 +%.1f%%、扣非同比 %+.1f%%，差值 %.1f 个百分点；"+
				"非经常性损益贡献了增长的主要部分", growth.ProfitYoY*100, growth.DeductedYoY*100,
				(growth.ProfitYoY-growth.DeductedYoY)*100),
		})
	}

	// ⑤ 减速：近三期同比连续下降
	if len(growth.RevenueYoYSeries) >= 3 {
		series := growth.RevenueYoYSeries
		last := series[len(series)-3:]
		if last[0] > last[1] && last[1] > last[2] {
			flags = append(flags, GrowthFlag{
				Code:  FlagDecelerating,
				Title: "增长连续减速",
				Detail: fmt.Sprintf("最近三个单季营收同比依次为 %.1f%% → %.1f%% → %.1f%%，逐期下滑",
					last[0]*100, last[1]*100, last[2]*100),
			})
		}
	}

	return flags
}

func evaluateQuality(input GrowthInput) Quality {
	out := Quality{}
	asOf := input.AsOf
	if asOf.LatestPeriod == "" {
		out.Note = "没有可用财报"
		return out
	}
	params := input.Params
	quality := params.QualityThresholds

	netProfit := asOf.TTM.NetProfit
	deducted := asOf.TTM.NetProfitDeduct
	cash := asOf.TTM.CashOperate

	// ---- 扣非占比 ----
	if netProfit > 0 && deducted != 0 {
		out.DeductedRatio = deducted / netProfit
	}
	// ---- 现金含量 ----
	// ⚠ 条件从"必须满 4 个季度"放宽为"至少 2 个季度"：现金流科目缺季很常见
	// （实测 capex 覆盖率 5553/5581），而现金含量是**判断利润质量的关键闸门** ——
	// 要求满 4 季会让大量公司在这一项上空白，风险约束「高增长但现金流差」
	// 就永远触发不了。用几个季度拼出来的比值由 CashOperateQuarters 记录，
	// 完整度不足时在置信度里扣分（见 estimateValuationConfidence）。
	if netProfit > 0 && asOf.Items[ItemNetCashOperate] != 0 && asOf.TTM.CashOperateQuarters >= 2 {
		out.CashContent = cash / netProfit
	}
	// ---- 负债 ----
	assets := asOf.Items[ItemTotalAssets]
	liabilities := asOf.Items[ItemTotalLiabilities]
	if assets > 0 {
		out.DebtRatio = liabilities / assets
		// 筹资活动现金流净额为正且不小 → 增长靠外部输血。
		// 用 TTM 口径（流量）除以时点总资产（存量）作近似比例，只用于分档比较。
		out.FinancingDependence = asOf.Items[ItemNetCashFinance] / assets
	}
	if input.PrevYearAsOf.Items[ItemTotalAssets] > 0 {
		previous := input.PrevYearAsOf.Items[ItemTotalLiabilities] / input.PrevYearAsOf.Items[ItemTotalAssets]
		out.DebtChange = out.DebtRatio - previous
	}
	// ---- 利润来源 ----
	totalProfit := asOf.Items[ItemTotalProfit]
	operatingProfit := asOf.Items[ItemOperatingProfit]
	if totalProfit > 0 {
		out.MainBusinessRatio = operatingProfit / totalProfit
	}
	// ---- 毛利率 ----
	out.GrossMargin = grossMarginOf(asOf)
	if previous := grossMarginOf(input.PrevYearAsOf); previous != 0 && out.GrossMargin != 0 {
		out.GrossMarginChange = out.GrossMargin - previous
	}

	// ---- 逐条判问题 ----
	if out.DeductedRatio != 0 && out.DeductedRatio < quality.DeductedRatioMin {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssueLowDeducted,
			Title:    "利润含较多非经常性损益",
			Detail:   fmt.Sprintf("扣非净利润 / 归母净利润 = %.2f（低于 %.2f）；非经常性收益不可持续", out.DeductedRatio, quality.DeductedRatioMin),
			Severity: clamp01((quality.DeductedRatioMin - out.DeductedRatio) / quality.DeductedRatioMin),
		})
	}
	if out.CashContent != 0 && out.CashContent < params.RiskThresholds.CashContentMin {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssueWeakCash,
			Title:    "利润缺少现金流支持",
			Detail:   fmt.Sprintf("经营现金流 / 归母净利润 = %.2f（低于 %.2f）；利润没有变成现金", out.CashContent, params.RiskThresholds.CashContentMin),
			Severity: clamp01((params.RiskThresholds.CashContentMin - out.CashContent) / params.RiskThresholds.CashContentMin),
		})
	}
	if out.DebtRatio > quality.DebtRatioMax {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssueHighDebt,
			Title:    "资产负债率偏高",
			Detail:   fmt.Sprintf("资产负债率 %.1f%%（高于 %.1f%%）；增长对负债的依赖度高", out.DebtRatio*100, quality.DebtRatioMax*100),
			Severity: clamp01((out.DebtRatio - quality.DebtRatioMax) / (1 - quality.DebtRatioMax)),
		})
	}
	if out.FinancingDependence > 0.05 {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssuePersistentFin,
			Title:    "依赖持续融资",
			Detail:   fmt.Sprintf("近一年筹资活动现金流净额相当于总资产的 %.1f%%；增长靠外部输血", out.FinancingDependence*100),
			Severity: clamp01(out.FinancingDependence / 0.20),
		})
	}
	if out.GrossMarginChange < -0.03 {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssueMarginErosion,
			Title:    "毛利率下滑",
			Detail:   fmt.Sprintf("毛利率同比变化 %+.1f 个百分点；盈利能力在被侵蚀", out.GrossMarginChange*100),
			Severity: clamp01(-out.GrossMarginChange / 0.10),
		})
	}
	if out.MainBusinessRatio != 0 && out.MainBusinessRatio < 0.85 {
		out.Issues = append(out.Issues, QualityIssue{
			Code:     IssueNotMainBusiness,
			Title:    "利润不主要来自主营",
			Detail:   fmt.Sprintf("营业利润 / 利润总额 = %.2f；投资收益、政府补助等占比偏高", out.MainBusinessRatio),
			Severity: clamp01((0.85 - out.MainBusinessRatio) / 0.85),
		})
	}
	if len(input.Singles) >= 6 {
		profits := make([]float64, 0, len(input.Singles))
		for _, single := range input.Singles {
			if value, ok := single.Items[ItemNetProfit]; ok {
				profits = append(profits, value)
			}
		}
		if len(profits) >= 4 {
			mean := 0.0
			for _, value := range profits {
				mean += value
			}
			mean /= float64(len(profits))
			if mean > 0 && stddev(profits)/mean > 0.60 {
				out.Issues = append(out.Issues, QualityIssue{
					Code:     IssueVolatileProfit,
					Title:    "盈利波动较大",
					Detail:   fmt.Sprintf("单季净利润的变异系数 %.2f（>0.60）；盈利可预测性低", stddev(profits)/mean),
					Severity: clamp01((stddev(profits)/mean - 0.60) / 0.60),
				})
			}
		}
	}

	out.Available = assets > 0 || netProfit != 0
	out.Score = qualityScore(out)
	return out
}

// growthScore 把成长指标折成 0~100。
//
// 权重设计：短期同比 30 + 3 年 CAGR 25 + 持续性 20 + 现金流成长 15 + 资本回报 10。
// 5 年 CAGR 不进权重，只作展示（覆盖率低：需要 6 个年报期，次新股都没有）。
func growthScore(g Growth) float64 {
	score := 0.0
	// 同比：+30% 记满分，0% 记 45 分（不涨不跌不算差），−30% 记 0 分
	score += 30 * growthCurve(g.RevenueYoY, 0.30, 0.45)
	score += 25 * growthCurve(g.RevenueCAGR3, 0.30, 0.40)
	// 持续性：全部季度为正记满分
	score += 20 * g.Consistency
	// 现金流成长
	score += 15 * growthCurve(g.OCFGrowth, 0.30, 0.40)
	// 资本回报：ROE 15% 记满分
	roeCurve := 0.0
	if g.ROE > 0 {
		roeCurve = math.Min(g.ROE/0.15, 1.0)
	}
	score += 10 * roeCurve

	// 成长形态修正：低基数/一次性收益/周期反弹都要打折 ——
	// 它们意味"这个增速不可外推"，而成长评分是要外推的
	penalty := 0.0
	for _, flag := range g.Flags {
		switch flag.Code {
		case FlagLowBase:
			penalty += 0.12
		case FlagOneOff:
			penalty += 0.15
		case FlagCyclical:
			penalty += 0.10
		case FlagTurnaround:
			penalty += 0.10
		case FlagDecelerating:
			penalty += 0.08
		}
	}
	if penalty > 0.35 {
		penalty = 0.35
	}
	score *= 1 - penalty

	// 减速额外压一档：连续下滑对"未来成长"的指示性很强
	for _, flag := range g.Flags {
		if flag.Code == FlagDecelerating {
			score = math.Min(score, 60)
		}
	}
	return clampScore(score)
}

// qualityScore 把质量指标折成 0~100。
//
// 与 growthScore 不同，这里以 100 为起点**逐条扣分**：质量问题的存在是硬事实，
// 而"质量好"并没有一个可以线性外推的指标。
func qualityScore(q Quality) float64 {
	score := 100.0
	for _, issue := range q.Issues {
		score -= 25 * issue.Severity
	}
	// 扣非占比与现金含量再各给一次温和调整（没有触发 issue 时也有区分度）
	if q.DeductedRatio > 0 {
		if q.DeductedRatio >= 0.95 {
			score += 0
		} else {
			score -= 15 * clamp01((0.95-q.DeductedRatio)/0.95)
		}
	}
	if q.CashContent > 0 && q.CashContent > 1.2 {
		score += 5
	}
	if q.GrossMarginChange > 0.02 {
		score += 5
	}
	return clampScore(score)
}

// growthCurve 把增速映射到 0~1：excellent 及以上为 1，0% 为 neutral，−30% 为 0。
func growthCurve(value, excellent, neutral float64) float64 {
	switch {
	case value >= excellent:
		return 1
	case value >= 0:
		// 0 ~ excellent 之间从 neutral 线性升到 1
		return neutral + (1-neutral)*(value/excellent)
	case value <= -0.30:
		return 0
	default:
		// −30% ~ 0 之间从 0 升到 neutral
		return neutral * (1 + value/0.30)
	}
}

// returnOnEquity 用 TTM 净利润 ÷ 平均归母净资产算 ROE。
//
// 为什么不用数据湖 indicator 里的 `roe`：那一列是**年内累计**口径
// （Q3 行给的是前三季 ROE），直接拿来做 TTM 比较会系统性偏低。
// 自己用 TTM 净利润 ÷ 平均净资产得到的口径才与 PE/PB 自洽。
func returnOnEquity(asOf, prev AsOf) float64 {
	profit := asOf.TTM.NetProfit
	equity := asOf.Items[ItemTotalEquity]
	previousEquity := prev.Items[ItemTotalEquity]
	if profit == 0 || equity <= 0 {
		return 0
	}
	average := equity
	if previousEquity > 0 {
		average = (equity + previousEquity) / 2
	}
	if average <= 0 {
		return 0
	}
	return profit / average
}

// incrementalROIC 近似"新增投入资本的回报率" = ΔNOPAT / Δ投入资本。
//
// 用 Δ(TTM 营业利润 × (1−实际税率)) ÷ Δ(总资产 − 货币资金) 近似。
// ⚠ 这是**近似**：本机没有"折旧摊销"与"有息负债"科目，所以既不能算严格的
// NOPAT，也不能算严格的投资资本。界面上必须写明是近似口径（方案 §16）。
func incrementalROIC(asOf, prev AsOf) float64 {
	profitNow := asOf.TTM.OperatingProfit
	profitBefore := prev.TTM.OperatingProfit
	assetsNow := asOf.Items[ItemTotalAssets]
	assetsBefore := prev.Items[ItemTotalAssets]
	if profitNow == 0 || profitBefore == 0 || assetsNow <= 0 || assetsBefore <= 0 {
		return 0
	}
	// 实际税率：所得税 ÷ 利润总额（为 0 或缺失时按 25% 法定税率）
	taxRate := 0.25
	if totalProfit := asOf.Items[ItemTotalProfit]; totalProfit > 0 {
		if tax := asOf.Items[ItemIncomeTax]; tax > 0 {
			taxRate = clamp01(tax / totalProfit)
		}
	}
	deltaNOPAT := (profitNow - profitBefore) * (1 - taxRate)
	deltaCapital := assetsNow - assetsBefore
	if deltaCapital <= 0 {
		// 投入资本没有增加（或减少）时，"增量回报"无从谈起；
		// 这时返回 0 而不是一个凭空的巨大数字
		return 0
	}
	return deltaNOPAT / deltaCapital
}

// grossMarginOf 取毛利率：优先用数据湖 indicator 里的 gross_margin（YTD 口径，
// 做同比变化是自洽的），缺失时用 (营收−营业成本)/营收 现算。
func grossMarginOf(asOf AsOf) float64 {
	if value, ok := asOf.Items[ItemGrossMargin]; ok && value != 0 {
		// 实测该列是百分数（如 91.5 表示 91.5%），大于 1 就折成小数
		if math.Abs(value) > 1.5 {
			return value / 100
		}
		return value
	}
	revenue := asOf.TTM.Revenue
	if revenue > 0 && asOf.TTM.OperatingCost > 0 {
		return (revenue - asOf.TTM.OperatingCost) / revenue
	}
	return 0
}

// earlierAsOfReports 返回"往前推 n 个季度"的报告期集合。
//
// 用途：算同比。注意这里返回的是**报告期切片**而不是一个完整的 AsOf ——
// 因为算同比只需要同一批科目，不需要重跑整套时点逻辑。
func earlierAsOfReports(asOf AsOf, quartersBack int) ([]Report, bool) {
	if len(asOf.Reports) == 0 || quartersBack <= 0 {
		return nil, false
	}
	latest := asOf.Reports[len(asOf.Reports)-1]
	targetYear, targetQuarter := latest.Year, latest.Quarter-quartersBack
	for targetQuarter <= 0 {
		targetYear--
		targetQuarter += 4
	}
	keep := make([]Report, 0, len(asOf.Reports))
	for _, report := range asOf.Reports {
		if ReportKey(report.Year, report.Quarter) <= ReportKey(targetYear, targetQuarter) {
			keep = append(keep, report)
		}
	}
	if len(keep) == 0 {
		return nil, false
	}
	return keep, true
}

// medianOf 是 Median 的别名，方便在聚合各处统一读法。
func medianOf(values []float64) float64 { return Median(values) }

// positiveOnly 过滤出 > 0 的样本（算 PE/PS 中位数前必须做的一步）。
func positiveOnly(values []float64) []float64 {
	out := make([]float64, 0, len(values))
	for _, value := range values {
		if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			out = append(out, value)
		}
	}
	return out
}

// sortedCopy 返回升序副本（不改入参）。
func sortedCopy(values []float64) []float64 {
	out := make([]float64, len(values))
	copy(out, values)
	sort.Float64s(out)
	return out
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func clampScore(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
