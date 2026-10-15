package valuation

import (
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 价格序列（复权口径的唯一入口）
//
// ⚠ 本文件是全包唯一允许把"价格"变成"收益"的地方。三条铁律（方案 §2.3）：
//
//  1. 收益 / 波动 / 回撤 / 区间涨幅 → **必须**用 AdjustedClose（后复权）。
//     不复权行情在除权日会凭空出现 −67% 这种"暴跌"（实测 002594 在 2025-07-29
//     因 1:3 送转由 337.00 变成 111.42），拿它算波动与回撤会得到虚假的巨幅风险。
//  2. 市值 / PE / PB / PS → **必须**用原始 RawClose × 当期股本（见 MarketCap）。
//     复权只是记账口径，市值要用真实成交价。
//  3. 成交量 / 成交额 → 保持原始，绝不随复权放大。
//
// 所以 Bar 里 **同时**保留 RawClose 与 AdjClose 两个字段，且两条取值路径
// 用不同的方法名（Return 只读 AdjClose，MarketCap 只读 RawClose）——
// 让"用错口径"在代码评审里一眼可见。
// ---------------------------------------------------------------------------

// Bar 是一根日线，同时带原始价与后复权价。
type Bar struct {
	Date   int64   // YYYYMMDD
	Open   float64 // 原始开盘
	High   float64 // 原始最高
	Low    float64 // 原始最低
	Close  float64 // 原始收盘（不复权）—— 市值口径
	Volume float64 // 原始成交量
	Amount float64 // 原始成交额
	// AdjFactor 是该日的后复权乘数 F(t)（1 表示不复权）。
	// 由服务层从数据湖 meta/adj_factors_cache 取得，本包不碰文件系统。
	AdjFactor float64
}

// AdjustedClose 返回后复权收盘价（收益/波动/回撤唯一允许用的价格）。
func (b Bar) AdjustedClose() float64 {
	if b.AdjFactor <= 0 || b.AdjFactor == 1 {
		return b.Close
	}
	return b.Close * b.AdjFactor
}

// AdjustedHigh 返回后复权最高价。
func (b Bar) AdjustedHigh() float64 {
	if b.AdjFactor <= 0 || b.AdjFactor == 1 {
		return b.High
	}
	return b.High * b.AdjFactor
}

// AdjustedLow 返回后复权最低价。
func (b Bar) AdjustedLow() float64 {
	if b.AdjFactor <= 0 || b.AdjFactor == 1 {
		return b.Low
	}
	return b.Low * b.AdjFactor
}

// PriceSeries 是一只股票在评估窗口内的价格序列（按日期升序）。
type PriceSeries struct {
	Code string
	Bars []Bar

	// Adjusted 标记这份序列是否真的应用了复权因子。
	//
	// 为 false 时（例如因子文件缺失）**必须**在结论里降置信度并写明：
	// 没有复权的区间收益在含除权的窗口里是不可信的。
	Adjusted bool
}

// NewPriceSeries 构造价格序列；bars 会按日期升序排序（不改入参切片）。
func NewPriceSeries(code string, bars []Bar) PriceSeries {
	sorted := make([]Bar, len(bars))
	copy(sorted, bars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date < sorted[j].Date })

	adjusted := false
	for _, bar := range sorted {
		if bar.AdjFactor > 0 && bar.AdjFactor != 1 {
			adjusted = true
			break
		}
	}
	return PriceSeries{Code: code, Bars: sorted, Adjusted: adjusted}
}

// Len 返回序列长度。
func (s PriceSeries) Len() int { return len(s.Bars) }

// Last 返回最后一根；空序列返回 false。
func (s PriceSeries) Last() (Bar, bool) {
	if len(s.Bars) == 0 {
		return Bar{}, false
	}
	return s.Bars[len(s.Bars)-1], true
}

// LastDate 返回最后一个交易日（空序列返回 0）。
func (s PriceSeries) LastDate() int64 {
	if bar, ok := s.Last(); ok {
		return bar.Date
	}
	return 0
}

// LastClose 返回最新**不复权**收盘价（市值口径）。
func (s PriceSeries) LastClose() float64 {
	if bar, ok := s.Last(); ok {
		return bar.Close
	}
	return 0
}

