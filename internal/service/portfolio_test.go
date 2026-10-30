package service

import (
	"strings"
	"testing"

	"freestock/internal/portfolio"
)

// newPortfolioService 返回一个组合库落在临时目录的服务实例。
//
// 必须隔离：默认路径是 config.json 同目录的 portfolio.db，
// 测试跑到开发机真实的组合库上就麻烦了。
// 也不能用 `svc.stock = nil` 来绕开行情依赖 —— 那样路径推导会退化成
// 相对路径，同一个测试进程里的多个用例会串用同一个库。
func newPortfolioService(t *testing.T) (*PortfolioService, *StockService) {
	t.Helper()
	stock, _ := newIsolatedService(t)
	dbPath := portfolio.DefaultPath(stock.cfg.Path())
	return NewPortfolioServiceAt(stock, dbPath), stock
}

func TestPortfolioServiceCreateAndDetail(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "测试组合", Type: "semi_auto", StrategyID: portfolio.StrategyTrendLowBuy,
		TargetPositionCount: intPtr(3), TotalLayers: (10),
		ScopeType: portfolio.ScopeIndustry, ScopeValue: &portfolio.ScopeValue{Items: []string{"银行"}},
	})
	if err != nil {
		t.Fatalf("创建组合失败: %v", err)
	}
	if created.Name != "测试组合" {
		t.Errorf("名称错误: %s", created.Name)
	}

	// 加两个持仓
	if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "600519", Name: "贵州茅台", EntryPrice: 1500, Layers: intPtr(1),
	}); err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}
	if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "000001", Name: "平安银行", EntryPrice: 10, Layers: intPtr(1),
	}); err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}

	list, err := svc.ListPortfolios()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 1 || list[0].PositionCount != 2 {
		t.Fatalf("组合列表错误: %+v", list)
	}
	if list[0].UsedLayers != 2 || list[0].AvailableLayers != 8 {
		t.Errorf("层数统计错误: 已用 %d 可用 %d", list[0].UsedLayers, list[0].AvailableLayers)
	}

	detail, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if len(detail.Positions) != 2 {
		t.Errorf("应有 2 个持仓: %d", len(detail.Positions))
	}
	if len(detail.Transactions) != 2 {
		t.Errorf("应有 2 条流水: %d", len(detail.Transactions))
	}
	// 这里只验证存储与结构。
	// 现价/盈亏的口径由 TestPortfolioServiceDetailWithRealPrices 专门覆盖 ——
	// 本机 free-stockdb 是否在跑会改变现价能否取到，写死断言会随环境飘。

	// 权重按层数换算：1/10 = 10%
	for _, position := range detail.Positions {
		if position.PositionWeight != 10 {
			t.Errorf("%s 的权重应为 10%%: %v", position.Code, position.PositionWeight)
		}
	}
}

// 持仓要按权重从大到小排序（界面默认顺序）。
func TestPortfolioServicePositionsSortedByWeight(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	created, _ := svc.CreatePortfolio(portfolio.PortfolioInput{Name: "排序组合", TotalLayers: (10)})
	// 层数会被 clamp 到 [1,2]，用总层数不同的组合来区分权重不方便，
	// 直接看排序稳定性：同权重时按代码升序
	for _, code := range []string{"600519", "000001", "300750"} {
		if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: code, EntryPrice: 10, Layers: intPtr(1),
		}); err != nil {
			t.Fatalf("加持仓 %s 失败: %v", code, err)
		}
	}

	detail, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	for i := 1; i < len(detail.Positions); i++ {
		if detail.Positions[i-1].PositionWeight < detail.Positions[i].PositionWeight {
			t.Errorf("未按权重降序: %v", detail.Positions)
			break
		}
	}
	if detail.Positions[0].Code != "000001" {
		t.Errorf("同权重时应按代码升序，实际首行 %s", detail.Positions[0].Code)
	}
}

