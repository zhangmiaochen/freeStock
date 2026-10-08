package service

import (
	"strings"
	"testing"

	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 快照层：纯函数，用构造数据精确验证
// ---------------------------------------------------------------------------

func snapshotStock(code, name, market string, overrides func(*StockItem)) StockItem {
	item := StockItem{
		Code:     code,
		Name:     name,
		Market:   market,
		Close:    10,
		PctChg:   1,
		Amount:   1e8,
		Turnover: 2,
		PeTTM:    20,
		PB:       2,
		TotalMV:  100e8, // 100 亿
		FloatMV:  80e8,
		VolRatio: 1.5,
		HasQuote: true,
	}
	if overrides != nil {
		overrides(&item)
	}
	return item
}

func TestScreenMarketCapRange(t *testing.T) {
	cond := ScreenCondition{MinTotalMV: 50, MaxTotalMV: 200}

	if !matchSnapshot(snapshotStock("A", "甲", "sh-main", nil), cond) {
		t.Error("100 亿应落在 50~200 亿区间内")
	}
	small := snapshotStock("B", "乙", "sh-main", func(s *StockItem) { s.TotalMV = 10e8 })
	if matchSnapshot(small, cond) {
		t.Error("10 亿不该通过 50 亿下限")
	}
	big := snapshotStock("C", "丙", "sh-main", func(s *StockItem) { s.TotalMV = 900e8 })
	if matchSnapshot(big, cond) {
		t.Error("900 亿不该通过 200 亿上限")
	}
}

// 字段缺失（值为 0）时应跳过该条件，而不是判为"不符合" ——
// 否则会把"数据没同步"误判成"不满足条件"，结果凭空少一批股票。
func TestScreenSkipsConditionWhenDataMissing(t *testing.T) {
	cond := ScreenCondition{MinPE: 10, MaxPE: 30}

	noPE := snapshotStock("A", "甲", "sh-main", func(s *StockItem) { s.PeTTM = 0 })
	if !matchSnapshot(noPE, cond) {
		t.Error("PE 缺失时应跳过 PE 条件，而不是排除")
	}

	// 但其它字段不缺失时仍要正常判定
	outOfRange := snapshotStock("B", "乙", "sh-main", func(s *StockItem) { s.PeTTM = 99 })
	if matchSnapshot(outOfRange, cond) {
		t.Error("PE=99 应被 30 的上限排除")
	}
}

func TestScreenExcludeST(t *testing.T) {
	cond := ScreenCondition{ExcludeST: true}

	if matchSnapshot(snapshotStock("A", "ST某某", "sh-main", nil), cond) {
		t.Error("名称含 ST 应被排除")
	}
	if matchSnapshot(snapshotStock("B", "*ST退市", "sh-main", nil), cond) {
		t.Error("名称含 *ST 应被排除")
	}
	// IsST 标记也要认（名称可能还没改）
	flagged := snapshotStock("C", "某某股份", "sh-main", func(s *StockItem) { s.IsST = true })
	if matchSnapshot(flagged, cond) {
		t.Error("IsST 标记为真应被排除")
	}
	if !matchSnapshot(snapshotStock("D", "某某股份", "sh-main", nil), cond) {
		t.Error("普通股票不该被排除")
	}

	// 关掉开关后 ST 也要放行
	if !matchSnapshot(snapshotStock("E", "ST某某", "sh-main", nil), ScreenCondition{}) {
		t.Error("未启用排除 ST 时不该过滤")
	}
}

func TestScreenMarkets(t *testing.T) {
	cond := ScreenCondition{Markets: []string{"sh-main", "sz-main"}}

	if !matchSnapshot(snapshotStock("A", "甲", "sh-main", nil), cond) {
		t.Error("沪市主板应通过")
	}
	if !matchSnapshot(snapshotStock("B", "乙", "sz-main", nil), cond) {
		t.Error("深市主板应通过（多选是并集）")
	}
	if matchSnapshot(snapshotStock("C", "丙", "chinext", nil), cond) {
		t.Error("创业板不在选择范围内")
	}
	// 不选市场 = 全部
	if !matchSnapshot(snapshotStock("D", "丁", "bse", nil), ScreenCondition{}) {
		t.Error("未选市场时不该过滤")
	}
}

func TestScreenVolumeRatioAndAmount(t *testing.T) {
	cond := ScreenCondition{MinVolumeRatio: 1.2, MinAmount: 0.5} // 成交额单位亿元

	if !matchSnapshot(snapshotStock("A", "甲", "sh-main", nil), cond) {
		t.Error("量比 1.5、成交额 1 亿应通过")
	}
	quiet := snapshotStock("B", "乙", "sh-main", func(s *StockItem) { s.VolRatio = 0.8 })
	if matchSnapshot(quiet, cond) {
		t.Error("量比 0.8 应被下限排除")
	}
	thin := snapshotStock("C", "丙", "sh-main", func(s *StockItem) { s.Amount = 1e7 })
	if matchSnapshot(thin, cond) {
		t.Error("成交额 0.1 亿应被下限排除")
	}
}

// ROE 由 PB ÷ PE 推导（TTM 口径），测试这个换算是否被真正接进筛选。
func TestScreenDerivedROE(t *testing.T) {
	// PB 3 / PE 20 = 15%
	item := snapshotStock("A", "甲", "sh-main", func(s *StockItem) { s.PB = 3; s.PeTTM = 20 })
	if got := derivedROE(item); got != 15 {
		t.Errorf("ROE 推导 = %v，期望 15", got)
	}

	cond := ScreenCondition{MinROE: 12}
	if !matchSnapshot(item, cond) {
		t.Error("ROE 15% 应通过 12% 下限")
	}
	weak := snapshotStock("B", "乙", "sh-main", func(s *StockItem) { s.PB = 1; s.PeTTM = 50 })
	if matchSnapshot(weak, cond) {
		t.Error("ROE 2% 不该通过 12% 下限")
	}

	// 亏损股（PE 为负）推导出负 ROE，应被下限排除 —— 这个结果是正确的
	loss := snapshotStock("C", "丙", "sh-main", func(s *StockItem) { s.PB = 1.3; s.PeTTM = -83 })
	if matchSnapshot(loss, cond) {
		t.Error("亏损股不该通过 ROE 下限")
	}
}

func TestWithinSemantics(t *testing.T) {
	// 两端都为 0 = 不限制
	if !within(123, 0, 0, false) {
		t.Error("两端为 0 应表示不限制")
	}
	// 单边限制
	if !within(50, 10, 0, false) || within(5, 10, 0, false) {
		t.Error("只有下限时判定错误")
	}
	if !within(5, 0, 10, false) || within(50, 0, 10, false) {
		t.Error("只有上限时判定错误")
	}
	// 跳过标志
	if !within(999, 10, 20, true) {
		t.Error("skip 为真时应放行")
	}
	// 负值区间（涨跌幅可能为负）
	if !within(-3, -5, -2, false) || within(-1, -5, -2, false) {
		t.Error("负数区间判定错误")
	}
}

// ---------------------------------------------------------------------------
// 技术层：纯函数
// ---------------------------------------------------------------------------

func quotesFromCloses(closes []float64) []*stockdb.Quote {
	out := make([]*stockdb.Quote, 0, len(closes))
	for i, close := range closes {
		out = append(out, &stockdb.Quote{Close: close, High: close * 1.05, Low: close * 0.98, Date: int64(20260101 + i)})
	}
	return out
}

func TestMovingAverage(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	got, ok := movingAverage(closes, 5)
	if !ok || got != 8 { // (6+7+8+9+10)/5
		t.Errorf("MA5 = %v (ok=%v)，期望 8", got, ok)
	}
	// 数据不足
	if _, ok := movingAverage(closes, 20); ok {
		t.Error("数据不足时应返回 false")
	}
	if _, ok := movingAverage(closes, 0); ok {
		t.Error("周期为 0 应返回 false")
	}
}

func TestEvaluateHistoryMAConditions(t *testing.T) {
	// 单调上涨：短期均线一定在长期之上
	rising := quotesFromCloses([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})

	cond := ScreenCondition{MAConditions: []MACondition{{Fast: 5, Op: "gt", Slow: 10}}}
	if _, ok := evaluateHistory(rising, cond); !ok {
		t.Error("单调上涨时 MA5 应大于 MA10")
	}

	ltCond := ScreenCondition{MAConditions: []MACondition{{Fast: 5, Op: "lt", Slow: 10}}}
	if _, ok := evaluateHistory(rising, ltCond); ok {
		t.Error("单调上涨时 MA5 不该小于 MA10")
	}

	// 单调下跌：反过来
	falling := quotesFromCloses([]float64{12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})
	if _, ok := evaluateHistory(falling, cond); ok {
		t.Error("单调下跌时 MA5 不该大于 MA10")
	}
	if _, ok := evaluateHistory(falling, ltCond); !ok {
		t.Error("单调下跌时 MA5 应小于 MA10")
	}
}

// 多条均线条件之间是 AND（与原项目一致）。
func TestEvaluateHistoryMAMultipleAreAnd(t *testing.T) {
	rising := quotesFromCloses([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21})

	both := ScreenCondition{MAConditions: []MACondition{
		{Fast: 5, Op: "gt", Slow: 10},
		{Fast: 10, Op: "gt", Slow: 20},
	}}
	if _, ok := evaluateHistory(rising, both); !ok {
		t.Error("多头排列时两条都该满足")
	}

	// 一条不可能满足的条件会让整体失败
	impossible := ScreenCondition{MAConditions: []MACondition{
		{Fast: 5, Op: "gt", Slow: 10},
		{Fast: 5, Op: "lt", Slow: 10},
	}}
	if _, ok := evaluateHistory(rising, impossible); ok {
		t.Error("互相矛盾的两条条件不该同时满足")
	}
}

// 数据不足以计算均线（新股）时应判为不满足，而不是放行 ——
// 均线条件是形态判断，算不出来就不该算命中。
func TestEvaluateHistoryMAInsufficientData(t *testing.T) {
	short := quotesFromCloses([]float64{1, 2, 3})
	cond := ScreenCondition{MAConditions: []MACondition{{Fast: 5, Op: "gt", Slow: 20}}}

	if _, ok := evaluateHistory(short, cond); ok {
		t.Error("数据不足以算 MA20 时不该算命中")
	}
}

func TestEvaluateHistoryDrawdown(t *testing.T) {
	// 最高 100，最新 80 → 回撤 20%
	closes := []float64{50, 60, 70, 80, 90, 100, 95, 90, 85, 80}
	quotes := make([]*stockdb.Quote, 0, len(closes))
	for i, close := range closes {
		quotes = append(quotes, &stockdb.Quote{Close: close, High: close, Date: int64(20260101 + i)})
	}

	cond := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10}
	tech, ok := evaluateHistory(quotes, cond)
	if !ok {
		t.Fatal("应通过（只启用回撤、未限区间）")
	}
	if tech.drawdown != 20 {
		t.Errorf("回撤 = %v，期望 20", tech.drawdown)
	}

	// 区间过滤
	inRange := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MinDrawdown: 15, MaxDrawdown: 25}
	if _, ok := evaluateHistory(quotes, inRange); !ok {
		t.Error("20% 应落在 15~25% 区间内")
	}
	tooSmall := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MinDrawdown: 30}
	if _, ok := evaluateHistory(quotes, tooSmall); ok {
		t.Error("20% 不该通过 30% 下限")
	}
	tooBig := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MaxDrawdown: 10}
	if _, ok := evaluateHistory(quotes, tooBig); ok {
		t.Error("20% 不该通过 10% 上限")
	}
}

