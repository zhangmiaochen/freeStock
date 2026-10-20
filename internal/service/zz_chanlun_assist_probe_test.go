package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/chanlun"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
)

// 诊断用：真实数据湖上缠论信号的出现频率与耗时（默认跳过）。
//
//	$env:FREESTOCK_CA_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	go test ./internal/service -run TestChanlunAssistSignalProbe -v -timeout 30m
func TestChanlunAssistSignalProbe(t *testing.T) {
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	if lakeRoot == "" {
		t.Skip("未设置 FREESTOCK_CA_LAKE")
	}
	stock := NewStockService(config.NewStoreAt(filepath.Join(t.TempDir(), "config.json")))
	stock.applySource(config.KindCnLake, lakeRoot)
	provider, _ := stock.snapshot()
	if provider == nil {
		t.Fatal("数据源未就绪")
	}

	codes := []string{
		"600519", "000001", "300750", "601318", "002594", "600036",
		"000858", "601899", "300059", "600276", "002415", "601012",
	}
	began := time.Now()
	barsByCode := map[string][]chanlun.Bar{}
	for _, code := range codes {
		loadBegan := time.Now()
		quotes, err := provider.DailyHistory(context.Background(), code, 20240101, 20261019)
		if err != nil {
			t.Logf("%s 取数失败：%v", code, err)
			continue
		}
		bars := chanlunBarsFromQuotes(quotes)
		barsByCode[code] = bars
		t.Logf("%s: %d 根日线，取数 %s", code, len(bars), time.Since(loadBegan).Round(time.Millisecond))
	}
	t.Logf("取数合计 %s，%d 只", time.Since(began).Round(time.Millisecond), len(barsByCode))

	cfg := chanlun.DefaultConfig()
	assist := chanlunassist.DefaultConfig()
	analyzer := chanlunassist.NewAnalyzer(barsByCode, cfg, assist)

	counts := map[chanlun.SignalType]int{}
	analyzed := 0
	analyzeBegan := time.Now()
	for code, bars := range barsByCode {
		// 只扫最近 250 个交易日
		start := 0
		if len(bars) > 250 {
			start = len(bars) - 250
		}
		seen := map[string]bool{}
		for i := start; i < len(bars); i++ {
			snapshot := analyzer.Signals(code, bars[i].Date)
			analyzed++
			for _, sig := range []*chanlunassist.Signal{
				snapshot.Buy2, snapshot.Buy3, snapshot.Sell1, snapshot.Sell2, snapshot.Sell3,
			} {
				if sig == nil {
					continue
				}
				// 同一确认日只计一次
				dedupe := code + "|" + string(sig.Type) + "|" + itoa64(int64(sig.ConfirmDate))
				if seen[dedupe] {
					continue
				}
				seen[dedupe] = true
				counts[sig.Type]++
			}
		}
	}
	t.Logf("逐日分析 %d 次，耗时 %s（单次均值 %s）",
		analyzed, time.Since(analyzeBegan).Round(time.Millisecond),
		(time.Since(analyzeBegan) / time.Duration(analyzed)).Round(time.Microsecond))
	for _, sigType := range []chanlun.SignalType{
		chanlun.SignalBuy2, chanlun.SignalBuy3,
		chanlun.SignalSell1, chanlun.SignalSell2, chanlun.SignalSell3,
	} {
		t.Logf("%s：%d 个（12 只 × 约 250 日）", chanlun.SignalTypeCN(sigType), counts[sigType])
	}
}
