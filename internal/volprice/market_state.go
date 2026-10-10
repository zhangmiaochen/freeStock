package volprice

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// 对应 `volume_price_market_state.py`。

// MarketStates / MarketStateLabels 对应原实现的常量。
var MarketStates = []string{"bull", "range", "bear"}

var MarketStateLabels = map[string]string{
	"bull":  "牛市/趋势扩张",
	"range": "震荡/轮动",
	"bear":  "熊市/风险释放",
}

// NormalizeMarketState 对应 `normalize_market_state`。
//
// def == "" 时无法识别返回空串（这是原实现里 `normalize_market_state(x, default="")`
// 的用法，用来区分"没指定"与"震荡"）。
func NormalizeMarketState(value string, def string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	aliases := map[string]string{
		"牛市": "bull", "趋势": "bull", "趋势扩张": "bull", "bull": "bull",
		"震荡": "range", "震荡市": "range", "range": "range",
		"熊市": "bear", "风险": "bear", "风险释放": "bear", "bear": "bear",
	}
	if key, ok := aliases[s]; ok {
		return key
	}
	if def == "" {
		return ""
	}
	for _, state := range MarketStates {
		if state == def {
			return def
		}
	}
	return "range"
}

// MarketStateMetrics 对应原实现 metrics 子对象。
type MarketStateMetrics struct {
	ProxyRet20     float64
	UpRatio5       float64
	DownRatio5     float64
	AmountGrowth5  float64
	HigherStruct   bool
	LowerStructure bool
}

// MarketStateInfo 对应原实现返回的市场状态字典。
type MarketStateInfo struct {
	State     string
	Label     string
	Score     float64
	Source    string
	Reason    string
	TradeDate string
	// HasMetrics 标记是否带了 metrics（原实现只有自动识别成功时才有）
	HasMetrics bool
	Metrics    MarketStateMetrics
}

// MarketDailyRow 是"全市场日线"的一行（宽度的原始输入）。
//
// 对应原实现传进 `build_market_width_daily` 的 DataFrame 的列子集。
type MarketDailyRow struct {
	TradeDate string
	Close     float64
	PreClose  float64
	// PctChg 允许是 NaN（原实现里该单元格缺失，mean/count 都会跳过它）
	PctChg float64
	Amount float64
}

// MarketDailyFrame 对应传进 `build_market_width_daily` 的 DataFrame。
//
// ⚠ 这两个 Bool 是**帧级**的，不是行级 —— 这是最容易搞错的地方。
// 原实现判的是"**这一列**不存在或**整列**全是 NaN"：
//
//	if "pct_chg" not in work.columns or work["pct_chg"].isna().all():
//	    work["pct_chg"] = (close / pre_close - 1.0) * 100.0   # 或 0.0
//
// 而**单个单元格**的 NaN 是**保留**的：`mean` 跳过它、`count` 不数它。
// 把"某一行缺失"误当成"整列缺失"去补算，会把 NaN 填成一个具体值 ——
// 于是 `avg_pct` 与 `count` 都偏，整个市场状态判定跟着偏。
type MarketDailyFrame struct {
	Rows []MarketDailyRow
	// HasPctChgColumn 表示 pct_chg 列存在且**不是全 NaN**
	HasPctChgColumn bool
	// HasAmountColumn 表示 amount 列存在
	HasAmountColumn bool
}

// WidthDaily 是"市场宽度日线"（`build_market_width_daily` 的输出）。
type WidthDaily struct {
	TradeDate string
	AvgPct    float64
	UpRatio   float64
	DownRatio float64
	Amount    float64
	Count     float64
}

// widthRowFromGroup 把一个日期的所有行聚合成一行宽度（对应 groupby.agg）。
func widthRowFromGroup(date string, rows []MarketDailyRow, derivePctChg, useAmount bool) WidthDaily {
	out := WidthDaily{TradeDate: date}
	pctValues := make([]float64, 0, len(rows))
	amountValues := make([]float64, 0, len(rows))
	upCount, downCount, nonNaNCount := 0, 0, 0
	for _, row := range rows {
		pct := row.PctChg
		if derivePctChg {
			if row.PreClose != 0 {
				pct = (row.Close/row.PreClose - 1.0) * 100.0
			} else {
				pct = 0.0
			}
		}
		// `(pct_chg > 0).mean()` 的分母是**整组行数**，NaN 记 False
		if pct > 0 {
			upCount++
		}
		if pct < 0 {
			downCount++
		}
		if !isNaN(pct) {
			nonNaNCount++
			pctValues = append(pctValues, pct)
		}
		if useAmount {
			amountValues = append(amountValues, row.Amount)
		}
	}
	total := len(rows)
	out.Count = float64(nonNaNCount)
	if total > 0 {
		out.UpRatio = float64(upCount) / float64(total)
		out.DownRatio = float64(downCount) / float64(total)
	}
	out.AvgPct = meanNonNaN(pctValues)
	if useAmount {
		out.Amount = meanNonNaN(amountValues)
	} else {
		// 原实现：没有 amount 列时退回 `("pct_chg","count")`
		out.Amount = out.Count
	}
	return out
}