func TestPortfolioServiceUpdateAndDelete(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	created, _ := svc.CreatePortfolio(portfolio.PortfolioInput{Name: "原名"})
	updated, err := svc.UpdatePortfolio(created.ID, portfolio.PortfolioInput{
		Name: "改名", Type: "auto", TotalLayers: (20),
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Name != "改名" || updated.Type != portfolio.TypeAuto || updated.TotalLayers != 20 {
		t.Errorf("更新未生效: %+v", updated)
	}

	if err := svc.DeletePortfolio(created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if list, _ := svc.ListPortfolios(); len(list) != 0 {
		t.Errorf("删除后应为空: %d", len(list))
	}
	if err := svc.DeletePortfolio(created.ID); err == nil {
		t.Error("重复删除应报错")
	}
}

func TestPortfolioServiceOverviewAndOptions(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	a, _ := svc.CreatePortfolio(portfolio.PortfolioInput{Name: "运行中", TotalLayers: (10)})
	svc.AddPosition(a.ID, portfolio.PositionInput{Code: "600519", EntryPrice: 10, Layers: intPtr(1)})
	svc.CreatePortfolio(portfolio.PortfolioInput{Name: "已归档", Status: "archived"})

	overview, err := svc.GetPortfolioOverview()
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if overview.PortfolioCount != 2 || overview.ActiveCount != 1 {
		t.Errorf("总览错误: 共 %d 活跃 %d", overview.PortfolioCount, overview.ActiveCount)
	}
	if overview.PositionCount != 1 || overview.UsedLayers != 1 {
		t.Errorf("总览持仓统计错误: %d / %d", overview.PositionCount, overview.UsedLayers)
	}

	// 元信息要覆盖界面上的全部下拉项
	options := svc.GetPortfolioOptions()
	if len(options.Types) != 3 {
		t.Errorf("组合类型应有 3 种: %d", len(options.Types))
	}
	if len(options.Strategies) != 5 {
		t.Errorf("策略应有 5 种: %d", len(options.Strategies))
	}
	foundQuality := false
	for _, item := range options.Strategies {
		if item.Key == portfolio.StrategyQualityGrowth {
			foundQuality = true
		}
	}
	if !foundQuality {
		t.Error("缺少质量成长策略选项")
	}
	if len(options.HoldingTypes) == 0 || len(options.ScopeTypes) == 0 || len(options.Markets) == 0 {
		t.Error("范围/市场/仓位性质不该为空")
	}
	if options.DefaultBenchmark != "000300.SH" {
		t.Errorf("默认基准应为 000300.SH: %s", options.DefaultBenchmark)
	}
	if options.DefaultLayers != portfolio.DefaultTotalLayers {
		t.Errorf("默认层数应为 %d: %d", portfolio.DefaultTotalLayers, options.DefaultLayers)
	}
}

// 组合库路径必须与 config.json 同目录，且不能叫 config.json。
func TestPortfolioServiceDatabasePath(t *testing.T) {
	svc, stock := newPortfolioService(t)
	t.Cleanup(svc.Close)

	got := svc.GetDatabasePath()
	want := portfolio.DefaultPath(stock.cfg.Path())
	if got != want {
		t.Errorf("组合库路径错误: %s，期望 %s", got, want)
	}
	if !strings.HasSuffix(got, "portfolio.db") {
		t.Errorf("组合库应为独立文件: %s", got)
	}
}

// 名称留空时要从股票列表补全。
func TestPortfolioServiceFillsNameFromUniverse(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{Name: "补全组合"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	position, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "600519", EntryPrice: 1500, Layers: intPtr(1),
	})
	if err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}
	if position.Name == "" || position.Name == "600519" {
		t.Errorf("名称应从股票列表补全，实际 %q", position.Name)
	}
	t.Logf("600519 补全名称: %s", position.Name)
}

// 现价来自股票列表缓存，能算出真实的浮动盈亏。
func TestPortfolioServiceDetailWithRealPrices(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{Name: "真实行情组合", TotalLayers: (10)})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 用远低于市价的成本，保证必然是浮盈
	if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "600519", EntryPrice: 1, Layers: intPtr(1),
	}); err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}
	// 用远高于市价的成本，保证必然是浮亏
	if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "000001", EntryPrice: 99999, Layers: intPtr(1),
	}); err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}

	detail, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if detail.Summary.MissingPrices != 0 {
		t.Fatalf("真实行情下不该缺现价: %d", detail.Summary.MissingPrices)
	}
	if detail.Summary.PriceDate == "" {
		t.Error("应记录现价对应的交易日")
	}

	byCode := map[string]portfolio.Position{}
	for _, position := range detail.Positions {
		byCode[position.Code] = position
	}
	if byCode["600519"].ProfitLossPct <= 0 {
		t.Errorf("成本 1 元应大幅浮盈: %v", byCode["600519"].ProfitLossPct)
	}
	if byCode["000001"].ProfitLossPct >= 0 {
		t.Errorf("成本 99999 应大幅浮亏: %v", byCode["000001"].ProfitLossPct)
	}
	// 现价应等于收盘价（>0）
	if byCode["600519"].CurrentPrice <= 0 {
		t.Errorf("现价应大于 0: %v", byCode["600519"].CurrentPrice)
	}

	// 手工核对组合收益率 = Σ(逐仓收益率 × 权重)
	want := 0.0
	for _, position := range detail.Positions {
		want += position.ProfitLossPct * position.PositionWeight / 100
	}
	if diff := detail.Summary.TotalReturnPct - want; diff > 0.001 || diff < -0.001 {
		t.Errorf("组合收益率与逐仓加权不符: %v vs %v", detail.Summary.TotalReturnPct, want)
	}

	t.Logf("茅台 %+.2f%%（现价 %.2f）／平安 %+.2f%%（现价 %.2f）→ 组合 %+.2f%%（现价日 %s）",
		byCode["600519"].ProfitLossPct, byCode["600519"].CurrentPrice,
		byCode["000001"].ProfitLossPct, byCode["000001"].CurrentPrice,
		detail.Summary.TotalReturnPct, detail.Summary.PriceDate)
}
