package service

import (
	"testing"

	"freestock/internal/stockdb"
)

// 构造板块聚合的输入
func stock(code, name string, pctChg, amount, floatMV float64) StockItem {
	return StockItem{
		Code:     code,
		Name:     name,
		Close:    10,
		PctChg:   pctChg,
		Amount:   amount,
		FloatMV:  floatMV,
		TotalMV:  floatMV,
		HasQuote: true,
	}
}

func board(code, name string, symbols ...string) stockdb.Board {
	return stockdb.Board{Code: code, Name: name, Category: "申万一级", Type: "sw_1", Symbols: symbols}
}

func TestAggregateBoardWeightedByFloatMarketCap(t *testing.T) {
	// 小盘股涨 10%（市值 1 亿），大盘股跌 1%（市值 99 亿）
	quotes := map[string]StockItem{
		"A": stock("A", "小盘", 10, 1e6, 1e8),
		"B": stock("B", "大盘", -1, 1e8, 99e8),
	}

	got := aggregateBoard(board("BK1", "测试板块", "A", "B"), quotes)

	// 加权：(10×1 + (-1)×99) / 100 = -0.89%
	if got.PctChg != -0.89 {
		t.Errorf("加权涨跌幅 = %v，期望 -0.89", got.PctChg)
	}
	// 等权：(10 + (-1)) / 2 = 4.5%
	if got.PctChgEqual != 4.5 {
		t.Errorf("等权涨跌幅 = %v，期望 4.5", got.PctChgEqual)
	}
	// 这两个值的巨大差异正是要同时给出的理由：等权看着在涨，市值加权其实在跌
	if got.PctChg >= 0 {
		t.Error("这个用例的用意就是加权为负，等权为正")
	}
}

func TestAggregateBoardTurnoverAndAmount(t *testing.T) {
	quotes := map[string]StockItem{
		"A": stock("A", "甲", 1, 3e8, 100e8),
		"B": stock("B", "乙", 2, 1e8, 100e8),
	}

	got := aggregateBoard(board("BK1", "测试", "A", "B"), quotes)

	if got.Amount != 4e8 {
		t.Errorf("成交额 = %v，期望 4e8", got.Amount)
	}
	if got.FloatMV != 200e8 {
		t.Errorf("流通市值 = %v，期望 200e8", got.FloatMV)
	}
	// 4e8 / 200e8 × 100 = 2%
	if got.Turnover != 2 {
		t.Errorf("换手率 = %v，期望 2", got.Turnover)
	}
}

func TestAggregateBoardBreadthAndLeader(t *testing.T) {
	quotes := map[string]StockItem{
		"A": stock("A", "涨停", 10, 1e8, 1e9),
		"B": stock("B", "小涨", 2.5, 1e8, 1e9),
		"C": stock("C", "平盘", 0, 1e8, 1e9),
		"D": stock("D", "大跌", -5.5, 1e8, 1e9),
	}

	got := aggregateBoard(board("BK1", "测试", "A", "B", "C", "D"), quotes)

	if got.UpCount != 2 || got.DownCount != 1 || got.FlatCount != 1 {
		t.Errorf("涨跌家数错误: 涨=%d 跌=%d 平=%d", got.UpCount, got.DownCount, got.FlatCount)
	}
	if got.MemberCount != 4 || got.QuotedCount != 4 {
		t.Errorf("成分数错误: member=%d quoted=%d", got.MemberCount, got.QuotedCount)
	}
	if got.Leader == nil || got.Leader.Code != "A" {
		t.Errorf("领涨股错误: %+v", got.Leader)
	}
	if got.Laggard == nil || got.Laggard.Code != "D" {
		t.Errorf("领跌股错误: %+v", got.Laggard)
	}
}

// 成分缺行情（停牌/退市/未同步）时要如实反映，不能假装成分数就是有行情的数量。
func TestAggregateBoardMissingQuotes(t *testing.T) {
	quotes := map[string]StockItem{
		"A": stock("A", "甲", 3, 1e8, 1e9),
	}
	got := aggregateBoard(board("BK1", "测试", "A", "B", "C"), quotes)

	if got.MemberCount != 3 {
		t.Errorf("成分总数应为 3，实际 %d", got.MemberCount)
	}
	if got.QuotedCount != 1 {
		t.Errorf("有行情的成分应为 1，实际 %d", got.QuotedCount)
	}
	// 加权与等权都只按有行情的算，不能把缺失的当成 0 涨幅摊进去
	if got.PctChg != 3 {
		t.Errorf("加权涨跌幅 = %v，期望 3（只按有行情的成分算）", got.PctChg)
	}
	if got.PctChgEqual != 3 {
		t.Errorf("等权涨跌幅 = %v，期望 3", got.PctChgEqual)
	}
}

