package service

import (
	"context"
	"testing"

	"freestock/internal/stockdb"
)

// 第 3 项的前提：**两个数据源的日线口径是否一致**。
//
// 评分只读 OHLCV（复权后的价格序列），所以如果把评分取数从 HTTP 换成
// 本地 parquet 直读，必须保证两者给出的价格**逐日相同** ——
// 否则 MA250、回撤、20 日收益这些全都会变，历史评分也就变了。
//
// 这个测试就是拿来回答"能不能换"的：
//   - 全部逐日相等 → 可以换（换完结果不变）
//   - 有差异       → **不换**，并在注释里写清楚差异性质
func TestProviderPriceParity(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要同时打 HTTP 与数据湖）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// HTTP 侧：直接用本机 free-stockdb
	httpProvider := NewStockdbProvider(stockdb.NewClient("http://127.0.0.1:7899"), "http://127.0.0.1:7899")
	// 数据湖侧
	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars，无法对比")
	}
	lakeProvider := NewLakeProvider(lake)

	codes, _, err := svc.scoringUniverse(mustStore(t, svc), 6)
	if err != nil || len(codes) == 0 {
		t.Skipf("拿不到代码：%v", err)
	}
	latest, err := httpProvider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	start := shiftDays(t, latest, -400) // 覆盖一段足够长的窗口，含分红除权

	same, diff, missing := 0, 0, 0
	for _, code := range codes {
		httpQuotes, err := httpProvider.DailyHistory(context.Background(), code, start, latest)
		if err != nil || len(httpQuotes) == 0 {
			missing++
			continue
		}
		lakeQuotes, err := lakeProvider.DailyHistory(context.Background(), code, start, latest)
		if err != nil || len(lakeQuotes) == 0 {
			missing++
			t.Logf("%s：数据湖没有日线（HTTP %d 条）", code, len(httpQuotes))
			continue
		}
		byDate := map[int64]*stockdb.Quote{}
		for _, q := range lakeQuotes {
			byDate[q.Date] = q
		}
		for _, hq := range httpQuotes {
			lq, ok := byDate[hq.Date]
			if !ok {
				missing++
				continue
			}
			if lq.Close == hq.Close && lq.Open == hq.Open && lq.High == hq.High && lq.Low == hq.Low {
				same++
			} else {
				if diff < 6 {
					t.Logf("  差异 %s %d：HTTP close=%.4f，湖 close=%.4f（开 %.4f/%.4f）",
						code, hq.Date, hq.Close, lq.Close, hq.Open, lq.Open)
				}
				diff++
			}
		}
		t.Logf("%s：HTTP %d 条，湖 %d 条", code, len(httpQuotes), len(lakeQuotes))
	}

	t.Logf("逐日比对结果：相同 %d，不同 %d，缺一侧 %d", same, diff, missing)
	if diff == 0 && same > 0 {
		t.Logf("→ 两个数据源的日线口径**一致**，评分取数可以安全换成本地直读")
	} else if diff > 0 {
		t.Logf("→ 口径**不一致**（%d 处差异）：**不能**把评分取数换成数据湖，否则结果会变", diff)
	}

}
