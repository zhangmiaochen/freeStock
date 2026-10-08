package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/cnlake"
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

	// ---- 数据湖层 ----
	//
	// 这四项 free-stockdb 没有对应字段，只有 cnequity 数据湖才有。
	// 数据集尚未入库时条件会被**跳过**并在 Notes 里说明原因，不会报错。

	// MinPS/MaxPS 市销率（valuation_metrics.ps_ttm）
	MinPS float64 `json:"minPS"`
	MaxPS float64 `json:"maxPS"`
	// MinInstitution/MaxInstitution 机构参与度 %：
	// top_holders 里 is_institution 的**流通股东**持股比例合计
	MinInstitution float64 `json:"minInstitution"`
	MaxInstitution float64 `json:"maxInstitution"`
	// MinQuarterGrowth 单季归母净利润同比增长率 %：
	// financial_statement_items 里 statement_type=income + item_code=net_profit
	MinQuarterGrowth float64 `json:"minQuarterGrowth"`

	// ---- 板块层 ----
	BoardFilters []BoardFilter `json:"boardFilters"`

	// ---- 技术层（要拉 K 线） ----
	MAConditions   []MACondition `json:"maConditions"`
	EnableDrawdown bool          `json:"enableDrawdown"`
	DrawdownDays   int           `json:"drawdownDays"`
	MinDrawdown    float64       `json:"minDrawdown"` // %
	MaxDrawdown    float64       `json:"maxDrawdown"` // %

	// EnableRelativeStrength 相对强度 RS = 个股 N 日涨幅 − 对标指数 N 日涨幅。
	// 对标指数取自数据湖的 index_bars（free-stockdb 没有指数行情）。
	EnableRelativeStrength bool    `json:"enableRelativeStrength"`
	RelativeStrengthDays   int     `json:"relativeStrengthDays"`
	MinRelativeStrength    float64 `json:"minRelativeStrength"`

	// Limit 限制返回条数，<=0 表示不限
	Limit int `json:"limit"`

	// TradeDate 指定筛选所用的交易日（YYYYMMDD）。
	//
	// 0 表示用最新交易日。指定时会落到**该日或之前最近一个有数据的交易日** ——
	// 用户填的那天可能不是交易日，更常见的是当天数据还没同步，
	// 这时按"指定日期或之前"取数才有意义。
	TradeDate int64 `json:"tradeDate"`
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
	PS          float64 `json:"ps"`
	ROE         float64 `json:"roe"`
	TotalMV     float64 `json:"totalMV"`
	FloatMV     float64 `json:"floatMV"`
	IsST        bool    `json:"isST"`

	// 来自数据湖的指标（对应数据集未入库时保持 0）
	Institution      float64 `json:"institution"`      // 机构参与度 %
	QuarterGrowth    float64 `json:"quarterGrowth"`    // 单季归母净利润同比 %
	RelativeStrength float64 `json:"relativeStrength"` // 相对强度 %
	// Benchmark 是算 RS 时用的对标指数（例如 399006.SZ）
	Benchmark string `json:"benchmark"`

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
	// LakePassed 再通过数据湖层（市销率/机构参与度/季报增长）的数量
	LakePassed int `json:"lakePassed"`
	// TechPassed 再通过技术层的数量（= 最终命中数）
	TechPassed int `json:"techPassed"`
	Matched    int `json:"matched"`
	// HistoryEvaluated 表示技术层确实执行了（要拉 K 线）
	HistoryEvaluated bool `json:"historyEvaluated"`

	Elapsed string `json:"elapsed"`
	// Notes 说明哪些条件没有生效、为什么 —— 不能静默忽略
	Notes []string `json:"notes"`
	// RequestedDate 是用户指定的日期（0 表示未指定）
	RequestedDate int64 `json:"requestedDate"`
	// Historical 表示这次筛选用的是历史交易日而不是最新交易日
	Historical bool `json:"historical"`
}

// UnsupportedScreenConditions 是数据源**完全没有对应字段**、因此无法实现的条件。
//
// 原来列在这里的市销率 PS / 机构参与度 / 季报增长 / 相对强度 RS 已经全部
// 对接 cnequity 数据湖的字段（见 LakeScreenConditions），所以这里是空的。
// 保留这个结构是为了以后出现真正无解的条件时有地方登记。
var UnsupportedScreenConditions = []struct {
	Name   string
	Reason string
}{}

