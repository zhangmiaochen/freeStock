package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/market"
	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 条件选股
//
// 条件逻辑移植自 D:\workspace\python\stock\tushare-stock-tonyz 的
// StockSelector.filter_stocks（44 个参数 + apply_filter_post_filters 后置过滤）。
//
// 移植时做了两处必要调整：
//
//  1. **板块/行业条件换成 FreeStock 可用的数据**。原项目用同花顺/东财的
//     行业与概念（ths_industry / ths_concept / em_industry / em_concept），
//     这里换成 FreeStock 已有的 申万一级 / 申万二级 / 申万三级 / 概念。
//  2. **按数据可得性裁剪条件**。原项目依赖的市销率(PS)、机构参与度、季报增长、
//     相对强度(RS) 在 FreeStock 的数据源里没有对应字段，
//     接口会通过 Notes 明确说明"哪些条件没生效"，而不是静默忽略。
//
// 分层执行（这是性能关键）：
//
//	快照层（0 成本，股票列表已在内存）
//	  → 板块层（0 成本，板块数据已缓存）
//	    → 技术层（要拉 K 线，只对前两层通过者执行）
//
// 如果技术层也对全市场执行，7500 只逐只拉日线要几分钟。
// ---------------------------------------------------------------------------

// MACondition 一条均线比较条件，例如 MA5 > MA10。
// 多条之间是 AND（与原项目一致）。
type MACondition struct {
	Fast int    `json:"fast"`
	Op   string `json:"op"` // gt / lt
	Slow int    `json:"slow"`
}

// BoardFilter 一条板块条件。
//
// 原项目的 board_filters 是按"数据源"分组的（同花顺行业/概念、东财行业/概念），
// 同一数据源内 and/or、不同数据源之间 OR。这里简化为：
// 一条条件 = 一个分类 + 若干板块名 + 匹配模式；**多条条件之间是 AND**，
// 因为"医药生物 且 疫苗概念"这种求交集才是常见诉求。
type BoardFilter struct {
	// Category 是板块分类名：申万一级 / 申万二级 / 申万三级 / 概念
	Category string   `json:"category"`
	Names    []string `json:"names"`
	// Mode：any=属于其中任一（并集，默认）；all=必须同时属于全部（交集）
	Mode string `json:"mode"`
}

// ScreenCondition 是一整套筛选条件。零值表示"该条件不启用"。
type ScreenCondition struct {
	// ---- 快照层（单位均按界面口径，后端负责换算） ----
	MinTotalMV     float64 `json:"minTotalMV"` // 亿元
	MaxTotalMV     float64 `json:"maxTotalMV"`
	MinFloatMV     float64 `json:"minFloatMV"` // 亿元
	MaxFloatMV     float64 `json:"maxFloatMV"`
	MinPE          float64 `json:"minPE"`
	MaxPE          float64 `json:"maxPE"`
	MinPB          float64 `json:"minPB"`
	MaxPB          float64 `json:"maxPB"`
	MinROE         float64 `json:"minROE"` // %
	MaxROE         float64 `json:"maxROE"`
	MinTurnover    float64 `json:"minTurnover"` // %
	MaxTurnover    float64 `json:"maxTurnover"`
	MinPctChg      float64 `json:"minPctChg"` // %
	MaxPctChg      float64 `json:"maxPctChg"`
	MinVolumeRatio float64 `json:"minVolumeRatio"`
	MaxVolumeRatio float64 `json:"maxVolumeRatio"`
	MinAmount      float64 `json:"minAmount"` // 亿元
	MaxAmount      float64 `json:"maxAmount"`

	ExcludeST    bool     `json:"excludeST"`
	Markets      []string `json:"markets"`
	HasQuoteOnly bool     `json:"hasQuoteOnly"`

	// ---- 板块层 ----
	BoardFilters []BoardFilter `json:"boardFilters"`

	// ---- 技术层（要拉 K 线） ----
	MAConditions   []MACondition `json:"maConditions"`
	EnableDrawdown bool          `json:"enableDrawdown"`
	DrawdownDays   int           `json:"drawdownDays"`
	MinDrawdown    float64       `json:"minDrawdown"` // %
	MaxDrawdown    float64       `json:"maxDrawdown"` // %

	// Limit 限制返回条数，<=0 表示不限
	Limit int `json:"limit"`
}

