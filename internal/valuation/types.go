// Package valuation 实现「A 股多期限性价比评估」。
//
// 设计基准见 docs/A股多期限性价比评估方案.md，实施记录见
// docs/A股多期限性价比评估开发计划.md。本文件只放**贯穿全流程的类型与常量**。
//
// 三条不可违背的口径（方案 §2.3、§16）：
//
//  1. **收益/波动/回撤一律用后复权价**（`daily_bars × F(t)`）。数据湖的
//     daily_bars 是不复权行情，实测 002594 在 2025-07-29 因 1:3 送转从 337.00
//     直接变成 111.42；不复权算出来的区间涨幅会在除权日凭空"暴跌"。
//  2. **市值/PE/PB/PS 一律用不复权原始价 × 当期股本**。复权只是记账口径，
//     市值必须用真实成交价，否则 PE 全是错的。
//  3. **成交量/成交额保持原始**，不随复权放大。
//
// 这三条分别由 PriceSeries.Return (AdjHFQ) 与 MarketCap (AdjNone) 两条取值路径
// 保证 —— 绝不允许同一个价格数组兼作两种用途。
package valuation

import "fmt"

// ModelVersion 是模型版本号。
//
// 权重、阈值、参数表任何一项改动都要升版本：历史验证结果与参数集一起存档，
// 「事后反复调参」就是靠这个字段被发现的（方案 §11.3）。
const ModelVersion = "v1-2026.10"

// ---------------------------------------------------------------------------
// 期限
// ---------------------------------------------------------------------------

// Horizon 是一个投资期限。
type Horizon struct {
	// Key 是稳定标识，同时用于配置与排名分组。
	Key string `json:"key"`
	// Label 是界面标签，如 "1 个月"。
	Label string `json:"label"`
	// TradingDays 是该期限对应的交易日数。
	TradingDays int `json:"tradingDays"`
	// Months 是自然月数，用于预测与情景的表述。
	Months int `json:"months"`
}

// Horizons 是五档评估期限，顺序固定（短 → 长）。
//
// 交易日数按 A 股年均 242 个交易日折算：1 月=21、3 月=63、6 月=126、
// 12 月=252、36 月=756。这些数字同时是**区间长度**（算历史收益/波动）
// 与**预测跨度**（算情景收益），两处必须一致，所以只在这里定义一次。
var Horizons = []Horizon{
	{Key: "1m", Label: "1 个月", TradingDays: 21, Months: 1},
	{Key: "3m", Label: "3 个月", TradingDays: 63, Months: 3},
	{Key: "6m", Label: "6 个月", TradingDays: 126, Months: 6},
	{Key: "12m", Label: "12 个月", TradingDays: 252, Months: 12},
	{Key: "36m", Label: "36 个月", TradingDays: 756, Months: 36},
}

// HorizonKeys 返回五档期限的 Key，顺序与 Horizons 一致。
func HorizonKeys() []string {
	out := make([]string, 0, len(Horizons))
	for _, h := range Horizons {
		out = append(out, h.Key)
	}
	return out
}

// HorizonByKey 按 Key 找期限；找不到返回 false。
func HorizonByKey(key string) (Horizon, bool) {
	for _, h := range Horizons {
		if h.Key == key {
			return h, true
		}
	}
	return Horizon{}, false
}

// ---------------------------------------------------------------------------
// 估值方法
// ---------------------------------------------------------------------------

// Method 是估值方法标识。
type Method string

const (
	MethodPE          Method = "pe"
	MethodPB          Method = "pb"
	MethodPS          Method = "ps"
	MethodEVEBITDA    Method = "ev_ebitda"
	MethodDCF         Method = "dcf"
	MethodPEG         Method = "peg"
	MethodPBROE       Method = "pb_roe"
	MethodCyclicalAdj Method = "cyclical_adj"
	MethodDividend    Method = "dividend"
)

// MethodLabel 返回方法的中文名。
func MethodLabel(m Method) string {
	switch m {
	case MethodPE:
		return "PE（市盈率）"
	case MethodPB:
		return "PB（市净率）"
	case MethodPS:
		return "PS（市销率）"
	case MethodEVEBITDA:
		return "EV/EBITDA"
	case MethodDCF:
		return "DCF（现金流折现）"
	case MethodPEG:
		return "PEG"
	case MethodPBROE:
		return "PB-ROE"
	case MethodCyclicalAdj:
		return "周期调整估值"
	case MethodDividend:
		return "股息率"
	}
	return string(m)
}

// ---------------------------------------------------------------------------
// 评级与风险
// ---------------------------------------------------------------------------

// Rating 是最终评级档位。顺序即优劣，比较用 RatingRank。
type Rating string

