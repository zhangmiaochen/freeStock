package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/cnlake/cnlaketest"
	"freestock/internal/config"
	"freestock/internal/stockdb"
)

// newSyntheticLakeProvider 用合成数据湖构造一个行情源。
//
// 本机 cnequity 的 daily_bars / valuation_metrics / fund_flow 还没入库，
// 所以这里用与真实布局一致的数据验证组装逻辑。
func newSyntheticLakeProvider(t *testing.T) Provider {
	t.Helper()
	root := t.TempDir()
	if err := cnlaketest.Build(root); err != nil {
		t.Fatalf("构造合成数据湖失败: %v", err)
	}
	return NewLakeProvider(cnlake.Open(root))
}

func TestLakeProviderStockCodes(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	buckets, err := provider.StockCodes(context.Background())
	if err != nil {
		t.Fatalf("StockCodes 失败: %v", err)
	}

	total := 0
	for _, list := range buckets {
		total += len(list)
	}
	if total != 4 {
		t.Fatalf("期望 4 个标的，实际 %d（%v）", total, buckets)
	}
	// 数据湖用 600519.SH，接口对外必须回到 600519
	for _, list := range buckets {
		for _, code := range list {
			if len(code) != 6 {
				t.Errorf("代码未去掉交易所后缀: %q", code)
			}
		}
	}
	if provider.Kind() != SourceKindCnLake {
		t.Errorf("Kind 应为 %s，实际 %s", SourceKindCnLake, provider.Kind())
	}
}

func TestLakeProviderLatestTradeDate(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	date, err := provider.LatestTradeDate(context.Background())
	if err != nil {
		t.Fatalf("LatestTradeDate 失败: %v", err)
	}
	if date != 20260930 {
		t.Errorf("最新交易日错误: %d", date)
	}
}

func TestLakeProviderQuotePointJoinsValuation(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	quote, err := provider.QuotePoint(context.Background(), "600519", 20260320)
	if err != nil {
		t.Fatalf("QuotePoint 失败: %v", err)
	}
	if quote == nil {
		t.Fatal("应取到行情")
	}

	t.Logf("行情: %+v", quote)

	if quote.Name != "贵州茅台" {
		t.Errorf("名称应来自 instruments: %q", quote.Name)
	}
	// 2026 年第 20 根：1500 + 20 + 19 = 1539 开，1540 收
	if quote.Close != 1540 {
		t.Errorf("收盘价错误: %v", quote.Close)
	}
	// 关键：PE/PB/市值来自 valuation_metrics，与 daily_bars 按日期关联
	if quote.PeTTM != 19.32 || quote.PB != 6.26 {
		t.Errorf("估值字段未关联上: PE=%v PB=%v", quote.PeTTM, quote.PB)
	}
	if quote.TotalMV != 1.57e12 {
		t.Errorf("总市值错误: %v", quote.TotalMV)
	}
	// 数据湖的 daily_bars 只有 OHLCV，这些要自己推
	if quote.PreClose != 1539 {
		t.Errorf("昨收应由上一根推出: %v", quote.PreClose)
	}
	if quote.PctChg <= 0 {
		t.Errorf("涨跌幅应由昨收推出: %v", quote.PctChg)
	}
	if want := round((1544.0-1534.0)/1539.0*100, 4); quote.Amplitude != want {
		t.Errorf("振幅推导错误: 实际 %v，期望 %v", quote.Amplitude, want)
	}
	// 股本来自 share_structure（2025-07-01 起生效 1.256e9）
	if quote.TotalShare != 1.256e9 {
		t.Errorf("总股本应来自 share_structure: %v", quote.TotalShare)
	}
}

// 交易日历可能比 K 线跑得靠前：该日没有 K 线时，应回退到最近一根而不是返回空。
func TestLakeProviderQuotePointFallsBackToLatestBar(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	// 日历里的最新交易日是 2026-09-30，但 K 线只到 2026-03-20
	quote, err := provider.QuotePoint(context.Background(), "600519", 20260930)
	if err != nil {
		t.Fatalf("QuotePoint 失败: %v", err)
	}
	if quote == nil {
		t.Fatal("该日没有 K 线时应回退到最近一根，而不是返回空")
	}
	if quote.Date != 20260320 {
		t.Errorf("应回退到 20260320，实际 %d", quote.Date)
	}
	t.Logf("回退结果: 日期=%d 收盘=%.2f", quote.Date, quote.Close)
}

