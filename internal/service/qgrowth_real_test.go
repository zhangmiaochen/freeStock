package service

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"freestock/internal/config"
	"freestock/internal/stockdb"
)

// 质量成长策略的真实数据回测（需要显式设环境变量才跑）。
//
//	$env:FREESTOCK_QG_EXP="1"
//	$env:FREESTOCK_QG_DB="D:\...\portfolio.db"        # 副本库
//	$env:FREESTOCK_QG_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_QG_PORTFOLIO="pf_..."
//	$env:FREESTOCK_QG_START="20240101"; $env:FREESTOCK_QG_END="20251231"
//	go test ./internal/service -run TestQualityGrowthBacktestReal -v -timeout 60m
func TestQualityGrowthBacktestReal(t *testing.T) {
	if os.Getenv("FREESTOCK_QG_EXP") == "" {
		t.Skip("设 FREESTOCK_QG_EXP=1 才跑（真实数据、只读副本库）")
	}
	dbPath := os.Getenv("FREESTOCK_QG_DB")
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	portfolioID := os.Getenv("FREESTOCK_QG_PORTFOLIO")
	if dbPath == "" || lakeRoot == "" || portfolioID == "" {
		t.Fatal("需要 FREESTOCK_QG_DB / FREESTOCK_QG_LAKE / FREESTOCK_QG_PORTFOLIO")
	}
	start := envInt(t, "FREESTOCK_QG_START", 20240101)
	end := envInt(t, "FREESTOCK_QG_END", 20251231)

	// 固定缓存目录（不用 t.TempDir：数据湖缓存文件在测试结束时仍被后台写线程占用）
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
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
	if _, err := store.DB().Exec(
		"UPDATE portfolio SET strategy_id=? WHERE portfolio_id=?",
		"quality_growth_pullback", portfolioID); err != nil {
		t.Fatalf("切换策略失败：%v", err)
	}

	res, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID: portfolioID, StartDate: start, EndDate: end, InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	m := res.Metrics
	t.Logf("质量成长回测 %d~%d：总收益 %.2f%% / 年化 %.2f%% / 回撤 %.2f%% / 夏普 %.2f / 成交 %d / 交易日 %d",
		start, end, m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio, m.TotalTrades, m.TradingDays)
	t.Logf("QG_RESULT start=%d end=%d total=%.2f cagr=%.2f dd=%.2f sharpe=%.2f trades=%d days=%d",
		start, end, m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio, m.TotalTrades, m.TradingDays)

	// 把逐日净值导出来做逐年/逐月对账
	rows, err := store.DB().Query(
		`SELECT trade_date, total_value FROM portfolio_backtest_daily WHERE run_id=? ORDER BY trade_date`, res.RunID)
	if err == nil {
		type pt struct {
			Date  string
			Value float64
		}
		var series []pt
		for rows.Next() {
			var d string
			var v float64
			if err := rows.Scan(&d, &v); err == nil {
				series = append(series, pt{d, v})
			}
		}
		rows.Close()
		out, ferr := os.Create(filepath.Join("..", "..", ".gotmp", "qg_go_equity.csv"))
		if ferr == nil {
			defer out.Close()
			out.WriteString("date,value\n")
			for _, p := range series {
				out.WriteString(p.Date + "," + strconv.FormatFloat(p.Value, 'f', 2, 64) + "\n")
			}
		}
		// 逐年收益
		byYear := map[string][2]float64{} // year -> [first, last]
		for _, p := range series {
			y := p.Date[:4]
			if cur, ok := byYear[y]; ok {
				cur[1] = p.Value
				byYear[y] = cur
			} else {
				byYear[y] = [2]float64{p.Value, p.Value}
			}
		}
		prev := 0.0
		for y := start / 10000; y <= end/10000; y++ {
			key := strconv.Itoa(y)
			v, ok := byYear[key]
			if !ok {
				continue
			}
			if prev > 0 {
				t.Logf("QG_YEAR year=%s ret=%.2f", key, (v[1]/prev-1)*100)
			}
			prev = v[1]
		}
	}
	if m.TradingDays == 0 || m.TotalTrades == 0 {
		t.Fatalf("回测没有产生交易：days=%d trades=%d", m.TradingDays, m.TotalTrades)
	}
}
