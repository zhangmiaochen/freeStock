package cnlake

import (
	"testing"
	"time"
)

// 混合布局：当期粗分区比最后一个天分区更新，TradeDateAt 必须跟上，
// 且重复调用要快（不能每次重扫全量）。
func TestTradeDateAtSeesCoarsePartition(t *testing.T) {
	root := t.TempDir()
	base := root + "/curated/daily_bars"
	writeDayBars(t, base+"/trade_date=2026-11-16", 2026, 11, 16)
	writeDayBars(t, base+"/trade_date=2026-11-13", 2026, 11, 13)
	writeMonthBars(t, base+"/trade_date=2026-11", [][3]int{{2026, 11, 13}, {2026, 11, 16}, {2026, 11, 18}})

	lake := Open(root)
	got, err := lake.TradeDateAt(0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 20261118 {
		t.Fatalf("TradeDateAt(0)=%d，应为当期分区里的 20261118", got)
	}
	// 指定一个更早的日期，不能被"最新"盖过去
	got, err = lake.TradeDateAt(20261114)
	if err != nil {
		t.Fatal(err)
	}
	if got != 20261113 {
		t.Fatalf("TradeDateAt(20261114)=%d，应为 20261113", got)
	}
	began := time.Now()
	for i := 0; i < 200; i++ {
		if _, err := lake.TradeDateAt(0); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("200 次 TradeDateAt 用了 %s，热路径仍在做重活", elapsed)
	}
}
