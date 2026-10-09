package score

import (
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 换股配对（3.1）
// ---------------------------------------------------------------------------

// buildReplacementPairs 生成换股配对。
//
// 规则（逐条对齐原实现 portfolio_rotation_logic.py:2173-2280）：
//   - 持仓按 total_score **升序**遍历（最弱的优先换）
//   - **低吸策略永不换股**（直接跳过）
//   - 每个持仓取第一个通过 candidate_quality_ok(replacement_mode=True) 的未用候选
//   - 分差门槛 required_gap = max(20.0, replace_score_gap)
//   - force_clear 分支**跳过分差校验**（只要候选分数 >= ready_score）
//   - rebound_ready 时也免分差
//   - 全局最多 max_replace_count 对
//   - mode = partial_rotate（持仓层数≥2 且 计划只数 < max_stock_limit 且 非 bull），
//     否则 full_replace
//
// 返回配对列表与"已被用掉的候选代码"集合。
func buildReplacementPairs(in RotationInput, protectStart, protectRetrace float64) ([]ReplacementPair, map[string]bool) {
	used := map[string]bool{}
	cfg := in.Config
	state := in.MarketState

	// 低吸永不换股
	if isLowBuyStrategy(cfg.StrategyID) {
		return nil, used
	}
	if len(in.Holdings) == 0 || len(in.Candidates) == 0 {
		return nil, used
	}

	// 持仓排序（原实现 :2161-2164）：
	//   sort_values(["total_score","relative_strength_20d","repair_score","position_layers","ts_code"],
	//               ascending=[True, True, True, False, True])
	// 即总分升序（最弱优先）→ RS 升序 → 修复分升序 → **层数降序** → 代码升序。
	holdings := make([]Row, len(in.Holdings))
	copy(holdings, in.Holdings)
	sort.SliceStable(holdings, func(i, j int) bool {
		a, b := holdings[i], holdings[j]
		if a.TotalScore != b.TotalScore {
			return a.TotalScore < b.TotalScore
		}
		if a.RelativeStrength20D != b.RelativeStrength20D {
			return a.RelativeStrength20D < b.RelativeStrength20D
		}
		if a.RepairScore != b.RepairScore {
			return a.RepairScore < b.RepairScore
		}
		if a.PositionLayers != b.PositionLayers {
			return a.PositionLayers > b.PositionLayers
		}
		return a.Code < b.Code
	})

	plannedNames := len(in.Holdings)
	requiredGap := math.Max(20.0, cfg.ReplaceScoreGap)
	maxPairs := cfg.MaxReplaceCount
	if maxPairs < 1 {
		maxPairs = 1
	}

	var pairs []ReplacementPair
	for _, hold := range holdings {
		if len(pairs) >= maxPairs {
			break
		}

		broken := IsStructureBroken(hold, cfg.StrategyID, state, protectStart, protectRetrace)
		repairable := CanHoldStrategyRepairPullback(hold, cfg.StrategyID, state)
		// oversold_failed 也要计入 force_clear（原实现 :2203-2204
		// `force_clear = force_clear or oversold_failed`）
		oversoldFailed := strategyIsOversold(cfg.StrategyID) &&
			IsFailedOversoldReboundHolding(hold, state)
		// force_clear：结构破坏且不可修复（或超跌反弹失败）→ 换股时免分差
		forceClear := (broken && !repairable) || oversoldFailed

		// 急涨兑现的持仓不参与配对（原实现 :2205-2206
		// `if quick_profit_take or repair_hold: continue`）——
		// 否则会产生"有换入、无对应换出"的悬空行
		if ShouldQuickProfitTake(hold, state) {
			continue
		}

		// rebound_ready 的**正确定义**（原实现 :2193-2194）：
		//   rebound_wait = needs_rebound_exit(row, state)   ← 非低吸 + **未结构破坏** + close<ma30*0.995
		//   rebound_ready = rebound_wait and _is_big_rebound_candle(row)
		// ⚠ 它**不免分差**（分差校验对非 force_clear 的所有分支都执行）
		reboundReady := NeedsReboundExit(hold, cfg.StrategyID, state, protectStart, protectRetrace) &&
			IsBigReboundCandle(hold)

		found := false
		for _, cand := range in.Candidates {
			if used[cand.Code] {
				continue
			}
			ok, _ := CandidateQualityOK(cand, cfg, state, true)
			if !ok {
				continue
			}
			gap := cand.TotalScore - hold.TotalScore

			// 只有 force_clear 免分差；rebound_ready **不免**
			if forceClear {
				if cand.TotalScore < cfg.ReadyScore {
					continue
				}
			} else if gap < requiredGap {
				continue
			}

			// pair_mode 按分支决定（原实现 :2222 / :2231 / :2237-2241 三处规则不同）
			mode := "full_replace"
			switch {
			case forceClear:
				// :2222 硬编码 full_replace（不看层数、不看空位）
				mode = "full_replace"
			case reboundReady:
				// :2231 `full_replace if hold_layers<=1 or bull else partial_rotate`
				// —— **不看**计划只数是否已满
				if hold.PositionLayers > 1 && state != StateBull {
					mode = "partial_rotate"
				}
			default:
				if hold.PositionLayers >= 2 && plannedNames < in.MaxStockLimit && state != StateBull {
					mode = "partial_rotate"
				}
			}

			reason := "换入更强标的（分差 " + ftoa(gap, 1) + "）"
			if forceClear {
				reason = "结构破坏，换入更强标的"
			} else if reboundReady {
				reason = "跌破MA30且候选更强，换股"
			}

			totalGap := round(cand.TotalScore-hold.TotalScore, 4)
			pairs = append(pairs, ReplacementPair{
				HoldingCode:    hold.Code,
				HoldingName:    hold.Name,
				CandidateCode:  cand.Code,
				CandidateName:  cand.Name,
				Mode:           mode,
				HoldingScore:   hold.TotalScore,
				CandidateScore: cand.TotalScore,
				TotalScoreGap:  totalGap,
				RSScoreGap:     round(cand.RSScore-hold.RSScore, 4),
				RepairScoreGap: round(cand.RepairScore-hold.RepairScore, 4),
				MainReason:     reason,
				ExpectedUplift: round(math.Max(0, totalGap), 4),
				RankNo:         len(pairs) + 1,
			})
			used[cand.Code] = true
			found = true
			// 计划只数只在**正常分支选中 partial_rotate** 时递增
			// （原实现 :2239；force_clear / rebound_ready 分支不递增）
			if mode == "partial_rotate" && !forceClear && !reboundReady {
				plannedNames++
			}
			break
		}
		_ = found
	}
	return pairs, used
}

// strategyIsOversold 判断是否超跌反弹策略。
func strategyIsOversold(strategyID string) bool {
	return strategyID == StrategyOversold
}

// ---------------------------------------------------------------------------
// 候选入场质量（3.2.2）
// ---------------------------------------------------------------------------

// CandidateQualityOK 判定候选是否达到入场质量。
//
// 返回 (是否通过, 原因)。replacementMode 为 true 时门槛 +1 分。
//
// 低吸策略走自己的结构条件（is_trend_pullback_low_buy_candidate）。
func CandidateQualityOK(row Row, cfg StrategyConfig, marketState string, replacementMode bool) (bool, string) {
	if isLowBuyStrategy(cfg.StrategyID) {
		ok := isLowBuyCandidate(row, cfg, cfg.LowBuyBreadthReadyCount)
		if !ok {
			return false, "低吸结构条件不满足"
		}
		return true, "低吸结构条件满足"
	}

	// 公共前置：分数下限
	if row.TotalScore < cfg.ReadyScore {
		return false, "总分低于建仓线"
	}

	// 公共前置：5 个质量过滤（原实现 :1131-1140）——
	// 缺了它们会放进过热票、拥挤票、下跌中继票
	if ok, reason := passesCandidatePrefilters(row, cfg.StrategyID, marketState); !ok {
		return false, reason
	}

	// 均线位置门槛（各策略不同）
	switch cfg.StrategyID {
	case StrategyExpert:
		if row.Ma30 > 0 && row.Close < row.Ma30*0.958 {
			return false, "跌破MA30过多"
		}
	case StrategyOversold:
		if row.Ma30 > 0 && row.Close < row.Ma30*0.90 {
			return false, "跌破MA30过多"
		}
	default:
		if row.Ma30 > 0 && row.Close < row.Ma30*0.995 {
			return false, "跌破MA30"
		}
	}

	// 熊市必须有新鲜信号
	if marketState == StateBear {
		ageLimit := 2
		if !row.hasFreshSignal(ageLimit) {
			return false, "熊市且无新鲜信号"
		}
	}

	// 各策略的分项下限
	var minScore, minTrend, minStructure, minRS, minRS20 float64
	switch cfg.StrategyID {
	case StrategyOversold:
		minScore = cfg.ReadyScore - 2
		minTrend, minStructure, minRS, minRS20 = 14, 8, 7, -6
	case StrategyExpert:
		base := 60.0
		if marketState == StateRange {
			base = 62
		}
		minScore = math.Max(cfg.ReadyScore, base) - 2
		minTrend = 15
		if marketState == StateBull {
			minTrend = 16
		}
		minStructure, minRS, minRS20 = 8, 6, -5
	default: // trend_growth
		base := 70.0
		if marketState == StateRange {
			base = 72
		}
		minScore = math.Max(cfg.ReadyScore, base) - 2
		minTrend = 18
		if marketState == StateBull {
			minTrend = 19
		}
		minStructure, minRS = 9, 7
		minRS20 = -3
		if marketState == StateBull {
			minRS20 = -5
		}
	}
	if replacementMode {
		minScore += 1.0
	}

	// 市场档位的分数下限（按组合范围 + 个股所属市场分档）
	floor := marketScopeFloor(cfg.StrategyID, cfg.MarketScopeKey, row.Market)
	if row.TotalScore < math.Max(minScore, floor) {
		return false, "总分未达门槛"
	}
	if row.TrendScore < minTrend || row.StructureScore < minStructure || row.RSScore < minRS {
		return false, "分项未达门槛"
	}
	if row.RelativeStrength20D < minRS20 {
		return false, "相对强度过低"
	}

	// 各策略的追加条件
	switch cfg.StrategyID {
	case StrategyOversold:
		if row.DrawdownFromHigh120 < -32 || row.DrawdownFromHigh120 > -18 {
			return false, "回撤不在超跌区间"
		}
		if row.ReboundFromLow60 < 0.25 || row.ReboundFromLow60 > 0.62 {
			return false, "反弹位置不合适"
		}
		if row.RelativeStrength20D > 18 {
			return false, "相对强度过热"
		}
		if row.PctChg < 0.5 || row.PctChg > 4.5 {
			return false, "当日涨幅不合适"
		}
		if row.StockReturn10D > 5 {
			return false, "10 日涨幅过大"
		}
		if row.Ma20 > 0 && (row.Close < row.Ma20*0.94 || row.Close > row.Ma20*1.04) {
			return false, "偏离MA20过多"
		}
		if row.Ma30 > 0 && (row.Close < row.Ma30*0.95 || row.Close > row.Ma30*1.08) {
			return false, "偏离MA30过多"
		}
		if row.VolumeRatio20 < 0.9 || row.VolumeRatio20 > 2.0 {
			return false, "量比不合适"
		}
		if row.RepairScore < 5 {
			return false, "修复分不足"
		}
	case StrategyExpert:
		// `industry_edge = industry_return_20d - benchmark_return_20d`
		//（原实现 :1121-1126）。
		// ⚠ `mainline_score = max(mainline_board_score, industry_board_score,
		// board_research_score)` 依赖**板块研究分** —— 本项目没有那个数据源，
		// 所以 `row.MainlineScore` 恒为 0。`industry_edge` 这一支是真的。
		industryEdge := row.IndustryReturn20D - row.BenchmarkReturn20D
		if row.MainlineScore < 28 && industryEdge < 2 &&
			!(row.TrendScore >= 20 && row.RSScore >= 9) {
			return false, "主线强度不足"
		}
		if row.DrawdownFromHigh120 < -30 || row.DrawdownFromHigh120 > 8 {
			return false, "回撤不在区间"
		}
		if row.Ma20 > 0 && row.Close > row.Ma20*1.28 {
			return false, "偏离MA20过多"
		}
		if row.Ma30 > 0 && row.Close < row.Ma30*0.958 {
			return false, "跌破MA30"
		}
		if row.Ma60 > 0 && row.Ma20 > 0 && row.Ma20 < row.Ma60*0.94 {
			return false, "MA20 低于 MA60 过多"
		}
		if row.StockReturn5D > 18 && row.RelativeStrength20D > 42 {
			return false, "短期过热"
		}
	default: // trend_growth
		if row.Ma20 > 0 && row.Close < row.Ma20*0.975 {
			return false, "跌破MA20过多"
		}
		if row.Ma30 > 0 && row.Close < row.Ma30*0.965 {
			return false, "跌破MA30过多"
		}
	}
	return true, "符合建仓条件"
}

// StockMarketOf 把本项目的市场 key 映射成原实现的词汇（main/gem/star）。
//
// ⚠ 这两个词汇表**不一样**：本项目 `market.KeyOf` 返回 `sh-main` / `sz-main` /
// `chinext` / `star` / `bse`，而原实现用 主板 / 创业板 / 科创板。
// 直接比较会导致分支永远不命中（这个坑踩过一次）。
func StockMarketOf(key string) string {
	switch key {
	case "sh-main", "sz-main", "main":
		return "main"
	case "chinext", "gem":
		return "gem"
	case "star":
		return "star"
	}
	return ""
}

// marketScopeFloor 是市场档位的分数下限。
//
// 原实现只有**两个**策略有这张表（`trend_growth_market_scope_score_floor` 与
// `oversold_rebound_market_scope_score_floor`），且策略不匹配时返回 **0**；
// 最后取两者的 `max`。其余策略一律 0。
//
//	trend_growth:  main→主板80 ; gem→创业板80 ; main_gem→主板80/创业板82 ;
//	               gem_star|all→创业板82
//	oversold:      创业板 74 ; 科创板 90
//
// 表里没写到的组合返回 0（不额外设门槛）—— 不臆造原实现没有的阈值。
//
// stockMarket 是本项目的市场 key（sh-main/chinext/star/...），内部会做映射。
func marketScopeFloor(strategyID, scopeKey, stockMarket string) float64 {
	market := StockMarketOf(stockMarket)
	if market == "" {
		return 0
	}

	switch strategyID {
	case StrategyOversold:
		switch market {
		case "gem":
			return 74
		case "star":
			return 90
		}
		return 0

	case StrategyTrendGrowth:
		switch scopeKey {
		case "main":
			if market == "main" {
				return 80
			}
		case "gem":
			if market == "gem" {
				return 80
			}
		case "main_gem":
			switch market {
			case "main":
				return 80
			case "gem":
				return 82
			}
		case "gem_star", "all":
			if market == "gem" {
				return 82
			}
		}
		return 0
	}
	// 高手模拟、低吸：原实现没有这张表
	return 0
}

// isLowBuyCandidate 低吸入场条件。
//
// 逐条对照 portfolio_rotation_logic.py:550-586：
//
//	min(open, close, ma5, ma20, ma30, ma60, ma120, ma250, ma5_prev, ma250_prev) <= 0 → False
//	ma120 > ma250 且 **ma250 > ma250_prev** 且 ma30 > ma60 且 ma20 > ma30
//	ma30*0.975 <= close <= ma20*1.04
//	ma5 < ma30 且 **ma5 > ma5_prev**
//	close > open（阳线）
//	ret20 = **stock_return_20d** <= 25
//	dd20 落在 ready_count 决定的两档区间内
//
// ⚠ 三处曾实现错：漏 `ma250 > ma250_prev`、漏 `ma5 > ma5_prev`、
// 用 `relative_strength_20d` 代替了 `stock_return_20d`（大盘涨跌时判定会相反）。
func isLowBuyCandidate(row Row, cfg StrategyConfig, breadthReadyCount int) bool {
	// 0 值前置校验（原实现是 min(...) <= 0 一次性拒绝）
	if row.Open <= 0 || row.Close <= 0 || row.Ma5 <= 0 || row.Ma20 <= 0 ||
		row.Ma30 <= 0 || row.Ma60 <= 0 || row.Ma120 <= 0 || row.Ma250 <= 0 ||
		row.Ma5Prev <= 0 || row.Ma250Prev <= 0 {
		return false
	}
	if !(row.Ma120 > row.Ma250 && row.Ma250 > row.Ma250Prev &&
		row.Ma30 > row.Ma60 && row.Ma20 > row.Ma30) {
		return false
	}
	if row.Close < row.Ma30*0.975 || row.Close > row.Ma20*1.04 {
		return false
	}
	if !(row.Ma5 < row.Ma30 && row.Ma5 > row.Ma5Prev) {
		return false
	}
	if row.Close <= row.Open {
		return false
	}
	if row.StockReturn20D > 25.0 {
		return false
	}
	low, high := lowBuyDrawdownBounds(breadthReadyCount)
	if row.DrawdownFromHigh20 < low || row.DrawdownFromHigh20 > high {
		return false
	}
	return true
}

// lowBuyDrawdownBounds 按广度就绪数给出 dd20 的允许区间。
//
// 对应原实现 `_low_buy_drawdown_bounds`：>=1200 用紧档 (-18,-1.5)，否则 (-28,-3)。
func lowBuyDrawdownBounds(breadthReadyCount int) (float64, float64) {
	if breadthReadyCount >= 1200 {
		return -18.0, -1.5
	}
	return -28.0, -3.0
}

// sortCandidates 按候选池排序规则排序（4.2）。
//
//	total_score DESC, signal_priority ASC, active_signal_score DESC,
//	signal_age ASC, rs20 DESC, repair_score DESC, research_score DESC, ts_code ASC
func sortCandidates(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.TotalScore != b.TotalScore {
			return a.TotalScore > b.TotalScore
		}
		pa, pb := signalPriority(a), signalPriority(b)
		if pa != pb {
			return pa < pb
		}
		if a.SignalScore != b.SignalScore {
			return a.SignalScore > b.SignalScore
		}
		if a.SignalAge != b.SignalAge {
			return a.SignalAge < b.SignalAge
		}
		if a.RelativeStrength20D != b.RelativeStrength20D {
			return a.RelativeStrength20D > b.RelativeStrength20D
		}
		if a.RepairScore != b.RepairScore {
			return a.RepairScore > b.RepairScore
		}
		if a.ResearchScore != b.ResearchScore {
			return a.ResearchScore > b.ResearchScore
		}
		return a.Code < b.Code
	})
}

