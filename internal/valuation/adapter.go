package valuation

import "sort"

// ---------------------------------------------------------------------------
// 服务层适配用的纯函数
//
// 这三个函数存在的理由都一样：**把"什么时候用哪个数"做成可单测的纯函数**，
// 而不是散在服务层里的一段循环。服务层只负责取数，口径判断留在这里。
// ---------------------------------------------------------------------------

// AdjScaleAt 在复权因子阶梯上求某交易日的乘数（后复权口径）。
//
// 这是对 `cnlake.adjScaleAt` 的**再导出**：服务层拿到的因子是 cnlake 的类型，
// 而"某一天该乘多少"是个纯计算，放在纯计算包里才能脱离文件系统单测。
//
// 语义：取最后一个 Date <= date 的因子；该日之前没有记录时返回 1（不复权）。
func AdjScaleAt(points []AdjFactorPoint, date int64) float64 {
	if len(points) == 0 || date <= 0 {
		return 1
	}
	index := -1
	for i := range points {
		if points[i].Date <= date {
			index = i
			continue
		}
		break
	}
	if index < 0 {
		return 1
	}
	factor := points[index].Factor
	if factor <= 0 {
		return 1
	}
	return factor
}

// FundamentalsSchedule 返回一个"按交易日取当时已披露 TTM"的回调。
//
// 这是历史估值序列重建的**时点正确性**所在：序列上的每个交易日都必须用它
// **当时能看到**的那一期财报，而不是最新一期。用最新一期会把未来的业绩
// 提前泄露到历史 PE 上（也就是"前视偏差"），重建出来的分位会系统性失真。
//
// 实现：把"可见报告期"precompute 成**阶梯函数**，而不是每次现算。
//
// "当时已披露的那一期"只会在**公告日**上跳变，一只股票一年约 4 次、6 年约 24 次。
// 所以先把全部公告日升序去重、每个跳变点算一次 `BuildAsOfFrom`，
// 查询时二分找到"最后一个 ≤ date 的状态"直接返回。
//
// ⚠ 这一步是性能关键，不是微优化：行业历史序列要对 5000+ 只标的 × 80 个快照日
// 采样，若每次查询都现算（`BuildAsOfFrom` 要遍历 21 个科目、每个科目再扫报告期
// 求 TTM），实测会让一次全市场评估从 ~25 秒涨到 **134 秒**。
// 预计算之后每只标的只付"公告日数"（约 24）次计算，而不是"交易日数"（约 1400）次。
func FundamentalsSchedule(full AsOf) func(date int64) (TTMValues, float64, bool) {
	reports := full.Reports
	if len(reports) == 0 {
		return func(int64) (TTMValues, float64, bool) { return TTMValues{}, 0, false }
	}

	// ① 全部公告日（升序去重）—— 状态只在这些日期上跳变
	announces := make([]int64, 0, len(reports))
	seen := make(map[int64]struct{}, len(reports))
	for _, report := range reports {
		if _, dup := seen[report.AnnounceDate]; dup {
			continue
		}
		seen[report.AnnounceDate] = struct{}{}
		announces = append(announces, report.AnnounceDate)
	}
	sort.Slice(announces, func(i, j int) bool { return announces[i] < announces[j] })

	// ② 每个跳变点预计算一次状态。
	//
	// reports 已按报告期升序，所以"截至 announce 的前缀"可以一次线性推进出来，
	// 不需要每个 announce 都重扫一遍全部报告期。
	type state struct {
		announce int64
		ttm      TTMValues
		equity   float64
		ok       bool
	}
	states := make([]state, 0, len(announces))
	cursor := 0
	for _, announce := range announces {
		for cursor < len(reports) && reports[cursor].AnnounceDate <= announce {
			cursor++
		}
		entry := state{announce: announce}
		if cursor > 0 {
			asOf := BuildAsOfFrom(reports[:cursor], announce)
			entry.ttm = asOf.TTM
			entry.equity = asOf.Items[ItemTotalEquity]
			entry.ok = asOf.TTM.NetProfit != 0 || asOf.TTM.Revenue != 0 || entry.equity > 0
		}
		states = append(states, entry)
	}

	return func(date int64) (TTMValues, float64, bool) {
		if date <= 0 || len(states) == 0 {
			return TTMValues{}, 0, false
		}
		index := sort.Search(len(states), func(i int) bool { return states[i].announce > date })
		if index == 0 {
			return TTMValues{}, 0, false
		}
		entry := states[index-1]
		return entry.ttm, entry.equity, entry.ok
	}
}

// ApplyIndustryEnvironment 把行业估值环境修正重新应用到一份合理价值上。
//
// 为什么需要单独一步：合理价值第一遍算的时候行业还没聚合出来（行业聚合要用
// 全部个股的市值与盈利）。方案要求"行业估值也要参与多期限风险收益分析"，
// 所以第二遍拿到行业结果后必须**重算**，而不是留一个"未做行业修正"的值。
//
// 重算的方式是完整重跑 EvaluateFairValue 并把 industry 与 IndustryAvailable
// 传进去 —— 这样：
//   - 行业修正只做一次（不会在已修正的值上再乘一次）；
//   - 冲突检测、置信度、关键假设都跟着更新。
func ApplyIndustryEnvironment(previous FairValue, company CompanyValuation, asOf AsOf,
	growth Growth, quality Quality, industry IndustryValuation, params Params,
	horizon Horizon, series PriceSeries) FairValue {

	if previous.Price <= 0 || previous.Shares <= 0 {
		return previous
	}
	return EvaluateFairValue(FairValueInput{
		Company:           company,
		AsOf:              asOf,
		Price:             previous.Price,
		Shares:            previous.Shares,
		Growth:            growth,
		Quality:           quality,
		Industry:          industry,
		IndustryAvailable: industry.Available,
		// 市场 PE 锚：行业 PE 中位数。行业成分不足时为 0，
		// 由调用方在传入前回落到全市场中位数（见 service 侧的 peAnchor）。
		PEAnchor:    industry.MedianPE,
		Horizon:     horizon,
		Params:      params,
		PriceSeries: series,
	})
}

// AdjFactorPoint 是复权因子阶梯上的一个点。
//
// 与 `cnlake.AdjFactorPoint` **同形同字段名**：服务层把 cnlake 的阶梯直接传进来
// 即可（Go 的结构体字面量赋值不需要转换代码），而"某一天乘多少"这个口径判断
// 留在纯计算包里，可以脱离文件系统单测。
type AdjFactorPoint struct {
	Date   int64
	Factor float64
}

// HorizonByTradingDays 按交易日数找期限（供"给定一个交易日数，用哪一档参数"）。
func HorizonByTradingDays(days int) (Horizon, bool) {
	for _, horizon := range Horizons {
		if horizon.TradingDays == days {
			return horizon, true
		}
	}
	return Horizon{}, false
}

// FilterByDateRange 从估值序列里取出 [from, to] 区间的点（to<=0 表示不限）。
//
// 服务层在行业历史重建等场景下需要切片，把它做成纯函数避免各调用点各写一遍。
func FilterByDateRange(series []ValuationPoint, from, to int64) []ValuationPoint {
	out := make([]ValuationPoint, 0, len(series))
	for _, point := range series {
		if from > 0 && point.Date < from {
			continue
		}
		if to > 0 && point.Date > to {
			continue
		}
		out = append(out, point)
	}
	return out
}
