package chanlun

// 背驰（方案 §6）：
//
//   - **前提**：趋势背驰必须先有趋势（至少两个同向中枢）；盘整背驰只适用于一个中枢的盘整；
//     **没有中枢就没有背驰** —— 这条在 detectDivergences 的入口直接拦掉。
//   - **比较对象**：离开中枢的同向两段，不是相邻两笔，也不是简单比 MACD 柱子。
//   - **力度**：结构 / MACD / 斜率 / 量能四维加权，后一段明显小于前一段才构成背驰。
//   - 背驰只保证至少回到最后一个中枢，**不保证反转**；后续三种可能由界面提示。

// legStats 是一段走势的力度原始指标。
type legStats struct {
	bars      int
	priceMove float64 // 起止价差（绝对值）
	avgRange  float64 // 平均 K 线振幅
	macdArea  float64 // |MACD 柱| 面积
	macdPeak  float64 // |DIF| 峰值
	avgVolume float64 // 平均成交量
}

// measureLeg 统计一段（原始下标区间）的力度。
func measureLeg(bars []Bar, macd []macdPoint, start, end int) legStats {
	if start > end {
		start, end = end, start
	}
	if start < 0 {
		start = 0
	}
	if end >= len(bars) {
		end = len(bars) - 1
	}
	stats := legStats{}
	if start > end || start >= len(bars) {
		return stats
	}
	for i := start; i <= end; i++ {
		bar := bars[i]
		stats.bars++
		stats.avgRange += bar.High - bar.Low
		stats.avgVolume += bar.Volume
		if i < len(macd) {
			stats.macdArea += absFloat(macd[i].Hist)
			if absFloat(macd[i].Diff) > stats.macdPeak {
				stats.macdPeak = absFloat(macd[i].Diff)
			}
		}
	}
	if stats.bars > 0 {
		stats.avgRange /= float64(stats.bars)
		stats.avgVolume /= float64(stats.bars)
	}
	return stats
}

// legRef 是一段参与力度比较的走势（用原始 K 线下标区间表示）。
type legRef struct {
	startIndex int
	endIndex   int
	startPrice float64
	endPrice   float64
	high       float64
	low        float64
}

// detectDivergences 检测背驰。
//
// 趋势背驰对**每一对依次抬高/降低的中枢**都判定一次，而不是只看最后两个：
// 只看最后一对的话，历史上一买/一卖永远不会出现（它们恰恰发生在过去的某对中枢之间），
// 状态机与回放也就没有历史可讲。每一对的"后一段"都以**下一个中枢的起点**为界，
// 所以判定只用当时已经走出来的走势，不会因为后面的中枢而改变。
func detectDivergences(
	bars []Bar,
	strokes []Stroke,
	pivots []Pivot,
	trend Trend,
	macd []macdPoint,
	cfg Config,
) []Divergence {
	// 没有中枢就没有背驰（方案 §6.1）
	if len(pivots) == 0 {
		return nil
	}
	divCfg := cfg.Divergence
	out := make([]Divergence, 0, 4)

	// ---- 趋势背驰：逐对中枢判定 ----
	for i := 1; i < len(pivots); i++ {
		prevPivot := pivots[i-1]
		pivot := pivots[i]

		direction := 0
		switch {
		case pivot.ZD > prevPivot.ZG:
			direction = 1
		case pivot.ZG < prevPivot.ZD:
			direction = -1
		}
		if direction == 0 {
			continue // 两个中枢重叠 → 属于盘整，不是趋势
		}

		// 两段都取"离开各自中枢的同向一笔"：长度可比、口径一致。
		// 若一边取单笔、另一边取整段（跨多个笔），MACD 面积会差几十倍，
		// 力度比恒大于 1，背驰永远出不来。
		legA, okA := departureStrokeLeg(prevPivot, strokes, bars, direction)
		legB, okB := departureStrokeLeg(pivot, strokes, bars, direction)
		if !okA || !okB || legA.endIndex == legB.endIndex {
			continue
		}
		div := compareLegs(bars, macd, legA, legB, direction, "trend", pivot.Index, strokes, divCfg)
		if div == nil || !div.NewExtreme || div.Ratio >= divCfg.Threshold {
			// 没有创新高/新低、或力度没有明显变弱 → 不是背驰
			continue
		}
		div.Level = trend.Level
		out = append(out, *div)
	}

	// ---- 盘整背驰：只适用于"当前处于盘整"的那一个中枢 ----
	if trend.Type == TrendConsolidation {
		pivot := pivots[len(pivots)-1]
		for _, direction := range []int{1, -1} {
			legB, okB := departureStrokeLeg(pivot, strokes, bars, direction)
			if !okB {
				continue
			}
			legA, okA := legBeforePivot(pivot, strokes, bars, direction)
			if !okA {
				continue
			}
			div := compareLegs(bars, macd, legA, legB, direction, "consolidation", pivot.Index, strokes, divCfg)
			if div == nil || div.Ratio >= divCfg.Threshold {
				continue
			}
			div.Level = trend.Level
			out = append(out, *div)
		}
	}

	for i := range out {
		out[i].Index = i
	}
	return out
}

