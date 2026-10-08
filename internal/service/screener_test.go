package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"freestock/internal/cnlake"
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
	if _, ok := evaluateHistory(rising, cond, nil, ""); !ok {
		t.Error("单调上涨时 MA5 应大于 MA10")
	}

	ltCond := ScreenCondition{MAConditions: []MACondition{{Fast: 5, Op: "lt", Slow: 10}}}
	if _, ok := evaluateHistory(rising, ltCond, nil, ""); ok {
		t.Error("单调上涨时 MA5 不该小于 MA10")
	}

	// 单调下跌：反过来
	falling := quotesFromCloses([]float64{12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})
	if _, ok := evaluateHistory(falling, cond, nil, ""); ok {
		t.Error("单调下跌时 MA5 不该大于 MA10")
	}
	if _, ok := evaluateHistory(falling, ltCond, nil, ""); !ok {
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
	if _, ok := evaluateHistory(rising, both, nil, ""); !ok {
		t.Error("多头排列时两条都该满足")
	}

	// 一条不可能满足的条件会让整体失败
	impossible := ScreenCondition{MAConditions: []MACondition{
		{Fast: 5, Op: "gt", Slow: 10},
		{Fast: 5, Op: "lt", Slow: 10},
	}}
	if _, ok := evaluateHistory(rising, impossible, nil, ""); ok {
		t.Error("互相矛盾的两条条件不该同时满足")
	}
}

