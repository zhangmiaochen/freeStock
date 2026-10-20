package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
)

// 缠论辅助 · 明细归因（只读、只测）。
//
// 网格实验找出「哪些用法平均更好」之后，这里把代表方案在关键窗口上的
// **逐日净值 + 逐笔成交 + 缠论事件**落下来，用来回答：
//
//   - 收益差是来自被加层的那几只票，还是来自其它票的仓位变化？
//
//   - 回撤差发生在哪些日期？
//
//   - 同一个方案在不同股票池上是否由同一类标的驱动？
//
// ```text
//
//	$env:FREESTOCK_CA_DB="...\bin\portfolio.db"
//	$env:FREESTOCK_CA_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_CA_PORTFOLIOS="pf_...0002,pf_...0001"
//	$env:FREESTOCK_CA_DETAIL_WINDOWS="W1,W3,W4"
//	$env:FREESTOCK_CA_DETAIL_CASES="base,B1_hold_b3_f10,E4_b3_max10,E1_b3_plus1"
//	$env:FREESTOCK_CA_DETAIL_OUT=".gotmp\chanlun-detail.json"
//	go test ./internal/service -run TestChanlunAssistGridDetail -v -timeout 180m
//
// ```
func TestChanlunAssistGridDetail(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_CA_DB")
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	rawPortfolios := os.Getenv("FREESTOCK_CA_PORTFOLIOS")
	if rawPortfolios == "" {
		rawPortfolios = os.Getenv("FREESTOCK_CA_PORTFOLIO")
	}
	if srcDB == "" || lakeRoot == "" || rawPortfolios == "" {
		t.Skip("未设置 FREESTOCK_CA_DB / FREESTOCK_CA_LAKE / FREESTOCK_CA_PORTFOLIOS")
	}

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
	cfgDir, err := os.MkdirTemp("", "freestock-ca-detail-")
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

	allWindows := []struct {
		name       string
		start, end int
	}{
		{"W1_2022-01-12_2023-10-12", 20220112, 20231012},
		{"W2_2023-10-13_2024-10-11", 20231013, 20241011},
		{"W3_2024-10-14_2025-10-13", 20241014, 20251013},
		{"W4_2025-10-14_2026-10-19", 20251014, 20261019},
	}
	windowFilter := stringSet(os.Getenv("FREESTOCK_CA_DETAIL_WINDOWS"))
	if len(windowFilter) == 0 {
		windowFilter = map[string]bool{"W1": true, "W3": true, "W4": true}
	}

	allCases := map[string]gridCase{}
	for _, c := range gridCases() {
		allCases[c.name] = c
	}
	caseNames := strings.Split(os.Getenv("FREESTOCK_CA_DETAIL_CASES"), ",")
	if len(caseNames) == 1 && strings.TrimSpace(caseNames[0]) == "" {
		caseNames = []string{"base", "B1_hold_b3_f10", "E4_b3_max10", "E1_b3_plus1"}
	}
	var cases []gridCase
	for _, name := range caseNames {
		name = strings.TrimSpace(name)
		if c, ok := allCases[name]; ok {
			cases = append(cases, c)
		} else {
			t.Fatalf("未知方案 %q", name)
		}
	}

	type detailRun struct {
		Portfolio string              `json:"portfolio"`
		Window    string              `json:"window"`
		Case      string              `json:"case"`
		Metrics   backtest.Metrics    `json:"metrics"`
		Stats     chanlunassist.Stats `json:"stats"`
		Daily     []backtest.Snapshot `json:"daily"`
		Trades    []backtest.Trade    `json:"trades"`
	}
	runs := make([]detailRun, 0, len(cases)*len(allWindows)*2)
	for _, rawID := range strings.Split(rawPortfolios, ",") {
		portfolioID := strings.TrimSpace(rawID)
		if portfolioID == "" {
			continue
		}
		item, err := store.Get(portfolioID)
		if err != nil {
			t.Fatalf("读组合 %s 失败：%v", portfolioID, err)
		}
		for _, win := range allWindows {
			short := strings.SplitN(win.name, "_", 2)[0]
			if len(windowFilter) > 0 && !windowFilter[short] {
				continue
			}
			stock.applySource(config.KindCnLake, lakeRoot)
			began := time.Now()
			w := buildGridWindow(t, svc, store, item, win.name, win.start, win.end)
			t.Logf("%s / %s：准备 %s", item.ID, win.name, time.Since(began).Round(time.Second))
			for _, c := range cases {
				result, stats, err := runEngineCase(w, c)
				if err != nil {
					t.Fatalf("%s / %s / %s 失败：%v", item.ID, win.name, c.name, err)
				}
				runs = append(runs, detailRun{
					Portfolio: item.ID, Window: win.name, Case: c.name,
					Metrics: backtest.CalculateMetrics(result.Snapshots, result.Trades, w.cfg.InitialCapital),
					Stats:   stats, Daily: result.Snapshots, Trades: result.Trades,
				})
			}
			w.days, w.bars, w.analyzer, w.buildPlan = nil, nil, nil, nil
			runtime.GC()
		}
	}

	// 逐窗口打印对比，便于立即看出哪一段赚/亏。
	type key struct{ portfolio, window string }
	by := map[key]map[string]detailRun{}
	for _, r := range runs {
		k := key{r.Portfolio, r.Window}
		if by[k] == nil {
			by[k] = map[string]detailRun{}
		}
		by[k][r.Case] = r
	}
	for k, group := range by {
		base, ok := group["base"]
		if !ok {
			continue
		}
		for _, c := range cases {
			if c.name == "base" {
				continue
			}
			r, ok := group[c.name]
			if !ok {
				continue
			}
			t.Logf("%s / %s / %-18s 收益 %8.2f%%（%+7.2f）回撤 %6.2f%%（%+5.2f）成交 %3d（%+3d）加层 %2d 减层 %2d",
				k.portfolio, k.window, c.name,
				r.Metrics.TotalReturn, r.Metrics.TotalReturn-base.Metrics.TotalReturn,
				r.Metrics.MaxDrawdown, r.Metrics.MaxDrawdown-base.Metrics.MaxDrawdown,
				r.Metrics.TotalTrades, r.Metrics.TotalTrades-base.Metrics.TotalTrades,
				r.Stats.BuyLayersAdded, r.Stats.SellLayersSold)
		}
	}

	out := os.Getenv("FREESTOCK_CA_DETAIL_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "chanlun-detail.json")
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	raw, err := json.MarshalIndent(map[string]any{
		"generatedAt": fmt.Sprintf("%s", time.Now().Format(time.RFC3339)),
		"runs":        runs,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatalf("写明细失败：%v", err)
	}
	t.Logf("明细已写入：%s", out)
}

func stringSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(item); v != "" {
			out[v] = true
		}
	}
	return out
}
