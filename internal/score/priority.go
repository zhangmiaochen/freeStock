package score

import (
	"sort"
	"strings"
)

// 候选池的"入场优先级"重排。
//
// 1:1 移植自 portfolio_rotation_logic.py:1616-1698。
//
// **为什么需要它**：候选池原本只按 `total_score DESC, ts_code` 排，
// 而原实现会把趋势成长的候选按一个**六项加权的百分位复合分**重排 ——
// 于是"取前 80 名"取到的是**不同的集合**，配到哪只候选、给哪几只建仓都会不同。
//
// 注意其中三项是 **low_first（低者优先）**：`ma30 乖离`、`量比`、`rs20`。
// 也就是说它偏好"离 MA30 不远、量能温和、相对强度不极端"的入场点 ——
// 不是简单地取分最高的。

// seriesRankScore 对应原实现的 `_series_rank_score`（:1608-1613）：
//
//	ranked = values.rank(method="average", pct=True)
//	if not high_first: ranked = 1.0 - ranked
//
// 缺失值原实现用 `default` 填充；这里没有缺失概念（Go 的值都是 0）。
//
// 这里直接走 `seriesRankScoreRanked`（同一个口径，**只有一份实现**）——
// 单次调用多花一次排序无所谓；成批调用请用下面的查询器版本，
// 否则同一列问 n 次就是 O(n²)（回测每个交易日都要跑一遍）。
func seriesRankScore(values []float64, value float64, highFirst bool) float64 {
	return seriesRankScoreRanked(NewRankLookup(values), value, highFirst)
}

// trendGrowthEntryPriority 对应原实现的 `_trend_growth_entry_priority`（:1616-1628）。
//
//	0.25 * rank(total_score,            high_first)
//	0.25 * rank(structure_score,        high_first)
//	0.18 * rank(ma30_gap_pct,           low_first)
//	0.12 * rank(volume_ratio_20,        low_first)
//	0.12 * rank(relative_strength_20d,  low_first)
//	0.08 * rank(drawdown_from_high_120, high_first)
func trendGrowthEntryPriority(rows []Row) []float64 {
	n := len(rows)
	total := make([]float64, n)
	structure := make([]float64, n)
	volumeRatio := make([]float64, n)
	rs20 := make([]float64, n)
	drawdown := make([]float64, n)

	// ma30_gap_pct = (close/ma30 - 1)*100，**ma30 为 0 时该行不参与排名**。
	//
	// 原实现两步（portfolio_rotation_logic.py:1618-1624 + `_series_rank_score` :1604-1613）：
	//
	//	ma30 = ma30_price.replace(0, NaN)          # 0 → 缺失
	//	ma30_gap_pct = (close/ma30 - 1) * 100
	//	ranked = values.rank(pct=True, na_option="keep")   # 缺失行保持缺失
	//	if not high_first: ranked = 1 - ranked
	//	return ranked.fillna(default=0.0)          # 缺失行 → 0.0
	//
	// ⚠ 此前是"把 0 当成真实 gap 去排名" —— 而 low_first 下 `1 - pct(0)`
	// 通常接近 1（接近满分），等于给**没有 MA30 的票**（数据不足）
	// 一个高分，把入场优先级排序整体带偏。正确值是 **0.0**。
	ma30Gap := make([]float64, n)
	ma30Valid := make([]bool, n)
	ma30RankInput := make([]float64, 0, n)
	for i, row := range rows {
		if row.Ma30 != 0 {
			ma30Gap[i] = (row.Close/row.Ma30 - 1) * 100
			ma30Valid[i] = true
			ma30RankInput = append(ma30RankInput, ma30Gap[i])
		}
	}

	for i, row := range rows {
		total[i] = row.TotalScore
		structure[i] = row.StructureScore
		volumeRatio[i] = row.VolumeRatio20
		rs20[i] = row.RelativeStrength20D
		drawdown[i] = row.DrawdownFromHigh120
	}

	// 六列各自"排序一次、按行查询" —— 与逐行 RankPct 的结果逐位相同，
	// 但把 O(6n²) 降成 O(6n log n)。回测里这个函数**每个交易日都要跑一遍**。
	rankers := [6]*RankLookup{
		NewRankLookup(total), NewRankLookup(structure), NewRankLookup(ma30RankInput),
		NewRankLookup(volumeRatio), NewRankLookup(rs20), NewRankLookup(drawdown),
	}
	out := make([]float64, n)
	for i := range rows {
		// 缺失 MA30 的行：排名分量取 default=0.0，不参与排名也不查询
		ma30Term := 0.0
		if ma30Valid[i] {
			ma30Term = seriesRankScoreRanked(rankers[2], ma30Gap[i], false)
		}
		out[i] = seriesRankScoreRanked(rankers[0], total[i], true)*0.25 +
			seriesRankScoreRanked(rankers[1], structure[i], true)*0.25 +
			ma30Term*0.18 +
			seriesRankScoreRanked(rankers[3], volumeRatio[i], false)*0.12 +
			seriesRankScoreRanked(rankers[4], rs20[i], false)*0.12 +
			seriesRankScoreRanked(rankers[5], drawdown[i], true)*0.08
	}
	return out
}

// seriesRankScoreRanked 是 seriesRankScore 的查询器版本（同一个口径）。
func seriesRankScoreRanked(lookup *RankLookup, value float64, highFirst bool) float64 {
	ranked := lookup.Pct(value)
	if !highFirst {
		return 1.0 - ranked
	}
	return ranked
}