// BuildMarketWidthDaily 对应 `build_market_width_daily`。
func BuildMarketWidthDaily(frame MarketDailyFrame) []WidthDaily {
	derivePctChg := !frame.HasPctChgColumn || allPctChgMissing(frame.Rows)
	byDate := map[string][]MarketDailyRow{}
	for _, row := range frame.Rows {
		date := TrimDate(row.TradeDate)
		if date == "" {
			continue
		}
		row.TradeDate = date
		byDate[date] = append(byDate[date], row)
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	out := make([]WidthDaily, 0, len(dates))
	for _, date := range dates {
		out = append(out, widthRowFromGroup(date, byDate[date], derivePctChg, frame.HasAmountColumn))
	}
	return out
}

// allPctChgMissing 对应 `work["pct_chg"].isna().all()`。
func allPctChgMissing(rows []MarketDailyRow) bool {
	for _, row := range rows {
		if !isNaN(row.PctChg) {
			return false
		}
	}
	return true
}

// meanNonNaN 是 pandas `Series.mean()`（跳过 NaN；全 NaN 时为 NaN）。
func meanNonNaN(values []float64) float64 {
	sum, count := 0.0, 0
	for _, v := range values {
		if isNaN(v) {
			continue
		}
		sum += v
		count++
	}
	if count == 0 {
		return nan()
	}
	return sum / float64(count)
}

// DetectMarketStateFromWidth 对应 `detect_market_state_from_width`。
func DetectMarketStateFromWidth(width []WidthDaily, currentDate, manualState string) MarketStateInfo {
	if manual := NormalizeMarketState(manualState, ""); manual != "" {
		return manualStateInfo(manual, currentDate)
	}
	td := TrimDate(currentDate)
	if len(width) == 0 {
		return rangeStateInfo(td, "本地市场宽度数据不足，按震荡处理")
	}
	// 只保留 <= td 的行，并按日期升序
	filtered := make([]WidthDaily, 0, len(width))
	for _, row := range width {
		date := TrimDate(row.TradeDate)
		if td != "" && date > td {
			continue
		}
		row.TradeDate = date
		filtered = append(filtered, row)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].TradeDate < filtered[j].TradeDate })
	if len(filtered) == 0 {
		return rangeStateInfo(td, "指定日期前无市场宽度数据，按震荡处理")
	}
	if len(filtered) > 80 {
		filtered = filtered[len(filtered)-80:]
	}
	return detectFromPreparedWidth(filtered, td)
}

// BuildMarketStateCache 对应 `build_market_state_cache`。
func BuildMarketStateCache(frame MarketDailyFrame, tradeDates []string) map[string]MarketStateInfo {
	return buildMarketStateCacheFromWidth(BuildMarketWidthDaily(frame), tradeDates)
}

// BuildMarketStateCacheFromWidth 是宽度已经是现成列时的入口
// （原实现里 `volume_price_service` 直接传宽度帧的那条路径）。
func BuildMarketStateCacheFromWidth(width []WidthDaily, tradeDates []string) map[string]MarketStateInfo {
	return buildMarketStateCacheFromWidth(width, tradeDates)
}

func buildMarketStateCacheFromWidth(width []WidthDaily, tradeDates []string) map[string]MarketStateInfo {
	out := map[string]MarketStateInfo{}
	if len(width) == 0 {
		return out
	}
	sorted := append([]WidthDaily(nil), width...)
	for i := range sorted {
		sorted[i].TradeDate = TrimDate(sorted[i].TradeDate)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TradeDate < sorted[j].TradeDate })

	seen := map[string]bool{}
	dates := make([]string, 0, len(tradeDates))
	for _, item := range tradeDates {
		value := TrimDate(item)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		dates = append(dates, value)
	}
	sort.Strings(dates)

	for _, td := range dates {
		// `searchsorted(td, side="right")` = 第一个 > td 的位置 = <= td 的个数
		end := sort.Search(len(sorted), func(i int) bool { return sorted[i].TradeDate > td })
		if end <= 0 {
			out[td] = rangeStateInfo(td, "指定日期前无市场宽度数据，按震荡处理")
			continue
		}
		start := end - 80
		if start < 0 {
			start = 0
		}
		out[td] = detectFromPreparedWidth(sorted[start:end], td)
	}
	return out
}

