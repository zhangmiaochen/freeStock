package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/chanlun"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 缠论辅助 · 分类网格实验（**只读、只测，不改生产代码**）。
//
// 目的：在既有 `BacktestRequest.ChanlunAssist` 的能力范围内，系统性地试
// 「怎么用缠论」才有正向作用：
//
//	A 类 买入：只在计划买入日加层（boostHoldings=false）
//	B 类 持仓：持仓中出现确认二/三买也加层（boostHoldings=true）
//	C 类 卖出：只做分层卖出（一卖/二卖/三卖）
//	D 类 信号质量：新鲜度 3/5/10/20、最低综合分 0/70/80、触发信号是否算
//	E 类 力度：三买 +1/+2/+3 层、单股上限 6/7/8/10
//
// 先在同一组合的 4 个不重叠年度窗口上筛，再拿筛出的方案去另一个组合（不同股票池）
// 做样本外验证。**所有方案与基线共用同一份评分、日线、撮合与费用**，只改叠加层配置。
//
// 运行（默认跳过）：
//
//	$env:FREESTOCK_CA_DB="...\bin\portfolio.db"
//	$env:FREESTOCK_CA_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	$env:FREESTOCK_CA_PORTFOLIO="pf_1791522403274487100_0002"
//	$env:FREESTOCK_CA_GRID_OUT=".gotmp\chanlun-grid.json"
//	go test ./internal/service -run TestChanlunAssistGrid -v -timeout 180m
func TestChanlunAssistGrid(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_CA_DB")
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	portfolioID := os.Getenv("FREESTOCK_CA_PORTFOLIO")
	if srcDB == "" || lakeRoot == "" || portfolioID == "" {
		t.Skip("未设置 FREESTOCK_CA_DB / FREESTOCK_CA_LAKE / FREESTOCK_CA_PORTFOLIO")
	}

	// 每只股票每天两条诊断日志会把几万行刷进测试输出，这里只保留 Error。
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	dstDB := os.Getenv("FREESTOCK_CA_WORKDB")
	if dstDB == "" {
		dstDB = filepath.Join(t.TempDir(), "portfolio.db")
		copyFile(t, srcDB, dstDB)
	} else if _, err := os.Stat(dstDB); err != nil {
		copyFile(t, srcDB, dstDB)
	}
	cfgDir, err := os.MkdirTemp("", "freestock-ca-grid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfgDir) })
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dstDB)
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		t.Fatalf("读组合失败：%v", err)
	}
	t.Logf("组合 %s（%s）策略 %s，目标 %d 只 / %d 层，范围 %s",
		item.Name, item.ID, item.StrategyID, item.TargetPositionCount, item.TotalLayers, item.ScopeValueJSON)

	windows := []struct {
		name       string
		start, end int
	}{
		{"W1_2022-01-12_2023-10-12", 20220112, 20231012},
		{"W2_2023-10-13_2024-10-11", 20231013, 20241011},
		{"W3_2024-10-14_2025-10-13", 20241014, 20251013},
		{"W4_2025-10-14_2026-10-19", 20251014, 20261019},
	}

	cases := gridCases()
	if raw := os.Getenv("FREESTOCK_CA_GRID_CASES"); raw != "" {
		wanted := map[string]bool{"base": true}
		for _, item := range strings.Split(raw, ",") {
			if name := strings.TrimSpace(item); name != "" {
				wanted[name] = true
			}
		}
		filtered := make([]gridCase, 0, len(cases))
		for _, c := range cases {
			if wanted[c.name] {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) < 2 {
			t.Fatalf("FREESTOCK_CA_GRID_CASES 只匹配到 %d 个方案", len(filtered))
		}
		cases = filtered
	}
	results := make([]gridResult, 0, len(windows)*len(cases))
	for _, win := range windows {
		// 每个窗口换一个新 provider：lake 的批量缓存会按区间累积，4 个窗口叠加会吃掉几 GB。
		// 换源会清掉批量缓存，保证内存只与单个窗口相关。
		stock.applySource(config.KindCnLake, lakeRoot)
		began := time.Now()
		w := buildGridWindow(t, svc, store, item, win.name, win.start, win.end)
		t.Logf("%s：交易日 %d、日线 %d、股票 %d、准备耗时 %s",
			win.name, len(w.days), len(w.bars), w.analyzer.CodeCount(),
			time.Since(began).Round(time.Second))

		for _, c := range cases {
			runBegan := time.Now()
			metrics, stats, err := runGridCase(w, c)
			if err != nil {
				t.Fatalf("%s / %s 回测失败：%v", win.name, c.name, err)
			}
			results = append(results, gridResult{
				Window: win.name, Config: c.name,
				TotalReturn: metrics.TotalReturn, AnnualReturn: metrics.AnnualReturn,
				MaxDrawdown: metrics.MaxDrawdown, Sharpe: metrics.SharpeRatio,
				Calmar: metrics.CalmarRatio, Sortino: metrics.SortinoRatio,
				Trades: metrics.TotalTrades, WinRate: metrics.WinRate,
				AvgPositionRatio: metrics.AvgPositionRatio,
				BuyLayers:        stats.BuyLayersAdded, SellLayers: stats.SellLayersSold,
				Buy2Boosts: stats.Buy2Boosts, Buy3Boosts: stats.Buy3Boosts,
				HoldBoosts: stats.HoldBoosts, Sell1: stats.Sell1Reduces,
				Sell2: stats.Sell2Reduces, Sell3: stats.Sell3Reduces,
				ElapsedSeconds: time.Since(runBegan).Seconds(),
			})
		}
		// 确定性校验：同一窗口把基线再跑一遍，必须与第一次逐字段一致。
		repeat, _, err := runGridCase(w, cases[0])
		if err != nil {
			t.Fatalf("%s 基线的重复运行失败：%v", win.name, err)
		}
		for _, r := range results {
			if r.Window == win.name && r.Config == "base" {
				if r.TotalReturn != repeat.TotalReturn || r.MaxDrawdown != repeat.MaxDrawdown ||
					r.Trades != repeat.TotalTrades {
					t.Errorf("%s 基线不可复现：第一次 %.4f%% / %.2f%% / %d，第二次 %.4f%% / %.2f%% / %d",
						win.name, r.TotalReturn, r.MaxDrawdown, r.Trades,
						repeat.TotalReturn, repeat.MaxDrawdown, repeat.TotalTrades)
				}
				break
			}
		}
		// 主动释放该窗口的大对象，避免 4 个窗口叠加占满内存。
		w.days, w.bars, w.analyzer, w.buildPlan = nil, nil, nil, nil
		runtime.GC()
	}
	report := summarizeGrid(results)
	printGridReport(t, report)

	out := os.Getenv("FREESTOCK_CA_GRID_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "chanlun-grid.json")
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	raw, err := json.MarshalIndent(map[string]any{
		"generatedAt": time.Now().Format(time.RFC3339),
		"portfolio":   map[string]any{"id": item.ID, "name": item.Name, "strategy": item.StrategyID},
		"windows":     windowDescriptions(windows),
		"cases":       caseDescriptions(cases),
		"results":     results,
		"summary":     report,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	t.Logf("网格结果已写入：%s", out)
}

// gridCase 是一个待测方案。cfg == nil 表示基线。
//
// lagDays > 0 是**安慰剂对照**：信号查询回退 N 个自然日，规则与频率相同，
// 但时间点与真实结构无关。用来判断收益差是「信号选时」还是「加层本身/资金再分配」。
type gridCase struct {
	name    string
	cfg     *chanlunassist.Config
	note    string
	lagDays int
}

// gridCases 是分类后的方案清单。
//
// 只组合「有意义」的维度，不做全笛卡尔积：先把买入方式分开，再在最有希望的
// 「持仓加层」上试信号质量与力度，最后单独看卖出分层。
func gridCases() []gridCase {
	base := func(mutate func(*chanlunassist.Config)) *chanlunassist.Config {
		c := chanlunassist.DefaultConfig()
		c.Sell1Layers, c.Sell2Layers, c.Sell3Layers = 0, 0, 0 // 默认先关掉卖出侧
		c.BoostHoldings = true
		c.MaxLayersPerStock = 7
		mutate(&c)
		return &c
	}
	return []gridCase{
		{name: "base", cfg: nil, note: "基线：现有趋势低吸"},

		// A 类：只在计划买入日加层。
		{name: "A1_plan_buy23", cfg: base(func(c *chanlunassist.Config) {
			c.BoostHoldings = false
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 1, 2
		}), note: "计划买入日：二买+1/三买+2，不加持仓"},

		// B 类：持仓中也加层（三买为主）。
		{name: "B1_hold_b3_f10", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
		}), note: "持仓+计划：三买+2，新鲜度10"},
		{name: "B2_hold_b23_f10", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 1, 2
		}), note: "持仓+计划：二买+1/三买+2"},
		{name: "B3_hold_b2_f10", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 1, 0
		}), note: "持仓+计划：只认二买+1"},

		// D 类：信号质量。
		{name: "D1_b3_f3", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.BuyFreshBars = 3
		}), note: "三买+2，新鲜度3"},
		{name: "D2_b3_f5", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.BuyFreshBars = 5
		}), note: "三买+2，新鲜度5"},
		{name: "D3_b3_f20", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.BuyFreshBars = 20
		}), note: "三买+2，新鲜度20"},
		{name: "D4_b3_score70", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.MinSignalScore = 70
		}), note: "三买+2，要求综合分>=70"},
		{name: "D5_b3_score80", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.MinSignalScore = 80
		}), note: "三买+2，要求综合分>=80"},
		{name: "D6_b3_triggered", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.AllowTriggered = true
		}), note: "三买+2，触发也算（不限于确认）"},

		// E 类：力度与上限。
		{name: "E1_b3_plus1", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 1
		}), note: "三买+1（更轻）"},
		{name: "E2_b3_plus3", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 3
		}), note: "三买+3（更重）"},
		{name: "E3_b3_max6", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.MaxLayersPerStock = 6
		}), note: "三买+2，单股上限6"},
		{name: "E4_b3_max10", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.MaxLayersPerStock = 10
		}), note: "三买+2，单股上限10"},

		// C 类：卖出分层（叠加在 B1 上）。
		{name: "C1_b3_sell3x1", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.Sell3Layers = 1
		}), note: "三买+2，三卖-1"},
		{name: "C2_b3_sell3x2", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.Sell3Layers = 2
		}), note: "三买+2，三卖-2"},
		{name: "C3_b3_sell3x3", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.Sell3Layers = 3
		}), note: "三买+2，三卖-3（基本清仓）"},
		{name: "C4_b3_sell_all", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
			c.Sell1Layers, c.Sell2Layers, c.Sell3Layers = 1, 1, 2
		}), note: "三买+2，一/二/三卖都减"},

		// P 类：安慰剂对照（同样加层，但信号回退 20 / 45 个自然日）。
		{name: "P1_placebo_f10_lag20", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
		}), note: "安慰剂：三买+2，信号回退20日", lagDays: 20},
		{name: "P2_placebo_f10_lag45", cfg: base(func(c *chanlunassist.Config) {
			c.Buy2ExtraLayers, c.Buy3ExtraLayers = 0, 2
		}), note: "安慰剂：三买+2，信号回退45日", lagDays: 45},
	}
}

