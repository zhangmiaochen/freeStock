package service

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"freestock/internal/cnlake"
)

// ---------------------------------------------------------------------------
// 市场温度
//
// 「市场温度」是把**大盘指数的多周期走势 + 量能**压成一个 0~100 的读数，
// 用来回答一个问题：**当前的市场状态，在它自己的历史里算热还是冷？**
//
// 三个刻意的设计（都是为了让这个数字能横向、纵向比较）：
//
//  1. **不看点数高低，只看相对水平。** 指数长期向上，点数天然越来越高 ——
//     直接拿"点位在历史区间里的位置"当水平，任何时点都会被判成高位。
//     所以"位置"用的是**点位相对自身 250 日均线的偏离**，再看这个偏离
//     在全部历史里的分位。这一点是用户明确要求的口径（见 README「市场温度」）。
//  2. **所有分项都先转成"在全部历史中的分位"再加权。** 分位天然对齐了不同量纲
//     （涨幅 %、量能倍数、偏离 %、上涨占比），也让 5 日和 60 日这种不同长度的
//     周期可以共用同一套阈值。
//  3. **温度本身 = 综合分在全部历史里的分位。** 分项加权后的综合分是 0~100，
//     但它的分布是"四个分位数的平均"，中间密、两头稀；取分位之后
//     温度就均匀铺在 0~100 上，读数可以直接读成"比历史上 X% 的交易日更热"。
//
// ⚠ 数据来源只有**本地数据湖**的 `index_bars`（free-stockdb 没有指数行情），
// 本机湖里是 8 个指数、2023-10-09 起 728 个交易日。所以：
//   - 分位的样本就是这 728 天（"有史以来所有数据"= 数据源里现有的全部历史，
//     湖里以后补了更早的年份，样本自动变长，代码不用改）；
//   - 湖里没有的指数（北证50、中证2000）**不列出来**，界面上不会出现一排"未入库"，
//     但注册表之外、湖里新增的指数会被自动带上（见 loadMarketIndices）。
// ---------------------------------------------------------------------------

// MajorIndex 是一个大盘指数的展示信息。
type MajorIndex struct {
	Code  string // index_bars 里的代码（带交易所后缀）
	Name  string
	Group string // 界面分组：大盘 / 成长 / 宽基
}

// majorIndices 是市场温度页展示的指数及其顺序。
//
// 名字只能写在这里：数据湖的 `index_bars` 只有代码没有名称，
// `instruments` 里也没有指数（只有 stock/etf/cdr）。
var majorIndices = []MajorIndex{
	{Code: "000001.SH", Name: "上证指数", Group: "大盘"},
	{Code: "399001.SZ", Name: "深证成指", Group: "大盘"},
	{Code: "399006.SZ", Name: "创业板指", Group: "成长"},
	{Code: "000688.SH", Name: "科创50", Group: "成长"},
	{Code: "000300.SH", Name: "沪深300", Group: "宽基"},
	{Code: "000016.SH", Name: "上证50", Group: "宽基"},
	{Code: "000905.SH", Name: "中证500", Group: "宽基"},
	{Code: "000852.SH", Name: "中证1000", Group: "宽基"},
}

// 均线长度与最少样本数。
const (
	tempMaShort = 20
	tempMaMid   = 60
	tempMaLong  = 250

	// tempMinSamples 是算分位所需的最少有效样本。
	// 少于这个数说明这段历史还太短（刚建库），分位没有意义，此时分项取 50（中性）。
	tempMinSamples = 120

	// tempVolumeWindow 是量能的对照窗口：近一年（250 个交易日）日均量。
	tempVolumeWindow = 250
)

// 四个分项的权重。
//
// 趋势是主导（市场温度首先问"在涨还是在跌"），量能确认趋势的成色，
// 位置反映"离长期趋势有多远"（风险），共识反映普涨还是少数拉动。
var tempFactorWeights = map[string]float64{
	"trend":    0.40,
	"volume":   0.25,
	"position": 0.20,
	"breadth":  0.15,
}

// marketTemperatureTTL 是指数历史缓存的存活时间。
//
// 指数历史只增不改（当天那根收盘价会更新），30 分钟足够；
// 切换数据源时代次变化会让它作废。
const marketTemperatureTTL = 30 * time.Minute

