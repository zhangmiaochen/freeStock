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

// industryHistoryState 是增量累积行业历史加权 PE 的状态。
//
// 为什么做成"可以一份一份喂"：行业历史的成分来源分两批
// （未评分标的的裸快照 + 评分后的充实快照），如果等全部合并完再算，
// 就得把 5781 份快照连价格序列一起留在内存里。增量累积只需要一个
// 按 (行业, 日期) 索引的小累加器，内存是常数级的。
type industryHistoryState struct {
	industryOf map[string]string
	dates      []int64
	acc        map[string]map[int64]*historySlot
}

// historySlot 是某个行业在某个快照日上的累加值。
//
// ⚠ 这里累加的是**市值与盈利金额**（不是 PE 倍数），最后才相除：
// 行业加权 PE 的口径是"Σ市值 ÷ Σ盈利"，等价于用盈利加权的调和平均。
// 若改成"先算各成分 PE 再平均"，亏损公司会被算成 PE=0 参与平均，
// 结果与"亏损公司退出分母"的语义相反，且会被极端倍数带偏。
type historySlot struct {
	cap     float64
	profit  float64
	members int
}

// newIndustryHistoryState 建立增量累积状态。
//
// system 为空或没有可用快照日时返回 nil，调用方据此跳过整个重建。
func (s *StockService) newIndustryHistoryState(data *valuationDataset, system string, groups map[string][]string) *industryHistoryState {
	if system == "" || len(groups) == 0 {
		return nil
	}
	// 只保留**行情里真的存在**的快照日：申万快照是月末，而月末有时是
	// 周末/节假日（本机 69 个里对不上 5 个）。那些日子取不到收盘价，直接跳过 ——
	// 不是"算了个 0"。
	dates := data.lake.IndustryMembersByDateRange(system, valuationPriceStart, data.evalDate)
	if len(dates) == 0 {
		return nil
	}
	industryOf := make(map[string]string, 8192)
	for industry, codes := range groups {
		for _, code := range codes {
			industryOf[code] = industry
		}
	}
	return &industryHistoryState{
		industryOf: industryOf,
		dates:      dates,
		acc:        make(map[string]map[int64]*historySlot, len(groups)),
	}
}

// add 把一份公司快照纳入它所属行业的全部快照日。
//
// 只用**不复权口径**：市值 = 不复权价 × 该日生效股本，盈利 = 该日已披露的 TTM。
// 这正是行业加权 PE 的定义（见文件头的说明），因此**不需要复权因子** ——
// 这一点很关键：全市场 5781 只都读因子文件要多花 2.5~4.6 秒。
func (state *industryHistoryState) add(data *valuationDataset, snapshot companySnapshot, params valuation.Params) {
	if state == nil {
		return
	}
	industry, ok := state.industryOf[snapshot.code]
	if !ok || snapshot.ids.AsOf.LatestPeriod == "" {
		return
	}
	// 行业历史只需要「不复权价」与「当时已披露的 TTM」，**不需要复权因子**。
	//
	// 所以这里刻意不走 `snapshot.series`：轻量画像（profileCompany）根本没有
	// 构造价格序列（那是 1400 个 Bar 结构体），而它才是全市场绝大多数标的走的路径。
	// 直接读 data.bars 里的原始行，零额外开销。
	rawBars := data.bars[snapshot.code]
	if len(rawBars) == 0 {
		return
	}
	fundamentals := valuation.FundamentalsSchedule(snapshot.ids.AsOf)
	for _, date := range state.dates {
		// ① 该日必须有行情（停牌/未上市就没有这一天的价格）
		index := sort.Search(len(rawBars), func(i int) bool { return rawBars[i].Date() > date })
		if index == 0 {
			continue
		}
		bar := rawBars[index-1]
		if bar.Date() != date {
			continue
		}
		// ② 该日必须有生效股本
		sharePoint, ok := snapshot.shares.At(date)
		if !ok {
			continue
		}
		// ③ 市值 = 不复权价 × 当期股本（第二条铁律：绝不用复权价算市值）
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

		byDate := state.acc[industry]
		if byDate == nil {
			byDate = map[int64]*historySlot{}
			state.acc[industry] = byDate
		}
		slot := byDate[date]
		if slot == nil {
			slot = &historySlot{}
			byDate[date] = slot
		}
		slot.cap += total
		slot.members++
		if ttm.NetProfit > 0 {
			slot.profit += ttm.NetProfit
		}
	}
}

// finish 把累加器整理成每个行业的历史序列。
func (state *industryHistoryState) finish(params valuation.Params) map[string]industryHistoryResult {
	out := make(map[string]industryHistoryResult)
	if state == nil {
		return out
	}
	for industry, byDate := range state.acc {
		result := industryHistoryResult{SnapshotDays: len(state.dates)}
		industryDates := make([]int64, 0, len(byDate))
		for date := range byDate {
			industryDates = append(industryDates, date)
		}
		sort.Slice(industryDates, func(i, j int) bool { return industryDates[i] < industryDates[j] })
		for _, date := range industryDates {
			slot := byDate[date]
			// 成分太少的日子不给值：3 只票的"行业加权 PE"不是行业估值
			if slot.members < params.IndustryMinMembers || slot.profit <= 0 || slot.cap <= 0 {
				continue
			}
			result.Series = append(result.Series, slot.cap/slot.profit)
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
		"快照日数", len(state.dates), "有序列的行业", used, "行业总数", len(out))
	return out
}

// industrySnapshotSpan 返回某体系在区间内的快照日（供置信度与说明文案）。
func industrySnapshotSpan(lake *cnlake.Lake, system string, from, to int64) []int64 {
	return lake.IndustryMembersByDateRange(system, from, to)
}

var _ = time.Now