func caseDescriptions(cases []gridCase) []map[string]string {
	out := make([]map[string]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, map[string]string{"name": c.name, "note": c.note, "lagDays": strconv.Itoa(c.lagDays)})
	}
	return out
}

func windowDescriptions(windows []struct {
	name       string
	start, end int
}) []map[string]any {
	out := make([]map[string]any, 0, len(windows))
	for _, win := range windows {
		out = append(out, map[string]any{
			"name": win.name, "startDate": win.start, "endDate": win.end,
		})
	}
	return out
}

// gridWindow 是一个窗口的只读数据集（评分、日线、闸门、分析器）。
type gridWindow struct {
	name      string
	cfg       backtest.Config
	days      []backtest.StrengthDay
	bars      []backtest.PriceBar
	benchmark map[string]float64
	buildPlan backtest.PlanBuilder
	analyzer  *chanlunassist.Analyzer
	chanBars  map[string][]chanlun.Bar
	actions   map[string][]backtest.CorporateAction
}

// permissiveAssist 给分析器用的「宽口径」配置：新鲜度放到 30、触发信号也算，
// 具体方案的新鲜度/评分/类型过滤由 gridSignalProvider 在查询时再做，
// 这样多个方案可以共享同一份分析缓存，不必为每个方案重算一遍结构。
func permissiveAssist() chanlunassist.Config {
	return chanlunassist.Config{
		Enable: true, Buy2ExtraLayers: 1, Buy3ExtraLayers: 1,
		BuyFreshBars: 30, Sell1Layers: 1, Sell2Layers: 1, Sell3Layers: 1,
		SellFreshBars: 30, MaxLayersPerStock: 10, AllowTriggered: true,
	}
}

