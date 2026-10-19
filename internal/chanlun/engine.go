package chanlun

import "strings"

// Analyze 对**一个级别**做完整分析，返回可以直接给界面消费的标注数据。
//
// 调用约定：
//   - `bars` 必须是该级别**自己的** K 线序列（日线 / 周线 / 月线 / 年线），
//     不允许从大级别推出小级别的笔与中枢（方案 §3.3）；
//   - `info` 是服务层能提供、引擎算不出来的信息（流通市值、级别数量等）；
//   - 结果是纯函数输出，不含任何缓存与 IO。
//
// 结构计算的顺序就是"确认顺序"，也是无未来数据的保证：
// 包含处理 → 分型（等第三根）→ 笔（等分型确认）→ 线段（等反向破坏）
// → 中枢（等第三笔）→ 走势类型（等中枢）→ 背驰（等离开段）→ 买卖点状态机。
func Analyze(bars []Bar, level Level, cfg Config, info AdaptInfo) *Analysis {
	cfg = cfg.Sanitize()
	if !ValidLevel(level) {
		level = cfg.OperLevel
	}
	// 只保留有效的 K 线并按日期升序，避免上游给的顺序问题影响结构
	bars = normalizeBars(bars)
	if cfg.LookbackDays > 0 && len(bars) > cfg.LookbackDays {
		bars = bars[len(bars)-cfg.LookbackDays:]
	}

	analysis := &Analysis{
		Level:      level,
		LevelCN:    LevelCN(level),
		Disclaimer: Disclaimer,
		Notes:      []string{},
	}
	if len(bars) == 0 {
		analysis.State = StateInfo{
			Name:   StateInsufficient,
			NameCN: "数据不足",
			Note:   "没有可用的 K 线数据",
		}
		return analysis
	}

	merged := mergeBars(bars)
	analysis.Data = buildDataInfo(bars, merged, info, level)
	analysis.Merged = merged

	if len(bars) < cfg.MinBars {
		analysis.State = StateInfo{
			Name:   StateInsufficient,
			NameCN: "数据不足",
			Note:   "K 线不足 " + itoa(cfg.MinBars) + " 根，无法形成可靠的分型与笔",
		}
		analysis.Notes = append(analysis.Notes,
			"数据不足：至少需要 "+itoa(cfg.MinBars)+" 根 K 线，当前只有 "+itoa(len(bars))+" 根")
		analysis.Adaptability = assessAdaptability(bars, nil, info)
		return analysis
	}

	macd := computeMACD(bars, 12, 26, 9)
	fractals := detectFractals(merged, level)
	strokes := buildStrokes(fractals, bars, level)
	segments := buildSegments(strokes, level)

	// 日线笔级别：只输出分型与笔。
	// 方案 §3.2 明确"最小级别定义为日线笔，操作级别为日线线段/中枢/走势类型" ——
	// 所以在笔级别上不给中枢、走势与买卖点，避免把最小级别当成操作级别误用。
	var pivots []Pivot
	var trend Trend
	var position Position
	var divergences []Divergence
	var signals []Signal

	if level == LevelDayStroke {
		trend = Trend{
			Type: TrendNone, TypeCN: trendTypeCN(TrendNone), Level: level,
			Status: TrendForming, StatusCN: TrendStatusCN(TrendForming),
		}
		position = Position{Phase: PhaseBefore, PhaseCN: "中枢前", PivotIndex: -1,
			Note: "日线笔是最小级别，只输出分型与笔"}
	} else {
		pivots = buildPivots(strokes, bars, level)
		trend = classifyTrend(pivots, strokes, bars, level)
		position = detectPosition(pivots, strokes, bars)
		divergences = detectDivergences(bars, strokes, pivots, trend, macd, cfg)
		signals = buildSignals(bars, strokes, pivots, trend, divergences, cfg)
		signals = scoreSignals(signals, trend, position, divergences, cfg, len(bars)-1)
		if !cfg.ShowCandidate {
			signals = filterCandidates(signals)
		}
		// 背驰的三种可能（反转 / 盘整 / 失败）要用**已确认的信号**来判定，
		// 所以放在信号生成之后：这样"反转"只在二买/二卖或原中枢被破坏后才成立。
		divergences = resolveDivergenceOutcomes(divergences, signals, pivots, bars, cfg)
	}

	analysis.Fractals = fractals
	analysis.Strokes = strokes
	analysis.Segments = segments
	analysis.Pivots = pivots
	analysis.Trend = trend
	analysis.Position = position
	analysis.Divergences = divergences
	// 状态判定用**限制之前**的完整信号集：确认过的一买/一卖可能因为留痕条数被裁掉，
	// 但"当前处于反转确认向上/向下"这个判断不能因此改变。
	analysis.State = buildState(trend, pivots, divergences, signals, level)
	analysis.Signals = limitSignals(signals, cfg.MaxSignalsPerType)
	analysis.Adaptability = assessAdaptability(bars, strokes, info)
	analysis.Notes = append(analysis.Notes, buildNotes(analysis, cfg, len(bars))...)
	return analysis
}

