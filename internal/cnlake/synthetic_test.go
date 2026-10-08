// 这是外部测试包：cnlaketest 需要 import cnlake 才能复用行结构，
// 若测试文件也用 package cnlake 就会形成导入环。
package cnlake_test

import (
	"path/filepath"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/cnlake/cnlaketest"
)

// buildSyntheticLake 造一份结构完整的小数据湖。
//
// 本机 cnequity 的 init 卡在 daily_bars 回填，daily_bars / valuation_metrics /
// fund_flow 等还没入库，所以这里按真实 schema 造出来，
// 保证读取逻辑在数据到位前就已经验证过。
func buildSyntheticLake(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := cnlaketest.Build(root); err != nil {
		t.Fatalf("构造合成数据湖失败: %v", err)
	}
	return root
}

func TestSyntheticLakeInstruments(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	rows, err := lake.Instruments()
	if err != nil {
		t.Fatalf("Instruments 失败: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("期望 4 个标的，实际 %d", len(rows))
	}
	if rows[0].Symbol != "000001.SZ" {
		t.Errorf("首个标的不满序: %s", rows[0].Symbol)
	}

	byName := map[string]cnlake.InstrumentRow{}
	for _, row := range rows {
		byName[cnlake.BareSymbol(row.Symbol)] = row
	}
	if got := byName["600519"].Name; got != "贵州茅台" {
		t.Errorf("名称解析错误: %q", got)
	}
	if got := byName["600519"].ListDate; got != cnlaketest.Day(2001, 8, 27) {
		t.Errorf("上市日解析错误: %d", got)
	}
	if got := byName["510980"].AssetType; got != "etf" {
		t.Errorf("资产类型解析错误: %q", got)
	}
}

func TestSyntheticLakeDailyBars(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	rows, err := lake.DailyBars("600519", 2024, 2026)
	if err != nil {
		t.Fatalf("DailyBars 失败: %v", err)
	}
	if len(rows) != 60 {
		t.Fatalf("期望 60 根，实际 %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].TradeDate <= rows[i-1].TradeDate {
			t.Fatalf("未按日期升序: %d <= %d", rows[i].TradeDate, rows[i-1].TradeDate)
		}
	}
	for _, row := range rows {
		if cnlake.BareSymbol(row.Symbol) != "600519" {
			t.Fatalf("混入了其它标的: %s", row.Symbol)
		}
	}
	if got := rows[0].Date(); got != 20240301 {
		t.Errorf("首根日期错误: %d", got)
	}
	if got := rows[len(rows)-1].Date(); got != 20260320 {
		t.Errorf("末根日期错误: %d", got)
	}
}

func TestSyntheticLakePartitionPruning(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	all := lake.DatasetFiles("daily_bars")
	if len(all) != 3 {
		t.Fatalf("应有 3 个分区文件，实际 %d", len(all))
	}

	only2025 := lake.FilesForYears("daily_bars", 2025, 2025)
	if len(only2025) != 1 {
		t.Fatalf("裁剪后应只剩 1 个文件，实际 %d: %v", len(only2025), only2025)
	}
	if filepath.Base(filepath.Dir(only2025[0])) != "trade_date=2025" {
		t.Errorf("裁剪结果不对: %s", only2025[0])
	}

	if got := len(lake.FilesForYears("daily_bars", 2024, 2025)); got != 2 {
		t.Errorf("2024~2025 应命中 2 个文件，实际 %d", got)
	}
	if got := len(lake.FilesForYears("daily_bars", 0, 0)); got != 3 {
		t.Errorf("不限定年份应返回全部，实际 %d", got)
	}
	// 没有分区的数据集永远全取
	if got := len(lake.FilesForYears("instruments", 2020, 2021)); got != 1 {
		t.Errorf("无分区数据集应全取，实际 %d", got)
	}
}

func TestSyntheticLakeValuationsAndFlow(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	valuations, err := lake.Valuations("600519", 2026, 2026)
	if err != nil {
		t.Fatalf("Valuations 失败: %v", err)
	}
	if len(valuations) != 1 {
		t.Fatalf("期望 1 条估值，实际 %d", len(valuations))
	}
	v := valuations[0]
	if v.PeTTM != 19.32 || v.PB != 6.26 {
		t.Errorf("估值字段错误: %+v", v)
	}
	// 数据湖的估值表足以推出 ROE
	if roe := v.PB / v.PeTTM * 100; roe < 32 || roe > 33 {
		t.Errorf("由估值推出的 ROE 异常: %.2f", roe)
	}

	flows, err := lake.FundFlows("600519", 2026, 2026)
	if err != nil {
		t.Fatalf("FundFlows 失败: %v", err)
	}
	if len(flows) != 2 {
		t.Fatalf("期望 2 条资金流，实际 %d", len(flows))
	}
	if flows[0].MainNetInflow >= flows[1].MainNetInflow {
		t.Error("资金流未按日期升序")
	}
	if flows[1].SuperLargeNetInflow != 3.37e8 {
		t.Errorf("超大单字段错误: %v", flows[1].SuperLargeNetInflow)
	}
}

func TestSyntheticLakeCorporateActionsAndAdjFactors(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	actions, err := lake.CorporateActions("600519")
	if err != nil {
		t.Fatalf("CorporateActions 失败: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("期望 1 条分红，实际 %d", len(actions))
	}
	if actions[0].CashDividend != 23.882 {
		t.Errorf("每股派息错误: %v", actions[0].CashDividend)
	}
	if got := actions[0].Date(); got != 20250619 {
		t.Errorf("除权日错误: %d", got)
	}

	factors, err := lake.AdjFactors("600519")
	if err != nil {
		t.Fatalf("AdjFactors 失败: %v", err)
	}
	if len(factors) != 2 {
		t.Fatalf("期望 2 条复权因子，实际 %d", len(factors))
	}
	if factors[0].Factor != 1.0 || factors[1].Factor != 1.019 {
		t.Errorf("复权因子错误: %+v", factors)
	}
}

func TestSyntheticLakeFinanceDatasets(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	structures, err := lake.ShareStructures("600519")
	if err != nil {
		t.Fatalf("ShareStructures 失败: %v", err)
	}
	if len(structures) != 2 {
		t.Fatalf("期望 2 条股本结构，实际 %d", len(structures))
	}
	if structures[1].ChangeReason != "回购注销" {
		t.Errorf("变更原因解析错误: %q", structures[1].ChangeReason)
	}
	if got := structures[0].Date(); got != 20240101 {
		t.Errorf("变更日错误: %d", got)
	}

	counts, err := lake.ShareholderCounts("600519")
	if err != nil {
		t.Fatalf("ShareholderCounts 失败: %v", err)
	}
	if len(counts) != 2 {
		t.Fatalf("期望 2 条股东户数，实际 %d", len(counts))
	}
	if counts[1].HolderCount >= counts[0].HolderCount {
		t.Error("股东户数应为下降趋势")
	}

	holders, err := lake.TopHolders("600519")
	if err != nil {
		t.Fatalf("TopHolders 失败: %v", err)
	}
	if len(holders) != 2 {
		t.Fatalf("期望 2 个十大股东，实际 %d", len(holders))
	}
	if holders[0].HolderRank != 1 || holders[1].HolderRank != 2 {
		t.Errorf("名次顺序错误: %+v", holders)
	}
	if !holders[0].IsInstitution {
		t.Error("机构标记解析错误")
	}
}

func TestSyntheticLakeStatements(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	items, err := lake.StatementItems(t.Context(), "600519", 10)
	if err != nil {
		t.Fatalf("StatementItems 失败: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("期望 3 个财报科目，实际 %d", len(items))
	}
	if items[0].ReportPeriod != "2025Q2" {
		t.Errorf("报告期解析错误: %q", items[0].ReportPeriod)
	}
	if items[0].ItemCode != "TOTAL_OPERATE_INCOME" {
		t.Errorf("科目代码解析错误: %q", items[0].ItemCode)
	}
	if got := items[0].AnnounceDate; got != 20250815 {
		t.Errorf("公告日错误: %d", got)
	}
}

func TestSyntheticLakeIndustryAndCalendar(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))

	industries, err := lake.IndustryMembers()
	if err != nil {
		t.Fatalf("IndustryMembers 失败: %v", err)
	}
	if len(industries) != 2 {
		t.Fatalf("期望 2 条行业成分，实际 %d", len(industries))
	}

	sectors, err := lake.SectorMembers()
	if err != nil {
		t.Fatalf("SectorMembers 失败: %v", err)
	}
	if len(sectors) != 1 || sectors[0].SectorName != "酿酒行业" {
		t.Errorf("板块成分错误: %+v", sectors)
	}

	latest, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatalf("LatestTradeDate 失败: %v", err)
	}
	// is_trading=false 的 2026-09-26 要被跳过
	if latest != 20260930 {
		t.Errorf("最新交易日错误: %d，期望 20260930", latest)
	}

	// TradeDateAt：不晚于指定日期的最近交易日
	at, err := lake.TradeDateAt(20260920)
	if err != nil {
		t.Fatalf("TradeDateAt 失败: %v", err)
	}
	if at > 20260920 {
		t.Errorf("TradeDateAt(%d) 返回了更晚的日期 %d", 20260920, at)
	}
	if at == 0 {
		t.Error("20260920 之前应该有交易日")
	}
	// before<=0 等价于 LatestTradeDate
	if all, err := lake.TradeDateAt(0); err != nil || all != latest {
		t.Errorf("TradeDateAt(0) 应等价于最新交易日: got=%d want=%d err=%v", all, latest, err)
	}
	// 指定最新交易日本身
	if same, err := lake.TradeDateAt(20260930); err != nil || same != 20260930 {
		t.Errorf("TradeDateAt(20260930) = %d (err=%v)，期望 20260930", same, err)
	}
	// 早于日历起点时返回 0 而不是报错
	if early, err := lake.TradeDateAt(19900101); err != nil || early != 0 {
		t.Errorf("早于日历起点应返回 0: got=%d err=%v", early, err)
	}
	t.Logf("trading_calendar 最新=%d，不晚于 20260920 的最近交易日=%d", latest, at)
}

