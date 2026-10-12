package score

import (
	"fmt"
	"strings"
)

// "为什么没有建议" —— 1:1 移植 `_build_no_action_reasons`
//（portfolio_rotation_logic.py:1863-1995）。
//
// 界面只显示一个空的建议列表时，用户没法判断是"数据没准备好"、
// "策略不给开仓"还是"持仓都还稳"。这一段就是把这三种情况说清楚，最多 3 条。

// noActionInput 汇总判定所需的状态（避免超长参数列表）。
type noActionInput struct {
	plan          RotationPlan
	in            RotationInput
	cfg           StrategyConfig
	lowBuy        bool
	desiredCount  int
	desiredLayers int
	currentCount  int
	currentLayers int
	addNeed       int
	freeLayers    int
}

// dedupeReasons 去重并保持顺序（原实现 `_dedupe_reasons`）。
func dedupeReasons(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	seen := map[string]bool{}
	for _, reason := range reasons {
		text := strings.TrimSpace(reason)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

// countPositionChangeActions 统计"会改变持仓"的动作数
// （原实现 `_count_position_change_actions`）—— keep / observe 不计。
func countPositionChangeActions(rows []RotationRow) int {
	count := 0
	for _, row := range rows {
		switch row.Action {
		case ActionKeep, ActionObserve, "":
			continue
		}
		count++
	}
	return count
}

// buildNoActionReasons 生成"没有建议"的解释。
func buildNoActionReasons(input noActionInput) []string {
	// 候选池为空、或已经有改仓动作 → 不需要解释
	if len(input.in.Candidates) == 0 || countPositionChangeActions(input.plan.Rows) > 0 {
		return nil
	}

	cfg := input.cfg
	reasons := make([]string, 0, 4)
	heldCount := 0
	for _, row := range input.in.Holdings {
		if row.Code != "" {
			heldCount++
		}
	}
	readyCount := input.in.LowBuyBreadthReadyCount

	if input.lowBuy {
		breadthAllowed := LowBuyBreadthEntryAllowed(readyCount, cfg.LowBuyPositionMode)
		indexAllowed := LowBuyIndexEntryAllowed(input.in.LowBuyIndex, readyCount)
		threshold := lowBuyPositionThresholds(cfg.LowBuyPositionMode)[0][0]
		if !breadthAllowed {
			reasons = append(reasons,
				fmt.Sprintf("趋势低吸广度未到开仓阈值：%d只，低于%d只", readyCount, threshold))
		}
		if breadthAllowed && !indexAllowed {
			reasons = append(reasons,
				"趋势低吸指数闸门未确认：创业板指数未同时站上MA20和MA60")
		}
		if input.desiredCount <= 0 {
			reasons = append(reasons, "趋势低吸动态目标持仓为0，只保留候选观察")
		} else if input.currentCount >= maxIntVal(0, input.desiredCount) {
			reasons = append(reasons,
				fmt.Sprintf("当前持仓已达到趋势低吸动态目标%d只", maxIntVal(0, input.desiredCount)))
		}
		if input.desiredLayers > 0 && input.currentLayers >= input.desiredLayers {
			reasons = append(reasons,
				fmt.Sprintf("目标层数已用满：当前%d层，目标%d层", input.currentLayers, input.desiredLayers))
		}
		if len(reasons) == 0 && input.addNeed <= 0 {
			reasons = append(reasons, "目标持仓数已满足，候选池继续排序观察")
		}
		if len(reasons) == 0 {
			reasons = append(reasons,
				"候选通过低吸池筛选，但当天新增数量、重复持仓或层数约束未打开新仓")
		}
		return topReasons(reasons, 3)
	}

	if !cfg.AllowNewAdds {
		reasons = append(reasons, fmt.Sprintf(
			"%s策略不主动开新仓，候选池先观察", marketStateLabelOf(cfg.MarketState)))
	}
	if !input.plan.EntryAllowed {
		readyScore, minReady := breadthThresholds(input.cfg.StrategyID)
		actual := 0
		for _, score := range input.in.UniverseScores {
			if score >= readyScore {
				actual++
			}
		}
		reasons = append(reasons, fmt.Sprintf(
			"市场广度不足：%d只≥%.0f分，低于%d只", actual, readyScore, minReady))
	}
	if input.addNeed <= 0 && heldCount > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"持仓数量已达到目标：当前%d只，目标%d只", input.currentCount, maxIntVal(0, input.desiredCount)))
	}
	if input.desiredLayers > 0 && input.currentLayers >= input.desiredLayers {
		reasons = append(reasons, fmt.Sprintf(
			"目标层数已用满：当前%d层，目标%d层", input.currentLayers, input.desiredLayers))
	}

	quality := make([]Row, 0, len(input.in.Candidates))
	for _, row := range input.in.Candidates {
		if ok, _ := CandidateQualityOK(row, cfg, cfg.MarketState, false); ok {
			quality = append(quality, row)
		}
	}
	if len(quality) == 0 {
		reasons = append(reasons, "候选池有数据，但未通过入场质量过滤")
	} else if heldCount > 0 && len(input.plan.Compare) == 0 {
		weakest := input.in.Holdings[0]
		for _, row := range input.in.Holdings {
			if row.TotalScore < weakest.TotalScore ||
				(row.TotalScore == weakest.TotalScore && row.Code < weakest.Code) {
				weakest = row
			}
		}
		best := quality[0]
		for _, row := range quality {
			if row.TotalScore > best.TotalScore ||
				(row.TotalScore == best.TotalScore && row.RSScore > best.RSScore) ||
				(row.TotalScore == best.TotalScore && row.RSScore == best.RSScore && row.Code < best.Code) {
				best = row
			}
		}
		gap := best.TotalScore - weakest.TotalScore
		required := cfg.ReplaceScoreGap
		if required < 20 {
			required = 20
		}
		if gap < required {
			reasons = append(reasons, fmt.Sprintf(
				"最佳候选相对最弱持仓分差%.1f分，未达到替换门槛%.1f分", gap, required))
		} else {
			reasons = append(reasons, "现有持仓未触发退出或替换条件，候选暂不承接仓位")
		}
	}
	if input.addNeed > 0 && input.freeLayers <= 0 {
		reasons = append(reasons, "仍有目标持仓缺口，但目标层数没有空余")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "候选池有数据，但现有持仓评分稳定，尚未触发新增、减仓或替换规则")
	}
	return topReasons(reasons, 3)
}

