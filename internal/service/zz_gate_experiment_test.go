package service

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 低吸指数闸门对照实验（**只读副本库**，不改任何生产代码）。
//
// 目的：回答两个问题，用同一份评分/日线/撮合，只换"指数闸门"这一个变量：
//
//	① 399006.SZ（创业板指）在 2010-06 之前不存在 → 闸门恒关。
//	   用 000300.SH（沪深300）或 000001.SH（上证指数）兜底，结果差多少？
//	② 闸门到底该用创业板指，还是上证指数 / 沪深300？
//
// 做法：复制一份 portfolio.db，走与 CreateBacktest **完全相同**的
// 评分读取 → 范围过滤 → 信号贴合 → 取日线 → 引擎撮合 → 指标计算，
// 唯一区别是传给 planBuilder 的网关 map 用哪个指数算。
//
// 跑法：
//
//	$env:FREESTOCK_GATE_EXP="1"
//	$env:FREESTOCK_GATE_DB="D:\...\.gotmp\gate_exp\portfolio.db"
//	$env:FREESTOCK_GATE_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	$env:FREESTOCK_GATE_PORTFOLIO="pf_1791522403274487100_0002"
//	$env:FREESTOCK_GATE_RANGE="B"                 # A=2000起 / B=2005起 / C=2010-09起
//	$env:FREESTOCK_GATE_VARIANTS="399006,399006+000300,000300,000001,399006+000001"
//	$env:FREESTOCK_GATE_OUT="docs"                # 结果 CSV 输出目录
//	go test ./internal/service -run TestLowBuyGateExperiment -v -timeout 6h
func TestLowBuyGateExperiment(t *testing.T) {
	if os.Getenv("FREESTOCK_GATE_EXP") == "" {
		t.Skip("设 FREESTOCK_GATE_EXP=1 才跑（真实数据、只读副本库）")
	}
	dbPath := os.Getenv("FREESTOCK_GATE_DB")
	lakeRoot := os.Getenv("FREESTOCK_GATE_LAKE")
	portfolioID := os.Getenv("FREESTOCK_GATE_PORTFOLIO")
	if dbPath == "" || lakeRoot == "" || portfolioID == "" {
		t.Fatal("需要 FREESTOCK_GATE_DB / FREESTOCK_GATE_LAKE / FREESTOCK_GATE_PORTFOLIO")
	}
	rangeKey := strings.ToUpper(strings.TrimSpace(os.Getenv("FREESTOCK_GATE_RANGE")))
	if rangeKey == "" {
		rangeKey = "B"
	}
	variants := parseGateVariants(os.Getenv("FREESTOCK_GATE_VARIANTS"))
	if len(variants) == 0 {
		variants = parseGateVariants("399006,399006+000300,000300,000001,399006+000001")
	}
	outDir := os.Getenv("FREESTOCK_GATE_OUT")
	if outDir == "" {
		outDir = "docs"
	}

	type window struct {
		name       string
		start, end int
	}
	windows := map[string]window{
		// A = 与用户这次回测完全一致的区间（会包含 2000-2004 的 1200 天无评分空仓）
		"A": {"2000-01-04~2026-10-28（含 2000-2004 无评分段）", 20000101, 20261029},
		// B = 评分真实覆盖的区间（2005-01-04 起）
		"B": {"2005-01-04~2026-10-28（评分全区间）", 20050101, 20261029},
		// C = 创业板指存在之后的区间（2010-09 起），用来比较"用哪个指数"
		"C": {"2010-09-01~2026-10-28（创业板指可用之后）", 20100901, 20261029},
	}
	win, ok := windows[rangeKey]
	if customFrom := envInt(t, "FREESTOCK_GATE_FROM", 0); customFrom > 0 {
		customTo := envInt(t, "FREESTOCK_GATE_TO", 0)
		if customTo <= 0 {
			t.Fatalf("设了 FREESTOCK_GATE_FROM 就必须给 FREESTOCK_GATE_TO")
		}
		win = window{
			name:  fmt.Sprintf("%d~%d（自定义窗口）", customFrom, customTo),
			start: customFrom, end: customTo,
		}
		// 自定义窗口用区间当标签，免得几段结果在 CSV 里都叫 "B"。
		rangeKey = fmt.Sprintf("%d-%d", customFrom, customTo)
		ok = true
	}
	if !ok {
		t.Fatalf("FREESTOCK_GATE_RANGE=%q 不认识（A/B/C）", rangeKey)
	}
	t.Logf("实验窗口 %s [%s]", rangeKey, win.name)

	// 用固定路径的配置目录（不用 t.TempDir）：数据湖的"按股票日线缓存"
	// 会被写到配置目录下，临时目录在测试结束时清不干净会让整轮报 FAIL，
	// 而这时结果其实已经写出来了。
	cfgPath := os.Getenv("FREESTOCK_GATE_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join(filepath.Dir(dbPath), "exp-config.json")
	}
	cfgStore := config.NewStoreAt(cfgPath)
	stock := NewStockService(cfgStore)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dbPath)
	t.Cleanup(svc.Close)

	if universe, err := stock.GetStockUniverse(true); err == nil && universe != nil {
		t.Logf("数据源=%s 股票=%d 最新交易日=%d", universe.SourceURL, len(universe.Stocks), universe.TradeDate)
	}

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		t.Fatalf("读组合失败：%v", err)
	}

	// ---- 以下与 CreateBacktest 逐步对齐，只把 gates 换成实验变量 ----
	began := time.Now()
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(win.start), int64(win.end))
	if err != nil {
		t.Fatal(err)
	}
	if len(strengths) == 0 {
		t.Fatalf("区间内没有评分")
	}
	// **全市场股票数（按日）**：相对广度阈值要用它把"绝对只数"折成比例。
	// 口径与 readyByDate 一致（只数 A 股、排除北交所/302），在范围过滤之前统计。
	universeByDate := map[string]int{}
	for _, row := range strengths {
		if portfolio.IsAShareBareCode(row.Code) {
			universeByDate[fmt.Sprintf("%d", row.TradeDate)]++
		}
	}
	days, codes, _, readyByDate := svc.buildStrengthDays(store, strengths, item)
	strengths = nil
	if len(days) == 0 {
		t.Fatalf("没有可用的评分交易日")
	}
	days = svc.attachSignalsToDays(store, days)
	t.Logf("评分：%d 天 / %d 只 / 载入+组装 %s", len(days), len(codes), time.Since(began).Round(time.Second))

	began = time.Now()
	bars, err := svc.loadBacktestBars(codes, win.start, win.end)
	if err != nil {
		t.Fatalf("取日线失败：%v", err)
	}
	t.Logf("日线：%d 根 / %s", len(bars), time.Since(began).Round(time.Second))

	// 基准（指标里的 beta/alpha 用，保持与生产一致：000300）
	benchmark := map[string]float64{}
	if closes, err := stock.loadBenchmark(score.LowBuyIndexBenchmarkCode, win.start, win.end); err == nil {
		for date, close := range closes {
			benchmark[fmt.Sprintf("%d", date)] = close
		}
	}

	// 把 bars 裁到窗口内（区间 A 的 end 是 20261029，湖里只到 20261028）
	bars = filterBars(bars, win.start, win.end)
	states := marketStateByDate(store, days)

	cfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		BenchmarkCode:       score.LowBuyIndexBenchmarkCode,
		StartDate:           strconv.Itoa(win.start),
		EndDate:             strconv.Itoa(win.end),
		TargetPositionCount: item.TargetPositionCount,
		MaxStockCount:       item.MaxStockCount,
		TotalLayers:         item.TotalLayers,
		MaxLayersPerStock:   maxLayersForBacktest(item.StrategyID, item.TotalLayers),
		InitialCapital:      100000,
		MarketScope:         scopeOfPortfolio(item),
	}

	metricPath := filepath.Join(outDir, "低吸指数闸门_实验指标.csv")
	yearPath := filepath.Join(outDir, "低吸指数闸门_年度收益.csv")
	gatePath := filepath.Join(outDir, "低吸指数闸门_闸门统计.csv")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, v := range variants {
		began = time.Now()
		gates, stats := gateMapFor(stock, days, v.Primary, v.Fallback)
		if len(gates) == 0 {
			t.Fatalf("变体 %s：闸门为空（指数数据取不到）", v.Name)
		}
		ready := readyByDate
		breadthLabel := "绝对"
		if v.Relative {
			ready = scaleReady(readyByDate, universeByDate, 5000)
			breadthLabel = "相对(5000口径)"
		}
		cfgV := cfg
		if v.CapLayers > 0 {
			cfgV.MaxLayersPerStock = v.CapLayers
		}
		engine := backtest.New(cfgV, days, bars, benchmark)
		buildPlan := svc.backtestPlanBuilder(item, store, states, item.TargetPositionCount, item.MaxStockCount, gates, ready)
		if !v.Entry.empty() {
			buildPlan = wrapEntryRules(buildPlan, v.Entry, gates)
		}
		if !v.Exit.empty() {
			buildPlan = wrapExitRules(buildPlan, v.Exit)
		}
		if !v.Regime.empty() {
			buildPlan = wrapRegime(buildPlan, v.Regime)
		}
		if v.CapLayers > 0 {
			buildPlan = capPlanLayers(buildPlan, v.CapLayers)
		}
		result, err := engine.Run(buildPlan)
		if err != nil {
			t.Fatalf("变体 %s：回测失败 %v", v.Name, err)
		}
		metrics := backtest.CalculateMetrics(result.Snapshots, result.Trades, cfgV.InitialCapital)

		expDays, yearRows, firstTrade, lastTrade := summarizeExperiment(result)

		row := experimentRow{
			Window: rangeKey, Variant: v.Name, Primary: v.Primary, Fallback: v.Fallback,
			Breadth: breadthLabel, CapLayers: v.CapLayers,
			Start: win.start, End: win.end,
			TotalReturn: metrics.TotalReturn, AnnualReturn: metrics.AnnualReturn,
			MaxDrawdown: metrics.MaxDrawdown, Volatility: metrics.Volatility,
			Sharpe: metrics.SharpeRatio, Calmar: metrics.CalmarRatio,
			Trades: metrics.TotalTrades, WinRate: metrics.WinRate,
			AvgHoldingDays: metrics.AvgHoldingDays, MaxPositions: metrics.MaxPositionCount,
			AvgPositionRatio: metrics.AvgPositionRatio,
			ExposurePct:      float64(expDays) * 100 / float64(len(result.Snapshots)),
			Snapshots:        len(result.Snapshots),
			FirstTrade:       firstTrade, LastTrade: lastTrade,
			GateReadyPct: stats.ReadyPct(), GateAllowedPct: stats.AllowedPct(),
			FallbackDays: stats.FallbackDays, ElapsedSec: time.Since(began).Seconds(),
		}
		appendExperimentMetric(t, metricPath, row)
		appendYearly(t, yearPath, rangeKey, v.Name, yearRows)
		appendGateStats(t, gatePath, rangeKey, v.Name, stats)

		t.Logf("[%s/%s] 总收益 %.2f%% | 年化 %.2f%% | 回撤 %.2f%% | 夏普 %.2f | 卡玛 %.2f | 成交 %d | 胜率 %.1f%% | 均仓 %.1f%% | 最大持仓 %d | 首笔 %s",
			rangeKey, v.Name, metrics.TotalReturn, metrics.AnnualReturn, metrics.MaxDrawdown,
			metrics.SharpeRatio, metrics.CalmarRatio, metrics.TotalTrades, metrics.WinRate,
			metrics.AvgPositionRatio, metrics.MaxPositionCount, firstTrade)

		// 释放这一次引擎里那份（行是深拷贝的）持仓/评分索引
		engine = nil
		result = nil
		runtime.GC()
	}
}