// buildState 先判状态，再交给信号（方案 §7.3）。
//
// 状态与可关注信号的映射严格按方案表：
//
//	下跌趋势，无背驰              → 无买点
//	下跌趋势，趋势背驰，底分型确认 → 一买候选/触发
//	一买后，次级别回抽不破前低    → 二买
//	盘整突破，回抽不回中枢        → 三买
//	（上涨/顶背驰/二卖/三卖对称）
func buildState(trend Trend, pivots []Pivot, divs []Divergence, signals []Signal, level Level) StateInfo {
	state := StateInfo{Name: StateNoPivot, NameCN: "无中枢（走势未完成）", SinceDate: trend.StartDate}
	if level == LevelDayStroke {
		state.Name = StateInsufficient
		state.NameCN = "笔级别"
		state.Note = "最小级别只输出分型与笔；买卖点需在日线及以上级别判断"
		return state
	}
	if len(pivots) == 0 {
		state.Note = "尚无中枢，走势未完成：不输出趋势背驰与三类买卖点"
		return state
	}

	hasTopDivergence := hasDivergence(divs, "top")
	hasBottomDivergence := hasDivergence(divs, "bottom")
	topDivergence := latestDivergence(divs, "top")
	bottomDivergence := latestDivergence(divs, "bottom")
	sell1Confirmed := hasSignalState(signals, SignalSell1, StateConfirmed)
	buy1Confirmed := hasSignalState(signals, SignalBuy1, StateConfirmed)

	switch trend.Type {
	case TrendUp:
		switch {
		case sell1Confirmed:
			state = StateInfo{
				Name: StateReverseDown, NameCN: "反转确认向下",
				Note:         "顶背驰后一卖已确认（二卖成立或原中枢被反向破坏），上涨趋势可能结束",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"sell2", "sell3"},
			}
		case topDivergence != nil && topDivergence.Outcome == "failure":
			// 背驰已经失败：它是"力度衰竭"的误判，原趋势继续，不能当卖点用
			state = StateInfo{
				Name: StateUpTrend, NameCN: "上涨趋势（顶背驰失败）",
				Note:         "顶背驰已被否定：价格继续创新高，原上涨趋势延续；背驰不构成卖点，三类卖点可独立出现",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"sell3"},
			}
		case topDivergence != nil && topDivergence.Outcome == "consolidation":
			state = StateInfo{
				Name: StateConsolidation, NameCN: "盘整（顶背驰后）",
				Note:         "顶背驰后进入盘整：背驰只保证回到最后一个中枢，方向要看中枢震荡结果",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"sell3", "buy3"},
			}
		case hasTopDivergence:
			state = StateInfo{
				Name: StateReversalSell, NameCN: "上涨趋势背驰（反转候选卖）",
				Note:         "上涨趋势出现顶背驰：只关注一卖候选/触发，背驰不保证反转；确认要看二卖或原趋势最后一个中枢被破坏",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"sell1"},
			}
		default:
			state = StateInfo{
				Name: StateUpTrend, NameCN: "上涨趋势",
				Note:         "上涨趋势无背驰：无卖点；三类卖点可以独立出现",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"sell3", "sell2"},
			}
		}
	case TrendDown:
		switch {
		case buy1Confirmed:
			state = StateInfo{
				Name: StateReverseUp, NameCN: "反转确认向上",
				Note:         "底背驰后一买已确认（二买成立或原中枢被反向破坏），下跌趋势可能结束",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"buy2", "buy3"},
			}
		case bottomDivergence != nil && bottomDivergence.Outcome == "failure":
			// 背驰失败：原趋势延续，此时再把底背驰当买点就是典型的误读
			state = StateInfo{
				Name: StateDownTrend, NameCN: "下跌趋势（底背驰失败）",
				Note:         "底背驰已被否定：价格继续创新低，原下跌趋势延续；背驰不构成买点，三类买点可独立出现",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"buy3"},
			}
		case bottomDivergence != nil && bottomDivergence.Outcome == "consolidation":
			state = StateInfo{
				Name: StateConsolidation, NameCN: "盘整（底背驰后）",
				Note:         "底背驰后进入盘整：背驰只保证回到最后一个中枢，方向要看中枢震荡结果",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"buy3", "sell3"},
			}
		case hasBottomDivergence:
			state = StateInfo{
				Name: StateReversalBuy, NameCN: "下跌趋势背驰（反转候选买）",
				Note:         "下跌趋势出现底背驰：只关注一买候选/触发，背驰不保证反转；确认要看二买或原趋势最后一个中枢被破坏",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"buy1"},
			}
		default:
			state = StateInfo{
				Name: StateDownTrend, NameCN: "下跌趋势",
				Note:         "下跌趋势无背驰：无买点；三类买点可以独立出现",
				SinceDate:    trend.StartDate,
				WatchSignals: []string{"buy3"},
			}
		}
	default:
		state = StateInfo{
			Name: StateConsolidation, NameCN: "盘整",
			Note:         "盘整：只有突破中枢后回抽不回中枢（三买/三卖）或盘整背驰可参考",
			SinceDate:    trend.StartDate,
			WatchSignals: []string{"buy3", "sell3"},
		}
	}

	// 附上当前仍然有效的信号类型（不改变主状态，只补充"可关注"）
	for _, sig := range signals {
		if sig.State == StateInvalid || sig.State == StateExpired {
			continue
		}
		state.WatchSignals = appendUnique(state.WatchSignals, string(sig.Type))
	}
	return state
}

