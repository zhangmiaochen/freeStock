package cnlake

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// dayNumber 把年月日折算成 parquet DATE 天数（与生产代码同一口径）。
func dayNumber(y, m, d int) int32 {
	return int32(time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// writeBars 造一个"按年分区"的 daily_bars（这是 cne 的退化布局：
// trade_date=2026/part-merged.parquet），日期集合由 dates 决定。
func writeBars(t *testing.T, root string, dates [][3]int) string {
	t.Helper()
	path := filepath.Join(root, "curated", "daily_bars", "trade_date=2026", "part-merged.parquet")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	rows := make([]BarRow, 0, len(dates))
	for _, d := range dates {
		rows = append(rows, BarRow{
			Symbol:    "600519.SH",
			TradeDate: dayNumber(d[0], d[1], d[2]),
			Open:      100, High: 101, Low: 99, Close: 100.5, Volume: 1000, Amount: 100500,
		})
	}
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
	return path
}

// 真实数据湖是**混合布局**：历史按天分区，当期按年月分区。
// 最新交易日必须取两者中更晚的那个（当期分区里可能有比最后一天分区更新的数据）。
func TestLatestTradeDateSeesCoarsePartitionNewerThanDayPartitions(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "curated", "daily_bars")

	// 按天分区：到 11-16
	writeDayBars(t, filepath.Join(base, "trade_date=2026-11-16"), 2026, 11, 16)
	writeDayBars(t, filepath.Join(base, "trade_date=2026-11-13"), 2026, 11, 13)
	// 当期按月分区：里面有 11-18（比最后一个天分区更新）
	writeMonthBars(t, filepath.Join(base, "trade_date=2026-11"), [][3]int{
		{2026, 11, 13}, {2026, 11, 16}, {2026, 11, 18},
	})

	lake := Open(root)
	date, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatal(err)
	}
	if date != 20261118 {
		t.Fatalf("最新交易日=%d，应为当期月分区里的 20261118", date)
	}
}

func writeDayBars(t *testing.T, dir string, y, m, d int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := []BarRow{{
		Symbol: "600519.SH", TradeDate: dayNumber(y, m, d),
		Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1000, Amount: 100500,
	}}
	if err := parquet.WriteFile(filepath.Join(dir, "part-merged.parquet"), rows); err != nil {
		t.Fatal(err)
	}
}

func writeMonthBars(t *testing.T, dir string, dates [][3]int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := make([]BarRow, 0, len(dates))
	for _, d := range dates {
		rows = append(rows, BarRow{
			Symbol: "600519.SH", TradeDate: dayNumber(d[0], d[1], d[2]),
			Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1000, Amount: 100500,
		})
	}
	if err := parquet.WriteFile(filepath.Join(dir, "part-merged.parquet"), rows); err != nil {
		t.Fatal(err)
	}
}

// cne 日更常常**原地重写**当月/当年分区里的 parquet：文件内容变了，
// 但目录 mtime 不变。只按目录 mtime 失效的缓存会一直答旧日期 ——
// 用户看到的现象就是"数据更到 1118，评分日还是 1113"。
func TestLatestTradeDateSeesInPlaceParquetRewrite(t *testing.T) {
	root := t.TempDir()
	path := writeBars(t, root, [][3]int{{2026, 11, 12}, {2026, 11, 13}})

	lake := Open(root)
	first, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatal(err)
	}
	if first != 20261113 {
		t.Fatalf("初始最新交易日=%d，应为 20261113", first)
	}

	// 原地重写同一个文件：新增 11-18（目录 mtime 不变）。
	writeBars(t, root, [][3]int{{2026, 11, 12}, {2026, 11, 13}, {2026, 11, 18}})

	// 让指纹核对越过 TTL（生产是 30s，测试直接改时间戳）。
	lake.barDatesMu.Lock()
	// LatestTradeDate 自己还有一道"结果缓存"（30s 内零 IO），测试要模拟
	// "过了 TTL 才重新核对"，否则会直接命中缓存看不到重写。
	lake.recentBarAt = time.Now().Add(-time.Hour)
	lake.barDatesMu.Unlock()

	second, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatal(err)
	}
	if second != 20261118 {
		t.Fatalf("原地重写后最新交易日=%d，应为 20261118（缓存没被打破）", second)
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Size() == 0 {
		t.Fatal("测试文件写坏了")
	}
}
