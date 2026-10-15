package valuation

import (
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// 第一层估值：个股自身历史估值
//
// 方案 §5.1 的关键限定：**历史分位只说明当前估值在自身历史中的相对位置，
// 不能直接作为合理价值**。所以这里输出的是"位置"与"归因"，合理价值留给
// fairvalue.go 用成长调整后的方法算。
//
// 另有一条硬性降权规则（方案 §5.1 末段）：公司商业模式 / 盈利能力 / 成长阶段
// 发生结构性变化时，历史估值参照权重要下调。这里的判据是**估值中枢移动**与
// **盈利能力中枢移动**两项，见 detectStructuralShift。
// ---------------------------------------------------------------------------

// SelfValuation 是个股自身历史估值评估结果。
type SelfValuation struct {
	// Available 表示历史样本足够（不足则分位无意义）。
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`

	// Current 是当前估值。
	Current ValuationPoint `json:"current"`
	// 当前 PE / PB / PS（0 表示不可用，例如亏损公司没有 PE）。
	CurrentPE float64 `json:"currentPE"`
	CurrentPB float64 `json:"currentPB"`
	CurrentPS float64 `json:"currentPS"`

	// 3 年 / 5 年窗口的分位（0~100）。
	PEPercentile3Y float64 `json:"pePercentile3Y"`
	PEPercentile5Y float64 `json:"pePercentile5Y"`
	PBPercentile3Y float64 `json:"pbPercentile3Y"`
	PBPercentile5Y float64 `json:"pbPercentile5Y"`
	PSPercentile5Y float64 `json:"psPercentile5Y"`

	// 历史区间（分位点）。
	PEBand ValuationBand `json:"peBand"`
	PBBand ValuationBand `json:"pbBand"`
	PSBand ValuationBand `json:"psBand"`

	// SampleDays 是参与分位计算的交易日数（3 年 / 5 年）。
	SampleDays3Y int `json:"sampleDays3Y"`
	SampleDays5Y int `json:"sampleDays5Y"`

	// StructuralShift 为真表示历史估值参照已不可靠。
	StructuralShift bool `json:"structuralShift"`
	// StructuralReasons 是结构性变化的具体依据（带数值）。
	StructuralReasons []string `json:"structuralReasons"`

	// 归因：近一年 PE 变化中有多少来自价格、多少来自盈利。
	PriceEffect   float64 `json:"priceEffect"`
	EarningEffect float64 `json:"earningEffect"`

	// Score 是 0~100 的"自身估值合理性"得分（越高越便宜）。
	Score float64 `json:"score"`
}

// ValuationBand 是某个估值指标的历史分位区间。
type ValuationBand struct {
	P10 float64 `json:"p10"`
	P25 float64 `json:"p25"`
	P50 float64 `json:"p50"`
	P75 float64 `json:"p75"`
	P90 float64 `json:"p90"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// BuildBand 从样本算出分位区间。
func BuildBand(samples []float64) ValuationBand {
	valid := positiveOnly(samples)
	if len(valid) == 0 {
		return ValuationBand{}
	}
	return ValuationBand{
		P10: Quantile(valid, 0.10),
		P25: Quantile(valid, 0.25),
		P50: Quantile(valid, 0.50),
		P75: Quantile(valid, 0.75),
		P90: Quantile(valid, 0.90),
		Min: Quantile(valid, 0),
		Max: Quantile(valid, 1),
	}
}

// PercentileLabel 把分位翻译成人话（界面直接用）。
func PercentileLabel(p float64) string {
	switch {
	case p <= 10:
		return "历史极低位"
	case p <= 30:
		return "历史低位"
	case p <= 45:
		return "历史中位偏下"
	case p <= 55:
		return "历史中位"
	case p <= 70:
		return "历史中位偏上"
	case p <= 90:
		return "历史高位"
	default:
		return "历史极高位"
	}
}

// minPercentileSamples 是分位计算所需的最少样本数。
//
// 取 120（约半年交易日）：再少的话"分位"只是把几个点排了个序，
// 一个季度的涨跌就能让分位从 10 跳到 90。
const minPercentileSamples = 120

// peScoreSamples 是算 PE 分位时用的参考指标：PB 作为 PE 不可用时的替代。
//
// 为什么 PB 能当替代：亏损公司没有 PE（净利润 ≤ 0），但 PB 仍然存在，
// 且对重资产行业 PB 本身就是主要参照。替代时会在结论里注明。

// EvaluateSelfValuation 计算自身历史估值。
//
// series 是自建的历史估值序列（升序）；evalDate 是评估日；params 提供窗口与阈值。
func EvaluateSelfValuation(series []ValuationPoint, current ValuationPoint, params Params, anchors HistoryAnchors) SelfValuation {
	out := SelfValuation{Current: current}
	if len(series) < minPercentileSamples {
		out.Note = fmt.Sprintf("历史估值样本不足（%d 个交易日，至少需要 %d 个）；"+
			"数据湖的估值表历史很短，本功能用「股本 × 价格 ÷ 当期已披露 TTM 业绩」自建，"+
			"起点为 %d", len(series), minPercentileSamples, params.ValueStartDate)
		// 即使分位算不出来，"当前估值本身"仍然是有意义的（只是没有历史位置）
		out.CurrentPE, out.CurrentPB, out.CurrentPS = current.PE, current.PB, current.PS
		out.Score = 50
		out.StructuralShift = true
		out.StructuralReasons = append(out.StructuralReasons, out.Note)
		return out
	}

	out.Available = true
	out.CurrentPE, out.CurrentPB, out.CurrentPS = current.PE, current.PB, current.PS

	// ---- 按窗口切分样本 ----
	cut3 := dateYearsBefore(current.Date, 3)
	cut5 := dateYearsBefore(current.Date, 5)
	pe3 := collectPE(series, cut3, 0)
	pe5 := collectPE(series, cut5, 0)
	pb3 := collectPB(series, cut3, 0)
	pb5 := collectPB(series, cut5, 0)
	ps5 := collectPS(series, cut5, 0)

	out.SampleDays3Y = len(pe3)
	out.SampleDays5Y = len(pe5)

	if current.PE > 0 {
		out.PEPercentile3Y, _ = Percentile(pe3, current.PE, minPercentileSamples)
		out.PEPercentile5Y, _ = Percentile(pe5, current.PE, minPercentileSamples)
	}
	if current.PB > 0 {
		out.PBPercentile3Y, _ = Percentile(pb3, current.PB, minPercentileSamples)
		out.PBPercentile5Y, _ = Percentile(pb5, current.PB, minPercentileSamples)
	}
	if current.PS > 0 {
		out.PSPercentile5Y, _ = Percentile(ps5, current.PS, minPercentileSamples)
	}

	out.PEBand = BuildBand(pe5)
	out.PBBand = BuildBand(pb5)
	out.PSBand = BuildBand(ps5)

	// ---- 结构性变化判定 ----
	out.StructuralShift, out.StructuralReasons = detectStructuralShift(series, current, anchors)

	// ---- 归因：ΔlnPE = ΔlnP − ΔlnE ----
	out.PriceEffect, out.EarningEffect = decomposePEReturn(series, current)

	out.Score = selfValuationScore(out, current)
	return out
}

// HistoryAnchors 是判定"结构性变化"用的历史锚点。
type HistoryAnchors struct {
	// ROE3Y / ROE5Y 是近 3 年 / 近 5 年的 ROE（年报口径）。
	ROE3Y float64
	ROE5Y float64
	// Growth3Y / Growth5Y 是近 3 年 / 5 年的营收 CAGR。
	Growth3Y float64
	Growth5Y float64
}

// detectStructuralShift 判断历史估值参照是否已经不可靠。
//
// 三个判据（任一命中即降权，并把依据写进结论）：
//
//  1. **估值中枢移动**：近 3 年 PE 中位数相对近 5 年 PE 中位数偏移超过 25%。
//     典型场景：公司从高增长阶段进入成熟阶段，历史 40 倍 PE 再也不会回来。
//  2. **盈利能力中枢移动**：近 3 年 ROE 相对近 5 年 ROE 偏移超过 30%。
//  3. **成长阶段切换**：近 3 年营收 CAGR 与近 5 年 CAGR 的差值超过 10 个百分点
//     （例如从 30% 降到 5%，"历史估值分位"就比较错了对象）。
func detectStructuralShift(series []ValuationPoint, current ValuationPoint, anchors HistoryAnchors) (bool, []string) {
	reasons := make([]string, 0, 2)

	cut3 := dateYearsBefore(current.Date, 3)
	pe3 := collectPE(series, cut3, 0)
	pe5 := collectPE(series, 0, 0)
	median3 := Median(positiveOnly(pe3))
	median5 := Median(positiveOnly(pe5))
	if median3 > 0 && median5 > 0 {
		shift := math.Abs(median3-median5) / median5
		if shift > 0.25 {
			reasons = append(reasons, fmt.Sprintf(
				"估值中枢移动：近 3 年 PE 中位数 %.1f 倍 vs 近 5 年 %.1f 倍（偏移 %.0f%%）——"+
					"公司可能已切换成长阶段，历史分位的参照价值下降",
				median3, median5, shift*100))
		}
	}

	if anchors.ROE3Y != 0 && anchors.ROE5Y != 0 {
		shift := math.Abs(anchors.ROE3Y-anchors.ROE5Y) / math.Abs(anchors.ROE5Y)
		if shift > 0.30 {
			reasons = append(reasons,
				fmt.Sprintf("盈利能力中枢移动：近 3 年 ROE %.1f%% vs 近 5 年 %.1f%%（偏移 %.0f%%）",
					anchors.ROE3Y*100, anchors.ROE5Y*100, shift*100))
		}
	}

	if anchors.Growth3Y != 0 && anchors.Growth5Y != 0 {
		gap := math.Abs(anchors.Growth3Y - anchors.Growth5Y)
		if gap > 0.10 {
			reasons = append(reasons,
				fmt.Sprintf("成长阶段切换：近 3 年营收 CAGR %.1f%% vs 近 5 年 %.1f%%（相差 %.1f 个百分点）",
					anchors.Growth3Y*100, anchors.Growth5Y*100, gap*100))
		}
	}

	return len(reasons) > 0, reasons
}

// decomposePEReturn 把近一年的 PE 变化分解成价格效应与盈利效应。
//
// PE = P / E，取对数后 ΔlnPE = ΔlnP − ΔlnE。这样可以回答
// "PE 从 25 倍降到 18 倍，是股价跌了还是盈利涨了？" ——
// 两者对后市的含义完全不同（前者可能是机会，后者是基本面改善）。
func decomposePEReturn(series []ValuationPoint, current ValuationPoint) (priceEffect, earningEffect float64) {
	oneYearAgo := dateYearsBefore(current.Date, 1)
	start := -1
	for i := len(series) - 1; i >= 0; i-- {
		if series[i].Date <= oneYearAgo {
			start = i
			break
		}
	}
	if start < 0 || current.MarketCap <= 0 || current.PE <= 0 {
		return 0, 0
	}
	previous := series[start]
	if previous.PE <= 0 || previous.MarketCap <= 0 {
		return 0, 0
	}
	// 价格效应 = 市值变化（股本变化也计入市值，这一点在结论里要说明）
	priceEffect = math.Log(current.MarketCap) - math.Log(previous.MarketCap)
	// 盈利效应 = 市值效应 − PE 效应
	peEffect := math.Log(current.PE) - math.Log(previous.PE)
	earningEffect = priceEffect - peEffect
	return priceEffect, earningEffect
}

// selfValuationScore 把自身估值折成 0~100。
//
// 分位越低越便宜 → 分越高。**优先用 PE 分位**；PE 不可用（亏损）时用 PB 分位。
// 结构性变化会**把分位向 50 收缩**（方案 §5.1：降低历史估值参照的权重）——
// 收缩而不是扣分，是因为"历史分位"本身没有方向性偏见，只是不再可信。
func selfValuationScore(out SelfValuation, current ValuationPoint) float64 {
	percentile, has := 0.0, false
	switch {
	case current.PE > 0 && out.PEPercentile5Y > 0:
		percentile, has = out.PEPercentile5Y, true
	case current.PB > 0 && out.PBPercentile5Y > 0:
		percentile, has = out.PBPercentile5Y, true
	case current.PE > 0 && out.PEPercentile3Y > 0:
		percentile, has = out.PEPercentile3Y, true
	case current.PB > 0 && out.PBPercentile3Y > 0:
		percentile, has = out.PBPercentile3Y, true
	}
	if !has {
		// 没有任何可用分位：不给"看起来中性"的 50，而是标记不可用
		// （亏损失去 PE、又缺 PB 的情况确实无法判断估值）
		return 50
	}
	// 分位 0 → 88 分，分位 100 → 10 分。
	//
	// ⚠ 这里刻意**不用满 0/100**：历史分位只是"在自身历史里的相对位置"，
	// 它本身不含"这只股票值多少钱"的信息（方案 §5.1 的核心限定）。
	// 给 95/5 会让一个"跌到历史最低分位"的公司看起来几乎满分，
	// 而正确读法是"它在自己历史里最便宜，但便宜可能是基本面变坏的结果"。
	score := 88 - percentile*0.78
	if out.StructuralShift {
		score = 50 + (score-50)*0.5
	}
	return clampScore(score)
}

// collectPE / collectPB / collectPS 按日期窗口收集正样本。

func collectPE(series []ValuationPoint, after, before int64) []float64 {
	out := make([]float64, 0, len(series))
	for _, point := range series {
		if after > 0 && point.Date < after {
			continue
		}
		if before > 0 && point.Date > before {
			continue
		}
		if point.PE > 0 {
			out = append(out, point.PE)
		}
	}
	return out
}

func collectPB(series []ValuationPoint, after, before int64) []float64 {
	out := make([]float64, 0, len(series))
	for _, point := range series {
		if after > 0 && point.Date < after {
			continue
		}
		if before > 0 && point.Date > before {
			continue
		}
		if point.PB > 0 {
			out = append(out, point.PB)
		}
	}
	return out
}

func collectPS(series []ValuationPoint, after, before int64) []float64 {
	out := make([]float64, 0, len(series))
	for _, point := range series {
		if after > 0 && point.Date < after {
			continue
		}
		if before > 0 && point.Date > before {
			continue
		}
		if point.PS > 0 {
			out = append(out, point.PS)
		}
	}
	return out
}

// dateYearsBefore 返回 date 往前 n 年的 YYYYMMDD（n<=0 返回 0 表示不限）。
func dateYearsBefore(date int64, years int) int64 {
	if date <= 0 || years <= 0 {
		return 0
	}
	year := int(date / 10000)
	month := int(date / 100 % 100)
	day := int(date % 100)
	year -= years
	if year < 1900 {
		return 0
	}
	// 2 月 29 日往前推落在平年时收成 28 日，避免造出非法日期
	if month == 2 && day == 29 {
		day = 28
	}
	return int64(year*10000 + month*100 + day)
}
