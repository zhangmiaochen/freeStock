package chanlun

import "fmt"

// classifyTrend 判定当前走势类型与它的**生命周期**（方案 §5.6）。
//
// 完全分类只有三种：盘整、上涨趋势、下跌趋势。"反转"不是独立走势类型，
// 它用信号状态（反转候选 / 反转确认）表达，这里不产生第四种走势。
//
// 判定规则（与方案一致）：
//   - 至少两个中枢，且后一个中枢整体高于前一个（ZD2 > ZG1）→ 上涨趋势；
//   - 至少两个中枢，且后一个中枢整体低于前一个（ZG2 < ZD1）→ 下跌趋势；
//   - 其余（单个中枢 / 多个中枢互相重叠）→ 盘整；
//   - **没有中枢 → 走势未完成**（TrendNone），此时不输出趋势背驰与三类买卖点。
//
// 除类型外还输出两样东西（对应"走势终完美"与"不预测只应对"）：
//   - `Status`：这段走势走到哪一步了（形成中 / 延伸中 / 已完成）；
//   - `Outlook`：当前走势接下来**只有这几种可能**，各自的结构条件与当前状态。
func classifyTrend(pivots []Pivot, strokes []Stroke, bars []Bar, level Level) Trend {
	trend := Trend{
		Type:  TrendNone,
		Level: level,
		Note:  "尚无中枢，走势未完成：不输出趋势背驰与三类买卖点",
	}
	if len(bars) > 0 {
		trend.StartDate = bars[0].Date
		trend.EndDate = bars[len(bars)-1].Date
	}
	if len(pivots) == 0 {
		trend.TypeCN = trendTypeCN(TrendNone)
		trend.Status = TrendForming
		trend.StatusCN = TrendStatusCN(TrendForming)
		return trend
	}

	last := pivots[len(pivots)-1]
	trend.StartDate = pivots[0].StartDate
	trend.PivotIndex = []int{last.Index}

	if len(pivots) >= 2 {
		prev := pivots[len(pivots)-2]
		trend.PivotIndex = []int{prev.Index, last.Index}
		switch {
		case last.ZD > prev.ZG:
			trend.Type = TrendUp
			trend.Note = "至少两个依次抬高的中枢 → 上涨趋势"
		case last.ZG < prev.ZD:
			trend.Type = TrendDown
			trend.Note = "至少两个依次降低的中枢 → 下跌趋势"
		default:
			trend.Type = TrendConsolidation
			trend.Note = "中枢互相重叠，未形成趋势 → 盘整"
		}
	} else {
		trend.Type = TrendConsolidation
		trend.Note = "只有一个中枢 → 盘整（盘整背驰只适用于这种结构）"
	}

	trend.TypeCN = trendTypeCN(trend.Type)

	// ---- 生命周期：这段走势完成了吗？ ----
	if idx, reason, completedBy, ok := trendCompletion(pivots, strokes, bars, trend.Type); ok {
		trend.Status = TrendCompleted
		trend.CompletedDate = bars[idx].Date
		trend.CompletionReason = reason
		trend.CompletedBy = completedBy
	} else {
		trend.Status = TrendExtending
	}
	trend.StatusCN = TrendStatusCN(trend.Status)

	// ---- 完全分类：接下来只有这几种可能 ----
	trend.Outlook = buildOutlook(pivots, bars, trend)
	for i := range trend.Outlook {
		trend.Outlook[i].StatusCN = ScenarioStatusCN(trend.Outlook[i].Status)
	}
	return trend
}