func buildGridWindow(
	t *testing.T, svc *PortfolioService, store *portfolio.Store,
	item portfolio.Portfolio, name string, start, end int,
) *gridWindow {
	t.Helper()
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(start), int64(end))
	if err != nil {
		t.Fatalf("%s 读评分失败：%v", name, err)
	}
	if len(strengths) == 0 {
		t.Fatalf("%s 区间内没有评分", name)
	}
	days, codes, _, readyByDate := svc.buildStrengthDays(store, strengths, item)
	strengths = nil
	if len(days) == 0 {
		t.Fatalf("%s 没有可用评分日", name)
	}
	days = svc.attachSignalsToDays(store, days)
	bars, err := svc.loadBacktestBars(codes, start, end)
	if err != nil || len(bars) == 0 {
		t.Fatalf("%s 取日线失败：%v", name, err)
	}

	benchmarkCode := item.BenchmarkCode
	if benchmarkCode == "" {
		benchmarkCode = score.LowBuyIndexBenchmarkCode
	}
	benchmark := map[string]float64{}
	if svc.stock != nil {
		if closes, err := svc.stock.loadBenchmark(benchmarkCode, start, end); err == nil {
			for date, closePrice := range closes {
				benchmark[fmt.Sprintf("%d", date)] = closePrice
			}
		}
	}

	targetCount, maxStocks := item.TargetPositionCount, item.MaxStockCount
	if maxStocks < targetCount {
		maxStocks = targetCount
	}
	cfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		BenchmarkCode:       benchmarkCode,
		StartDate:           fmt.Sprintf("%d", start),
		EndDate:             fmt.Sprintf("%d", end),
		TargetPositionCount: targetCount, MaxStockCount: maxStocks,
		TotalLayers:       item.TotalLayers,
		MaxLayersPerStock: maxLayersForBacktest(item.StrategyID, item.TotalLayers),
		InitialCapital:    100000,
		MarketScope:       scopeOfPortfolio(item),
	}

	gates := map[string]score.LowBuyIndexGate{}
	if item.StrategyID == portfolio.StrategyTrendLowBuy && svc.stock != nil {
		dates := make([]string, 0, len(days))
		for _, day := range days {
			dates = append(dates, day.Date)
		}
		gates = svc.stock.lowBuyIndexGatesForRange(dates)
	}
	buildPlan := svc.backtestPlanBuilder(item, store, marketStateByDate(store, days), targetCount, maxStocks, gates, readyByDate)

	chanBars, err := svc.loadChanlunBars(codes, shiftBack(start, 550), end)
	if err != nil || len(chanBars) == 0 {
		t.Fatalf("%s 取缠论日线失败：%v", name, err)
	}
	var actions map[string][]backtest.CorporateAction
	if lk := svc.stock.lakeHandle(); lk != nil && lk.HasCorporateActions() {
		actions = buildBacktestActions(lk, start, end)
	}
	return &gridWindow{
		name: name, cfg: cfg, days: days, bars: bars, benchmark: benchmark,
		buildPlan: buildPlan, analyzer: chanlunassist.NewAnalyzer(chanBars, chanlun.DefaultConfig(), permissiveAssist()),
		chanBars: chanBars, actions: actions,
	}
}