type gateVariant struct {
	Name     string
	Primary  string
	Fallback string
	// Relative=true 时把绝对广度就绪数按"当日全市场股票数"折成 5000 只口径，
	// 等价于把 1000/1500/2000/2600 这些绝对阈值改成 20%/30%/40%/52% 的相对比例。
	Relative bool
	// CapLayers>0 时覆盖回测引擎的单股层数上限（总层数 10 → 2 层 = 单票 20%）。
	CapLayers int
	// Exit 是"出场规则"实验：止盈 / 移动止盈 / 最长持有 / 放宽 MA30 止损。
	Exit exitRules
	// Entry 是"入场过滤"实验：熊市不买 / 动量确认 / 站上MA20 / 高分 / 长周期闸门。
	Entry entryRules
	// Regime 是"牛市/非牛市分支"实验：非牛市不开仓、非牛市清仓、熊市清仓、非牛市单票上限。
	Regime regimeRules
}

// regimeRules 是按 market_state（bull / range / bear）切换的不同应对分支。
//
// 牛市 = state=="bull"，非牛市 = range 或 bear。
type regimeRules struct {
	// OnlyBull=true：非牛市一律不开新仓（已有持仓按原规则处理）。
	OnlyBull bool
	// ClearNotBull=true：非牛市把持仓全部清掉（只做牛市）。
	ClearNotBull bool
	// ClearBear=true：熊市清仓，震荡市继续持有。
	ClearBear bool
	// NonBullCap>0：非牛市的单票层数上限（10 层制，2 = 20%）。
	NonBullCap int
}

