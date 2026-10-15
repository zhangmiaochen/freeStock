package valuation

import (
	"fmt"
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 第二层与第三层估值：行业整体估值 + 个股相对行业/同行
//
// 方案 §5.2 的硬性要求：**行业平均估值、行业中位数和行业历史分位需要分别保留，
// 不能将三者混为一个指标**。所以 IndustryValuation 里有三个独立字段，
// 界面上也分三列展示。
//
// 另一条硬性要求：行业整体估值**不能简单采用所有公司估值倍数的算术平均**。
// 这里用"先加总市值、再加总盈利、最后相除"的调和加权口径（见 weightedPE），
// 它等价于按市值加权的倒数平均，也是行业指数 PE 的标准算法。
// ---------------------------------------------------------------------------

// CompanyValuation 是行业聚合与同行比较用的一家公司快照。
type CompanyValuation struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// IndustryCode / IndustryName 是行业归属。
	IndustryCode string `json:"industryCode"`
	IndustryName string `json:"industryName"`

	MarketCap      float64 `json:"marketCap"`
	FloatMarketCap float64 `json:"floatMarketCap"`

	PE float64 `json:"pe"`
	PB float64 `json:"pb"`
	PS float64 `json:"ps"`

	// TTM 是参与聚合的盈利与现金流（用于调和加权与行业盈利结构）。
	NetProfit float64 `json:"-"`
	Revenue   float64 `json:"-"`
	Equity    float64 `json:"-"`

	// ROE、成长与质量得分（由各自模块算出，供相对估值判断"溢价是否有支撑"）。
	ROE          float64 `json:"roe"`
	GrowthScore  float64 `json:"growthScore"`
	QualityScore float64 `json:"qualityScore"`
	DebtRatio    float64 `json:"debtRatio"`
	RevenueYoY   float64 `json:"revenueYoY"`
	ProfitYoY    float64 `json:"profitYoY"`

	// SelfPercentile 是自身历史估值分位（用于"个股与行业都低"的判断）。
	SelfPercentile float64 `json:"selfPercentile"`
	// SelfAvailable 表示自身分位是否可用。
	SelfAvailable bool `json:"-"`
}