// signalPriority 信号优先级：buy=0 / candidate_buy=1 / 其他=2。
func signalPriority(row Row) int {
	switch row.SignalType {
	case "buy":
		return 0
	case "candidate_buy":
		return 1
	}
	return 2
}

// initialEntryLayers 计算建仓层数（3.3）。
//
// 低吸用 clip(int(total_layers/target), 1, total_layers)，其余默认 1 层。
func initialEntryLayers(row Row, cfg StrategyConfig, freeLayers int) int {
	_ = row
	_ = freeLayers
	if isLowBuyStrategy(cfg.StrategyID) {
		if cfg.TargetCount <= 0 {
			return 1
		}
		layers := cfg.TotalLayers / cfg.TargetCount
		if layers < 1 {
			layers = 1
		}
		if layers > cfg.TotalLayers {
			layers = cfg.TotalLayers
		}
		return layers
	}
	return 1
}

// ---------------------------------------------------------------------------
// 市场广度闸门（3.7）
// ---------------------------------------------------------------------------

// breadthThresholds 是广度闸门的阈值。
func breadthThresholds(strategyID string) (readyScore float64, minReadyCount int) {
	switch strategyID {
	case StrategyOversold:
		return 62, 5
	case StrategyExpert:
		return 60, 2
	default:
		return 70, 20
	}
}