// resolveDivergenceOutcomes 给每个背驰判定**背驰后的三种可能**各自的状态。
//
// 缠论对背驰的保证非常克制：只保证至少回到最后一个中枢，**不保证反转**。
// 之后的走向有三种：反转 / 盘整 / 背驰失败（继续原趋势）。这里用已经发生的走势
// 给三种可能打状态（而不是把背驰直接当成买卖信号）：
//
//	反转       —— 由该背驰派生的一买/一卖（或二买/二卖）已经确认；
//	盘整       —— 背驰之后出现了与最后一个中枢重叠的新中枢；
//	背驰失败   —— 继续原趋势，创出新低/新高（超过失效容差）。
//
// 取**最早发生**的那一个作为结果：它一发生，另外两条分支就被排除；
// 都还没发生则全部是"待发生"。所有日期都取事件真正成立的那一天，不含未来数据。
func resolveDivergenceOutcomes(
	divs []Divergence,
	signals []Signal,
	pivots []Pivot,
	bars []Bar,
	cfg Config,
) []Divergence {
	if len(divs) == 0 || len(bars) == 0 {
		return divs
	}
	tolerance := cfg.InvalidTolerance

	for i := range divs {
		div := &divs[i]
		if div.LegBEnd < 0 || div.LegBEnd >= len(bars) {
			continue
		}
		isBottom := div.Direction == "bottom"
		extreme := bars[div.LegBEnd].Low
		if !isBottom {
			extreme = bars[div.LegBEnd].High
		}

		// ---- 背驰失败：继续原趋势并创新低/新高 ----
		failureDate := 0
		for k := div.LegBEnd + 1; k < len(bars); k++ {
			bar := bars[k]
			if isBottom && bar.Low > 0 && bar.Low < extreme*(1-tolerance) {
				failureDate = bar.Date
				break
			}
			if !isBottom && bar.High > 0 && bar.High > extreme*(1+tolerance) {
				failureDate = bar.Date
				break
			}
		}

		// ---- 反转：该背驰派生的一买/二买（或一卖/二卖）已经确认 ----
		reversalDate := 0
		for _, sig := range signals {
			if sig.DivergenceIndex != div.Index || sig.ConfirmDate <= 0 {
				continue
			}
			if isBottom && sig.Direction != "buy" {
				continue
			}
			if !isBottom && sig.Direction != "sell" {
				continue
			}
			if sig.State != StateConfirmed {
				continue
			}
			if reversalDate == 0 || sig.ConfirmDate < reversalDate {
				reversalDate = sig.ConfirmDate
			}
		}

		// ---- 盘整：背驰之后形成了与最后一个中枢重叠的新中枢 ----
		consolidationDate := 0
		if div.PivotIndex >= 0 && div.PivotIndex < len(pivots) {
			zone := pivots[div.PivotIndex]
			for _, pivot := range pivots {
				if pivot.Index == zone.Index || !pivot.Confirmed || pivot.StartIndex <= div.LegBEnd {
					continue
				}
				if pivot.ZD <= zone.ZG && pivot.ZG >= zone.ZD {
					consolidationDate = pivot.ConfirmDate
					break
				}
			}
		}

		type branch struct {
			key       string
			label     string
			condition string
			date      int
		}
		branches := []branch{
			{
				key: "reversal", label: "反转",
				condition: "形成二买/二卖确认，或反向走势破坏原趋势最后一个中枢",
				date:      reversalDate,
			},
			{
				key: "consolidation", label: "盘整",
				condition: "回到最后一个中枢，并形成与原中枢重叠的新中枢",
				date:      consolidationDate,
			},
			{
				key: "failure", label: "背驰失败",
				condition: "继续原趋势并创出新低/新高（超过失效容差）",
				date:      failureDate,
			},
		}

		earliestKey := ""
		earliestDate := 0
		for _, item := range branches {
			if item.date > 0 && (earliestDate == 0 || item.date < earliestDate) {
				earliestKey, earliestDate = item.key, item.date
			}
		}

		div.Outcomes = make([]DivergenceOutcome, 0, len(branches))
		for _, item := range branches {
			status := ScenarioPending
			switch {
			case item.key == earliestKey:
				status = ScenarioConfirmed
			case item.date > 0:
				// 这条分支也发生过，但不是最早的那条 → 该背驰已经走了另一条路
				status = ScenarioInvalidated
			}
			div.Outcomes = append(div.Outcomes, DivergenceOutcome{
				Key:       item.key,
				Label:     item.label,
				Condition: item.condition,
				Status:    status,
				StatusCN:  ScenarioStatusCN(status),
				Date:      item.date,
			})
		}
		if earliestKey == "" {
			div.Outcome = "pending"
			div.OutcomeCN = "待定（三条路都还没走出来）"
		} else {
			div.Outcome = earliestKey
			for _, item := range branches {
				if item.key == earliestKey {
					div.OutcomeCN = item.label
				}
			}
		}
	}
	return divs
}

