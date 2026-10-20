package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
	"freestock/internal/portfolio"
)

// 真实数据对照实验：同一组合、同一区间、同一撮合，只改「缠论辅助」开关。
//
// 跑法（**只读副本**，绝不写真实组合库）：
//
//	$env:FREESTOCK_CA_DB="D:\workspace\go\stockSoft\freeStock\bin\portfolio.db"
//	$env:FREESTOCK_CA_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	$env:FREESTOCK_CA_PORTFOLIO="pf_1791522403274487100_0002"
//	$env:FREESTOCK_CA_START="20241014"
//	$env:FREESTOCK_CA_END="20261019"
//	$env:FREESTOCK_CA_RUNS="baseline,sells_only,combined"
//	$env:FREESTOCK_CA_OUT="..\..\docs\缠论辅助组合回测_原始结果.json"
//	go test ./internal/service -run TestChanlunAssistRealBacktest -v -timeout 180m
//
// 建议同时设置 FREESTOCK_CA_WORKDB（可复用的副本路径）：2.9GB 的库复制一次要十几秒，
// 多次调参时不必每次重拷。
func TestChanlunAssistRealBacktest(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_CA_DB")
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	portfolioID := os.Getenv("FREESTOCK_CA_PORTFOLIO")
	start := envInt(t, "FREESTOCK_CA_START", 0)
	end := envInt(t, "FREESTOCK_CA_END", 0)
	if srcDB == "" || lakeRoot == "" || portfolioID == "" || start <= 0 || end <= 0 {
		t.Skip("未设置 FREESTOCK_CA_DB / FREESTOCK_CA_LAKE / FREESTOCK_CA_PORTFOLIO / FREESTOCK_CA_START / FREESTOCK_CA_END")
	}

	dstDB := os.Getenv("FREESTOCK_CA_WORKDB")
	if dstDB == "" {
		dstDB = filepath.Join(t.TempDir(), "portfolio.db")
		copyFile(t, srcDB, dstDB)
	} else if _, err := os.Stat(dstDB); err != nil {
		copyFile(t, srcDB, dstDB)
	}
	t.Logf("实验库：%s", dstDB)

	// 配置目录**不要用 t.TempDir()**：数据湖的「按股票日线缓存」会在后台异步建缓存，
	// 测试结束时仍在写文件，testing 的 TempDir 清理会因此报 "directory is not empty"
	// 把整个用例判失败。这里用自己管理的临时目录，清理失败直接忽略。
	cfgDir, err := os.MkdirTemp("", "freestock-ca-*")
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
		t.Fatalf("读组合 %s 失败：%v", portfolioID, err)
	}
	t.Logf("组合 %s（%s）策略 %s，目标 %d 只 / %d 层，范围 %s",
		item.Name, item.ID, item.StrategyID, item.TargetPositionCount, item.TotalLayers, item.ScopeValueJSON)

	// 方案定义：只改缠论辅助配置，其余完全一致。
	base := chanlunassist.DefaultConfig()
	base.MaxLayersPerStock = 7
	sellsOnly := chanlunassist.Config{
		Enable: true, Sell1Layers: 1, Sell2Layers: 1, Sell3Layers: 2,
		SellFreshBars: 10, MaxLayersPerStock: 7,
	}
	buysOnly := chanlunassist.Config{
		Enable: true, Buy2ExtraLayers: 1, Buy3ExtraLayers: 2,
		BuyFreshBars: 10, MaxLayersPerStock: 7,
	}
	// 「持仓中也加层」的扩展方案：缠论三买常出现在持仓期间，而计划当天并不买入。
	combinedHold := base
	combinedHold.BoostHoldings = true
	// 敏感性分析：统一改新鲜度窗口（例如 10 / 20 / 30 个交易日）。
	if raw := os.Getenv("FREESTOCK_CA_FRESH"); raw != "" {
		fresh, convErr := strconv.Atoi(raw)
		if convErr != nil || fresh <= 0 {
			t.Fatalf("FREESTOCK_CA_FRESH=%q 不是正整数", raw)
		}
		base.BuyFreshBars, base.SellFreshBars = fresh, fresh
		base.MaxLayersPerStock = 7
		sellsOnly.SellFreshBars = fresh
		buysOnly.BuyFreshBars = fresh
		combinedHold.BuyFreshBars, combinedHold.SellFreshBars = fresh, fresh
		combinedHold.MaxLayersPerStock = 7
		t.Logf("新鲜度窗口已覆盖为 %d 个交易日", fresh)
	}
	type variant struct {
		name string
		cfg  *chanlunassist.Config
	}
	all := map[string]variant{
		"baseline":      {"baseline", nil},
		"sells_only":    {"sells_only", &sellsOnly},
		"buys_only":     {"buys_only", &buysOnly},
		"combined":      {"combined", &base},
		"combined_hold": {"combined_hold", &combinedHold},
	}
	selected := strings.Split(os.Getenv("FREESTOCK_CA_RUNS"), ",")
	if len(selected) == 1 && strings.TrimSpace(selected[0]) == "" {
		selected = []string{"baseline", "sells_only", "combined"}
	}
	var variants []variant
	for _, name := range selected {
		name = strings.TrimSpace(name)
		if v, ok := all[name]; ok {
			variants = append(variants, v)
		}
	}
	if len(variants) == 0 {
		t.Fatalf("FREESTOCK_CA_RUNS 里没有可识别的方案：%v", selected)
	}

	type runResult struct {
		Name      string                    `json:"name"`
		Config    *chanlunassist.Config     `json:"config,omitempty"`
		Elapsed   float64                   `json:"elapsedSeconds"`
		Metrics   backtest.Metrics          `json:"metrics"`
		Summary   map[string]any            `json:"summary"`
		Daily     []portfolio.BacktestDaily `json:"daily"`
		Trades    []portfolio.BacktestTrade `json:"trades"`
		Truncated bool                      `json:"tradesTruncated,omitempty"`
	}
	type output struct {
		GeneratedAt    string      `json:"generatedAt"`
		PortfolioID    string      `json:"portfolioId"`
		PortfolioName  string      `json:"portfolioName"`
		StrategyID     string      `json:"strategyId"`
		StartDate      int         `json:"startDate"`
		EndDate        int         `json:"endDate"`
		InitialCapital float64     `json:"initialCapital"`
		Runs           []runResult `json:"runs"`
		Notes          []string    `json:"notes"`
	}

	out := output{
		GeneratedAt:    time.Now().Format(time.RFC3339),
		PortfolioID:    item.ID,
		PortfolioName:  item.Name,
		StrategyID:     item.StrategyID,
		StartDate:      start,
		EndDate:        end,
		InitialCapital: 100000,
		Notes: []string{
			"同一组合、同一评分、同一日线、同一撮合；只切换缠论辅助配置。",
			"买/卖信号均由截断到计划日的日线现算，无未来函数。",
			"层=总资产/总层数；低吸基准建仓 2 层，二买 +1 层、三买 +2 层，单股上限 4 层。",
		},
	}

	for _, v := range variants {
		began := time.Now()
		req := BacktestRequest{
			PortfolioID: item.ID, StartDate: start, EndDate: end, InitialCapital: 100000,
		}
		if v.cfg != nil {
			req.ChanlunAssist = v.cfg
		}
		result, err := svc.CreateBacktest(req)
		if err != nil {
			t.Fatalf("%s 回测失败：%v", v.name, err)
		}
		payload, err := svc.GetBacktestPayload(result.RunID)
		if err != nil {
			t.Fatalf("%s 读结果失败：%v", v.name, err)
		}
		m := result.Metrics
		elapsed := time.Since(began)
		t.Logf("【%s】总收益 %.2f%% / 年化 %.2f%% / 回撤 %.2f%% / 夏普 %.2f / 卡玛 %.2f / 成交 %d 笔 / 胜率 %.2f%% / 日均仓位 %.2f%% / 耗时 %.0fs",
			v.name, m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio, m.CalmarRatio,
			m.TotalTrades, m.WinRate, m.AvgPositionRatio, elapsed.Seconds())

		summary := payload.Run.Metrics
		if summary == nil {
			summary = map[string]any{}
		}
		if assist, ok := summary["chanlunAssist"].(map[string]any); ok {
			if stats, ok := assist["stats"].(map[string]any); ok {
				t.Logf("  缠论：买2 %v / 买3 %v / 加层 %v；卖1 %v / 卖2 %v / 卖3 %v / 减层 %v；持仓日见到卖点 %v 次（其中基计划已退出 %v 次）",
					stats["buy2Boosts"], stats["buy3Boosts"], stats["buyLayersAdded"],
					stats["sell1Reduces"], stats["sell2Reduces"], stats["sell3Reduces"], stats["sellLayersSold"],
					stats["sellSignalsSeen"], stats["sellSignalsOnExit"])
			}
		}

		entry := runResult{
			Name: v.name, Config: v.cfg, Elapsed: elapsed.Seconds(),
			Metrics: m, Summary: summary, Daily: payload.Daily, Trades: payload.Trades,
		}
		// 原始结果可能很大：成交超过 500 笔时只留前 500 笔并标记。
		if len(entry.Trades) > 500 {
			entry.Trades = entry.Trades[:500]
			entry.Truncated = true
		}
		out.Runs = append(out.Runs, entry)
	}

	if len(out.Runs) >= 2 {
		baseRun := out.Runs[0]
		for _, run := range out.Runs[1:] {
			t.Logf("对比 %s vs %s：收益 %+.2f 个百分点，回撤 %+.2f 个百分点，成交 %+d 笔",
				run.Name, baseRun.Name,
				run.Metrics.TotalReturn-baseRun.Metrics.TotalReturn,
				run.Metrics.MaxDrawdown-baseRun.Metrics.MaxDrawdown,
				len(run.Trades)-len(baseRun.Trades))
		}
	}

	outPath := os.Getenv("FREESTOCK_CA_OUT")
	if outPath == "" {
		outPath = filepath.Join(t.TempDir(), "chanlun_assist_backtest.json")
	}
	if !filepath.IsAbs(outPath) {
		abs, err := filepath.Abs(outPath)
		if err == nil {
			outPath = abs
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("创建输出目录失败：%v", err)
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("序列化结果失败：%v", err)
	}
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	t.Logf("原始结果已写入：%s", outPath)
}