// breadthEntryAllowed 判断市场广度是否允许建仓。
//
// universe < 100 时闸门**不启用**（直接放行）。
//
// 分子分母都应当在**全量评分**上算（原实现 portfolio_rotation_logic.py:1772-1786
// `universe_count = len(scores)`、`ready_count = (scores >= ready_score).sum()`）。
func breadthEntryAllowed(universeScores []float64, strategyID string) bool {
	universeCount := len(universeScores)
	if universeCount < 100 {
		return true
	}
	readyScore, minReadyCount := breadthThresholds(strategyID)
	readyCount := 0
	for _, score := range universeScores {
		if score >= readyScore {
			readyCount++
		}
	}
	return readyCount >= minReadyCount
}

// applyBreadthGate 广度不足时的处理：
//
//	replace_in / add 行**直接删除**
//	add_layer 改写为 keep（暂停加仓）
//	replace_out 改写为 clear（清空候选字段）
//	compare_rows 全部清空
func applyBreadthGate(rows []RotationRow, plan *RotationPlan) []RotationRow {
	out := make([]RotationRow, 0, len(rows))
	for _, row := range rows {
		switch row.Action {
		case ActionReplaceIn, ActionAdd:
			continue // 直接删除
		case ActionAddLayer:
			row.Action = ActionKeep
			row.DecisionState = StateBreadthGateHold
			row.Reason = "市场广度不足，暂停加仓"
			row.TargetLayers = row.CurrentLayers
			row.LayersDelta = 0
		case ActionReplaceOut:
			row.Action = ActionClear
			row.DecisionState = StateBreadthGateExit
			row.Reason = "市场广度不足，换股改为清仓"
			row.CandidateCode = ""
			row.CandidateName = ""
			row.CandidateScore = 0
			row.ScoreGap = 0
			row.ExpectedImprovement = 0
		}
		out = append(out, row)
	}
	plan.Compare = nil
	return out
}