// LakeScreenCondition 是需要 cnequity 数据湖才能生效的条件。
//
// 字段已经对接好了，但**数据集可能尚未入库**。界面要如实显示这个状态：
// "字段已对接 / 数据未入库"和"根本不支持"是两回事。
type LakeScreenCondition struct {
	Name    string `json:"name"`
	Dataset string `json:"dataset"`
	Field   string `json:"field"`
	// Available 表示该数据集当前是否已入库
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

// lakeScreenConditions 列出依赖数据湖的条件及其数据集。
var lakeScreenConditions = []LakeScreenCondition{
	{
		Name: "市销率 PS", Dataset: "valuation_metrics", Field: "ps_ttm",
		Note: "估值表，含 pe_ttm / pb / ps_ttm / 总市值 / 流通市值",
	},
	{
		Name: "机构参与度", Dataset: "top_holders", Field: "is_institution + holding_pct + holder_scope",
		Note: "十大股东，取流通股东口径的机构持股比例合计",
	},
	{
		Name: "季报增长", Dataset: "financial_statement_items", Field: "income / net_profit",
		Note: "财报科目长表，归母净利润按单季同比计算",
	},
	{
		Name: "相对强度 RS", Dataset: "index_bars", Field: "close",
		Note: "指数日线，用作对标的基准（创业板指 / 科创50 / 深证成指 / 上证指数 / 沪深300）",
	},
}

// validTradeDateParam 校验用户填的 YYYYMMDD 是否是合法日期。
//
// 需要这层校验是因为界面允许手输：填成 20260960（9 月没有 60 号）或 20261301
// 的话，不校验就会一路传到数据源，最后报一句"找不到该日期之前的交易日"，
// 用户根本看不出是日期本身写错了。
func validTradeDateParam(date int) bool {
	if date == 0 {
		return true // 0 表示不指定
	}
	if date < 0 {
		return false
	}
	year, month, day := date/10000, date/100%100, date%100
	if year < 1990 || year > 2999 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	// 交给 time.Date 归一化，再比对：2026-02-30 会被归一成 3 月 2 日，从而被识别为非法
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Year() == year && int(t.Month()) == month && t.Day() == day
}

// ScreenStocks 执行条件选股。
func (s *StockService) ScreenStocks(cond ScreenCondition) (*ScreenResult, error) {
	started := time.Now()

	if !validTradeDateParam(int(cond.TradeDate)) {
		return nil, fmt.Errorf("%d 不是有效日期，请按 YYYYMMDD 填写（例如 20260930）", cond.TradeDate)
	}

	provider, source := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ---- 解析交易日 ----
	//
	// 不指定时用最新交易日；指定时落到"该日或之前最近一个有数据的交易日"。
	// 后者是这个功能存在的理由：当天数据常常还没同步，用户要按昨天的数据筛。
	tradeDate, err := provider.TradeDateAt(ctx, int(cond.TradeDate))
	if err != nil {
		return nil, err
	}

	var universe *StockUniverse
	if cond.TradeDate <= 0 {
		universe, err = s.GetStockUniverse(false)
	} else if latest := s.peekUniverse(); latest != nil && int(latest.TradeDate) == tradeDate {
		// 指定日期解析后正好就是最新交易日，直接复用缓存，省一次全量抓取
		universe = latest
	} else {
		universe, err = s.universeOn(ctx, provider, source, tradeDate)
	}
	if err != nil {
		return nil, err
	}

	result := &ScreenResult{
		TradeDate:     universe.TradeDate,
		RequestedDate: cond.TradeDate,
		Scanned:       len(universe.Stocks),
	}
	// 用户指定的日期与真正用上的交易日不一致时，把解析结果告诉他，
	// 否则他会以为"我明明选的是 3 号，怎么显示 2 号"。
	if cond.TradeDate > 0 && universe.TradeDate != cond.TradeDate {
		result.Historical = true
		result.Notes = append(result.Notes, fmt.Sprintf(
			"指定日期 %d 不是交易日或当天数据尚未同步，已改用 %d 及之前的数据",
			cond.TradeDate, universe.TradeDate))
	} else if cond.TradeDate > 0 {
		result.Historical = true
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

		// 板块成分名单只有"当前"这一份 —— free-stockdb 的板块表没有历史版本。
		// 指定历史日期时这一点必须说清楚，否则用户会以为选的是"当时"的成分股。
		// 注意：界面按纯文本渲染这些提示，所以不要用 Markdown 标记。
		if result.Historical {
			result.Notes = append(result.Notes, fmt.Sprintf(
				"板块成分名单只有当前这一份（数据源没有历史成分），"+
					"所以板块条件按今天的成分股匹配，只有行情类条件用的是 %d 的数据",
				universe.TradeDate))
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

	// ---- 第 3 层：数据湖条件（市销率 / 机构参与度 / 季报增长）----
	//
	// 这三项的字段只在 cnequity 数据湖里。数据集没入库时会跳过并说明原因，
	// 不会让整个筛选失败。
	var lakeIndex map[string]lakeMetrics
	{
		var lakeNotes []string
		passed, lakeIndex, lakeNotes = s.applyLakeConditions(ctx, passed, cond, tradeDate)
		result.Notes = append(result.Notes, lakeNotes...)
	}
	result.LakePassed = len(passed)

	// ---- 第 4 层：技术条件（要拉 K 线，只对幸存者做） ----
	var techIndex map[string]screenTech
	needsHistory := len(cond.MAConditions) > 0 || cond.EnableDrawdown || cond.EnableRelativeStrength
	if needsHistory {
		var techNotes []string
		// 关键：日线的终点必须是解析后的交易日，不能是"今天" ——
		// 否则指定历史日期时，均线与回撤会混入指定日期之后的数据。
		passed, techIndex, techNotes = s.applyHistoryConditions(ctx, provider, passed, cond, tradeDate)
		result.Notes = append(result.Notes, techNotes...)
		result.HistoryEvaluated = true
	}
	result.TechPassed = len(passed)

	// ---- 结果股票的板块名（与筛选条件无关，纯粹是展示信息）----
	//
	// 只对最终命中的股票建索引，不对全市场做 —— 命中数通常远小于 7500。
	codes := make([]string, 0, len(passed))
	for _, item := range passed {
		codes = append(codes, item.Code)
	}
	boardNames, boardErr := s.boardsForCodes(codes)
	if boardErr != nil {
		// 板块是展示信息，取不到不该让整个筛选失败
		slog.Warn("读取板块名失败", "err", boardErr)
		boardNames = map[string][]string{}
	}

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
		// 板块名**与是否设置了板块条件无关** —— 它是结果的一列信息。
		// 之前只在设置了板块条件时才填（boardIndex 非 nil），
		// 导致不设板块条件时"板块"这一列永远是空的。
		row.Boards = boardNames[item.Code]
		if lake, ok := lakeIndex[item.Code]; ok {
			row.PS = lake.ps
			row.Institution = lake.institution
			row.QuarterGrowth = lake.quarterGrowth
		}
		if tech, ok := techIndex[item.Code]; ok {
			row.DrawdownPct = tech.drawdown
			row.MAValues = tech.maValues
			row.RelativeStrength = tech.relativeStrength
			row.Benchmark = tech.benchmark
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

	slog.Info("条件选股完成",
		"scanned", result.Scanned, "snapshot", result.SnapshotPassed,
		"board", result.BoardPassed, "lake", result.LakePassed,
		"tech", result.TechPassed, "matched", result.Matched, "elapsed", result.Elapsed)
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
//
// 这里只负责**筛选**。结果里要展示的板块名走 boardsForCodes ——
// 那个只对命中的股票建索引，不对全市场扫两遍。
type boardMembership struct {
	// filters[i] 是第 i 条条件允许的代码集合
	filters []map[string]struct{}
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

	out := &boardMembership{}

	for _, filter := range filters {
		if filter.Category == "" || len(filter.Names) == 0 {
			continue
		}

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
	drawdown         float64
	maValues         []MAValue
	relativeStrength float64
	benchmark        string
}

// ---------------------------------------------------------------------------
// 数据湖层
//
// 市销率 / 机构参与度 / 季报增长 这三项 free-stockdb 没有对应字段，
// 只有 cnequity 数据湖才有。数据集未入库时条件被跳过并写进 Notes ——
// 用户明确要求"有字段就先对接字段"，所以这里对接的是 schema，
// 数据到位后不需要改代码。
// ---------------------------------------------------------------------------

// lakeMetrics 是数据湖算出来、不在 StockItem 上的指标。
type lakeMetrics struct {
	ps            float64
	institution   float64
	quarterGrowth float64
}

// lakeConditionCoverageMin 是条件生效所需的最低数据覆盖率。
//
// 沿用原项目的做法：季报这类数据如果只覆盖很小一部分股票，
// 条件实际上是在"放行绝大多数"，那还不如明确跳过并说明。
const lakeConditionCoverageMin = 0.2

func (s *StockService) applyLakeConditions(
	ctx context.Context,
	passed []StockItem,
	cond ScreenCondition,
	tradeDate int,
) ([]StockItem, map[string]lakeMetrics, []string) {
	metrics := make(map[string]lakeMetrics, len(passed))

	needsPS := cond.MinPS != 0 || cond.MaxPS != 0
	needsInstitution := cond.MinInstitution != 0 || cond.MaxInstitution != 0
	needsGrowth := cond.MinQuarterGrowth != 0
	if !needsPS && !needsInstitution && !needsGrowth {
		return passed, metrics, nil
	}

	var notes []string
	lake := s.lakeHandle()
	if !lake.Available() {
		notes = append(notes, "市销率 / 机构参与度 / 季报增长 需要 cnequity 数据湖，"+
			"当前没有可用的数据湖，这些条件已跳过")
		return passed, metrics, notes
	}

	// ---- 市销率 ----
	if needsPS {
		values, err := lake.ValuationsOn(int64(tradeDate))
		if err != nil {
			notes = append(notes, "市销率条件已跳过："+err.Error())
		} else {
			covered := 0
			kept := passed[:0]
			for _, item := range passed {
				row, ok := values[item.Code]
				if !ok {
					// 该日没有估值记录（停牌/退市/未同步）→ 跳过该条件，与原项目口径一致
					kept = append(kept, item)
					continue
				}
				covered++
				if !within(row.PsTTM, cond.MinPS, cond.MaxPS, row.PsTTM == 0) {
					continue
				}
				metric := metrics[item.Code]
				metric.ps = row.PsTTM
				metrics[item.Code] = metric
				kept = append(kept, item)
			}
			passed = kept
			notes = append(notes, coverageNote("市销率", covered, len(passed)))
		}
	}

	// ---- 机构参与度（流通股东口径）----
	if needsInstitution {
		rows, err := lake.LatestTopHolders(int64(tradeDate))
		if err != nil {
			notes = append(notes, "机构参与度条件已跳过："+err.Error())
		} else {
			pct := aggregateInstitutionPct(rows)
			covered := 0
			kept := passed[:0]
			for _, item := range passed {
				value, ok := pct[item.Code]
				if !ok {
					kept = append(kept, item)
					continue
				}
				covered++
				if !within(value, cond.MinInstitution, cond.MaxInstitution, false) {
					continue
				}
				metric := metrics[item.Code]
				metric.institution = value
				metrics[item.Code] = metric
				kept = append(kept, item)
			}
			passed = kept
			notes = append(notes, coverageNote("机构参与度", covered, len(passed)))
		}
	}

	// ---- 季报增长（单季归母净利润同比）----
	if needsGrowth {
		profits, err := lake.NetProfitsByPeriod()
		if err != nil {
			notes = append(notes, "季报增长条件已跳过："+err.Error())
		} else {
			growth := make(map[string]float64, len(profits))
			for code, byPeriod := range profits {
				if value, ok := singleQuarterGrowth(byPeriod); ok {
					growth[code] = value
				}
			}
			covered := 0
			kept := passed[:0]
			for _, item := range passed {
				value, ok := growth[item.Code]
				if !ok {
					kept = append(kept, item)
					continue
				}
				covered++
				if value < cond.MinQuarterGrowth {
					continue
				}
				metric := metrics[item.Code]
				metric.quarterGrowth = value
				metrics[item.Code] = metric
				kept = append(kept, item)
			}
			passed = kept
			notes = append(notes, coverageNote("季报增长", covered, len(passed)))
		}
	}

	return passed, metrics, notes
}

// coverageNote 覆盖率过低时给一句提示（覆盖率按筛选前的样本数算）。
func coverageNote(name string, covered, total int) string {
	if total == 0 {
		return ""
	}
	ratio := float64(covered) / float64(total)
	if ratio >= lakeConditionCoverageMin {
		return ""
	}
	return fmt.Sprintf("%s：只有 %d/%d（%.0f%%）的标的取到数据，该条件实际作用有限；"+
		"数据湖对应数据集可能尚未完整入库", name, covered, total, ratio*100)
}

// aggregateInstitutionPct 汇总每只股票的机构持股比例。
//
// 口径取**流通股东**（holder_scope=float）：原项目算机构参与度用的就是
// 十大流通股东，而且是"机构持股比例合计"。total 口径不能和 float 混用 ——
// 两者的分母不同（总股本 vs 流通股本）。
func aggregateInstitutionPct(rows []cnlake.TopHolderRow) map[string]float64 {
	out := make(map[string]float64, 1024)
	for _, row := range rows {
		if !row.IsInstitution {
			continue
		}
		if !strings.EqualFold(row.HolderScope, "float") {
			continue
		}
		out[cnlake.BareSymbol(row.Symbol)] += row.HoldingPct
	}
	return out
}

// singleQuarterGrowth 用累计归母净利润算最近一期的单季同比增速（%）。
//
// 数据湖的 net_profit 是**年内累计**值，所以单季要减上一季：
//
//	单季(Q1) = 累计(Q1)
//	单季(Qn) = 累计(Qn) - 累计(Qn-1)
//
// 同比 = (本期单季 − 去年同期单季) / |去年同期单季| × 100
func singleQuarterGrowth(byPeriod map[string]float64) (float64, bool) {
	if len(byPeriod) < 2 {
		return 0, false
	}
	periods := make([]string, 0, len(byPeriod))
	for period := range byPeriod {
		periods = append(periods, period)
	}
	sort.Strings(periods)
	latest := periods[len(periods)-1]

	year, quarter, ok := parseReportPeriod(latest)
	if !ok {
		return 0, false
	}
	currentSingle, ok := singleQuarterValue(byPeriod, year, quarter)
	if !ok {
		return 0, false
	}
	previousSingle, ok := singleQuarterValue(byPeriod, year-1, quarter)
	if !ok || previousSingle == 0 {
		return 0, false
	}
	// 用绝对值做分母：去年亏损时增速的正负号才有意义
	return round((currentSingle-previousSingle)/math.Abs(previousSingle)*100, 4), true
}

// singleQuarterValue 由累计值推出某一季的单季值。
func singleQuarterValue(byPeriod map[string]float64, year, quarter int) (float64, bool) {
	key := fmt.Sprintf("%dQ%d", year, quarter)
	cumulative, ok := byPeriod[key]
	if !ok {
		return 0, false
	}
	if quarter <= 1 {
		return cumulative, true
	}
	previousKey := fmt.Sprintf("%dQ%d", year, quarter-1)
	previous, ok := byPeriod[previousKey]
	if !ok {
		return 0, false
	}
	return cumulative - previous, true
}

// parseReportPeriod 解析 2025Q2 这样的报告期。
func parseReportPeriod(period string) (int, int, bool) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(period)), "Q", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	year, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	quarter, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || year < 1990 || quarter < 1 || quarter > 4 {
		return 0, 0, false
	}
	return year, quarter, true
}

// relativeStrengthBenchmark 给出个股 RS 的对标指数。
//
// 映射沿用原项目：
//
//	300/301（创业板）→ 399006.SZ 创业板指
//	688/689（科创板）→ 000688.SH 科创 50
//	深市其它        → 399001.SZ 深证成指
//	沪市其它        → 000001.SH 上证指数
//	兜底            → 000300.SH 沪深 300
//
// 数据湖 index_bars 里这五个指数都有。
func relativeStrengthBenchmark(code string) string {
	bare := cnlake.BareSymbol(code)
	switch {
	case strings.HasPrefix(bare, "300"), strings.HasPrefix(bare, "301"):
		return "399006.SZ"
	case strings.HasPrefix(bare, "688"), strings.HasPrefix(bare, "689"):
		return "000688.SH"
	case strings.HasPrefix(bare, "000"), strings.HasPrefix(bare, "001"),
		strings.HasPrefix(bare, "002"), strings.HasPrefix(bare, "003"):
		return "399001.SZ"
	case strings.HasPrefix(bare, "600"), strings.HasPrefix(bare, "601"),
		strings.HasPrefix(bare, "603"), strings.HasPrefix(bare, "605"):
		return "000001.SH"
	}
	return "000300.SH"
}

// loadBenchmark 读取某个指数在窗口内的收盘价，按 YYYYMMDD 索引。
func (s *StockService) loadBenchmark(symbol string, start, end int) (map[int64]float64, error) {
	lake := s.lakeHandle()
	if !lake.HasIndexBars() {
		return nil, fmt.Errorf("数据湖的 index_bars 尚未入库")
	}
	rows, err := lake.IndexBars(symbol, start/10000, end/10000)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]float64, len(rows))
	for _, row := range rows {
		out[row.Date()] = row.Close
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("数据湖里没有指数 %s 在 %d~%d 的数据", symbol, start, end)
	}
	return out, nil
}

// relativeStrength 用同一天的收盘价算 N 日涨幅之差（%）。
// 指数当天没有数据时向前找最近一个交易日。
func relativeStrength(stockCloses []float64, stockDates []int64, benchmark map[int64]float64, days int) (float64, bool) {
	if days <= 0 || len(stockCloses) < days+1 || len(stockDates) != len(stockCloses) {
		return 0, false
	}
	stockNow := stockCloses[len(stockCloses)-1]
	stockThen := stockCloses[len(stockCloses)-1-days]
	if stockThen <= 0 {
		return 0, false
	}

	endDate := stockDates[len(stockDates)-1]
	startDate := stockDates[len(stockDates)-1-days]
	benchmarkNow, okNow := benchmarkAt(benchmark, endDate)
	benchmarkThen, okThen := benchmarkAt(benchmark, startDate)
	if !okNow || !okThen || benchmarkThen <= 0 {
		return 0, false
	}

	stockReturn := (stockNow/stockThen - 1) * 100
	benchmarkReturn := (benchmarkNow/benchmarkThen - 1) * 100
	return round(stockReturn-benchmarkReturn, 4), true
}

// benchmarkAt 取指定日期的指数收盘价；当天没有就往前找最近的。
func benchmarkAt(benchmark map[int64]float64, date int64) (float64, bool) {
	for offset := 0; offset < 10; offset++ {
		if value, ok := benchmark[int64(shiftBack(int(date), offset))]; ok {
			return value, true
		}
	}
	return 0, false
}

// applyHistoryConditions 对已通过前两层的股票拉日线，应用均线与回撤条件，
// 返回通过者、算出的指标缓存、以及需要告诉用户的提示。
//
// 这一步是筛选里唯一"贵"的部分，所以放在最后，且只处理幸存者。
// 原项目也是这个顺序（基础面 → 历史指标），并且一次批量拉取共享窗口。
func (s *StockService) applyHistoryConditions(
	ctx context.Context,
	provider Provider,
	passed []StockItem,
	cond ScreenCondition,
	tradeDate int,
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
	if cond.EnableRelativeStrength {
		if rs := cond.RelativeStrengthDays; rs > days {
			days = rs
		} else if rs <= 0 && defaultRelativeStrengthDays > days {
			days = defaultRelativeStrengthDays
		}
	}
	calendarDays := days*3 + 30

	// tradeDate 由调用方解析好传进来（指定日期时为"该日或之前最近交易日"），
	// 这里不再自己取最新交易日，否则历史筛选会拉到未来的数据。
	start := shiftBack(tradeDate, calendarDays)

	// ---- 相对强度的对标指数 ----
	//
	// 指数行情只在数据湖的 index_bars 里。不同板块的股票对标不同指数
	// （创业板→399006.SZ、科创板→000688.SH…），所以按需加载并缓存。
	var (
		benchmarkCache = make(map[string]map[int64]float64, 4)
		benchmarkErr   error
	)
	benchmarkFor := func(code string) (map[int64]float64, string) {
		if !cond.EnableRelativeStrength {
			return nil, ""
		}
		symbol := relativeStrengthBenchmark(code)
		if cached, ok := benchmarkCache[symbol]; ok {
			return cached, symbol
		}
		if benchmarkErr != nil {
			return nil, symbol
		}
		loaded, err := s.loadBenchmark(symbol, start, tradeDate)
		if err != nil {
			benchmarkErr = err
			return nil, symbol
		}
		benchmarkCache[symbol] = loaded
		return loaded, symbol
	}

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
		// 若放锁内，worker 的计算会被串行化，也白白拉长临界区。
		matched := false
		var value screenTech
		if err == nil && len(quotes) > 0 {
			benchmark, symbol := benchmarkFor(item.Code)
			if v, ok := evaluateHistory(quotes, cond, benchmark, symbol); ok {
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

	if benchmarkErr != nil {
		notes = append(notes, "相对强度条件已跳过："+benchmarkErr.Error())
	}
	if failed > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d 只股票没有取到日线（停牌 / 退市 / 未同步），已排除在技术条件之外", failed))
	}
	if len(kept) == 0 && len(passed) > 0 {
		notes = append(notes, "技术条件没有筛出任何股票，可放宽均线 / 回撤 / 相对强度区间")
	}
	return kept, tech, notes
}

// evaluateHistory 判断一只股票是否满足均线、回撤与相对强度条件，
// 并算出要展示的指标。
//
// benchmark 是 RS 用的对标指数收盘价（按日期索引），未启用 RS 时为 nil。
func evaluateHistory(
	quotes []*stockdb.Quote,
	cond ScreenCondition,
	benchmark map[int64]float64,
	benchmarkCode string,
) (screenTech, bool) {
	closes := make([]float64, 0, len(quotes))
	highs := make([]float64, 0, len(quotes))
	dates := make([]int64, 0, len(quotes))
	for _, q := range quotes {
		if q == nil || q.Close <= 0 {
			continue
		}
		closes = append(closes, q.Close)
		highs = append(highs, q.High)
		dates = append(dates, q.Date)
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

	// 相对强度 RS = 个股 N 日涨幅 − 对标指数 N 日涨幅
	if cond.EnableRelativeStrength {
		if benchmark == nil {
			// 指数数据拿不到（index_bars 未入库）——这种情况在调用方已经提示过，
			// 这里判为不满足而不是放行，否则"RS 筛选"会退化成"不过滤"
			return screenTech{}, false
		}
		days := cond.RelativeStrengthDays
		if days <= 0 {
			days = defaultRelativeStrengthDays
		}
		value, ok := relativeStrength(closes, dates, benchmark, days)
		if !ok {
			return screenTech{}, false
		}
		tech.relativeStrength = value
		tech.benchmark = benchmarkCode
		if value < cond.MinRelativeStrength {
			return screenTech{}, false
		}
	}

	return tech, true
}

// defaultRelativeStrengthDays 是 RS 的默认窗口（原项目同样是 20）。
const defaultRelativeStrengthDays = 20

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

// ScreenOptions 汇总界面需要知道的信息：可选分类、支持的市场、条件的数据可用性。
type ScreenOptions struct {
	Categories []BoardCategoryInfo `json:"categories"`
	Markets    []MarketInfo        `json:"markets"`
	// Unsupported 列出数据源完全没有字段、无法实现的条件
	Unsupported []string `json:"unsupported"`
	// LakeConditions 列出依赖数据湖的条件，以及数据集是否已入库
	LakeConditions []LakeScreenCondition `json:"lakeConditions"`
	// LakeAvailable 表示当前是否有可用的 cnequity 数据湖
	LakeAvailable bool `json:"lakeAvailable"`
	// LakeRoot 是数据湖路径（便于界面提示去哪配置）
	LakeRoot string `json:"lakeRoot"`
	// Defaults 是首次使用时的默认条件
	Defaults ScreenCondition `json:"defaults"`
}

// GetScreenOptions 返回条件选股界面需要的元信息。
func (s *StockService) GetScreenOptions() (*ScreenOptions, error) {
	categories, err := s.GetBoardCategories()
	if err != nil {
		return nil, err
	}

	lake := s.lakeHandle()

	out := &ScreenOptions{
		Categories:    categories,
		Markets:       marketInfos(),
		LakeAvailable: lake.Available(),
		LakeRoot:      lake.Root(),
		Defaults: ScreenCondition{
			ExcludeST:    true,
			HasQuoteOnly: true,
			DrawdownDays: 30,
		},
	}
	for _, item := range UnsupportedScreenConditions {
		out.Unsupported = append(out.Unsupported, item.Name+"："+item.Reason)
	}

	// 依赖数据湖的条件：如实报告数据集是否已入库
	for _, item := range lakeScreenConditions {
		entry := item
		entry.Available = lake.Available() && lake.HasDataset(item.Dataset)
		out.LakeConditions = append(out.LakeConditions, entry)
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