// MarketIndexStat 是一个大盘指数在市场温度页上的一行。
type MarketIndexStat struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Group        string  `json:"group"`
	Date         int64   `json:"date"`
	Close        float64 `json:"close"`
	PctChg       float64 `json:"pctChg"`       // 当日涨跌幅 %
	PeriodPctChg float64 `json:"periodPctChg"` // 所选周期涨跌幅 %
	Amount       float64 `json:"amount"`       // 当日成交额

	// 所选周期的最高/最低（区间区间内）
	PeriodHigh float64 `json:"periodHigh"`
	PeriodLow  float64 `json:"periodLow"`

	// VolumeRatio 是"所选周期日均量 ÷ 近一年日均量"，>1 放量。
	VolumeRatio float64 `json:"volumeRatio"`

	Ma20  float64 `json:"ma20"`
	Ma60  float64 `json:"ma60"`
	Ma250 float64 `json:"ma250"`

	// ---- 位置：全部历史算出来的相对水平（不是点数高低）----
	//
	// MaDeviation 是点位相对 250 日均线的偏离 %；
	// LevelScore 是这个偏离在其**全部历史**中的分位（0~100）。
	MaDeviation float64 `json:"maDeviation"`
	LevelScore  float64 `json:"levelScore"`
	LevelText   string  `json:"levelText"`

	// DrawdownFromHigh 是距历史（样本内）最高点的回撤 %（负数）。
	DrawdownFromHigh float64 `json:"drawdownFromHigh"`
	HighDate         int64   `json:"highDate"`
	HighClose        float64 `json:"highClose"`

	// NewHighDays / NewLowDays 是当日创下的新高/新低天数（0 表示没有）。
	NewHighDays int `json:"newHighDays"`
	NewLowDays  int `json:"newLowDays"`

	// 分项与综合动向（都先在自身历史里取分位，再按权重合成）
	MomentumScore float64 `json:"momentumScore"`
	VolumeScore   float64 `json:"volumeScore"`
	TrendScore    float64 `json:"trendScore"`
	TrendLabel    string  `json:"trendLabel"`
	TrendNote     string  `json:"trendNote"`

	// 走势数据：最近 Window 个交易日（与结果里的 Dates 对齐）
	Recent []float64 `json:"recent"` // 原始收盘点数
	Norm   []float64 `json:"norm"`   // 归一化（窗口首日 = 100）

	// HistoryDays 是该指数用于分位的历史交易日数，HistoryStart 是起点。
	HistoryDays  int   `json:"historyDays"`
	HistoryStart int64 `json:"historyStart"`
}

// TemperatureFactor 是市场温度的一个分项。
type TemperatureFactor struct {
	Key       string  `json:"key"`   // trend / volume / position / breadth
	Name      string  `json:"name"`  // 趋势 / 量能 / 位置 / 共识
	Score     float64 `json:"score"` // 0~100：该分项在全部历史中的分位
	Weight    float64 `json:"weight"`
	Value     float64 `json:"value"`     // 原始值（%/倍/%/%）
	ValueText string  `json:"valueText"` // 原始值的可读形式
	Detail    string  `json:"detail"`
}