func TestAggregateBoardEmptyMembers(t *testing.T) {
	got := aggregateBoard(board("BK1", "空板块"), map[string]StockItem{})

	if got.QuotedCount != 0 || got.PctChg != 0 || got.Turnover != 0 {
		t.Errorf("空板块所有指标应为 0: %+v", got)
	}
	if got.Leader != nil || got.Laggard != nil {
		t.Error("没有成分时不该有领涨/领跌股")
	}
}

// 有些标的没有流通市值，应退化为总市值，否则权重全为 0、加权涨跌幅会变成 0。
func TestAggregateBoardFallsBackToTotalMV(t *testing.T) {
	item := stock("A", "甲", 5, 1e8, 0)
	item.TotalMV = 50e8
	quotes := map[string]StockItem{"A": item}

	got := aggregateBoard(board("BK1", "测试", "A"), quotes)

	if got.PctChg != 5 {
		t.Errorf("流通市值缺失时应退化为总市值，加权涨跌幅 = %v", got.PctChg)
	}
	if got.FloatMV != 50e8 {
		t.Errorf("应回填总市值作为权重，实际 %v", got.FloatMV)
	}
}

// 全部分量权重为 0（数据源既没有流通市值也没有总市值）时不能出 NaN。
func TestAggregateBoardZeroWeight(t *testing.T) {
	item := stock("A", "甲", 5, 1e8, 0)
	item.TotalMV = 0
	got := aggregateBoard(board("BK1", "测试", "A"), map[string]StockItem{"A": item})

	if got.PctChg != 0 {
		t.Errorf("权重为 0 时加权涨跌幅应为 0，实际 %v", got.PctChg)
	}
	if got.Turnover != 0 {
		t.Errorf("权重为 0 时换手率应为 0，实际 %v", got.Turnover)
	}
	// 等权仍然有意义
	if got.PctChgEqual != 5 {
		t.Errorf("等权涨跌幅应仍为 5，实际 %v", got.PctChgEqual)
	}
}

// 概念板块的成交额会跨板块重复计入，必须被识别出来，否则界面会把 25 万亿
// 当成市场总成交额展示（真实值是 1.4 万亿）。
func TestGetBoardQuotesMarksOverlap(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 申万一级是层级分类：每只股票只属于一个一级行业
	industry, err := svc.GetBoardQuotes("申万一级", 0)
	if err != nil {
		t.Fatalf("申万一级聚合失败: %v", err)
	}
	if industry.Overlapping {
		t.Errorf("申万一级不该被判定为重叠，overlapRatio=%v", industry.OverlapRatio)
	}

	// 概念是多对多
	concept, err := svc.GetBoardQuotes("概念", 0)
	if err != nil {
		t.Fatalf("概念聚合失败: %v", err)
	}
	if !concept.Overlapping {
		t.Errorf("概念应被判定为重叠，overlapRatio=%v", concept.OverlapRatio)
	}
	if concept.OverlapRatio <= 1.2 {
		t.Errorf("概念的成分重叠倍数偏低: %v", concept.OverlapRatio)
	}

	t.Logf("申万一级: %d 个板块，重叠倍数 %.2f，成交额合计 %.1f 亿",
		industry.BoardCount, industry.OverlapRatio, industry.TotalAmount/1e8)
	t.Logf("概念: %d 个板块，重叠倍数 %.2f，成交额合计 %.1f 亿（重复计入）",
		concept.BoardCount, concept.OverlapRatio, concept.TotalAmount/1e8)
}

func TestGetBoardQuotesAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	result, err := svc.GetBoardQuotes("申万一级", 0)
	if err != nil {
		t.Fatalf("GetBoardQuotes 失败: %v", err)
	}
	if result.BoardCount < 20 {
		t.Fatalf("申万一级板块数偏少: %d", result.BoardCount)
	}
	if result.TradeDate <= 0 {
		t.Error("缺少交易日")
	}
	// 涨/跌/平家数应等于板块总数
	if result.UpBoards+result.DownBoards+result.FlatBoards != result.BoardCount {
		t.Errorf("板块涨跌家数之和对不上: %d+%d+%d != %d",
			result.UpBoards, result.DownBoards, result.FlatBoards, result.BoardCount)
	}

	// 默认按涨跌幅从高到低
	for i := 1; i < len(result.Boards); i++ {
		if result.Boards[i].PctChg > result.Boards[i-1].PctChg {
			t.Fatalf("未按涨跌幅降序: 第 %d 个 %v > 前一个 %v",
				i, result.Boards[i].PctChg, result.Boards[i-1].PctChg)
		}
	}

	top := result.Boards[0]
	t.Logf("%d 个板块，上涨 %d / 下跌 %d，总成交额 %.1f 亿",
		result.BoardCount, result.UpBoards, result.DownBoards, result.TotalAmount/1e8)
	t.Logf("领涨板块 %s %+.2f%%（成分 %d，有行情 %d，成交额 %.1f 亿）",
		top.Name, top.PctChg, top.MemberCount, top.QuotedCount, top.Amount/1e8)
	if top.Leader != nil {
		t.Logf("  领涨股 %s %+.2f%%", top.Leader.Name, top.Leader.PctChg)
	}

	// 每个板块的加权涨跌幅不该超过成分股的最大涨幅（加权平均的性质）
	if top.Leader != nil && top.PctChg > top.Leader.PctChg+0.01 {
		t.Errorf("板块加权涨跌幅 %v 超过了领涨股 %v", top.PctChg, top.Leader.PctChg)
	}

	// 板块涨跌幅应在合理范围内（A 股单日 ±10%，科创板/创业板 ±20%）
	for _, item := range result.Boards {
		if item.PctChg < -25 || item.PctChg > 25 {
			t.Errorf("%s 涨跌幅异常: %v", item.Name, item.PctChg)
		}
	}
}

func TestGetBoardQuotesLimit(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	full, err := svc.GetBoardQuotes("申万一级", 0)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	limited, err := svc.GetBoardQuotes("申万一级", 5)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if len(limited.Boards) != 5 {
		t.Fatalf("limit 未生效: %d", len(limited.Boards))
	}
	// BoardCount 与统计必须仍然是全量的，否则界面上的汇总数字会随显示条数变化
	if limited.BoardCount != full.BoardCount {
		t.Errorf("截断后 BoardCount 变了: %d vs %d", limited.BoardCount, full.BoardCount)
	}
	if limited.UpBoards != full.UpBoards || limited.DownBoards != full.DownBoards {
		t.Errorf("截断后涨跌家数变了: %d/%d vs %d/%d",
			limited.UpBoards, limited.DownBoards, full.UpBoards, full.DownBoards)
	}
	if limited.TotalAmount != full.TotalAmount {
		t.Errorf("截断后总成交额变了: %v vs %v", limited.TotalAmount, full.TotalAmount)
	}
}

func TestGetBoardMembersAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	result, err := svc.GetBoardQuotes("申万一级", 0)
	if err != nil || len(result.Boards) == 0 {
		t.Fatalf("拿不到板块: %v", err)
	}

	target := result.Boards[0]
	members, err := svc.GetBoardMembers(target.Code)
	if err != nil {
		t.Fatalf("GetBoardMembers 失败: %v", err)
	}
	if len(members) != target.MemberCount {
		t.Errorf("成分数对不上: %d vs %d", len(members), target.MemberCount)
	}
	// 按涨跌幅降序
	for i := 1; i < len(members); i++ {
		if members[i].PctChg > members[i-1].PctChg {
			t.Fatalf("成分股未按涨跌幅降序: 第 %d 个", i)
		}
	}
	// 第一个应该就是领涨股
	if target.Leader != nil && members[0].Code != target.Leader.Code {
		t.Errorf("成分股榜首与领涨股不一致: %s vs %s", members[0].Code, target.Leader.Code)
	}
	t.Logf("%s 共 %d 只成分，第一名 %s %+.2f%%",
		target.Name, len(members), members[0].Name, members[0].PctChg)
}

func TestGetBoardMembersUnknownCode(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	if _, err := svc.GetBoardMembers("NOT_A_BOARD"); err == nil {
		t.Error("不存在的板块代码应报错，便于界面区分")
	}
}
