package chanlun

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// mkBars 生成一段价格恒定的 K 线，供手工构造结构用。
func mkBars(n int) []Bar {
	bars := make([]Bar, n)
	for i := 0; i < n; i++ {
		bars[i] = Bar{
			Date:   20200101 + i,
			Open:   20,
			High:   22,
			Low:    18,
			Close:  20,
			Volume: 1000,
			Amount: 1e8,
		}
	}
	return bars
}

// zigzag 按给定的转折价生成锯齿 K 线，每段 seg 根。
//
// 这是结构测试最基础的输入：转折点清晰，分型与笔可以人工推算。
func zigzag(points []float64, seg int) []Bar {
	bars := make([]Bar, 0, (len(points)-1)*seg)
	date := 20200101
	price := points[0]
	for i := 1; i < len(points); i++ {
		target := points[i]
		for k := 0; k < seg; k++ {
			next := price + (target-price)*float64(k+1)/float64(seg)
			bars = append(bars, Bar{
				Date:   date,
				Open:   price,
				High:   math.Max(price, next) + 0.2,
				Low:    math.Min(price, next) - 0.2,
				Close:  next,
				Volume: 1000,
				Amount: 5e8,
			})
			date++
			price = next
		}
	}
	return bars
}

// mkStroke 构造一条笔（下标与价格直接给出）。
func mkStroke(index, direction, startIdx, endIdx int, startPrice, endPrice float64, bars []Bar, confirmed bool) Stroke {
	stroke := Stroke{
		Index:      index,
		Direction:  direction,
		StartIndex: startIdx,
		EndIndex:   endIdx,
		StartPrice: startPrice,
		EndPrice:   endPrice,
		StartDate:  bars[startIdx].Date,
		EndDate:    bars[endIdx].Date,
		Confirmed:  confirmed,
		Level:      LevelDay,
	}
	stroke.High, stroke.Low = strokeRange(stroke)
	// 确认时间落在终点之后两根，模拟"分型等后续 K 线确认"
	if endIdx+2 < len(bars) {
		stroke.ConfirmDate = bars[endIdx+2].Date
	} else {
		stroke.ConfirmDate = bars[endIdx].Date
	}
	return stroke
}

// ---------------------------------------------------------------------------
// 1. 包含处理
// ---------------------------------------------------------------------------

func TestMergeBarsInclusion(t *testing.T) {
	bars := []Bar{
		{Date: 20200101, Open: 8, High: 10, Low: 5, Close: 9},
		{Date: 20200102, Open: 8, High: 9, Low: 6, Close: 7}, // 被第一根包含
		{Date: 20200103, Open: 8, High: 11, Low: 7, Close: 10},
	}
	merged := mergeBars(bars)
	if len(merged) != 2 {
		t.Fatalf("包含处理应合并为 2 根，得到 %d", len(merged))
	}
	first := merged[0]
	if first.Members != 2 || first.StartIndex != 0 || first.EndIndex != 1 {
		t.Fatalf("合并区间不正确：%+v", first)
	}
	// 向下包含：新高取较小、新低取较小
	if first.High != 9 || first.Low != 5 {
		t.Fatalf("向下包含结果应为 H9/L5，得到 H%.2f/L%.2f", first.High, first.Low)
	}
	if merged[1].Direction != 1 {
		t.Fatalf("第二根相对第一根向上，方向应为 +1，得到 %d", merged[1].Direction)
	}
}

func TestMergeBarsDirtyRowsSkipped(t *testing.T) {
	bars := []Bar{
		{Date: 20200101, Open: 8, High: 10, Low: 5, Close: 9},
		{Date: 20200102, Open: 0, High: 0, Low: 0, Close: 0}, // 停牌/未同步
		{Date: 20200103, Open: 9, High: 12, Low: 8, Close: 11},
	}
	merged := mergeBars(bars)
	if len(merged) != 2 {
		t.Fatalf("脏数据应被跳过但不影响其余结构，得到 %d 根", len(merged))
	}
	if merged[1].StartIndex != 2 {
		t.Fatalf("第二根合并 K 线应指向原始下标 2，得到 %d", merged[1].StartIndex)
	}
}

// ---------------------------------------------------------------------------
// 2. 分型与笔
// ---------------------------------------------------------------------------

func TestFractalsAreConfirmedByNextBar(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22}, 6)
	merged := mergeBars(bars)
	fractals := detectFractals(merged, LevelDay)
	if len(fractals) == 0 {
		t.Fatal("锯齿数据应至少识别出一个分型")
	}
	for _, fractal := range fractals {
		if fractal.ConfirmDate <= 0 {
			t.Fatalf("分型必须有确认时间：%+v", fractal)
		}
		if fractal.ConfirmDate < fractal.Date {
			t.Fatalf("确认时间不能早于形成时间：%+v", fractal)
		}
		if fractal.Type == FractalTop && fractal.Price <= 0 {
			t.Fatalf("顶分型价格异常：%+v", fractal)
		}
	}
}

