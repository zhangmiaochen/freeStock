package volprice

import (
	"math"
	"testing"
)

// **市场状态识别与原版 pandas 实现的比对。**
//
// 黄金值由 `testdata/gen_market_state_golden.py` 生成 —— 脚本直接 import 原项目的
// `volume_price_market_state`。分三块比：
//
//  1. `build_market_width_daily`（分组、均值跳过 NaN、缺失 pct_chg 走 close/pre_close）
//  2. `detect_market_state_from_width` 与 `build_market_state_cache`
//     （后者用 `searchsorted(side="right")` 切 80 日窗口，边界最容易错）
//  3. `detect_stock_context_state_prepared`（回测里逐根 K 线真正用的那个）
func TestMarketWidthParityWithOriginalPandas(t *testing.T) {
	rows := make([]MarketDailyRow, 0, len(goldenMarketRows))
	for _, item := range goldenMarketRows {
		rows = append(rows, MarketDailyRow{
			TradeDate: item.TradeDate, Close: item.Close, PreClose: item.PreClose,
			PctChg: item.PctChg, Amount: item.Amount,
		})
	}
	got := BuildMarketWidthDaily(MarketDailyFrame{
		Rows:            rows,
		HasPctChgColumn: goldenHasPctChgColumn,
		HasAmountColumn: goldenHasAmountColumn,
	})
	compareWidthRows(t, got, goldenWidth)

	// 第二组：整列缺失 → 补算分支
	deriveRows := make([]MarketDailyRow, 0, len(goldenDeriveRows))
	for _, item := range goldenDeriveRows {
		deriveRows = append(deriveRows, MarketDailyRow{
			TradeDate: item.TradeDate, Close: item.Close, PreClose: item.PreClose,
			PctChg: item.PctChg, Amount: item.Amount,
		})
	}
	deriveGot := BuildMarketWidthDaily(MarketDailyFrame{
		Rows:            deriveRows,
		HasPctChgColumn: goldenDeriveHasPctChgColumn,
		HasAmountColumn: goldenHasAmountColumn,
	})
	compareWidthRows(t, deriveGot, goldenDeriveWidth)
}

func compareWidthRows(t *testing.T, got, want []WidthDaily) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("宽度行数不一致：go %d，pandas %d", len(got), len(want))
	}
	for i, expected := range want {
		mine := got[i]
		if mine.TradeDate != expected.TradeDate {
			t.Errorf("第 %d 行日期：go %q，pandas %q", i, mine.TradeDate, expected.TradeDate)
			continue
		}
		compareGoldenFloat(t, "width["+expected.TradeDate+"].avg_pct", mine.AvgPct, expected.AvgPct)
		compareGoldenFloat(t, "width["+expected.TradeDate+"].up_ratio", mine.UpRatio, expected.UpRatio)
		compareGoldenFloat(t, "width["+expected.TradeDate+"].down_ratio", mine.DownRatio, expected.DownRatio)
		compareGoldenFloat(t, "width["+expected.TradeDate+"].amount", mine.Amount, expected.Amount)
		compareGoldenFloat(t, "width["+expected.TradeDate+"].count", mine.Count, expected.Count)
	}
}

func TestDetectMarketStateFromWidthParity(t *testing.T) {
	width := append([]WidthDaily(nil), goldenWidth...)
	for date, want := range goldenFromWidth {
		got := DetectMarketStateFromWidth(width, date, "")
		compareMarketState(t, "detect_market_state_from_width["+date+"]", got, want)
	}
}

func TestBuildMarketStateCacheParity(t *testing.T) {
	rows := make([]MarketDailyRow, 0, len(goldenMarketRows))
	for _, item := range goldenMarketRows {
		rows = append(rows, MarketDailyRow{
			TradeDate: item.TradeDate, Close: item.Close, PreClose: item.PreClose,
			PctChg: item.PctChg, Amount: item.Amount,
		})
	}
	dates := make([]string, 0, len(goldenCache))
	for date := range goldenCache {
		dates = append(dates, date)
	}
	got := BuildMarketStateCache(MarketDailyFrame{
		Rows:            rows,
		HasPctChgColumn: goldenHasPctChgColumn,
		HasAmountColumn: goldenHasAmountColumn,
	}, dates)
	if len(got) != len(goldenCache) {
		t.Fatalf("缓存条数不一致：go %d，pandas %d", len(got), len(goldenCache))
	}
	for date, want := range goldenCache {
		compareMarketState(t, "build_market_state_cache["+date+"]", got[date], want)
	}
}

func TestDetectStockContextStateParity(t *testing.T) {
	bars := make([]Bar, 0, len(goldenStockBars))
	for _, item := range goldenStockBars {
		bars = append(bars, Bar{
			Date: item.Date, Open: item.Open, High: item.High,
			Low: item.Low, Close: item.Close, Volume: item.Volume,
		})
	}
	for date, want := range goldenStockState {
		got := DetectStockContextState(bars, date, "")
		compareMarketState(t, "detect_stock_context_state["+date+"]", got, want)
	}
}

func compareMarketState(t *testing.T, label string, got MarketStateInfo, want goldenState) {
	t.Helper()
	if got.State != want.State {
		t.Errorf("%s.state：go %q，pandas %q", label, got.State, want.State)
	}
	if got.Label != want.Label {
		t.Errorf("%s.label：go %q，pandas %q", label, got.Label, want.Label)
	}
	if got.Source != want.Source {
		t.Errorf("%s.source：go %q，pandas %q", label, got.Source, want.Source)
	}
	if got.TradeDate != want.TradeDate {
		t.Errorf("%s.trade_date：go %q，pandas %q", label, got.TradeDate, want.TradeDate)
	}
	// 打分有 round(...,2)，浮点尾差按 0.011 容忍
	if math.Abs(got.Score-want.Score) > 0.011 {
		t.Errorf("%s.score：go %v，pandas %v", label, got.Score, want.Score)
	}
	if got.Reason != want.Reason {
		t.Errorf("%s.reason：\n  go     %q\n  pandas %q", label, got.Reason, want.Reason)
	}
}

func compareGoldenFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	gotNaN, wantNaN := math.IsNaN(got), math.IsNaN(want)
	if gotNaN || wantNaN {
		if gotNaN != wantNaN {
			t.Errorf("%s：go %v，pandas %v（NaN 不一致）", label, got, want)
		}
		return
	}
	if diff := math.Abs(got - want); diff > 1e-9 {
		t.Errorf("%s：go %.12f，pandas %.12f（差 %.3g）", label, got, want, diff)
	}
}