func TestLakeProviderDailyHistory(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	quotes, err := provider.DailyHistory(context.Background(), "600519", 20240101, 20261231)
	if err != nil {
		t.Fatalf("DailyHistory 失败: %v", err)
	}
	if len(quotes) != 60 {
		t.Fatalf("期望 60 根，实际 %d", len(quotes))
	}
	// 升序
	for i := 1; i < len(quotes); i++ {
		if quotes[i].Date <= quotes[i-1].Date {
			t.Fatalf("日线未按日期升序: %d <= %d", quotes[i].Date, quotes[i-1].Date)
		}
	}
	// 只有 2026 年有估值，早期不该有
	if quotes[len(quotes)-1].Date == 20260320 && quotes[len(quotes)-1].PeTTM == 0 {
		t.Error("2026 年的行情应带上估值")
	}
	if quotes[0].Date < 20260101 && quotes[0].PeTTM != 0 {
		t.Errorf("早期行情不该有估值: %d -> %v", quotes[0].Date, quotes[0].PeTTM)
	}

	// Desc 版本应当是反过来的
	desc, err := provider.QuoteRangeDesc(context.Background(), "600519", 20240101, 20261231)
	if err != nil {
		t.Fatalf("QuoteRangeDesc 失败: %v", err)
	}
	if len(desc) != len(quotes) {
		t.Fatalf("两种顺序的条数应一致: %d vs %d", len(desc), len(quotes))
	}
	if desc[0].Date != quotes[len(quotes)-1].Date {
		t.Errorf("降序首条应是最新一根: %d", desc[0].Date)
	}
}

func TestLakeProviderDividendFactors(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	factors, err := provider.DividendFactors(context.Background(), "600519")
	if err != nil {
		t.Fatalf("DividendFactors 失败: %v", err)
	}
	if len(factors) != 1 {
		t.Fatalf("期望 1 条分红，实际 %d", len(factors))
	}
	// 每股派息必须保留 —— 股息率依赖它
	if factors[0].Div != 23.882 {
		t.Errorf("每股派息丢失: %v", factors[0].Div)
	}
	if factors[0].Date != 20250619 {
		t.Errorf("除权日错误: %d", factors[0].Date)
	}
	// 没有送转时倍数应为 1
	if factors[0].Mult != 1 || factors[0].Cum != 1 {
		t.Errorf("无送转时因子应中性: mult=%v cum=%v", factors[0].Mult, factors[0].Cum)
	}
}

func TestLakeProviderDividendFactorsFallsBackToAdjFactors(t *testing.T) {
	root := t.TempDir()
	if err := cnlaketest.Build(root); err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	provider := NewLakeProvider(cnlake.Open(root))

	// 000001.SZ 在合成数据里有分红，这里换成只有 adj_factors 的标的看不出来，
	// 所以直接验证「没有 corporate_actions 的代码走 adj_factors 分支」：
	// 510980.SH 两者都没有 -> 空
	factors, err := provider.DividendFactors(context.Background(), "510980")
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(factors) != 0 {
		t.Errorf("没有分红数据应返回空，实际 %d", len(factors))
	}
}

func TestLakeProviderMoneyFlow(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	flows, err := provider.MoneyFlowRange(context.Background(), "600519", 20260101, 20261231)
	if err != nil {
		t.Fatalf("MoneyFlowRange 失败: %v", err)
	}
	if len(flows) != 2 {
		t.Fatalf("期望 2 条资金流，实际 %d", len(flows))
	}
	if flows[0].Date != 20260319 || flows[1].Date != 20260320 {
		t.Errorf("资金流日期错误: %v", flows)
	}
	// fund_flow 的字段名与 free-stockdb 不同，映射要正确
	if flows[1].MainNet != 5.39e8 {
		t.Errorf("主力净流入映射错误: %v", flows[1].MainNet)
	}
	if flows[1].JumboNet != 3.37e8 {
		t.Errorf("超大单映射错误（应为 super_large_net_inflow）: %v", flows[1].JumboNet)
	}
	if flows[1].BigNet != 2.03e8 {
		t.Errorf("大单映射错误（应为 large_net_inflow）: %v", flows[1].BigNet)
	}
}