// 窗口必须只回看最近 N 个交易日，不能把很久以前的高点算进来。
func TestEvaluateHistoryDrawdownWindow(t *testing.T) {
	// 很久以前有个 1000 的高点，最近 10 天在 100 附近横盘
	closes := append([]float64{1000, 900, 800}, make([]float64, 10)...)
	for i := 3; i < len(closes); i++ {
		closes[i] = 100
	}
	quotes := make([]*stockdb.Quote, 0, len(closes))
	for i, close := range closes {
		quotes = append(quotes, &stockdb.Quote{Close: close, High: close, Date: int64(20260101 + i)})
	}

	tech, ok := evaluateHistory(quotes, ScreenCondition{EnableDrawdown: true, DrawdownDays: 8})
	if !ok {
		t.Fatal("应通过")
	}
	if tech.drawdown != 0 {
		t.Errorf("窗口内应该没有回撤（都是 100），实际 %v —— 说明老高点被算进来了", tech.drawdown)
	}
}

// ---------------------------------------------------------------------------
// 板块层：需要真实板块数据
// ---------------------------------------------------------------------------

func TestScreenBoardFilterUsesRealBoards(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 单板块
	single, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"}},
	})
	if err != nil {
		t.Fatalf("筛选失败: %v", err)
	}
	if single.Matched < 100 {
		t.Fatalf("医药生物成分数偏少: %d", single.Matched)
	}
	for _, item := range single.Items {
		found := false
		for _, name := range item.Boards {
			if name == "医药生物" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s 不属于医药生物却被选出", item.Code)
		}
	}

	// 多板块「任一」= 并集，结果应明显更多
	union, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物", "银行"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("筛选失败: %v", err)
	}
	if union.Matched <= single.Matched {
		t.Errorf("并集(%d)应多于单板块(%d)", union.Matched, single.Matched)
	}

	t.Logf("医药生物 %d 只，加上银行后 %d 只", single.Matched, union.Matched)
}