// departureStrokeLeg 取"离开中枢的那一笔"（同向、且终点走出重叠区）。
//
// 为什么是一笔而不是"到下一个中枢的整段"：相邻中枢常常首尾相接，
// 到下一中枢的跨度可能是 1 笔也可能是几十笔；一边取单笔、另一边取整段时，
// MACD 面积会差几十倍，力度比恒大于 1，背驰永远出不来。
// 两段都取"离开各自中枢的同向一笔"，长度可比、口径一致。
//
// 从 `LeaveStroke` 开始找：它是中枢之后第一个不再与重叠区相交的笔；
// 若那一笔是回抽（方向相反），继续往后找同向笔。
func departureStrokeLeg(pivot Pivot, strokes []Stroke, bars []Bar, direction int) (legRef, bool) {
	if len(strokes) == 0 {
		return legRef{}, false
	}
	startK := pivot.LeaveStroke
	if startK < 0 || startK <= pivot.EndStroke {
		startK = pivot.EndStroke + 1
	}
	if startK < 0 {
		startK = 0
	}
	for k := startK; k < len(strokes); k++ {
		stroke := strokes[k]
		if stroke.Direction != direction {
			continue
		}
		exited := (direction > 0 && stroke.EndPrice > pivot.ZG) ||
			(direction < 0 && stroke.EndPrice < pivot.ZD)
		if !exited {
			continue
		}
		if stroke.StartIndex < 0 || stroke.EndIndex >= len(bars) || stroke.EndIndex <= stroke.StartIndex {
			return legRef{}, false
		}
		return legSpan(bars, stroke.StartIndex, stroke.EndIndex), true
	}
	return legRef{}, false
}

// legBeforePivot 取中枢形成之前、给定方向上的最近一笔（盘整背驰的"进入段"）。
//
// 用笔而不是"往前扫 120 根找极值"：笔的端点本身就是结构点，
// 扫出来的极值可能是中枢内部的一次回抽，比较对象就错了。
func legBeforePivot(pivot Pivot, strokes []Stroke, bars []Bar, direction int) (legRef, bool) {
	for k := pivot.StartStroke - 1; k >= 0 && k < len(strokes); k-- {
		stroke := strokes[k]
		if stroke.Direction != direction {
			continue
		}
		if stroke.StartIndex < 0 || stroke.EndIndex >= len(bars) || stroke.EndIndex <= stroke.StartIndex {
			return legRef{}, false
		}
		return legSpan(bars, stroke.StartIndex, stroke.EndIndex), true
	}
	return legRef{}, false
}

// legSpan 汇总一段原始下标区间的起止价与高低点。
func legSpan(bars []Bar, start, end int) legRef {
	if start > end {
		start, end = end, start
	}
	leg := legRef{startIndex: start, endIndex: end}
	if start < 0 {
		start = 0
	}
	if end >= len(bars) {
		end = len(bars) - 1
	}
	leg.startPrice = bars[start].Close
	leg.endPrice = bars[end].Close
	leg.high = bars[start].High
	leg.low = bars[start].Low
	for i := start; i <= end; i++ {
		if bars[i].High > leg.high {
			leg.high = bars[i].High
		}
		if bars[i].Low < leg.low {
			leg.low = bars[i].Low
		}
	}
	return leg
}