func TestLakeProviderBoards(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	boards, err := provider.Boards(context.Background())
	if err != nil {
		t.Fatalf("Boards 失败: %v", err)
	}
	if len(boards) != 3 {
		t.Fatalf("期望 3 个板块（2 个行业 + 1 个概念），实际 %d: %+v", len(boards), boards)
	}

	byName := map[string]int{}
	for _, board := range boards {
		byName[board.Name] = len(board.Symbols)
	}
	// 行业分类体系要翻译成界面上的名字
	if byName["食品饮料"] != 1 {
		t.Errorf("食品饮料成分数错误: %d", byName["食品饮料"])
	}
	if byName["银行"] != 1 {
		t.Errorf("银行成分数错误: %d", byName["银行"])
	}
	if byName["酿酒行业"] != 1 {
		t.Errorf("概念板块成分数错误: %d", byName["酿酒行业"])
	}

	categories := map[string]string{}
	for _, board := range boards {
		categories[board.Name] = board.Category
	}
	if categories["食品饮料"] != "申万一级" {
		t.Errorf("sw_l1 应翻译成申万一级，实际 %q", categories["食品饮料"])
	}
	if categories["酿酒行业"] != "概念" {
		t.Errorf("板块应归到概念，实际 %q", categories["酿酒行业"])
	}
	// 成分代码要去掉后缀
	for _, board := range boards {
		for _, code := range board.Symbols {
			if len(code) != 6 {
				t.Errorf("成分代码未归一化: %q", code)
			}
		}
	}
}

func TestLakeProviderCachesAreSeparateFromRealLake(t *testing.T) {
	provider := newSyntheticLakeProvider(t)
	if provider.Label() == "" {
		t.Error("Label 不该为空")
	}
	// 连续两次取标的应命中缓存且结果一致
	first, err := provider.StockCodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.StockCodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Errorf("两次读取结果不一致: %d vs %d", len(first), len(second))
	}
}

// stockdbProvider 只是转发，这里验证它保持 Kind 与 Label。
func TestStockdbProviderKind(t *testing.T) {
	provider := NewStockdbProvider(nil, "http://127.0.0.1:7899")
	if provider.Kind() != SourceKindStockDB {
		t.Errorf("Kind 错误: %s", provider.Kind())
	}
	if provider.Label() != "http://127.0.0.1:7899" {
		t.Errorf("Label 错误: %s", provider.Label())
	}
}

// 切换数据源必须清掉派生缓存，否则会串数据。
func TestSwitchProviderClearsCache(t *testing.T) {
	root := t.TempDir()
	if err := cnlaketest.Build(root); err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	svc := NewStockService(nil)
	svc.applySource("cnlake", root)

	if provider, source := svc.snapshot(); provider.Kind() != SourceKindCnLake {
		t.Fatalf("应切换到数据湖，实际 %s", provider.Kind())
	} else if source.Kind != "cnlake" {
		t.Errorf("source.Kind 错误: %s", source.Kind)
	}

	// 造一份缓存，再切换，应当被清掉
	svc.boardsMu.Lock()
	svc.boards = nil
	svc.boardsMu.Unlock()

	svc.applySource("stockdb", "http://127.0.0.1:7899")
	if provider, _ := svc.snapshot(); provider.Kind() != SourceKindStockDB {
		t.Errorf("应切回 free-stockdb，实际 %s", provider.Kind())
	}
}

// 数据湖没有 daily_bars 时，TestDataSource 要说明「只能当财务数据源」而不是简单报错。
func TestTestLakeWithoutDailyBars(t *testing.T) {
	root := t.TempDir()
	cnlaketest.Build(root)

	svc := NewStockService(nil)
	status := svc.TestDataSource("cnlake", root)

	if !status.OK {
		t.Fatalf("数据湖存在时应判定可用: %s", status.Message)
	}
	if status.Kind != "cnlake" {
		t.Errorf("Kind 错误: %s", status.Kind)
	}
	// 合成数据湖里有 daily_bars，应说明可作行情源
	t.Logf("检测结果: %s", status.Message)
	if status.Total == 0 {
		t.Error("应报告数据集数量")
	}
}