// trendCompletion 判断当前走势是否已经"完成"，以及是被什么结构破坏的。
//
// 完成条件（全部用**已经确认的笔**，不用未来数据）：
//
//	上涨趋势：出现向下笔跌破最后一个中枢的下沿 ZD；
//	下跌趋势：出现向上笔升破最后一个中枢的上沿 ZG；
//	盘整    ：离开中枢后**回抽不回**（向上离开 + 回抽低点仍高于 ZG，或向下对称）。
//
// 为什么"跌破最后一个中枢"就算趋势完成：趋势的定义就是"同向中枢依次排列"，
// 最后一个中枢被反向破坏，这段趋势的结构就不成立了 —— 这正是"走势终完美"
// 在系统里的可编程判据。注意它只说明**这段走势结束**，不保证反向趋势立刻成立。
func trendCompletion(
	pivots []Pivot,
	strokes []Stroke,
	bars []Bar,
	trendType TrendType,
) (int, string, string, bool) {
	if len(pivots) == 0 || len(bars) == 0 {
		return 0, "", "", false
	}
	last := pivots[len(pivots)-1]

	switch trendType {
	case TrendUp:
		for _, stroke := range strokes {
			if !stroke.Confirmed || stroke.Direction >= 0 || stroke.EndIndex <= last.EndIndex {
				continue
			}
			if stroke.EndPrice < last.ZD {
				return confirmedStrokeIndex(stroke, bars),
					fmt.Sprintf("向下笔跌破最后一个中枢下沿 ZD=%.2f，上涨趋势完成", last.ZD),
					"breakdown", true
			}
		}
	case TrendDown:
		for _, stroke := range strokes {
			if !stroke.Confirmed || stroke.Direction <= 0 || stroke.EndIndex <= last.EndIndex {
				continue
			}
			if stroke.EndPrice > last.ZG {
				return confirmedStrokeIndex(stroke, bars),
					fmt.Sprintf("向上笔升破最后一个中枢上沿 ZG=%.2f，下跌趋势完成", last.ZG),
					"breakup", true
			}
		}
	case TrendConsolidation:
		// 向上离开：向上笔越过 ZG，之后第一笔已确认的向下笔低点仍在 ZG 之上
		for i := 0; i < len(strokes); i++ {
			up := strokes[i]
			if !up.Confirmed || up.Direction <= 0 || up.EndIndex <= last.EndIndex || up.EndPrice <= last.ZG {
				continue
			}
			for j := i + 1; j < len(strokes); j++ {
				down := strokes[j]
				if !down.Confirmed || down.Direction >= 0 {
					continue
				}
				if down.Low > last.ZG {
					return confirmedStrokeIndex(down, bars),
						fmt.Sprintf("向上离开中枢后回抽不回 ZG=%.2f（三买结构），盘整结束", last.ZG),
						"leave-up", true
				}
				break
			}
		}
		// 向下离开：向下笔跌破 ZD，之后第一笔已确认的向上笔高点仍在 ZD 之下
		for i := 0; i < len(strokes); i++ {
			down := strokes[i]
			if !down.Confirmed || down.Direction >= 0 || down.EndIndex <= last.EndIndex || down.EndPrice >= last.ZD {
				continue
			}
			for j := i + 1; j < len(strokes); j++ {
				up := strokes[j]
				if !up.Confirmed || up.Direction <= 0 {
					continue
				}
				if up.High < last.ZD {
					return confirmedStrokeIndex(up, bars),
						fmt.Sprintf("向下离开中枢后反抽不回 ZD=%.2f（三卖结构），盘整结束", last.ZD),
						"leave-down", true
				}
				break
			}
		}
	}
	return 0, "", "", false
}

// confirmedStrokeIndex 返回一笔被确认的那根 K 线下标（分型确认日）。
func confirmedStrokeIndex(stroke Stroke, bars []Bar) int {
	for i, bar := range bars {
		if bar.Date == stroke.ConfirmDate {
			return i
		}
	}
	if stroke.EndIndex >= 0 && stroke.EndIndex < len(bars) {
		return stroke.EndIndex
	}
	return 0
}