func (r regimeRules) empty() bool {
	return !r.OnlyBull && !r.ClearNotBull && !r.ClearBear && r.NonBullCap <= 0
}

// entryRules 是可以在不改生产代码的前提下，通过丢弃计划里的买入行实现的入场过滤。
type entryRules struct {
	// NoBear=true：市场状态为 bear 时不开新仓。
	NoBear bool
	// MinRet5>0：要求个股 5 日涨幅 >= 该值（动量确认，避免接下跌中的刀）。
	MinRet5 float64
	// AboveMA20=true：要求收盘价站上 MA20。
	AboveMA20 bool
	// MinScore>0：只买总分 >= 该值的候选。
	MinScore float64
	// RequireLongCycle=true：要求闸门指数自身在 MA250 上方（长周期多头）。
	RequireLongCycle bool

	// ---- 选股类过滤 ----
	// MinRS20>0：要求 20 日相对强度（相对沪深300）>= 该值。
	MinRS20 float64
	// AboveMA60=true：要求收盘价站上 MA60（中期趋势向上）。
	AboveMA60 bool
	// MinAmount>0：要求成交额 >= 该值（元），过滤流动性差的票。
	MinAmount float64
	// DDHigh/DDLow：要求 20 日回撤落在 [DDLow, DDHigh] 区间（都是负数）。
	// 例如 -12 / -3 表示"只买回调 3%~12% 的票"，比默认的 -18~-1.5 更严。
	DDHigh float64
	DDLow  float64
	// MaxPerIndustry>0：同一天同一行业最多买几只。
	MaxPerIndustry int
}

func (r entryRules) empty() bool {
	return !r.NoBear && r.MinRet5 <= 0 && !r.AboveMA20 && r.MinScore <= 0 && !r.RequireLongCycle &&
		r.MinRS20 <= 0 && !r.AboveMA60 && r.MinAmount <= 0 && r.DDHigh == 0 && r.DDLow == 0 && r.MaxPerIndustry <= 0
}

// exitRules 是可以在不改生产代码的前提下，通过改写计划行实现的出场规则。
//
// 零值 = 完全沿用原策略的出场（跌破 MA30×0.96 或广度不足清仓）。
type exitRules struct {
	// TakeProfit>0：浮盈达到该百分比就整仓卖出（"做波段"）。
	TakeProfit float64
	// TrailStop>0：从持仓最高价回撤该百分比就整仓卖出。
	TrailStop float64
	// MaxHold>0：持有交易日数达到该值就卖出。
	MaxHold int
	// StopRatio>0：把"跌破 MA30×0.96"放宽成"跌破 MA30×StopRatio"（<0.96 才是放宽）。
	// 例如 0.90 = 只要还在 MA30 上方 90% 就不卖。
	StopRatio float64
}

func (r exitRules) empty() bool {
	return r.TakeProfit <= 0 && r.TrailStop <= 0 && r.MaxHold <= 0 && r.StopRatio <= 0
}