// 数据不足以计算均线（新股）时应判为不满足，而不是放行 ——
// 均线条件是形态判断，算不出来就不该算命中。
func TestEvaluateHistoryMAInsufficientData(t *testing.T) {
	short := quotesFromCloses([]float64{1, 2, 3})
	cond := ScreenCondition{MAConditions: []MACondition{{Fast: 5, Op: "gt", Slow: 20}}}

	if _, ok := evaluateHistory(short, cond, nil, ""); ok {
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
	tech, ok := evaluateHistory(quotes, cond, nil, "")
	if !ok {
		t.Fatal("应通过（只启用回撤、未限区间）")
	}
	if tech.drawdown != 20 {
		t.Errorf("回撤 = %v，期望 20", tech.drawdown)
	}

	// 区间过滤
	inRange := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MinDrawdown: 15, MaxDrawdown: 25}
	if _, ok := evaluateHistory(quotes, inRange, nil, ""); !ok {
		t.Error("20% 应落在 15~25% 区间内")
	}
	tooSmall := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MinDrawdown: 30}
	if _, ok := evaluateHistory(quotes, tooSmall, nil, ""); ok {
		t.Error("20% 不该通过 30% 下限")
	}
	tooBig := ScreenCondition{EnableDrawdown: true, DrawdownDays: 10, MaxDrawdown: 10}
	if _, ok := evaluateHistory(quotes, tooBig, nil, ""); ok {
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

	tech, ok := evaluateHistory(quotes, ScreenCondition{EnableDrawdown: true, DrawdownDays: 8}, nil, "")
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
	// 交集的正确性用**子集关系**验证，而不是看结果里的板块名列表 ——
	// 展示用的板块名有数量上限（maxBoardsPerStock），概念排在后面会被截掉，
	// 所以"结果里能看到两个板块名"并不是一个可靠的断言。
	// 行业（申万一级）排在最前不会被截断，可以断言它在。
	conceptOnly, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST: true, HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "概念", Names: []string{"创新药"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	inConcept := map[string]bool{}
	for _, item := range conceptOnly.Items {
		inConcept[item.Code] = true
	}
	inIndustry := map[string]bool{}
	for _, item := range industry.Items {
		inIndustry[item.Code] = true
	}

	for _, item := range combined.Items {
		// 交集里的每一只都必须同时属于两个板块
		if !inConcept[item.Code] {
			t.Errorf("%s 不在创新药成分里，交集算错了", item.Code)
		}
		if !inIndustry[item.Code] {
			t.Errorf("%s 不在医药生物成分里，交集算错了", item.Code)
		}
		// 行业板块排在最前，不会被上限截掉
		if len(item.Boards) == 0 || item.Boards[0] != "医药生物" {
			t.Errorf("%s 的板块列表应以申万一级行业开头: %v", item.Code, item.Boards)
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

// ---------------------------------------------------------------------------
// 数据湖条件
// ---------------------------------------------------------------------------

// RS 的对标指数映射必须和原项目一致 —— 映射错了整条 RS 就没有意义。
func TestRelativeStrengthBenchmark(t *testing.T) {
	cases := map[string]string{
		"300750": "399006.SZ", // 创业板
		"301080": "399006.SZ",
		"688185": "000688.SH", // 科创板
		"689009": "000688.SH",
		"000001": "399001.SZ", // 深市主板
		"002594": "399001.SZ",
		"003816": "399001.SZ",
		"600519": "000001.SH", // 沪市主板
		"601398": "000001.SH",
		"603259": "000001.SH",
		"605499": "000001.SH",
		"430047": "000300.SH", // 北交所等其它 → 兜底沪深300
		"830799": "000300.SH",
	}
	for code, want := range cases {
		if got := relativeStrengthBenchmark(code); got != want {
			t.Errorf("%s 的对标指数 = %s，期望 %s", code, got, want)
		}
	}
	// 带后缀的写法也要能处理
	if got := relativeStrengthBenchmark("600519.SH"); got != "000001.SH" {
		t.Errorf("带后缀时应对标上证指数，实际 %s", got)
	}
}

func TestParseReportPeriod(t *testing.T) {
	year, quarter, ok := parseReportPeriod("2025Q2")
	if !ok || year != 2025 || quarter != 2 {
		t.Errorf("2025Q2 解析错误: %d %d %v", year, quarter, ok)
	}
	if y, q, ok := parseReportPeriod(" 2024q4 "); !ok || y != 2024 || q != 4 {
		t.Errorf("大小写/空格应容忍: %d %d %v", y, q, ok)
	}
	for _, bad := range []string{"", "2025", "Q2", "2025Q5", "2025Q0", "abcdQ1"} {
		if _, _, ok := parseReportPeriod(bad); ok {
			t.Errorf("%q 不该被解析成合法报告期", bad)
		}
	}
}

// 数据湖的净利是年内累计值，单季要减上一季。
func TestSingleQuarterValue(t *testing.T) {
	byPeriod := map[string]float64{
		"2025Q1": 100,
		"2025Q2": 260,
		"2025Q3": 420,
	}

	if got, ok := singleQuarterValue(byPeriod, 2025, 1); !ok || got != 100 {
		t.Errorf("Q1 单季 = %v，期望 100（累计即单季）", got)
	}
	if got, ok := singleQuarterValue(byPeriod, 2025, 2); !ok || got != 160 {
		t.Errorf("Q2 单季 = %v，期望 160", got)
	}
	if got, ok := singleQuarterValue(byPeriod, 2025, 3); !ok || got != 160 {
		t.Errorf("Q3 单季 = %v，期望 160", got)
	}
	if _, ok := singleQuarterValue(byPeriod, 2025, 4); ok {
		t.Error("缺 Q3 时 Q4 不该算得出来")
	}
	if _, ok := singleQuarterValue(byPeriod, 2024, 1); ok {
		t.Error("不存在的年份不该算得出来")
	}
}

func TestSingleQuarterGrowth(t *testing.T) {
	// 2025 单季：Q1=100 Q2=180；2024 单季：Q1=80 Q2=100 → 增速 = 80%
	byPeriod := map[string]float64{
		"2024Q1": 80, "2024Q2": 180,
		"2025Q1": 100, "2025Q2": 280,
	}
	got, ok := singleQuarterGrowth(byPeriod)
	if !ok {
		t.Fatal("应能算出增速")
	}
	if got != 80 {
		t.Errorf("单季同比 = %v%%，期望 80", got)
	}

	if _, ok := singleQuarterGrowth(map[string]float64{"2025Q2": 100}); ok {
		t.Error("只有一期时不该算得出同比")
	}
	if _, ok := singleQuarterGrowth(map[string]float64{"2024Q2": 0, "2025Q2": 100}); ok {
		t.Error("去年同期为 0 时不该给出增速")
	}

	// 去年同期亏损：分母用绝对值，符号才有意义
	// 2024Q2 单季 = -70-(-20) = -50；2025Q2 单季 = 60-10 = 50 → (50-(-50))/50 = 200%
	loss := map[string]float64{"2024Q1": -20, "2024Q2": -70, "2025Q1": 10, "2025Q2": 60}
	if got, ok := singleQuarterGrowth(loss); !ok || got != 200 {
		t.Errorf("由亏转盈的增速 = %v%%（ok=%v），期望 200", got, ok)
	}
}

// 机构参与度取**流通股东**口径，且必须只算机构。
func TestAggregateInstitutionPct(t *testing.T) {
	rows := []cnlake.TopHolderRow{
		{Symbol: "600519.SH", HolderScope: "float", IsInstitution: true, HoldingPct: 5.5},
		{Symbol: "600519.SH", HolderScope: "float", IsInstitution: true, HoldingPct: 3.2},
		// 非机构（个人）不该计入
		{Symbol: "600519.SH", HolderScope: "float", IsInstitution: false, HoldingPct: 9.9},
		// total 口径不该计入 —— 分母是总股本，与 float 不可比
		{Symbol: "600519.SH", HolderScope: "total", IsInstitution: true, HoldingPct: 4.0},
		{Symbol: "000001.SZ", HolderScope: "float", IsInstitution: true, HoldingPct: 12.0},
	}

	got := aggregateInstitutionPct(rows)

	if got["600519"] != 8.7 {
		t.Errorf("600519 机构参与度 = %v，期望 8.7（只算 float 口径的机构）", got["600519"])
	}
	if got["000001"] != 12 {
		t.Errorf("000001 机构参与度 = %v，期望 12", got["000001"])
	}
	if len(got) != 2 {
		t.Errorf("应只有 2 只股票有值，实际 %d", len(got))
	}
}

// RS = 个股涨幅 − 指数涨幅，且指数缺失时要向前找最近交易日。
func TestRelativeStrength(t *testing.T) {
	closes := []float64{10, 11, 12, 13, 14, 15}
	dates := []int64{20260101, 20260102, 20260103, 20260104, 20260105, 20260106}
	benchmark := map[int64]float64{
		20260101: 100, 20260102: 102, 20260103: 104,
		20260104: 106, 20260105: 108, 20260106: 110,
	}

	// 5 日：个股 10→15 = +50%；指数 100→110 = +10% → RS = +40%
	got, ok := relativeStrength(closes, dates, benchmark, 5)
	if !ok {
		t.Fatal("应能算出 RS")
	}
	if got != 40 {
		t.Errorf("RS = %v%%，期望 40", got)
	}

	if _, ok := relativeStrength(closes, dates, benchmark, 10); ok {
		t.Error("窗口大于数据长度时不该算得出 RS")
	}
	if _, ok := relativeStrength(closes, dates, map[int64]float64{}, 5); ok {
		t.Error("指数无数据时不该算得出 RS")
	}
	// 指数当天缺失 → 向前找
	sparse := map[int64]float64{20260105: 100, 20260106: 110}
	if _, ok := relativeStrength(closes, dates, sparse, 1); !ok {
		t.Error("指数当天缺失时应能向前取到")
	}
}

// 数据集没入库时要跳过条件并说明，不能报错也不能静默。
func TestLakeConditionsDegradeGracefully(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	baseline, err := svc.ScreenStocks(ScreenCondition{ExcludeST: true, HasQuoteOnly: true})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	cases := []struct {
		name string
		cond ScreenCondition
	}{
		{"市销率", ScreenCondition{MaxPS: 30}},
		{"机构参与度", ScreenCondition{MinInstitution: 20}},
		{"季报增长", ScreenCondition{MinQuarterGrowth: 20}},
	}
	for _, c := range cases {
		cond := c.cond
		cond.ExcludeST = true
		cond.HasQuoteOnly = true

		result, err := svc.ScreenStocks(cond)
		if err != nil {
			t.Fatalf("%s：不该报错，应优雅降级: %v", c.name, err)
		}
		if len(result.Notes) == 0 {
			t.Errorf("%s：数据集不可用时必须给出提示", c.name)
		}
		if result.Matched != baseline.Matched {
			t.Errorf("%s：条件被跳过后结果应不变: %d vs %d", c.name, result.Matched, baseline.Matched)
		}
		for _, note := range result.Notes {
			t.Logf("%s 提示: %s", c.name, note)
		}
	}
}

// GetScreenOptions 要如实报告每个数据湖条件的数据集是否已入库。
func TestScreenOptionsReportsLakeAvailability(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	options, err := svc.GetScreenOptions()
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if len(options.LakeConditions) < 4 {
		t.Fatalf("应列出 4 项数据湖条件，实际 %d", len(options.LakeConditions))
	}
	byName := map[string]LakeScreenCondition{}
	for _, item := range options.LakeConditions {
		byName[item.Name] = item
		if item.Dataset == "" || item.Field == "" {
			t.Errorf("%s 缺少数据集或字段说明", item.Name)
		}
	}

	// 四个条件的字段都已在 cnequity schema 里对接好，
	// 所以"完全不支持的条件"应该是 0 项
	if len(options.Unsupported) != 0 {
		t.Errorf("四个条件都已对接字段，不该再列为不支持: %v", options.Unsupported)
	}

	// index_bars 有真实数据 → RS 必须报告为可用
	if rs := byName["相对强度 RS"]; !rs.Available {
		t.Error("index_bars 已入库，RS 应报告为可用")
	}
	for _, name := range []string{"市销率 PS", "机构参与度", "季报增长"} {
		if item, ok := byName[name]; ok {
			t.Logf("%s 数据集=%s 可用=%v", name, item.Dataset, item.Available)
		} else {
			t.Errorf("缺少条件 %s", name)
		}
	}
}

// RS 用真实指数行情端到端跑通（index_bars 已入库）。
func TestRelativeStrengthEndToEnd(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	base := ScreenCondition{
		ExcludeST:    true,
		HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"银行"}, Mode: "any"},
		},
		EnableRelativeStrength: true,
		RelativeStrengthDays:   20,
	}

	all, err := svc.ScreenStocks(base)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if all.Matched == 0 {
		t.Fatal("银行板块该有股票能算出 RS")
	}
	for _, item := range all.Items {
		if item.Benchmark == "" {
			t.Errorf("%s 没有对标指数", item.Code)
			break
		}
		if item.RelativeStrength < -100 || item.RelativeStrength > 200 {
			t.Errorf("%s 的 RS 异常: %v", item.Code, item.RelativeStrength)
		}
	}

	strict := base
	strict.MinRelativeStrength = 5
	strong, err := svc.ScreenStocks(strict)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if strong.Matched >= all.Matched {
		t.Errorf("加阈值后不该变多: %d → %d", all.Matched, strong.Matched)
	}
	for _, item := range strong.Items {
		if item.RelativeStrength < 5 {
			t.Errorf("%s 的 RS %v 低于阈值 5", item.Code, item.RelativeStrength)
		}
	}
	t.Logf("银行 RS20：全部 %d 只 → RS>5%% 的 %d 只", all.Matched, strong.Matched)
	for i := 0; i < 3 && i < len(strong.Items); i++ {
		item := strong.Items[i]
		t.Logf("  %s %s RS=%+.2f%% 对标 %s", item.Code, item.Name, item.RelativeStrength, item.Benchmark)
	}
}

// 回归：板块列**与是否设置板块条件无关**。
//
// 原来的实现只在设置了板块条件时才填 Boards（boardIndex 非 nil 才走），
// 导致不设板块条件时"板块"这一列永远是空的。
func TestScreenBoardsPopulatedWithoutBoardFilter(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 注意：没有任何 BoardFilters
	result, err := svc.ScreenStocks(ScreenCondition{
		MinTotalMV:   500,
		MaxPE:        20,
		ExcludeST:    true,
		HasQuoteOnly: true,
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if result.Matched == 0 {
		t.Fatal("该有条件命中")
	}

	withBoards := 0
	for _, item := range result.Items {
		if len(item.Boards) > 0 {
			withBoards++
		}
	}
	// 绝大多数股票都该有板块归属（未分类/退市的少数除外）
	ratio := float64(withBoards) / float64(result.Matched)
	if ratio < 0.8 {
		t.Errorf("不设板块条件时板块信息覆盖率过低: %d/%d", withBoards, result.Matched)
	}

	// 排序正确性用下面的纯函数用例验证。
	// 这里不能按"名字是否属于申万分类"来判断 —— 同名板块可以跨分类存在
	// （申万三级有「饲料」，概念里也可能有），用真实数据判断会有歧义。

	// 每只股票的板块名不该重复，且不超过上限
	for _, item := range result.Items {
		seen := map[string]bool{}
		for _, name := range item.Boards {
			if seen[name] {
				t.Errorf("%s 的板块名重复: %s", item.Code, name)
				break
			}
			seen[name] = true
		}
		if len(item.Boards) > maxBoardsPerStock {
			t.Errorf("%s 的板块数超过上限 %d", item.Code, maxBoardsPerStock)
		}
	}

	t.Logf("不设板块条件：命中 %d，其中有板块信息 %d", result.Matched, withBoards)
	for i := 0; i < 3 && i < len(result.Items); i++ {
		item := result.Items[i]
		t.Logf("  %s %s → %v", item.Code, item.Name, item.Boards)
	}
}

// 板块排序的确定性验证。
//
// 关键点：**排序键跟着板块条目走，不能事后按名字查表** ——
// 同名板块跨分类存在时，按名字查表会让后遍历到的分类覆盖前面的优先级。
func TestOrderBoardNames(t *testing.T) {
	// 申万一级=0 二级=1 三级=2 概念=3
	rank := map[string]int{"申万一级": 0, "申万二级": 1, "申万三级": 2, "概念": 3}
	_ = rank

	// 概念先入列（模拟板块表的遍历顺序），行业后入列 —— 排序必须把它们调正
	entries := []boardEntry{
		{name: "融资融券", rank: 3},
		{name: "白酒概念", rank: 3},
		{name: "食品饮料", rank: 0},
		{name: "饮料制造", rank: 1},
		{name: "白酒", rank: 2},
	}
	got := orderBoardNames(entries)
	want := []string{"食品饮料", "饮料制造", "白酒", "融资融券", "白酒概念"}
	if len(got) != len(want) {
		t.Fatalf("结果长度 %d，期望 %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序错误: %v，期望 %v", got, want)
		}
	}

	// 同名跨分类：只保留一个，且取优先级最高的那个位置
	dup := []boardEntry{
		{name: "石油石化", rank: 3}, // 同名的概念
		{name: "石油石化", rank: 0}, // 同名的申万一级
		{name: "石油矿业开采", rank: 2},
	}
	got = orderBoardNames(dup)
	if len(got) != 2 {
		t.Fatalf("同名板块应去重，实际 %v", got)
	}
	if got[0] != "石油石化" || got[1] != "石油矿业开采" {
		t.Errorf("同名跨分类时应取最高优先级的位置: %v", got)
	}

	// 截断到上限
	many := make([]boardEntry, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, boardEntry{name: fmt.Sprintf("概念%d", i), rank: 3})
	}
	if got := orderBoardNames(many); len(got) != maxBoardsPerStock {
		t.Errorf("应截断到 %d，实际 %d", maxBoardsPerStock, len(got))
	}

	// 空输入
	if got := orderBoardNames(nil); len(got) != 0 {
		t.Errorf("空输入应返回空，实际 %v", got)
	}
}

// mustBoards 返回某个分类下的全部板块名。
func mustBoards(t *testing.T, svc *StockService, category string) []string {
	t.Helper()
	list, err := svc.GetBoards(category)
	if err != nil {
		t.Fatalf("取板块失败: %v", err)
	}
	out := make([]string, 0, len(list))
	for _, board := range list {
		out = append(out, board.Name)
	}
	return out
}

// K 线页的股票信息也要带板块。
func TestKlineProfileIncludesBoards(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	bundle, err := svc.GetKline("600519", "qfq", 0)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if bundle == nil {
		t.Fatal("返回空")
	}
	if len(bundle.Profile.Boards) == 0 {
		t.Fatal("K 线的股票信息里应包含板块")
	}
	// 白酒是贵州茅台必然所属的概念，行业是食品饮料
	joined := strings.Join(bundle.Profile.Boards, ",")
	if !strings.Contains(joined, "食品饮料") {
		t.Errorf("应包含申万一级行业「食品饮料」，实际 %v", bundle.Profile.Boards)
	}
	// 行业排最前
	if bundle.Profile.Boards[0] != "食品饮料" {
		t.Errorf("第一个板块应为申万一级行业，实际 %q", bundle.Profile.Boards[0])
	}
	t.Logf("600519 板块(%d): %v", len(bundle.Profile.Boards), bundle.Profile.Boards)
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
	// 原先列在这里的四个条件（市销率 / 机构参与度 / 季报增长 / 相对强度）
	// 已经全部对接 cnequity 数据湖的字段，所以"完全不支持"的清单应为空。
	// 数据集是否已入库由 LakeConditions 另行如实报告。
	if len(options.Unsupported) != 0 {
		t.Errorf("四个条件的字段都已对接，不该再列为不支持: %v", options.Unsupported)
	}
	if len(options.LakeConditions) == 0 {
		t.Error("应列出依赖数据湖的条件及其数据可用性")
	}
	if !options.Defaults.ExcludeST || !options.Defaults.HasQuoteOnly {
		t.Error("默认条件应排除 ST 且只保留有行情的")
	}
}

// ---------------------------------------------------------------------------
// 指定日期筛选
// ---------------------------------------------------------------------------

// 指定未来日期或非交易日时，必须落到该日或之前最近的一个交易日。
func TestTradeDateAtResolvesBackwards(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	provider, _ := svc.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	latest, err := provider.LatestTradeDate(ctx)
	if err != nil {
		t.Fatalf("取最新交易日失败: %v", err)
	}

	// 未指定 = 最新
	got, err := provider.TradeDateAt(ctx, 0)
	if err != nil || got != latest {
		t.Errorf("date<=0 应等价于最新交易日: got=%d want=%d err=%v", got, latest, err)
	}

	// 指定最新交易日本身
	if got, err := provider.TradeDateAt(ctx, latest); err != nil || got != latest {
		t.Errorf("指定最新交易日应原样返回: got=%d err=%v", got, err)
	}

	// 指定一个数据里不存在的未来日期（但必须是合法日期）
	future := 20261231
	if latest >= future {
		t.Skipf("最新交易日 %d 已晚于 %d，该用例不适用", latest, future)
	}
	got, err = provider.TradeDateAt(ctx, future)
	if err != nil {
		t.Fatalf("指定未来日期失败: %v", err)
	}
	if got > future {
		t.Errorf("解析结果不能晚于指定日期: got=%d requested=%d", got, future)
	}
	// 未来日期在数据里没有，应回退到最新交易日
	if got != latest {
		t.Errorf("指定未来日期应回退到最新交易日 %d，实际 %d", latest, got)
	}
	t.Logf("最新交易日=%d，指定 %d → 解析为 %d", latest, future, got)
}

// 非法日期要在入口就被挡下，给出一句看得懂的提示，
// 而不是传到数据源后报"找不到该日期之前的交易日"。
func TestScreenRejectsInvalidDate(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	invalid := []int64{20260960, 20261301, 20260230, 20260000, 123456789}
	for _, date := range invalid {
		if _, err := svc.ScreenStocks(ScreenCondition{TradeDate: date, Limit: 1}); err == nil {
			t.Errorf("%d 是非法日期，应该报错", date)
		} else if !strings.Contains(err.Error(), "有效日期") {
			t.Errorf("%d 的错误提示不够明确: %v", date, err)
		}
	}

	// 合法日期不该被误挡
	for _, date := range []int64{0, 20260930, 20260228, 20241231} {
		if _, err := svc.ScreenStocks(ScreenCondition{TradeDate: date, Limit: 1}); err != nil {
			t.Errorf("%d 是合法日期，不该被挡下: %v", date, err)
		}
	}
}

func TestValidTradeDateParam(t *testing.T) {
	valid := []int{0, 20260930, 20260101, 20260228, 20241231, 19990101}
	for _, date := range valid {
		if !validTradeDateParam(date) {
			t.Errorf("%d 应判为合法", date)
		}
	}
	invalid := []int{20260931, 20260960, 20261301, 20260230, 20260229, 20260000, -1, 20260900}
	for _, date := range invalid {
		if validTradeDateParam(date) {
			t.Errorf("%d 应判为非法", date)
		}
	}
	// 2024 是闰年，2 月 29 合法
	if !validTradeDateParam(20240229) {
		t.Error("20240229 是闰日，应判为合法")
	}
}

// **核心正确性**：指定历史日期时，取到的必须真的是那天的行情，
// 而不是把最新数据原样返回。
func TestScreenAsOfDateUsesHistoricalQuotes(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	dates := []int64{20260930, 20260901, 20260630}
	closes := make(map[int64]float64, len(dates))

	for _, date := range dates {
		result, err := svc.ScreenStocks(ScreenCondition{
			TradeDate: date,
			Limit:     5000,
		})
		if err != nil {
			t.Fatalf("按 %d 筛选失败: %v", date, err)
		}
		if result.TradeDate > date {
			t.Errorf("实际交易日 %d 晚于指定日期 %d —— 说明拉到了未来的数据", result.TradeDate, date)
		}
		if result.TradeDate != date {
			t.Logf("指定 %d → 实际 %d（该日无数据，已回退）", date, result.TradeDate)
		}
		if !result.Historical {
			t.Errorf("指定了日期，Historical 应为真")
		}
		if result.RequestedDate != date {
			t.Errorf("RequestedDate 应为 %d，实际 %d", date, result.RequestedDate)
		}

		for _, item := range result.Items {
			if item.Code == "000001" {
				closes[date] = item.Close
			}
		}
	}

	if len(closes) < len(dates) {
		t.Fatalf("没能在每个日期都找到 000001: %v", closes)
	}
	// 三个日期的收盘价必须不同 —— 否则说明根本没按日期取数
	seen := map[float64]bool{}
	for _, close := range closes {
		seen[close] = true
	}
	if len(seen) != len(dates) {
		t.Errorf("不同日期取到了相同的收盘价，说明没有真正按日期取数: %v", closes)
	}
	t.Logf("000001 的收盘价：%v", closes)
}

// 技术条件也必须以指定日期为终点，不能算进指定日期之后的数据。
func TestScreenAsOfDateAppliesToHistoryConditions(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	base := ScreenCondition{
		ExcludeST:    true,
		HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"银行"}, Mode: "any"},
		},
		MAConditions: []MACondition{{Fast: 5, Op: "gt", Slow: 20}},
	}

	matched := make(map[int64]int, 3)
	for _, date := range []int64{0, 20260901, 20260630} {
		cond := base
		cond.TradeDate = date
		result, err := svc.ScreenStocks(cond)
		if err != nil {
			t.Fatalf("按 %d 筛选失败: %v", date, err)
		}
		if !result.HistoryEvaluated {
			t.Error("启用了均线条件，HistoryEvaluated 应为真")
		}
		matched[date] = result.Matched
	}

	// 三个时点的均线形态命中数不该完全相同（相同则说明日线窗口没跟着日期走）
	distinct := map[int]bool{}
	for _, count := range matched {
		distinct[count] = true
	}
	if len(distinct) < 2 {
		t.Errorf("不同日期算出的均线命中数完全相同，可能没按日期取日线: %v", matched)
	}
	t.Logf("银行 MA5>MA20 命中数：最新(0)=%d 20260901=%d 20260630=%d",
		matched[0], matched[20260901], matched[20260630])
}