// buildOutlook 给出当前走势的**完全分类**：接下来只有这几种可能。
//
// 这是"不预测，只应对"的落地方式：系统不判断"明天涨还是跌"，
// 而是把结构上可能的走向都列出来，并说明每一种的成立条件与当前状态。
func buildOutlook(pivots []Pivot, bars []Bar, trend Trend) []Scenario {
	if len(pivots) == 0 || len(bars) == 0 {
		return nil
	}
	last := pivots[len(pivots)-1]
	close := bars[len(bars)-1].Close
	completed := trend.Status == TrendCompleted

	switch trend.Type {
	case TrendUp:
		continuation := ScenarioActive
		consolidation := ScenarioPending
		reversal := ScenarioPending
		switch {
		case completed && trend.CompletedBy == "breakdown":
			// 跌破最后一个中枢：至少转为盘整，反向走势是否成立要看后续中枢
			continuation = ScenarioInvalidated
			consolidation = ScenarioConfirmed
			reversal = ScenarioActive
		case close < last.ZD:
			// 价格已在中枢下沿之下：延续被破坏，但还要等笔确认才算"趋势完成"。
			// 反向趋势也还没形成（缺反向中枢），所以是"待发生"而不是"进行中"。
			continuation = ScenarioInvalidated
			consolidation = ScenarioActive
			reversal = ScenarioPending
		case close <= last.ZG:
			// 回到中枢内部：趋势没破，但也没在延续 → 延伸暂停、盘整进行中
			continuation = ScenarioPaused
			consolidation = ScenarioActive
		}
		return []Scenario{
			{
				Key: "continuation", Label: "上涨趋势延续",
				Condition: fmt.Sprintf("不跌破最后一个中枢下沿 ZD=%.2f", last.ZD),
				Status:    continuation,
				Note:      "中枢不破，趋势结构仍在",
			},
			{
				Key: "consolidation", Label: "转为盘整",
				Condition: fmt.Sprintf("回到中枢 %.2f ~ %.2f 区间震荡", last.ZD, last.ZG),
				Status:    consolidation,
				Note:      "趋势完成后至少转为盘整",
			},
			{
				Key: "reversal", Label: "转为下跌趋势",
				Condition: fmt.Sprintf("跌破 ZD=%.2f 后反抽不回，并形成反向中枢", last.ZD),
				Status:    reversal,
				Note:      "反转要靠结构确认，背驰本身不保证",
			},
		}

	case TrendDown:
		continuation := ScenarioActive
		consolidation := ScenarioPending
		reversal := ScenarioPending
		switch {
		case completed && trend.CompletedBy == "breakup":
			continuation = ScenarioInvalidated
			consolidation = ScenarioConfirmed
			reversal = ScenarioActive
		case close > last.ZG:
			continuation = ScenarioInvalidated
			consolidation = ScenarioActive
			reversal = ScenarioPending
		case close >= last.ZD:
			continuation = ScenarioPaused
			consolidation = ScenarioActive
		}
		return []Scenario{
			{
				Key: "continuation", Label: "下跌趋势延续",
				Condition: fmt.Sprintf("不升破最后一个中枢上沿 ZG=%.2f", last.ZG),
				Status:    continuation,
				Note:      "中枢不破，趋势结构仍在",
			},
			{
				Key: "consolidation", Label: "转为盘整",
				Condition: fmt.Sprintf("回到中枢 %.2f ~ %.2f 区间震荡", last.ZD, last.ZG),
				Status:    consolidation,
				Note:      "趋势完成后至少转为盘整",
			},
			{
				Key: "reversal", Label: "转为上涨趋势",
				Condition: fmt.Sprintf("升破 ZG=%.2f 后回抽不回，并形成反向中枢", last.ZG),
				Status:    reversal,
				Note:      "反转要靠结构确认，背驰本身不保证",
			},
		}

	default: // 盘整（含无中枢时由调用方跳过）
		leaveUp := ScenarioPending
		leaveDown := ScenarioPending
		continuation := ScenarioActive
		if completed {
			continuation = ScenarioInvalidated
			switch trend.CompletedBy {
			case "leave-up":
				leaveUp = ScenarioConfirmed
				leaveDown = ScenarioInvalidated
			case "leave-down":
				leaveDown = ScenarioConfirmed
				leaveUp = ScenarioInvalidated
			}
		} else if close > last.ZG {
			leaveUp = ScenarioActive
			continuation = ScenarioInvalidated
		} else if close < last.ZD {
			leaveDown = ScenarioActive
			continuation = ScenarioInvalidated
		}
		return []Scenario{
			{
				Key: "leave-up", Label: "向上离开中枢（转上涨）",
				Condition: fmt.Sprintf("升破 ZG=%.2f 且回抽不回中枢", last.ZG),
				Status:    leaveUp,
				Note:      "对应三买结构：突破后回抽不回中枢",
			},
			{
				Key: "leave-down", Label: "向下离开中枢（转下跌）",
				Condition: fmt.Sprintf("跌破 ZD=%.2f 且反抽不回中枢", last.ZD),
				Status:    leaveDown,
				Note:      "对应三卖结构：跌破后反抽不回中枢",
			},
			{
				Key: "continuation", Label: "盘整延续",
				Condition: fmt.Sprintf("价格继续在 %.2f ~ %.2f 内震荡", last.ZD, last.ZG),
				Status:    continuation,
				Note:      "中枢震荡，方向未定",
			},
		}
	}
}