// MAValue 均线取值，用切片而不是 map —— Wails 生成 map 绑定的体验很差。
type MAValue struct {
	Period int     `json:"period"`
	Value  float64 `json:"value"`
}

// ScreenItem 是筛选结果的一行。
type ScreenItem struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Market   string `json:"market"`
	MarketCN string `json:"marketCN"`

	Close       float64 `json:"close"`
	PctChg      float64 `json:"pctChg"`
	Amount      float64 `json:"amount"`
	Turnover    float64 `json:"turnover"`
	VolumeRatio float64 `json:"volumeRatio"`
	PeTTM       float64 `json:"peTTM"`
	PB          float64 `json:"pb"`
	ROE         float64 `json:"roe"`
	TotalMV     float64 `json:"totalMV"`
	FloatMV     float64 `json:"floatMV"`
	IsST        bool    `json:"isST"`

	// Boards 是命中的板块名（用于回答"它为什么被选出来"）
	Boards []string `json:"boards"`

	// 技术层指标（仅在启用对应条件时计算）
	DrawdownPct float64   `json:"drawdownPct"`
	MAValues    []MAValue `json:"maValues"`
}

// ScreenResult 是一次筛选的完整结果。
type ScreenResult struct {
	Items     []ScreenItem `json:"items"`
	TradeDate int64        `json:"tradeDate"`

	// Scanned 参与筛选的标的数
	Scanned int `json:"scanned"`
	// SnapshotPassed 通过快照层的数量
	SnapshotPassed int `json:"snapshotPassed"`
	// BoardPassed 再通过板块层的数量
	BoardPassed int `json:"boardPassed"`
	// TechPassed 再通过技术层的数量（= 最终命中数）
	TechPassed int `json:"techPassed"`
	Matched    int `json:"matched"`
	// HistoryEvaluated 表示技术层确实执行了（要拉 K 线）
	HistoryEvaluated bool `json:"historyEvaluated"`

	Elapsed string `json:"elapsed"`
	// Notes 说明哪些条件没有生效、为什么 —— 不能静默忽略
	Notes []string `json:"notes"`
}

// UnsupportedScreenConditions 是数据源不支持、因此无法移植的条件。
// 单独列出来是为了让界面能明确告诉用户"这些条件去哪了"。
var UnsupportedScreenConditions = []struct {
	Name   string
	Reason string
}{
	{"市销率 PS", "free-stockdb 的日K没有市销率字段，估值表只有 PE_TTM / PB / 市值"},
	{"机构参与度", "数据源没有股东与机构持仓数据（十大股东在 cnequity 数据湖里，尚未入库）"},
	{"季报增长", "本地数据集不含财务报表科目；需要 cnequity 数据湖的 financial_statement_items 入库后才能算"},
	{"相对强度 RS", "需要指数行情做对标基准，free-stockdb 只提供个股与基金，没有指数日线"},
}

