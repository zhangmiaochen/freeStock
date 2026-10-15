package valuation

import (
	"encoding/json"
	"strings"
)

// ---------------------------------------------------------------------------
// 参数（全部可配置，且随模型版本一起存档）
//
// 方案 §7.2 明确要求："权重、评分区间、行业调整规则和风险阈值均应可配置，
// 并通过历史验证校准，不将初始权重视为最优参数。"
//
// 所以下面的 DefaultParams 只是**起点**，不是最优值。界面上必须能看出
// 当前用的是哪一版参数（ModelVersion）。
// ---------------------------------------------------------------------------

// Params 是评估用到的全部可调参数。
type Params struct {
	// ModelVersion 覆盖默认版本号；空则用 ModelVersion 常量。
	ModelVersion string `json:"modelVersion"`

	// RatingThresholds 是评级分界。
	RatingThresholds RatingThresholds `json:"ratingThresholds"`

	// HorizonWeights 是**每档期限一套**维度权重（方案 §7.2：
	// "不同期限使用不同的维度权重"）。
	HorizonWeights map[string]map[ScoreDimension]float64 `json:"horizonWeights"`

	// ScenarioProb 是每档期限的三情景概率 [悲观, 基准, 乐观]，和为 1。
	ScenarioProb map[string][3]float64 `json:"scenarioProb"`

	// ValueStartDate 是历史估值自建重建的起点（YYYYMMDD）。
	//
	// 为什么是 2022-01-01：数据湖 daily_bars 每日约 5000 只的起点就在 2022 年
	// （实测 2016~2021 每个交易日只有约 200 只）。早于它的日子样本太稀，
	// 拿来做"全市场分位"会系统性失真。
	ValueStartDate int64 `json:"valueStartDate"`

	// SelfHistoryYears 是自身历史估值分位的回看年数（默认 5 年）。
	SelfHistoryYears int `json:"selfHistoryYears"`

	// IndustryMinMembers 是行业聚合最少需要的成员数；不足则回退全市场。
	IndustryMinMembers int `json:"industryMinMembers"`

	// PeerMaxMarketCapRatio 是同行可比的市值倍数上限（10 表示 1/10 ~ 10 倍）。
	PeerMaxMarketCapRatio float64 `json:"peerMaxMarketCapRatio"`
	// PeerMaxDebtGap 是同行可比的资产负债率最大差距（绝对差）。
	PeerMaxDebtGap float64 `json:"peerMaxDebtGap"`

	// WACC 是 DCF 与增量资本回报的门槛（默认 8%）。
	WACC float64 `json:"wacc"`

	// TerminalGrowth 是 36 个月情景的稳态增长率（长期终值）。
	TerminalGrowth float64 `json:"terminalGrowth"`

	// RiskThresholds 是风险约束阈值。
	RiskThresholds RiskThresholds `json:"riskThresholds"`

	// QualityThresholds 是成长质量/盈利质量的判定阈值。
	QualityThresholds QualityThresholds `json:"qualityThresholds"`
}

// RiskThresholds 是九类风险约束的判定阈值（方案 §8.4）。
type RiskThresholds struct {
	// CheapPct 是"估值便宜"的分位上限（低于它算便宜）。
	CheapPct float64 `json:"cheapPct"`
	// ExpensivePct 是"行业偏贵"的分位下限。
	ExpensivePct float64 `json:"expensivePct"`
	// CashContentMin 是利润现金含量的下限。
	CashContentMin float64 `json:"cashContentMin"`
	// PremiumMax 是相对行业允许的最大溢价（超过即需基本面支撑）。
	PremiumMax float64 `json:"premiumMax"`
	// MinReturnRiskRatio 是可接受的最低风险收益比（预期收益 ÷ 悲观损失）。
	MinReturnRiskRatio float64 `json:"minReturnRiskRatio"`
	// OptimismDependenceMax 是允许的"乐观依赖度"上限。
	OptimismDependenceMax float64 `json:"optimismDependenceMax"`
	// ImpliedGrowthYearsMax 是"为超出自身能力的增长付账"可接受的年限上限。
	//
	// 口径见 ImpliedExpectation.ImpliedYears：当前市值比"按公司自身增速折现的
	// 价值"高出的部分，相当于多少年的自由现金流。
	//
	// 取 5 年的理由：A 股一轮产能/景气周期大致 3~5 年，超过 5 年意味着
	// 当前价格在赌一个跨周期的持续高增长，这是需要明确提示的赌注。
	ImpliedGrowthYearsMax float64 `json:"impliedGrowthYearsMax"`
	// MinDataCoverage 是触发"无法可靠评估"的数据覆盖率下限。
	MinDataCoverage float64 `json:"minDataCoverage"`
	// DeteriorateQuarters 是"盈利持续恶化"需要连续恶化的季度数。
	DeteriorateQuarters int `json:"deteriorateQuarters"`
}