// DetectStockContextState 对应 `detect_stock_context_state_prepared`：
// 用**该股自己的收盘价**当"宽度"，判断它此刻处于哪种市场状态。
//
// 这就是回测里逐根 K 线用的状态来源（原实现 `run_volume_price_backtest`
// 的 `detect_stock_context_state(visible, current_date=...)`）。
func DetectStockContextState(bars []Bar, currentDate, manualState string) MarketStateInfo {
	if manual := NormalizeMarketState(manualState, ""); manual != "" {
		return manualStateInfo(manual, currentDate)
	}
	td := TrimDate(currentDate)
	if len(bars) == 0 {
		return rangeStateInfo(td, "本地市场宽度数据不足，按震荡处理")
	}
	work := make([]Bar, 0, len(bars))
	for _, bar := range bars {
		date := TrimDate(bar.Date)
		if date == "" || isNaN(bar.Close) {
			continue
		}
		if td != "" && date > td {
			continue
		}
		bar.Date = date
		work = append(work, bar)
	}
	if len(work) == 0 {
		return rangeStateInfo(td, "本地市场宽度数据不足，按震荡处理")
	}
	sort.SliceStable(work, func(i, j int) bool { return work[i].Date < work[j].Date })

	width := make([]WidthDaily, 0, len(work))
	for i, bar := range work {
		pct := 0.0
		if i > 0 && work[i-1].Close != 0 {
			pct = (bar.Close/work[i-1].Close - 1.0) * 100.0
		}
		row := WidthDaily{TradeDate: bar.Date, AvgPct: pct, Amount: 1.0, Count: 1}
		if pct > 0 {
			row.UpRatio = 1.0
		}
		if pct < 0 {
			row.DownRatio = 1.0
		}
		width = append(width, row)
	}
	last := td
	if last == "" {
		last = work[len(work)-1].Date
	}
	return detectFromPreparedWidth(width, last)
}