// compareLegs 比较两段同向走势的力度并给出背驰判定。
//
// 四个维度的比值都在 1 附近：>1 表示后段更强，<1 表示更弱。
// 加权平均后与阈值比较，比值小于阈值才判背驰。
func compareLegs(
	bars []Bar,
	macd []macdPoint,
	legA, legB legRef,
	direction int,
	kind string,
	pivotIndex int,
	strokes []Stroke,
	cfg DivergenceConfig,
) *Divergence {
	statsA := measureLeg(bars, macd, legA.startIndex, legA.endIndex)
	statsB := measureLeg(bars, macd, legB.startIndex, legB.endIndex)
	if statsA.bars < cfg.MinLegBars || statsB.bars < cfg.MinLegBars {
		return nil
	}

	priceA := absFloat(legA.endPrice - legA.startPrice)
	priceB := absFloat(legB.endPrice - legB.startPrice)
	if priceA <= 0 {
		return nil
	}

	slopeA := priceA / float64(statsA.bars)
	slopeB := priceB / float64(statsB.bars)

	// 结构维度：幅度比；后段被前段包含时再打 8 折（结构更弱）
	structureRatio := priceB / priceA
	contained := (direction > 0 && legB.high <= legA.high) || (direction < 0 && legB.low >= legA.low)
	structureNote := "幅度比"
	if contained {
		structureRatio *= 0.8
		structureNote = "幅度比（后段被前段包含，已折算）"
	}

	dims := []DimScore{
		{
			Name: "结构", A: round4(priceA), B: round4(priceB),
			Ratio: round4(structureRatio), Weight: cfg.StructureWeight, Note: structureNote,
		},
		{
			// MACD 用**单位 K 线**的柱面积：两段长度不同时，直接比总面积会系统性
			// 把更长的那一段判成"更强"，背驰就永远出不来
			Name: "MACD", A: round4(statsA.macdArea), B: round4(statsB.macdArea),
			Ratio: round4(ratioOf(
				statsB.macdArea/float64(maxInt(statsB.bars, 1)),
				statsA.macdArea/float64(maxInt(statsA.bars, 1)),
			)),
			Weight: cfg.MacdWeight, Note: "离开段 MACD 柱面积（按单位 K 线折算）",
		},
		{
			Name: "斜率", A: round4(slopeA), B: round4(slopeB),
			Ratio: round4(ratioOf(slopeB, slopeA)), Weight: cfg.SlopeWeight,
			Note: "单位 K 线价格变化",
		},
		{
			Name: "量能", A: round4(statsA.avgVolume), B: round4(statsB.avgVolume),
			Ratio: round4(ratioOf(statsB.avgVolume, statsA.avgVolume)), Weight: cfg.VolumeWeight,
			Note: "平均成交量（仅辅助）",
		},
	}

	totalWeight := 0.0
	weighted := 0.0
	for _, dim := range dims {
		if dim.Weight <= 0 {
			continue
		}
		totalWeight += dim.Weight
		weighted += dim.Weight * dim.Ratio
	}
	if totalWeight <= 0 {
		return nil
	}
	ratio := weighted / totalWeight

	// 创新高/新低：趋势背驰的必要条件
	newExtreme := (direction > 0 && legB.high > legA.high) ||
		(direction < 0 && legB.low < legA.low)

	kindCN := "趋势背驰"
	if kind == "consolidation" {
		kindCN = "盘整背驰"
	}
	directionName := "top"
	if direction < 0 {
		directionName = "bottom"
	}

	div := &Divergence{
		Date:          bars[legB.endIndex].Date,
		Kind:          kind,
		KindCN:        kindCN,
		Direction:     directionName,
		PivotIndex:    pivotIndex,
		LegAStart:     legA.startIndex,
		LegAEnd:       legA.endIndex,
		LegBStart:     legB.startIndex,
		LegBEnd:       legB.endIndex,
		LegAStartDate: bars[legA.startIndex].Date,
		LegAEndDate:   bars[legA.endIndex].Date,
		LegBStartDate: bars[legB.startIndex].Date,
		LegBEndDate:   bars[legB.endIndex].Date,
		StrengthA:     1,
		StrengthB:     round4(ratio),
		Ratio:         round4(ratio),
		Dims:          dims,
		NewExtreme:    newExtreme,
		Confirmed:     extremeConfirmed(strokes, legB.endIndex),
	}
	if kind == "trend" {
		div.Note = "趋势背驰：只保证至少回到最后一个中枢，不保证反转（后续可能反转 / 盘整 / 背驰失败）"
	} else {
		div.Note = "盘整背驰：对应中枢震荡里的买卖机会，力度弱于趋势背驰"
	}
	return div
}

// extremeConfirmed 判断一段走势的极值是否已被后续走势确认。
//
// 判据：存在一条已确认的笔以该极值点为端点（说明那里的分型已经确认）。
// 延伸中的最后一笔不算确认 —— 这时只能给"候选"。
func extremeConfirmed(strokes []Stroke, extremeIndex int) bool {
	for _, stroke := range strokes {
		if !stroke.Confirmed {
			continue
		}
		if stroke.StartIndex == extremeIndex || stroke.EndIndex == extremeIndex {
			return true
		}
	}
	return false
}

func ratioOf(b, a float64) float64 {
	if a == 0 {
		if b == 0 {
			return 1
		}
		return 2
	}
	return b / a
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
