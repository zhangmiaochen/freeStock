package chanlun

// 买卖点与状态机（方案 §7）。
//
// 每个信号都按"形成 → 触发 → 确认 → 失效 / 过期"五段式建模，
// 并且**只使用当时已确认的数据**：
//
//   - 形成：背驰成立 / 回抽到位的当下；
//   - 触发：对应的分型被后续 K 线确认（`ConfirmDate`）；
//   - 确认：反向笔破坏了关键位置（前高 / 前低 / 中枢边沿）；
//   - 失效：价格反向越过保护位（带容差）；
//   - 过期：触发后 N 根内没有确认。
//
// 一买/一卖来自**趋势背驰**；二买/二卖来自一买后的次级别回抽不破前低/前高；
// 三买/三卖来自突破中枢后回抽不回中枢，可以独立出现。

type signalCtx struct {
	bars    []Bar
	strokes []Stroke
	pivots  []Pivot
	trend   Trend
	cfg     Config
}

// buildSignals 生成全部买卖点信号。
func buildSignals(
	bars []Bar,
	strokes []Stroke,
	pivots []Pivot,
	trend Trend,
	divs []Divergence,
	cfg Config,
) []Signal {
	ctx := &signalCtx{bars: bars, strokes: strokes, pivots: pivots, trend: trend, cfg: cfg}

	first := make([]Signal, 0, len(divs))
	for _, div := range divs {
		// 一买/一卖只来自趋势背驰；盘整背驰对应中枢震荡，不产生一类买卖点
		if div.Kind != "trend" {
			continue
		}
		if sig := ctx.fromTrendDivergence(div); sig != nil {
			first = append(first, *sig)
		}
	}

	// 二买/二卖：以一买/一卖为锚。
	//
	// ⚠ 一买/一卖的**确认**从这里来：缠论对一买的定性是"高赔率、低胜率、确认难"，
	// 背驰后的反转要用二买（次级别回抽不破前低 + 分型确认）来确认，
	// 而不是单笔突破前高就宣布"反转确认"。二买确认后回填到它的一买上。
	seconds := make([]Signal, 0, len(first))
	for i := range first {
		second := ctx.secondFrom(first[i])
		if second == nil {
			continue
		}
		seconds = append(seconds, *second)
		if second.ConfirmIndex >= 0 &&
			(first[i].ConfirmIndex < 0 || second.ConfirmIndex < first[i].ConfirmIndex) {
			first[i].ConfirmIndex = second.ConfirmIndex
			first[i].Reasons = append(first[i].Reasons,
				"二买/二卖确认：次级别回抽不破前低/前高后反向突破，反转得到确认")
		}
	}

	signals := make([]Signal, 0, len(first)+len(seconds)+len(pivots)*2)
	signals = append(signals, first...)
	signals = append(signals, seconds...)

	// 三买/三卖：独立于一类买卖点
	for _, pivot := range pivots {
		if pivot.ZG <= pivot.ZD {
			continue
		}
		if sig := ctx.thirdFrom(pivot, true); sig != nil {
			signals = append(signals, *sig)
		}
		if sig := ctx.thirdFrom(pivot, false); sig != nil {
			signals = append(signals, *sig)
		}
	}

	for i := range signals {
		finalizeSignal(&signals[i], bars)
	}
	// 同一个触发点可能被多个重叠中枢各算出一遍（三买/三卖尤其常见）：
	// 类型 + 形成 + 触发都相同就是同一个信号，去重后再交给评分与展示
	return dedupeSignals(signals)
}

// dedupeSignals 去掉"同类型 + 同形成点 + 同触发点"的重复信号。
func dedupeSignals(signals []Signal) []Signal {
	seen := make(map[string]struct{}, len(signals))
	out := make([]Signal, 0, len(signals))
	for _, sig := range signals {
		key := string(sig.Type) + "|" + itoa(sig.FormIndex) + "|" + itoa(sig.TriggerIndex)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, sig)
	}
	return out
}