// gridSignalProvider 在共享分析器之上做「方案级」过滤。
type gridSignalProvider struct {
	inner   chanlunassist.Provider
	cfg     chanlunassist.Config
	lagDays int
}

func (p gridSignalProvider) Signals(code string, asOf int) chanlunassist.Snapshot {
	if p.lagDays > 0 {
		asOf = shiftBack(asOf, p.lagDays)
	}
	snapshot := p.inner.Signals(code, asOf)
	pick := func(sig *chanlunassist.Signal, layers, fresh int) *chanlunassist.Signal {
		if sig == nil || layers <= 0 {
			return nil
		}
		if !p.cfg.AllowTriggered && sig.State != chanlun.StateConfirmed {
			return nil
		}
		if sig.AgeBars > fresh {
			return nil
		}
		if p.cfg.MinSignalScore > 0 && sig.Score < p.cfg.MinSignalScore {
			return nil
		}
		return sig
	}
	return chanlunassist.Snapshot{
		Buy2:  pick(snapshot.Buy2, p.cfg.Buy2ExtraLayers, p.cfg.BuyFreshBars),
		Buy3:  pick(snapshot.Buy3, p.cfg.Buy3ExtraLayers, p.cfg.BuyFreshBars),
		Sell1: pick(snapshot.Sell1, p.cfg.Sell1Layers, p.cfg.SellFreshBars),
		Sell2: pick(snapshot.Sell2, p.cfg.Sell2Layers, p.cfg.SellFreshBars),
		Sell3: pick(snapshot.Sell3, p.cfg.Sell3Layers, p.cfg.SellFreshBars),
	}
}