// trendTypeCN 返回走势类型中文名。
func trendTypeCN(t TrendType) string {
	switch t {
	case TrendUp:
		return "上涨趋势"
	case TrendDown:
		return "下跌趋势"
	case TrendConsolidation:
		return "盘整"
	}
	return "无中枢（走势未完成）"
}

// detectPosition 判断当前价格在最后一个中枢结构中的位置（方案 §9.3）。
//
// 输出：中枢前 / 中枢中 / 中枢后 / 离开段 / 回抽段。
// 这个信息是"操作层面"最直接的一句回答，所以单独建模而不是让前端去猜。
func detectPosition(pivots []Pivot, strokes []Stroke, bars []Bar) Position {
	pos := Position{Phase: PhaseUnknown, PhaseCN: "未知", PivotIndex: -1}
	if len(bars) == 0 {
		return pos
	}
	closePrice := bars[len(bars)-1].Close

	if len(pivots) == 0 {
		pos.Phase = PhaseBefore
		pos.PhaseCN = "中枢前"
		pos.Note = "尚未形成中枢"
		return pos
	}

	pivot := pivots[len(pivots)-1]
	pos.PivotIndex = pivot.Index
	pos.BarsFromPivot = countBarsFrom(bars, pivot.EndDate)

	// 最近一笔的方向与终点，决定"离开 / 回抽 / 回到中枢"
	var lastStroke *Stroke
	if len(strokes) > 0 {
		lastStroke = &strokes[len(strokes)-1]
	}

	switch {
	case closePrice > pivot.ZG:
		if lastStroke != nil && lastStroke.Direction > 0 {
			pos.Phase = PhaseDeparture
			pos.PhaseCN = "离开段（向上）"
			pos.Note = "价格已向上离开中枢，关注回抽是否不回到中枢（三买前提）"
		} else {
			pos.Phase = PhasePullback
			pos.PhaseCN = "回抽段（向上离开后）"
			pos.Note = "向上离开后正在回抽，回抽不跌回中枢 ZG 之下才成立三买"
		}
	case closePrice < pivot.ZD:
		if lastStroke != nil && lastStroke.Direction < 0 {
			pos.Phase = PhaseDeparture
			pos.PhaseCN = "离开段（向下）"
			pos.Note = "价格已向下离开中枢，关注反抽是否不回到中枢（三卖前提）"
		} else {
			pos.Phase = PhasePullback
			pos.PhaseCN = "回抽段（向下离开后）"
			pos.Note = "向下离开后正在回抽，反抽不涨回中枢 ZD 之上才成立三卖"
		}
	case closePrice >= pivot.ZD && closePrice <= pivot.ZG:
		pos.Phase = PhaseInside
		pos.PhaseCN = "中枢中"
		pos.Note = "价格仍在中枢重叠区内，属中枢震荡"
	default:
		pos.Phase = PhaseAfter
		pos.PhaseCN = "中枢后"
	}
	return pos
}

// countBarsFrom 返回从某个日期之后到现在有多少根 K 线。
func countBarsFrom(bars []Bar, date int) int {
	count := 0
	for i := len(bars) - 1; i >= 0; i-- {
		if bars[i].Date <= date {
			break
		}
		count++
	}
	return count
}