// IndustryValuation 是行业整体估值环境。
type IndustryValuation struct {
	Code string `json:"code"`
	Name string `json:"name"`

	// ---- 三者分列，绝不合并 ----
	// Weighted 是自由流通市值加权的调和口径（≈ 行业指数 PE）。
	WeightedPE float64 `json:"weightedPE"`
	WeightedPB float64 `json:"weightedPB"`
	WeightedPS float64 `json:"weightedPS"`
	// Median 是成分股中位数（抗极端值）。
	MedianPE float64 `json:"medianPE"`
	MedianPB float64 `json:"medianPB"`
	MedianPS float64 `json:"medianPS"`
	// 历史分位（自建的行业估值序列分位）。
	PEPercentile float64 `json:"pePercentile"`
	PBPercentile float64 `json:"pbPercentile"`
	// Band 是行业 PE 的历史区间。
	PEBand ValuationBand `json:"peBand"`

	// Members 是参与聚合的成分股数。
	Members int `json:"members"`
	// LossMembers 是 TTM 净利润 ≤ 0 的成分股数。
	LossMembers int `json:"lossMembers"`
	// LossRatio 是亏损公司占比（过高时行业 PE 不可信）。
	LossRatio float64 `json:"lossRatio"`

	// 盈利结构。
	ROEMedian      float64 `json:"roeMedian"`
	RevenueYoYMed  float64 `json:"revenueYoYMedian"`
	ProfitYoYMed   float64 `json:"profitYoYMedian"`
	GrossMarginMed float64 `json:"grossMarginMedian"`

	// 集中度：前 3 大权重公司对行业市值的贡献占比。
	Top3Weight float64 `json:"top3Weight"`
	// PEExTop3 是剔除前 3 大权重后的行业中位数 PE（用于识别"被少数公司拉动"）。
	PEExTop3 float64 `json:"peExTop3"`

	// Stage 是行业所处阶段：growth / mature / decline / cyclical。
	Stage string `json:"stage"`
	// StageNote 是这个判定的依据文案。
	StageNote string `json:"stageNote"`

	// CyclicalWarning 为真表示"PE 低 + ROE 高位"，是周期顶部的典型特征。
	CyclicalWarning bool   `json:"cyclicalWarning"`
	CyclicalReason  string `json:"cyclicalReason,omitempty"`

	// Available / Note：成分不足时不可用。
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`

	// Score 是 0~100 的行业估值环境得分（越高越便宜）。
	Score float64 `json:"score"`
}

// weightedPE 用"先加总市值再加总盈利"算调和加权 PE。
//
// 为什么不用 Σ(市值ᵢ/W) × PEᵢ：那个算法在成分股盈利差异大时会失真 ——
// 一家 PE 200 倍的小公司按权重乘进去会被"平均"掉，而实际上它对行业整体
// 盈利的贡献极小。调和加权（总市值 ÷ 总盈利）才是行业指数 PE 的口径，
// 也自动让亏损公司退出分母（它们的市值仍然算在分子里，这一点的含义
// 由 LossRatio 单独说明，不能假装不存在）。
func weightedPE(companies []CompanyValuation, useFloatCap bool) (pe, pb, ps float64, ok bool) {
	totalCap, totalProfit, totalEquity, totalRevenue := 0.0, 0.0, 0.0, 0.0
	for _, company := range companies {
		cap := company.MarketCap
		if useFloatCap && company.FloatMarketCap > 0 {
			cap = company.FloatMarketCap
		}
		if cap <= 0 {
			continue
		}
		totalCap += cap
		if company.NetProfit > 0 {
			totalProfit += company.NetProfit
		}
		if company.Equity > 0 {
			totalEquity += company.Equity
		}
		if company.Revenue > 0 {
			totalRevenue += company.Revenue
		}
	}
	if totalCap <= 0 {
		return 0, 0, 0, false
	}
	if totalProfit > 0 {
		pe = totalCap / totalProfit
	}
	if totalEquity > 0 {
		pb = totalCap / totalEquity
	}
	if totalRevenue > 0 {
		ps = totalCap / totalRevenue
	}
	return pe, pb, ps, true
}

// AggregateIndustry 聚合一个行业的估值环境。
//
// history 是该行业的历史加权 PE 序列（升序，可为空 —— 本机行业归属只有
// 5 个快照，早期日期没有行业归属，所以历史分位经常算不出来，此时
// PEPercentile 为 0 并在 Note 里说明）。
func AggregateIndustry(code, name string, companies []CompanyValuation, history []float64, params Params) IndustryValuation {
	out := IndustryValuation{Code: code, Name: name, Members: len(companies)}
	if len(companies) < params.IndustryMinMembers {
		out.Note = fmt.Sprintf("行业成分不足 %d 只（实际 %d 只），行业中位数与加权估值都不可靠；"+
			"该行业下的个股请改用全市场分位判断", params.IndustryMinMembers, len(companies))
		out.Score = 50
		return out
	}
	out.Available = true

	// ---- 加权（调和）口径 ----
	// 用自由流通市值加权更接近行业指数口径；缺流通市值时回落到总市值。
	out.WeightedPE, out.WeightedPB, out.WeightedPS, _ = weightedPE(companies, true)

	// ---- 中位数口径 ----
	pes := make([]float64, 0, len(companies))
	pbs := make([]float64, 0, len(companies))
	pss := make([]float64, 0, len(companies))
	roes := make([]float64, 0, len(companies))
	revenueYoys := make([]float64, 0, len(companies))
	profitYoys := make([]float64, 0, len(companies))
	for _, company := range companies {
		if company.PE > 0 {
			pes = append(pes, company.PE)
		} else {
			out.LossMembers++
		}
		if company.PB > 0 {
			pbs = append(pbs, company.PB)
		}
		if company.PS > 0 {
			pss = append(pss, company.PS)
		}
		if company.ROE != 0 {
			roes = append(roes, company.ROE)
		}
		if company.RevenueYoY != 0 {
			revenueYoys = append(revenueYoys, company.RevenueYoY)
		}
		if company.ProfitYoY != 0 {
			profitYoys = append(profitYoys, company.ProfitYoY)
		}
	}
	out.LossRatio = float64(out.LossMembers) / float64(len(companies))
	// 缩尾后再取中位数：中位数本身抗极端值，但缩尾能让"分位区间"不被一只
	// 千倍 PE 的股票拉爆（区间是要给人看的，必须可信）
	out.MedianPE = medianOf(Winsorize(pes, 0.02, 0.98))
	out.MedianPB = medianOf(Winsorize(pbs, 0.02, 0.98))
	out.MedianPS = medianOf(Winsorize(pss, 0.02, 0.98))
	out.ROEMedian = medianOf(roes)
	out.RevenueYoYMed = medianOf(revenueYoys)
	out.ProfitYoYMed = medianOf(profitYoys)

	// ---- 集中度 ----
	sortedByCap := make([]CompanyValuation, len(companies))
	copy(sortedByCap, companies)
	sort.Slice(sortedByCap, func(i, j int) bool { return sortedByCap[i].MarketCap > sortedByCap[j].MarketCap })
	totalCap := 0.0
	for _, company := range companies {
		totalCap += company.MarketCap
	}
	if totalCap > 0 {
		top := math.Min(3, float64(len(sortedByCap)))
		topCap := 0.0
		for i := 0; i < int(top); i++ {
			topCap += sortedByCap[i].MarketCap
		}
		out.Top3Weight = topCap / totalCap
	}
	if len(sortedByCap) > 3 {
		exTop := make([]float64, 0, len(sortedByCap)-3)
		for _, company := range sortedByCap[3:] {
			if company.PE > 0 {
				exTop = append(exTop, company.PE)
			}
		}
		out.PEExTop3 = medianOf(Winsorize(exTop, 0.02, 0.98))
	}

	// ---- 历史分位 ----
	valid := positiveOnly(history)
	if len(valid) >= 60 {
		if out.WeightedPE > 0 {
			out.PEPercentile, _ = Percentile(valid, out.WeightedPE, 60)
		}
		if out.WeightedPB > 0 {
			pbHistory := positiveOnly(history)
			out.PBPercentile, _ = Percentile(pbHistory, out.WeightedPB, 60)
		}
		out.PEBand = BuildBand(valid)
	} else {
		out.Note = fmt.Sprintf("行业历史估值样本不足（%d 个交易日，至少需要 60 个）："+
			"本机 industry_members 只有最近 5 个快照，更早的交易日在数据里没有行业归属，"+
			"所以行业历史分位无法时点正确重建；此处只给当前加权值与中位数", len(valid))
	}

	// ---- 行业阶段 ----
	out.Stage, out.StageNote = classifyStage(companies, out)

	// ---- 周期顶部预警：PE 低 + ROE 高位 ----
	if out.PEPercentile > 0 && out.PEPercentile < 35 && out.ROEMedian > 0.15 {
		out.CyclicalWarning = true
		out.CyclicalReason = fmt.Sprintf(
			"行业 PE 处于自身历史 %.0f%% 分位（偏低）而 ROE 中位数 %.1f%%（偏高）——"+
				"这是周期高点的典型组合：当前盈利可能是周期峰值，低 PE 不代表便宜",
			out.PEPercentile, out.ROEMedian*100)
	}

	out.Score = industryScore(out, params)
	return out
}

// classifyStage 判定行业所处阶段。
//
// 二维判定（方案 §4.3 要求区分"成长/成熟/衰退/周期波动"）：
//
//	高增速 + 低波动 → growth（成长）
//	低增速 + 低波动 → mature（成熟）
//	负增速 + 低波动 → decline（衰退）
//	高波动         → cyclical（周期波动，无论增速正负）
func classifyStage(companies []CompanyValuation, out IndustryValuation) (string, string) {
	growths := make([]float64, 0, len(companies))
	for _, company := range companies {
		if company.RevenueYoY != 0 {
			growths = append(growths, company.RevenueYoY)
		}
	}
	if len(growths) < 5 {
		return "unknown", "成分股成长数据不足，无法判断行业阶段"
	}
	median := medianOf(growths)
	dispersion := stddev(growths)

	// 波动阈值 0.35：A 股行业内部的营收增速标准差超过 35 个百分点时，
	// "行业整体"这个概念本身就不成立（成分股处在完全不同的景气位置）
	if dispersion > 0.35 {
		return "cyclical", fmt.Sprintf(
			"成分股营收增速离散度高（标准差 %.0f 个百分点，中位数 %.1f%%）——"+
				"行业内部景气分化明显，按周期波动处理，估值用双口径（TTM 与 5 年均值）",
			dispersion*100, median*100)
	}
	switch {
	case median >= 0.15:
		return "growth", fmt.Sprintf("成分股营收增速中位数 %.1f%%、离散度 %.0f 个百分点，处于成长阶段",
			median*100, dispersion*100)
	case median >= 0.0:
		return "mature", fmt.Sprintf("成分股营收增速中位数 %.1f%%、离散度 %.0f 个百分点，处于成熟阶段",
			median*100, dispersion*100)
	default:
		return "decline", fmt.Sprintf("成分股营收增速中位数 %.1f%%（为负），处于衰退阶段",
			median*100)
	}
}

// industryScore 把行业估值环境折成 0~100。
//
// 有历史分位时以分位为主（分位低 = 行业便宜 = 分高）。
//
// ⚠ 没有历史分位时**不能简单地给 50 分**：本机 `industry_members` 只有 5 个快照，
// 那种写法会让所有行业的"行业估值环境"维度都变成同一个常数，这个维度就
// 对最终排序毫无贡献了。所以退化为**横截面结构口径**，见
// industryScoreWithoutHistory（加权与中位 PE 的背离 + 集中度）。
//
// 明确不做的事：不拿"加权 PE 的绝对倍数"当便宜/贵的判据 —— 银行的 6 倍与
// 半导体的 60 倍都可能是合理的，横向比绝对倍数只会得出"银行永远最便宜"。
func industryScore(out IndustryValuation, params Params) float64 {
	var score float64
	if out.PEPercentile > 0 {
		score = 95 - out.PEPercentile*0.9
		// 亏损公司占比高 → 行业 PE 不可信，向中性收缩
		if out.LossRatio > 0.30 {
			score = 50 + (score-50)*(1-out.LossRatio)
		}
		// 集中度过高 → 加权值被少数公司主导，同样收缩
		if out.Top3Weight > 0.50 {
			score = 50 + (score-50)*0.6
		}
	} else {
		score, _ = industryScoreWithoutHistory(out)
	}
	// 周期顶部预警是明确的负面信号
	if out.CyclicalWarning {
		score -= 10
	}
	return clampScore(score)
}

// industryStructureNote 返回"无历史分位时"行业分的口径说明（供依据追溯）。
func industryStructureNote(out IndustryValuation) string {
	if out.PEPercentile > 0 {
		return ""
	}
	_, note := industryScoreWithoutHistory(out)
	return note
}

// ---------------------------------------------------------------------------
// 第三层：个股相对行业与同行
// ---------------------------------------------------------------------------

// 相对估值的四象限（方案 §5.3 的判定矩阵）。
const (
	QuadrantCheapGood     = "cheap_good"     // 便宜 + 基本面优于行业 → 估值折价机会
	QuadrantCheapBad      = "cheap_bad"      // 便宜 + 基本面弱于行业 → 合理折价或价值陷阱
	QuadrantExpensiveGood = "expensive_good" // 贵 + 基本面好 → 允许合理溢价，但要检验幅度
	QuadrantExpensiveBad  = "expensive_bad"  // 贵 + 基本面没有优势 → 估值风险高
	QuadrantMixed         = "mixed"          // 数据不足或差异不显著
)

// RelativeValuation 是个股相对行业与同行的估值评估。
type RelativeValuation struct {
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`

	IndustryCode string `json:"industryCode"`
	IndustryName string `json:"industryName"`

	// 对行业加权口径的折溢价（正数=溢价）。
	PremiumVsWeighted float64 `json:"premiumVsWeighted"`
	// 对行业中位数的折溢价。
	PremiumVsMedian float64 `json:"premiumVsMedian"`
	// 个股 PE 在行业成分中的分位（0~100，越低越便宜）。
	PEPercentileInIndustry float64 `json:"pePercentileInIndustry"`
	// 个股 PB 在行业成分中的分位。
	PBPercentileInIndustry float64 `json:"pbPercentileInIndustry"`

	// 基本面相对行业的位置（分位）。
	GrowthPercentileInIndustry  float64 `json:"growthPercentileInIndustry"`
	QualityPercentileInIndustry float64 `json:"qualityPercentileInIndustry"`
	ROEPercentileInIndustry     float64 `json:"roePercentileInIndustry"`

	// Quadrant 是四象限判定结果。
	Quadrant string `json:"quadrant"`
	// QuadrantLabel 是中文结论。
	QuadrantLabel string `json:"quadrantLabel"`
	// QuadrantReason 是判定依据（带数值）。
	QuadrantReason string `json:"quadrantReason"`

	// BothCheap 表示"个股与行业都处于历史低位"。
	//
	// 方案 §5.3 明确要求：这种情形**不能直接判定高性价比**，必须继续看
	// 行业周期与未来成长 —— 所以它不是加分项，而是一个需要额外检查的标记。
	BothCheap     bool   `json:"bothCheap"`
	BothCheapNote string `json:"bothCheapNote,omitempty"`

	// Comparables 是最终选用的同行可比公司数。
	Comparables int `json:"comparables"`
	// ComparableNote 说明同行是怎么筛出来的（可比性下降时要如实说明）。
	ComparableNote string `json:"comparableNote"`

	// Score 是 0~100 的相对估值得分。
	Score float64 `json:"score"`
}