const (
	RatingUnavailable Rating = "unavailable" // 暂无法可靠评估
	RatingAvoid       Rating = "avoid"       // 回避
	RatingPoor        Rating = "poor"        // 偏低
	RatingNeutral     Rating = "neutral"     // 中性
	RatingGood        Rating = "good"        // 良好
	RatingExcellent   Rating = "excellent"   // 优秀
)

// RatingOrder 是评级从差到好的固定顺序（不含 unavailable）。
var RatingOrder = []Rating{RatingAvoid, RatingPoor, RatingNeutral, RatingGood, RatingExcellent}

// RatingRank 返回评级序号；unavailable 返回 -1，未知返回 -2。
func RatingRank(r Rating) int {
	for i, item := range RatingOrder {
		if item == r {
			return i
		}
	}
	if r == RatingUnavailable {
		return -1
	}
	return -2
}

// RatingLabel 返回评级中文名。
func RatingLabel(r Rating) string {
	switch r {
	case RatingExcellent:
		return "优秀"
	case RatingGood:
		return "良好"
	case RatingNeutral:
		return "中性"
	case RatingPoor:
		return "偏低"
	case RatingAvoid:
		return "回避"
	case RatingUnavailable:
		return "暂无法可靠评估"
	}
	return string(r)
}

// RatingForScore 把 0~100 综合分映射成评级（阈值可配置）。
func RatingForScore(score float64, thresholds RatingThresholds) Rating {
	switch {
	case score >= thresholds.Excellent:
		return RatingExcellent
	case score >= thresholds.Good:
		return RatingGood
	case score >= thresholds.Neutral:
		return RatingNeutral
	case score >= thresholds.Poor:
		return RatingPoor
	default:
		return RatingAvoid
	}
}

// RatingThresholds 是评级分界（含下界）。
type RatingThresholds struct {
	Excellent float64 `json:"excellent"`
	Good      float64 `json:"good"`
	Neutral   float64 `json:"neutral"`
	Poor      float64 `json:"poor"`
}

// DefaultRatingThresholds 是默认分界。
//
// 为什么是这组数（按实测分布定的，不是拍脑袋）：用 150 只大市值标的跑出来
// 五档均分约 47~51、P90 约 61~63、极值约 73。如果照搬"80 分才算优秀"这类
// 直觉阈值，**所有标的都会落在"中性/偏低"两档**，评级就失去了区分力
// （实测第一版就是这个结果：榜上全是"中性"）。
// 所以分界按分布的分位来定：优秀≈前 3%、良好≈前 12%、中性≈中位偏上。
//
// ⚠ 这组阈值仍然只是**起点**：方案 §7.2 要求"权重、评分区间、行业调整规则和
// 风险阈值均应可配置，并通过历史验证校准，不将初始权重视为最优参数"。
// 真正的校准要等历史验证（见开发计划 §四）。
func DefaultRatingThresholds() RatingThresholds {
	return RatingThresholds{Excellent: 72, Good: 62, Neutral: 50, Poor: 38}
}

// RiskSeverity 是风险约束的强度。三级，对应方案 §7.3 的处置分层。
type RiskSeverity string

const (
	// RiskNotice 是「一般预警」：只标注，不改评分。
	RiskNotice RiskSeverity = "notice"
	// RiskCap 是「限制评级」：评级封顶。
	RiskCap RiskSeverity = "cap"
	// RiskUnavailable 是「无法可靠评估」：不出评级。
	RiskUnavailable RiskSeverity = "unavailable"
)

// RiskCode 是风险类型的稳定标识。
type RiskCode string

const (
	RiskCheapButDeteriorating RiskCode = "cheap_but_deteriorating" // 估值低但盈利持续恶化
	RiskIndustryExpensive     RiskCode = "industry_expensive"      // 个股便宜但行业贵
	RiskGrowthNoCash          RiskCode = "growth_no_cash"          // 高增长但现金流差
	RiskPremiumUnsupported    RiskCode = "premium_unsupported"     // 估值溢价但无基本面支撑
	RiskDownsideTooLarge      RiskCode = "downside_too_large"      // 预期收益高但悲观损失过大
	RiskOptimismDependent     RiskCode = "optimism_dependent"      // 估值依赖乐观假设
	RiskCyclicalMisjudge      RiskCode = "cyclical_misjudge"       // 周期性误判
	RiskDataQuality           RiskCode = "data_quality"            // 数据缺失/过期/口径不一致
	RiskMethodConflict        RiskCode = "method_conflict"         // 多方法结果冲突
)

// RiskFlag 是一条风险约束结论。
type RiskFlag struct {
	Code     RiskCode     `json:"code"`
	Severity RiskSeverity `json:"severity"`
	// Title 是一句话结论（界面直接显示）。
	Title string `json:"title"`
	// Reason 是可核对的依据：必须带上具体数值，不能只说"数据不足"。
	Reason string `json:"reason"`
	// CapRating 是 severity=cap 时的评级上限；其它情况为空。
	CapRating Rating `json:"capRating,omitempty"`
}

