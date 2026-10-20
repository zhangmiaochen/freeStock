package chanlunassist

import (
	"reflect"
	"testing"

	"freestock/internal/chanlun"
)

// 造一段锯齿行情：每个拐点之间用 barsPerLeg 根 K 线线性插值。
// 日期用连续整数即可 —— 缠论只依赖日期的先后顺序。
func zigzagBars(points []float64, barsPerLeg int, startDate int) []chanlun.Bar {
	if len(points) < 2 || barsPerLeg < 1 {
		return nil
	}
	bars := make([]chanlun.Bar, 0, (len(points)-1)*barsPerLeg)
	date := startDate
	prevClose := points[0]
	for i := 1; i < len(points); i++ {
		from, to := points[i-1], points[i]
		for step := 1; step <= barsPerLeg; step++ {
			closePrice := from + (to-from)*float64(step)/float64(barsPerLeg)
			openPrice := prevClose
			high := maxFloat(openPrice, closePrice) * 1.008
			low := minFloat(openPrice, closePrice) * 0.992
			bars = append(bars, chanlun.Bar{
				Date: date, Open: openPrice, High: high, Low: low, Close: closePrice,
				Volume: 1_000_000,
			})
			prevClose = closePrice
			date++
		}
	}
	return bars
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// 信号筛选：只看确认、新鲜、不矛盾、分够高的；同类型取最强。
func TestSignalsFromAnalysisSelection(t *testing.T) {
	lastIdx := 100
	signals := []chanlun.Signal{
		// 二买：95 分低、98 分高 → 取 98
		{Type: chanlun.SignalBuy2, Direction: "buy", State: chanlun.StateConfirmed,
			ConfirmIndex: 95, ConfirmDate: 20251231, Score: 60},
		{Type: chanlun.SignalBuy2, Direction: "buy", State: chanlun.StateConfirmed,
			ConfirmIndex: 98, ConfirmDate: 20260101, Score: 70},
		// 三买太老（20 根前）→ 丢弃
		{Type: chanlun.SignalBuy3, Direction: "buy", State: chanlun.StateConfirmed,
			ConfirmIndex: 80, ConfirmDate: 20251220, Score: 90},
		// 一卖只是触发，默认不算
		{Type: chanlun.SignalSell1, Direction: "sell", State: chanlun.StateTriggered,
			TriggerIndex: 99, TriggerDate: 20260101, Score: 80},
		// 二卖结构矛盾 → 默认剔除
		{Type: chanlun.SignalSell2, Direction: "sell", State: chanlun.StateConfirmed,
			ConfirmIndex: 90, ConfirmDate: 20251228, Score: 60, Conflict: true},
		// 三卖正常保留
		{Type: chanlun.SignalSell3, Direction: "sell", State: chanlun.StateConfirmed,
			ConfirmIndex: 92, ConfirmDate: 20251230, Score: 50},
		// 一买不参与辅助规则（本设计只认二/三买）
		{Type: chanlun.SignalBuy1, Direction: "buy", State: chanlun.StateConfirmed,
			ConfirmIndex: 99, ConfirmDate: 20260101, Score: 99},
	}

	cfg := DefaultConfig()
	got := signalsFromAnalysis(signals, lastIdx, cfg)
	if got.Buy2 == nil || got.Buy2.Score != 70 || got.Buy2.ConfirmIndex != 98 {
		t.Errorf("二买应取分最高的那条（98/70），实际 %+v", got.Buy2)
	}
	if got.Buy3 != nil {
		t.Errorf("过期三买不应保留，实际 %+v", got.Buy3)
	}
	if got.Sell1 != nil {
		t.Errorf("默认不认触发信号，实际 %+v", got.Sell1)
	}
	if got.Sell2 != nil {
		t.Errorf("矛盾信号默认应剔除，实际 %+v", got.Sell2)
	}
	if got.Sell3 == nil {
		t.Errorf("三卖应保留")
	}

	// 打开「允许触发 + 允许矛盾」后，一卖与二卖都应出现。
	cfg.AllowTriggered = true
	cfg.AllowConflict = true
	loose := signalsFromAnalysis(signals, lastIdx, cfg)
	if loose.Sell1 == nil || loose.Sell2 == nil {
		t.Errorf("放宽后一卖/二卖都应保留：%+v", loose)
	}
	if loose.Sell1.ConfirmDate != 20260101 {
		t.Errorf("触发信号应回落到触发日，实际 %d", loose.Sell1.ConfirmDate)
	}

	// 最低分过滤。
	cfg = DefaultConfig()
	cfg.MinSignalScore = 65
	filtered := signalsFromAnalysis(signals, lastIdx, cfg)
	if filtered.Sell3 != nil {
		t.Errorf("三卖分 50 < 65，应被过滤")
	}
	if filtered.Buy2 == nil {
		t.Errorf("二买分 70 应保留")
	}
}

// 新鲜度边界：恰好 10 根前保留，11 根前丢弃。
func TestSignalsFreshnessBoundary(t *testing.T) {
	cfg := DefaultConfig()
	makeSignal := func(idx int) chanlun.Signal {
		return chanlun.Signal{Type: chanlun.SignalSell1, Direction: "sell",
			State: chanlun.StateConfirmed, ConfirmIndex: idx, ConfirmDate: 20260101, Score: 80}
	}
	if got := signalsFromAnalysis([]chanlun.Signal{makeSignal(90)}, 100, cfg); got.Sell1 == nil {
		t.Error("10 个交易日前确认的信号应保留")
	}
	if got := signalsFromAnalysis([]chanlun.Signal{makeSignal(89)}, 100, cfg); got.Sell1 != nil {
		t.Error("11 个交易日前确认的信号应丢弃")
	}
}

// Analyzer 必须只用 asOf 之前的 K 线：追加未来暴跌不能改变历史某天的信号。
func TestAnalyzerNoLookahead(t *testing.T) {
	base := zigzagBars([]float64{100, 80, 92, 74, 95, 70, 100, 120}, 12, 20240101)

	// 未来一段暴跌（如果用了未来数据，历史的二买/三买会被改成失效）。
	future := make([]chanlun.Bar, 0, 40)
	last := base[len(base)-1]
	price := last.Close
	for i := 0; i < 40; i++ {
		price *= 0.96
		future = append(future, chanlun.Bar{
			Date: last.Date + 1 + i, Open: price * 1.01, High: price * 1.02,
			Low: price * 0.97, Close: price, Volume: 1_000_000,
		})
	}
	extended := append(append([]chanlun.Bar{}, base...), future...)

	cfg := chanlun.DefaultConfig()
	a1 := NewAnalyzer(map[string][]chanlun.Bar{"600000": base}, cfg, DefaultConfig())
	a2 := NewAnalyzer(map[string][]chanlun.Bar{"600000": extended}, cfg, DefaultConfig())

	checked := 0
	for i := 20; i < len(base); i += 3 {
		asOf := base[i].Date
		s1 := a1.Signals("600000", asOf)
		s2 := a2.Signals("600000", asOf)
		if !reflect.DeepEqual(s1, s2) {
			t.Fatalf("日期 %d 的信号受未来数据影响：\n base=%+v\n ext =%+v", asOf, s1, s2)
		}
		for _, sig := range []*Signal{s1.Buy2, s1.Buy3, s1.Sell1, s1.Sell2, s1.Sell3} {
			if sig == nil {
				continue
			}
			if sig.ConfirmDate > asOf && sig.TriggerDate > asOf {
				t.Errorf("信号日期晚于 asOf %d：%+v", asOf, sig)
			}
			if sig.AgeBars < 0 || sig.AgeBars > DefaultConfig().BuyFreshBars {
				t.Errorf("新鲜度越界：%+v", sig)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有检查任何日期")
	}
	if a1.CacheSize() == 0 {
		t.Error("分析结果应被缓存")
	}
	if a1.CodeCount() != 1 {
		t.Errorf("股票数应为 1，实际 %d", a1.CodeCount())
	}
}

// Analyzer 在数据不足 / 未知代码 / asOf 过早时必须返回空快照而不是 panic。
func TestAnalyzerInsufficientData(t *testing.T) {
	bars := zigzagBars([]float64{10, 11, 12}, 3, 20240101)
	a := NewAnalyzer(map[string][]chanlun.Bar{"600000": bars}, chanlun.DefaultConfig(), DefaultConfig())
	if got := a.Signals("600000", 20230101); !got.Empty() {
		t.Errorf("asOf 早于全部 K 线时应为空，实际 %+v", got)
	}
	if got := a.Signals("600000", 20240103); !got.Empty() {
		t.Errorf("K 线不足时不应产出信号，实际 %+v", got)
	}
	if got := a.Signals("999999", 20240201); !got.Empty() {
		t.Errorf("未知代码应返回空，实际 %+v", got)
	}
}

// 分析器结果必须与「直接对截断序列调用 chanlun.Analyze」一致 ——
// 这是无未来函数的最小可验证定义。
func TestAnalyzerMatchesDirectTruncatedAnalysis(t *testing.T) {
	bars := zigzagBars([]float64{100, 75, 105, 70, 110, 65, 120}, 10, 20240101)
	cfg := chanlun.DefaultConfig()
	assist := DefaultConfig()
	a := NewAnalyzer(map[string][]chanlun.Bar{"600000": bars}, cfg, assist)

	for i := 30; i < len(bars); i += 7 {
		asOf := bars[i].Date
		want := signalsFromAnalysis(chanlun.Analyze(bars[:i+1], chanlun.LevelDay, cfg, chanlun.AdaptInfo{}).Signals, i, assist)
		if got := a.Signals("600000", asOf); !reflect.DeepEqual(got, want) {
			t.Fatalf("日期 %d：分析器结果与截断分析不一致\n got=%+v\nwant=%+v", asOf, got, want)
		}
	}
}