func topReasons(reasons []string, limit int) []string {
	out := dedupeReasons(reasons)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func maxIntVal(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// marketStateLabelOf 取市场状态中文标签。
//
// 对应原版 `_market_state_label`（portfolio_rotation_logic.py:1875-1882）：
//
//	mapping = {"bull": "牛市", "range": "震荡市", "bear": "熊市"}
//	state = normalize_market_state(value)
//	return mapping.get(state, state)
//
// ⚠ 两处曾与原版不同，而这个标签**是用户可见的**（它拼进
// "`%s`策略不主动开新仓，候选池先观察" 这条 no_action 原因里，
// 见 no_action.go:108 ↔ 原版 :1957）：
//
//  1. Go 用的是 `StateLabels` 那套**长描述**（"牛市/趋势扩张"、"震荡/轮动"、
//     "熊市/风险释放"）—— 那是策略说明用的文案，不是这个字段；
//  2. 没有走 `normalizeMarketState` → 传 "BULL" 会原样吐回 "BULL"，
//     传 "" 会吐回空串，而原版一律归到"震荡市"。
func marketStateLabelOf(state string) string {
	switch normalizeMarketState(state) {
	case StateBull:
		return "牛市"
	case StateBear:
		return "熊市"
	}
	return "震荡市"
}
