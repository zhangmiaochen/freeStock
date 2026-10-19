package chanlun

import (
	"math"
	"sort"
)

// 优先级与综合评分（方案 §8）。
//
// 综合分 = 100 × (级别权重 × 类型权重 × 确认强度 × 背驰强度 × 位置权重 × 环境权重)^(1/6)
//
// 方案给的是六个因子的**乘积**。六个 [0,1] 的因子直接相乘会落到 0.1 量级，
// 不适合作为 0~100 的展示分；取几何平均既保持乘性排序（任一因子变强，分数一定变高），
// 又稳定落在 0~100。分项因子全部输出，界面上不展示无法解释的黑盒数字。

// scoreSignals 计算综合分、标出冲突并按方案 §8.1 排序。
func scoreSignals(
	signals []Signal,
	trend Trend,
	position Position,
	divs []Divergence,
	cfg Config,
	lastIndex int,
) []Signal {
	divByIndex := make(map[int]Divergence, len(divs))
	for _, div := range divs {
		divByIndex[div.Index] = div
	}

	for i := range signals {
		signals[i].Score, signals[i].ScoreFactors = scoreSignal(&signals[i], trend, position, divByIndex, cfg)
	}

	markConflicts(signals, lastIndex, cfg)
	// 冲突信号按方案 §8.3 降权：标记结构矛盾，不输出高优先级
	for i := range signals {
		if signals[i].Conflict {
			signals[i].Score = round1(signals[i].Score * 0.5)
			signals[i].ScoreFactors = append(signals[i].ScoreFactors, ScoreFactor{
				Name: "结构矛盾", Value: 0.5, Note: "同级别出现反向信号，综合分减半、排序下沉",
			})
		}
	}

	sortSignals(signals, trend)
	return signals
}

// scoreSignal 计算单个信号的综合分与分项因子。
func scoreSignal(
	sig *Signal,
	trend Trend,
	position Position,
	divByIndex map[int]Divergence,
	cfg Config,
) (float64, []ScoreFactor) {
	levelFactor := clamp01(cfg.LevelWeight(sig.Level))
	typeFactor := clamp01(cfg.TypeWeight(sig.Type))

	confirmFactor := 0.6
	confirmNote := "候选：结构已成立，等待确认"
	switch sig.State {
	case StateConfirmed:
		confirmFactor, confirmNote = 1.0, "已确认"
	case StateTriggered:
		confirmFactor, confirmNote = 0.85, "已触发（分型确认）"
	case StateExpired:
		confirmFactor, confirmNote = 0.15, "已过期"
	case StateInvalid:
		confirmFactor, confirmNote = 0.05, "已失效"
	}

	divFactor := 0.9
	divNote := "三买/三卖可独立于背驰成立"
	if div, ok := divByIndex[sig.DivergenceIndex]; ok {
		// 背驰强度按阈值归一：ratio 越小越强。
		// ratio 刚好等于阈值 → 0.6（仍算背驰），ratio 越接近 0 → 越接近 1。
		threshold := cfg.Divergence.Threshold
		if threshold <= 0 {
			threshold = 0.9
		}
		relative := (threshold - div.Ratio) / threshold
		divFactor = clamp01(0.6 + relative*0.4)
		divNote = div.KindCN + " 力度比 " + formatRatio(div.Ratio)
	}

	positionFactor, positionNote := positionWeight(position.Phase)
	envFactor, envNote := environmentWeight(sig, trend)

	factors := []ScoreFactor{
		{Name: "级别", Value: round2(levelFactor), Note: LevelCN(sig.Level)},
		{Name: "类型", Value: round2(typeFactor), Note: SignalTypeCN(sig.Type)},
		{Name: "确认强度", Value: round2(confirmFactor), Note: confirmNote},
		{Name: "背驰强度", Value: round2(divFactor), Note: divNote},
		{Name: "位置", Value: round2(positionFactor), Note: positionNote},
		{Name: "环境", Value: round2(envFactor), Note: envNote},
	}

	product := levelFactor * typeFactor * confirmFactor * divFactor * positionFactor * envFactor
	score := 0.0
	if product > 0 {
		score = 100 * math.Pow(product, 1.0/6.0)
	}
	return round1(score), factors
}