func TestStrokesAlternateAndObeyMinGap(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	fractals := detectFractals(mergeBars(bars), LevelDay)
	strokes := buildStrokes(fractals, bars, LevelDay)
	if len(strokes) == 0 {
		t.Fatal("应至少形成一笔")
	}
	for i, stroke := range strokes {
		if i == 0 {
			continue
		}
		prev := strokes[i-1]
		if stroke.Direction == prev.Direction {
			t.Fatalf("笔必须方向交替：第 %d 与第 %d 笔同向", i-1, i)
		}
		if prev.EndIndex != stroke.StartIndex {
			t.Fatalf("相邻笔应首尾相接：%d -> %d", prev.EndIndex, stroke.StartIndex)
		}
		// 延伸中的最后一笔没有"下一个分型"，不参与最小间隔校验
		if stroke.Confirmed && stroke.EndMerged-stroke.StartMerged < minStrokeMergedGap {
			t.Fatalf("笔不满足最小合并 K 线间隔：%+v", stroke)
		}
		if stroke.Direction > 0 && stroke.EndPrice <= stroke.StartPrice {
			t.Fatalf("向上笔终点应高于起点：%+v", stroke)
		}
		if stroke.Direction < 0 && stroke.EndPrice >= stroke.StartPrice {
			t.Fatalf("向下笔终点应低于起点：%+v", stroke)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. 中枢
// ---------------------------------------------------------------------------

func TestPivotOverlapAndExtension(t *testing.T) {
	bars := mkBars(80)
	strokes := []Stroke{
		mkStroke(0, 1, 0, 5, 10, 20, bars, true),
		mkStroke(1, -1, 5, 10, 20, 14, bars, true),
		mkStroke(2, 1, 10, 15, 14, 19, bars, true),
		mkStroke(3, -1, 15, 20, 19, 15, bars, true), // 仍在重叠区
		mkStroke(4, 1, 20, 25, 15, 22, bars, true),  // 仍在重叠区
		mkStroke(5, -1, 25, 30, 22, 11, bars, true), // 穿越重叠区，仍算延伸
		mkStroke(6, 1, 30, 35, 11, 13, bars, true),  // 完全在 ZD 之下 → 离开中枢
	}
	pivots := buildPivots(strokes, bars, LevelDay)
	if len(pivots) == 0 {
		t.Fatal("三笔重叠应形成中枢")
	}
	pivot := pivots[0]
	if pivot.ZG != 19 || pivot.ZD != 14 {
		t.Fatalf("中枢区间应为 14~19，得到 %.2f~%.2f", pivot.ZD, pivot.ZG)
	}
	// 3 笔成立 + 3 笔延伸（含穿越重叠区的那一笔）
	if pivot.StrokeCount != 6 {
		t.Fatalf("中枢应延伸为 6 笔，得到 %d", pivot.StrokeCount)
	}
	if pivot.Status != PivotExtension {
		t.Fatalf("中枢状态应为延伸，得到 %s", pivot.Status)
	}
	if pivot.LeaveStroke != 6 {
		t.Fatalf("离开段应为第 6 笔，得到 %d", pivot.LeaveStroke)
	}
	if !pivot.Confirmed {
		t.Fatal("第三笔确认后中枢应已确认")
	}
}

func TestPivotNeedsOverlap(t *testing.T) {
	bars := mkBars(40)
	// 第二笔跌回起点之下、第三笔仍在下沿之下：三段没有共同重叠区 → 不构成中枢
	strokes := []Stroke{
		mkStroke(0, 1, 0, 5, 10, 20, bars, true),
		mkStroke(1, -1, 5, 10, 20, 5, bars, true),
		mkStroke(2, 1, 10, 15, 5, 8, bars, true),
	}
	if pivots := buildPivots(strokes, bars, LevelDay); len(pivots) != 0 {
		t.Fatalf("三段没有共同重叠区不应形成中枢，得到 %d 个", len(pivots))
	}
}

// ---------------------------------------------------------------------------
// 4. 走势类型
// ---------------------------------------------------------------------------

func TestTrendClassification(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	cases := []struct {
		name   string
		pivots []Pivot
		want   TrendType
	}{
		{"无中枢", nil, TrendNone},
		{"单中枢盘整", []Pivot{{Index: 0, ZD: 10, ZG: 12}}, TrendConsolidation},
		{"两个中枢抬高", []Pivot{{Index: 0, ZD: 10, ZG: 12}, {Index: 1, ZD: 13, ZG: 15}}, TrendUp},
		{"两个中枢降低", []Pivot{{Index: 0, ZD: 13, ZG: 15}, {Index: 1, ZD: 10, ZG: 12}}, TrendDown},
		{"两个中枢重叠", []Pivot{{Index: 0, ZD: 10, ZG: 13}, {Index: 1, ZD: 11, ZG: 14}}, TrendConsolidation},
	}
	for _, tc := range cases {
		trend := classifyTrend(tc.pivots, nil, bars, LevelDay)
		if trend.Type != tc.want {
			t.Fatalf("%s：期望 %s，得到 %s", tc.name, tc.want, trend.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// 4.1 走势终完美：生命周期与完全分类
// ---------------------------------------------------------------------------

func TestTrendLifecycleAndOutlook(t *testing.T) {
	bars := mkBars(120)
	// 价格放在最后一个中枢附近：完全分类里的"状态"是按**当前收盘价**相对
	// 中枢边沿判断的，测试数据先把这点固定下来（否则 close=20 会直接被判成反转进行中）
	for i := range bars {
		bars[i].Open, bars[i].High, bars[i].Low, bars[i].Close = 14, 14.6, 13.4, 14
	}
	// 两个依次降低的中枢 → 下跌趋势
	pivots := []Pivot{
		{Index: 0, ZG: 20, ZD: 18, StartIndex: 5, EndIndex: 35, StartStroke: 0, EndStroke: 0, Confirmed: true},
		{Index: 1, ZG: 15, ZD: 13, StartIndex: 40, EndIndex: 60, StartStroke: 1, EndStroke: 2, Confirmed: true},
	}

	// 还没出现反向破坏 → 延伸中，三种可能都列出来
	trend := classifyTrend(pivots, nil, bars, LevelDay)
	if trend.Type != TrendDown {
		t.Fatalf("应为下跌趋势，得到 %s", trend.Type)
	}
	if trend.Status != TrendExtending {
		t.Fatalf("未出现反向破坏时应为「延伸中」，得到 %s", trend.Status)
	}
	if len(trend.Outlook) != 3 {
		t.Fatalf("完全分类应有 3 种可能，得到 %d", len(trend.Outlook))
	}
	// 收盘价在中枢内部：趋势没破但也没在延续 → 延伸暂停、盘整进行中
	if trend.Outlook[0].Status != ScenarioPaused {
		t.Fatalf("价格回到中枢时「延续」应为暂停：%+v", trend.Outlook[0])
	}
	if trend.Outlook[1].Status != ScenarioActive {
		t.Fatalf("价格回到中枢时「转为盘整」应为进行中：%+v", trend.Outlook[1])
	}
	if trend.Outlook[2].Key != "reversal" || trend.Outlook[2].Status != ScenarioPending {
		t.Fatalf("未完成时「反转」应为待发生：%+v", trend.Outlook[2])
	}

	// 向上笔升破最后一个中枢上沿 → 下跌趋势完成
	breaking := []Stroke{mkStroke(3, 1, 60, 70, 13, 16, bars, true)}
	trend = classifyTrend(pivots, breaking, bars, LevelDay)
	if trend.Status != TrendCompleted {
		t.Fatalf("升破最后一个中枢后应「已完成」，得到 %s", trend.Status)
	}
	if trend.CompletedBy != "breakup" {
		t.Fatalf("完成方式应为 breakup，得到 %q", trend.CompletedBy)
	}
	if trend.CompletedDate != bars[72].Date {
		t.Fatalf("完成时间应为该笔分型确认日 %d，得到 %d", bars[72].Date, trend.CompletedDate)
	}
	if trend.CompletionReason == "" {
		t.Fatal("完成必须有原因说明")
	}
	// 完成之后：延续被排除、至少转盘整、反转进行中
	if trend.Outlook[0].Status != ScenarioInvalidated {
		t.Fatalf("趋势完成后「延续」应被排除：%+v", trend.Outlook[0])
	}
	if trend.Outlook[1].Status != ScenarioConfirmed {
		t.Fatalf("趋势完成后至少「转为盘整」：%+v", trend.Outlook[1])
	}
	if trend.Outlook[2].Status != ScenarioActive {
		t.Fatalf("趋势完成后「反转」应为进行中：%+v", trend.Outlook[2])
	}

	// 盘整完成：向上离开后回抽不回中枢（三买结构）
	consolidation := []Pivot{
		{Index: 0, ZG: 12, ZD: 10, StartIndex: 5, EndIndex: 30, StartStroke: 0, EndStroke: 1, Confirmed: true},
	}
	leaveUp := []Stroke{
		mkStroke(0, 1, 30, 40, 10, 13, bars, true),    // 越过 ZG=12
		mkStroke(1, -1, 40, 50, 13, 12.5, bars, true), // 回抽低点 12.5 仍在 ZG 之上
	}
	trend = classifyTrend(consolidation, leaveUp, bars, LevelDay)
	if trend.Type != TrendConsolidation || trend.Status != TrendCompleted {
		t.Fatalf("向上离开且回抽不回应判盘整完成，得到 type=%s status=%s", trend.Type, trend.Status)
	}
	if trend.CompletedBy != "leave-up" {
		t.Fatalf("完成方式应为 leave-up，得到 %q", trend.CompletedBy)
	}
	if trend.Outlook[0].Status != ScenarioConfirmed || trend.Outlook[1].Status != ScenarioInvalidated {
		t.Fatalf("盘整向上完成后的三种可能状态不对：%+v", trend.Outlook)
	}
}

// ---------------------------------------------------------------------------
// 4.2 背驰 ≠ 反转：三种可能的状态
// ---------------------------------------------------------------------------

func TestDivergenceOutcomes(t *testing.T) {
	cfg := DefaultConfig()
	makeBars := func() []Bar {
		bars := mkBars(100)
		bars[40].Low = 10
		return bars
	}
	pivots := []Pivot{
		{Index: 0, ZG: 20, ZD: 15, StartIndex: 5, EndIndex: 30, StartStroke: 0, EndStroke: 1, Confirmed: true},
	}
	div := Divergence{
		Index: 0, Kind: "trend", KindCN: "趋势背驰", Direction: "bottom",
		Level: LevelDay, PivotIndex: 0, LegBEnd: 40, Ratio: 0.8,
		NewExtreme: true, Confirmed: true,
	}

	outcomeOf := func(divs []Divergence, key string) DivergenceOutcome {
		for _, item := range divs[0].Outcomes {
			if item.Key == key {
				return item
			}
		}
		t.Fatalf("缺少分支 %s", key)
		return DivergenceOutcome{}
	}

	// 1) 三条路都还没走出来 → 待定
	out := resolveDivergenceOutcomes([]Divergence{div}, nil, pivots, makeBars(), cfg)
	if out[0].Outcome != "pending" {
		t.Fatalf("都未发生时应为待定，得到 %q", out[0].Outcome)
	}
	for _, item := range out[0].Outcomes {
		if item.Status != ScenarioPending {
			t.Fatalf("都未发生时每条都应是待发生：%+v", item)
		}
	}
	if len(out[0].Outcomes) != 3 {
		t.Fatalf("背驰后应有三种可能，得到 %d", len(out[0].Outcomes))
	}

	// 2) 继续创新低 → 背驰失败
	failBars := makeBars()
	failBars[60].Low = 9.5
	out = resolveDivergenceOutcomes([]Divergence{div}, nil, pivots, failBars, cfg)
	if out[0].Outcome != "failure" {
		t.Fatalf("创新低应判背驰失败，得到 %q", out[0].Outcome)
	}
	if outcomeOf(out, "failure").Date != failBars[60].Date {
		t.Fatalf("失败时间应为创新低那天：%+v", outcomeOf(out, "failure"))
	}

	// 3) 由该背驰派生的买点确认 → 反转
	signals := []Signal{{
		Type: SignalBuy2, Direction: "buy", Level: LevelDay,
		DivergenceIndex: 0, State: StateConfirmed,
		ConfirmIndex: 70, ConfirmDate: makeBars()[70].Date,
	}}
	out = resolveDivergenceOutcomes([]Divergence{div}, signals, pivots, makeBars(), cfg)
	if out[0].Outcome != "reversal" {
		t.Fatalf("买点确认后应判反转，得到 %q", out[0].Outcome)
	}
	if outcomeOf(out, "reversal").Status != ScenarioConfirmed {
		t.Fatalf("反转应为已确认：%+v", outcomeOf(out, "reversal"))
	}

	// 4) 背驰后形成与最后一个中枢重叠的新中枢 → 盘整
	withPivot := append([]Pivot{}, pivots...)
	withPivot = append(withPivot, Pivot{
		Index: 1, ZG: 18, ZD: 14, StartIndex: 55, EndIndex: 65,
		StartStroke: 2, EndStroke: 3, Confirmed: true, ConfirmDate: makeBars()[67].Date,
	})
	out = resolveDivergenceOutcomes([]Divergence{div}, nil, withPivot, makeBars(), cfg)
	if out[0].Outcome != "consolidation" {
		t.Fatalf("形成重叠中枢应判盘整，得到 %q", out[0].Outcome)
	}
	if outcomeOf(out, "consolidation").Date != makeBars()[67].Date {
		t.Fatalf("盘整确认时间应为新中枢确认日：%+v", outcomeOf(out, "consolidation"))
	}
}

// ---------------------------------------------------------------------------
// 5. 背驰的前置约束
// ---------------------------------------------------------------------------

func TestNoPivotNoDivergence(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	macd := computeMACD(bars, 12, 26, 9)
	cfg := DefaultConfig()

	if divs := detectDivergences(bars, nil, nil, Trend{Type: TrendNone}, macd, cfg); len(divs) != 0 {
		t.Fatalf("没有中枢不应有背驰，得到 %d 个", len(divs))
	}

	// 上涨趋势但只有一个中枢：不满足"趋势至少两个中枢"
	pivots := []Pivot{{Index: 0, ZD: 10, ZG: 12, StartIndex: 10, EndIndex: 20, StartStroke: 1, EndStroke: 3}}
	divs := detectDivergences(bars, nil, pivots, Trend{Type: TrendUp, Level: LevelDay}, macd, cfg)
	if len(divs) != 0 {
		t.Fatalf("单中枢不得判趋势背驰，得到 %d 个", len(divs))
	}
}

func TestDivergenceNeedsNewExtreme(t *testing.T) {
	// 中枢 A 与中枢 B 抬高，但离开段没有创新高 → 不构成趋势背驰
	bars := make([]Bar, 0)
	prices := []float64{10, 14, 11, 15, 12, 16, 13, 15.5, 12.5, 14.5, 12, 14}
	bars = append(bars, zigzag(prices, 5)...)
	macd := computeMACD(bars, 12, 26, 9)
	cfg := DefaultConfig()

	pivots := []Pivot{
		{Index: 0, ZG: 12, ZD: 11, StartIndex: 2, EndIndex: 12, StartStroke: 1, EndStroke: 3},
		{Index: 1, ZG: 14, ZD: 13, StartIndex: 20, EndIndex: 35, StartStroke: 5, EndStroke: 7},
	}
	divs := detectDivergences(bars, nil, pivots, Trend{Type: TrendUp, Level: LevelDay}, macd, cfg)
	for _, div := range divs {
		if !div.NewExtreme {
			t.Fatalf("没有创新高不应输出背驰：%+v", div)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. 买卖点状态机
// ---------------------------------------------------------------------------

func TestBuy1StateMachine(t *testing.T) {
	cfg := DefaultConfig()
	// 一买/一卖的确认常常要等很久（二买成立或原中枢被破坏），窗口给宽一点；
	// "过期"用例单独用一个小窗口验证。
	cfg.ExpiryBars = 60
	bars := mkBars(80)
	// 背驰低点在 40 号 K 线；信号参考价取的是这根 K 线的 low
	bars[40].Low = 10

	buildStrokesFixture := func(upEnd float64, source []Bar) []Stroke {
		return []Stroke{
			mkStroke(0, -1, 10, 40, 25, 10, source, true),
			mkStroke(1, 1, 40, 50, 10, upEnd, source, true),
			mkStroke(2, -1, 50, 55, upEnd, 12, source, true),
		}
	}
	pivots := []Pivot{{Index: 0, ZG: 18, ZD: 12, StartIndex: 5, EndIndex: 35, StartStroke: 0, EndStroke: 0}}
	trend := Trend{Type: TrendDown, Level: LevelDay}

	div := Divergence{
		Index: 0, Kind: "trend", KindCN: "趋势背驰", Direction: "bottom",
		Level: LevelDay, PivotIndex: 0, LegBEnd: 40, Confirmed: true,
	}

	// 触发：分型已确认，但触发后还没到过期根数（数据只有 50 根）
	shortBars := mkBars(50)
	shortBars[40].Low = 10
	shortStrokes := []Stroke{
		mkStroke(0, -1, 10, 40, 25, 10, shortBars, true),
		mkStroke(1, 1, 40, 45, 10, 18, shortBars, true),
	}
	signals := buildSignals(shortBars, shortStrokes, pivots, trend, []Divergence{div}, cfg)
	buy1 := findSignal(signals, SignalBuy1)
	if buy1 == nil {
		t.Fatal("应生成一买")
	}
	if buy1.FormIndex != 40 {
		t.Fatalf("一买形成于背驰低点 40，得到 %d", buy1.FormIndex)
	}
	if buy1.State != StateTriggered {
		t.Fatalf("底分型确认后应处于「触发」，得到 %s", buy1.State)
	}
	if buy1.TriggerDate != shortBars[42].Date {
		t.Fatalf("触发时间应为分型确认日 %d，得到 %d", shortBars[42].Date, buy1.TriggerDate)
	}

	// 单笔突破前高**不再**直接确认：背驰只代表力度衰竭，不保证反转
	signals = buildSignals(bars, buildStrokesFixture(30, bars), pivots, trend, []Divergence{div}, cfg)
	buy1 = findSignal(signals, SignalBuy1)
	if buy1 == nil {
		t.Fatal("应生成一买")
	}
	if buy1.State == StateConfirmed {
		t.Fatalf("单笔突破前高不应把一买标成「确认」，得到 %s（%v）", buy1.State, buy1.Reasons)
	}

	// 原趋势最后一个中枢被反向破坏（三买结构）→ 一买确认
	reclaimStrokes := []Stroke{
		mkStroke(0, -1, 10, 40, 25, 10, bars, true),
		mkStroke(1, 1, 40, 50, 10, 20, bars, true),  // 越过 ZG=18
		mkStroke(2, -1, 50, 60, 20, 19, bars, true), // 回抽低点 19 仍在 ZG 之上
	}
	signals = buildSignals(bars, reclaimStrokes, pivots, trend, []Divergence{div}, cfg)
	buy1 = findSignal(signals, SignalBuy1)
	if buy1 == nil {
		t.Fatal("应生成一买")
	}
	if buy1.State != StateConfirmed {
		t.Fatalf("原中枢被反向破坏且回抽不回时应「确认」，得到 %s", buy1.State)
	}
	if buy1.ConfirmDate != bars[62].Date {
		t.Fatalf("确认时间应为回抽笔分型确认日 %d，得到 %d", bars[62].Date, buy1.ConfirmDate)
	}

	// 二买确认后回填到一买：一买只是候选/触发，确认要靠二买
	secondStrokes := []Stroke{
		mkStroke(0, -1, 10, 40, 25, 10, bars, true),
		mkStroke(1, 1, 40, 50, 10, 18, bars, true),
		mkStroke(2, -1, 50, 58, 18, 11, bars, true), // 回抽不破前低 10
		mkStroke(3, 1, 58, 70, 11, 19, bars, true),  // 突破回抽起点 18 → 二买确认
	}
	signals = buildSignals(bars, secondStrokes, pivots, trend, []Divergence{div}, cfg)
	buy1 = findSignal(signals, SignalBuy1)
	buy2 := findSignal(signals, SignalBuy2)
	if buy2 == nil || buy2.State != StateConfirmed {
		t.Fatalf("回抽不破前低且反向突破后应生成已确认的二买，得到 %+v（signals=%+v）", buy2, signals)
	}
	if buy1 == nil || buy1.State != StateConfirmed {
		t.Fatalf("二买确认后一买才应确认，得到 %+v", buy1)
	}
	if buy1.ConfirmDate != buy2.ConfirmDate {
		t.Fatalf("一买的确认时间应回填为二买确认日 %d，得到 %d", buy2.ConfirmDate, buy1.ConfirmDate)
	}

	// 失效：触发后跌破低点（带容差）
	invalidBars := mkBars(80)
	invalidBars[40].Low = 10
	invalidBars[45].Low = 9
	signals = buildSignals(invalidBars, buildStrokesFixture(18, invalidBars), pivots, trend,
		[]Divergence{div}, cfg)
	buy1 = findSignal(signals, SignalBuy1)
	if buy1 == nil || buy1.State != StateInvalid {
		t.Fatalf("跌破保护位应「失效」，得到 %+v", buy1)
	}

	// 过期：触发后 N 根内没有任何确认（用一个小窗口单独验证）
	expireCfg := DefaultConfig()
	expireCfg.ExpiryBars = 10
	signals = buildSignals(bars, []Stroke{mkStroke(0, -1, 10, 40, 25, 10, bars, true)}, pivots,
		trend, []Divergence{div}, expireCfg)
	buy1 = findSignal(signals, SignalBuy1)
	if buy1 == nil {
		t.Fatal("应生成一买")
	}
	if buy1.State != StateExpired {
		t.Fatalf("触发后 %d 根仍未确认应「过期」，得到 %s", expireCfg.ExpiryBars, buy1.State)
	}

	// 确认**晚于**过期窗口：仍然算过期 —— 不允许用后来的确认美化历史信号
	lateCfg := DefaultConfig()
	lateCfg.ExpiryBars = 5
	signals = buildSignals(bars, reclaimStrokes, pivots, trend, []Divergence{div}, lateCfg)
	buy1 = findSignal(signals, SignalBuy1)
	if buy1 == nil {
		t.Fatal("应生成一买")
	}
	if buy1.State != StateExpired {
		t.Fatalf("确认晚于过期窗口应判「过期」，得到 %s", buy1.State)
	}
}

func TestSecondBuyNeedsHigherLow(t *testing.T) {
	cfg := DefaultConfig()
	bars := mkBars(90)
	base := Signal{
		Type: SignalBuy1, TypeCN: "一买", Direction: "buy", Level: LevelDay,
		State: StateTriggered, StateCN: "触发", Price: 10,
		FormIndex: 40, TriggerIndex: 42,
	}
	ctx := &signalCtx{bars: bars, cfg: cfg, trend: Trend{Type: TrendDown, Level: LevelDay}}

	// 回抽低点 11 > 一买低点 10 → 成立
	ctx.strokes = []Stroke{
		mkStroke(0, -1, 10, 40, 25, 10, bars, true),
		mkStroke(1, 1, 40, 50, 10, 18, bars, true),
		mkStroke(2, -1, 50, 58, 18, 11, bars, true),
		mkStroke(3, 1, 58, 66, 11, 19, bars, true),
	}
	sig := ctx.secondFrom(base)
	if sig == nil {
		t.Fatal("回抽不破前低应生成二买")
	}
	finalizeSignal(sig, bars)
	if sig.Price != 11 {
		t.Fatalf("二买参考价应为回抽低点 11，得到 %.2f", sig.Price)
	}
	if sig.State != StateConfirmed {
		t.Fatalf("向上笔突破回抽起点后二买应确认，得到 %s", sig.State)
	}

	// 回抽跌破一买低点 → 二买不成立
	ctx.strokes = []Stroke{
		mkStroke(0, -1, 10, 40, 25, 10, bars, true),
		mkStroke(1, 1, 40, 50, 10, 18, bars, true),
		mkStroke(2, -1, 50, 58, 18, 9.5, bars, true),
	}
	if sig := ctx.secondFrom(base); sig != nil {
		t.Fatalf("回抽跌破前低不应生成二买：%+v", sig)
	}
}

func TestThirdBuyIndependent(t *testing.T) {
	cfg := DefaultConfig()
	bars := mkBars(90)
	pivot := Pivot{Index: 0, Level: LevelDay, ZG: 18, ZD: 12, EndIndex: 25, StartIndex: 5}
	ctx := &signalCtx{
		bars: bars, cfg: cfg, trend: Trend{Type: TrendUp, Level: LevelDay},
		pivots: []Pivot{pivot},
		strokes: []Stroke{
			mkStroke(0, 1, 20, 30, 15, 22, bars, true),
			mkStroke(1, -1, 31, 40, 22, 19, bars, true), // 回抽低点 19 > ZG 18
			mkStroke(2, 1, 40, 50, 19, 25, bars, true),
		},
	}
	sig := ctx.thirdFrom(pivot, true)
	if sig == nil {
		t.Fatal("回抽不回中枢应生成三买")
	}
	finalizeSignal(sig, bars)
	if sig.Type != SignalBuy3 {
		t.Fatalf("类型应为三买，得到 %s", sig.Type)
	}
	if sig.Price != 19 {
		t.Fatalf("三买参考价应为回抽低点 19，得到 %.2f", sig.Price)
	}
	if sig.State != StateConfirmed {
		t.Fatalf("三买应已确认，得到 %s", sig.State)
	}
	if sig.PivotIndex != 0 {
		t.Fatalf("三买应关联中枢 0，得到 %d", sig.PivotIndex)
	}

	// 回抽跌回中枢 → 不是三买
	ctx.strokes[1] = mkStroke(1, -1, 31, 40, 22, 17, bars, true)
	if sig := ctx.thirdFrom(pivot, true); sig != nil {
		t.Fatalf("跌回中枢不应生成三买：%+v", sig)
	}
}

// ---------------------------------------------------------------------------
// 7. 评分与排序
// ---------------------------------------------------------------------------

func TestScoreAndSorting(t *testing.T) {
	cfg := DefaultConfig()
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	trend := Trend{Type: TrendUp, Level: LevelDay}
	position := Position{Phase: PhasePullback}

	signals := []Signal{
		{Type: SignalSell3, Level: LevelDay, State: StateConfirmed, FormIndex: 40,
			Direction: "sell", TypeCN: "三卖"},
		{Type: SignalBuy3, Level: LevelDay, State: StateCandidate, FormIndex: 30,
			Direction: "buy", TypeCN: "三买"},
		{Type: SignalBuy1, Level: LevelWeek, State: StateConfirmed, FormIndex: 20,
			Direction: "buy", TypeCN: "一买"},
	}
	scored := scoreSignals(signals, trend, position, nil, cfg, len(bars)-1)

	// 方向优先：买点排在卖点之前
	if scored[0].Direction != "buy" || scored[len(scored)-1].Direction != "sell" {
		t.Fatalf("排序应把买点与卖点分开（买在前）：%+v", scored)
	}
	// 级别优先：周线一买应排在日线三买之前
	if scored[0].Type != SignalBuy1 {
		t.Fatalf("级别更高的信号应排在最前，得到 %s", scored[0].Type)
	}
	for _, sig := range scored {
		if sig.Score <= 0 || sig.Score > 100 {
			t.Fatalf("综合分应落在 0~100：%s = %.2f", sig.Type, sig.Score)
		}
		if len(sig.ScoreFactors) == 0 {
			t.Fatalf("综合分必须有可解释的分项：%s", sig.Type)
		}
	}
}

func TestConflictingSignalsGetPenalised(t *testing.T) {
	cfg := DefaultConfig()
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	signals := []Signal{
		{Type: SignalBuy3, Level: LevelDay, State: StateConfirmed, FormIndex: 10,
			Direction: "buy", TypeCN: "三买"},
		{Type: SignalSell3, Level: LevelDay, State: StateConfirmed, FormIndex: 15,
			Direction: "sell", TypeCN: "三卖"},
	}
	scored := scoreSignals(signals, Trend{Type: TrendUp, Level: LevelDay},
		Position{Phase: PhasePullback}, nil, cfg, len(bars)-1)
	if !scored[0].Conflict || !scored[1].Conflict {
		t.Fatalf("同级别反向信号应标记结构矛盾：%+v", scored)
	}
	for _, sig := range scored {
		if sig.ConflictNote == "" {
			t.Fatalf("结构矛盾必须有说明：%+v", sig)
		}
	}
}

// 结构矛盾只对"同一/相邻中枢"成立：相隔多个中枢的旧买点与新卖点
// 是先后两段结构，不能因为"都还没失效"就互判矛盾。
func TestConflictOnlyForSameStructure(t *testing.T) {
	cfg := DefaultConfig()
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24}, 6)
	base := []Signal{
		{Type: SignalBuy3, Level: LevelDay, State: StateConfirmed, Direction: "buy",
			FormIndex: 10, PivotIndex: 5, TypeCN: "三买"},
		{Type: SignalSell3, Level: LevelDay, State: StateConfirmed, Direction: "sell",
			FormIndex: 15, PivotIndex: 8, TypeCN: "三卖"},
	}
	trend := Trend{Type: TrendUp, Level: LevelDay}
	position := Position{Phase: PhasePullback}

	scored := scoreSignals(append([]Signal{}, base...), trend, position, nil, cfg, len(bars)-1)
	if scored[0].Conflict || scored[1].Conflict {
		t.Fatalf("相隔 3 个中枢的买卖点不应判结构矛盾：%+v", scored)
	}

	// 相邻中枢 → 同一段结构内出现反向结论
	adjacent := append([]Signal{}, base...)
	adjacent[1].PivotIndex = 6
	scored = scoreSignals(adjacent, trend, position, nil, cfg, len(bars)-1)
	if !scored[0].Conflict || !scored[1].Conflict {
		t.Fatalf("相邻中枢的反向信号应判矛盾：%+v", scored)
	}

	// 已经失效/过期的历史信号不参与"当前矛盾"判定
	historical := append([]Signal{}, base...)
	historical[0].State = StateExpired
	historical[0].ExpireIndex = 12
	historical[1].PivotIndex = 5
	scored = scoreSignals(historical, trend, position, nil, cfg, len(bars)-1)
	if scored[0].Conflict || scored[1].Conflict {
		t.Fatalf("历史信号不应参与结构矛盾：%+v", scored)
	}

	// 锚定同一个背驰（没有中枢下标）→ 也算同一结构
	byDivergence := []Signal{
		{Type: SignalBuy1, Level: LevelDay, State: StateConfirmed, Direction: "buy",
			FormIndex: 10, PivotIndex: -1, DivergenceIndex: 2, TypeCN: "一买"},
		{Type: SignalSell2, Level: LevelDay, State: StateConfirmed, Direction: "sell",
			FormIndex: 15, PivotIndex: -1, DivergenceIndex: 2, TypeCN: "二卖"},
	}
	scored = scoreSignals(byDivergence, trend, position, nil, cfg, len(bars)-1)
	if !scored[0].Conflict || !scored[1].Conflict {
		t.Fatalf("同一背驰派生的反向信号应判矛盾：%+v", scored)
	}
}

// ---------------------------------------------------------------------------
// 8. 整体分析 / 无未来数据
// ---------------------------------------------------------------------------

func TestAnalyzeProducesConsistentStructure(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20, 13, 21}, 6)
	cfg := DefaultConfig()
	analysis := Analyze(bars, LevelDay, cfg, AdaptInfo{Levels: 3, LevelTotal: 3})

	if analysis.Level != LevelDay || analysis.LevelCN != "日线" {
		t.Fatalf("级别元信息不正确：%+v", analysis.Level)
	}
	if analysis.Disclaimer != Disclaimer {
		t.Fatal("分析结果必须携带免责声明")
	}
	if analysis.State.Name == "" || analysis.Data.PrecisionNote == "" {
		t.Fatal("状态与精度提示不能为空")
	}
	for _, pivot := range analysis.Pivots {
		if pivot.ZG <= pivot.ZD {
			t.Fatalf("中枢上沿必须高于下沿：%+v", pivot)
		}
	}
	for _, sig := range analysis.Signals {
		if sig.Level != LevelDay {
			t.Fatalf("信号级别必须等于分析级别：%+v", sig)
		}
		if sig.FormDate == 0 || sig.StateCN == "" {
			t.Fatalf("信号缺少形成时间或状态：%+v", sig)
		}
	}
}

func TestLevelIndependence(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20, 13, 21}, 6)
	cfg := DefaultConfig()

	day := Analyze(bars, LevelDay, cfg, AdaptInfo{})
	week := Analyze(bars, LevelWeek, cfg, AdaptInfo{})

	if day.Level != LevelDay || week.Level != LevelWeek {
		t.Fatal("每个级别必须独立标注自己的级别")
	}
	// 同一份数据换级别分析，笔的数量不应完全相同（级别不是标签，是独立结构）
	if len(day.Strokes) == len(week.Strokes) && len(day.Strokes) > 0 {
		// 允许偶然相同，但中枢/笔的级别字段必须各自独立
		t.Logf("日线与周线笔数相同（%d），属可接受的偶然情况", len(day.Strokes))
	}
	for _, stroke := range week.Strokes {
		if stroke.Level != LevelWeek {
			t.Fatalf("周线笔的级别字段应为 week，得到 %s", stroke.Level)
		}
	}
}

func TestNoPivotMeansNoSignals(t *testing.T) {
	// 单调上涨：没有分型、没有笔、没有中枢
	bars := make([]Bar, 60)
	for i := range bars {
		price := 10 + float64(i)*0.3
		bars[i] = Bar{Date: 20200101 + i, Open: price, High: price + 0.1, Low: price - 0.1, Close: price + 0.05}
	}
	analysis := Analyze(bars, LevelDay, DefaultConfig(), AdaptInfo{})
	if len(analysis.Pivots) != 0 {
		t.Fatalf("单调走势不应形成中枢，得到 %d 个", len(analysis.Pivots))
	}
	if analysis.State.Name != StateNoPivot {
		t.Fatalf("无中枢状态应为 no-pivot，得到 %s", analysis.State.Name)
	}
	if len(analysis.Divergences) != 0 || len(analysis.Signals) != 0 {
		t.Fatal("无中枢不得输出背驰与买卖点")
	}
}

func TestAuditHasNoLookahead(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20, 13, 21, 10, 19}, 6)
	issues := Audit(bars, LevelDay, DefaultConfig(), AdaptInfo{})
	if len(issues) > 0 {
		t.Fatalf("审计发现用到未来数据的信号：%+v", issues)
	}
}

func TestReplayOnlyUsesPastData(t *testing.T) {
	bars := zigzag([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20}, 6)
	cfg := DefaultConfig()
	cfg.ReplayStep = 5
	replay := BuildReplay(bars, LevelDay, cfg, AdaptInfo{})
	if len(replay.Steps) == 0 {
		t.Fatal("回放应至少有一个快照")
	}
	last := -1
	for _, step := range replay.Steps {
		if step.Index <= last {
			t.Fatalf("回放下标必须严格递增：%d -> %d", last, step.Index)
		}
		last = step.Index
		for _, sig := range step.Signals {
			if sig.FormIndex > step.Index {
				t.Fatalf("回放快照里出现了未来信号：form=%d step=%d", sig.FormIndex, step.Index)
			}
		}
	}
}

func TestTrendDivergenceDetected(t *testing.T) {
	// 两段下跌：前一段陡（20 → 13.5，20 根），后一段缓（13.8 → 9.9，30 根）并创新低。
	// 这是标准的"下跌趋势背驰"形态：价格新低、力度变弱。
	points := []float64{25}
	points = interpPath(points, 20, 20)   // 0 → 20
	points = interpPath(points, 13.5, 20) // 20 → 40
	points = interpPath(points, 14.5, 10) // 40 → 50
	points = interpPath(points, 13.8, 10) // 50 → 60
	points = interpPath(points, 10.5, 15) // 60 → 75
	points = interpPath(points, 11.5, 8)  // 75 → 83
	points = interpPath(points, 9.9, 7)   // 83 → 90
	points = interpPath(points, 12, 30)   // 90 → 120
	bars := pathBars(points)

	// 相邻中枢共用端点（prev.EndIndex == next.StartIndex），正是真实数据里的情形
	pivots := []Pivot{
		{
			Index: 0, Level: LevelDay, ZG: 20, ZD: 18,
			StartIndex: 5, EndIndex: 20, StartStroke: 0, EndStroke: 0, LeaveStroke: 1,
			Confirmed: true,
		},
		{
			Index: 1, Level: LevelDay, ZG: 15, ZD: 13,
			StartIndex: 20, EndIndex: 60, StartStroke: 1, EndStroke: 3, LeaveStroke: 4,
			Confirmed: true,
		},
	}
	// 下标必须是切片里的真实位置：中枢记录的是笔的序号
	strokes := []Stroke{
		mkStroke(0, 1, 0, 5, 18.5, 20, bars, true),
		mkStroke(1, -1, 20, 40, 20, 13.5, bars, true), // 离开 P1 的下跌段
		mkStroke(2, 1, 40, 50, 13.5, 14.5, bars, true),
		mkStroke(3, -1, 50, 60, 14.5, 13.8, bars, true),
		mkStroke(4, -1, 80, 90, 11.5, 9.9, bars, true), // 后一段的极值（创新低）
		mkStroke(5, 1, 90, 110, 9.9, 12, bars, true),
	}
	macd := computeMACD(bars, 12, 26, 9)
	cfg := DefaultConfig()

	divs := detectDivergences(bars, strokes, pivots, Trend{Type: TrendDown, Level: LevelDay}, macd, cfg)
	if len(divs) != 1 {
		t.Fatalf("应检测到 1 个趋势背驰，得到 %d 个", len(divs))
	}
	div := divs[0]
	if div.Kind != "trend" || div.KindCN != "趋势背驰" {
		t.Fatalf("应为趋势背驰，得到 %s / %s", div.Kind, div.KindCN)
	}
	if div.Direction != "bottom" {
		t.Fatalf("下跌趋势应判底背驰，得到 %s", div.Direction)
	}
	if !div.NewExtreme {
		t.Fatal("后一段创新低才算趋势背驰")
	}
	if div.Ratio >= cfg.Divergence.Threshold {
		t.Fatalf("力度比 %.2f 应小于阈值 %.2f", div.Ratio, cfg.Divergence.Threshold)
	}
	if len(div.Dims) != 4 {
		t.Fatalf("力度应有 4 个维度，得到 %d", len(div.Dims))
	}
	if div.LegAEndDate == 0 || div.LegBEndDate == 0 {
		t.Fatal("背驰段必须带起止日期（前端按日期映射）")
	}
}

// interpPath 按 n 步从最后一点线性走到 target，返回追加后的路径。
func interpPath(points []float64, target float64, n int) []float64 {
	from := points[len(points)-1]
	for k := 1; k <= n; k++ {
		points = append(points, from+(target-from)*float64(k)/float64(n))
	}
	return points
}

// pathBars 把一条价格路径变成 K 线（每点一根，高低各留 0.1 的影线）。
func pathBars(points []float64) []Bar {
	bars := make([]Bar, 0, len(points))
	for i, price := range points {
		open := price
		if i > 0 {
			open = points[i-1]
		}
		bars = append(bars, Bar{
			Date:   20200101 + i,
			Open:   open,
			High:   math.Max(open, price) + 0.1,
			Low:    math.Min(open, price) - 0.1,
			Close:  price,
			Volume: 1000,
			Amount: 1e8,
		})
	}
	return bars
}

func TestLimitSignalsKeepsActive(t *testing.T) {
	// 候选/触发永远保留；确认/失效/过期按形成时间保留最近的 N 条
	signals := []Signal{
		{Type: SignalBuy3, State: StateCandidate, FormIndex: 5},
		{Type: SignalBuy3, State: StateConfirmed, FormIndex: 10},
		{Type: SignalBuy3, State: StateInvalid, FormIndex: 50},
		{Type: SignalBuy3, State: StateInvalid, FormIndex: 40},
		{Type: SignalBuy3, State: StateInvalid, FormIndex: 30},
		{Type: SignalSell3, State: StateExpired, FormIndex: 20},
	}
	out := limitSignals(signals, 2)

	forms := make([]int, 0, len(out))
	for _, sig := range out {
		forms = append(forms, sig.FormIndex)
	}
	// 候选 5 + 最近两条三买历史 50/40 + 三卖历史 20
	want := []int{5, 50, 40, 20}
	if len(forms) != len(want) {
		t.Fatalf("应保留 %v，得到 %v", want, forms)
	}
	for i := range want {
		if forms[i] != want[i] {
			t.Fatalf("应保留 %v，得到 %v", want, forms)
		}
	}
}

func TestDedupeSignals(t *testing.T) {
	signals := []Signal{
		{Type: SignalBuy3, FormIndex: 10, TriggerIndex: 12},
		{Type: SignalBuy3, FormIndex: 10, TriggerIndex: 12}, // 重叠中枢导致的重复
		{Type: SignalBuy3, FormIndex: 10, TriggerIndex: 15},
		{Type: SignalSell3, FormIndex: 10, TriggerIndex: 12},
	}
	out := dedupeSignals(signals)
	if len(out) != 3 {
		t.Fatalf("同类型同形成同触发应去重，得到 %d 条", len(out))
	}
}

func TestConfigSanitize(t *testing.T) {
	cfg := Config{
		MinLevel:  "bogus",
		OperLevel: "bogus",
		Divergence: DivergenceConfig{
			StructureWeight: -1,
			MacdWeight:      0,
			SlopeWeight:     3,
			Threshold:       2,
			MinLegBars:      0,
		},
		TypeWeights: []TypeWeight{{Type: "unknown", Weight: 5}},
		MinBars:     0,
	}
	sanitized := cfg.Sanitize()
	if sanitized.MinLevel != LevelDayStroke || sanitized.OperLevel != LevelDay {
		t.Fatalf("非法级别应回落到默认：%+v", sanitized)
	}
	if sanitized.Divergence.Threshold < 0.1 || sanitized.Divergence.Threshold > 0.99 {
		t.Fatalf("背驰阈值越界：%v", sanitized.Divergence.Threshold)
	}
	total := sanitized.Divergence.StructureWeight + sanitized.Divergence.MacdWeight +
		sanitized.Divergence.SlopeWeight + sanitized.Divergence.VolumeWeight
	if total <= 0 {
		t.Fatal("力度权重不能全为 0")
	}
	if len(sanitized.TypeWeights) != 6 {
		t.Fatalf("类型权重应补全 6 条，得到 %d", len(sanitized.TypeWeights))
	}
	if sanitized.MinBars < 10 {
		t.Fatalf("MinBars 应有下限，得到 %d", sanitized.MinBars)
	}
}

// findSignal 取第一条指定类型的信号。
func findSignal(signals []Signal, t SignalType) *Signal {
	for i := range signals {
		if signals[i].Type == t {
			return &signals[i]
		}
	}
	return nil
}