// peerThreshold 是"显著差异"的门槛：折溢价低于 10% 视为没有明显差异。
const peerPremiumThreshold = 0.10

// EvaluateRelativeValuation 计算个股相对行业与同行的估值。
//
// members 是**全行业成分**（含自己）—— 行业内的分位必须在完整横截面上算；
// peers 是已筛出的同行可比公司（不含自己，见 SelectComparables），
// 用于"基本面是否优于同行"的判断。
func EvaluateRelativeValuation(company CompanyValuation, industry IndustryValuation, members []CompanyValuation, peers []CompanyValuation, params Params) RelativeValuation {
	out := RelativeValuation{
		IndustryCode:  industry.Code,
		IndustryName:  industry.Name,
		Comparables:   len(peers),
		Quadrant:      QuadrantMixed,
		QuadrantLabel: "数据不足，暂不判定",
	}
	if !industry.Available {
		out.Note = industry.Note
		out.Score = 50
		return out
	}
	out.Available = true

	// ---- 折溢价 ----
	if industry.WeightedPE > 0 && company.PE > 0 {
		out.PremiumVsWeighted = company.PE/industry.WeightedPE - 1
	}
	if industry.MedianPE > 0 && company.PE > 0 {
		out.PremiumVsMedian = company.PE/industry.MedianPE - 1
	}

	// ---- 行业内分位 ----
	// 样本取**全行业**（含自己），因为问题是"我在同行里排第几"，
	// 缺了自己就不是一个完整的横截面。
	industryPEs := make([]float64, 0, len(members))
	industryPBs := make([]float64, 0, len(members))
	for _, member := range members {
		if member.PE > 0 {
			industryPEs = append(industryPEs, member.PE)
		}
		if member.PB > 0 {
			industryPBs = append(industryPBs, member.PB)
		}
	}
	if company.PE > 0 {
		if _, found := findCompany(members, company.Code); !found {
			industryPEs = append(industryPEs, company.PE)
		}
		out.PEPercentileInIndustry, _ = Percentile(industryPEs, company.PE, 5)
	}
	if company.PB > 0 {
		if _, found := findCompany(members, company.Code); !found {
			industryPBs = append(industryPBs, company.PB)
		}
		out.PBPercentileInIndustry, _ = Percentile(industryPBs, company.PB, 5)
	}

	// ---- 基本面在同行中的分位 ----
	// 这里用**同行可比公司**（peers）而不是全行业：判断"基本面是否优于可比对象"，
	// 可比对象才是正确的参照系。缺同行时回落到行业全体。
	reference := peers
	if len(reference) == 0 {
		reference = members
	}
	peerGrowth := make([]float64, 0, len(reference)+1)
	peerQuality := make([]float64, 0, len(reference)+1)
	peerROE := make([]float64, 0, len(reference)+1)
	for _, peer := range reference {
		if peer.Code == company.Code {
			continue
		}
		if peer.GrowthScore > 0 {
			peerGrowth = append(peerGrowth, peer.GrowthScore)
		}
		if peer.QualityScore > 0 {
			peerQuality = append(peerQuality, peer.QualityScore)
		}
		if peer.ROE != 0 {
			peerROE = append(peerROE, peer.ROE)
		}
	}
	if company.GrowthScore > 0 {
		peerGrowth = append(peerGrowth, company.GrowthScore)
		out.GrowthPercentileInIndustry, _ = Percentile(peerGrowth, company.GrowthScore, 5)
	}
	if company.QualityScore > 0 {
		peerQuality = append(peerQuality, company.QualityScore)
		out.QualityPercentileInIndustry, _ = Percentile(peerQuality, company.QualityScore, 5)
	}
	if company.ROE != 0 {
		peerROE = append(peerROE, company.ROE)
		out.ROEPercentileInIndustry, _ = Percentile(peerROE, company.ROE, 5)
	}

	// ---- 四象限判定 ----
	out.Quadrant, out.QuadrantLabel, out.QuadrantReason = classifyQuadrant(out, company, industry)

	// ---- "两者都低"检查 ----
	if company.SelfAvailable && industry.PEPercentile > 0 &&
		company.SelfPercentile < params.RiskThresholds.CheapPct &&
		industry.PEPercentile < params.RiskThresholds.CheapPct {
		out.BothCheap = true
		out.BothCheapNote = fmt.Sprintf(
			"个股自身估值处于历史 %.0f%% 分位、行业加权 PE 处于历史 %.0f%% 分位，两者都在低位。"+
				"这**不等于**高性价比：需要继续判断行业是否处于周期下行（低 PE 可能是盈利峰值造成的），"+
				"以及公司未来成长是否还在。本结论已交给风险约束模块单独处理，此处不额外加分",
			company.SelfPercentile, industry.PEPercentile)
	}

	out.Score = relativeScore(out, company, industry)
	return out
}