// fromTrendDivergence 由趋势背驰生成一买 / 一卖。
func (c *signalCtx) fromTrendDivergence(div Divergence) *Signal {
	if div.LegBEnd < 0 || div.LegBEnd >= len(c.bars) {
		return nil
	}
	isBuy := div.Direction == "bottom"
	extreme := div.LegBEnd
	formIndex := extreme

	price := c.bars[extreme].Low
	if !isBuy {
		price = c.bars[extreme].High
	}

	triggerIndex := -1
	if div.Confirmed {
		triggerIndex = c.fractalConfirmIndex(extreme)
	}

	// 确认只有两条路径：
	//   1. 反向笔破坏原趋势的最后一个中枢，且回抽不回（三买/三卖结构）；
	//   2. 二买/二卖确认（在 buildSignals 里回填）。
	//
	// ⚠ **单笔突破前高不构成一买确认**：背驰只说明原趋势力度衰竭，不保证反转。
	// 用一笔突破就宣布"反转确认"，正是缠论实战最常见的误读。
	confirmIndex := -1
	if triggerIndex >= 0 {
		confirmIndex = c.pivotReclaimConfirm(div, isBuy, extreme)
	}

	invalidIndex := c.invalidateAfter(price, isBuy, triggerIndex, formIndex)
	expireIndex := c.expireAfter(triggerIndex, confirmIndex, invalidIndex)

	reasons := []string{
		div.KindCN + "（后段/前段力度 " + formatRatio(div.Ratio) + "）",
	}
	if isBuy {
		reasons = append(reasons, "下跌趋势背驰 + 底分型确认 → 一买（候选/触发，不是确认）")
	} else {
		reasons = append(reasons, "上涨趋势背驰 + 顶分型确认 → 一卖（候选/触发，不是确认）")
	}
	switch {
	case confirmIndex >= 0:
		reasons = append(reasons,
			"反向笔破坏原趋势最后一个中枢且回抽不回，反转得到结构确认")
	default:
		reasons = append(reasons,
			"确认还要等：二买/二卖成立，或原趋势最后一个中枢被反向破坏（背驰本身不保证反转）")
	}

	signalType := SignalBuy1
	if !isBuy {
		signalType = SignalSell1
	}
	return &Signal{
		Type:            signalType,
		Direction:       directionOfSignal(signalType),
		Level:           div.Level,
		Price:           round4(price),
		FormIndex:       formIndex,
		TriggerIndex:    triggerIndex,
		ConfirmIndex:    confirmIndex,
		InvalidIndex:    invalidIndex,
		ExpireIndex:     expireIndex,
		PivotIndex:      div.PivotIndex,
		DivergenceIndex: div.Index,
		Reasons:         reasons,
	}
}

// secondFrom 由一买/一卖生成二买/二卖。
func (c *signalCtx) secondFrom(base Signal) *Signal {
	isBuy := base.Direction == "buy"
	pullbackDirection := -1
	if !isBuy {
		pullbackDirection = 1
	}
	// 锚点是**一买/一卖的低点/高点本身**（不是它的确认时间）：
	// 回抽笔从该极值点就开始，用确认时间当锚点会把这笔自己排除掉。
	anchor := base.FormIndex
	if anchor < 0 {
		return nil
	}

	// 一买之后的第一笔反向回抽
	var pullback *Stroke
	for i := range c.strokes {
		stroke := c.strokes[i]
		if stroke.StartIndex <= anchor || stroke.Direction != pullbackDirection {
			continue
		}
		pullback = &c.strokes[i]
		break
	}
	if pullback == nil {
		return nil
	}

	// 回抽不破前低 / 不破前高，否则二买/二卖不成立
	if isBuy && pullback.Low <= base.Price {
		return nil
	}
	if !isBuy && pullback.High >= base.Price {
		return nil
	}

	formIndex := pullback.StartIndex
	triggerIndex := -1
	if pullback.Confirmed {
		triggerIndex = c.indexOfDate(pullback.ConfirmDate)
	}

	refPrice := pullback.StartPrice
	confirmIndex := -1
	if triggerIndex >= 0 {
		for _, stroke := range c.strokes {
			if stroke.StartIndex < pullback.EndIndex || stroke.Direction != pullbackDirection*-1 {
				continue
			}
			if isBuy && stroke.EndPrice > refPrice {
				confirmIndex = c.indexOfDate(stroke.ConfirmDate)
				break
			}
			if !isBuy && stroke.EndPrice < refPrice {
				confirmIndex = c.indexOfDate(stroke.ConfirmDate)
				break
			}
		}
	}

	// 二买/二卖的失效位是**一买/一卖的低点/高点**：破了它，整个反转结构就作废
	invalidIndex := c.invalidateAfter(base.Price, isBuy, triggerIndex, formIndex)
	expireIndex := c.expireAfter(triggerIndex, confirmIndex, invalidIndex)

	signalType := SignalBuy2
	if !isBuy {
		signalType = SignalSell2
	}
	reasons := []string{
		"一买/一卖之后的次级别回抽" + pullbackNoBreakText(isBuy),
		"回抽结束于 " + formatPrice(pullback.EndPrice),
	}
	if confirmIndex >= 0 {
		reasons = append(reasons, "反向笔突破回抽起点，二买/二卖确认")
	}
	return &Signal{
		Type:         signalType,
		Direction:    directionOfSignal(signalType),
		Level:        base.Level,
		Price:        round4(pullback.EndPrice),
		FormIndex:    formIndex,
		TriggerIndex: triggerIndex,
		ConfirmIndex: confirmIndex,
		InvalidIndex: invalidIndex,
		ExpireIndex:  expireIndex,
		PivotIndex:   base.PivotIndex,
		// 二买/二卖以一买/一卖为锚，继承它的背驰依据
		DivergenceIndex: base.DivergenceIndex,
		Reasons:         reasons,
		Note:            "二买/二卖确认度高于一买/一卖，但幅度通常更小",
	}
}