// positionWeight 位置权重：中枢后 / 回抽段的信号最"干净"。
func positionWeight(phase PositionPhase) (float64, string) {
	switch phase {
	case PhasePullback:
		return 1.0, "回抽段（离开中枢后的确认位）"
	case PhaseAfter:
		return 1.0, "中枢后"
	case PhaseDeparture:
		return 0.95, "离开段（趋势展开中）"
	case PhaseInside:
		return 0.7, "中枢中（震荡，方向不明）"
	case PhaseBefore:
		return 0.6, "中枢前（结构未完成）"
	}
	return 0.75, "位置未知"
}

// environmentWeight 环境权重：顺趋势的三类买卖点最强，反转类的一类买卖点弱但赔率高。
func environmentWeight(sig *Signal, trend Trend) (float64, string) {
	trendDirection := 0
	switch trend.Type {
	case TrendUp:
		trendDirection = 1
	case TrendDown:
		trendDirection = -1
	}
	signalDirection := 1
	if sig.Direction == "sell" {
		signalDirection = -1
	}

	// 与趋势同向：趋势延续类（三买/三卖）最可靠
	if trendDirection != 0 && signalDirection == trendDirection {
		if sig.Type == SignalBuy3 || sig.Type == SignalSell3 {
			return 1.0, "顺势三类买卖点（趋势延续）"
		}
		return 0.9, "顺势二类/一类买卖点"
	}
	// 与趋势反向：反转类，胜率低、赔率高
	if trendDirection != 0 && signalDirection == -trendDirection {
		if sig.Type == SignalBuy1 || sig.Type == SignalSell1 {
			return 0.75, "逆势一类买卖点（高赔率、低胜率）"
		}
		return 0.85, "逆势二类买卖点"
	}
	return 0.8, "盘整环境"
}

// markConflicts 标记同级别反向冲突（方案 §8.3）。
//
// 只有四条同时成立才算冲突：
//  1. 方向相反、级别相同；
//  2. **解读的是同一个结构**（同一个或相邻中枢；背驰信号看同一个背驰）；
//  3. 两条信号都**还没有失效/过期**（历史信号不参与："现在能不能做"才是重点）；
//  4. 有效区间有重叠（形成之后、失效/过期之前）。
//
// ⚠ 第 2、3 条是后来补的：只按时间重叠判断时，2021 年一个一直没失效的
// 一买会和 2026 年新中枢的三卖"冲突"，每条旧信号都挂着矛盾标记 ——
// 那不是同一结构给出的反向结论，只是先后两段走势，而且早就不能做了。
func markConflicts(signals []Signal, lastIndex int, cfg Config) {
	if !cfg.ShowConflict {
		return
	}
	for i := 0; i < len(signals); i++ {
		if isTerminalState(signals[i].State) {
			continue
		}
		startI, endI := activeRange(signals[i], lastIndex)
		for j := i + 1; j < len(signals); j++ {
			if isTerminalState(signals[j].State) {
				continue
			}
			if signals[i].Direction == signals[j].Direction {
				continue
			}
			if signals[i].Level != signals[j].Level {
				continue
			}
			if !sameStructure(signals[i], signals[j]) {
				continue
			}
			startJ, endJ := activeRange(signals[j], lastIndex)
			if startI > endJ || startJ > endI {
				continue
			}
			signals[i].Conflict = true
			signals[j].Conflict = true
			note := "同级别同一结构内同时出现" + SignalTypeCN(signals[i].Type) + " 与 " +
				SignalTypeCN(signals[j].Type) + "，标记结构矛盾，综合分减半、排序下沉"
			signals[i].ConflictNote = note
			signals[j].ConflictNote = note
		}
	}
}

// isTerminalState 判断信号是否已经有结论（失效/过期）。
func isTerminalState(state SignalState) bool {
	return state == StateInvalid || state == StateExpired
}

// sameStructure 判断两条反向信号是否在解读**同一个结构**。
//
//   - 都锚定中枢：同一个或相邻中枢才算（|Δ| <= 1）；
//   - 都锚定背驰：必须是同一个背驰；
//   - 其余（理论上不会出现，因为买卖点都要有中枢）按冲突处理。
func sameStructure(a, b Signal) bool {
	if a.PivotIndex >= 0 && b.PivotIndex >= 0 {
		diff := a.PivotIndex - b.PivotIndex
		if diff < 0 {
			diff = -diff
		}
		return diff <= 1
	}
	if a.DivergenceIndex >= 0 && b.DivergenceIndex >= 0 {
		return a.DivergenceIndex == b.DivergenceIndex
	}
	return true
}