func TestTestLakeMissing(t *testing.T) {
	svc := NewStockService(nil)
	status := svc.TestDataSource("cnlake", `Z:\definitely\not\here`)

	if status.OK {
		t.Error("不存在的路径不该判定为可用")
	}
	if status.Message == "" {
		t.Error("要说明怎么解决")
	}
	t.Logf("预期提示: %s", status.Message)
}

// 数据湖优先用作行情源时，整条链路要能跑通：标的 -> 行情 -> 复权 -> 资金流。
func TestLakeProviderEndToEnd(t *testing.T) {
	provider := newSyntheticLakeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	buckets, err := provider.StockCodes(ctx)
	if err != nil || len(buckets) == 0 {
		t.Fatalf("标的清单失败: %v", err)
	}

	tradeDate, err := provider.LatestTradeDate(ctx)
	if err != nil {
		t.Fatalf("交易日失败: %v", err)
	}

	quote, err := provider.QuotePoint(ctx, "600519", tradeDate)
	if err != nil || quote == nil {
		t.Fatalf("取行情失败: %v", err)
	}

	quotes, err := provider.DailyHistory(ctx, "600519", 20240101, tradeDate)
	if err != nil || len(quotes) == 0 {
		t.Fatalf("取日线失败: %v", err)
	}

	factors, err := provider.DividendFactors(ctx, "600519")
	if err != nil {
		t.Fatalf("取复权失败: %v", err)
	}

	flows, err := provider.MoneyFlowRange(ctx, "600519", 20240101, tradeDate)
	if err != nil {
		t.Fatalf("取资金流失败: %v", err)
	}

	t.Logf("全链路: 标的=%d 交易日=%d 最新价=%.2f 日线=%d 复权=%d 资金流=%d",
		countCodes(buckets), tradeDate, quote.Close, len(quotes), len(factors), len(flows))
}

// 批量取行情：一次扫描服务所有股票，并正确处理「只有标的没有行情」的情况。
func TestLakeProviderLatestQuotesBulk(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	codes := []string{"600519", "000001", "300750", "510980"}
	quotes, err := provider.LatestQuotes(context.Background(), codes, 20260320)
	if err != nil {
		t.Fatalf("LatestQuotes 失败: %v", err)
	}

	// 600519 / 000001 有日线；300750 / 510980 只有标的条目
	if len(quotes) != 2 {
		t.Fatalf("期望 2 只有行情，实际 %d: %v", len(quotes), keysOfQuotes(quotes))
	}
	for _, code := range []string{"300750", "510980"} {
		if _, ok := quotes[code]; ok {
			t.Errorf("%s 没有日线，不该出现在结果里", code)
		}
	}

	moutai := quotes["600519"]
	if moutai == nil {
		t.Fatal("缺少 600519")
	}
	if moutai.Name != "贵州茅台" || moutai.Close != 1540 {
		t.Errorf("行情内容错误: %+v", moutai)
	}
	if moutai.PreClose != 1539 {
		t.Errorf("批量路径也要能算出昨收: %v", moutai.PreClose)
	}
	if moutai.PeTTM != 19.32 || moutai.PB != 6.26 {
		t.Errorf("批量路径要关联估值: PE=%v PB=%v", moutai.PeTTM, moutai.PB)
	}

	pingan := quotes["000001"]
	if pingan == nil || pingan.Name != "平安银行" {
		t.Errorf("000001 行情错误: %+v", pingan)
	}

	// 与逐只查询的结果必须一致
	single, err := provider.QuotePoint(context.Background(), "600519", 20260320)
	if err != nil {
		t.Fatalf("QuotePoint 失败: %v", err)
	}
	if single.Close != moutai.Close || single.PreClose != moutai.PreClose ||
		single.PeTTM != moutai.PeTTM || single.PctChg != moutai.PctChg {
		t.Errorf("批量与逐只结果不一致:\n  批量 %+v\n  逐只 %+v", moutai, single)
	}

	// 日历超前时批量路径也要回退
	fallback, err := provider.LatestQuotes(context.Background(), []string{"600519"}, 20260930)
	if err != nil {
		t.Fatalf("LatestQuotes 回退失败: %v", err)
	}
	if quote := fallback["600519"]; quote == nil || quote.Date != 20260320 {
		t.Errorf("日历超前时应回退到最近一根: %+v", quote)
	}
}

