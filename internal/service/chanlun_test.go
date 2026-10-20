package service

import (
	"bytes"
	"context"
	"math"
	"testing"

	"freestock/internal/chanlun"
)

// 缠论服务层的单元测试：只覆盖"不依赖网络/数据湖"的部分。
//
// 重点是有两件容易写错、后果又很严重的事：
//  1. **开关关闭时**必须一个分支都不进 —— 否则一次改动就可能悄悄改变所有选股结果；
//  2. 周期聚合口径必须与前端 `aggregate()` 一致（ISO 周 / 自然月 / 自然年）。

// zigzagChanlunBars 生成锯齿日 K，供结构分析用。
func zigzagChanlunBars(points []float64, segment int) []chanlun.Bar {
	bars := make([]chanlun.Bar, 0, (len(points)-1)*segment)
	date := 20260101
	price := points[0]
	for i := 1; i < len(points); i++ {
		target := points[i]
		for k := 0; k < segment; k++ {
			next := price + (target-price)*float64(k+1)/float64(segment)
			bars = append(bars, chanlun.Bar{
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

func TestChanlunFilterDisabledDoesNothing(t *testing.T) {
	svc := &StockService{}
	passed := []StockItem{{Code: "600519", Name: "贵州茅台"}, {Code: "000001", Name: "平安银行"}}
	cond := ScreenCondition{} // Chanlun.Enable 默认 false

	// provider 传 nil：开关关闭时**不允许**碰到取数逻辑，传 nil 正好能证明这一点
	kept, hits, notes := svc.applyChanlunFilter(context.Background(), nil, passed, cond, 20260101)
	if len(kept) != len(passed) {
		t.Fatalf("关闭缠论筛选时结果必须原样返回：期望 %d 只，得到 %d 只", len(passed), len(kept))
	}
	if len(hits) != 0 {
		t.Fatalf("关闭时不应产生缠论命中信息，得到 %d 条", len(hits))
	}
	if len(notes) != 0 {
		t.Fatalf("关闭时不应追加任何提示，得到 %v", notes)
	}
}

func TestAggregateChanlunBarsMatchesFrontend(t *testing.T) {
	bars := []chanlun.Bar{
		{Date: 20260105, Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100, Amount: 1000},
		{Date: 20260106, Open: 10.5, High: 12, Low: 10, Close: 11, Volume: 200, Amount: 2000},
		{Date: 20260107, Open: 11, High: 11.5, Low: 8.5, Close: 9, Volume: 300, Amount: 3000},
	}

	// 日线原样返回
	if got := aggregateChanlunBars(bars, "day"); len(got) != 3 {
		t.Fatalf("日线不应聚合，得到 %d 根", len(got))
	}

	// 周线：开盘取首、最高取最大、最低取最小、收盘取末、量额求和、日期取最后一个交易日
	weekly := aggregateChanlunBars(bars, "week")
	if len(weekly) != 1 {
		t.Fatalf("同一 ISO 周应聚成 1 根，得到 %d 根", len(weekly))
	}
	week := weekly[0]
	if week.Open != 10 || week.High != 12 || week.Low != 8.5 || week.Close != 9 {
		t.Fatalf("周线 OHLC 不正确：%+v", week)
	}
	if week.Volume != 600 || week.Amount != 6000 {
		t.Fatalf("周线量额应求和：%+v", week)
	}
	if week.Date != 20260107 {
		t.Fatalf("周线日期应取该周最后一个交易日 20260107，得到 %d", week.Date)
	}

	// 月线：同一自然月一根
	monthly := aggregateChanlunBars(bars, "month")
	if len(monthly) != 1 || monthly[0].Date != 20260107 {
		t.Fatalf("月线聚合不正确：%+v", monthly)
	}

	// 年线
	yearly := aggregateChanlunBars(bars, "year")
	if len(yearly) != 1 {
		t.Fatalf("年线聚合不正确：%+v", yearly)
	}
}

func TestChanlunPeriodKeyISOWeek(t *testing.T) {
	// 2026-01-05 是周一，2026-01-11 是周日：同一 ISO 周
	// （2026 年第 2 周，ISO 年仍是 2026）
	first := chanlunPeriodKey(20260105, "week")
	second := chanlunPeriodKey(20260111, "week")
	if first != second {
		t.Fatalf("同一 ISO 周应得到相同键：%q vs %q", first, second)
	}
	// 下周一必须换周
	if chanlunPeriodKey(20260112, "week") == first {
		t.Fatal("跨周应换键")
	}
	// 月 / 年键
	if chanlunPeriodKey(20260131, "month") != "2026-01" {
		t.Fatalf("月键不正确：%q", chanlunPeriodKey(20260131, "month"))
	}
	if chanlunPeriodKey(20260131, "year") != "2026" {
		t.Fatalf("年键不正确：%q", chanlunPeriodKey(20260131, "year"))
	}
}

func TestEvaluateChanlunStockProducesHit(t *testing.T) {
	bars := zigzagChanlunBars([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20, 13, 21}, 6)
	cfg := chanlun.DefaultConfig()
	info := chanlun.AdaptInfo{Levels: 1, LevelTotal: 5, FloatMV: 5e10, Amount: 1e9}

	filter := normalizeChanlunScreenCondition(ChanlunScreenCondition{
		Enable:    true,
		OperLevel: string(chanlun.LevelDay),
	}, cfg)

	hit := evaluateChanlunStock(bars, chanlun.LevelDay, cfg, filter, info)
	if hit == nil {
		t.Fatal("不设额外阈值时应当命中")
	}
	if hit.Level != string(chanlun.LevelDay) || hit.LevelCN == "" {
		t.Fatalf("命中的级别信息不完整：%+v", hit)
	}
	if hit.Risk != chanlun.Disclaimer {
		t.Fatalf("命中结果必须带风险提示，得到 %q", hit.Risk)
	}
	if hit.Precision == "" {
		t.Fatal("命中结果必须带数据精度提示")
	}
	if hit.AdaptGrade == "" || hit.Adaptability <= 0 {
		t.Fatalf("适配性评分缺失：%+v", hit)
	}
}

func TestEvaluateChanlunStockRespectsFilters(t *testing.T) {
	bars := zigzagChanlunBars([]float64{10, 20, 12, 22, 14, 24, 9, 18, 11, 20, 13, 21}, 6)
	cfg := chanlun.DefaultConfig()
	info := chanlun.AdaptInfo{Levels: 1, LevelTotal: 5, FloatMV: 5e10, Amount: 1e9}

	base := ChanlunScreenCondition{Enable: true, OperLevel: string(chanlun.LevelDay)}

	// 适配性阈值超过 100：任何标的都不可能命中
	impossible := normalizeChanlunScreenCondition(base, cfg)
	impossible.MinAdaptability = 101
	if hit := evaluateChanlunStock(bars, chanlun.LevelDay, cfg, impossible, info); hit != nil {
		t.Fatalf("适配性阈值超过 100 不应命中：%+v", hit)
	}

	// 指定一个几乎不会出现的类型组合 + 综合分阈值 100
	strict := normalizeChanlunScreenCondition(base, cfg)
	strict.Types = []string{"buy3"}
	strict.MinScore = 100
	if hit := evaluateChanlunStock(bars, chanlun.LevelDay, cfg, strict, info); hit != nil {
		t.Fatalf("综合分阈值 100 不应命中：%+v", hit)
	}

	// 无中枢状态：这段锯齿数据一旦形成中枢就不是 none，用 none 过滤应当不命中
	noPivotFilter := normalizeChanlunScreenCondition(base, cfg)
	noPivotFilter.Trends = []string{string(chanlun.TrendNone)}
	analysis := chanlun.Analyze(bars, chanlun.LevelDay, cfg, info)
	if len(analysis.Pivots) > 0 {
		if hit := evaluateChanlunStock(bars, chanlun.LevelDay, cfg, noPivotFilter, info); hit != nil {
			t.Fatalf("已有中枢时按「无中枢」过滤不应命中：%+v", hit)
		}
	}
}

func TestNormalizeChanlunScreenConditionDefaults(t *testing.T) {
	cfg := chanlun.DefaultConfig()
	filter := normalizeChanlunScreenCondition(ChanlunScreenCondition{Enable: true}, cfg)

	if filter.OperLevel != string(cfg.OperLevel) {
		t.Fatalf("非法/空级别应回落到配置的操作级别，得到 %q", filter.OperLevel)
	}
	if filter.LookbackDays != 500 {
		t.Fatalf("默认窗口应为 500 根，得到 %d", filter.LookbackDays)
	}
	if !filter.AllowCandidate {
		t.Fatal("默认应允许候选信号")
	}

	// 只看已确认时，候选开关不应被强制打开
	confirmedOnly := normalizeChanlunScreenCondition(
		ChanlunScreenCondition{Enable: true, ConfirmedOnly: true, AllowCandidate: false}, cfg)
	if confirmedOnly.AllowCandidate {
		t.Fatal("ConfirmedOnly 时不应强制允许候选")
	}
}

func TestChanlunScreenLookbackByLevel(t *testing.T) {
	cases := map[chanlun.Level]int{
		chanlun.LevelDay:   500,
		chanlun.LevelWeek:  800,
		chanlun.LevelMonth: 1600,
		chanlun.LevelYear:  1600,
	}
	for level, want := range cases {
		if got := chanlunScreenLookback(level, 0); got != want {
			t.Fatalf("%s 级别的取数窗口应为 %d 根，得到 %d", level, want, got)
		}
	}
	// 用户显式给更大的窗口时以用户为准
	if got := chanlunScreenLookback(chanlun.LevelDay, 2000); got != 2000 {
		t.Fatalf("用户指定更大窗口时应以用户为准，得到 %d", got)
	}
}

// 信号新鲜度：15 个月前确认、之后一直没失效的三买，不是"当前机会"。
func TestSignalFresh(t *testing.T) {
	// 最新 K 线下标 400，确认发生在 100 根之前
	old := chanlun.Signal{State: chanlun.StateConfirmed, FormIndex: 280, TriggerIndex: 290, ConfirmIndex: 300}
	if signalFresh(old, 400, 0) != true {
		t.Fatal("0 应表示不限")
	}
	if signalFresh(old, 400, 60) {
		t.Fatal("100 根前确认的信号不应算新鲜")
	}
	if !signalFresh(old, 400, 120) {
		t.Fatal("120 根窗口内应算新鲜")
	}

	// 同一信号：确认很旧，但触发刚发生（状态为触发）→ 按触发算，是新鲜的
	freshTrigger := chanlun.Signal{State: chanlun.StateTriggered, FormIndex: 280, TriggerIndex: 395, ConfirmIndex: -1}
	if !signalFresh(freshTrigger, 400, 60) {
		t.Fatal("刚触发的信号应按触发日算新鲜")
	}

	// 候选还没触发，按形成日算
	candidate := chanlun.Signal{State: chanlun.StateCandidate, FormIndex: 300, TriggerIndex: -1, ConfirmIndex: -1}
	if signalFresh(candidate, 400, 60) {
		t.Fatal("候选按形成日算，100 根前已不新鲜")
	}
}

// 旧配置没有 maxSignalAge 字段时应补默认窗口；但 0 是用户显式的"不限"，不能被覆盖。
func TestScreenConditionMaxSignalAgeMigration(t *testing.T) {
	defaults := defaultChanlunScreenCondition()
	if defaults.MaxSignalAge != 60 {
		t.Fatalf("默认新鲜度窗口应为 60，得到 %d", defaults.MaxSignalAge)
	}
	// 这里只验证"缺字段"的判据本身：旧 raw 不含该键
	if bytesContainsKey([]byte(`{"chanlun":{"enable":true,"lookbackDays":500}}`), "maxSignalAge") {
		t.Fatal("旧配置不应被识别为包含 maxSignalAge")
	}
	if !bytesContainsKey([]byte(`{"chanlun":{"enable":true,"maxSignalAge":0}}`), "maxSignalAge") {
		t.Fatal("显式写 0 的配置应被识别为包含 maxSignalAge")
	}
}

// bytesContainsKey 与 GetScreenCondition 里的迁移判据保持一致。
func bytesContainsKey(raw []byte, key string) bool {
	return bytes.Contains(raw, []byte(`"`+key+`"`))
}
