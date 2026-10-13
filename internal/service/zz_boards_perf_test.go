package service

import (
	"context"
	"os"
	"testing"
	"time"

	"freestock/internal/config"
)

// **板块行情性能探针**：把一次 `GetBoardQuotes` 拆成几个阶段分别计时，
// 用来回答"初始加载慢在哪一段"（而不是靠猜）。
//
//	FREESTOCK_BOARDS_PERF=1 go test ./internal/service/ -run TestBoardsPerfProbe -v -count=1
//
// 会分别测 free-stockdb 与数据湖两条路（哪条不可用就跳过那条）。
func TestBoardsPerfProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_BOARDS_PERF") != "1" {
		t.Skip("板块行情性能探针：设 FREESTOCK_BOARDS_PERF=1 才跑")
	}
	periods := []int{5, 10, 15, 30, 60}

	run := func(label string, svc *StockService) {
		if svc == nil {
			return
		}
		timeit := func(name string, fn func()) time.Duration {
			began := time.Now()
			fn()
			elapsed := time.Since(began)
			t.Logf("  %-34s %8s", name, elapsed.Round(time.Millisecond))
			return elapsed
		}

		t.Logf("== %s ==", label)

		// ① 板块名单
		var boards int
		timeit("loadBoards（板块成分名单）", func() {
			list, err := svc.loadBoards()
			if err != nil {
				t.Fatalf("loadBoards: %v", err)
			}
			boards = len(list)
		})

		// ② 全量股票列表（当前实现的第一步）
		var universe *StockUniverse
		timeit("GetStockUniverse（全量行情快照）", func() {
			got, err := svc.GetStockUniverse(true)
			if err != nil {
				t.Fatalf("GetStockUniverse: %v", err)
			}
			universe = got
		})

		// ③ 交易日历
		provider, _ := svc.snapshot()
		ctx := context.Background()
		tradeDate := int(universe.TradeDate)
		needFrom := shiftDate(tradeDate, -(maxBoardPeriod*boardPeriodWindowFactor + boardPeriodWindowExtra))
		timeit("TradingDates（交易日历）", func() {
			if _, err := provider.TradingDates(ctx, needFrom, tradeDate); err != nil {
				t.Fatalf("TradingDates: %v", err)
			}
		})

		// ④ 基准日收盘价（默认周期集合）
		list, _ := svc.loadBoards()
		codes := boardSymbols(list)
		timeit("loadMarketIndices（指数，仅市场温度用）", func() {
			_, _, _, _ = svc.loadMarketIndices()
		})

		calendar, err := provider.TradingDates(ctx, needFrom, tradeDate)
		if err != nil {
			t.Fatalf("TradingDates: %v", err)
		}
		trimmed := trimDates(calendar, needFrom, tradeDate)
		_, baseDates := boardPeriodWindows(trimmed, periods)
		dates := make([]int, 0, len(baseDates))
		for _, days := range periods {
			if date := baseDates[days]; date > 0 {
				dates = append(dates, date)
			}
		}
		timeit("ClosesBefore（基准日收盘价，5 个周期）", func() {
			if _, err := provider.ClosesBefore(ctx, codes, dates); err != nil {
				t.Fatalf("ClosesBefore: %v", err)
			}
		})

		// ⑤ 端到端（缓存已热，看的是"再点一次"的成本）
		timeit("GetBoardQuotes（端到端，缓存已热）", func() {
			if _, err := svc.GetBoardQuotes("申万一级", periods, 0); err != nil {
				t.Fatalf("GetBoardQuotes: %v", err)
			}
		})

		// ⑥ 冷启动端到端：清缓存后重来一次
		svc.ClearCache()
		timeit("GetBoardQuotes（端到端，冷启动）", func() {
			if _, err := svc.GetBoardQuotes("申万一级", periods, 0); err != nil {
				t.Fatalf("GetBoardQuotes: %v", err)
			}
		})
		t.Logf("  板块 %d 个，成分并集 %d 只，标的 %d 只", boards, len(codes), len(universe.Stocks))
	}

	t.Run("stockdb", func(t *testing.T) {
		svc := newLocalService()
		requireLocalServer(t, svc)
		run("free-stockdb "+stockdbURL(), svc)
	})

	t.Run("cnlake", func(t *testing.T) {
		svc := newLakeService(t)
		if svc == nil {
			t.Skip("本机没有可用的数据湖")
		}
		run("数据湖 "+svc.lakeHandle().Root(), svc)
	})
}

func stockdbURL() string {
	return config.Presets[0].URL
}