// activeRange 返回信号的有效区间 [开始, 结束]。
func activeRange(sig Signal, lastIndex int) (int, int) {
	start := sig.FormIndex
	if start < 0 {
		start = 0
	}
	end := lastIndex
	switch sig.State {
	case StateInvalid:
		if sig.InvalidIndex >= 0 {
			end = sig.InvalidIndex
		}
	case StateExpired:
		if sig.ExpireIndex >= 0 {
			end = sig.ExpireIndex
		}
	}
	if end < start {
		end = start
	}
	return start, end
}

// sortSignals 按方案 §8.1 排序：
// 方向（买点一组、卖点一组）→ 级别 → 状态 → 类型 → 时间（越新越前）。
func sortSignals(signals []Signal, trend Trend) {
	sort.SliceStable(signals, func(i, j int) bool {
		a, b := signals[i], signals[j]

		if a.Direction != b.Direction {
			return a.Direction == "buy"
		}
		if rankA, rankB := LevelRank(a.Level), LevelRank(b.Level); rankA != rankB {
			return rankA > rankB
		}
		if stateA, stateB := statePriority(a.State), statePriority(b.State); stateA != stateB {
			return stateA > stateB
		}
		if typeA, typeB := typePriority(a.Type, trend.Type), typePriority(b.Type, trend.Type); typeA != typeB {
			return typeA > typeB
		}
		if a.FormIndex != b.FormIndex {
			return a.FormIndex > b.FormIndex
		}
		return a.Type < b.Type
	})
}

// statePriority 状态优先级：已确认 > 触发 > 候选 > 过期 > 失效。
func statePriority(state SignalState) int {
	switch state {
	case StateConfirmed:
		return 4
	case StateTriggered:
		return 3
	case StateCandidate:
		return 2
	case StateExpired:
		return 1
	}
	return 0
}

// typePriority 类型优先级。
//
// 趋势中：三类 > 二类 > 一类（延续信号胜率高）；
// 反转初：一类最高（但必须有大级别配合，见 multilevel）。
func typePriority(t SignalType, trend TrendType) int {
	// 判断这条信号是"顺势延续"还是"逆势反转"
	direction := 1
	if SignalDirection(t) == "sell" {
		direction = -1
	}
	trendDirection := 0
	switch trend {
	case TrendUp:
		trendDirection = 1
	case TrendDown:
		trendDirection = -1
	}
	reversal := trendDirection != 0 && direction == -trendDirection

	var level int
	switch t {
	case SignalBuy3, SignalSell3:
		level = 3
	case SignalBuy2, SignalSell2:
		level = 2
	default:
		level = 1
	}
	if reversal {
		// 反转初：一类优先
		return 4 - level
	}
	return level
}

// limitSignals 限制每种买卖点保留的**历史**信号条数。
//
// - 候选 / 触发是"当下正在走的"，全部保留；
// - 确认 / 失效 / 过期按形成时间从新到旧保留前 N 条。
//
// 不限制的话，一只票跑十年会攒出上百条信号：图上的标签会糊成一片，
// 面板里也全是三年前的历史，真正该看的那一条反而找不到。
func limitSignals(signals []Signal, maxPerType int) []Signal {
	if maxPerType <= 0 {
		return signals
	}

	type entry struct {
		index int
		form  int
	}
	history := make(map[SignalType][]entry, 6)
	for i, sig := range signals {
		if sig.State == StateCandidate || sig.State == StateTriggered {
			continue
		}
		history[sig.Type] = append(history[sig.Type], entry{index: i, form: sig.FormIndex})
	}

	keep := make(map[int]struct{}, len(signals))
	for _, entries := range history {
		sort.SliceStable(entries, func(a, b int) bool { return entries[a].form > entries[b].form })
		for i, item := range entries {
			if i >= maxPerType {
				break
			}
			keep[item.index] = struct{}{}
		}
	}

	out := make([]Signal, 0, len(signals))
	for i, sig := range signals {
		if sig.State == StateCandidate || sig.State == StateTriggered {
			out = append(out, sig)
			continue
		}
		if _, ok := keep[i]; ok {
			out = append(out, sig)
		}
	}
	return out
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func round1(value float64) float64 { return math.Round(value*10) / 10 }
func round2(value float64) float64 { return math.Round(value*100) / 100 }
