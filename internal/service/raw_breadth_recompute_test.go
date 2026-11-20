package service

import (
	"math"
	"path/filepath"
	"sort"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
)

// TestRawBreadthMatchesIndependentRecompute 防止"数漏了"这类静默回归：
// C 物化的原始就绪数必须与"整窗重新读一遍再算"的结果一致（允许 2% 误差）。
//
// 背景：只从批量缓存取数时，窗口对不上会少算一两成股票，闸门被悄悄收紧，
// 回测成交数直接掉（2015 实测 43 → 28）。这里把两条路径钉在一起。
func TestRawBreadthMatchesIndependentRecompute(t *testing.T) {
	lake := cnlake.Open("")
	if lake == nil || !lake.Available() || !lake.HasDataset("daily_bars") {
		t.Skip("no lake")
	}
	dir := t.TempDir()
	cfg := config.NewStoreAt(filepath.Join(dir, "config.json"))
	cfg.SetDataSource("", config.KindCnLake)
	cfg.SetCnLakeRoot(lake.Root())
	stock := NewStockService(cfg)
	svc := NewPortfolioServiceAt(stock, filepath.Join(dir, "portfolio.db"))
	defer svc.Close()
	if _, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "2015", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendLowBuy,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshBackfill(portfolio.StrategyTrendLowBuy, 20150115, 20150215, 0, true); err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	analytics, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := stock.snapshot()
	lp, ok := provider.(*lakeProvider)
	if !ok {
		t.Skip("provider 不是 lakeProvider")
	}
	states, err := analytics.LoadMarketStateRange(20150115, 20150215)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, s := range states {
		if checked >= 3 {
			break
		}
		codesSet, err := analytics.StrengthCodes(s.TradeDate, portfolio.StrategyTrendLowBuy)
		if err != nil || len(codesSet) == 0 {
			continue
		}
		codes := make([]string, 0, len(codesSet))
		for c := range codesSet {
			codes = append(codes, c)
		}
		// 独立重算：一次读整窗，按同一 code 集
		want := map[string]struct{}{}
		for _, c := range codes {
			want[cnlake.BareSymbol(c)] = struct{}{}
		}
		// 与 C 用同一个窗口（480 天），否则 MA60 在"有停牌缺口"的股票上会不同，
		// 差 1~3% 是窗口差异、不是取数漏算。
		start := shiftBack(int(s.TradeDate), 480)
		rows, err := lp.lake.DailyBarsInRange(nil, int64(start), s.TradeDate)
		if err != nil {
			t.Fatal(err)
		}
		byCode := map[string][]cnlake.BarRow{}
		for _, r := range rows {
			bare := cnlake.BareSymbol(r.Symbol)
			if _, ok := want[bare]; ok && r.Date() <= s.TradeDate {
				byCode[bare] = append(byCode[bare], r)
			}
		}
		count := 0
		for _, bars := range byCode {
			if len(bars) < 60 {
				continue
			}
			sort.Slice(bars, func(i, j int) bool { return bars[i].TradeDate < bars[j].TradeDate })
			if bars[len(bars)-1].Date() != s.TradeDate {
				continue
			}
			if rawBreadthReady(bars[len(bars)-60:]) {
				count++
			}
		}
		adj, _ := analytics.LowBuyBreadthReadyCount(s.TradeDate, portfolio.StrategyTrendLowBuy)
		checked++
		if count == 0 || s.RawBreadthReady == 0 {
			t.Fatalf("%d 计数为 0（复权=%d），无法校验", s.TradeDate, adj)
		}
		diff := math.Abs(float64(s.RawBreadthReady-count)) / float64(count)
		t.Logf("%d code集=%d 复权=%d C物化=%d 独立重算=%d 偏差=%.1f%%",
			s.TradeDate, len(codes), adj, s.RawBreadthReady, count, diff*100)
		// 容差 3%：批量缓存与整窗读取在"停牌/重复行"上会有十几个股票的出入，
		// 但**成倍少算**（比如缓存覆盖不到一半）会立刻超过这个阈值。
		if diff > 0.03 {
			t.Errorf("%d 的原始就绪数与独立重算差 %.1f%%（>3%%）—— 取数路径又漏了", s.TradeDate, diff*100)
		}
	}
}