// variantPresets 是"策略参数"实验的预设。
//
// 399006 固定为主闸门（上一轮结论：保持创业板指），只动广度口径、单票上限、出场规则。
var variantPresets = map[string]gateVariant{
	"base":     {Name: "base（现状）", Primary: "399006"},
	"rel":      {Name: "rel（相对广度）", Primary: "399006", Relative: true},
	"cap2":     {Name: "cap2（单票≤20%）", Primary: "399006", CapLayers: 2},
	"cap3":     {Name: "cap3（单票≤30%）", Primary: "399006", CapLayers: 3},
	"rel+cap2": {Name: "rel+cap2", Primary: "399006", Relative: true, CapLayers: 2},
	"rel+cap3": {Name: "rel+cap3", Primary: "399006", Relative: true, CapLayers: 3},

	// ---- 出场规则（做波段）----
	"tp10":    {Name: "tp10（+10%止盈）", Primary: "399006", Exit: exitRules{TakeProfit: 10}},
	"tp15":    {Name: "tp15（+15%止盈）", Primary: "399006", Exit: exitRules{TakeProfit: 15}},
	"tp20":    {Name: "tp20（+20%止盈）", Primary: "399006", Exit: exitRules{TakeProfit: 20}},
	"trail08": {Name: "trail08（回撤8%卖）", Primary: "399006", Exit: exitRules{TrailStop: 8}},
	"trail12": {Name: "trail12（回撤12%卖）", Primary: "399006", Exit: exitRules{TrailStop: 12}},
	"hold10":  {Name: "hold10（持10日）", Primary: "399006", Exit: exitRules{MaxHold: 10}},
	"hold20":  {Name: "hold20（持20日）", Primary: "399006", Exit: exitRules{MaxHold: 20}},
	"stop90":  {Name: "stop90（MA30×0.90）", Primary: "399006", Exit: exitRules{StopRatio: 0.90}},
	"swing":   {Name: "swing（+15%止盈/回撤8%）", Primary: "399006", Exit: exitRules{TakeProfit: 15, TrailStop: 8}},
	"cap2+tp15": {
		Name: "cap2+tp15", Primary: "399006", CapLayers: 2,
		Exit: exitRules{TakeProfit: 15},
	},
	"cap2+trail8": {
		Name: "cap2+trail8", Primary: "399006", CapLayers: 2,
		Exit: exitRules{TrailStop: 8},
	},

	// ---- 入场过滤（做波段的前提：别在下跌里接刀）----
	"nobear":     {Name: "nobear（熊市不买）", Primary: "399006", Entry: entryRules{NoBear: true}},
	"mom5":       {Name: "mom5（5日涨幅>0才买）", Primary: "399006", Entry: entryRules{MinRet5: 0.01}},
	"above20":    {Name: "above20（站上MA20才买）", Primary: "399006", Entry: entryRules{AboveMA20: true}},
	"score70":    {Name: "score70（只买≥70分）", Primary: "399006", Entry: entryRules{MinScore: 70}},
	"longcycle":  {Name: "longcycle（指数>MA250）", Primary: "399006", Entry: entryRules{RequireLongCycle: true}},
	"lc+mom5":   {Name: "lc+mom5", Primary: "399006", Entry: entryRules{RequireLongCycle: true, MinRet5: 0.01}},
	"lc+nobear": {Name: "lc+nobear", Primary: "399006", Entry: entryRules{RequireLongCycle: true, NoBear: true}},
	"lc+above20": {
		Name: "lc+above20（指数>MA250 且 个股>MA20）", Primary: "399006",
		Entry: entryRules{RequireLongCycle: true, AboveMA20: true},
	},
	"lc+cap2": {
		Name: "lc+cap2（长周期闸门+单票≤20%）", Primary: "399006", CapLayers: 2,
		Entry: entryRules{RequireLongCycle: true},
	},
	"lc+cap3": {
		Name: "lc+cap3（长周期闸门+单票≤30%）", Primary: "399006", CapLayers: 3,
		Entry: entryRules{RequireLongCycle: true},
	},

	// ---- 牛市 / 非牛市分支 ----
	"bull_only":      {Name: "bull_only（只在牛市开仓）", Primary: "399006", Regime: regimeRules{OnlyBull: true}},
	"bull_clear":     {Name: "bull_clear（非牛市清仓）", Primary: "399006", Regime: regimeRules{OnlyBull: true, ClearNotBull: true}},
	"bull_bear_clear": {Name: "bull_bear_clear（熊市清仓）", Primary: "399006", Regime: regimeRules{OnlyBull: true, ClearBear: true}},
	"regime_cap2":    {Name: "regime_cap2（非牛市单票≤20%）", Primary: "399006", Regime: regimeRules{NonBullCap: 2}},
	"regime_cap3":    {Name: "regime_cap3（非牛市单票≤30%）", Primary: "399006", Regime: regimeRules{NonBullCap: 3}},
	"lc+rcap2": {
		Name: "lc+rcap2（指数>MA250 + 非牛市≤20%）", Primary: "399006",
		Entry: entryRules{RequireLongCycle: true}, Regime: regimeRules{NonBullCap: 2},
	},
	"lc+rcap3": {
		Name: "lc+rcap3（指数>MA250 + 非牛市≤30%）", Primary: "399006",
		Entry: entryRules{RequireLongCycle: true}, Regime: regimeRules{NonBullCap: 3},
	},

	// ---- 选股过滤 ----
	"rs20":    {Name: "rs20（RS20>0）", Primary: "399006", Entry: entryRules{MinRS20: 0.01}},
	"ma60":    {Name: "ma60（个股>MA60）", Primary: "399006", Entry: entryRules{AboveMA60: true}},
	"amt1e8":  {Name: "amt1e8（成交额≥1亿）", Primary: "399006", Entry: entryRules{MinAmount: 1e8}},
	"dd12":    {Name: "dd12（只买回调3~12%）", Primary: "399006", Entry: entryRules{DDHigh: -3, DDLow: -12}},
	"score60": {Name: "score60（只买≥60分）", Primary: "399006", Entry: entryRules{MinScore: 60}},
	"selA":    {Name: "selA（>MA20 + RS20>0）", Primary: "399006", Entry: entryRules{AboveMA20: true, MinRS20: 0.01}},
	"selB":    {Name: "selB（>MA20 + >MA60 + RS20>0）", Primary: "399006", Entry: entryRules{AboveMA20: true, AboveMA60: true, MinRS20: 0.01}},
	"selC":    {Name: "selC（>MA20 + RS20>0 + 回调3~12%）", Primary: "399006", Entry: entryRules{AboveMA20: true, MinRS20: 0.01, DDHigh: -3, DDLow: -12}},
	"selD":    {Name: "selD（>MA20 + RS20>0 + 行业≤2）", Primary: "399006", Entry: entryRules{AboveMA20: true, MinRS20: 0.01, MaxPerIndustry: 2}},

	// ---- 选股 × 之前有增益的条件 ----
	"rs20+cap3": {
		Name: "rs20+cap3（RS20>0 + 单票≤30%）", Primary: "399006", CapLayers: 3,
		Entry: entryRules{MinRS20: 0.01},
	},
	"lc+rs20": {
		Name: "lc+rs20（指数>MA250 + RS20>0）", Primary: "399006",
		Entry: entryRules{RequireLongCycle: true, MinRS20: 0.01},
	},
	"lc+rs20+cap3": {
		Name: "lc+rs20+cap3（指数>MA250 + RS20>0 + 单票≤30%）", Primary: "399006", CapLayers: 3,
		Entry: entryRules{RequireLongCycle: true, MinRS20: 0.01},
	},
	"rs20+cap2": {
		Name: "rs20+cap2（RS20>0 + 单票≤20%）", Primary: "399006", CapLayers: 2,
		Entry: entryRules{MinRS20: 0.01},
	},
	"lc+rs20+cap2": {
		Name: "lc+rs20+cap2（指数>MA250 + RS20>0 + 单票≤20%）", Primary: "399006", CapLayers: 2,
		Entry: entryRules{RequireLongCycle: true, MinRS20: 0.01},
	},
}