func runGridCase(w *gridWindow, c gridCase) (backtest.Metrics, chanlunassist.Stats, error) {
	result, stats, err := runEngineCase(w, c)
	if err != nil {
		return backtest.Metrics{}, chanlunassist.Stats{}, err
	}
	return backtest.CalculateMetrics(result.Snapshots, result.Trades, w.cfg.InitialCapital), stats, nil
}

// runEngineCase 返回完整结果（逐日净值 + 逐笔成交），供网格与明细归因共用。
func runEngineCase(w *gridWindow, c gridCase) (*backtest.Result, chanlunassist.Stats, error) {
	engine := backtest.New(w.cfg, w.days, w.bars, w.benchmark)
	if len(w.actions) > 0 {
		engine.SetCorporateActions(w.actions)
	}
	if c.cfg == nil {
		result, err := engine.Run(w.buildPlan)
		if err != nil {
			return nil, chanlunassist.Stats{}, err
		}
		return result, chanlunassist.Stats{}, nil
	}
	cfg := c.cfg.Sanitized()
	provider := gridSignalProvider{inner: w.analyzer, cfg: cfg, lagDays: c.lagDays}
	state := chanlunassist.NewState()
	base := w.buildPlan
	builder := func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		plan := base(held, day)
		asOf, err := strconv.Atoi(day.Date)
		if err != nil {
			return plan
		}
		return chanlunassist.Apply(plan, held, asOf, provider, cfg, state)
	}
	result, err := engine.Run(builder)
	if err != nil {
		return nil, chanlunassist.Stats{}, err
	}
	return result, state.Stats, nil
}

// gridResult 是「一个窗口 × 一个方案」的结果。
type gridResult struct {
	Window           string  `json:"window"`
	Config           string  `json:"config"`
	TotalReturn      float64 `json:"totalReturn"`
	AnnualReturn     float64 `json:"annualReturn"`
	MaxDrawdown      float64 `json:"maxDrawdown"`
	Sharpe           float64 `json:"sharpe"`
	Calmar           float64 `json:"calmar"`
	Sortino          float64 `json:"sortino"`
	Trades           int     `json:"trades"`
	WinRate          float64 `json:"winRate"`
	AvgPositionRatio float64 `json:"avgPositionRatio"`
	BuyLayers        int     `json:"buyLayersAdded"`
	SellLayers       int     `json:"sellLayersSold"`
	Buy2Boosts       int     `json:"buy2Boosts"`
	Buy3Boosts       int     `json:"buy3Boosts"`
	HoldBoosts       int     `json:"holdBoosts"`
	Sell1            int     `json:"sell1"`
	Sell2            int     `json:"sell2"`
	Sell3            int     `json:"sell3"`
	ElapsedSeconds   float64 `json:"elapsedSeconds"`
}