// LastAdjustedClose 返回最新后复权收盘价。
func (s PriceSeries) LastAdjustedClose() float64 {
	if bar, ok := s.Last(); ok {
		return bar.AdjustedClose()
	}
	return 0
}

// BarAtOrBefore 返回日期 <= date 的最后一根；没有返回 false。
func (s PriceSeries) BarAtOrBefore(date int64) (Bar, bool) {
	idx := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date > date })
	if idx == 0 {
		return Bar{}, false
	}
	return s.Bars[idx-1], true
}

// IndexAtOrBefore 返回日期 <= date 的最后一根的下标；没有返回 -1。
func (s PriceSeries) IndexAtOrBefore(date int64) int {
	idx := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date > date })
	return idx - 1
}

// Tailing 返回最后 n 根（n <= 0 或超过长度时返回全序列）。
func (s PriceSeries) Tailing(n int) PriceSeries {
	if n <= 0 || n >= len(s.Bars) {
		return s
	}
	out := make([]Bar, n)
	copy(out, s.Bars[len(s.Bars)-n:])
	return PriceSeries{Code: s.Code, Bars: out, Adjusted: s.Adjusted}
}

// ---------------------------------------------------------------------------
// 收益与风险
// ---------------------------------------------------------------------------

// PeriodReturn 返回最近 n 个交易日的后复权收益率；长度不足返回 false。
//
// "n 个交易日"= 今天与 n 根之前那一根的比值，与 Horizons 里的 TradingDays
// 是同一套口径（这样"12 个月历史收益"与"12 个月情景收益"才可比）。
func (s PriceSeries) PeriodReturn(n int) (float64, bool) {
	if n <= 0 || len(s.Bars) <= n {
		return 0, false
	}
	end := s.Bars[len(s.Bars)-1].AdjustedClose()
	start := s.Bars[len(s.Bars)-1-n].AdjustedClose()
	if start <= 0 {
		return 0, false
	}
	return end/start - 1, true
}

// AnnualizedVolatility 返回最近 n 个交易日、按 242 个交易日年化的收益率标准差。
//
// 为什么用日收益率的标准差而不是区间涨跌幅：区间涨跌幅只反映首尾两点，
// 一次涨停就能让它从 +5% 变 +15%，而波动率要看整条路径。
func (s PriceSeries) AnnualizedVolatility(n int) (float64, bool) {
	series := s.Tailing(n + 1)
	if len(series.Bars) < 3 {
		return 0, false
	}
	returns := make([]float64, 0, len(series.Bars)-1)
	for i := 1; i < len(series.Bars); i++ {
		prev := series.Bars[i-1].AdjustedClose()
		curr := series.Bars[i].AdjustedClose()
		if prev <= 0 || curr <= 0 {
			continue
		}
		returns = append(returns, curr/prev-1)
	}
	deviation := stddev(returns)
	if deviation == 0 && len(returns) == 0 {
		return 0, false
	}
	return deviation * math.Sqrt(tradingDaysPerYear), true
}

// MaxDrawdown 返回最近 n 个交易日内的最大回撤（正数，如 0.35 表示 −35%）。
//
// 口径：峰值 → 之后谷值的最大跌幅，**在复权收盘价序列上**算。
// 首日为峰、后续更低也算；不跨窗口取峰（否则会把"窗口前的历史高点"
// 当成今天的成本，得出一个与本次持有没有关系的回撤）。
func (s PriceSeries) MaxDrawdown(n int) (float64, bool) {
	series := s.Tailing(n)
	if len(series.Bars) < 2 {
		return 0, false
	}
	peak := series.Bars[0].AdjustedClose()
	worst := 0.0
	for _, bar := range series.Bars {
		close := bar.AdjustedClose()
		if close <= 0 {
			continue
		}
		if close > peak {
			peak = close
		}
		if peak > 0 {
			if drop := (peak - close) / peak; drop > worst {
				worst = drop
			}
		}
	}
	return worst, true
}