// parseGateVariants 解析变体列表。
//
// 先查预设（base/rel/cap2/cap3/rel+cap2/rel+cap3），查不到再按"指数代码"理解，
// 支持 "399006+000300" 这种兜底写法。
func parseGateVariants(raw string) []gateVariant {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []gateVariant
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if preset, ok := variantPresets[part]; ok {
			out = append(out, preset)
			continue
		}
		if idx := strings.Index(part, "+"); idx >= 0 {
			primary, fallback := part[:idx], part[idx+1:]
			out = append(out, gateVariant{
				Name: part, Primary: primary, Fallback: fallback,
			})
			continue
		}
		out = append(out, gateVariant{Name: part, Primary: part})
	}
	return out
}

// gateStats 是一个变体的闸门统计（用来解释"为什么结果不同"）。
type gateStats struct {
	Days        int
	ReadyDays   int // 闸门数据齐全（指数有 60 日以上历史）
	AllowedDays int // close > ma20 && close > ma60
	FallbackDays int
	Primary     string
}

func (s gateStats) ReadyPct() float64 {
	if s.Days == 0 {
		return 0
	}
	return float64(s.ReadyDays) * 100 / float64(s.Days)
}

func (s gateStats) AllowedPct() float64 {
	if s.Days == 0 {
		return 0
	}
	return float64(s.AllowedDays) * 100 / float64(s.Days)
}

// gateMapFor 用指定指数（可带兜底）算整个区间的低吸指数闸门。
//
// 与生产 `lowBuyIndexGatesForRange` 的唯一区别：指数代码可换、并且当主指数
// 当天还不 Ready（比如 399006 在 2010-06 之前没有数据）时，改用兜底指数。
// 长周期基准恒为 000300.SH —— 这是另一个变量，本实验固定不动。
func gateMapFor(
	stock *StockService, days []backtest.StrengthDay, primary, fallback string,
) (map[string]score.LowBuyIndexGate, gateStats) {
	stats := gateStats{Primary: primary}
	out := map[string]score.LowBuyIndexGate{}
	if stock == nil || len(days) == 0 || primary == "" {
		return out, stats
	}
	// 实验参数允许写短代码（399006 / 000300 / 000001），数据湖里必须带后缀。
	if strings.EqualFold(primary, "none") {
		// 对照组：闸门永远放行（用来量"这道闸门本身有没有价值"）。
		for _, day := range days {
			out[day.Date] = score.LowBuyIndexGate{
				Code: "none", Close: 1, Ma20: 0.5, Ma60: 0.5, Ma250: 0.5,
				BenchmarkClose: 1, BenchmarkMa250: 0.5, Ready: true,
			}
		}
		stats.Days, stats.ReadyDays, stats.AllowedDays = len(days), len(days), len(days)
		return out, stats
	}
	primary = normalizeIndexCode(primary)
	fallback = normalizeIndexCode(fallback)
	dates := make([]int, 0, len(days))
	for _, day := range days {
		if value, err := strconv.Atoi(day.Date); err == nil && value > 0 {
			dates = append(dates, value)
		}
	}
	if len(dates) == 0 {
		return out, stats
	}
	sort.Ints(dates)
	first, last := dates[0], dates[len(dates)-1]
	// 250 日均线要多取两年
	start := (first/10000-2)*10000 + 101

	primaryCloses, err := stock.loadBenchmark(primary, start, last)
	if err != nil || len(primaryCloses) == 0 {
		fmt.Printf("[gate] %s 取不到指数日线（%d~%d）：err=%v rows=%d\n", primary, start, last, err, len(primaryCloses))
		return out, stats
	}
	benchCloses, err := stock.loadBenchmark(score.LowBuyIndexBenchmarkCode, start, last)
	if err != nil || len(benchCloses) == 0 {
		fmt.Printf("[gate] 基准 %s 取不到：err=%v rows=%d\n", score.LowBuyIndexBenchmarkCode, err, len(benchCloses))
		return out, stats
	}
	var fallbackCloses map[int64]float64
	if fallback != "" && fallback != primary {
		fallbackCloses, _ = stock.loadBenchmark(fallback, start, last)
	}

	stats.Days = len(dates)
	for _, date := range dates {
		gate := lowBuyIndexGateFrom(primaryCloses, benchCloses, date)
		if !gate.Ready && len(fallbackCloses) > 0 {
			if alt := lowBuyIndexGateFrom(fallbackCloses, benchCloses, date); alt.Ready {
				gate = alt
				gate.Code = fallback
				stats.FallbackDays++
			}
		}
		if gate.Ready {
			stats.ReadyDays++
		}
		if gate.Ready && gate.Close > gate.Ma20 && gate.Close > gate.Ma60 {
			stats.AllowedDays++
		}
		// gate.Code 只是标签；planBuilder 只用数值字段
		out[strconv.Itoa(date)] = gate
	}
	return out, stats
}

