package valuation

import "strconv"

// 本文件集中放"没有行业历史分位时怎么给行业打环境分"这一条口径。
//
// 为什么需要单独一处：本机 `industry_members` 只有 5 个快照，所以
// `PEPercentile` 在绝大多数调用里都是 0。如果此时简单地给 50 分，
// **所有行业的"行业估值环境"维度都会是同一个分**，这个维度就变成了常数、
// 对最终排序毫无贡献 —— 那等于白设一个维度（实测第一版就是这样，
// 60 个行业的行业环境分全是 50.0）。
//
// 替代口径只能从**同期横截面**里取，而且必须是有经济含义的量：
//
//  1. **加权 PE 与中位 PE 的背离**：加权远低于中位，说明行业里存在市值大、
//     估值低的龙头（银行、保险的典型形态），行业整体并不像加权值那么便宜；
//     反过来加权远高于中位则是高估值大票在拉动。
//  2. **权重集中度**：前 3 大权重占比过高时，加权值的代表性下降，向中性收敛。
//
// 明确不做的事：**不拿"加权 PE 的绝对值高低"当便宜/贵的判据**。
// 不同行业的合理 PE 中枢差别极大（银行 6 倍、半导体 60 倍都可能是合理的），
// 用绝对倍数横向比较只会得出"银行永远最便宜"这种无用结论。
//
// 局限也要说清楚：这个替代口径衡量的是"行业内部估值结构"，
// **不是"行业整体贵不贵"**。真正回答后者需要行业历史分位，等
// industry_members 的历史补齐后会自动切回分位口径（见 industryScore）。

// industryScoreWithoutHistory 在缺少历史分位时，用横截面结构给行业环境分。
func industryScoreWithoutHistory(out IndustryValuation) (float64, string) {
	if out.MedianPE <= 0 || out.WeightedPE <= 0 {
		// 连中位数与加权都算不出来（成分太少或普遍亏损）：只能给中性
		return 50, "行业 PE 无法计算（成分过少或普遍亏损），行业估值环境按中性处理"
	}

	ratio := out.WeightedPE / out.MedianPE
	// 加权/中位 = 1 → 50 分；比值越小（加权比中位便宜）分越高。
	// 每 10% 的背离给 6 分，并夹在 [20,80] 内。
	score := 50.0 - (ratio-1)*60
	score = clampRange(score, 20, 80)

	note := ""
	switch {
	case ratio <= 0.85:
		note = "加权 PE 明显低于中位 PE，说明低估值大市值成分在拉低行业加权值 —— 行业整体并不像加权值那么便宜"
	case ratio >= 1.15:
		note = "加权 PE 明显高于中位 PE，说明高估值大市值成分在拉高行业加权值 —— 行业里多数公司其实更便宜"
	default:
		note = "加权 PE 与中位 PE 接近，行业内部估值分歧不大"
	}
	detail := "行业历史估值分位不可用（industry_members 只有 5 个快照），改用横截面结构口径：" +
		note + "（加权 PE ÷ 中位 PE = " + strconv.FormatFloat(ratio, 'f', 2, 64) + "）"

	if out.Top3Weight > 0.50 {
		score = 50 + (score-50)*0.6
		detail += "；前 3 大权重合计超过 50%，加权值被少数公司主导，已向中性收敛"
	}
	return score, detail
}

func clampRange(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