// 多条板块条件之间是 AND：结果必须同时属于两个板块。
func TestScreenBoardConditionsAreIntersection(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	industry, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"}},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	combined, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"},
			{Category: "概念", Names: []string{"创新药"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if combined.Matched >= industry.Matched {
		t.Errorf("两个板块求交(%d)应少于单看行业(%d)", combined.Matched, industry.Matched)
	}
	if combined.Matched == 0 {
		t.Error("医药生物 ∩ 创新药 不该为空")
	}
	for _, item := range combined.Items {
		hasIndustry, hasConcept := false, false
		for _, name := range item.Boards {
			if name == "医药生物" {
				hasIndustry = true
			}
			if name == "创新药" {
				hasConcept = true
			}
		}
		if !hasIndustry || !hasConcept {
			t.Errorf("%s 没有同时命中两个板块: %v", item.Code, item.Boards)
		}
	}
	t.Logf("医药生物 %d 只 → 再叠加创新药概念 %d 只", industry.Matched, combined.Matched)
}

// 同一条条件里 mode=all 是交集。
func TestScreenBoardModeAll(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	any, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物", "银行"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	all, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物", "银行"}, Mode: "all"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if all.Matched >= any.Matched {
		t.Errorf("「全部」(%d)应少于「任一」(%d)", all.Matched, any.Matched)
	}
	// 一只股票不可能同时是医药生物和银行（同一个层级分类内互斥），所以应该是 0
	if all.Matched != 0 {
		t.Errorf("同一分类下两个互斥板块求交应为 0，实际 %d", all.Matched)
	}
}

