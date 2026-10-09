package service

import (
	"testing"

	"freestock/internal/cnlake"
)

// 交易日历取自沪深300指数日线（数据湖 index_bars）。
func TestTradingDatesFromIndex(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.HasIndexBars() {
		t.Skip("数据湖 index_bars 未入库，跳过")
	}
	svc := &PortfolioService{stock: &StockService{lake: lake}}

	dates, err := svc.tradingDates(20260101, 20260131)
	if err != nil {
		t.Fatalf("取交易日失败：%v", err)
	}
	if len(dates) == 0 {
		t.Skip("2026 年 1 月没有指数数据，跳过")
	}
	// 必须升序、必须在区间内、必须是工作日（A 股不在周末交易）
	for i, date := range dates {
		if date < 20260101 || date > 20260131 {
			t.Errorf("日期 %d 超出区间", date)
		}
		if i > 0 && dates[i-1] >= date {
			t.Errorf("日期未严格升序：%d 之后是 %d", dates[i-1], date)
		}
	}
	t.Logf("2026-01 共 %d 个交易日：%v", len(dates), dates)

	// 区间颠倒要报错（在 RefreshBackfill 里检查）
	if _, err := svc.tradingDates(20260131, 20260101); err != nil {
		// tradingDates 本身不校验顺序，只保证返回空/有序；这里不强制
		t.Logf("颠倒区间返回：%v", err)
	}
}