// TurnoverRate 返回最近 n 个交易日的日均换手率（成交额 ÷ 流通市值）。
//
// 传入的 floatMarketCap 是**当前**流通市值。用当前市值去除历史成交额是个
// 近似（历史流通市值会变），所以只用它衡量"流动性档位"，不用于任何绝对判断。
func (s PriceSeries) TurnoverRate(n int, floatMarketCap float64) (float64, bool) {
	if floatMarketCap <= 0 {
		return 0, false
	}
	series := s.Tailing(n)
	if len(series.Bars) == 0 {
		return 0, false
	}
	total := 0.0
	count := 0
	for _, bar := range series.Bars {
		if bar.Amount > 0 {
			total += bar.Amount
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return total / float64(count) / floatMarketCap * 100, true
}

// LatestTurnover 返回最新一根的换手率（成交额 ÷ 流通市值，百分数）。
func (s PriceSeries) LatestTurnover(floatMarketCap float64) (float64, bool) {
	if floatMarketCap <= 0 || len(s.Bars) == 0 {
		return 0, false
	}
	bar := s.Bars[len(s.Bars)-1]
	if bar.Amount <= 0 {
		return 0, false
	}
	return bar.Amount / floatMarketCap * 100, true
}

// DrawdownFromHigh 返回当前价相对最近 n 日最高价的回撤（正数）。
func (s PriceSeries) DrawdownFromHigh(n int) (float64, bool) {
	series := s.Tailing(n)
	if len(series.Bars) == 0 {
		return 0, false
	}
	high := 0.0
	for _, bar := range series.Bars {
		if value := bar.AdjustedHigh(); value > high {
			high = value
		}
	}
	current := series.Bars[len(series.Bars)-1].AdjustedClose()
	if high <= 0 || current <= 0 {
		return 0, false
	}
	return (high - current) / high, true
}

// tradingDaysPerYear 是波动率年化用的年交易日数（与 Horizons 的折算一致）。
const tradingDaysPerYear = 242

func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	sum := 0.0
	for _, value := range values {
		diff := value - mean
		sum += diff * diff
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

// ---------------------------------------------------------------------------
// 历史市值重建
// ---------------------------------------------------------------------------

// SharePoint 是一次股本变更（按变更日生效）。
type SharePoint struct {
	Date        int64   `json:"date"`
	TotalShares float64 `json:"totalShares"`
	FloatShares float64 `json:"floatShares"`
}

// ShareTimeline 是一只股票的股本变更时间线（按日期升序）。
type ShareTimeline []SharePoint

// NewShareTimeline 构造股本时间线（排序 + 丢弃非法行）。
func NewShareTimeline(points []SharePoint) ShareTimeline {
	out := make(ShareTimeline, 0, len(points))
	for _, point := range points {
		if point.Date <= 0 || point.TotalShares <= 0 {
			continue
		}
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// At 返回"截至 date 生效"的股本；早于第一条记录时返回 false。
//
// 语义要点：股本按**变更日生效**，所以取 Date <= date 的最后一条。
// 拿错方向（取之后的第一条）会让 2024 年的市值用上 2025 年增发后的股本。
func (t ShareTimeline) At(date int64) (SharePoint, bool) {
	idx := sort.Search(len(t), func(i int) bool { return t[i].Date > date })
	if idx == 0 {
		return SharePoint{}, false
	}
	return t[idx-1], true
}

// Latest 返回最后一条股本记录。
func (t ShareTimeline) Latest() (SharePoint, bool) {
	if len(t) == 0 {
		return SharePoint{}, false
	}
	return t[len(t)-1], true
}

// MarketCap 计算某日的总市值与流通市值。
//
// ⚠ 用**不复权**收盘价：复权只是记账口径，市值必须是真实成交价 × 当期股本。
// 这是本文件的第二条铁律，所以这里刻意不接受 Bar，而要求调用方把
// "不复权收盘价"与"股本"分别传进来，避免顺手传错一个复权价数组。
func MarketCap(rawClose float64, shares SharePoint) (total, float float64, ok bool) {
	if rawClose <= 0 || shares.TotalShares <= 0 {
		return 0, 0, false
	}
	total = rawClose * shares.TotalShares
	if shares.FloatShares > 0 {
		float = rawClose * shares.FloatShares
	}
	return total, float, true
}

// SampleValuationAt 在**给定的一组日期**上构造估值点（而不是逐日全建）。
//
// 为什么需要它：行业历史的每个时点只落在**行业快照日**上（本机申万是月末，
// 2020 起 82 个；而不是每个交易日）。用 BuildValuationSeries 会把 1400 个交易日
// 全建一遍再丢掉 95%，而它内部对每个交易日都要问一次"当时已披露的 TTM"。
//
// 语义与 BuildValuationSeries **完全一致**（同样是"不复权价 × 当期股本"作分子、
// 当时已披露的 TTM 作分母），只是把日期集合交给调用方指定：
//
//   - dates 无需排序，内部会排序与去重；
//   - 取不到股本或基本面时**跳过该日期**，不填 0（估值算错比留空危险得多）；
//   - 返回的序列按日期升序。
func SampleValuationAt(series PriceSeries, shares ShareTimeline, dates []int64,
	fundamentals func(date int64) (TTMValues, float64, bool)) []ValuationPoint {

	if len(series.Bars) == 0 || len(dates) == 0 || len(shares) == 0 {
		return nil
	}
	wanted := make([]int64, 0, len(dates))
	seen := make(map[int64]struct{}, len(dates))
	for _, date := range dates {
		if date <= 0 {
			continue
		}
		if _, dup := seen[date]; dup {
			continue
		}
		seen[date] = struct{}{}
		wanted = append(wanted, date)
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i] < wanted[j] })

	out := make([]ValuationPoint, 0, len(wanted))
	for _, date := range wanted {
		// ① 交易日必须真的有行情（停牌/未上市就没这一天的价格）
		bar, ok := series.BarAtOrBefore(date)
		if !ok || bar.Date != date {
			continue
		}
		// ② 市值：不复权价 × 当期股本
		sharePoint, ok := shares.At(date)
		if !ok {
			continue
		}
		total, floatCap, ok := MarketCap(bar.Close, sharePoint)
		if !ok {
			continue
		}
		// ③ 基本面：该日已披露的最新一期
		ttm, equity, ok := fundamentals(date)
		if !ok {
			continue
		}
		entry := ValuationPoint{
			Date:           date,
			MarketCap:      total,
			FloatMarketCap: floatCap,
		}
		if ttm.NetProfit > 0 {
			entry.PE = total / ttm.NetProfit
		}
		if equity > 0 {
			entry.PB = total / equity
		}
		if ttm.Revenue > 0 {
			entry.PS = total / ttm.Revenue
		}
		out = append(out, entry)
	}
	return out
}

// WeightedPEAt 从一组同行业的估值点算出**调和加权 PE**（总市值 ÷ 总盈利）。
//
// ⚠ 与 AggregateIndustry 的 weightedPE 同一口径 —— "先加总市值、再加总盈利、
// 最后相除"，不是成分 PE 的算术平均。但估值点里**没有盈利金额**（只有倍数），
// 所以这里反推：
//
//	总盈利 = Σ(市值 ÷ PE)，只累加 PE > 0 的（亏损公司没有 PE，也就退出分母）
//
// 这正是"亏损公司会退出分母、所以加权 PE 偏乐观"这一语义的实现点，
// 与行业聚合保持一致（对应的占比由 lossMembers 返回）。
func WeightedPEAt(points []ValuationPoint) (pe float64, members int, lossMembers int, ok bool) {
	totalCap, totalProfit := 0.0, 0.0
	for _, point := range points {
		if point.MarketCap <= 0 {
			continue
		}
		members++
		totalCap += point.MarketCap
		if point.PE > 0 {
			totalProfit += point.MarketCap / point.PE
		} else {
			lossMembers++
		}
	}
	if members == 0 || totalCap <= 0 || totalProfit <= 0 {
		return 0, members, lossMembers, false
	}
	return totalCap / totalProfit, members, lossMembers, true
}

// ValuationPoint 是自建估值序列上的一个点。
type ValuationPoint struct {
	Date int64   `json:"date"`
	PE   float64 `json:"pe"` // 总市值 ÷ TTM 归母净利润；净利润 <= 0 时为 0（不可用）
	PB   float64 `json:"pb"` // 总市值 ÷ 归母净资产
	PS   float64 `json:"ps"` // 总市值 ÷ TTM 营业收入
	// MarketCap 是总市值（元）。
	MarketCap float64 `json:"marketCap"`
	// FloatMarketCap 是流通市值（元），取不到等于 MarketCap。
	FloatMarketCap float64 `json:"floatMarketCap"`
}

// BuildValuationSeries 用价格序列 + 股本时间线 + 对每个"财报生效窗口"的
// TTM 指标，重建历史估值序列。
//
// fundamentals 按评估日给出对应报告期的 TTM 指标；调用方负责"每个交易日用它
// 当时已披露的那一期"，这就是时点正确（point-in-time）。
//
// 本函数不做任何 I/O，也不做插值：某个交易日取不到基本面就不产生该日的估值点
// （而不是填 0）。**估值算错比留空危险得多**。
func BuildValuationSeries(series PriceSeries, shares ShareTimeline, fundamentals func(date int64) (TTMValues, float64, bool)) []ValuationPoint {
	out := make([]ValuationPoint, 0, len(series.Bars))
	for _, bar := range series.Bars {
		// ① 市值：不复权价 × 当期股本
		point, ok := shares.At(bar.Date)
		if !ok {
			continue
		}
		total, floatCap, ok := MarketCap(bar.Close, point)
		if !ok {
			continue
		}
		// ② 基本面：该交易日已披露的最新一期
		ttm, equity, ok := fundamentals(bar.Date)
		if !ok {
			continue
		}
		entry := ValuationPoint{
			Date:           bar.Date,
			MarketCap:      total,
			FloatMarketCap: floatCap,
		}
		if ttm.NetProfit > 0 {
			entry.PE = total / ttm.NetProfit
		}
		if equity > 0 {
			entry.PB = total / equity
		}
		if ttm.Revenue > 0 {
			entry.PS = total / ttm.Revenue
		}
		out = append(out, entry)
	}
	return out
}

// ---------------------------------------------------------------------------
// 分位
// ---------------------------------------------------------------------------

// Percentile 返回 value 在 samples 中的分位（0~100）。
//
// 口径：**含等值的中位排名**（"当前值在历史里比多少比例的样本更低"）。
// 这样 PE 分位 20 读作"比历史上 80% 的时间便宜"，与界面文案一致。
//
// samples 为空的返回 (0,false)；样本数 < minSamples 时返回 (0,false) ——
// 样本太少的分位没有意义（3 个点的"分位"只是排序）。
func Percentile(samples []float64, value float64, minSamples int) (float64, bool) {
	valid := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample > 0 && !math.IsNaN(sample) && !math.IsInf(sample, 0) {
			valid = append(valid, sample)
		}
	}
	if len(valid) < minSamples || len(valid) == 0 {
		return 0, false
	}
	below := 0
	equal := 0
	for _, sample := range valid {
		switch {
		case sample < value:
			below++
		case sample == value:
			equal++
		}
	}
	return (float64(below) + 0.5*float64(equal)) / float64(len(valid)) * 100, true
}

// Winsorize 把样本缩尾到 [lower, upper] 分位（返回新切片）。
//
// 为什么不直接删异常值：删掉会把"极端公司"从行业聚合里彻底抹掉，
// 让行业中位数看起来永远很稳。缩尾保留了它们的存在与方向，只限制幅度。
func Winsorize(samples []float64, lower, upper float64) []float64 {
	if len(samples) == 0 {
		return nil
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	low := quantileSorted(sorted, lower)
	high := quantileSorted(sorted, upper)
	out := make([]float64, len(samples))
	for i, value := range samples {
		if value < low {
			value = low
		}
		if value > high {
			value = high
		}
		out[i] = value
	}
	return out
}

// Median 返回样本中位数；空样本返回 0。
func Median(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	return quantileSorted(sorted, 0.5)
}

// Quantile 返回样本的分位值（q 取 0~1）。
func Quantile(samples []float64, q float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	return quantileSorted(sorted, q)
}

// quantileSorted 在**已排序**切片上做线性插值分位。
func quantileSorted(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	position := q * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}