// 板块名写错时必须明确提示，而不是静默返回空结果。
func TestScreenBoardMissingNameIsReported(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	result, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"这个板块不存在"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(result.Notes) == 0 {
		t.Fatal("板块名找不到时必须给出提示")
	}
	found := false
	for _, note := range result.Notes {
		if strings.Contains(note, "这个板块不存在") {
			found = true
		}
	}
	if !found {
		t.Errorf("提示里应包含写错的板块名，实际: %v", result.Notes)
	}
	if result.Matched != 0 {
		t.Errorf("板块不存在时不该有结果，实际 %d", result.Matched)
	}
}

// ---------------------------------------------------------------------------
// 端到端
// ---------------------------------------------------------------------------

func TestScreenStocksEndToEnd(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 空条件 = 全市场
	all, err := svc.ScreenStocks(ScreenCondition{})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if all.Scanned == 0 || all.Matched != all.Scanned {
		t.Errorf("空条件应返回全部: scanned=%d matched=%d", all.Scanned, all.Matched)
	}

	// 逐层收窄
	result, err := svc.ScreenStocks(ScreenCondition{
		MinTotalMV: 100, MaxPE: 30, MinROE: 10,
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if result.SnapshotPassed > result.Scanned {
		t.Error("快照层通过数不该超过扫描数")
	}
	if result.BoardPassed > result.SnapshotPassed {
		t.Error("板块层通过数不该超过快照层")
	}
	if result.Matched != result.BoardPassed {
		t.Errorf("未启用技术条件时命中数应等于板块层通过数: %d vs %d", result.Matched, result.BoardPassed)
	}

	// 结果要按代码升序（与原项目一致）
	for i := 1; i < len(result.Items); i++ {
		if result.Items[i].Code < result.Items[i-1].Code {
			t.Fatalf("结果未按代码升序: %s 在 %s 之后", result.Items[i].Code, result.Items[i-1].Code)
		}
	}

	// 每一项都要真的满足市值下限
	for _, item := range result.Items {
		if item.TotalMV/1e8 < 100 {
			t.Errorf("%s 市值 %.1f 亿低于下限", item.Code, item.TotalMV/1e8)
		}
	}

	t.Logf("扫描 %d → 快照 %d → 板块 %d，耗时 %s",
		result.Scanned, result.SnapshotPassed, result.BoardPassed, result.Elapsed)
	for i := 0; i < 3 && i < len(result.Items); i++ {
		item := result.Items[i]
		t.Logf("  %s %s PE=%.2f PB=%.2f ROE=%.2f%% 市值=%.1f亿 板块=%v",
			item.Code, item.Name, item.PeTTM, item.PB, item.ROE, item.TotalMV/1e8, item.Boards)
	}
}

func TestScreenStocksLimit(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	result, err := svc.ScreenStocks(ScreenCondition{Limit: 10})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(result.Items) != 10 {
		t.Errorf("limit 未生效: %d", len(result.Items))
	}
	// Matched 必须是截断前的真实命中数
	if result.Matched != result.Scanned {
		t.Errorf("Matched 应为截断前的数量: %d vs %d", result.Matched, result.Scanned)
	}
}

func TestScreenHistoryConditionsEndToEnd(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	base := ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"银行"}, Mode: "any"},
		},
	}
	plain, err := svc.ScreenStocks(base)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	withMA := base
	withMA.MAConditions = []MACondition{{Fast: 5, Op: "gt", Slow: 20}}
	ma, err := svc.ScreenStocks(withMA)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if !ma.HistoryEvaluated {
		t.Error("启用了均线条件，HistoryEvaluated 应为真")
	}
	if ma.Matched > plain.Matched {
		t.Errorf("加了技术条件后不该变多: %d → %d", plain.Matched, ma.Matched)
	}
	// 均线值要回填，供界面展示
	for _, item := range ma.Items {
		if len(item.MAValues) == 0 {
			t.Errorf("%s 启用了均线条件但没有回填均线值", item.Code)
			break
		}
	}

	t.Logf("银行 %d 只 → 其中 MA5>MA20 的 %d 只（耗时 %s）",
		plain.Matched, ma.Matched, ma.Elapsed)
}

