package chanlun

// 多级别印证与区间套（方案 §3.4、§6.5）。
//
// 关键约束：**每个级别都必须独立算**（各自的分型、笔、中枢、走势），
// 大级别只用来"印证与压制"，不允许用它推出小级别的画线。
// 因此这里的输入是各个级别**分别算好的** Analysis，本文件只做：
//
//  1. 汇总每个级别的状态（LevelSummary）；
//  2. 大小级别方向一致 → 共振（综合分提升）；
//  3. 大小级别方向相反 → 大级别压制（综合分下调并写提示）；
//  4. 两个级别同时出现同向背驰 → 区间套（Nested）。

// BuildMultiLevel 生成多级别印证结果，并就地调整各级别信号的综合分与提示。
//
// `order` 是按级别从低到高排列的分析结果（同一只票、同一复权方式）。
func BuildMultiLevel(order []*Analysis, cfg Config) MultiLevel {
	out := MultiLevel{Levels: make([]LevelSummary, 0, len(order))}
	byLevel := make(map[Level]*Analysis, len(order))
	for _, analysis := range order {
		if analysis == nil {
			continue
		}
		byLevel[analysis.Level] = analysis
	}

	for _, analysis := range order {
		if analysis == nil {
			continue
		}
		parent := byLevel[ParentLevel(analysis.Level)]
		suppressed := 0
		boosted := 0

		for i := range analysis.Signals {
			sig := &analysis.Signals[i]
			if sig.State == StateInvalid || sig.State == StateExpired {
				continue
			}
			switch {
			case parent == nil:
				// 最高级别没有更大的级别可以印证
			case signalAlignedWithParent(*sig, parent):
				sig.Score = clampScore(sig.Score * 1.15)
				sig.ScoreFactors = append(sig.ScoreFactors, ScoreFactor{
					Name: "多级别共振", Value: 1.15,
					Note: "更高级别方向与它一致（" + LevelCN(parent.Level) + "）",
				})
				boosted++
			case signalAgainstParent(*sig, parent):
				sig.Score = clampScore(sig.Score * 0.8)
				sig.ScoreFactors = append(sig.ScoreFactors, ScoreFactor{
					Name: "大级别压制", Value: 0.8,
					Note: "更高级别方向相反（" + LevelCN(parent.Level) + "），信号降权",
				})
				if sig.Note == "" {
					sig.Note = "与大级别方向相反，参考价值下降"
				}
				suppressed++
			}
		}
		// 分数变了，排序也要重排
		sortSignals(analysis.Signals, analysis.Trend)

		if suppressed > 0 {
			analysis.Notes = append(analysis.Notes,
				itoa(suppressed)+" 条信号与大级别方向相反，已按「大级别压制小级别」降权")
		}
		if boosted > 0 {
			analysis.Notes = append(analysis.Notes,
				itoa(boosted)+" 条信号与更高级别方向一致，已按多级别共振提升综合分")
		}

		out.Levels = append(out.Levels, summarize(analysis))
	}

	// ---- 区间套：相邻两个级别同时出现同向背驰 ----
	for i := 0; i+1 < len(out.Levels); i++ {
		lower := byLevel[out.Levels[i].Level]
		upper := byLevel[out.Levels[i+1].Level]
		if lower == nil || upper == nil {
			continue
		}
		if sharesDivergence(lower, upper) {
			out.Nested = true
			out.Notes = append(out.Notes,
				LevelCN(lower.Level)+"与"+LevelCN(upper.Level)+"同时出现同向背驰，构成区间套定位")
			break
		}
	}

	// ---- 共振：任一级别出现"与更高级别同向"的有效信号 ----
	for _, summary := range out.Levels {
		for _, sig := range summary.Signals {
			if sig.State != StateConfirmed && sig.State != StateTriggered {
				continue
			}
			for _, factor := range sig.ScoreFactors {
				if factor.Name == "多级别共振" {
					out.Resonance = true
				}
			}
		}
	}
	if out.Resonance {
		out.Note = "多级别方向一致：这类信号综合分最高，优先关注"
	}
	if out.Nested {
		out.Note = "构成区间套：大级别定方向、小级别精确定位；仍需结合更大级别环境"
	}
	if !out.Resonance && !out.Nested {
		out.Note = "各别级别方向不完全一致：按大级别压制小级别处理，冲突时不作为高优先级参考"
	}
	if len(out.Levels) <= 1 {
		out.Notes = append(out.Notes,
			"只有一个级别参与印证：区间套精度有限（只有日 K 时无法做真正的分钟级区间套）")
	}
	return out
}

// summarize 生成单级别摘要。
func summarize(analysis *Analysis) LevelSummary {
	summary := LevelSummary{
		Level:     analysis.Level,
		LevelCN:   analysis.LevelCN,
		Trend:     analysis.Trend.Type,
		TrendCN:   analysis.Trend.TypeCN,
		StateName: analysis.State.Name,
		StateCN:   analysis.State.NameCN,
		Pivots:    len(analysis.Pivots),
		Signals:   analysis.Signals,
		Note:      analysis.State.Note,
	}
	for _, sig := range analysis.Signals {
		if sig.Conflict {
			summary.HasConflict = true
			break
		}
	}
	return summary
}

// signalAlignedWithParent 判断信号是否与更高级别方向一致。
func signalAlignedWithParent(sig Signal, parent *Analysis) bool {
	want := 1 // 买点期望大级别向上
	if sig.Direction == "sell" {
		want = -1
	}
	parentDirection := parentDirection(parent)
	if parentDirection == 0 {
		return false
	}
	return parentDirection == want
}

// signalAgainstParent 判断信号是否与更高级别方向相反。
func signalAgainstParent(sig Signal, parent *Analysis) bool {
	return !signalAlignedWithParent(sig, parent) && parentDirection(parent) != 0
}

// parentDirection 返回更高级别的方向：+1 向上 / -1 向下 / 0 无方向。
func parentDirection(parent *Analysis) int {
	switch parent.Trend.Type {
	case TrendUp:
		return 1
	case TrendDown:
		return -1
	}
	switch parent.State.Name {
	case StateReverseUp, StateReversalBuy:
		return 1
	case StateReverseDown, StateReversalSell:
		return -1
	}
	return 0
}

// sharesDivergence 判断两个级别是否有同向背驰。
func sharesDivergence(lower, upper *Analysis) bool {
	for _, a := range lower.Divergences {
		for _, b := range upper.Divergences {
			if a.Direction == b.Direction {
				return true
			}
		}
	}
	return false
}
