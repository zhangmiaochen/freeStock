package cnlake

import (
	"os"
	"testing"
)

// 数据湖 daily_bars 的**覆盖率**：最近几个交易日各有多少只标的、多少行。
//
// 为什么必须量：数据湖按交易日批量读极快（320 天全市场 173 万行约 2 秒，
// 见 byday_read_test.go），而 free-stockdb 走 HTTP 是**每只一次请求**
//（实测约 8ms/次、总吞吐 215 次/秒 → 7600 只约 35 秒）。
// 看起来"把取数换成本地湖"能快十几倍 —— 但**前提是覆盖面一致**：
// 湖里少一批标的，评分表就会少一批行，广度闸门与候选池都会跟着变。
//
// 设 FREESTOCK_LAKE_COVERAGE=1 开启。
func TestLakeDailyBarsCoverage(t *testing.T) {
	if os.Getenv("FREESTOCK_LAKE_COVERAGE") != "1" {
		t.Skip("要读本机数据湖：设 FREESTOCK_LAKE_COVERAGE=1 才跑")
	}
	lake := openLocalLake(t)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	dates, err := lake.BarPartitionDates()
	if err != nil || len(dates) == 0 {
		t.Skipf("列分区失败：%v", err)
	}
	t.Logf("分区 %d 个：%d ~ %d", len(dates), dates[0], dates[len(dates)-1])

	// 最近 5 个分区各有多少行、多少只标的
	tail := dates
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	for _, date := range tail {
		rows, err := lake.DailyBarsInRange(nil, date, date)
		if err != nil {
			t.Fatalf("%d 读取失败：%v", date, err)
		}
		codes := map[string]bool{}
		for _, row := range rows {
			codes[BareSymbol(row.Symbol)] = true
		}
		t.Logf("  %d：%d 行、%d 只标的", date, len(rows), len(codes))
	}

	// 再抽查一整年：平均每个交易日多少只
	if len(dates) > 250 {
		window := dates[len(dates)-250:]
		all, err := lake.DailyBarsInRange(nil, window[0], window[len(window)-1])
		if err != nil {
			t.Fatalf("按天读失败：%v", err)
		}
		t.Logf("近 250 个交易日：%d 行、平均 %.0f 行/日", len(all), float64(len(all))/float64(len(window)))
	}
}
