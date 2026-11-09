package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"freestock/internal/config"
	"freestock/internal/qgrowth"
	"freestock/internal/stockdb"
)

// 调试：打印指定日期的质量成长候选前 8（与 Python 参考对账用）。
//
//	$env:FREESTOCK_QG_EXP="1"; $env:FREESTOCK_QG_DATE="20230630"
//	go test ./internal/service -run TestQualityGrowthDebugDate -v
func TestQualityGrowthDebugDate(t *testing.T) {
	if os.Getenv("FREESTOCK_QG_EXP") == "" {
		t.Skip("设 FREESTOCK_QG_EXP=1 才跑")
	}
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	date, _ := strconv.Atoi(os.Getenv("FREESTOCK_QG_DATE"))
	if lakeRoot == "" || date == 0 {
		t.Fatal("需要 FREESTOCK_QG_LAKE / FREESTOCK_QG_DATE")
	}
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, filepath.Join("..", "..", ".gotmp", "gate_exp", "portfolio.db"))
	t.Cleanup(svc.Close)

	days, _, _, err := svc.buildQGrowthDays(date, date, qgrowth.DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) == 0 {
		t.Fatal("没有该日期的候选")
	}
	// 全区间 checksum（对账用）：FREESTOCK_QG_START/END
	if a := os.Getenv("FREESTOCK_QG_START"); a != "" {
		if st, e1 := strconv.Atoi(a); e1 == nil {
			if en, e2 := strconv.Atoi(os.Getenv("FREESTOCK_QG_END")); e2 == nil {
				all, _, _, e3 := svc.buildQGrowthDays(st, en, qgrowth.DefaultConfig(), nil)
				if e3 == nil {
					n, sum := 0, 0.0
					for _, day := range all {
						for _, r := range day.Rows {
							n++
							sum += r.TotalScore
						}
					}
					t.Logf("QG_SUM days=%d rows=%d sum=%.6f", len(all), n, sum)
				}
			}
		}
	}
	t.Logf("QG_DATE date=%d candidates=%d", date, len(days[0].Rows))
	for i, row := range days[0].Rows {
		if i >= 8 {
			break
		}
		t.Logf("QG_CAND %s %s %.2f", row.Code, row.Name, row.TotalScore)
	}
	// 打印指定股票在数据源的收盘与 MA200（对账用）
	if code := os.Getenv("FREESTOCK_QG_CODE"); code != "" {
		provider, _ := stock.snapshot()
		quotes, err := provider.DailyHistory(context.Background(), code, shiftBack(date, 400), date)
		if err == nil && len(quotes) > 0 {
			closes := make([]float64, 0, len(quotes))
			for _, q := range quotes {
				if q != nil && q.Close > 0 {
					closes = append(closes, q.Close)
				}
			}
			// 导出整段序列，便于和 Python 缓存逐日对比
			if f, ferr := os.Create(filepath.Join("..", "..", ".gotmp", "qg_provider_"+code+".csv")); ferr == nil {
				f.WriteString("date,close\n")
				for _, q := range quotes {
					if q != nil && q.Close > 0 {
						f.WriteString(strconv.FormatInt(q.Date, 10) + "," + strconv.FormatFloat(q.Close, 'f', 4, 64) + "\n")
					}
				}
				f.Close()
			}
			if len(closes) >= 200 {
				sum := 0.0
				for _, v := range closes[len(closes)-200:] {
					sum += v
				}
				t.Logf("QG_PROVIDER %s bars=%d last=%.2f ma200=%.2f", code, len(closes),
					closes[len(closes)-1], sum/200)
			} else {
				t.Logf("QG_PROVIDER %s bars=%d (不足200)", code, len(closes))
			}
		}
	}
}