// gridSummary 是跨窗口汇总：均值、最差窗口、正收益窗口数。
type gridSummary struct {
	Config           string  `json:"config"`
	MeanReturnDelta  float64 `json:"meanReturnDelta"`
	WorstReturnDelta float64 `json:"worstReturnDelta"`
	MeanDDDelta      float64 `json:"meanDrawdownDelta"`
	WorstDDDelta     float64 `json:"worstDrawdownDelta"`
	MeanCalmarDelta  float64 `json:"meanCalmarDelta"`
	PositiveWindows  int     `json:"positiveReturnWindows"`
	NonWorseWindows  int     `json:"nonWorseDrawdownWindows"`
	MeanReturn       float64 `json:"meanTotalReturn"`
	MeanDrawdown     float64 `json:"meanMaxDrawdown"`
	TotalTrades      int     `json:"totalTrades"`
	Notes            string  `json:"notes"`
}

func summarizeGrid(results []gridResult) []gridSummary {
	byWindow := map[string]map[string]gridResult{}
	windowOrder := make([]string, 0, 4)
	for _, r := range results {
		if byWindow[r.Window] == nil {
			byWindow[r.Window] = map[string]gridResult{}
			windowOrder = append(windowOrder, r.Window)
		}
		byWindow[r.Window][r.Config] = r
	}
	sort.Strings(windowOrder)
	configs := make([]string, 0, 32)
	seen := map[string]bool{}
	for _, r := range results {
		if !seen[r.Config] {
			seen[r.Config] = true
			configs = append(configs, r.Config)
		}
	}
	out := make([]gridSummary, 0, len(configs))
	for _, name := range configs {
		if name == "base" {
			continue
		}
		summary := gridSummary{Config: name}
		for _, win := range windowOrder {
			base, okBase := byWindow[win]["base"]
			run, okRun := byWindow[win][name]
			if !okBase || !okRun {
				continue
			}
			summary.MeanReturnDelta += run.TotalReturn - base.TotalReturn
			summary.MeanDDDelta += run.MaxDrawdown - base.MaxDrawdown
			summary.MeanCalmarDelta += run.Calmar - base.Calmar
			summary.MeanReturn += run.TotalReturn
			summary.MeanDrawdown += run.MaxDrawdown
			summary.TotalTrades += run.Trades
			if run.TotalReturn > base.TotalReturn {
				summary.PositiveWindows++
			}
			if run.MaxDrawdown <= base.MaxDrawdown {
				summary.NonWorseWindows++
			}
		}
		n := float64(len(windowOrder))
		if n == 0 {
			continue
		}
		summary.MeanReturnDelta /= n
		summary.MeanDDDelta /= n
		summary.MeanCalmarDelta /= n
		summary.MeanReturn /= n
		summary.MeanDrawdown /= n
		// 最差窗口
		summary.WorstReturnDelta = 1e9
		summary.WorstDDDelta = -1e9
		for _, win := range windowOrder {
			base, okBase := byWindow[win]["base"]
			run, okRun := byWindow[win][name]
			if !okBase || !okRun {
				continue
			}
			if d := run.TotalReturn - base.TotalReturn; d < summary.WorstReturnDelta {
				summary.WorstReturnDelta = d
			}
			if d := run.MaxDrawdown - base.MaxDrawdown; d > summary.WorstDDDelta {
				summary.WorstDDDelta = d
			}
		}
		out = append(out, summary)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MeanReturnDelta != out[j].MeanReturnDelta {
			return out[i].MeanReturnDelta > out[j].MeanReturnDelta
		}
		return out[i].WorstReturnDelta > out[j].WorstReturnDelta
	})
	return out
}

func printGridReport(t *testing.T, summary []gridSummary) {
	t.Logf("%-20s %9s %9s %8s %8s %6s %6s", "配置", "均收益Δ", "最差Δ", "均回撤Δ", "最差回撤Δ", "正窗口", "不差回撤")
	for _, s := range summary {
		t.Logf("%-20s %+8.2f %+8.2f %+8.2f %+8.2f %5d/%d %5d/%d",
			s.Config, s.MeanReturnDelta, s.WorstReturnDelta, s.MeanDDDelta, s.WorstDDDelta,
			s.PositiveWindows, 4, s.NonWorseWindows, 4)
	}
}