func TestSyntheticLakeIsolatedFromRealLake(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))
	if lake.Root() == cnlake.Detect() && cnlake.Detect() != "" {
		t.Fatal("合成数据湖的路径与真实数据湖相同")
	}
	if !lake.Available() {
		t.Error("合成数据湖应判定为可用")
	}
	if !lake.HasDataset("daily_bars") {
		t.Error("合成数据湖应有 daily_bars")
	}
}

func TestSymbolHelpers(t *testing.T) {
	cases := []struct{ in, bare, lake string }{
		{"600519", "600519", "600519.SH"},
		{"600519.SH", "600519", "600519.SH"},
		{"000001", "000001", "000001.SZ"},
		{"300750", "300750", "300750.SZ"},
		{"510980", "510980", "510980.SH"},
		{"830799", "830799", "830799.BJ"},
		{"430047", "430047", "430047.BJ"},
	}
	for _, item := range cases {
		if got := cnlake.BareSymbol(item.in); got != item.bare {
			t.Errorf("BareSymbol(%q) = %q，期望 %q", item.in, got, item.bare)
		}
		if got := cnlake.LakeSymbol(item.in); got != item.lake {
			t.Errorf("LakeSymbol(%q) = %q，期望 %q", item.in, got, item.lake)
		}
	}
}