// PrioritizeEntryPoints 按策略对候选做入场优先级重排（原实现 :1646-1698）。
//
//	趋势成长 → 六项加权百分位复合分（DESC，越大越好）
//	低吸     → 按 low_buy_sort_mode 三选一（见 prioritizeLowBuy）
//	其余     → 原样返回（超跌/高手原实现不做这一步）
func PrioritizeEntryPoints(rows []Row, cfg StrategyConfig) []Row {
	if len(rows) == 0 {
		return rows
	}
	switch cfg.StrategyID {
	case StrategyTrendGrowth:
		return prioritizeTrendGrowth(rows)
	case StrategyTrendLowBuy:
		return prioritizeLowBuy(rows, cfg)
	}
	return rows
}

// prioritizeTrendGrowth 趋势成长的重排。
func prioritizeTrendGrowth(rows []Row) []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	priority := trendGrowthEntryPriority(out)
	index := make(map[int]float64, len(out))
	for i := range out {
		index[i] = priority[i]
	}
	sort.SliceStable(out, func(a, b int) bool {
		if index[a] != index[b] {
			return index[a] > index[b]
		}
		if out[a].TotalScore != out[b].TotalScore {
			return out[a].TotalScore > out[b].TotalScore
		}
		if out[a].StructureScore != out[b].StructureScore {
			return out[a].StructureScore > out[b].StructureScore
		}
		if out[a].RelativeStrength20D != out[b].RelativeStrength20D {
			return out[a].RelativeStrength20D > out[b].RelativeStrength20D
		}
		if out[a].RepairScore != out[b].RepairScore {
			return out[a].RepairScore > out[b].RepairScore
		}
		return out[a].Code < out[b].Code
	})
	return out
}

// prioritizeLowBuy 低吸候选的排序（原实现 `prioritize_trend_growth_entry_points`
// 的低吸分支，:1650-1683）。
//
// 三种模式（`low_buy_sort_mode`）：
//
//	amount_entry（默认）→ 成交额 DESC → 优先级分 ASC → 代码 ASC
//	deep_amount         → 20日回撤 ASC → 成交额 DESC → 优先级分 ASC → 代码 ASC
//	board_norm_amount   → **市场内**成交额百分位 DESC → 优先级分 ASC → 代码 ASC
//
// ⚠ 优先级分（`LowBuyEntryScore`）是**惩罚项**，排序用 **ASC**（越小越好），
// 与趋势成长的 DESC 相反。
func prioritizeLowBuy(rows []Row, cfg StrategyConfig) []Row {
	out := make([]Row, len(rows))
	copy(out, rows)

	// amount_rank = 1 - 成交额百分位（原实现 `_low_buy_entry_score_series`）
	amounts := make([]float64, len(out))
	for i := range out {
		amounts[i] = out[i].Amount
	}
	amountRanks := NewRankLookup(amounts)
	priority := make([]float64, len(out))
	for i := range out {
		amountRank := 1 - amountRanks.Pct(amounts[i])
		priority[i] = LowBuyEntryScore(out[i], cfg.LowBuyBreadthReadyCount, amountRank)
	}

	// board_norm_amount 需要"市场内"的成交额百分位
	//
	// ⚠ **按原实现的词汇分组，不是按本项目的 market key**。
	// 原实现 `frame.groupby("market")`（portfolio_rotation_logic.py:1642），
	// 而它的 market 列是 主板 / 创业板 / 科创板 —— 也就是说
	// **沪市主板与深市主板是同一个组**。
	//
	// 本项目 `market` 列是 `market.KeyOf` 的 key（sh-main / sz-main / ...），
	// 直接分组会把沪深主板拆成两组、各算各的百分位 → 候选顺序与原版不同。
	// 映射不出来时（bse / 基金）退回原始 key，至少不会把互不相干的市场并成一组。
	marketRank := make([]float64, len(out))
	if strings.EqualFold(cfg.LowBuySortMode, "board_norm_amount") {
		byMarket := map[string][]float64{}
		keys := make([]string, len(out))
		for i := range out {
			key := StockMarketOf(out[i].Market)
			if key == "" {
				key = out[i].Market
			}
			keys[i] = key
			byMarket[key] = append(byMarket[key], out[i].Amount)
		}
		// 每个市场一个查询器（市场数是几个，不是几千）
		lookupByMarket := make(map[string]*RankLookup, len(byMarket))
		for market, values := range byMarket {
			lookupByMarket[market] = NewRankLookup(values)
		}
		for i := range out {
			marketRank[i] = lookupByMarket[keys[i]].Pct(out[i].Amount)
		}
	}

	sort.SliceStable(out, func(a, b int) bool {
		switch strings.ToLower(cfg.LowBuySortMode) {
		case "deep_amount":
			if out[a].DrawdownFromHigh20 != out[b].DrawdownFromHigh20 {
				return out[a].DrawdownFromHigh20 < out[b].DrawdownFromHigh20
			}
			if out[a].Amount != out[b].Amount {
				return out[a].Amount > out[b].Amount
			}
		case "board_norm_amount":
			if marketRank[a] != marketRank[b] {
				return marketRank[a] > marketRank[b]
			}
		default: // amount_entry
			if out[a].Amount != out[b].Amount {
				return out[a].Amount > out[b].Amount
			}
		}
		if priority[a] != priority[b] {
			return priority[a] < priority[b]
		}
		return out[a].Code < out[b].Code
	})
	return out
}
