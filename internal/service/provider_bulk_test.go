package service

import (
	"context"
	"testing"

	"freestock/internal/cnlake"
)

// keepLastBar 是"只留最近几根"的核心，先把它的行为钉住。
func TestKeepLastBarKeepsAscendingTail(t *testing.T) {
	var list []cnlake.BarRow
	// 乱序喂入 10 天，只留 3 根
	dates := []int32{5, 1, 9, 3, 7, 2, 8, 4, 6, 0}
	for _, d := range dates {
		list = keepLastBar(list, cnlake.BarRow{TradeDate: d, Close: float64(d)}, 3)
	}
	if len(list) != 3 {
		t.Fatalf("应只留 3 根，实际 %d: %+v", len(list), list)
	}
	// 必须是最大的三天，且升序
	want := []int32{7, 8, 9}
	for i, w := range want {
		if list[i].TradeDate != w {
			t.Errorf("第 %d 根应为 %d，实际 %d（%+v）", i, w, list[i].TradeDate, list)
		}
	}
}

// 同一天重复（多份快照/重复写）要以后来的为准，不能出现两根同一天
func TestKeepLastBarReplacesSameDay(t *testing.T) {
	list := keepLastBar(nil, cnlake.BarRow{TradeDate: 10, Close: 1}, 3)
	list = keepLastBar(list, cnlake.BarRow{TradeDate: 10, Close: 2}, 3)
	if len(list) != 1 {
		t.Fatalf("同一天不该占两根：%+v", list)
	}
	if list[0].Close != 2 {
		t.Errorf("应以后来那根为准（2），实际 %v", list[0].Close)
	}
}

// 历史很长时，bulkQuotes 也要给出**最后一根**和前收 ——
// 这是股票列表的核心口径，改成"只留尾巴"后必须完全一致。
func TestLakeBulkQuotesUsesLastBarWithPrevClose(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {
			"2026-01-01": 1, "2026-01-02": 2, "2026-01-03": 3,
			"2026-01-04": 4, "2026-01-05": 5, "2026-01-06": 6,
			"2026-01-07": 7, "2026-01-08": 8,
		},
	})
	p := &lakeProvider{lake: lake}

	// start 给到很早：这就是"不限起点补抓"那条路
	got, missed, err := p.bulkQuotes(context.Background(), []string{"600519.SH"}, 19900101, 20260108)
	if err != nil {
		t.Fatalf("bulkQuotes 失败：%v", err)
	}
	if len(missed) != 0 {
		t.Errorf("不该有缺行情的代码：%v", missed)
	}
	quote := got["600519"]
	if quote == nil {
		t.Fatalf("应拿到行情，实际 %+v", got)
	}
	if quote.Close != 8 {
		t.Errorf("收盘应为最后一根 8，实际 %v", quote.Close)
	}
	if quote.Date != 20260108 {
		t.Errorf("日期应为 20260108，实际 %v", quote.Date)
	}
}

// 只有一根的股票：前收为 0，但不能报错也不能被算成"缺失"
func TestLakeBulkQuotesSingleBar(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"300750.SZ": {"2026-01-08": 30},
	})
	p := &lakeProvider{lake: lake}

	got, missed, err := p.bulkQuotes(context.Background(), []string{"300750.SZ"}, 19900101, 20260108)
	if err != nil {
		t.Fatalf("bulkQuotes 失败：%v", err)
	}
	if len(missed) != 0 {
		t.Errorf("有一根就不算缺失：%v", missed)
	}
	if q := got["300750"]; q == nil || q.Close != 30 {
		t.Errorf("应拿到 30，实际 %+v", q)
	}
}

// 完全没有日线的代码要进 missed（上层据此再走"按股票缓存/全表扫描"）
func TestLakeBulkQuotesMissed(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {"2026-01-08": 8},
	})
	p := &lakeProvider{lake: lake}

	_, missed, err := p.bulkQuotes(context.Background(), []string{"600519.SH", "000002.SZ"}, 19900101, 20260108)
	if err != nil {
		t.Fatalf("bulkQuotes 失败：%v", err)
	}
	if len(missed) != 1 || missed[0] != "000002" {
		t.Errorf("应只把没有日线的 000002 记进 missed，实际 %v", missed)
	}
}

// 区间上限要生效：end 之后的日线不能算进来（否则会拿到"未来"的收盘价）
func TestLakeBulkQuotesRespectsEnd(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {"2026-01-07": 7, "2026-01-08": 8},
	})
	p := &lakeProvider{lake: lake}

	got, _, err := p.bulkQuotes(context.Background(), []string{"600519.SH"}, 19900101, 20260107)
	if err != nil {
		t.Fatalf("bulkQuotes 失败：%v", err)
	}
	q := got["600519"]
	if q == nil {
		t.Fatalf("应拿到行情")
	}
	if q.Date != 20260107 || q.Close != 7 {
		t.Errorf("截止 1/7 时应取 1/7 的 7，实际 date=%v close=%v", q.Date, q.Close)
	}
}
