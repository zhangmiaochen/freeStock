package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"freestock/internal/chanlun"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
)

// 诊断工具（默认跳过）：看指定股票在指定区间内，缠论买卖点到底出现多少天。
//
// 用途：当回测里「加层/减仓一次都没触发」时，先用它区分
//
//   - 数据/实现问题（一只票一年一个信号都没有）；
//
//   - 策略与信号的时序错位（信号有，但都不在持仓/买入日附近）。
//
// ```text
//
//	$env:FREESTOCK_CA_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_CA_CODES="600519,000001,300750"
//	$env:FREESTOCK_CA_START="20251013"; $env:FREESTOCK_CA_END="20261019"
//	go test ./internal/service -run TestChanlunAssistCodeSignalFrequency -v
//
// ```
func TestChanlunAssistCodeSignalFrequency(t *testing.T) {
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	rawCodes := os.Getenv("FREESTOCK_CA_CODES")
	if lakeRoot == "" || rawCodes == "" {
		t.Skip("未设置 FREESTOCK_CA_LAKE / FREESTOCK_CA_CODES")
	}
	start := envInt(t, "FREESTOCK_CA_START", 20250101)
	end := envInt(t, "FREESTOCK_CA_END", 20261019)
	codes := make([]string, 0, 16)
	for _, item := range strings.Split(rawCodes, ",") {
		if code := strings.TrimSpace(item); code != "" {
			codes = append(codes, code)
		}
	}

	cfgDir, err := os.MkdirTemp("", "freestock-ca-freq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfgDir) })
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatal(err)
	}
	provider, _ := stock.snapshot()

	barsByCode := map[string][]chanlun.Bar{}
	for _, code := range codes {
		quotes, err := provider.DailyHistory(context.Background(), code, start-550, end)
		if err != nil || len(quotes) == 0 {
			t.Logf("%s 取数失败：%v", code, err)
			continue
		}
		barsByCode[code] = chanlunBarsFromQuotes(quotes)
	}
	analyzer := chanlunassist.NewAnalyzer(barsByCode, chanlun.DefaultConfig(), chanlunassist.DefaultConfig())

	for _, code := range codes {
		bars := barsByCode[code]
		if len(bars) == 0 {
			continue
		}
		counts := map[string]int{}
		firstDates := map[string]int{}
		for _, bar := range bars {
			if bar.Date < start || bar.Date > end {
				continue
			}
			snapshot := analyzer.Signals(code, bar.Date)
			for name, sig := range map[string]*chanlunassist.Signal{
				"buy2": snapshot.Buy2, "buy3": snapshot.Buy3,
				"sell1": snapshot.Sell1, "sell2": snapshot.Sell2, "sell3": snapshot.Sell3,
			} {
				if sig == nil {
					continue
				}
				counts[name]++
				if _, ok := firstDates[name]; !ok {
					firstDates[name] = sig.ConfirmDate
				}
			}
		}
		t.Logf("%s: %v（首次确认日 %v）", code, counts, firstDates)
	}
}