// ScreenStocks 执行条件选股。
func (s *StockService) ScreenStocks(cond ScreenCondition) (*ScreenResult, error) {
	started := time.Now()

	provider, _ := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	universe, err := s.GetStockUniverse(false)
	if err != nil {
		return nil, err
	}
	_ = provider
	result := &ScreenResult{
		TradeDate: universe.TradeDate,
		Scanned:   len(universe.Stocks),
	}

	// ---- 第 1 层：快照条件（纯内存，0 额外成本） ----
	passed := make([]StockItem, 0, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if matchSnapshot(item, cond) {
			passed = append(passed, item)
		}
	}
	result.SnapshotPassed = len(passed)

	// ---- 第 2 层：板块条件 ----
	var boardIndex *boardMembership
	if len(cond.BoardFilters) > 0 {
		boardIndex, err = s.buildBoardMembership(cond.BoardFilters)
		if err != nil {
			return nil, err
		}
		if len(boardIndex.missing) > 0 {
			result.Notes = append(result.Notes, fmt.Sprintf(
				"以下板块没有找到，已忽略：%s", strings.Join(boardIndex.missing, "、")))
		}
		if len(boardIndex.notes) > 0 {
			result.Notes = append(result.Notes, boardIndex.notes...)
		}

		kept := passed[:0]
		for _, item := range passed {
			if boardIndex.match(item.Code) {
				kept = append(kept, item)
			}
		}
		passed = kept
	}
	result.BoardPassed = len(passed)

	// ---- 第 3 层：技术条件（要拉 K 线，只对幸存者做） ----
	var techIndex map[string]screenTech
	needsHistory := len(cond.MAConditions) > 0 || cond.EnableDrawdown
	if needsHistory {
		var techNotes []string
		passed, techIndex, techNotes = s.applyHistoryConditions(ctx, passed, cond)
		result.Notes = append(result.Notes, techNotes...)
		result.HistoryEvaluated = true
	}
	result.TechPassed = len(passed)

	// ---- 组装结果 ----
	items := make([]ScreenItem, 0, len(passed))
	for _, item := range passed {
		row := ScreenItem{
			Code:        item.Code,
			Name:        item.Name,
			Market:      item.Market,
			MarketCN:    item.MarketCN,
			Close:       item.Close,
			PctChg:      item.PctChg,
			Amount:      item.Amount,
			Turnover:    item.Turnover,
			VolumeRatio: item.VolRatio,
			PeTTM:       item.PeTTM,
			PB:          item.PB,
			TotalMV:     item.TotalMV,
			FloatMV:     item.FloatMV,
			IsST:        item.IsST,
		}
		row.ROE = derivedROE(item)
		if boardIndex != nil {
			row.Boards = boardIndex.namesFor(item.Code)
		}
		if tech, ok := techIndex[item.Code]; ok {
			row.DrawdownPct = tech.drawdown
			row.MAValues = tech.maValues
		}
		items = append(items, row)
	}

	// 原项目的 filter_stocks 不做排序，结果按代码升序。
	// 这里保持同样的确定性顺序，界面自己提供交互排序。
	sort.Slice(items, func(i, j int) bool { return items[i].Code < items[j].Code })

	result.Matched = len(items)
	if cond.Limit > 0 && len(items) > cond.Limit {
		items = items[:cond.Limit]
	}
	result.Items = items
	result.Elapsed = time.Since(started).Round(time.Millisecond).String()

	// 未生效的条件的提示：只有在用户确实用到了不可移植的条件时才提
	// （这些条件在界面上是灰的，这里只是兜底说明）

	slog.Info("条件选股完成",
		"scanned", result.Scanned, "snapshot", result.SnapshotPassed,
		"board", result.BoardPassed, "matched", result.Matched, "elapsed", result.Elapsed)
	return result, nil
}

// derivedROE 由 PE/PB 推导 ROE（TTM 口径）——见 finance.go 的说明。
func derivedROE(item StockItem) float64 {
	if item.PeTTM == 0 || item.PB == 0 {
		return 0
	}
	return round(item.PB/item.PeTTM*100, 4)
}

