package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mozillazg/go-pinyin"

	"freestock/internal/cnlake"
	"freestock/internal/market"
	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 前端数据结构
// ---------------------------------------------------------------------------

// StockSearchItem 是搜索联想的一条结果。
type StockSearchItem struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Market   string  `json:"market"`
	MarketCN string  `json:"marketCN"`
	Pinyin   string  `json:"pinyin"`
	Initials string  `json:"initials"`
	Close    float64 `json:"close"`
	PctChg   float64 `json:"pctChg"`
	MatchOn  string  `json:"matchOn"`
	// IsIndex 标记这条是**大盘指数**（不是股票/基金）。
	//
	// 指数与股票必须能分辨，否则两个后果：界面上分不清"000001.SH 上证指数"
	// 与"000001 平安银行"；列表页会把指数当成股票去筛（筛不出来）。
	IsIndex bool `json:"isIndex"`
}

// KlineCandle 是一根 K 线（已按需复权）。
type KlineCandle struct {
	Date   int64   `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	Amount float64 `json:"amount"`
}

// StockProfile 是 K 线页面上方展示的股票简要信息。
//
// 说明：free-stockdb 的默认本地数据集**不包含财务报表**（营收/净利润/ROE 等），
// 因此这里只包含日 K 自带的估值字段，以及由复权表推导出的分红/股息率。
type StockProfile struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Market    string `json:"market"`
	MarketCN  string `json:"marketCN"`
	TradeDate int64  `json:"tradeDate"`

	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	PreClose  float64 `json:"preClose"`
	PctChg    float64 `json:"pctChg"`
	Amplitude float64 `json:"amplitude"`

	Volume   float64 `json:"volume"`
	Amount   float64 `json:"amount"`
	Turnover float64 `json:"turnover"`
	VolRatio float64 `json:"volRatio"`

	PeTTM float64 `json:"peTTM"`
	PB    float64 `json:"pb"`

	TotalMV    float64 `json:"totalMV"`
	FloatMV    float64 `json:"floatMV"`
	TotalShare float64 `json:"totalShare"`
	FloatShare float64 `json:"floatShare"`
	IsST       bool    `json:"isST"`

	// 由复权（分红）数据推导
	DividendYield float64 `json:"dividendYield"` // 股息率 %
	DividendTTM   float64 `json:"dividendTTM"`   // 最近 12 个月每股派息
	LatestDiv     float64 `json:"latestDiv"`     // 最近一次每股派息
	LatestDivDate int64   `json:"latestDivDate"` // 最近一次除权日
	DividendCount int     `json:"dividendCount"` // 历史除权次数

	// 由行情推导的 TTM 口径财务指标（见 finance.go 的说明，无需财报）
	ROE          float64 `json:"roe"`          // 净资产收益率 %
	EPS          float64 `json:"eps"`          // 每股收益
	BPS          float64 `json:"bps"`          // 每股净资产
	NetProfitTTM float64 `json:"netProfitTTM"` // 归母净利润 TTM（元）
	NetAssets    float64 `json:"netAssets"`    // 净资产（元）

	// 资金流（本机「资金流」表；缺失时为 0）
	MainNetToday float64 `json:"mainNetToday"` // 当日主力净流入
	MainNet5D    float64 `json:"mainNet5D"`    // 近 5 日主力净流入合计
	MainNet20D   float64 `json:"mainNet20D"`   // 近 20 日主力净流入合计
	MoneyFlowDay int64   `json:"moneyFlowDay"` // 资金流最新日期

	// Boards 是所属板块名，按分类优先级排序（申万一级在最前）。
	//
	// 板块成分名单只有"当前"这一份（数据源没有历史版本），
	// 所以这是**当前**的板块归属，与 K 线显示的日期无关。
	Boards []string `json:"boards"`
}

// MoneyFlowPoint 是给图表用的资金流点（日期 + 各口径净流入）。
type MoneyFlowPoint struct {
	Date     int64   `json:"date"`
	MainNet  float64 `json:"mainNet"`  // 主力（超大单+大单）
	JumboNet float64 `json:"jumboNet"` // 超大单
	BigNet   float64 `json:"bigNet"`   // 大单
	MidNet   float64 `json:"midNet"`   // 中单
	SmallNet float64 `json:"smallNet"` // 小单
}

// KlineBundle 是 K 线页面一次加载所需的全部数据。
type KlineBundle struct {
	Code       string           `json:"code"`
	Name       string           `json:"name"`
	Adjust     string           `json:"adjust"`
	IsIndex    bool             `json:"isIndex"`
	Profile    StockProfile     `json:"profile"`
	Candles    []KlineCandle    `json:"candles"`
	Dividends  []stockdb.Factor `json:"dividends"`
	MoneyFlows []MoneyFlowPoint `json:"moneyFlows"`
	Notes      []string         `json:"notes"`
}

// ---------------------------------------------------------------------------
// 搜索索引
// ---------------------------------------------------------------------------

type searchEntry struct {
	code     string
	name     string
	pinyin   string
	initials string
	market   string
	marketCN string
	close    float64
	pctChg   float64
	isIndex  bool
}

// maxPinyinVariants 限制多音字组合的数量，避免长名字组合爆炸。
const maxPinyinVariants = 24

// pinyinVariants 返回名称的拼音全拼与拼音首字母，多音字的所有读法都会列出，
// 各写法之间用空格分隔。
//
// 为什么需要多读法：中文多音字很常见，例如「银行」的「行」读 háng 而不是 xíng。
// go-pinyin 默认只给最常见读音，于是「平安银行」会得到 payx，用户输入 payh 就搜不到。
// 这里把每种读法都列出来，任一读法都能命中。
func pinyinVariants(name string) (string, string) {
	if name == "" {
		return "", ""
	}

	args := pinyin.NewArgs()
	args.Heteronym = true
	perChar := pinyin.Pinyin(name, args)

	full := []string{""}
	initials := []string{""}
	for _, readings := range perChar {
		if len(readings) == 0 {
			continue
		}
		full = expandVariants(full, readings, false)
		initials = expandVariants(initials, readings, true)
	}

	return strings.ToLower(strings.Join(full, " ")), strings.ToLower(strings.Join(initials, " "))
}

func expandVariants(bases, readings []string, firstLetterOnly bool) []string {
	seen := make(map[string]struct{}, len(bases)*len(readings))
	out := make([]string, 0, len(bases)*len(readings))

	for _, base := range bases {
		for _, reading := range readings {
			piece := reading
			if firstLetterOnly {
				runes := []rune(reading)
				if len(runes) == 0 {
					continue
				}
				piece = string(runes[0])
			}
			value := base + piece
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
			if len(out) >= maxPinyinVariants {
				return out
			}
		}
	}
	return out
}

// firstVariant 取出多读法字符串里的第一个（最常见读音），用于展示。
func firstVariant(value string) string {
	if idx := strings.IndexByte(value, ' '); idx >= 0 {
		return value[:idx]
	}
	return value
}

// searchIndex 依据当前股票列表构建（并缓存）搜索索引。
func (s *StockService) searchIndex(universe *StockUniverse) []searchEntry {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	if s.searchIdx != nil && s.indexLoadedAt == universe.LoadedAt {
		return s.searchIdx
	}

	started := time.Now()
	index := make([]searchEntry, 0, len(universe.Stocks))
	for _, stock := range universe.Stocks {
		full, initials := pinyinVariants(stock.Name)
		index = append(index, searchEntry{
			code:     stock.Code,
			name:     stock.Name,
			pinyin:   full,
			initials: initials,
			market:   stock.Market,
			marketCN: stock.MarketCN,
			close:    stock.Close,
			pctChg:   stock.PctChg,
		})
	}

	s.searchIdx = index
	s.indexLoadedAt = universe.LoadedAt
	slog.Info("搜索索引已构建", "entries", len(index), "elapsed", time.Since(started).Round(time.Millisecond).String())
	return index
}

// matchScore 计算匹配分与命中方式，0 表示不匹配。
func matchScore(entry searchEntry, keyword string) (int, string) {
	name := strings.ToLower(entry.name)
	// ⚠ 代码也要转小写再比：关键词在 SearchStocks 里已被 lower，
	// 而**指数代码带交易所后缀**（000001.SH），不转小写就永远匹配不上。
	// 股票/基金代码是纯数字，转小写不影响它们。
	code := strings.ToLower(entry.code)

	if code == keyword {
		return 100, "code"
	}
	if strings.HasPrefix(code, keyword) {
		return 92, "code"
	}

	initials := strings.Fields(entry.initials)
	for _, variant := range initials {
		if variant == keyword {
			return 88, "initials"
		}
	}
	for _, variant := range initials {
		if strings.HasPrefix(variant, keyword) {
			return 82, "initials"
		}
	}

	if name == keyword {
		return 78, "name"
	}
	if strings.Contains(name, keyword) {
		return 72, "name"
	}

	full := strings.Fields(entry.pinyin)
	for _, variant := range full {
		if strings.HasPrefix(variant, keyword) {
			return 64, "pinyin"
		}
	}
	for _, variant := range full {
		if strings.Contains(variant, keyword) {
			return 55, "pinyin"
		}
	}
	return 0, ""
}

// SearchStocks 按代码 / 名称 / 拼音 / 拼音首字母搜索股票。
//
// **指数也在结果里**（带 `IsIndex` 标记）：K 线页要能查指数，
// 而指数不在股票列表里（数据湖的 instruments 只有 stock/etf/cdr），
// 只靠股票列表的索引是搜不到的。
func (s *StockService) SearchStocks(keyword string, limit int) ([]StockSearchItem, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return []StockSearchItem{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	universe, err := s.GetStockUniverse(false)
	if err != nil {
		return nil, err
	}
	index := s.searchIndex(universe)

	type hit struct {
		entry   searchEntry
		score   int
		matchOn string
	}
	hits := make([]hit, 0, 32)
	collect := func(entries []searchEntry) {
		for _, entry := range entries {
			if score, on := matchScore(entry, keyword); score > 0 {
				hits = append(hits, hit{entry: entry, score: score, matchOn: on})
			}
		}
	}
	collect(index)
	// 指数单独取（按需读一块缓存的指数历史），不能并进上面那份缓存：
	// 那份索引跟着股票列表的有效期走，而指数数据的更新节奏不同。
	collect(s.indexSearchEntries())

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		// 同分时股票优先：裸码 000001 既是平安银行也是上证指数的一部分，
		// 用户输入裸码时更可能想找股票（指数请带后缀，界面也是这么展示的）
		if hits[i].entry.isIndex != hits[j].entry.isIndex {
			return !hits[i].entry.isIndex
		}
		return hits[i].entry.code < hits[j].entry.code
	})

	if len(hits) > limit {
		hits = hits[:limit]
	}

	results := make([]StockSearchItem, 0, len(hits))
	for _, item := range hits {
		results = append(results, StockSearchItem{
			Code:     item.entry.code,
			Name:     item.entry.name,
			Market:   item.entry.market,
			MarketCN: item.entry.marketCN,
			Pinyin:   firstVariant(item.entry.pinyin),
			Initials: firstVariant(item.entry.initials),
			Close:    item.entry.close,
			PctChg:   item.entry.pctChg,
			MatchOn:  item.matchOn,
			IsIndex:  item.entry.isIndex,
		})
	}
	return results, nil
}

// indexSearchEntries 把数据湖里的大盘指数做成搜索条目。
//
// 代码保留**交易所后缀**（`000001.SH` 而不是 `000001`）：裸码会与股票撞车
// （上证指数与平安银行都是 000001），界面也按带后缀的写法展示与跳转。
func (s *StockService) indexSearchEntries() []searchEntry {
	series, _, _, err := s.loadMarketIndices()
	if err != nil || len(series) == 0 {
		// 指数搜不到不该让整个搜索失败（数据湖没开、index_bars 未入库都会走到这里）
		return nil
	}

	out := make([]searchEntry, 0, len(series))
	for _, item := range series {
		n := len(item.dates)
		if n == 0 {
			continue
		}
		entry := searchEntry{
			code:     item.meta.Code,
			name:     item.meta.Name,
			market:   "index",
			marketCN: "指数",
			close:    item.close[n-1],
			isIndex:  true,
		}
		if n >= 2 && item.close[n-2] > 0 {
			entry.pctChg = round((item.close[n-1]/item.close[n-2]-1)*100, 4)
		}
		entry.pinyin, entry.initials = pinyinVariants(item.meta.Name)
		out = append(out, entry)
	}
	return out
}

// ---------------------------------------------------------------------------
// K 线
// ---------------------------------------------------------------------------

// GetKline 返回某只股票（或大盘指数）的完整日 K 与简要信息。
// adjust 取值：none（不复权）/ qfq（前复权，默认）/ hfq（后复权）。
//
// **指数走另一条路**：数据湖的 `index_bars`，不是 `daily_bars`
// （free-stockdb 没有指数表），见 getIndexKline。
func (s *StockService) GetKline(code, adjust string, startYear int) (*KlineBundle, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("请先选择股票")
	}
	switch adjust {
	case "none", "qfq", "hfq":
	default:
		adjust = "qfq"
	}
	if startYear <= 0 || startYear > 2100 {
		startYear = 1990
	}

	// 指数与股票共用这一个入口：界面上是同一个 K 线页，搜索框返回的也是同一个结构。
	if symbol, ok := s.resolveIndexSymbol(code); ok {
		return s.getIndexKline(symbol, adjust, startYear)
	}

	provider, source := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tradeDate, err := s.cachedTradeDate(ctx, provider)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	quotes, err := provider.DailyHistory(ctx, code, startYear*10000+101, tradeDate)
	if err != nil {
		return nil, fmt.Errorf("读取日K失败：%w", err)
	}
	if len(quotes) == 0 {
		return nil, fmt.Errorf("没有 %s 的日K数据，请确认数据源 %s 已完成同步", code, source.URL)
	}

	// 复权因子取不到时退化为不复权，不影响出图
	factors, factorErr := provider.DividendFactors(ctx, code)
	if factorErr != nil {
		slog.Warn("读取复权/分红数据失败，按不复权处理", "code", code, "err", factorErr)
	}

	latest := quotes[len(quotes)-1]
	profile := buildProfile(code, latest, factors, tradeDate)

	// 所属板块（申万行业 + 概念）。板块是展示信息，取不到不影响 K 线本身。
	if names, err := s.boardsForCodes([]string{code}); err != nil {
		slog.Warn("读取板块名失败", "code", code, "err", err)
	} else {
		profile.Boards = names[code]
	}

	notes := []string{
		"ROE / EPS / BPS / 净利润 / 净资产 由日K的 PE_TTM 与 PB 推导（ROE = PB ÷ PE），TTM 口径、逐日更新，非财报口径。",
		"本地数据集不含财务报表（营收 / 毛利率 / 资产负债等）。",
	}
	if factorErr != nil {
		notes = append(notes, "未取到复权/分红数据，股息率与复权价格不可用。")
	}

	// 资金流（本机「资金流」表，缺失时忽略）。
	// 取与 K 线相同的时间范围：既要用来算当日/5日/20日摘要，也要给资金流图表用。
	var moneyFlows []MoneyFlowPoint
	flows, flowErr := provider.MoneyFlowRange(ctx, code, startYear*10000+101, tradeDate)
	if flowErr != nil {
		slog.Warn("读取资金流失败", "code", code, "err", flowErr)
	} else if len(flows) > 0 {
		last := flows[len(flows)-1]
		profile.MainNetToday = last.MainNet
		profile.MoneyFlowDay = last.Date
		for i := len(flows) - 1; i >= 0 && i >= len(flows)-5; i-- {
			profile.MainNet5D += flows[i].MainNet
		}
		for i := len(flows) - 1; i >= 0 && i >= len(flows)-20; i-- {
			profile.MainNet20D += flows[i].MainNet
		}

		moneyFlows = make([]MoneyFlowPoint, 0, len(flows))
		for _, f := range flows {
			moneyFlows = append(moneyFlows, MoneyFlowPoint{
				Date:     f.Date,
				MainNet:  f.MainNet,
				JumboNet: f.JumboNet,
				BigNet:   f.BigNet,
				MidNet:   f.MidNet,
				SmallNet: f.SmallNet,
			})
		}
	}

	bundle := &KlineBundle{
		Code:       code,
		Name:       latest.Name,
		Adjust:     adjust,
		Profile:    profile,
		Candles:    adjustCandles(quotes, factors, adjust),
		Dividends:  factors,
		MoneyFlows: moneyFlows,
		Notes:      notes,
	}

	slog.Info("K线数据就绪",
		"code", code, "name", latest.Name, "adjust", adjust,
		"candles", len(bundle.Candles), "dividends", len(factors),
		"elapsed", time.Since(started).Round(time.Millisecond).String(),
	)
	return bundle, nil
}

// resolveIndexSymbol 判断这个代码是不是**大盘指数**，是就返回带交易所后缀的指数代码。
//
// 三种输入：
//  1. `000001.SH`（带后缀）—— 指数与股票的重名只在这里能区分开，直接查指数表；
//  2. 裸码且**是股票/基金** —— 按股票走（`000001` 是平安银行，不是上证指数）；
//  3. 裸码但不是股票（`399001` 深证成指、`000300` 沪深300）—— 按指数走，
//     这样手输/收藏夹里的裸码也能打开。
func (s *StockService) resolveIndexSymbol(code string) (string, bool) {
	symbols, byBare := s.indexSymbolTable()
	if len(symbols) == 0 {
		return "", false
	}

	upper := strings.ToUpper(strings.TrimSpace(code))
	if strings.Contains(upper, ".") {
		if _, ok := symbols[upper]; ok {
			return upper, true
		}
		return "", false
	}

	bare := cnlake.BareSymbol(upper)
	if bare == "" {
		return "", false
	}
	full, ok := byBare[bare]
	if !ok {
		return "", false
	}

	// 裸码优先当股票：只有确认它不是股票/基金时才按指数处理。
	// 用一次点查询判断（stockdb 是一次键查询，数据湖是单标的区间读），
	// 比拉全量股票列表便宜得多。
	//
	// ⚠ 结果要缓存：`000001`（平安银行 / 上证指数）、`000016`（深康佳A / 上证50）、
	// `000905`（厦门港务 / 中证500）、`000688`（国城矿业 / 科创50）这几个裸码两边都有，
	// 不缓存的话每次打开这几只股票的 K 线都要多查一次。
	s.indexResolveMu.Lock()
	cached, hit := s.indexResolveCache[bare]
	s.indexResolveMu.Unlock()
	if hit {
		if cached == "" {
			return "", false
		}
		return cached, true
	}

	provider, _ := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tradeDate, err := s.cachedTradeDate(ctx, provider)
	if err != nil {
		return "", false
	}

	resolved := ""
	if quote, err := provider.QuotePoint(ctx, bare, tradeDate); err == nil && quote != nil {
		resolved = "" // 是股票/基金
	} else {
		resolved = full
	}

	s.indexResolveMu.Lock()
	if s.indexResolveCache == nil {
		s.indexResolveCache = map[string]string{}
	}
	s.indexResolveCache[bare] = resolved
	s.indexResolveMu.Unlock()

	return resolved, resolved != ""
}

// indexSymbolTable 返回指数代码表：完整代码 → 是否存在，以及裸码 → 完整代码。
//
// 表随指数历史一起缓存（30 分钟），所以搜索框每敲一个字都不会去读数据湖。
func (s *StockService) indexSymbolTable() (map[string]struct{}, map[string]string) {
	series, _, _, err := s.loadMarketIndices()
	if err != nil || len(series) == 0 {
		return nil, nil
	}
	symbols := make(map[string]struct{}, len(series))
	byBare := make(map[string]string, len(series))
	for _, item := range series {
		code := strings.ToUpper(item.meta.Code)
		symbols[code] = struct{}{}
		if bare := cnlake.BareSymbol(code); bare != "" {
			byBare[bare] = code
		}
	}
	return symbols, byBare
}

// getIndexKline 读取大盘指数的日 K。
//
// 与股票那条路的差别（都是数据本身决定的，不是省事）：
//   - 数据来自数据湖的 `index_bars`（free-stockdb 没有指数表，按年分区）；
//   - **没有复权**：指数不存在分红送转，qfq/hfq 与不复权完全一样（界面会提示）；
//   - **没有估值/财务/资金流/板块**：指数只有价格与成交量，PE/PB/市值/换手率
//     这些是"个股 + 估值表"才有的东西，所以全部留 0 并在提示里说清楚，
//     而不是编一个看起来像模像样的数字。
func (s *StockService) getIndexKline(symbol, adjust string, startYear int) (*KlineBundle, error) {
	lake := s.lakeHandle()
	if lake == nil || !lake.HasIndexBars() {
		return nil, fmt.Errorf("指数行情只在本地数据湖的 index_bars 里（free-stockdb 没有指数表），"+
			"请确认数据源已切到数据湖且 index_bars 已同步：%s", symbol)
	}

	started := time.Now()
	rows, err := lake.IndexBars(symbol, startYear, 0)
	if err != nil {
		return nil, fmt.Errorf("读取指数 %s 的日线失败：%w", symbol, err)
	}

	candles := make([]KlineCandle, 0, len(rows))
	for _, row := range rows {
		date := row.Date()
		if date <= 0 || row.Close <= 0 {
			continue
		}
		if date < int64(startYear*10000+101) {
			continue
		}
		candles = append(candles, KlineCandle{
			Date:   date,
			Open:   round(row.Open, 4),
			High:   round(row.High, 4),
			Low:    round(row.Low, 4),
			Close:  round(row.Close, 4),
			Volume: row.Volume,
			Amount: row.Amount,
		})
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("数据湖里没有指数 %s 在 %d 年之后的日线", symbol, startYear)
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].Date < candles[j].Date })

	latest := candles[len(candles)-1]
	profile := StockProfile{
		Code:      symbol,
		Name:      indexDisplayName(symbol),
		Market:    "index",
		MarketCN:  "指数",
		TradeDate: latest.Date,
		Open:      latest.Open,
		High:      latest.High,
		Low:       latest.Low,
		Close:     latest.Close,
		Volume:    latest.Volume,
		Amount:    latest.Amount,
	}
	if len(candles) >= 2 {
		prev := candles[len(candles)-2].Close
		if prev > 0 {
			profile.PreClose = prev
			profile.PctChg = round((latest.Close/prev-1)*100, 4)
			profile.Amplitude = round((latest.High-latest.Low)/prev*100, 4)
		}
	}

	bundle := &KlineBundle{
		Code:      symbol,
		Name:      profile.Name,
		Adjust:    adjust,
		IsIndex:   true,
		Profile:   profile,
		Candles:   candles,
		Dividends: []stockdb.Factor{},
		Notes: []string{
			"指数行情来自本地数据湖的 index_bars（free-stockdb 没有指数表）。",
			"指数没有 PE / PB / 市值 / 股本 / 换手率 / 资金流 / 板块归属 —— 这些是「个股 + 估值表」才有的数据，这里不会编。",
			"指数不存在分红送转，因此**没有复权概念**：前复权 / 后复权与不复权的曲线完全一样。",
		},
	}

	slog.Info("指数K线数据就绪",
		"symbol", symbol, "name", profile.Name, "candles", len(candles),
		"start", candles[0].Date, "end", latest.Date,
		"elapsed", time.Since(started).Round(time.Millisecond).String())
	return bundle, nil
}

// indexDisplayName 返回指数的中文名（注册表里没有就用代码）。
func indexDisplayName(symbol string) string {
	upper := strings.ToUpper(strings.TrimSpace(symbol))
	for _, meta := range majorIndices {
		if strings.ToUpper(meta.Code) == upper {
			return meta.Name
		}
	}
	return upper
}

// adjustCandles 按复权因子折算价格。算法与官方 Python SDK 保持一致：
// 前复权 ratio = f最新 / f当期，后复权 ratio = 1 / f当期，价格 = 原价 / ratio。
func adjustCandles(quotes []*stockdb.Quote, factors []stockdb.Factor, mode string) []KlineCandle {
	out := make([]KlineCandle, 0, len(quotes))

	if mode == "none" || len(factors) == 0 {
		for _, q := range quotes {
			out = append(out, candleOf(q, 1))
		}
		return out
	}

	latestFactor := factors[len(factors)-1].Cum
	if latestFactor == 0 {
		latestFactor = 1
	}

	cursor := 0
	for _, q := range quotes {
		for cursor+1 < len(factors) && factors[cursor+1].Date <= q.Date {
			cursor++
		}
		current := 1.0
		if factors[cursor].Date <= q.Date && factors[cursor].Cum != 0 {
			current = factors[cursor].Cum
		}

		ratio := latestFactor / current
		if mode == "hfq" {
			ratio = 1 / current
		}
		out = append(out, candleOf(q, ratio))
	}
	return out
}

func candleOf(q *stockdb.Quote, ratio float64) KlineCandle {
	if ratio == 0 {
		ratio = 1
	}
	round := func(v float64) float64 { return math.Round(v/ratio*10000) / 10000 }
	return KlineCandle{
		Date:   q.Date,
		Open:   round(q.Open),
		High:   round(q.High),
		Low:    round(q.Low),
		Close:  round(q.Close),
		Volume: q.Volume,
		Amount: q.Amount,
	}
}

func buildProfile(code string, latest *stockdb.Quote, factors []stockdb.Factor, tradeDate int) StockProfile {
	marketKey := market.KeyOf(code)

	profile := StockProfile{
		Code:       code,
		Name:       latest.Name,
		Market:     marketKey,
		MarketCN:   market.NameOf(marketKey),
		TradeDate:  latest.Date,
		Open:       latest.Open,
		High:       latest.High,
		Low:        latest.Low,
		Close:      latest.Close,
		PreClose:   latest.PreClose,
		PctChg:     latest.PctChg,
		Amplitude:  latest.Amplitude,
		Volume:     latest.Volume,
		Amount:     latest.Amount,
		Turnover:   latest.Turnover,
		VolRatio:   latest.VolRatio,
		PeTTM:      latest.PeTTM,
		PB:         latest.PB,
		TotalMV:    latest.TotalMV,
		FloatMV:    latest.FloatMV,
		TotalShare: latest.TotalShare,
		FloatShare: latest.FloatShare,
		IsST:       latest.IsST,
	}

	// 股息率：最近 12 个月的每股派息合计 / 最新收盘价。
	// YYYYMMDD 形式的日期减去 10000 即约一年前。
	cutoff := int64(tradeDate - 10000)
	ttm := 0.0
	for _, f := range factors {
		if f.Date > cutoff {
			ttm += f.Div
		}
	}
	profile.DividendTTM = math.Round(ttm*10000) / 10000
	profile.DividendCount = len(factors)
	if profile.Close > 0 && ttm > 0 {
		profile.DividendYield = math.Round(ttm/profile.Close*10000) / 100
	}
	if len(factors) > 0 {
		last := factors[len(factors)-1]
		profile.LatestDiv = last.Div
		profile.LatestDivDate = last.Date
	}

	// TTM 口径的财务指标：由 PE_TTM / PB / 总市值推导，不依赖财报
	derived := deriveFundamentals(latest)
	profile.ROE = derived.ROE
	profile.EPS = derived.EPS
	profile.BPS = derived.BPS
	profile.NetProfitTTM = derived.NetProfitTTM
	profile.NetAssets = derived.NetAssets

	return profile
}

// cachedTradeDate 取最新交易日（缓存 30 分钟）。
func (s *StockService) cachedTradeDate(ctx context.Context, provider Provider) (int, error) {
	s.tradeDateMu.Lock()
	if s.tradeDate > 0 && time.Since(s.tradeDateAt) < 30*time.Minute {
		date := s.tradeDate
		s.tradeDateMu.Unlock()
		return date, nil
	}
	s.tradeDateMu.Unlock()

	date, err := provider.LatestTradeDate(ctx)
	if err != nil {
		return 0, err
	}

	s.tradeDateMu.Lock()
	s.tradeDate = date
	s.tradeDateAt = time.Now()
	s.tradeDateMu.Unlock()
	return date, nil
}