// thirdFrom 由中枢生成三买 / 三卖。
func (c *signalCtx) thirdFrom(pivot Pivot, isBuy bool) *Signal {
	if pivot.EndIndex < 0 || pivot.EndIndex >= len(c.bars) {
		return nil
	}
	pullbackDirection := -1
	if !isBuy {
		pullbackDirection = 1
	}

	var pullback *Stroke
	for i := range c.strokes {
		stroke := c.strokes[i]
		if stroke.StartIndex <= pivot.EndIndex || stroke.Direction != pullbackDirection {
			continue
		}
		// 回抽不回中枢：三买要求低点 > ZG；三卖要求高点 < ZD
		if isBuy && stroke.Low <= pivot.ZG {
			continue
		}
		if !isBuy && stroke.High >= pivot.ZD {
			continue
		}
		pullback = &c.strokes[i]
		break
	}
	if pullback == nil {
		return nil
	}

	formIndex := pullback.StartIndex
	triggerIndex := -1
	if pullback.Confirmed {
		triggerIndex = c.indexOfDate(pullback.ConfirmDate)
	}

	refPrice := pullback.StartPrice
	confirmIndex := -1
	if triggerIndex >= 0 {
		for _, stroke := range c.strokes {
			if stroke.StartIndex < pullback.EndIndex || stroke.Direction != pullbackDirection*-1 {
				continue
			}
			if isBuy && stroke.EndPrice > refPrice {
				confirmIndex = c.indexOfDate(stroke.ConfirmDate)
				break
			}
			if !isBuy && stroke.EndPrice < refPrice {
				confirmIndex = c.indexOfDate(stroke.ConfirmDate)
				break
			}
		}
	}

	// 失效位：跌回中枢（三买）/ 涨回中枢（三卖）
	guard := pivot.ZG
	if !isBuy {
		guard = pivot.ZD
	}
	invalidIndex := c.invalidateAfter(guard, isBuy, triggerIndex, formIndex)
	expireIndex := c.expireAfter(triggerIndex, confirmIndex, invalidIndex)

	signalType := SignalBuy3
	if !isBuy {
		signalType = SignalSell3
	}
	reasons := []string{
		"离开中枢后回抽" + pivotNoReturnText(isBuy),
		"中枢区间 " + formatPrice(pivot.ZD) + " ~ " + formatPrice(pivot.ZG),
	}
	if pivot.StrokeCount > 3 {
		reasons = append(reasons, "中枢延伸 "+itoa(pivot.StrokeCount)+" 笔")
	}
	return &Signal{
		Type:         signalType,
		Direction:    directionOfSignal(signalType),
		Level:        pivot.Level,
		Price:        round4(pullback.EndPrice),
		FormIndex:    formIndex,
		TriggerIndex: triggerIndex,
		ConfirmIndex: confirmIndex,
		InvalidIndex: invalidIndex,
		ExpireIndex:  expireIndex,
		PivotIndex:   pivot.Index,
		// 三买/三卖可以独立于背驰出现，显式给 -1：
		// 零值 0 会被误认为"关联第 0 个背驰"，综合分里的背驰强度就取错了
		DivergenceIndex: -1,
		Reasons:         reasons,
		Note:            "三买/三卖是趋势延续信号：胜率较高，但可能追高/追空",
	}
}