// matchSnapshot 判断一只股票是否通过快照层条件。
//
// 与原项目一致：某个字段缺失（值为 0）时**跳过该条件**而不是判为不满足，
// 否则会把"数据没同步"误判成"不符合条件"。
func matchSnapshot(item StockItem, cond ScreenCondition) bool {
	if cond.ExcludeST && (item.IsST || isSTName(item.Name)) {
		return false
	}
	if cond.HasQuoteOnly && !item.HasQuote {
		return false
	}
	if len(cond.Markets) > 0 {
		found := false
		for _, market := range cond.Markets {
			if item.Market == market {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// 市值：界面单位是亿元
	if !within(item.TotalMV/1e8, cond.MinTotalMV, cond.MaxTotalMV, item.TotalMV == 0) {
		return false
	}
	if !within(item.FloatMV/1e8, cond.MinFloatMV, cond.MaxFloatMV, item.FloatMV == 0) {
		return false
	}
	// 成交额：界面单位是亿元
	if !within(item.Amount/1e8, cond.MinAmount, cond.MaxAmount, item.Amount == 0) {
		return false
	}
	if !within(item.PeTTM, cond.MinPE, cond.MaxPE, item.PeTTM == 0) {
		return false
	}
	if !within(item.PB, cond.MinPB, cond.MaxPB, item.PB == 0) {
		return false
	}
	if !within(item.Turnover, cond.MinTurnover, cond.MaxTurnover, item.Turnover == 0) {
		return false
	}
	if !within(item.PctChg, cond.MinPctChg, cond.MaxPctChg, false) {
		return false
	}
	if !within(item.VolRatio, cond.MinVolumeRatio, cond.MaxVolumeRatio, item.VolRatio == 0) {
		return false
	}

	// ROE 由 PE/PB 推导；两个都缺就没法算，跳过而不是判不满足
	roe := derivedROE(item)
	if !within(roe, cond.MinROE, cond.MaxROE, roe == 0) {
		return false
	}
	// 创业板 / 科创板 允许 -20% 涨跌幅，主板 ±10%，这里不做涨跌停校验，
	// 因为区间条件本身已经限定了范围。
	return true
}

// within 判断 value 是否落在 [min, max] 内（0 表示该端不限制）。
// skip 为 true 时表示数据缺失，直接放行 —— 与原项目"字段缺失则跳过"一致。
func within(value, min, max float64, skip bool) bool {
	if min == 0 && max == 0 {
		return true
	}
	if skip || math.IsNaN(value) || math.IsInf(value, 0) {
		return true
	}
	if min != 0 && value < min {
		return false
	}
	if max != 0 && value > max {
		return false
	}
	return true
}

func isSTName(name string) bool {
	upper := strings.ToUpper(name)
	return strings.Contains(upper, "ST")
}

// ---------------------------------------------------------------------------
// 板块层
// ---------------------------------------------------------------------------

// boardMembership 把"股票 → 板块"的匹配预先算好。
//
// 每条 BoardFilter 变成一个代码集合；一只股票要通过全部条件
// （多条条件之间 AND）。
type boardMembership struct {
	// filters[i] 是第 i 条条件允许的代码集合
	filters []map[string]struct{}
	// 代码 → 命中的板块名（仅用于结果展示）
	names map[string][]string
	// 没找到的板块名
	missing []string
	notes   []string
}

func (b *boardMembership) match(code string) bool {
	for _, allowed := range b.filters {
		if _, ok := allowed[code]; !ok {
			return false
		}
	}
	return true
}

func (b *boardMembership) namesFor(code string) []string {
	return b.names[code]
}

// buildBoardMembership 根据板块条件构建匹配索引。
func (s *StockService) buildBoardMembership(filters []BoardFilter) (*boardMembership, error) {
	boards, err := s.loadBoards()
	if err != nil {
		return nil, err
	}

	// (分类, 名称) -> 成分代码集合
	byKey := make(map[string]map[string]struct{}, len(boards))
	// 代码 -> 板块名（分类限定在条件用到的分类里）
	for _, board := range boards {
		key := board.Category + "\x00" + board.Name
		set := byKey[key]
		if set == nil {
			set = make(map[string]struct{}, len(board.Symbols))
			byKey[key] = set
		}
		// 同名板块可能有多条（不同 code），成分取并集
		for _, symbol := range board.Symbols {
			set[symbol] = struct{}{}
		}
	}

	out := &boardMembership{
		names: make(map[string][]string),
	}
	// 哪些分类被条件引用（用于限定 names 的收集范围）
	wantedCategories := make(map[string]struct{}, len(filters))

	for _, filter := range filters {
		if filter.Category == "" || len(filter.Names) == 0 {
			continue
		}
		wantedCategories[filter.Category] = struct{}{}

		// 每个名称先各自求成分集合
		perName := make([]map[string]struct{}, 0, len(filter.Names))
		for _, name := range filter.Names {
			set, ok := byKey[filter.Category+"\x00"+name]
			if !ok || len(set) == 0 {
				out.missing = append(out.missing, filter.Category+" / "+name)
				continue
			}
			perName = append(perName, set)
		}
		if len(perName) == 0 {
			// 一个都没匹配上：条件无法满足，用空集合让结果为空
			out.filters = append(out.filters, map[string]struct{}{})
			continue
		}

		merged := map[string]struct{}{}
		if filter.Mode == "all" {
			// 交集：以第一个集合为基准逐个求交
			for code := range perName[0] {
				inAll := true
				for _, other := range perName[1:] {
					if _, ok := other[code]; !ok {
						inAll = false
						break
					}
				}
				if inAll {
					merged[code] = struct{}{}
				}
			}
		} else {
			// 并集
			for _, set := range perName {
				for code := range set {
					merged[code] = struct{}{}
				}
			}
		}
		out.filters = append(out.filters, merged)
	}

	// 收集展示用的板块名：只对最终可能命中的代码回填
	for _, board := range boards {
		if _, ok := wantedCategories[board.Category]; !ok {
			continue
		}
		for _, symbol := range board.Symbols {
			out.names[symbol] = appendUnique(out.names[symbol], board.Name)
		}
	}

	if len(out.filters) == 0 {
		out.notes = append(out.notes, "板块条件没有生效：没有指定任何板块")
	}
	return out, nil
}

func appendUnique(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

// ---------------------------------------------------------------------------
// 技术层
// ---------------------------------------------------------------------------

// screenTech 是技术层算出来的指标缓存，供结果组装时取用。
type screenTech struct {
	drawdown float64
	maValues []MAValue
}

// applyHistoryConditions 对已通过前两层的股票拉日线，应用均线与回撤条件，
// 返回通过者、算出的指标缓存、以及需要告诉用户的提示。
//
// 这一步是筛选里唯一"贵"的部分，所以放在最后，且只处理幸存者。
// 原项目也是这个顺序（基础面 → 历史指标），并且一次批量拉取共享窗口。
func (s *StockService) applyHistoryConditions(
	ctx context.Context,
	passed []StockItem,
	cond ScreenCondition,
) ([]StockItem, map[string]screenTech, []string) {
	tech := make(map[string]screenTech, len(passed))
	if len(passed) == 0 {
		return passed, tech, nil
	}

	var notes []string

	// 窗口要覆盖最长均线与回撤天数，再留缓冲应对停牌与节假日
	days := 60
	for _, ma := range cond.MAConditions {
		if ma.Fast > days {
			days = ma.Fast
		}
		if ma.Slow > days {
			days = ma.Slow
		}
	}
	if cond.EnableDrawdown {
		if cond.DrawdownDays < 2 {
			notes = append(notes, "阶段回撤的窗口小于 2 个交易日，该条件已忽略")
		} else if cond.DrawdownDays > days {
			days = cond.DrawdownDays
		}
	}
	calendarDays := days*3 + 30

	provider, _ := s.snapshot()
	tradeDate, err := s.cachedTradeDate(ctx, provider)
	if err != nil {
		notes = append(notes, "取交易日失败，技术条件已跳过："+err.Error())
		return passed, tech, notes
	}
	start := shiftBack(tradeDate, calendarDays)

	s.emit("screen:progress", Progress{Phase: "history", Total: len(passed)})

	var (
		done   atomic.Int64
		mu     sync.Mutex
		kept   = make([]StockItem, 0, len(passed))
		failed int
	)
	runWorkers(ctx, fetchWorkers, len(passed), func(i int) {
		item := passed[i]
		quotes, err := provider.DailyHistory(ctx, item.Code, start, tradeDate)

		// 形态计算放在锁外：evaluateHistory 是纯函数，不需要保护。
		// 若放锁内，16 个 worker 的计算会被串行化，也白白拉长临界区。
		matched := false
		var value screenTech
		if err == nil && len(quotes) > 0 {
			if v, ok := evaluateHistory(quotes, cond); ok {
				value, matched = v, true
			}
		}

		mu.Lock()
		if err != nil || len(quotes) == 0 {
			failed++
		} else if matched {
			kept = append(kept, item)
			tech[item.Code] = value
		}
		n := done.Add(1)
		mu.Unlock()

		// 进度事件在临界区外发 —— 持锁调用外部事件系统是不必要的风险
		if int(n)%progressInterval == 0 || int(n) == len(passed) {
			s.emit("screen:progress", Progress{Phase: "history", Done: int(n), Total: len(passed)})
		}
	})

	if failed > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d 只股票没有取到日线（停牌 / 退市 / 未同步），已排除在技术条件之外", failed))
	}
	if len(kept) == 0 && len(passed) > 0 {
		notes = append(notes, "技术条件没有筛出任何股票，可放宽均线或回撤区间")
	}
	return kept, tech, notes
}

// evaluateHistory 判断一只股票是否满足均线与回撤条件，并算出要展示的指标。
func evaluateHistory(quotes []*stockdb.Quote, cond ScreenCondition) (screenTech, bool) {
	closes := make([]float64, 0, len(quotes))
	highs := make([]float64, 0, len(quotes))
	for _, q := range quotes {
		if q == nil || q.Close <= 0 {
			continue
		}
		closes = append(closes, q.Close)
		highs = append(highs, q.High)
	}
	if len(closes) == 0 {
		return screenTech{}, false
	}

	tech := screenTech{}

	// 均线条件：多条 AND
	if len(cond.MAConditions) > 0 {
		tech.maValues = make([]MAValue, 0, len(cond.MAConditions))
		for _, ma := range cond.MAConditions {
			if ma.Fast <= 0 || ma.Slow <= 0 || ma.Fast == ma.Slow {
				continue
			}
			fast, okFast := movingAverage(closes, ma.Fast)
			slow, okSlow := movingAverage(closes, ma.Slow)
			if !okFast || !okSlow {
				// 数据不足以算这条均线（新股），判为不满足而不是放行 ——
				// 均线条件本质是形态判断，算不出来就不该算命中
				return screenTech{}, false
			}
			tech.maValues = append(tech.maValues, MAValue{Period: ma.Fast, Value: fast})
			tech.maValues = append(tech.maValues, MAValue{Period: ma.Slow, Value: slow})

			switch ma.Op {
			case "lt":
				if fast >= slow {
					return screenTech{}, false
				}
			default: // gt
				if fast <= slow {
					return screenTech{}, false
				}
			}
		}
	}

	// 阶段回撤：(窗口内最高 - 最新收盘) / 窗口内最高 × 100
	if cond.EnableDrawdown && cond.DrawdownDays >= 2 {
		window := cond.DrawdownDays
		if window > len(highs) {
			window = len(highs)
		}
		if window < 2 {
			return screenTech{}, false
		}
		recentHigh := highs[len(highs)-window]
		for _, high := range highs[len(highs)-window:] {
			if high > recentHigh {
				recentHigh = high
			}
		}
		latest := closes[len(closes)-1]
		if recentHigh <= 0 {
			return screenTech{}, false
		}
		drawdown := round((recentHigh-latest)/recentHigh*100, 4)
		tech.drawdown = drawdown
		if cond.MinDrawdown != 0 && drawdown < cond.MinDrawdown {
			return screenTech{}, false
		}
		if cond.MaxDrawdown != 0 && drawdown > cond.MaxDrawdown {
			return screenTech{}, false
		}
	}

	return tech, true
}

// movingAverage 算最近 period 根的收盘均价；数据不足时返回 false。
func movingAverage(closes []float64, period int) (float64, bool) {
	if period <= 0 || len(closes) < period {
		return 0, false
	}
	sum := 0.0
	for _, close := range closes[len(closes)-period:] {
		sum += close
	}
	return round(sum/float64(period), 4), true
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// ScreenConditionInfo 是筛选条件的读取结果。
// 用结构体而不是 Go 的多返回值：Wails 会把多返回值变成元组，前端用起来别扭。
type ScreenConditionInfo struct {
	Condition ScreenCondition `json:"condition"`
	// Saved 表示配置里确实存过条件（false = 返回的是默认值）
	Saved bool `json:"saved"`
}

// GetScreenCondition 读取上次使用的筛选条件；从未保存过时返回默认值。
func (s *StockService) GetScreenCondition() ScreenConditionInfo {
	defaults := ScreenCondition{ExcludeST: true, HasQuoteOnly: true, DrawdownDays: 30}
	if s.cfg == nil {
		return ScreenConditionInfo{Condition: defaults}
	}
	raw := s.cfg.ScreenPrefs()
	if len(raw) == 0 {
		return ScreenConditionInfo{Condition: defaults}
	}
	var cond ScreenCondition
	if err := json.Unmarshal(raw, &cond); err != nil {
		slog.Warn("筛选条件解析失败，回退默认值", "err", err)
		return ScreenConditionInfo{Condition: defaults}
	}
	return ScreenConditionInfo{Condition: cond, Saved: true}
}

// SaveScreenCondition 保存筛选条件到配置文件。
func (s *StockService) SaveScreenCondition(cond ScreenCondition) error {
	if s.cfg == nil {
		return fmt.Errorf("没有可用的配置文件，筛选条件无法保存")
	}
	raw, err := json.Marshal(cond)
	if err != nil {
		return err
	}
	s.cfg.SetScreenPrefs(raw)
	return nil
}

// ScreenCategories 返回可用于板块条件的分类（供界面下拉）。
func (s *StockService) ScreenCategories() ([]BoardCategoryInfo, error) {
	return s.GetBoardCategories()
}

// ScreenOptions 汇总界面需要知道的信息：可选分类、支持的市场、不支持的条件。
type ScreenOptions struct {
	Categories []BoardCategoryInfo `json:"categories"`
	Markets    []MarketInfo        `json:"markets"`
	// Unsupported 列出数据源不支持、因此无法移植的条件及原因
	Unsupported []string `json:"unsupported"`
	// Defaults 是首次使用时的默认条件
	Defaults ScreenCondition `json:"defaults"`
}

// GetScreenOptions 返回条件选股界面需要的元信息。
func (s *StockService) GetScreenOptions() (*ScreenOptions, error) {
	categories, err := s.GetBoardCategories()
	if err != nil {
		return nil, err
	}

	out := &ScreenOptions{
		Categories: categories,
		Markets:    marketInfos(),
		Defaults: ScreenCondition{
			ExcludeST:    true,
			HasQuoteOnly: true,
			DrawdownDays: 30,
		},
	}
	for _, item := range UnsupportedScreenConditions {
		out.Unsupported = append(out.Unsupported, item.Name+"："+item.Reason)
	}
	return out, nil
}

// marketInfos 列出全部市场分类（不带数量），供界面做多选。
func marketInfos() []MarketInfo {
	out := make([]MarketInfo, 0, len(market.Definitions))
	for _, def := range market.Definitions {
		out = append(out, MarketInfo{Key: def.Key, Name: def.Name, Group: def.Group})
	}
	return out
}