// Confidence 是数据可信度。
type Confidence struct {
	// Score 是 0~100 的置信度得分。
	Score float64 `json:"score"`
	// Level 是等级：high / medium / low。
	Level string `json:"level"`
	// Reasons 是扣分原因（每条都带具体数值）。
	Reasons []string `json:"reasons"`
}

// ConfidenceLevelFor 把置信度得分映射成等级。
func ConfidenceLevelFor(score float64) string {
	switch {
	case score >= 80:
		return "high"
	case score >= 60:
		return "medium"
	default:
		return "low"
	}
}

// ---------------------------------------------------------------------------
// 依据追溯
// ---------------------------------------------------------------------------

// Evidence 是一条可追溯的评分依据（方案 §10.6）。
//
// ⚠ 每个分数与结论都必须能追到 (指标, 值, 数据时点, 来源)。界面上「为什么是这个分」
// 就是把这个切片列出来 —— 所以不允许出现"值为空但结论很确定"的条目。
type Evidence struct {
	// Dimension 是所属评分维度（见 ScoreDimension）。
	Dimension string `json:"dimension"`
	// Metric 是指标名（人话，如"近 3 年营收 CAGR"）。
	Metric string `json:"metric"`
	// Value 是格式化好的值文本（如 "12.4%"），界面直接显示。
	Value string `json:"value"`
	// Raw 是原始数值（可选，供排序/二次计算）。
	Raw float64 `json:"raw,omitempty"`
	// AsOf 是数据时点（YYYYMMDD；财务为报告期，如 "2026Q2"）。
	AsOf string `json:"asOf"`
	// Source 是数据来源，如 "cnequity:financial_statement_items"。
	Source string `json:"source"`
	// Note 是补充说明（口径、为什么这么算）。
	Note string `json:"note,omitempty"`
}

// ScoreDimension 是评分维度的稳定标识。
type ScoreDimension string

const (
	DimSelfValuation ScoreDimension = "selfValuation" // 个股自身估值合理性
	DimIndValuation  ScoreDimension = "indValuation"  // 行业整体估值环境
	DimRelValuation  ScoreDimension = "relValuation"  // 个股相对同行的估值性价比
	DimGrowth        ScoreDimension = "growth"        // 成长能力
	DimQuality       ScoreDimension = "quality"       // 成长质量与盈利质量
	DimCapital       ScoreDimension = "capital"       // 资本回报与资本使用效率
	DimMargin        ScoreDimension = "margin"        // 安全边际
	DimReturn        ScoreDimension = "return"        // 多期限预期收益与下行风险
	DimData          ScoreDimension = "data"          // 数据质量与模型置信度
)

// ScoreDimensions 是九个维度的固定顺序（方案 §8.1）。
var ScoreDimensions = []ScoreDimension{
	DimSelfValuation, DimIndValuation, DimRelValuation,
	DimGrowth, DimQuality, DimCapital,
	DimMargin, DimReturn, DimData,
}

// DimensionLabel 返回维度中文名。
func DimensionLabel(d ScoreDimension) string {
	switch d {
	case DimSelfValuation:
		return "个股自身估值"
	case DimIndValuation:
		return "行业估值环境"
	case DimRelValuation:
		return "相对同行估值"
	case DimGrowth:
		return "成长能力"
	case DimQuality:
		return "成长与盈利质量"
	case DimCapital:
		return "资本回报效率"
	case DimMargin:
		return "安全边际"
	case DimReturn:
		return "预期收益与风险"
	case DimData:
		return "数据质量与置信度"
	}
	return string(d)
}

// ---------------------------------------------------------------------------
// 溯源（评估日 / 数据截止 / 模型版本）
// ---------------------------------------------------------------------------

// Snapshot 是每次评估都必须携带的溯源信息（方案 §4.1）。
type Snapshot struct {
	EvalDate int64 `json:"evalDate"` // 评估基准交易日 YYYYMMDD
	// DataCutoff 是数据截止时点描述，如 "行情 2026-10-14 收盘；财务公告日 ≤ 2026-10-14"。
	DataCutoff string `json:"dataCutoff"`
	// ModelVersion 是模型版本号。
	ModelVersion string `json:"modelVersion"`
	// Universe 是评估覆盖的标的数。
	Universe int `json:"universe"`
	// Evaluated 是真正给出评级的标的数。
	Evaluated int `json:"evaluated"`
	// Unavailable 是被标为"暂无法可靠评估"的标的数。
	Unavailable int `json:"unavailable"`
}

// String 便于日志与测试断言。
func (s Snapshot) String() string {
	return fmt.Sprintf("Snapshot(evalDate=%d universe=%d evaluated=%d unavailable=%d model=%s)",
		s.EvalDate, s.Universe, s.Evaluated, s.Unavailable, s.ModelVersion)
}