// 条件要能写进配置文件并读回来。
func TestScreenConditionPersistence(t *testing.T) {
	svc, _ := newIsolatedService(t)

	if info := svc.GetScreenCondition(); info.Saved {
		t.Error("全新配置不该报告已保存")
	} else if !info.Condition.ExcludeST {
		t.Error("默认条件应默认排除 ST")
	}

	want := ScreenCondition{
		MinTotalMV: 50, MaxPE: 30, MinROE: 12,
		ExcludeST: true, HasQuoteOnly: true,
		Markets: []string{"sh-main"},
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物", "银行"}, Mode: "any"},
		},
		MAConditions:   []MACondition{{Fast: 5, Op: "gt", Slow: 20}},
		EnableDrawdown: true, DrawdownDays: 20, MinDrawdown: 5,
	}
	if err := svc.SaveScreenCondition(want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got := svc.GetScreenCondition()
	if !got.Saved {
		t.Fatal("应报告已保存")
	}
	check := got.Condition
	if check.MinTotalMV != 50 || check.MaxPE != 30 || check.MinROE != 12 {
		t.Errorf("数值条件没读回: %+v", check)
	}
	if len(check.Markets) != 1 || check.Markets[0] != "sh-main" {
		t.Errorf("市场没读回: %v", check.Markets)
	}
	if len(check.BoardFilters) != 1 || len(check.BoardFilters[0].Names) != 2 {
		t.Fatalf("板块条件没读回: %+v", check.BoardFilters)
	}
	if check.BoardFilters[0].Names[0] != "医药生物" || check.BoardFilters[0].Mode != "any" {
		t.Errorf("板块条件内容不对: %+v", check.BoardFilters[0])
	}
	if len(check.MAConditions) != 1 || check.MAConditions[0].Slow != 20 {
		t.Errorf("均线条件没读回: %+v", check.MAConditions)
	}
	if !check.EnableDrawdown || check.DrawdownDays != 20 || check.MinDrawdown != 5 {
		t.Errorf("回撤条件没读回: %+v", check)
	}
}

func TestGetScreenOptions(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	options, err := svc.GetScreenOptions()
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(options.Categories) == 0 {
		t.Error("应返回可用的板块分类")
	}
	if len(options.Markets) == 0 {
		t.Error("应返回可选市场")
	}
	// 不支持的条件必须如实列出，不能静默吞掉
	if len(options.Unsupported) == 0 {
		t.Error("应列出数据源不支持的条件")
	}
	for _, item := range options.Unsupported {
		t.Logf("不支持: %s", item)
	}
	if !options.Defaults.ExcludeST || !options.Defaults.HasQuoteOnly {
		t.Error("默认条件应排除 ST 且只保留有行情的")
	}
}
