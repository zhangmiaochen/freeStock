package chanlun

import "math"

// 股票适配性评分（方案 §10.5）。
//
// 缠论结构的可靠性依赖"有足够多的参与者、足够长的历史、足够完整的多级别数据"。
// 小市值/低流动性标的容易被单一资金画出漂亮的结构，所以：
//
//   - 流动性、成交额、上市时长、多级别完整性、结构稳定性各占一档权重；
//   - 小流通市值额外扣分（不是一票否决，而是降权 —— 方案说的是"降权或剔除"）；
//   - 评分只影响综合分与选股阈值，**不改变结构定义本身**。

// assessAdaptability 计算股票适配性评分。
func assessAdaptability(bars []Bar, strokes []Stroke, info AdaptInfo) Adaptability {
	out := Adaptability{}
	if len(bars) == 0 {
		out.Grade = "D"
		out.Reasons = []string{"没有 K 线数据"}
		return out
	}

	// ---- 流动性：最近 20 根的平均成交额 ----
	avgAmount := info.Amount
	if avgAmount <= 0 {
		sum := 0.0
		count := 0
		for i := len(bars) - 1; i >= 0 && i >= len(bars)-20; i-- {
			if bars[i].Amount > 0 {
				sum += bars[i].Amount
				count++
			}
		}
		if count > 0 {
			avgAmount = sum / float64(count)
		}
	}
	out.Amount = round4(avgAmount)
	out.Liquidity = liquidityScore(avgAmount)

	// 指数：没有"流通市值太小被操纵"的问题，成交额也是全市场口径，不按个股扣分
	if info.IsIndex {
		out.Liquidity = 100
		out.Reasons = append(out.Reasons,
			"指数：不存在小市值操纵问题，适配性只反映历史长度与数据完整性")
	}

	// ---- 历史长度：3 年（约 750 根）为满分 ----
	out.History = clamp01(float64(len(bars))/750) * 100

	// ---- 多级别完整性 ----
	out.Completeness = 100
	if info.LevelTotal > 0 {
		out.Completeness = clamp01(float64(info.Levels)/float64(info.LevelTotal)) * 100
	}
	if info.HasIntraday {
		out.Completeness = math.Min(100, out.Completeness+10)
	}

	// ---- 结构稳定性：过短的笔占比越高，结构越毛躁 ----
	out.Stability = structureStability(strokes)

	out.Score = round1(
		out.Liquidity*0.35 +
			out.History*0.15 +
			out.Completeness*0.20 +
			out.Stability*0.30,
	)

	// ---- 小市值扣分（易被单一资金操纵） ----
	// 指数不参与这部分：它的"成分"是全市场或一篮子股票，不存在被单一资金画图的问题。
	if !info.IsIndex {
		switch {
		case info.FloatMV > 0 && info.FloatMV < 1e9:
			out.Penalty = 25
			out.Reasons = append(out.Reasons, "流通市值不足 10 亿，结构易被单一资金影响，建议放大级别、降低仓位")
		case info.FloatMV > 0 && info.FloatMV < 3e9:
			out.Penalty = 15
			out.Reasons = append(out.Reasons, "流通市值不足 30 亿，结构可靠性一般")
		}
		if avgAmount > 0 && avgAmount < 3e7 {
			out.Penalty = math.Max(out.Penalty, 20)
			out.Reasons = append(out.Reasons, "日均成交额偏低（< 3000 万），流动性与结构可靠性都受限")
		}
	}
	if len(bars) < 250 {
		out.Penalty = math.Max(out.Penalty, 10)
		out.Reasons = append(out.Reasons, "历史不足 1 年，笔与中枢的可信度偏低")
	}
	out.Score = clampScore(out.Score - out.Penalty)

	out.Grade = "D"
	switch {
	case out.Score >= 80:
		out.Grade = "A"
	case out.Score >= 65:
		out.Grade = "B"
	case out.Score >= 50:
		out.Grade = "C"
	}

	if len(out.Reasons) == 0 {
		out.Reasons = append(out.Reasons, "流动性、历史长度与数据完整性均满足结构分析要求")
	}
	return out
}

// liquidityScore 把日均成交额映射成 0~100（对数尺度：1 千万 ≈ 0，30 亿 ≈ 100）。
func liquidityScore(avgAmount float64) float64 {
	if avgAmount <= 0 {
		return 0
	}
	value := (math.Log10(avgAmount) - 7) / (9.5 - 7) * 100
	return clampScore(value)
}

// structureStability 用"过短的笔"占比衡量结构稳定性。
//
// 笔跨越的 K 线越少，说明这段走势越毛躁；这样的结构即使画出中枢也很难被后续验证。
func structureStability(strokes []Stroke) float64 {
	if len(strokes) == 0 {
		return 50
	}
	short := 0
	for _, stroke := range strokes {
		if stroke.EndIndex-stroke.StartIndex < 7 {
			short++
		}
	}
	ratio := float64(short) / float64(len(strokes))
	return clampScore((1 - ratio) * 100)
}

func clampScore(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return round1(value)
}

// buildDataInfo 汇总数据完整性信息（方案 §3.2、§4.4）。
func buildDataInfo(bars []Bar, merged []MergedBarInfo, info AdaptInfo, level Level) DataInfo {
	out := DataInfo{
		Bars:        len(bars),
		MergedBars:  len(merged),
		HasIntraday: info.HasIntraday,
	}
	if len(bars) > 0 {
		out.StartDate = bars[0].Date
		out.EndDate = bars[len(bars)-1].Date
	}
	switch {
	case info.HasIntraday:
		out.PrecisionNote = "已有多级别/分钟数据，可做区间套逐级定位"
	case level == LevelDayStroke:
		out.PrecisionNote = "只有日 K：最小级别为日线笔，只能输出分型与笔，不做区间套"
	default:
		out.PrecisionNote = "只有日 K：区间套只能在日线内部做，无法做真正的分钟级区间套，精度有限"
	}
	return out
}