// classifyQuadrant 按方案 §5.3 的矩阵给出结论。
//
// 判定用"相对行业的折溢价"与"基本面在行业中的分位"两个维度。
// 基本面综合分取成长与质量分位的平均（两者缺一时用另一个；都缺则不判定）。
func classifyQuadrant(out RelativeValuation, company CompanyValuation, industry IndustryValuation) (string, string, string) {
	fundamental, hasFundamental := combineFundamental(out)
	premium, hasPremium := out.PremiumVsMedian, out.PremiumVsMedian != 0
	if !hasPremium && industry.WeightedPE > 0 && company.PE > 0 {
		premium, hasPremium = out.PremiumVsWeighted, true
	}
	if !hasFundamental || !hasPremium {
		return QuadrantMixed, "数据不足，暂不判定",
			"缺少「相对行业折溢价」或「基本面行业分位」中的一项，按方案要求不做象限判定（不猜）"
	}
	if math.Abs(premium) < peerPremiumThreshold {
		return QuadrantMixed, "估值与行业基本持平",
			fmt.Sprintf("相对行业中位数折溢价 %.1f%%，未超过 %.0f%% 的显著门槛；估值上没有明显差异，不构成折价机会也不构成溢价风险",
				premium*100, peerPremiumThreshold*100)
	}
	cheap := premium < 0
	good := fundamental >= 50

	switch {
	case cheap && good:
		return QuadrantCheapGood, "估值折价 + 基本面优于行业 → 可能是估值折价机会",
			fmt.Sprintf("相对行业中位数折价 %.1f%%，基本面行业分位 %.0f（高于中位）；"+
				"这是方案认可的唯一可以直接给高性价比的信号组合",
				-premium*100, fundamental)
	case cheap && !good:
		return QuadrantCheapBad, "估值折价 + 基本面弱于行业 → 需区分合理折价与价值陷阱",
			fmt.Sprintf("相对行业中位数折价 %.1f%%，但基本面行业分位只有 %.0f（低于中位）；"+
				"折价可能是市场对基本面下滑的正确定价 —— 必须看成长趋势与现金流质量再定",
				-premium*100, fundamental)
	case !cheap && good:
		return QuadrantExpensiveGood, "估值溢价 + 基本面优于行业 → 允许合理溢价，但须检验幅度",
			fmt.Sprintf("相对行业中位数溢价 %.1f%%，基本面行业分位 %.0f；"+
				"溢价有基本面支撑，但不能无限 —— 需用 PEG / PB-ROE 检验溢价是否过高",
				premium*100, fundamental)
	default:
		return QuadrantExpensiveBad, "估值溢价 + 基本面没有优势 → 估值风险高",
			fmt.Sprintf("相对行业中位数溢价 %.1f%%，而基本面行业分位只有 %.0f；"+
				"溢价缺少基本面支撑，估值下修风险偏高", premium*100, fundamental)
	}
}