// pivotReclaimConfirm 判断"原趋势的最后一个中枢是否被反向破坏"。
//
// 这是缠论对一买/一卖提到的那条确认路径：反向走势**破坏原趋势的最后一个中枢**。
// 判据用三买/三卖的结构：
//
//	反向笔越过中枢边沿（做多：向上笔终点 > ZG）→ 随后的回抽笔不回中枢
//	（做多：向下笔的低点仍 > ZG）→ 结构确认。
//
// 第一次回抽就回到中枢里，说明只是中枢震荡，破坏不成立，继续往后找下一次突破。
// 全部只使用已确认的笔，绝不提前使用还没走完的走势。
func (c *signalCtx) pivotReclaimConfirm(div Divergence, isBuy bool, extreme int) int {
	if div.PivotIndex < 0 || div.PivotIndex >= len(c.pivots) {
		return -1
	}
	pivot := c.pivots[div.PivotIndex]
	if pivot.ZG <= pivot.ZD {
		return -1
	}

	direction := 1
	if !isBuy {
		direction = -1
	}
	breakIndex := -1
	for i := range c.strokes {
		stroke := c.strokes[i]
		if !stroke.Confirmed || stroke.StartIndex < extreme {
			continue
		}
		if stroke.Direction == direction {
			broke := (isBuy && stroke.EndPrice > pivot.ZG) ||
				(!isBuy && stroke.EndPrice < pivot.ZD)
			if broke {
				breakIndex = i
			}
			continue
		}
		if breakIndex < 0 {
			continue
		}
		// 回抽笔：不回到中枢即确认；回到中枢则破坏不成立，继续找下一次突破
		hold := (isBuy && stroke.Low > pivot.ZG) ||
			(!isBuy && stroke.High < pivot.ZD)
		if hold {
			return c.indexOfDate(stroke.ConfirmDate)
		}
		breakIndex = -1
	}
	return -1
}

// firstStrokeAfter 找出起点在指定原始下标之后、给定方向的第一笔。
func (c *signalCtx) firstStrokeAfter(barIndex, direction int) (Stroke, bool) {
	for _, stroke := range c.strokes {
		if stroke.StartIndex >= barIndex && stroke.Direction == direction {
			return stroke, true
		}
	}
	return Stroke{}, false
}

// fractalConfirmIndex 返回极值点处分型被确认的 K 线下标；未确认返回 -1。
func (c *signalCtx) fractalConfirmIndex(barIndex int) int {
	best := -1
	for _, stroke := range c.strokes {
		if !stroke.Confirmed {
			continue
		}
		if stroke.StartIndex != barIndex && stroke.EndIndex != barIndex {
			continue
		}
		if stroke.ConfirmDate <= 0 {
			continue
		}
		index := c.indexOfDate(stroke.ConfirmDate)
		if index < 0 {
			continue
		}
		if best < 0 || index < best {
			best = index
		}
	}
	return best
}

// invalidateAfter 返回失效下标：价格反向越过保护位的第一根 K 线。
func (c *signalCtx) invalidateAfter(guard float64, isBuy bool, triggerIndex, formIndex int) int {
	if guard <= 0 {
		return -1
	}
	start := triggerIndex
	if start < 0 {
		start = formIndex
	}
	if start < 0 {
		return -1
	}
	tolerance := c.cfg.InvalidTolerance
	level := guard * (1 - tolerance)
	if !isBuy {
		level = guard * (1 + tolerance)
	}
	for i := start + 1; i < len(c.bars); i++ {
		bar := c.bars[i]
		if bar.High <= 0 || bar.Low <= 0 {
			continue
		}
		if isBuy && bar.Low < level {
			return i
		}
		if !isBuy && bar.High > level {
			return i
		}
	}
	return -1
}