// experimentRow 是一行实验结果。
type experimentRow struct {
	Window, Variant, Primary, Fallback string
	Breadth                            string
	CapLayers                          int
	Start, End                         int
	TotalReturn, AnnualReturn          float64
	MaxDrawdown, Volatility            float64
	Sharpe, Calmar                     float64
	Trades                             int
	WinRate, AvgHoldingDays            float64
	MaxPositions                       int
	AvgPositionRatio, ExposurePct      float64
	Snapshots                          int
	FirstTrade, LastTrade              string
	GateReadyPct, GateAllowedPct       float64
	FallbackDays                       int
	ElapsedSec                         float64
}

func appendExperimentMetric(t *testing.T, path string, row experimentRow) {
	t.Helper()
	newFile := !fileExists(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if newFile {
		_ = w.Write([]string{
			"窗口", "变体", "主指数", "兜底", "广度口径", "单票层数上限", "开始", "结束",
			"总收益%", "年化%", "最大回撤%", "波动率%", "夏普", "卡玛",
			"成交笔数", "胜率%", "平均持仓天", "最大持仓数", "平均仓位%", "有仓天数%",
			"净值天数", "首笔成交", "末笔成交", "闸门就绪%", "闸门放行%", "兜底天数", "耗时秒",
		})
	}
	_ = w.Write([]string{
		row.Window, row.Variant, row.Primary, row.Fallback, row.Breadth, strconv.Itoa(row.CapLayers),
		strconv.Itoa(row.Start), strconv.Itoa(row.End),
		f2(row.TotalReturn), f2(row.AnnualReturn), f2(row.MaxDrawdown), f2(row.Volatility),
		f2(row.Sharpe), f2(row.Calmar), strconv.Itoa(row.Trades), f2(row.WinRate),
		f2(row.AvgHoldingDays), strconv.Itoa(row.MaxPositions), f2(row.AvgPositionRatio),
		f2(row.ExposurePct), strconv.Itoa(row.Snapshots), row.FirstTrade, row.LastTrade,
		f2(row.GateReadyPct), f2(row.GateAllowedPct), strconv.Itoa(row.FallbackDays), f2(row.ElapsedSec),
	})
}

func appendYearly(t *testing.T, path, window, variant string, rows map[int]float64) {
	t.Helper()
	newFile := !fileExists(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("写年度收益失败：%v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if newFile {
		_ = w.Write([]string{"窗口", "变体", "年份", "年度收益%"})
	}
	years := make([]int, 0, len(rows))
	for y := range rows {
		years = append(years, y)
	}
	sort.Ints(years)
	for _, y := range years {
		_ = w.Write([]string{window, variant, strconv.Itoa(y), f2(rows[y])})
	}
}

func appendGateStats(t *testing.T, path, window, variant string, s gateStats) {
	t.Helper()
	newFile := !fileExists(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("写闸门统计失败：%v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if newFile {
		_ = w.Write([]string{"窗口", "变体", "交易日", "闸门就绪天数", "就绪%", "放行天数", "放行%", "兜底天数"})
	}
	_ = w.Write([]string{
		window, variant, strconv.Itoa(s.Days), strconv.Itoa(s.ReadyDays), f2(s.ReadyPct()),
		strconv.Itoa(s.AllowedDays), f2(s.AllowedPct()), strconv.Itoa(s.FallbackDays),
	})
}

// summarizeExperiment 返回有仓天数、年度收益、首末笔成交日。
func summarizeExperiment(result *backtest.Result) (int, map[int]float64, string, string) {
	exposure := 0
	yearRows := map[int]float64{}
	if result == nil {
		return 0, yearRows, "", ""
	}
	var yearEnds []int
	yearValues := map[int]float64{}
	prev := 0.0
	for _, snap := range result.Snapshots {
		if snap.PositionCount > 0 {
			exposure++
		}
		if len(snap.Date) < 4 {
			continue
		}
		year, err := strconv.Atoi(snap.Date[:4])
		if err != nil {
			continue
		}
		if _, seen := yearValues[year]; !seen {
			yearEnds = append(yearEnds, year)
			if prev > 0 {
				yearRows[year] = (snap.TotalValue/prev - 1) * 100
			}
		}
		yearValues[year] = snap.TotalValue
		prev = snap.TotalValue
	}
	// 重新按年末值算（上面先用当年首个快照近似，会有一天的误差）
	yearRows = map[int]float64{}
	sort.Ints(yearEnds)
	base := 0.0
	for _, y := range yearEnds {
		end := yearValues[y]
		if base > 0 {
			yearRows[y] = (end/base - 1) * 100
		} else {
			yearRows[y] = 0
		}
		base = end
	}
	first, last := "", ""
	if len(result.Trades) > 0 {
		first = result.Trades[0].TradeDate
		last = result.Trades[len(result.Trades)-1].TradeDate
	}
	return exposure, yearRows, first, last
}

func filterBars(bars []backtest.PriceBar, start, end int) []backtest.PriceBar {
	out := make([]backtest.PriceBar, 0, len(bars))
	for _, bar := range bars {
		date, err := strconv.Atoi(bar.Date)
		if err != nil || date < start || date > end {
			continue
		}
		out = append(out, bar)
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// wrapEntryRules 通过丢弃计划里的买入行来试不同的入场过滤。
//
// 只影响"开新仓"（add / replace_in）；持仓的 keep/减仓/清仓一律不动。
// 丢弃 replace_in 时会把配对的 replace_out 改回 keep，避免"只卖不买"。
func wrapEntryRules(
	next backtest.PlanBuilder, r entryRules, gates map[string]score.LowBuyIndexGate,
) backtest.PlanBuilder {
	if r.empty() {
		return next
	}
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := next(held, day)
		if len(plan.Rows) == 0 {
			return plan
		}
		// 市场级过滤：直接丢掉所有开仓行
		blockAll := false
		if r.RequireLongCycle {
			gate := gates[day.Date]
			if gate.Ma250 <= 0 || gate.Close <= gate.Ma250 {
				blockAll = true
			}
		}
		if r.NoBear && day.MarketState == score.StateBear {
			blockAll = true
		}
		src := make(map[string]score.Row, len(day.Rows))
		for _, row := range day.Rows {
			src[row.Code] = row
		}
		for i := range plan.Rows {
			row := &plan.Rows[i]
			if row.Action != score.ActionAdd && row.Action != score.ActionReplaceIn {
				continue
			}
			drop := blockAll
			if !drop {
				if s, ok := src[row.Code]; ok {
					switch {
					case r.MinRet5 > 0 && s.StockReturn5D < r.MinRet5:
						drop = true
					case r.AboveMA20 && (s.Ma20 <= 0 || s.Close < s.Ma20):
						drop = true
					case r.MinScore > 0 && s.TotalScore < r.MinScore:
						drop = true
					case r.MinRS20 > 0 && s.RelativeStrength20D < r.MinRS20:
						drop = true
					case r.AboveMA60 && (s.Ma60 <= 0 || s.Close < s.Ma60):
						drop = true
					case r.MinAmount > 0 && s.Amount < r.MinAmount:
						drop = true
					case (r.DDHigh != 0 || r.DDLow != 0) &&
						(s.DrawdownFromHigh20 > r.DDHigh || s.DrawdownFromHigh20 < r.DDLow):
						drop = true
					}
				}
			}
			if !drop {
				continue
			}
			dropPlanAdd(&plan, i)
		}
		// 行业分散：同一天同一行业最多买 N 只（按计划顺序，先到先得）
		if r.MaxPerIndustry > 0 {
			perIndustry := map[string]int{}
			for i := range plan.Rows {
				row := &plan.Rows[i]
				if row.Action != score.ActionAdd && row.Action != score.ActionReplaceIn {
					continue
				}
				industry := src[row.Code].Industry
				if industry == "" {
					continue
				}
				perIndustry[industry]++
				if perIndustry[industry] > r.MaxPerIndustry {
					dropPlanAdd(&plan, i)
				}
			}
		}
		return plan
	}
}

// dropPlanAdd 把一行买入（add / replace_in）改成 keep；
// 若是 replace_in，同时把配对的 replace_out 改回 keep，避免"只卖不买"。
func dropPlanAdd(plan *score.RotationPlan, i int) {
	row := &plan.Rows[i]
	if row.Action == score.ActionReplaceIn {
		for j := range plan.Rows {
			other := &plan.Rows[j]
			if other.Action == score.ActionReplaceOut && other.CandidateCode == row.Code {
				other.Action = score.ActionKeep
				other.ActionLabel = score.ActionLabel(score.ActionKeep)
				other.TargetLayers = other.CurrentLayers
				other.LayersDelta = 0
				other.Reason = "自定义：换入被过滤，保留原持仓"
			}
		}
	}
	row.Action = score.ActionKeep
	row.ActionLabel = score.ActionLabel(score.ActionKeep)
	row.TargetLayers = 0
	row.LayersDelta = 0
	row.Reason = "自定义：入场条件不满足"
	row.IsExecutable = false
}

// forceSellRow 把一行持仓改成"整仓卖出"。
func forceSellRow(row *score.RotationRow, reason string) {
	if row.CurrentLayers <= 0 {
		return
	}
	row.Action = score.ActionClear
	row.ActionLabel = score.ActionLabel(score.ActionClear)
	row.TargetLayers = 0
	row.LayersDelta = -row.CurrentLayers
	row.Reason = reason
	row.IsExecutable = true
}

// wrapRegime 按 market_state 切换牛市 / 非牛市的不同应对分支。
//
// 牛市 = state=="bull"；非牛市 = range 或 bear。
// 非牛市可以：不开新仓 / 全部清仓 / 熊市才清仓 / 单票层数压到 20%~30%。
func wrapRegime(next backtest.PlanBuilder, r regimeRules) backtest.PlanBuilder {
	if r.empty() {
		return next
	}
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := next(held, day)
		if len(plan.Rows) == 0 || day.MarketState == score.StateBull {
			return plan
		}
		if r.OnlyBull || r.ClearNotBull {
			for i := range plan.Rows {
				action := plan.Rows[i].Action
				if action == score.ActionAdd || action == score.ActionReplaceIn {
					dropPlanAdd(&plan, i)
				}
			}
		}
		if r.ClearNotBull || (r.ClearBear && day.MarketState == score.StateBear) {
			for i := range plan.Rows {
				row := &plan.Rows[i]
				if row.SourceType != "holding" {
					continue
				}
				forceSellRow(row, "牛市分支：非牛市清仓")
			}
		}
		if r.NonBullCap > 0 {
			for i := range plan.Rows {
				row := &plan.Rows[i]
				switch row.Action {
				case score.ActionAdd, score.ActionReplaceIn, score.ActionAddLayer:
				default:
					continue
				}
				if row.TargetLayers > r.NonBullCap {
					row.TargetLayers = r.NonBullCap
				}
				if row.LayersDelta > r.NonBullCap {
					row.LayersDelta = r.NonBullCap
				}
			}
		}
		return plan
	}
}

// wrapExitRules 在不改生产代码的前提下改写"持仓行"的出场决策。
//
// 原策略的出场有两条：跌破 MA30×0.96（趋势止损）、低吸广度不足（强制清仓）。
// 这里可以额外加：止盈、移动止盈（自最高价回撤）、最长持有天数，
// 以及把 MA30 止损放宽到 StopRatio（只对"跌破MA30"那一条生效，广度清仓不动）。
func wrapExitRules(next backtest.PlanBuilder, r exitRules) backtest.PlanBuilder {
	if r.empty() {
		return next
	}
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := next(held, day)
		byCode := make(map[string]score.Row, len(held))
		for _, h := range held {
			byCode[h.Code] = h
		}
		for i := range plan.Rows {
			row := &plan.Rows[i]
			if row.SourceType != "holding" {
				continue
			}
			h, ok := byCode[row.Code]
			if !ok || h.PositionLayers <= 0 {
				continue
			}
			// ① 放宽默认止损：仍在 MA30×StopRatio 上方就撤销售出
			if r.StopRatio > 0 && strings.Contains(row.Reason, "跌破MA30") {
				if h.Ma30 > 0 && h.CurrentPrice >= h.Ma30*r.StopRatio {
					row.Action = score.ActionKeep
					row.ActionLabel = score.ActionLabel(score.ActionKeep)
					row.TargetLayers = h.PositionLayers
					row.LayersDelta = 0
					row.Reason = "自定义：MA30 止损放宽"
					continue
				}
			}
			// ② 强制出场
			reason := ""
			switch {
			case r.TakeProfit > 0 && h.ProfitLossPct >= r.TakeProfit:
				reason = fmt.Sprintf("止盈 +%.1f%%", h.ProfitLossPct)
			case r.TrailStop > 0 && h.HighestPrice > 0 && h.CurrentPrice > 0 &&
				(h.CurrentPrice/h.HighestPrice-1)*100 <= -r.TrailStop:
				reason = fmt.Sprintf("移动止盈：自高点回撤 %.1f%%", (1-h.CurrentPrice/h.HighestPrice)*100)
			case r.MaxHold > 0 && h.HoldingTradeDays >= r.MaxHold:
				reason = fmt.Sprintf("持有 %d 个交易日到期", h.HoldingTradeDays)
			}
			if reason == "" {
				continue
			}
			row.Action = score.ActionClear
			row.ActionLabel = score.ActionLabel(score.ActionClear)
			row.TargetLayers = 0
			row.LayersDelta = -h.PositionLayers
			row.Reason = reason
			row.IsExecutable = true
		}
		return plan
	}
}

// capPlanLayers 限制每一笔买入的层数（从而限制单票权重 = cap / TotalLayers）。
//
// ⚠ 为什么必须改计划行、而不是只改 cfg.MaxLayersPerStock：
// 引擎的 `buyPosition` 会用 `MaxLayersPerStock` 夹住 target，但**预算**是调用方
// 按计划行的 `LayersDelta` 算的（`layerBudget(totalValue, row.LayersDelta)`）。
// 只改 cfg 时，层数记账被夹到 2，实际花的钱仍是 10 层的钱 —— 实测 cap2 与 base
// 完全同一条净值曲线，就是这个原因。所以要把计划行的 TargetLayers/LayersDelta
// 一起夹住，预算才会跟着降下来。
func capPlanLayers(next backtest.PlanBuilder, cap int) backtest.PlanBuilder {
	if cap < 1 {
		return next
	}
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := next(held, day)
		for i := range plan.Rows {
			row := &plan.Rows[i]
			switch row.Action {
			case score.ActionAdd, score.ActionReplaceIn, score.ActionAddLayer:
			default:
				continue
			}
			if row.TargetLayers > cap {
				row.TargetLayers = cap
			}
			if row.LayersDelta > cap {
				row.LayersDelta = cap
			}
		}
		return plan
	}
}

// scaleReady 把"绝对广度就绪数"折成固定 5000 只市场口径。
//
// 分数阈值是绝对值（1000/1500/2000/2600/900），只在 5000 只的市场里才有原本含义。
// 若当日全市场只有 1200 只，就用 ready × 5000/1200 去比阈值 —— 等价于把阈值
// 变成相对比例（20% / 30% / 40% / 52% / 18%）。
func scaleReady(ready, universe map[string]int, ref int) map[string]int {
	out := make(map[string]int, len(ready))
	for date, count := range ready {
		total := universe[date]
		if total <= 0 || ref <= 0 {
			out[date] = count
			continue
		}
		out[date] = int(math.Round(float64(count) * float64(ref) / float64(total)))
	}
	return out
}

// normalizeIndexCode 把实验参数里的短代码补成数据湖里的带后缀代码。
//
//	399006 -> 399006.SZ    000300 -> 000300.SH    000001 -> 000001.SH
func normalizeIndexCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || strings.Contains(code, ".") {
		return code
	}
	if strings.HasPrefix(code, "399") {
		return code + ".SZ"
	}
	return code + ".SH"
}