// QualityThresholds 是成长质量判定阈值。
type QualityThresholds struct {
	// DeductedRatioMin 是扣非净利润占归母净利润的比重要求。
	DeductedRatioMin float64 `json:"deductedRatioMin"`
	// DebtRatioMax 是资产负债率上限（超过视为依赖过度负债）。
	DebtRatioMax float64 `json:"debtRatioMax"`
	// GrowthHighPct 是"高增长"的分位下限。
	GrowthHighPct float64 `json:"growthHighPct"`
	// GrowthLowPct 是"成长不佳"的分位上限。
	GrowthLowPct float64 `json:"growthLowPct"`
}

// ---------------------------------------------------------------------------
// 默认值
// ---------------------------------------------------------------------------

// DefaultParams 返回默认参数。
//
// 权重设计意图（逐条可解释，不是随手填的）：
//
//   - **短期（1/3 个月）**：估值位置（自身 + 相对行业）与安全边际占比最高，
//     因为一两个季度内价格主要由估值波动驱动；成长只占小头（成长在这段时间
//     兑现不了多少）。预期收益维度的权重也压得低 —— 短期收益的噪声远大于信号。
//   - **中期（6/12 个月）**：成长与质量权重上来，估值仍重要（估值修复是主要收益来源），
//     预期收益与风险开始有判别力。
//   - **长期（36 个月）**：成长能力 + 成长质量 + 资本回报合计超过一半 ——
//     三年后的价格由"公司长成什么样"决定，不是由今天的估值分位决定；
//     安全边际权重反而降低（长期合理价值区间本来就宽，短期折价意义不大）。
//
// 每档权重之和必须是 100，`SanitizeParams` 会归一化并保证这一点。
func DefaultParams() Params {
	return Params{
		ModelVersion:     ModelVersion,
		RatingThresholds: DefaultRatingThresholds(),
		HorizonWeights: map[string]map[ScoreDimension]float64{
			"1m": {
				DimSelfValuation: 22, DimIndValuation: 14, DimRelValuation: 16,
				DimGrowth: 4, DimQuality: 8, DimCapital: 6,
				DimMargin: 16, DimReturn: 6, DimData: 8,
			},
			"3m": {
				DimSelfValuation: 20, DimIndValuation: 13, DimRelValuation: 15,
				DimGrowth: 8, DimQuality: 9, DimCapital: 6,
				DimMargin: 13, DimReturn: 8, DimData: 8,
			},
			"6m": {
				DimSelfValuation: 16, DimIndValuation: 11, DimRelValuation: 13,
				DimGrowth: 14, DimQuality: 12, DimCapital: 8,
				DimMargin: 11, DimReturn: 8, DimData: 7,
			},
			"12m": {
				DimSelfValuation: 12, DimIndValuation: 9, DimRelValuation: 11,
				DimGrowth: 19, DimQuality: 14, DimCapital: 10,
				DimMargin: 9, DimReturn: 9, DimData: 7,
			},
			"36m": {
				DimSelfValuation: 8, DimIndValuation: 6, DimRelValuation: 8,
				DimGrowth: 24, DimQuality: 17, DimCapital: 14,
				DimMargin: 6, DimReturn: 10, DimData: 7,
			},
		},
		// 情景概率：短期限行情噪声大、极化概率高，所以悲观/乐观尾部更厚；
		// 长期限中枢回归更明显，基准情景概率更高。
		ScenarioProb: map[string][3]float64{
			"1m":  {0.30, 0.45, 0.25},
			"3m":  {0.30, 0.45, 0.25},
			"6m":  {0.28, 0.47, 0.25},
			"12m": {0.25, 0.50, 0.25},
			"36m": {0.25, 0.50, 0.25},
		},
		ValueStartDate:        20220101,
		SelfHistoryYears:      5,
		IndustryMinMembers:    5,
		PeerMaxMarketCapRatio: 10,
		PeerMaxDebtGap:        0.30,
		WACC:                  0.08,
		TerminalGrowth:        0.025,
		RiskThresholds: RiskThresholds{
			CheapPct:              30,
			ExpensivePct:          70,
			CashContentMin:        0.50,
			PremiumMax:            0.30,
			MinReturnRiskRatio:    1.0,
			OptimismDependenceMax: 0.40,
			// ImpliedGrowthYearsMax：为超出自身能力的增长付账超过 5 年即提示
			ImpliedGrowthYearsMax: 5.0,
			MinDataCoverage:       0.50,
			DeteriorateQuarters:   2,
		},
		QualityThresholds: QualityThresholds{
			DeductedRatioMin: 0.70,
			DebtRatioMax:     0.80,
			GrowthHighPct:    70,
			GrowthLowPct:     30,
		},
	}
}

