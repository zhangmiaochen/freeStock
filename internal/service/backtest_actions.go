package service

import (
	"fmt"

	"freestock/internal/backtest"
	"freestock/internal/cnlake"
)

// buildBacktestActions 把数据湖的公司行为转成回测引擎的"按除权日索引"表。
//
// 为什么需要：复权只解决**指标**的正确性（价格序列连续）。只复权价格、不复权股数，
// 会让跨越除权日的持仓在**名义金额**上算错（收益率仍正确，因为复权序列本身是总收益序列）。
// 注入之后：送转调整股数并摊薄成本、现金分红进现金账户，名义金额与券商一致。
//
// 只取 [start, end] 区间内的事件 —— 区间外的除权不影响本次回测账本。
func buildBacktestActions(lake *cnlake.Lake, start, end int) map[string][]backtest.CorporateAction {
	out := map[string][]backtest.CorporateAction{}
	if lake == nil || !lake.HasCorporateActions() {
		return out
	}
	for date, list := range lake.ActionsByDate(int64(start), int64(end)) {
		key := fmt.Sprintf("%d", date) // YYYYMMDD，与回测里的交易日字符串一致
		items := make([]backtest.CorporateAction, 0, len(list))
		for _, a := range list {
			code := cnlake.BareSymbol(a.Symbol)
			if code == "" {
				continue
			}
			items = append(items, backtest.CorporateAction{
				Code:         code,
				ExDate:       key,
				CashDividend: a.CashPerShare(),
				ShareRatio:   a.ShareMultiplier(),
			})
		}
		if len(items) > 0 {
			out[key] = append(out[key], items...)
		}
	}
	return out
}

// shouldInjectCorporateActions 判断这次回测是否应该把公司行为注入引擎账本。
//
// 只有**原始价**口径需要：那时价格在除权日会跳空，必须靠送转扩股 / 现金分红
// 把股数与现金调整回来。复权口径（hfq/qfq）下价格序列已经连续、本身就是总收益
// 序列，再注入一次就会把同一笔分红/送转算两遍（实测 1 送 1 让总资产翻倍）。
func shouldInjectCorporateActions(mode cnlake.AdjMode, hasActions bool) bool {
	return hasActions && mode == cnlake.AdjNone
}