// 板块成分名单只有当前这一份，指定历史日期时必须把这个口径差异说清楚。
func TestScreenAsOfDateExplainsBoardCaveat(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	result, err := svc.ScreenStocks(ScreenCondition{
		TradeDate:    20260630,
		ExcludeST:    true,
		HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	found := false
	for _, note := range result.Notes {
		if strings.Contains(note, "板块成分名单") && strings.Contains(note, "当前") {
			found = true
		}
		// 界面按纯文本渲染提示，混进 Markdown 标记会原样显示出来
		if strings.Contains(note, "**") {
			t.Errorf("提示里不该出现 Markdown 标记: %s", note)
		}
	}
	if !found {
		t.Errorf("历史日期 + 板块条件时必须说明成分股口径，实际提示: %v", result.Notes)
	}

	// 不指定日期时不该出现这条提示（当前成分 = 正确口径）
	latest, err := svc.ScreenStocks(ScreenCondition{
		ExcludeST:    true,
		HasQuoteOnly: true,
		BoardFilters: []BoardFilter{
			{Category: "申万一级", Names: []string{"医药生物"}, Mode: "any"},
		},
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	for _, note := range latest.Notes {
		if strings.Contains(note, "板块成分名单") {
			t.Error("未指定日期时不该出现成分股口径提示")
		}
	}
}

// 指定日期与解析结果不一致时，要把实际用的日期告诉用户。
func TestScreenAsOfDateReportsFallback(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 20260101 是元旦，肯定不是交易日
	result, err := svc.ScreenStocks(ScreenCondition{TradeDate: 20260101, Limit: 1})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if result.TradeDate == 20260101 {
		t.Fatalf("20260101 不该被当成交易日")
	}
	if result.TradeDate > 20260101 {
		t.Errorf("解析结果 %d 晚于指定日期", result.TradeDate)
	}

	found := false
	for _, note := range result.Notes {
		if strings.Contains(note, "20260101") {
			found = true
		}
	}
	if !found {
		t.Errorf("指定日期被回退时应明确告知，实际提示: %v", result.Notes)
	}
	t.Logf("指定 20260101 → 实际使用 %d", result.TradeDate)
}

// 按日期的快照要缓存，来回改日期不该反复全量抓取。
func TestUniverseOnCachesByDate(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	provider, source := svc.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	first, err := svc.universeOn(ctx, provider, source, 20260630)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	second, err := svc.universeOn(ctx, provider, source, 20260630)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if first != second {
		t.Error("同一交易日应命中缓存（返回同一个对象）")
	}

	other, err := svc.universeOn(ctx, provider, source, 20260901)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if other == first {
		t.Error("不同交易日不该共用同一份快照")
	}
	if other.TradeDate == first.TradeDate {
		t.Errorf("两份快照的交易日不该相同: %d", other.TradeDate)
	}
	t.Logf("缓存了 %d 与 %d 两个交易日", first.TradeDate, other.TradeDate)
}