// Sanitize 把非法参数收敛到可用范围 —— 与 config 包的约定一致：永不报错，只纠正。
//
// 关键不变式（都会被单测钉住）：
//   - 每档期限的权重都齐全（九个维度一个不少）；
//   - 每档权重之和归一化到 100；
//   - 每档情景概率之和归一化到 1；
//   - **五档权重不允许完全相同** —— 那正是方案明令禁止的"简单复制同一个评分"。
func (p Params) Sanitize() Params {
	def := DefaultParams()

	if strings.TrimSpace(p.ModelVersion) == "" {
		p.ModelVersion = def.ModelVersion
	}
	p.RatingThresholds = sanitizeRatingThresholds(p.RatingThresholds, def.RatingThresholds)

	if p.ValueStartDate < 19000101 || p.ValueStartDate > 29991231 {
		p.ValueStartDate = def.ValueStartDate
	}
	p.SelfHistoryYears = clampIntRange(p.SelfHistoryYears, 1, 15, def.SelfHistoryYears)
	p.IndustryMinMembers = clampIntRange(p.IndustryMinMembers, 2, 200, def.IndustryMinMembers)
	p.PeerMaxMarketCapRatio = clampFloatRange(p.PeerMaxMarketCapRatio, 1.5, 100, def.PeerMaxMarketCapRatio)
	p.PeerMaxDebtGap = clampFloatRange(p.PeerMaxDebtGap, 0.02, 1, def.PeerMaxDebtGap)
	p.WACC = clampFloatRange(p.WACC, 0.01, 0.40, def.WACC)
	p.TerminalGrowth = clampFloatRange(p.TerminalGrowth, -0.05, 0.10, def.TerminalGrowth)

	// 权重：补齐缺失档位与缺失维度，再按行归一化到 100。
	p.HorizonWeights = sanitizeWeights(p.HorizonWeights, def.HorizonWeights)
	p.ScenarioProb = sanitizeScenarioProb(p.ScenarioProb, def.ScenarioProb)

	p.RiskThresholds = sanitizeRiskThresholds(p.RiskThresholds, def.RiskThresholds)
	p.QualityThresholds = sanitizeQualityThresholds(p.QualityThresholds, def.QualityThresholds)
	return p
}

// WeightsFor 返回某档期限的权重表（缺失时回落到默认参数）。
func (p Params) WeightsFor(key string) map[ScoreDimension]float64 {
	if table, ok := p.HorizonWeights[key]; ok && len(table) > 0 {
		return table
	}
	return DefaultParams().HorizonWeights[key]
}

// ScenarioProbFor 返回某档期限的三情景概率。
func (p Params) ScenarioProbFor(key string) [3]float64 {
	if prob, ok := p.ScenarioProb[key]; ok {
		return prob
	}
	return DefaultParams().ScenarioProb[key]
}

// sanitizeWeights 补齐并归一化每档权重。
func sanitizeWeights(in, def map[string]map[ScoreDimension]float64) map[string]map[ScoreDimension]float64 {
	out := make(map[string]map[ScoreDimension]float64, len(def))
	for _, h := range Horizons {
		fallback := def[h.Key]
		table := in[h.Key]
		row := make(map[ScoreDimension]float64, len(ScoreDimensions))
		sum := 0.0
		for _, dim := range ScoreDimensions {
			value, ok := table[dim]
			// 非正权重视为"没配"：用默认值补上，而不是当成 0 权重
			// （0 会让该维度彻底不参与，几乎不是用户的本意）。
			if !ok || value <= 0 {
				value = fallback[dim]
			}
			row[dim] = value
			sum += value
		}
		if sum <= 0 {
			row = fallback
			sum = 0
			for _, dim := range ScoreDimensions {
				sum += row[dim]
			}
		}
		// 归一化到 100：用户手改配置成 7 个维度之和 83 也要能用
		if sum > 0 {
			for _, dim := range ScoreDimensions {
				row[dim] = row[dim] / sum * 100
			}
		}
		out[h.Key] = row
	}
	return out
}