// detectFromPreparedWidth 对应 `_detect_market_state_from_prepared_width`。
//
// ⚠ **不做日期过滤、不做 80 日截断** —— 窗口由调用方切好（原实现如此）。
func detectFromPreparedWidth(grouped []WidthDaily, currentDate string) MarketStateInfo {
	if len(grouped) == 0 {
		return rangeStateInfo(TrimDate(currentDate), "本地市场宽度数据不足，按震荡处理")
	}
	if len(grouped) < 20 {
		return rangeStateInfo(grouped[len(grouped)-1].TradeDate, "市场宽度样本少于20个交易日，按震荡处理")
	}

	// proxy_close = (1 + avg_pct.fillna(0)/100).cumprod()
	proxyClose := make(Series, len(grouped))
	acc := 1.0
	for i, row := range grouped {
		avg := row.AvgPct
		if isNaN(avg) {
			avg = 0.0
		}
		acc *= 1.0 + avg/100.0
		proxyClose[i] = acc
	}

	recent := grouped[len(grouped)-20:]
	recentProxy := proxyClose[len(proxyClose)-20:]

	var prev []WidthDaily
	var prevProxy Series
	if len(grouped) >= 40 {
		prev = grouped[len(grouped)-40 : len(grouped)-20]
		prevProxy = proxyClose[len(proxyClose)-40 : len(proxyClose)-20]
	} else {
		keep := len(grouped) - 20
		if keep < 1 {
			keep = 1
		}
		prev = grouped[:keep]
		prevProxy = proxyClose[:keep]
	}

	proxyRet20 := (recentProxy[len(recentProxy)-1]/recentProxy[0] - 1.0) * 100.0
	recentHigh := maxSeries(recentProxy)
	recentLow := minSeries(recentProxy)
	prevHigh := maxSeries(prevProxy)
	prevLow := minSeries(prevProxy)

	upRatio := meanNonNaN(tailFloats(widthField(recent, func(w WidthDaily) float64 { return w.UpRatio }), 5))
	downRatio := meanNonNaN(tailFloats(widthField(recent, func(w WidthDaily) float64 { return w.DownRatio }), 5))
	amountRecent := meanNonNaN(tailFloats(widthField(recent, func(w WidthDaily) float64 { return w.Amount }), 5))
	amountPrev := meanNonNaN(tailFloats(widthField(prev, func(w WidthDaily) float64 { return w.Amount }), 5))
	if isNaN(amountRecent) {
		amountRecent = 0.0
	}
	if isNaN(amountPrev) {
		amountPrev = 0.0
	}
	amountGrowth := 0.0
	if amountPrev > 0 {
		amountGrowth = (amountRecent/amountPrev - 1.0) * 100.0
	}

	higher := recentHigh > prevHigh && recentLow > prevLow
	lower := recentHigh < prevHigh && recentLow < prevLow

	bullScore := 45.0
	if higher {
		bullScore += 25.0
	}
	if proxyRet20 >= 5.0 {
		bullScore += 20.0
	} else {
		bullScore += math.Max(0.0, proxyRet20*2.0)
	}
	if upRatio >= 0.55 {
		bullScore += 15.0
	}
	if amountGrowth >= 5.0 {
		bullScore += 10.0
	}

	bearScore := 45.0
	if lower {
		bearScore += 25.0
	}
	if proxyRet20 <= -5.0 {
		bearScore += 20.0
	} else {
		bearScore += math.Max(0.0, math.Abs(math.Min(proxyRet20, 0.0))*2.0)
	}
	if downRatio >= 0.55 {
		bearScore += 15.0
	}

	metrics := MarketStateMetrics{
		ProxyRet20:     round4(proxyRet20),
		UpRatio5:       round4(upRatio),
		DownRatio5:     round4(downRatio),
		AmountGrowth5:  round4(amountGrowth),
		HigherStruct:   higher,
		LowerStructure: lower,
	}
	lastDate := grouped[len(grouped)-1].TradeDate
	base := MarketStateInfo{
		Source: "auto", TradeDate: lastDate,
		HasMetrics: true, Metrics: metrics,
	}
	switch {
	case bullScore >= 75.0 && bullScore >= bearScore+10.0:
		base.State = "bull"
		base.Label = MarketStateLabels["bull"]
		base.Score = round2(math.Min(100.0, bullScore))
		base.Reason = fmt.Sprintf("高低点抬升=%s，20日等权收益%.2f%%，近5日上涨占比%.0f%%",
			pyBool(higher), proxyRet20, upRatio*100)
	case bearScore >= 75.0 && bearScore >= bullScore+10.0:
		base.State = "bear"
		base.Label = MarketStateLabels["bear"]
		base.Score = round2(math.Min(100.0, bearScore))
		base.Reason = fmt.Sprintf("高低点下移=%s，20日等权收益%.2f%%，近5日下跌占比%.0f%%",
			pyBool(lower), proxyRet20, downRatio*100)
	default:
		base.State = "range"
		base.Label = MarketStateLabels["range"]
		base.Score = round2(math.Max(0.0, math.Min(100.0, 100.0-math.Abs(bullScore-bearScore))))
		base.Reason = fmt.Sprintf("趋势优势不明显，20日等权收益%.2f%%，上涨占比%.0f%%",
			proxyRet20, upRatio*100)
	}
	return base
}

// pyBool 复刻 Python 的布尔文本（`True` / `False`）。
//
// 市场状态的 reason 里内嵌了布尔值（`f"高低点抬升={higher}"`），
// Go 的 `%v` 会输出 `true`，与原版的 `True` 不同 —— 而这段文字会落进
// `signal_history` 与界面，所以按 Python 的写法对齐。
func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func manualStateInfo(state, currentDate string) MarketStateInfo {
	label, ok := MarketStateLabels[state]
	if !ok {
		label = state
	}
	return MarketStateInfo{
		State: state, Label: label, Score: 100.0,
		Source: "manual", Reason: "用户手动指定市场状态",
		TradeDate: strings.TrimSpace(currentDate),
	}
}

func rangeStateInfo(tradeDate, reason string) MarketStateInfo {
	return MarketStateInfo{
		State: "range", Label: MarketStateLabels["range"], Score: 50.0,
		Source: "auto", Reason: reason, TradeDate: tradeDate,
	}
}

func widthField(rows []WidthDaily, pick func(WidthDaily) float64) []float64 {
	out := make([]float64, len(rows))
	for i, row := range rows {
		out[i] = pick(row)
	}
	return out
}

func tailFloats(values []float64, n int) []float64 {
	if n >= len(values) {
		return values
	}
	return values[len(values)-n:]
}

func maxSeries(values Series) float64 {
	out := values[0]
	for _, v := range values[1:] {
		if v > out {
			out = v
		}
	}
	return out
}

func minSeries(values Series) float64 {
	out := values[0]
	for _, v := range values[1:] {
		if v < out {
			out = v
		}
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