// combineFundamental 把成长与质量分位合成一个基本面位置分。
func combineFundamental(out RelativeValuation) (float64, bool) {
	hasGrowth := out.GrowthPercentileInIndustry > 0
	hasQuality := out.QualityPercentileInIndustry > 0
	switch {
	case hasGrowth && hasQuality:
		return (out.GrowthPercentileInIndustry + out.QualityPercentileInIndustry) / 2, true
	case hasGrowth:
		return out.GrowthPercentileInIndustry, true
	case hasQuality:
		return out.QualityPercentileInIndustry, true
	}
	return 0, false
}

// relativeScore 把相对估值折成 0~100。
//
// 逻辑：折价加分、溢价减分，但**折价的加分上限低于溢价的扣分上限** ——
// 因为"便宜"可能是价值陷阱（方案反复强调这一点），而"贵"的风险更直接。
// 基本面优势可以把溢价"赎回"一部分，但不能无限赎回。
func relativeScore(out RelativeValuation, company CompanyValuation, industry IndustryValuation) float64 {
	premium := out.PremiumVsMedian
	if premium == 0 {
		premium = out.PremiumVsWeighted
	}
	score := 60.0
	// 折价：每 10% 折价 +8 分，最多 +30（折价 37.5% 封顶）
	if premium < 0 {
		score += math.Min(-premium*100*0.8, 30)
	} else {
		// 溢价：每 10% 溢价 −10 分，最多 −45
		score -= math.Min(premium*100*1.0, 45)
	}
	// 基本面优势赎回溢价：基本面分位每高于中位 10 个点，回补 3 分（最多 12 分）
	if fundamental, ok := combineFundamental(out); ok && premium > 0 {
		grant := math.Min((fundamental-50)/10*3, 12)
		if grant > 0 {
			score += grant
		}
	}
	// 价值陷阱修正：便宜但基本面弱，折价的加分要打折
	if premium < 0 {
		if fundamental, ok := combineFundamental(out); ok && fundamental < 35 {
			score = 60 + (score-60)*0.5
		}
	}
	return clampScore(score)
}

