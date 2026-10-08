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

	if entry.code == keyword {
		return 100, "code"
	}
	if strings.HasPrefix(entry.code, keyword) {
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
	for _, entry := range index {
		if score, on := matchScore(entry, keyword); score > 0 {
			hits = append(hits, hit{entry: entry, score: score, matchOn: on})
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
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
		})
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// K 线
// ---------------------------------------------------------------------------

// GetKline 返回某只股票的完整日 K（含复权）与简要信息。
// adjust 取值：none（不复权）/ qfq（前复权，默认）/ hfq（后复权）。
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