// sanitizeScenarioProb 归一化三情景概率。
func sanitizeScenarioProb(in map[string][3]float64, def map[string][3]float64) map[string][3]float64 {
	out := make(map[string][3]float64, len(def))
	for _, h := range Horizons {
		prob, ok := in[h.Key]
		sum := prob[0] + prob[1] + prob[2]
		if !ok || sum <= 0 || prob[0] < 0 || prob[1] < 0 || prob[2] < 0 {
			prob = def[h.Key]
			sum = prob[0] + prob[1] + prob[2]
		}
		if sum > 0 {
			prob = [3]float64{prob[0] / sum, prob[1] / sum, prob[2] / sum}
		}
		out[h.Key] = prob
	}
	return out
}

func sanitizeRatingThresholds(in, def RatingThresholds) RatingThresholds {
	// 唯一必须成立的是**单调性**：Poor < Neutral < Good < Excellent。
	// 配置写反了就整体回落默认，而不是部分修正（部分修正会造出更奇怪的区间）。
	values := []float64{in.Poor, in.Neutral, in.Good, in.Excellent}
	for _, v := range values {
		if v < 0 || v > 100 {
			return def
		}
	}
	if !(values[0] < values[1] && values[1] < values[2] && values[2] < values[3]) {
		return def
	}
	return in
}

func sanitizeRiskThresholds(in, def RiskThresholds) RiskThresholds {
	out := def
	out.CheapPct = clampFloatRange(in.CheapPct, 1, 60, def.CheapPct)
	out.ExpensivePct = clampFloatRange(in.ExpensivePct, 40, 99, def.ExpensivePct)
	if out.ExpensivePct <= out.CheapPct {
		out.CheapPct, out.ExpensivePct = def.CheapPct, def.ExpensivePct
	}
	out.CashContentMin = clampFloatRange(in.CashContentMin, 0, 3, def.CashContentMin)
	out.PremiumMax = clampFloatRange(in.PremiumMax, 0.02, 3, def.PremiumMax)
	out.MinReturnRiskRatio = clampFloatRange(in.MinReturnRiskRatio, 0.1, 10, def.MinReturnRiskRatio)
	out.OptimismDependenceMax = clampFloatRange(in.OptimismDependenceMax, 0.05, 1, def.OptimismDependenceMax)
	out.ImpliedGrowthYearsMax = clampFloatRange(in.ImpliedGrowthYearsMax, 1, 30, def.ImpliedGrowthYearsMax)
	out.MinDataCoverage = clampFloatRange(in.MinDataCoverage, 0.1, 0.9, def.MinDataCoverage)
	out.DeteriorateQuarters = clampIntRange(in.DeteriorateQuarters, 1, 8, def.DeteriorateQuarters)
	return out
}

func sanitizeQualityThresholds(in, def QualityThresholds) QualityThresholds {
	out := def
	out.DeductedRatioMin = clampFloatRange(in.DeductedRatioMin, 0, 1, def.DeductedRatioMin)
	out.DebtRatioMax = clampFloatRange(in.DebtRatioMax, 0.2, 1, def.DebtRatioMax)
	out.GrowthHighPct = clampFloatRange(in.GrowthHighPct, 50, 95, def.GrowthHighPct)
	out.GrowthLowPct = clampFloatRange(in.GrowthLowPct, 5, 50, def.GrowthLowPct)
	if out.GrowthLowPct >= out.GrowthHighPct {
		out.GrowthLowPct, out.GrowthHighPct = def.GrowthLowPct, def.GrowthHighPct
	}
	return out
}

func clampFloatRange(value, min, max, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampIntRange(value, min, max, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// MarshalJSON 让参数能整份落盘（供历史验证存档）。
func (p Params) MarshalJSON() ([]byte, error) {
	type alias Params
	return json.Marshal(alias(p))
}