// expireAfter 返回过期下标：触发后 N 根内没有确认。
//
// ⚠ **确认晚于过期窗口也算过期**：一个买卖点在窗口内没等到确认，就已经失效于"过期"，
// 之后再出现的确认不能把它救回来 —— 否则回测与界面上会出现"三个月前触发、今天才确认"的
// 信号，那是用未来的走势去美化历史信号。
func (c *signalCtx) expireAfter(triggerIndex, confirmIndex, invalidIndex int) int {
	if triggerIndex < 0 {
		return -1
	}
	expire := triggerIndex + c.cfg.ExpiryBars
	if expire >= len(c.bars) {
		// 还没走到过期时间
		return -1
	}
	if confirmIndex >= 0 && confirmIndex <= expire {
		return -1 // 窗口内已确认
	}
	if invalidIndex >= 0 && invalidIndex <= expire {
		return -1 // 窗口内先失效了
	}
	return expire
}

// indexOfDate 返回该日期对应的 K 线下标；找不到返回 -1。
func (c *signalCtx) indexOfDate(date int) int {
	if date <= 0 {
		return -1
	}
	for i, bar := range c.bars {
		if bar.Date == date {
			return i
		}
	}
	return -1
}

// finalizeSignal 依据各阶段下标计算当前状态与全部时间字段。
func finalizeSignal(sig *Signal, bars []Bar) {
	sig.TypeCN = SignalTypeCN(sig.Type)
	sig.Direction = directionOfSignal(sig.Type)

	set := func(index int) (int, bool) {
		if index < 0 || index >= len(bars) {
			return 0, false
		}
		return bars[index].Date, true
	}
	sig.FormDate, _ = set(sig.FormIndex)
	sig.TriggerDate, _ = set(sig.TriggerIndex)
	sig.ConfirmDate, _ = set(sig.ConfirmIndex)
	sig.InvalidDate, _ = set(sig.InvalidIndex)
	sig.ExpireDate, _ = set(sig.ExpireIndex)

	// 状态判定：失效 / 过期都是"终止事件"，一旦发生，之后的事件不能把它救回来。
	//
	//  1. 取**最早**的终止事件（失效或过期）；
	//  2. 终止事件早于确认 → 就是失效/过期（晚到的确认不算数）；
	//  3. 确认之后才失效 → 仍然是失效（确认的买点跌破了保护位）；
	//  4. 确认在终止事件之前、之后也没有失效 → 确认；
	//  5. 其余按触发 / 候选。
	terminalIndex := -1
	terminalState := StateInvalid
	if sig.InvalidIndex >= 0 {
		terminalIndex = sig.InvalidIndex
		terminalState = StateInvalid
	}
	if sig.ExpireIndex >= 0 && (terminalIndex < 0 || sig.ExpireIndex < terminalIndex) {
		terminalIndex = sig.ExpireIndex
		terminalState = StateExpired
	}

	// 终止事件发生在确认之前 → 那次确认**不算**，时间字段也不该留着：
	// 否则会出现"2021 年触发、2024 年确认"的失效信号，读起来像真的确认过。
	if terminalIndex >= 0 && sig.ConfirmIndex >= 0 && terminalIndex < sig.ConfirmIndex {
		sig.ConfirmIndex = -1
		sig.ConfirmDate = 0
	}

	switch {
	case terminalIndex >= 0 && (sig.ConfirmIndex < 0 || terminalIndex < sig.ConfirmIndex):
		sig.State = terminalState
	case sig.ConfirmIndex >= 0 && sig.InvalidIndex > sig.ConfirmIndex:
		sig.State = StateInvalid
	case sig.ConfirmIndex >= 0:
		sig.State = StateConfirmed
	case sig.TriggerIndex >= 0:
		sig.State = StateTriggered
	case terminalIndex >= 0:
		sig.State = terminalState
	default:
		sig.State = StateCandidate
	}
	sig.StateCN = StateCN(sig.State)
}

func directionOfSignal(t SignalType) string {
	return SignalDirection(t)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