// ---------------------------------------------------------------------------
// 同行可比公司筛选
// ---------------------------------------------------------------------------

// SelectComparables 筛选同行可比公司（方案 §5.3：不能只因同属一个宽泛行业就比较）。
//
// 逐级放宽，每一步都记录"放宽到了哪一级"，以便在界面上说明可比性下降：
//
//	L1 同行业 + 市值在 1/R ~ R 倍 + 资产负债率差距 ≤ 阈值 + 成长分位同档
//	L2 同行业 + 市值在 1/R ~ R 倍 + 资产负债率差距 ≤ 阈值
//	L3 同行业 + 市值在 1/R ~ R 倍
//	L4 同行业全体
//	L5 同行业成分不足 5 只 → 返回空（调用方据此降置信度，而不是跨行业乱比）
func SelectComparables(target CompanyValuation, industry []CompanyValuation, params Params) ([]CompanyValuation, string) {
	const minPeers = 5
	others := make([]CompanyValuation, 0, len(industry))
	for _, member := range industry {
		if member.Code == target.Code {
			continue
		}
		others = append(others, member)
	}
	if len(others) == 0 {
		return nil, "同行业没有其它成分股，无法做同行比较"
	}

	ratio := params.PeerMaxMarketCapRatio
	capOK := func(member CompanyValuation) bool {
		if target.MarketCap <= 0 || member.MarketCap <= 0 {
			return false
		}
		upper := target.MarketCap * ratio
		lower := target.MarketCap / ratio
		return member.MarketCap >= lower && member.MarketCap <= upper
	}
	debtOK := func(member CompanyValuation) bool {
		return math.Abs(member.DebtRatio-target.DebtRatio) <= params.PeerMaxDebtGap
	}
	// "成长属性相近"用成长得分档位近似：把 0~100 分成 4 档（每 25 分一档）
	sameGrowthTier := func(member CompanyValuation) bool {
		return int(member.GrowthScore/25) == int(target.GrowthScore/25)
	}

	// 成长与负债数据缺失时，"同档"这类条件会把所有同行都筛掉 ——
	// 所以在 target 缺数据时跳过该条件，而不是用它筛出一个空集
	hasGrowth := target.GrowthScore > 0
	hasDebt := target.DebtRatio != 0

	type level struct {
		name string
		note string
		keep func(CompanyValuation) bool
	}
	levels := []level{
		{
			name: "L1",
			note: fmt.Sprintf("同行业 + 市值在 %.0f 倍以内 + 资产负债率差距 ≤ %.0f 个百分点 + 成长档位相同",
				ratio, params.PeerMaxDebtGap*100),
			keep: func(member CompanyValuation) bool {
				if !capOK(member) {
					return false
				}
				if hasDebt && !debtOK(member) {
					return false
				}
				if hasGrowth && !sameGrowthTier(member) {
					return false
				}
				return true
			},
		},
		{
			name: "L2",
			note: fmt.Sprintf("放宽成长档位：同行业 + 市值在 %.0f 倍以内 + 资产负债率差距 ≤ %.0f 个百分点",
				ratio, params.PeerMaxDebtGap*100),
			keep: func(member CompanyValuation) bool {
				if !capOK(member) {
					return false
				}
				if hasDebt && !debtOK(member) {
					return false
				}
				return true
			},
		},
		{
			name: "L3",
			note: fmt.Sprintf("放宽负债率：同行业 + 市值在 %.0f 倍以内", ratio),
			keep: capOK,
		},
		{
			name: "L4",
			note: "放宽市值：同行业全体（可比性下降，结论只作参考）",
			keep: func(CompanyValuation) bool { return true },
		},
	}

	var picked []CompanyValuation
	note := ""
	for _, item := range levels {
		candidate := make([]CompanyValuation, 0, len(others))
		for _, member := range others {
			if item.keep(member) {
				candidate = append(candidate, member)
			}
		}
		if len(candidate) >= minPeers {
			picked = candidate
			note = fmt.Sprintf("同行样本 %d 只（筛法 %s：%s）", len(candidate), item.name, item.note)
			break
		}
		if len(candidate) > len(picked) {
			// 记住"最宽的那一档里能拿到的最大集合"，最后兜底用
			picked = candidate
			note = fmt.Sprintf("同行样本仅 %d 只（筛法 %s：%s）—— 不足 %d 只，可比性不足，结论置信度已下调",
				len(candidate), item.name, item.note, minPeers)
		}
	}
	if len(picked) == 0 {
		return nil, fmt.Sprintf("同行业 %d 只成分股里没有找到可用于比较的同行（市值/负债率数据缺失）", len(others))
	}
	if len(picked) < minPeers {
		return picked, note
	}
	return picked, note
}

// findCompany 在成分列表里按代码查找。
func findCompany(members []CompanyValuation, code string) (CompanyValuation, bool) {
	for _, member := range members {
		if member.Code == code {
			return member, true
		}
	}
	return CompanyValuation{}, false
}
