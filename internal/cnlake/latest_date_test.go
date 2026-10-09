package cnlake

import "testing"

// ⚠ 回归测试：湖的"最新交易日"必须是**实际有行情数据**的那一天。
//
// 早期实现取 `trading_calendar` 的最大日期 —— 而交易日历是**预先排好的**、
// 天然包含未来年份（本机实测日历分区到 2027，`daily_bars` 只到 2026-10-08）。
// 于是湖报告"最新交易日 = 2027-10-08"，一个**没有任何数据**的未来日期，
// 导致评分/回填/回测全部落在空区间上，整个「切到数据湖」功能等于不可用。
func TestLatestTradeDateComesFromBarsNotCalendar(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}

	dates, err := lake.BarPartitionDates()
	if err != nil {
		t.Fatalf("列分区失败：%v", err)
	}
	if len(dates) == 0 {
		t.Skip("daily_bars 没有分区")
	}
	t.Logf("daily_bars 分区 %d 个，%d ~ %d", len(dates), dates[0], dates[len(dates)-1])

	latest, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatalf("取最新交易日失败：%v", err)
	}
	if latest != dates[len(dates)-1] {
		t.Errorf("最新交易日应为最后一个 bar 分区 %d，实际 %d", dates[len(dates)-1], latest)
	}

	// 交易日历可能延伸到未来；最新交易日**不能**超过最后一个 bar 分区
	if lake.HasDataset("trading_calendar") {
		if calendarLatest := calendarMaxDate(t, lake); calendarLatest > latest {
			t.Logf("交易日历到 %d（含未来排期），但 bar 数据只到 %d —— 取后者才是对的",
				calendarLatest, latest)
		}
	}

	// TradeDateAt：不晚于某日的最近**有数据**交易日
	mid := dates[len(dates)/2]
	got, err := lake.TradeDateAt(mid)
	if err != nil {
		t.Fatalf("TradeDateAt 失败：%v", err)
	}
	if got != mid {
		t.Errorf("TradeDateAt(%d) 应为 %d（分区里就有这一天），实际 %d", mid, mid, got)
	}
	// 指定一个远超数据范围的未来日 → 应为最后一个有数据的交易日
	future, err := lake.TradeDateAt(29991231)
	if err != nil {
		t.Fatalf("TradeDateAt 失败：%v", err)
	}
	if future != latest {
		t.Errorf("给定未来日期时应回落到 %d，实际 %d", latest, future)
	}
}

// calendarMaxDate 读交易日历的最大日期（仅用于对照，不参与生产逻辑）。
func calendarMaxDate(t *testing.T, lake *Lake) int64 {
	t.Helper()
	type calendarRow struct {
		TradeDate int32 `parquet:"trade_date,date"`
		IsTrading bool  `parquet:"is_trading"`
	}
	rows, err := readTyped(lake.DatasetFiles("trading_calendar"), func(row *calendarRow) bool {
		return row.IsTrading
	})
	if err != nil {
		return 0
	}
	best := int64(0)
	for _, row := range rows {
		if date := daysToDate(row.TradeDate); date > best {
			best = date
		}
	}
	return best
}