// MarketTemperatureResult 是市场温度页一次加载所需的全部数据。
type MarketTemperatureResult struct {
	// Available 为 false 时 Message 说明原因（例如数据湖不可用 / index_bars 未入库），
	// 此时其余字段为空 —— 界面据此显示空状态而不是一屏 0。
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`

	TradeDate   int64  `json:"tradeDate"`
	PeriodDays  int    `json:"periodDays"`
	PeriodLabel string `json:"periodLabel"`

	// Temperature 是 0~100 的市场温度：综合分在**全部历史**中的分位。
	Temperature float64 `json:"temperature"`
	Level       string  `json:"level"`     // 冰点 / 偏冷 / 中性 / 偏热 / 过热
	LevelText   string  `json:"levelText"` // 结论性的一句话
	Composite   float64 `json:"composite"` // 加权后的综合分（0~100）

	// 分位样本区间（"有史以来"在数据源里实际是多少）
	HistoryDays  int   `json:"historyDays"`
	HistoryStart int64 `json:"historyStart"`
	HistoryEnd   int64 `json:"historyEnd"`

	Factors    []TemperatureFactor `json:"factors"`
	Indices    []MarketIndexStat   `json:"indices"`
	IndexCount int                 `json:"indexCount"`

	// Window 是走势图/迷你图显示的交易日数，Dates 是所有指数共用的日期轴，
	// 每个指数的 Recent/Norm 与它一一对应。
	Window int     `json:"window"`
	Dates  []int64 `json:"dates"`

	Notes   []string `json:"notes"`
	Source  string   `json:"source"`
	Elapsed string   `json:"elapsed"`
}

// ---------------------------------------------------------------------------
// 指数历史
// ---------------------------------------------------------------------------

// indexSeries 是一个指数在全部历史上的序列与派生指标。
type indexSeries struct {
	meta   MajorIndex
	dates  []int64
	close  []float64
	amount []float64

	// 派生指标：日期 → 值（用 map 是因为各指数的可用日期不完全一致）
	momentum map[int64]float64 // 所选周期涨跌幅 %
	volume   map[int64]float64 // 量能倍数
	level    map[int64]float64 // 相对 250 日均线偏离 %
	maShort  map[int64]float64
	maMid    map[int64]float64
	maLong   map[int64]float64
}

// marketIndexCache 是指数历史的缓存。
type marketIndexCache struct {
	gen     int64
	series  []*indexSeries
	extras  []string // 注册表之外、湖里多出来的指数代码
	asOf    int64    // 每个指数最后一根 K 线的日期里最大的那个
	builtAt time.Time
}

// loadMarketIndices 读取数据湖里全部指数的全部历史（带缓存）。
//
// 返回的 series 里，注册表里的指数按注册顺序排在前面，湖里新增的
// （注册表没有的）按代码排在后面并带上一句提示 —— 这样湖里哪天补进了
// 北证50，页面会自动出现第 9 个指数，不需要改代码。
func (s *StockService) loadMarketIndices() ([]*indexSeries, []string, string, error) {
	s.tempMu.Lock()
	defer s.tempMu.Unlock()

	gen := s.universeGen.Load()
	if cached := s.tempCache; cached != nil &&
		cached.gen == gen && time.Since(cached.builtAt) < marketTemperatureTTL {
		return cached.series, cached.extras, "", nil
	}

	lake := s.lakeHandle()
	if lake == nil || !lake.HasIndexBars() {
		return nil, nil, "本地数据湖里没有 index_bars（指数日线）。" +
			"free-stockdb 不提供指数行情，所以市场温度只能在数据湖模式下使用：" +
			"请在「数据源设置」里切到数据湖，并确认 cne 已同步 index_bars。", nil
	}

	started := time.Now()
	rows, err := lake.AllIndexBars()
	if err != nil {
		return nil, nil, fmt.Sprintf("读取数据湖 index_bars 失败：%v", err), nil
	}
	if len(rows) == 0 {
		return nil, nil, "数据湖 index_bars 里没有任何指数日线，请先同步。", nil
	}

	// 按代码分组
	names := make(map[string]MajorIndex, len(majorIndices))
	for _, meta := range majorIndices {
		names[strings.ToUpper(meta.Code)] = meta
	}

	grouped := make(map[string][]cnlake.IndexBarRow, len(names))
	order := make([]string, 0, len(names))
	for _, row := range rows {
		code := strings.ToUpper(strings.TrimSpace(row.Symbol))
		if code == "" {
			continue
		}
		if _, seen := grouped[code]; !seen {
			order = append(order, code)
		}
		grouped[code] = append(grouped[code], row)
	}

	series := make([]*indexSeries, 0, len(order))
	extras := make([]string, 0, 4)
	// 先按注册顺序取，保证界面顺序稳定（大盘 → 成长 → 宽基）
	for _, meta := range majorIndices {
		code := strings.ToUpper(meta.Code)
		list, ok := grouped[code]
		if !ok || len(list) == 0 {
			continue
		}
		delete(grouped, code)
		item := newIndexSeries(meta, list)
		// 整只指数都是无效收盘价（Close<=0）时 dates 为空：不能进结果，
		// 否则后面 `item.dates[len-1]` 会越界
		if len(item.dates) == 0 {
			continue
		}
		series = append(series, item)
	}
	// 再把湖里多出来的指数补上
	rest := make([]string, 0, len(grouped))
	for code := range grouped {
		rest = append(rest, code)
	}
	sort.Strings(rest)
	for _, code := range rest {
		meta := MajorIndex{Code: code, Name: code, Group: "其它"}
		item := newIndexSeries(meta, grouped[code])
		if len(item.dates) == 0 {
			continue
		}
		series = append(series, item)
		extras = append(extras, code)
	}

	asOf := int64(0)
	for _, item := range series {
		if n := len(item.dates); n > 0 && item.dates[n-1] > asOf {
			asOf = item.dates[n-1]
		}
	}

	s.tempCache = &marketIndexCache{gen: gen, series: series, extras: extras, asOf: asOf, builtAt: time.Now()}
	slog.Info("指数历史就绪",
		"indices", len(series), "asOf", asOf,
		"elapsed", time.Since(started).Round(time.Millisecond).String())
	return series, extras, "", nil
}

// newIndexSeries 把某个指数的原始日线整理成序列（升序、去重）。
func newIndexSeries(meta MajorIndex, rows []cnlake.IndexBarRow) *indexSeries {
	item := &indexSeries{meta: meta}
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		date := row.Date()
		if date <= 0 || row.Close <= 0 {
			continue
		}
		if _, exists := seen[date]; exists {
			continue
		}
		seen[date] = struct{}{}
		item.dates = append(item.dates, date)
		item.close = append(item.close, row.Close)
		item.amount = append(item.amount, row.Amount)
	}

	// ⚠ 排序键必须与 dates/close 一起重排：parquet 里同一年内的顺序不保证，
	// 而后面所有指标（均线、区间涨跌、分位）都建立在"按日期升序"这个前提上。
	if !sort.SliceIsSorted(item.dates, func(i, j int) bool { return item.dates[i] < item.dates[j] }) {
		type pair struct {
			date   int64
			close  float64
			amount float64
		}
		pairs := make([]pair, len(item.dates))
		for i := range item.dates {
			pairs[i] = pair{item.dates[i], item.close[i], item.amount[i]}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].date < pairs[j].date })
		for i, p := range pairs {
			item.dates[i], item.close[i], item.amount[i] = p.date, p.close, p.amount
		}
	}
	return item
}

// ---------------------------------------------------------------------------
// 派生指标
// ---------------------------------------------------------------------------

// movingAverageSeries 返回与 closes 等长的简单均线（不足窗口时用当日前全部数据）。
//
// 与 screener.go 里那个 `movingAverage`（只取最新一根、返回 (值, 是否可用)）
// 刻意分开：那边是"算选股指标"，这边要的是**每根 K 线**上的均线，
// 因为位置分位是对全部历史逐日算的。
func movingAverageSeries(closes []float64, window int) []float64 {
	out := make([]float64, len(closes))
	sum := 0.0
	for i, close := range closes {
		sum += close
		if i >= window {
			sum -= closes[i-window]
		}
		count := i + 1
		if count > window {
			count = window
		}
		out[i] = sum / float64(count)
	}
	return out
}

// indexMetrics 计算某个指数在**全部历史**上的派生指标。
//
// 返回的四个 map 都以日期为键：区间涨跌幅、量能倍数、均线偏离、三条均线。
// 只有样本足够（够算均线/对照窗口）的日期才会出现。
func (item *indexSeries) metrics(periodDays int) (momentum, volume, level, maShort, maMid, maLong map[int64]float64) {
	n := len(item.dates)
	momentum = make(map[int64]float64, n)
	volume = make(map[int64]float64, n)
	level = make(map[int64]float64, n)
	maShort = make(map[int64]float64, n)
	maMid = make(map[int64]float64, n)
	maLong = make(map[int64]float64, n)

	ma20 := movingAverageSeries(item.close, tempMaShort)
	ma60 := movingAverageSeries(item.close, tempMaMid)
	ma250 := movingAverageSeries(item.close, tempMaLong)

	// 成交量前缀和：算"近一年日均量"不用每次回看 250 个元素
	amountSum := make([]float64, n+1)
	for i, amount := range item.amount {
		amountSum[i+1] = amountSum[i] + amount
	}
	windowMean := func(end, window int) float64 {
		// end 是下标（含），取它往前 window 个点的均值
		from := end - window + 1
		if from < 0 {
			from = 0
		}
		count := end - from + 1
		if count <= 0 {
			return 0
		}
		return (amountSum[end+1] - amountSum[from]) / float64(count)
	}

	for i, date := range item.dates {
		if i >= periodDays {
			base := item.close[i-periodDays]
			if base > 0 {
				momentum[date] = round((item.close[i]/base-1)*100, 4)
			}
		}
		if i+1 >= tempVolumeWindow {
			recent := windowMean(i, periodDays)
			year := windowMean(i, tempVolumeWindow)
			if year > 0 && recent > 0 {
				volume[date] = round(recent/year, 4)
			}
		}
		if i+1 >= tempMaLong {
			if ma250[i] > 0 {
				level[date] = round((item.close[i]/ma250[i]-1)*100, 4)
			}
			if ma20[i] > 0 {
				maShort[date] = ma20[i]
			}
			if ma60[i] > 0 {
				maMid[date] = ma60[i]
			}
			maLong[date] = ma250[i]
		}
	}
	return momentum, volume, level, maShort, maMid, maLong
}

// percentileRank 返回 value 在 sample 中的分位（0~100）。
//
// 相等值取中点，避免"同一天并列"被算成极端分位。
// sample 太短（< tempMinSamples）时返回 50（中性）—— 刚建库时样本不足，
// 分位没有意义，报 50 比报 0 或 100 安全。
func percentileRank(sample []float64, value float64) float64 {
	if len(sample) < tempMinSamples {
		return 50
	}
	less, equal := 0, 0
	for _, item := range sample {
		switch {
		case item < value:
			less++
		case item == value:
			equal++
		}
	}
	return round(float64(less)+float64(equal)/2, 0) / float64(len(sample)) * 100
}

// sortedValues 把 map 的值取出来升序排序（分位样本）。
func sortedValues(values map[int64]float64) []float64 {
	out := make([]float64, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	sort.Float64s(out)
	return out
}

// ---------------------------------------------------------------------------
// 主入口
// ---------------------------------------------------------------------------

// GetMarketTemperature 计算市场温度。
//
// periodDays 是"看多长周期的走势"：1 = 当日（默认），其余 2~250 个交易日。
// 与板块页的区间周期不同，这里 1 是合法值 —— 当日的走势本身就是市场温度
// 最直接的输入。
func (s *StockService) GetMarketTemperature(periodDays int) (*MarketTemperatureResult, error) {
	started := time.Now()
	period := normalizeTemperaturePeriod(periodDays)
	result := &MarketTemperatureResult{
		TradeDate:   0,
		PeriodDays:  period,
		PeriodLabel: periodLabelOf(period),
		Source:      s.lakeHandle().Root(),
	}

	series, extras, unavailable, err := s.loadMarketIndices()
	if err != nil {
		return nil, err
	}
	if unavailable != "" || len(series) == 0 {
		result.Message = unavailable
		if result.Message == "" {
			result.Message = "数据湖里没有可用的指数日线。"
		}
		result.Elapsed = time.Since(started).Round(time.Millisecond).String()
		return result, nil
	}
	result.Available = true

	// ---- 1. 每个指数的派生指标（全部历史） ----
	type indexWork struct {
		item        *indexSeries
		mom         map[int64]float64
		vol         map[int64]float64
		level       map[int64]float64
		maS         map[int64]float64
		maM         map[int64]float64
		maL         map[int64]float64
		momSample   []float64
		volSample   []float64
		levelSample []float64
		// levelStart 是该指数"位置分位"样本的起点。
		// 界面上的说明文字会写"XXXX-XX-XX 以来 X% 分位"，那个日期必须是
		// **这只指数自己的**样本起点（各指数缺失的交易日不完全一致），
		// 用全局起点会写出一句不准确的话。
		levelStart int64
	}
	works := make([]*indexWork, 0, len(series))
	for _, item := range series {
		mom, vol, level, maS, maM, maL := item.metrics(period)
		work := &indexWork{
			item: item, mom: mom, vol: vol, level: level, maS: maS, maM: maM, maL: maL,
			momSample:   sortedValues(mom),
			volSample:   sortedValues(vol),
			levelSample: sortedValues(level),
		}
		for _, date := range item.dates {
			if _, ok := level[date]; !ok {
				continue
			}
			if work.levelStart == 0 || date < work.levelStart {
				work.levelStart = date
			}
		}
		if work.levelStart == 0 {
			work.levelStart = item.dates[0]
		}
		works = append(works, work)
	}

	// ---- 2. 全市场的日度聚合（等权，只用当天所有指数都有值的项） ----
	//
	// 用并集日期：某个指数缺某天（停牌不存在，但数据可能缺）时，
	// 那一天的聚合就用其余指数的平均，而不是把缺失当成 0。
	dateSet := make(map[int64]struct{}, 1024)
	for _, work := range works {
		for _, date := range work.item.dates {
			dateSet[date] = struct{}{}
		}
	}
	dates := make([]int64, 0, len(dateSet))
	for date := range dateSet {
		dates = append(dates, date)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })

	aggMom := make(map[int64]float64, len(dates))
	aggVol := make(map[int64]float64, len(dates))
	aggLevel := make(map[int64]float64, len(dates))
	aggBreadth := make(map[int64]float64, len(dates))
	for _, date := range dates {
		momSum, momCount := 0.0, 0
		volSum, volCount := 0.0, 0
		levelSum, levelCount := 0.0, 0
		up, counted := 0, 0
		for _, work := range works {
			if value, ok := work.mom[date]; ok {
				momSum += value
				momCount++
				counted++
				if value > 0 {
					up++
				}
				if value, ok := work.vol[date]; ok {
					volSum += value
					volCount++
				}
				if value, ok := work.level[date]; ok {
					levelSum += value
					levelCount++
				}
			}
		}
		if momCount > 0 {
			aggMom[date] = momSum / float64(momCount)
		}
		if volCount > 0 {
			aggVol[date] = volSum / float64(volCount)
		}
		if levelCount > 0 {
			aggLevel[date] = levelSum / float64(levelCount)
		}
		if counted > 0 {
			aggBreadth[date] = float64(up) / float64(counted) * 100
		}
	}

	momSample := sortedValues(aggMom)
	volSample := sortedValues(aggVol)
	levelSample := sortedValues(aggLevel)
	breadthSample := sortedValues(aggBreadth)

	// ---- 3. 每个交易日的综合分，以及当日的分项得分 ----
	//
	// ⚠ **只在四个分项都有值的交易日上算综合分**（完整样本）：
	// 最早的 ~249 个交易日还没有 250 日均线与"近一年量能"，把缺失当 0 或 50
	// 都会凭空造出一批"极冷/中性"的日子，直接污染温度的分位分布。
	// 少算一年样本换来的是"温度每一格都由同口径的完整数据支撑"。
	compositeDates := make([]int64, 0, len(dates))
	for _, date := range dates {
		if _, ok := aggMom[date]; !ok {
			continue
		}
		if _, ok := aggVol[date]; !ok {
			continue
		}
		if _, ok := aggLevel[date]; !ok {
			continue
		}
		if _, ok := aggBreadth[date]; !ok {
			continue
		}
		compositeDates = append(compositeDates, date)
	}
	if len(compositeDates) == 0 {
		result.Available = false
		result.Message = fmt.Sprintf(
			"指数历史太短：%d 个交易日还不够算 250 日均线与近一年量能，无法给出温度。",
			len(dates))
		result.Elapsed = time.Since(started).Round(time.Millisecond).String()
		return result, nil
	}

	composite := make([]float64, 0, len(compositeDates))
	compositeAt := make(map[int64]float64, len(compositeDates))
	for _, date := range compositeDates {
		score := tempFactorWeights["trend"]*percentileRank(momSample, aggMom[date]) +
			tempFactorWeights["volume"]*percentileRank(volSample, aggVol[date]) +
			tempFactorWeights["position"]*percentileRank(levelSample, aggLevel[date]) +
			tempFactorWeights["breadth"]*percentileRank(breadthSample, aggBreadth[date])
		compositeAt[date] = score
		composite = append(composite, score)
	}
	sort.Float64s(composite)

	end := compositeDates[len(compositeDates)-1]
	start := compositeDates[0]
	result.TradeDate = end
	// ⚠ HistoryDays 是**温度分位样本**的天数（完整样本），不是指数历史的天数：
	// 界面上"比历史上 X% 的交易日更热"必须用同一套样本才对得上。
	result.HistoryDays = len(compositeDates)
	result.HistoryStart = start
	result.HistoryEnd = end
	result.Composite = round(compositeAt[end], 2)
	result.Temperature = round(percentileRank(composite, compositeAt[end]), 1)

	// ---- 4. 分项明细 ----
	factor := func(key, name string, value float64, valueText, detail string, sample []float64) TemperatureFactor {
		return TemperatureFactor{
			Key:       key,
			Name:      name,
			Score:     round(percentileRank(sample, value), 1),
			Weight:    tempFactorWeights[key],
			Value:     round(value, 2),
			ValueText: valueText,
			Detail:    detail,
		}
	}
	result.Factors = []TemperatureFactor{
		factor("trend", "趋势", aggMom[end], fmt.Sprintf("%+.2f%%", aggMom[end]),
			fmt.Sprintf("%d 个指数%s涨跌幅的等权平均", len(works), result.PeriodLabel), momSample),
		factor("volume", "量能", aggVol[end], fmt.Sprintf("%.2f 倍", aggVol[end]),
			fmt.Sprintf("%s日均成交额 ÷ 近一年日均成交额（>1 放量）", result.PeriodLabel), volSample),
		factor("position", "位置", aggLevel[end], fmt.Sprintf("%+.2f%%", aggLevel[end]),
			"各指数点位相对自身 250 日均线的平均偏离（越高越偏离长期趋势）", levelSample),
		factor("breadth", "共识", aggBreadth[end], fmt.Sprintf("%.0f%%", aggBreadth[end]),
			fmt.Sprintf("%s上涨的指数占比（普涨还是少数拉动）", result.PeriodLabel), breadthSample),
	}
	result.Level, result.LevelText = temperatureLevel(result.Temperature, result.Composite)

	// ---- 5. 走势窗口（对比图 + 迷你图共用一套日期轴） ----
	window := period
	if window < 20 {
		window = 20
	}
	if window > len(dates) {
		window = len(dates)
	}
	result.Window = window
	result.Dates = append([]int64(nil), dates[len(dates)-window:]...)

	// ---- 6. 每个指数的展示数据 ----
	result.Indices = make([]MarketIndexStat, 0, len(works))
	for _, work := range works {
		result.Indices = append(result.Indices, buildIndexStat(work.item, work.mom, work.vol, work.level, work.maS, work.maM, work.maL,
			work.momSample, work.volSample, work.levelSample, result.Dates, period, work.levelStart))
	}
	result.IndexCount = len(result.Indices)

	// ---- 7. 说明 ----
	result.Notes = []string{
		fmt.Sprintf("指数历史来自本地数据湖的 index_bars：%s ~ %s 共 %d 个交易日（%d 个指数）。",
			formatDateString(dates[0]), formatDateString(dates[len(dates)-1]), len(dates), len(works)),
		fmt.Sprintf("温度的分位样本是其中**四个分项都有值**的 %d 个交易日（%s 起）："+
			"最早那批交易日还没有 250 日均线与近一年量能，把缺失当 0 或 50 会凭空造出一批"+
			"极冷/中性的日子，污染整条分位分布。", len(compositeDates), formatDateString(start)),
		"所有分位都用**全部样本**（含当日）计算。这是「描述当前状态」，不是回测信号，所以不做滚动窗口。",
		"位置分不用点数高低：指数长期向上，点数天然越来越高；这里比的是「点位相对自身 250 日均线的偏离」在历史中的位置。",
		"价格是不复权收盘价；各指数等权、不加权（避免沪深300 一个指数决定读数）。",
	}
	if len(extras) > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf(
			"另有 %d 个注册表之外的指数被自动纳入：%s。", len(extras), strings.Join(extras, "、")))
	}

	result.Elapsed = time.Since(started).Round(time.Millisecond).String()
	slog.Info("市场温度就绪",
		"period", result.PeriodLabel, "temperature", result.Temperature,
		"indices", result.IndexCount, "history", len(compositeDates), "elapsed", result.Elapsed)
	return result, nil
}

// normalizeTemperaturePeriod 规范化周期：1（当日）~ 250 个交易日。
func normalizeTemperaturePeriod(days int) int {
	if days <= 0 {
		return 1
	}
	if days > maxBoardPeriod {
		return maxBoardPeriod
	}
	return days
}

// periodLabelOf 返回周期标签（1 = 当日）。
func periodLabelOf(days int) string {
	if days <= 1 {
		return "当日"
	}
	return fmt.Sprintf("%d日", days)
}

// temperatureLevel 把温度读成等级与一句话结论。
func temperatureLevel(temperature, composite float64) (string, string) {
	switch {
	case temperature >= 85:
		return "过热", "市场处于历史高位热度：趋势、量能与位置同时偏极端，注意拥挤与回撤风险。"
	case temperature >= 70:
		return "偏热", "市场热度高于历史多数时间：趋势向上、量能配合，属于进攻区间。"
	case temperature >= 45:
		return "中性", "市场热度处于历史中段：多空力量接近均衡，方向未明。"
	case temperature >= 25:
		return "偏冷", "市场热度低于历史多数时间：趋势偏弱、量能不足，适合等待信号。"
	default:
		return "冰点", fmt.Sprintf("市场处于历史低位热度（综合分 %.0f）：情绪低迷，往往也是中长期布局区间。", composite)
	}
}

// buildIndexStat 组装单个指数在页面上的展示数据。
func buildIndexStat(
	item *indexSeries,
	mom, vol, level, maS, maM, maL map[int64]float64,
	momSample, volSample, levelSample []float64,
	windowDates []int64,
	period int,
	historyStart int64,
) MarketIndexStat {
	n := len(item.dates)
	end := item.dates[n-1]
	stat := MarketIndexStat{
		Code:         item.meta.Code,
		Name:         item.meta.Name,
		Group:        item.meta.Group,
		Date:         end,
		Close:        item.close[n-1],
		Amount:       item.amount[n-1],
		HistoryDays:  n,
		HistoryStart: item.dates[0],
	}
	if n >= 2 && item.close[n-2] > 0 {
		stat.PctChg = round((item.close[n-1]/item.close[n-2]-1)*100, 4)
	}
	if value, ok := mom[end]; ok {
		stat.PeriodPctChg = value
	}
	if value, ok := vol[end]; ok {
		stat.VolumeRatio = value
	}
	if value, ok := maS[end]; ok {
		stat.Ma20 = round(value, 2)
	}
	if value, ok := maM[end]; ok {
		stat.Ma60 = round(value, 2)
	}
	if value, ok := maL[end]; ok {
		stat.Ma250 = round(value, 2)
	}

	// 所选周期的高低点（收盘价口径；当日就是当天收盘）
	from := n - period
	if from < 0 {
		from = 0
	}
	stat.PeriodHigh, stat.PeriodLow = item.close[from], item.close[from]
	for _, close := range item.close[from:] {
		if close > stat.PeriodHigh {
			stat.PeriodHigh = close
		}
		if close < stat.PeriodLow {
			stat.PeriodLow = close
		}
	}
	stat.PeriodHigh = round(stat.PeriodHigh, 2)
	stat.PeriodLow = round(stat.PeriodLow, 2)

	// ---- 位置：相对长期趋势的偏离 + 它在全部历史中的分位 ----
	if value, ok := level[end]; ok {
		stat.MaDeviation = value
		stat.LevelScore = round(percentileRank(levelSample, value), 1)
	}
	stat.LevelText = indexLevelText(stat, historyStart)

	// ---- 回撤与新高/新低 ----
	highIndex := 0
	for i, close := range item.close {
		if close > item.close[highIndex] {
			highIndex = i
		}
	}
	stat.HighDate = item.dates[highIndex]
	stat.HighClose = round(item.close[highIndex], 2)
	if item.close[highIndex] > 0 {
		stat.DrawdownFromHigh = round((item.close[n-1]/item.close[highIndex]-1)*100, 2)
	}
	stat.NewHighDays = newExtremeDays(item.close, true)
	stat.NewLowDays = newExtremeDays(item.close, false)

	// ---- 分项得分与趋势结论 ----
	stat.MomentumScore = round(percentileRank(momSample, stat.PeriodPctChg), 1)
	stat.VolumeScore = round(percentileRank(volSample, stat.VolumeRatio), 1)
	stat.TrendScore = round(0.45*stat.MomentumScore+0.30*stat.LevelScore+0.25*stat.VolumeScore, 1)
	stat.TrendLabel = indexTrendLabel(stat.TrendScore)
	stat.TrendNote = indexTrendNote(stat, period, historyStart)

	// ---- 走势窗口（与结果里的日期轴对齐；缺失日期沿用前值，避免断线）----
	byDate := make(map[int64]float64, n)
	for i, date := range item.dates {
		byDate[date] = item.close[i]
	}

	// 窗口第一天之前最近的一根收盘价：窗口首日缺数据时用它接着画
	last := 0.0
	if len(windowDates) > 0 {
		for i, date := range item.dates {
			if date >= windowDates[0] {
				break
			}
			last = item.close[i]
		}
	}

	stat.Recent = make([]float64, 0, len(windowDates))
	stat.Norm = make([]float64, 0, len(windowDates))
	base := 0.0
	for _, date := range windowDates {
		if value, ok := byDate[date]; ok {
			last = value
		}
		if last <= 0 {
			continue
		}
		if base == 0 {
			base = last
		}
		stat.Recent = append(stat.Recent, round(last, 2))
		stat.Norm = append(stat.Norm, round(last/base*100, 3))
	}
	return stat
}

// newExtremeDays 判断当日是否创下 20 / 60 / 250 日新高（新低），
// 返回创下的天数（例如 60 表示"创 60 日新高"），0 表示没有。
//
// 只报最长的那一档：界面上"创 60 日新高"已经足够表达，
// 不必再写"同时也创了 20 日新高"。
func newExtremeDays(closes []float64, high bool) int {
	n := len(closes)
	if n < 2 {
		return 0
	}
	current := closes[n-1]
	for _, window := range []int{250, 60, 20} {
		if n < window {
			continue
		}
		// 只比"之前"的 window-1 个点（今天自己是最后一根）
		slice := closes[n-window : n-1]
		isExtreme := true
		for _, value := range slice {
			if high && value >= current {
				isExtreme = false
				break
			}
			if !high && value <= current {
				isExtreme = false
				break
			}
		}
		if isExtreme {
			return window
		}
	}
	return 0
}

// indexLevelText 描述"当前点位在自身历史里算什么水平"。
func indexLevelText(stat MarketIndexStat, historyStart int64) string {
	if stat.Ma250 <= 0 {
		return "历史样本不足 250 个交易日，还算不出相对长期趋势的位置。"
	}
	position := "高于"
	if stat.MaDeviation < 0 {
		position = "低于"
	}
	return fmt.Sprintf("点位%s 250 日均线 %.2f%%，这个偏离在 %s 以来的历史里处于 %.0f%% 分位。",
		position, math.Abs(stat.MaDeviation), formatDateString(historyStart), stat.LevelScore)
}

// indexTrendLabel 把综合动向分读成标签。
func indexTrendLabel(score float64) string {
	switch {
	case score >= 75:
		return "强势上行"
	case score >= 60:
		return "偏强"
	case score >= 40:
		return "震荡"
	case score >= 25:
		return "偏弱"
	default:
		return "弱势下行"
	}
}

// indexTrendNote 用具体事实拼出这个指数的趋势说明。
//
// 为什么不直接给一句"偏强"：用户要看的是**依据**（均线关系、区间涨幅、
// 量能、距高点），所以这里把每个判断都写成可核对的事实。
func indexTrendNote(stat MarketIndexStat, period int, historyStart int64) string {
	parts := make([]string, 0, 5)

	// ① 均线关系
	if stat.Ma20 > 0 && stat.Ma60 > 0 && stat.Ma250 > 0 {
		above := func(ma float64) bool { return stat.Close >= ma }
		switch {
		case above(stat.Ma20) && above(stat.Ma60) && above(stat.Ma250):
			parts = append(parts, "站上 20 / 60 / 250 日均线")
		case above(stat.Ma20) && above(stat.Ma60):
			parts = append(parts, "站上 20 / 60 日均线，但在 250 日均线下方")
		case above(stat.Ma20):
			parts = append(parts, "站上 20 日均线，60 / 250 日均线仍未收复")
		case !above(stat.Ma20) && !above(stat.Ma60) && !above(stat.Ma250):
			parts = append(parts, "跌破 20 / 60 / 250 日均线")
		default:
			parts = append(parts, "跌破 20 日均线，仍在 60 / 250 日均线上方")
		}
	}

	// ② 区间涨跌幅与它的历史分位
	parts = append(parts, fmt.Sprintf("%s %+.2f%%（历史分位 %.0f%%）",
		periodLabelOf(period), stat.PeriodPctChg, stat.MomentumScore))

	// ③ 量能
	if stat.VolumeRatio > 0 {
		if stat.VolumeRatio >= 1 {
			parts = append(parts, fmt.Sprintf("量能较近一年均量放大 %.2f 倍", stat.VolumeRatio))
		} else {
			parts = append(parts, fmt.Sprintf("量能仅为近一年均量的 %.2f 倍", stat.VolumeRatio))
		}
	}

	// ④ 位置（相对长期趋势）
	if stat.Ma250 > 0 {
		parts = append(parts, fmt.Sprintf("点位相对 250 日均线 %+.2f%%（%s 以来 %.0f%% 分位）",
			stat.MaDeviation, formatDateString(historyStart), stat.LevelScore))
	}

	// ⑤ 新高/新低与回撤
	switch {
	case stat.NewHighDays > 0:
		parts = append(parts, fmt.Sprintf("创 %d 日新高", stat.NewHighDays))
	case stat.NewLowDays > 0:
		parts = append(parts, fmt.Sprintf("创 %d 日新低", stat.NewLowDays))
	case stat.DrawdownFromHigh < 0:
		parts = append(parts, fmt.Sprintf("距 %s 的高点回撤 %.2f%%",
			formatDateString(stat.HighDate), math.Abs(stat.DrawdownFromHigh)))
	}

	return strings.Join(parts, "；") + "。"
}

// formatDateString 把 YYYYMMDD 变成 YYYY-MM-DD（只用于说明文字）。
func formatDateString(date int64) string {
	text := fmt.Sprintf("%08d", date)
	if len(text) != 8 {
		return text
	}
	return text[:4] + "-" + text[4:6] + "-" + text[6:]
}