// filterCandidates 去掉候选信号（配置要求只看已触发的）。
func filterCandidates(signals []Signal) []Signal {
	out := make([]Signal, 0, len(signals))
	for _, sig := range signals {
		if sig.State == StateCandidate {
			continue
		}
		out = append(out, sig)
	}
	return out
}

// buildNotes 生成界面要常驻展示的口径与限制提示。
func buildNotes(analysis *Analysis, cfg Config, barCount int) []string {
	notes := make([]string, 0, 8)

	notes = append(notes, analysis.Data.PrecisionNote)
	notes = append(notes, "包含处理："+itoa(barCount)+" 根原始 K 线合并为 "+
		itoa(len(analysis.Merged))+" 根；分型/笔只使用确认之后的结构")

	if len(analysis.Pivots) == 0 {
		notes = append(notes, "当前级别尚未形成中枢：按定义不输出趋势背驰与三类买卖点")
	} else {
		notes = append(notes, "当前级别中枢 "+itoa(len(analysis.Pivots))+" 个，笔 "+
			itoa(len(analysis.Strokes))+" 条，线段 "+itoa(len(analysis.Segments))+" 条")
	}

	// 走势终完美：把"走到哪一步"说清楚，而不是只给一个静态类型
	if analysis.Trend.Type != TrendNone {
		lifecycle := "走势：" + analysis.Trend.TypeCN + " · " + analysis.Trend.StatusCN
		if analysis.Trend.Status == TrendCompleted {
			lifecycle += "（" + analysis.Trend.CompletionReason + "）"
		}
		notes = append(notes, lifecycle)
		if len(analysis.Trend.Outlook) > 0 {
			parts := make([]string, 0, len(analysis.Trend.Outlook))
			for _, scenario := range analysis.Trend.Outlook {
				parts = append(parts, scenario.Label+"["+scenario.StatusCN+"]")
			}
			notes = append(notes, "完全分类：后续只有这几种可能 —— "+strings.Join(parts, " / "))
		}
	}

	hasConflict := false
	for _, sig := range analysis.Signals {
		if sig.Conflict {
			hasConflict = true
			break
		}
	}
	if hasConflict {
		notes = append(notes, "同级别存在反向信号，已标记「结构矛盾」并降权，不作为高优先级参考")
	}

	for _, div := range analysis.Divergences {
		directionCN := "顶背驰"
		if div.Direction == "bottom" {
			directionCN = "底背驰"
		}
		notes = append(notes, div.KindCN+"（"+directionCN+"，力度比 "+
			formatRatio(div.Ratio)+"）：只保证至少回到最后一个中枢，不保证反转；当前判定为「"+
			div.OutcomeCN+"」")
	}

	// 常驻提示：这是缠论里最容易被误读的两条，放在口径里而不是只写在文档里
	if len(analysis.Divergences) > 0 {
		notes = append(notes,
			"背驰只代表原趋势力度衰竭，不等于反转：背驰后可能反转、可能盘整、也可能背驰失败继续原趋势")
	}
	notes = append(notes,
		"一买/一卖只是候选/触发：确认需要二买/二卖成立，或原趋势最后一个中枢被反向破坏；"+
			"背驰本身不构成买卖指令")

	if !cfg.VolumeAux {
		notes = append(notes, "量价辅助已关闭：背驰力度只由结构、MACD 与斜率决定")
	}
	return notes
}

func hasDivergence(divs []Divergence, direction string) bool {
	for _, div := range divs {
		if div.Direction == direction {
			return true
		}
	}
	return false
}

// latestDivergence 返回某一方向最近的一次背驰（含它的三种可能判定）。
func latestDivergence(divs []Divergence, direction string) *Divergence {
	for i := len(divs) - 1; i >= 0; i-- {
		if divs[i].Direction == direction {
			return &divs[i]
		}
	}
	return nil
}

func hasSignalState(signals []Signal, t SignalType, state SignalState) bool {
	for _, sig := range signals {
		if sig.Type == t && sig.State == state {
			return true
		}
	}
	return false
}

func appendUnique(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

// normalizeBars 过滤脏数据并按日期升序排列。
func normalizeBars(bars []Bar) []Bar {
	out := make([]Bar, 0, len(bars))
	for _, bar := range bars {
		if bar.Date <= 0 || bar.High <= 0 || bar.Low <= 0 || bar.Close <= 0 {
			continue
		}
		out = append(out, bar)
	}
	// 上游一般已经是升序；这里做一次插入排序以保证确定性（数据量 <= 数千）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Date < out[j-1].Date; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
