package service

import (
	"os"
	"testing"

	"freestock/internal/config"
	"freestock/internal/score"
)

// **给单日全市场评分做 CPU profile**，用来定位 7.66s/天到底花在哪。
//
// 之前我是"量到某个函数慢 → 直接归因"（比如以为瓶颈是组装 Quote），
// 结果去掉之后时间一分没少。这次改成先拿 profile，再看热点。
//
// 用法：
//
//	$env:FREESTOCK_PROFILE='1'
//	go test ./internal/service/ -run TestProfileScoreOneDay -cpuprofile cpu.out -v
//	go tool pprof -top -nodecount=25 cpu.out
func TestProfileScoreOneDay(t *testing.T) {
	if os.Getenv("FREESTOCK_PROFILE") != "1" {
		t.Skip("设 FREESTOCK_PROFILE=1 才跑（配合 -cpuprofile）")
	}
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	if _, err := svc.stock.SetDataSource("cnlake", root); err != nil {
		t.Skipf("切到数据湖失败：%v", err)
	}

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil {
		t.Skipf("拿不到股票列表：%v", err)
	}
	codes := make([]string, 0, 8192)
	for _, item := range universe.Stocks {
		if item.HasQuote {
			codes = append(codes, item.Code)
		}
	}
	t.Logf("全市场 %d 只，开始单日评分（profile 中）", len(codes))

	scoring := NewScoringService(svc.stock)
	result, err := scoring.ScoreStocks(codes, int(universe.TradeDate), 0, score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("评分失败：%v", err)
	}
	t.Logf("评分完成：%d 行（profile 已写盘，用 go tool pprof 看热点）", len(result.Rows))
}