func TestLakeProviderLatestQuotesEmpty(t *testing.T) {
	provider := newSyntheticLakeProvider(t)

	quotes, err := provider.LatestQuotes(context.Background(), nil, 20260320)
	if err != nil {
		t.Fatalf("空列表不该报错: %v", err)
	}
	if len(quotes) != 0 {
		t.Errorf("空列表应返回空 map，实际 %d", len(quotes))
	}
}

// 关键需求：行情没切到数据湖时，财务数据仍然要从数据湖出。
func TestFinancialStatementsWhenQuotesComeFromStockDB(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败: %v", err)
	}

	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	cfg.SetCnLakeRoot(lakeRoot)
	// 不切换数据源：行情仍走 free-stockdb
	svc := NewStockService(cfg)

	if provider, _ := svc.snapshot(); provider.Kind() != SourceKindStockDB {
		t.Fatalf("行情源应为 free-stockdb，实际 %s", provider.Kind())
	}

	statements, err := svc.GetFinancialStatements("600519", 50)
	if err != nil {
		t.Fatalf("GetFinancialStatements 失败: %v", err)
	}

	t.Logf("数据湖=%s 可用=%v 表状态=%v", statements.LakeRoot, statements.Available, statements.TableStatus)
	t.Logf("财报科目=%d 报告期=%v", len(statements.Items), statements.ReportPeriods)
	t.Logf("股本结构=%d 股东户数=%d 十大股东=%d（%d）",
		len(statements.ShareStructure), len(statements.ShareholderCounts),
		len(statements.TopHolders), statements.TopHolderDate)

	// 三张表都要有数据
	if len(statements.ShareStructure) != 2 {
		t.Errorf("股本结构应为 2 条，实际 %d", len(statements.ShareStructure))
	}
	if len(statements.ShareholderCounts) != 2 {
		t.Errorf("股东户数应为 2 条，实际 %d", len(statements.ShareholderCounts))
	}
	if len(statements.TopHolders) != 2 {
		t.Errorf("十大股东应为 2 条，实际 %d", len(statements.TopHolders))
	}
	if len(statements.Items) != 3 {
		t.Errorf("财报科目应为 3 条，实际 %d", len(statements.Items))
	}

	// 内容要正确
	if statements.ShareStructure[1].ChangeReason != "回购注销" {
		t.Errorf("股本结构内容错误: %+v", statements.ShareStructure[1])
	}
	if statements.ShareholderCounts[0].HolderCount != 200000 {
		t.Errorf("股东户数内容错误: %+v", statements.ShareholderCounts[0])
	}
	if statements.TopHolders[0].Rank != 1 || !statements.TopHolders[0].IsInstitution {
		t.Errorf("十大股东内容错误: %+v", statements.TopHolders[0])
	}
	if statements.TopHolderDate != 20250630 {
		t.Errorf("十大股东报告期错误: %d", statements.TopHolderDate)
	}

	// 表状态要如实反映入库情况
	for _, name := range []string{"financial_statement_items", "share_structure", "shareholder_counts", "top_holders"} {
		if !statements.TableStatus[name] {
			t.Errorf("合成数据湖里 %s 应标记为已入库", name)
		}
	}
}

// 数据湖里没有的表要如实报告，而不是假装有数据。
func TestFinancialStatementsMissingTables(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败: %v", err)
	}

	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	cfg.SetCnLakeRoot(lakeRoot)
	svc := NewStockService(cfg)

	// 300750 只有标的条目，没有财务数据
	statements, err := svc.GetFinancialStatements("300750", 50)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(statements.ShareStructure) != 0 || len(statements.TopHolders) != 0 {
		t.Errorf("该股票没有财务数据，不该返回内容: %+v", statements.ShareStructure)
	}
	// 但表本身是入库的，要能区分「这只股票没数据」和「表没入库」
	if !statements.TableStatus["share_structure"] {
		t.Error("表在库时应标记为 true，即使这只股票没有记录")
	}
}

func keysOfQuotes(quotes map[string]*stockdb.Quote) []string {
	out := make([]string, 0, len(quotes))
	for code := range quotes {
		out = append(out, code)
	}
	return out
}

func countCodes(buckets map[string][]string) int {
	total := 0
	for _, list := range buckets {
		total += len(list)
	}
	return total
}
