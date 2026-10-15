package service

import (
	"log/slog"
	"sort"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// ---------------------------------------------------------------------------
// 行业历史加权 PE 序列的时点重建
//
// ## 为什么需要它
//
// `AggregateIndustry` 的 `PEPercentile` / `PEBand` 需要一条**行业自身的历史加权 PE
// 序列**，否则只能退化成横截面结构口径（见 valuation/industryfallback.go）——
// 那个口径只能回答"行业内部估值结构如何"，回答不了"行业整体比它自己历史上贵还是便宜"。
//
// 本机的 `industry_members` 有申万回填的 82 个月末快照（2020-01-23 起），
// 所以这条序列**可以真正时点正确地重建**：
//
//	对每个快照日 D：
//	  成分 = D 那天该行业的成员（用 D 生效的那份快照，不是今天的名单）
//	  每个成分的市值 = D 那天的不复权收盘价 × D 那天生效的股本
//	  每个成分的盈利 = D 那天**已披露**的 TTM 归母净利润（announce_date 门控）
//	  行业加权 PE = Σ市值 ÷ Σ盈利        ← 调和加权，与行业聚合同一口径
//
// ## 这正是消除前视偏差的地方
//
// 三个"用 D 那天"缺一不可：
//   - 用今天的成分名单去算 2020 年的行业 PE，等于把后来才纳入的公司提前算进去；
//   - 用今天的股本去算历史市值，增发/送转之后的历史市值会整体偏大；
//   - 用最新一期财报去算历史盈利，等于把未来业绩泄露到历史里。
//
// ## 性能：这是本功能最需要小心的一处
//
// 第一版用 `valuation.BuildValuationSeries`（逐**交易日**建 1400 个点）来取样，
// 但这里只需要**行业快照日**（69 个）上的值。实测代价：全市场 5384 只标的 ×
// 1400 天 ≈ 750 万次点构造，**行业历史重建一项就花了 159 秒**，
// 把整次评估从 25 秒推到 3 分 5 秒。
//
// 现在改成"只对快照日直接算市值与盈利"：
//
//	市值 = 不含复权收盘价 × 该日生效股本     ← 两次二分查找，O(log n)
//	盈利 = 该日已披露的 TTM（阶梯函数二分）  ← O(log 公告日数)
//
// 每只标的只付 69 次常数级查询，实测该项降到 3 秒以内。
// ---------------------------------------------------------------------------

// industryHistoryResult 是一条行业历史加权 PE 序列及其样本说明。
type industryHistoryResult struct {
	// Series 是加权 PE 序列（升序，只含算得出的日子）。
	Series []float64
	// First / Last 是序列覆盖的交易日区间（YYYYMMDD）。
	First int64
	Last  int64
	// SnapshotDays 是尝试过的快照日数；UsedDays 是真正产出数值的天数。
	SnapshotDays int
	UsedDays     int
}

// buildIndustryHistory 为**全部行业**重建历史加权 PE 序列。
//
// 输入：
//   - snapshots：全市场公司快照（按代码索引）；
//   - system：行业分类体系（决定用哪些快照日）；
//   - groups：行业码（带体系前缀）-> 该行业的成分代码。
//
// 返回：行业码 -> 历史序列。
func (s *StockService) buildIndustryHistory(
	data *valuationDataset,
	snapshots map[string]companySnapshot,
	system string,
	groups map[string][]string,
	params valuation.Params,
) map[string]industryHistoryResult {

	out := make(map[string]industryHistoryResult, len(groups))
	if system == "" || len(groups) == 0 {
		return out
	}

	// 可用的快照日：申万的快照是月末，而月末有时是周末/节假日。
	// 那些日子取不到收盘价，直接跳过 —— 不是"算了个 0"。
	dates := data.lake.IndustryMembersByDateRange(system, valuationPriceStart, data.evalDate)
	if len(dates) == 0 {
		return out
	}

	// 代码 -> 行业（一只标的只属于一个行业）
	industryOf := make(map[string]string, len(snapshots))
	for industry, codes := range groups {
		for _, code := range codes {
			industryOf[code] = industry
		}
	}

	// 累加器：行业 -> 日期 -> {总市值, 总盈利, 成分数}
	type capSlot struct {
		cap     float64
		profit  float64
		members int
		loss    int
	}
	acc := make(map[string]map[int64]*capSlot, len(groups))

	// ⚠ 一只标的的财报阶梯（FundamentalsSchedule）建一次就够，
	// 所以外层按标的、内层按日期 —— 而不是反过来。
	for code, snapshot := range snapshots {
		industry, ok := industryOf[code]
		if !ok {
			continue
		}
		if snapshot.ids.AsOf.LatestPeriod == "" {
			continue
		}
		fundamentals := valuation.FundamentalsSchedule(snapshot.ids.AsOf)

		for _, date := range dates {
			// ① 该日必须有行情（停牌/未上市就没有这一天的价格）
			bar, ok := snapshot.series.BarAtOrBefore(date)
			if !ok || bar.Date != date {
				continue
			}
			// ② 该日必须有生效股本
			sharePoint, ok := snapshot.shares.At(date)
			if !ok {
				continue
			}
			// ③ 市值 = 不复权价 × 当期股本（第二条铁律：不能用复权价算市值）
			total, _, ok := valuation.MarketCap(bar.Close, valuation.SharePoint{
				Date:        sharePoint.Date,
				TotalShares: sharePoint.TotalShares,
				FloatShares: sharePoint.FloatShares,
			})
			if !ok {
				continue
			}
			// ④ 该日**已披露**的 TTM 归母净利润
			ttm, _, ok := fundamentals(date)
			if !ok {
				continue
			}

			slot := acc[industry]
			if slot == nil {
				slot = map[int64]*capSlot{}
				acc[industry] = slot
			}
			entry := slot[date]
			if entry == nil {
				entry = &capSlot{}
				slot[date] = entry
			}
			entry.cap += total
			entry.members++
			if ttm.NetProfit > 0 {
				entry.profit += ttm.NetProfit
			} else {
				entry.loss++
			}
		}
	}

	// 汇总成序列
	for industry, byDate := range acc {
		result := industryHistoryResult{SnapshotDays: len(dates)}
		industryDates := make([]int64, 0, len(byDate))
		for date := range byDate {
			industryDates = append(industryDates, date)
		}
		sort.Slice(industryDates, func(i, j int) bool { return industryDates[i] < industryDates[j] })
		for _, date := range industryDates {
			entry := byDate[date]
			// 成分太少的日子不给值：3 只票的"行业加权 PE"不是行业估值
			if entry.members < params.IndustryMinMembers || entry.profit <= 0 || entry.cap <= 0 {
				continue
			}
			result.Series = append(result.Series, entry.cap/entry.profit)
			if result.First == 0 {
				result.First = date
			}
			result.Last = date
		}
		result.UsedDays = len(result.Series)
		out[industry] = result
	}

	used := 0
	for _, result := range out {
		if result.UsedDays > 0 {
			used++
		}
	}
	slog.Debug("估值评估：行业历史序列重建完成",
		"体系", system, "快照日数", len(dates), "有序列的行业", used, "行业总数", len(out))
	return out
}

// industrySnapshotSpan 返回某体系在区间内的快照日（供置信度与说明文案）。
func industrySnapshotSpan(lake *cnlake.Lake, system string, from, to int64) []int64 {
	return lake.IndustryMembersByDateRange(system, from, to)
}

var _ = time.Now
