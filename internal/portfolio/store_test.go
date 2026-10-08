package portfolio

import (
	"path/filepath"
	"testing"
)

// fakePrices 是固定的现价来源。
type fakePrices map[string]PricePoint

func (f fakePrices) LatestClose(codes []string) (map[string]PricePoint, error) {
	out := map[string]PricePoint{}
	for _, code := range codes {
		if point, ok := f[code]; ok {
			out[code] = point
		}
	}
	return out, nil
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "portfolio.db"))
	if err != nil {
		t.Fatalf("打开组合库失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// ---------------------------------------------------------------------------
// 归一化（对齐原项目 _normalize_* 规则）
// ---------------------------------------------------------------------------

func TestNormalizeType(t *testing.T) {
	cases := map[string]string{
		"manual": TypeManual, "手动": TypeManual, "手动组合": TypeManual,
		"manual_portfolio": TypeManual,
		"semi_auto":        TypeSemiAuto, "半自动": TypeSemiAuto, "semi": TypeSemiAuto,
		"semi-auto": TypeSemiAuto,
		"auto":      TypeAuto, "自动": TypeAuto, "ai": TypeAuto, "ai管理": TypeAuto,
		"": TypeManual, "乱七八糟": TypeManual,
	}
	for input, want := range cases {
		if got := NormalizeType(input); got != want {
			t.Errorf("NormalizeType(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestNormalizeStrategyAndStatus(t *testing.T) {
	if got := NormalizeStrategy("trend_pullback_low_buy", ""); got != StrategyTrendLowBuy {
		t.Errorf("低吸策略不该被改写: %s", got)
	}
	// 无法识别 → 回退 trend_growth
	if got := NormalizeStrategy("不存在", "manual"); got != StrategyTrendGrowth {
		t.Errorf("未知策略应回退 trend_growth: %s", got)
	}

	for input, want := range map[string]string{
		"active": StatusActive, "paused": StatusPaused, "archived": StatusArchived,
		"": StatusActive, "bogus": StatusActive,
	} {
		if got := NormalizeStatus(input); got != want {
			t.Errorf("NormalizeStatus(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestNormalizeScopeTypeAndValue(t *testing.T) {
	for input, want := range map[string]string{
		"all": ScopeAll, "all_market": ScopeAll, "market": ScopeAll,
		"codes": ScopeManualCodes, "manual": ScopeManualCodes, "manual_codes": ScopeManualCodes,
		"industry": ScopeIndustry, "concept": ScopeConcept,
		"": ScopeAll, "junk": ScopeAll,
	} {
		if got := NormalizeScopeType(input); got != want {
			t.Errorf("NormalizeScopeType(%q) = %q，期望 %q", input, got, want)
		}
	}

	// 全选三个市场 → 视为"全部市场"，不写 markets
	raw := NormalizeScopeValue(ScopeAll, ScopeValue{Markets: []string{"main", "gem", "star"}})
	if got := ParseScopeValue(raw); len(got.Markets) != 0 {
		t.Errorf("三个市场全选应归一为「全部市场」: %s", raw)
	}
	// 部分选择 → 保留，且别名要能归一
	raw = NormalizeScopeValue(ScopeAll, ScopeValue{Markets: []string{"主板", "kcb"}})
	got := ParseScopeValue(raw)
	if len(got.Markets) != 2 || got.Markets[0] != "main" || got.Markets[1] != "star" {
		t.Errorf("市场别名归一错误: %v", got.Markets)
	}
	// 股票代码要去后缀、去重
	raw = NormalizeScopeValue(ScopeManualCodes, ScopeValue{
		Codes: []string{"600519.SH", "600519", "000001.SZ"},
	})
	got = ParseScopeValue(raw)
	if len(got.Codes) != 2 || got.Codes[0] != "600519" || got.Codes[1] != "000001" {
		t.Errorf("代码应去后缀并去重: %v", got.Codes)
	}
}

func TestNormalizeDate(t *testing.T) {
	cases := map[string]string{
		"20260930": "20260930", "2026-09-30": "20260930",
		"2026/09/30": "20260930", "2026.09.30": "20260930",
		"": "", "abc": "", "2026093": "",
	}
	for input, want := range cases {
		if got := NormalizeDate(input); got != want {
			t.Errorf("NormalizeDate(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestWeightFromLayers(t *testing.T) {
	// 分母是层数上限，不是市值 —— 原项目的核心口径
	if got := weightFromLayers(1, 10); got != 10 {
		t.Errorf("1/10 层 = %v%%，期望 10", got)
	}
	if got := weightFromLayers(3, 10); got != 30 {
		t.Errorf("3/10 层 = %v%%，期望 30", got)
	}
	// 分母为 0 时按 1 处理，不能除零
	if got := weightFromLayers(1, 0); got != 100 {
		t.Errorf("分母为 0 应按 1 处理: %v", got)
	}
}

// ---------------------------------------------------------------------------
// 组合 CRUD
// ---------------------------------------------------------------------------

func TestCreatePortfolioWithDefaults(t *testing.T) {
	store := newStore(t)

	item, err := store.Create(PortfolioInput{Name: "  我的组合  "})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if item.Name != "我的组合" {
		t.Errorf("名称应 strip: %q", item.Name)
	}
	if item.Type != TypeManual || item.TypeLabel != "手动组合" {
		t.Errorf("默认类型应为手动组合: %s / %s", item.Type, item.TypeLabel)
	}
	if item.StrategyID != StrategyTrendGrowth {
		t.Errorf("默认策略应为趋势成长: %s", item.StrategyID)
	}
	if item.BenchmarkCode != "000300.SH" {
		t.Errorf("默认基准应为 000300.SH: %s", item.BenchmarkCode)
	}
	if item.Status != StatusActive {
		t.Errorf("默认状态应为 active: %s", item.Status)
	}
	if item.TargetPositionCount != 5 {
		t.Errorf("默认目标持仓数应为 5: %d", item.TargetPositionCount)
	}
	if item.TotalLayers != DefaultTotalLayers {
		t.Errorf("默认层数应为 %d: %d", DefaultTotalLayers, item.TotalLayers)
	}
	// 调仓频率是死字段，恒为 signal
	if item.RebalanceFrequency != RotationMode {
		t.Errorf("调仓频率恒为 signal: %s", item.RebalanceFrequency)
	}
	// 执行模式三元组
	if item.ExecutionMode != "manual_review" || item.AllowApplyRotation || !item.RequireManualConfirmation {
		t.Errorf("手动组合的执行模式错误: %+v", item)
	}
}

func TestCreatePortfolioValidation(t *testing.T) {
	store := newStore(t)

	if _, err := store.Create(PortfolioInput{Name: "   "}); err == nil {
		t.Error("空名称应该报错")
	}

	// 最大持仓数不能小于目标持仓数
	item, err := store.Create(PortfolioInput{
		Name: "组合A", TargetPositionCount: 8, MaxStockCount: 3,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if item.MaxStockCount != 8 {
		t.Errorf("max_stock_count 应被抬到 target: %d", item.MaxStockCount)
	}

	// 目标持仓数下限 1
	item, err = store.Create(PortfolioInput{Name: "组合B", TargetPositionCount: -5})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if item.TargetPositionCount != 5 {
		t.Errorf("非法目标数应回退默认 5: %d", item.TargetPositionCount)
	}
}

func TestAutoPortfolioExecutionMode(t *testing.T) {
	store := newStore(t)

	item, err := store.Create(PortfolioInput{Name: "自动组合", Type: "auto"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if item.ExecutionMode != "auto_ready" {
		t.Errorf("自动组合应为 auto_ready: %s", item.ExecutionMode)
	}
	if !item.AllowApplyRotation {
		t.Error("自动组合应允许一键应用建议")
	}
	if item.RequireManualConfirmation {
		t.Error("自动组合不需要人工确认")
	}
}

func TestUpdateAndListPortfolios(t *testing.T) {
	store := newStore(t)

	created, err := store.Create(PortfolioInput{Name: "原名称"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	updated, err := store.Update(created.ID, PortfolioInput{
		Name: "新名称", Type: "semi_auto", StrategyID: StrategyTrendLowBuy,
		TargetPositionCount: 3, TotalLayers: 20,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Name != "新名称" || updated.Type != TypeSemiAuto || updated.StrategyID != StrategyTrendLowBuy {
		t.Errorf("更新未生效: %+v", updated)
	}
	if updated.TotalLayers != 20 {
		t.Errorf("层数未更新: %d", updated.TotalLayers)
	}
	// 创建时间要保留
	if updated.CreatedAt != created.CreatedAt {
		t.Error("更新不应改变创建时间")
	}

	// 不存在的组合
	if _, err := store.Update("pf_nope", PortfolioInput{Name: "x"}); err == nil {
		t.Error("更新不存在的组合应报错")
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 个组合，实际 %d", len(list))
	}
}

func TestDeletePortfolioCascades(t *testing.T) {
	store := newStore(t)

	item, _ := store.Create(PortfolioInput{Name: "待删除"})
	if _, err := store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 1500}); err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}

	if err := store.Delete(item.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := store.Get(item.ID); err == nil {
		t.Error("删除后不该还能读到")
	}

	// 持仓与流水应被级联清掉
	var positions, txns int
	store.db.QueryRow(`SELECT COUNT(*) FROM portfolio_position WHERE portfolio_id=?`, item.ID).Scan(&positions)
	store.db.QueryRow(`SELECT COUNT(*) FROM portfolio_position_txn WHERE portfolio_id=?`, item.ID).Scan(&txns)
	if positions != 0 || txns != 0 {
		t.Errorf("级联删除不完整: 持仓 %d，流水 %d", positions, txns)
	}

	if err := store.Delete(item.ID); err == nil {
		t.Error("重复删除应报错")
	}
}

// ---------------------------------------------------------------------------
// 持仓
// ---------------------------------------------------------------------------

func TestAddPositionClampsLayers(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", TotalLayers: 10})

	// 原项目 add_position 把层数 clamp 到 [1,2] —— 这是如实的照搬
	position, err := store.AddPosition(item.ID, PositionInput{
		Code: "600519.SH", Name: "贵州茅台", EntryPrice: 1500.123456, EntryDate: "2026-09-30", Layers: 9,
	})
	if err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}
	if position.PositionLayers != maxManualLayers {
		t.Errorf("层数应被 clamp 到 %d: %d", maxManualLayers, position.PositionLayers)
	}
	// 代码去后缀
	if position.Code != "600519" {
		t.Errorf("代码应为裸码: %s", position.Code)
	}
	// 权重按层数换算：2/10 = 20%
	if position.PositionWeight != 20 {
		t.Errorf("权重应为 20%%: %v", position.PositionWeight)
	}
	// 日期归一化
	if position.EntryDate != "20260930" {
		t.Errorf("日期应归一为 YYYYMMDD: %s", position.EntryDate)
	}
	// 价格保留 4 位
	if position.EntryPrice != 1500.1235 {
		t.Errorf("价格应保留 4 位小数: %v", position.EntryPrice)
	}
	if position.SourceType != SourceManual || position.LastAction != "manual_add" {
		t.Errorf("来源/动作错误: %s / %s", position.SourceType, position.LastAction)
	}

	// 层数为 0 → 按 总层数/目标持仓数 取整再 clamp
	second, err := store.AddPosition(item.ID, PositionInput{Code: "000001", EntryPrice: 10})
	if err != nil {
		t.Fatalf("加持仓失败: %v", err)
	}
	if second.PositionLayers < 1 || second.PositionLayers > maxManualLayers {
		t.Errorf("默认层数应在 [1,%d]: %d", maxManualLayers, second.PositionLayers)
	}
}

func TestAddPositionRejectsDuplicate(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合"})

	if _, err := store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 1500}); err != nil {
		t.Fatalf("第一次应成功: %v", err)
	}
	// 同一个组合一只股票只能有一条
	if _, err := store.AddPosition(item.ID, PositionInput{Code: "600519.SH", EntryPrice: 1600}); err == nil {
		t.Error("重复添加应报错")
	}
	// 空代码
	if _, err := store.AddPosition(item.ID, PositionInput{Code: "  "}); err == nil {
		t.Error("空代码应报错")
	}
	// 负价格
	if _, err := store.AddPosition(item.ID, PositionInput{Code: "000001", EntryPrice: -1}); err == nil {
		t.Error("负价格应报错")
	}
	// 不存在的组合
	if _, err := store.AddPosition("pf_nope", PositionInput{Code: "000001", EntryPrice: 1}); err == nil {
		t.Error("不存在的组合应报错")
	}
}

// 名称留空时回填代码（原项目行为）。
func TestAddPositionFillsName(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合"})

	position, err := store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 10})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if position.Name != "600519" {
		t.Errorf("名称应回填代码: %q", position.Name)
	}

	// 非法的仓位性质应归一为空
	position, _ = store.AddPosition(item.ID, PositionInput{Code: "000001", EntryPrice: 10, HoldingType: "乱填"})
	if position.HoldingType != "" {
		t.Errorf("非法仓位性质应清空: %q", position.HoldingType)
	}
	// 合法的保留
	position, _ = store.AddPosition(item.ID, PositionInput{Code: "000002", EntryPrice: 10, HoldingType: HoldingBase})
	if position.HoldingType != HoldingBase {
		t.Errorf("仓位性质应保留: %q", position.HoldingType)
	}
}

// 流水是唯一审计账：新增写 buy、删除写 clear，且只增不改。
func TestTransactionsAreAppendOnly(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合"})

	store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 1500, Layers: 1})
	store.DeletePosition(item.ID, "600519")

	txns, err := store.ListTransactions(item.ID)
	if err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if len(txns) != 2 {
		t.Fatalf("应有 2 条流水，实际 %d", len(txns))
	}
	// 最新在前
	if txns[0].Action != "clear" || txns[0].LayersDelta != -1 {
		t.Errorf("清仓流水错误: %+v", txns[0])
	}
	if txns[1].Action != "buy" || txns[1].LayersDelta != 1 {
		t.Errorf("买入流水错误: %+v", txns[1])
	}
	if txns[0].SourceSignalID != "manual" || txns[1].SourceSignalID != "manual" {
		t.Error("手动操作的 source_signal_id 应为 manual")
	}

	// 删持仓后持仓表应为空，但流水保留
	positions, _ := store.ListPositions(item.ID)
	if len(positions) != 0 {
		t.Errorf("持仓应已删除: %d", len(positions))
	}

	if err := store.DeletePosition(item.ID, "600519"); err == nil {
		t.Error("删除不存在的持仓应报错")
	}
}

// ---------------------------------------------------------------------------
// 汇总计算（核心口径）
// ---------------------------------------------------------------------------

func TestSummarizeWeightedReturn(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", TotalLayers: 10})

	// 仓位 A：1 层 → 10%，成本 100 → 现价 110（+10%）
	store.AddPosition(item.ID, PositionInput{Code: "600519", Name: "A", EntryPrice: 100, Layers: 1})
	// 仓位 B：1 层 → 10%，成本 100 → 现价 90（-10%）
	store.AddPosition(item.ID, PositionInput{Code: "000001", Name: "B", EntryPrice: 100, Layers: 1})

	prices := fakePrices{
		"600519": {Close: 110, Date: "20260930"},
		"000001": {Close: 90, Date: "20260930"},
	}

	detail, err := store.Detail(item.ID, prices)
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	summary := detail.Summary

	if summary.PositionCount != 2 || summary.UsedLayers != 2 {
		t.Errorf("持仓数/层数错误: %d / %d", summary.PositionCount, summary.UsedLayers)
	}
	if summary.AvailableLayers != 8 {
		t.Errorf("可用层数应为 8: %d", summary.AvailableLayers)
	}
	if summary.PositionRatio != 20 {
		t.Errorf("仓位占比应为 20%%: %v", summary.PositionRatio)
	}

	// 组合收益率 = 10%×0.1 + (-10%)×0.1 = 1% - 1% = 0
	//
	// 这是原项目的关键口径：加权和**不除以权重之和**，也不含现金。
	// 两个 10% 仓位、一涨一跌 → 组合收益正好 0。
	if summary.TotalReturnPct != 0 {
		t.Errorf("组合收益率应为 0，实际 %v", summary.TotalReturnPct)
	}

	// 逐仓盈亏
	byCode := map[string]Position{}
	for _, position := range detail.Positions {
		byCode[position.Code] = position
	}
	if byCode["600519"].ProfitLossPct != 10 {
		t.Errorf("A 的盈亏应为 +10%%: %v", byCode["600519"].ProfitLossPct)
	}
	if byCode["000001"].ProfitLossPct != -10 {
		t.Errorf("B 的盈亏应为 -10%%: %v", byCode["000001"].ProfitLossPct)
	}
	// 现价与日期要回填
	if byCode["600519"].CurrentPrice != 110 || byCode["600519"].PriceDate != "20260930" {
		t.Errorf("现价未回填: %+v", byCode["600519"])
	}
}

// 取不到现价时记 0，并且不计入收益率 —— 与原实现的 left join 行为一致。
func TestSummarizeMissingPrice(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", TotalLayers: 10})

	store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 100, Layers: 1})
	store.AddPosition(item.ID, PositionInput{Code: "000001", EntryPrice: 100, Layers: 1})

	// 只给一只股票价格
	prices := fakePrices{"600519": {Close: 120, Date: "20260930"}}
	detail, err := store.Detail(item.ID, prices)
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}

	if detail.Summary.MissingPrices != 1 {
		t.Errorf("应记录 1 只缺现价: %d", detail.Summary.MissingPrices)
	}
	for _, position := range detail.Positions {
		if position.Code == "000001" {
			// 缺价格 → 现价 0、盈亏 0（不是 NULL，也不是报错）
			if position.CurrentPrice != 0 || position.ProfitLossPct != 0 {
				t.Errorf("缺现价时应记 0: %+v", position)
			}
		}
	}
	// 只有 A 贡献收益：20% × 0.1 = 2%
	if detail.Summary.TotalReturnPct != 2 {
		t.Errorf("组合收益率应为 2%%，实际 %v", detail.Summary.TotalReturnPct)
	}
}

// 成本为 0 时盈亏记 0，不能除零。
func TestSummarizeZeroCost(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合"})
	store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 0, Layers: 1})

	detail, err := store.Detail(item.ID, fakePrices{"600519": {Close: 100, Date: "20260930"}})
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if detail.Summary.TotalReturnPct != 0 {
		t.Errorf("成本为 0 时收益率应为 0: %v", detail.Summary.TotalReturnPct)
	}
}

// ---------------------------------------------------------------------------
// 持久化
// ---------------------------------------------------------------------------

// 关掉再打开必须还能读到 —— 这是"组合数据不能丢"的底线。
func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "portfolio.db")

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	item, _ := store.Create(PortfolioInput{
		Name: "持久化组合", Type: "semi_auto", StrategyID: StrategyTrendLowBuy,
		TotalLayers: 20, TargetPositionCount: 3,
		ScopeType: ScopeIndustry, ScopeValue: ScopeValue{Items: []string{"银行", "证券"}, Markets: []string{"main"}},
		Notes: "备注内容",
	})
	store.AddPosition(item.ID, PositionInput{Code: "600519", Name: "贵州茅台", EntryPrice: 1500, Layers: 2})
	store.Close()

	// 重开
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.Get(item.ID)
	if err != nil {
		t.Fatalf("重开后读不到组合: %v", err)
	}
	if got.Name != "持久化组合" || got.Type != TypeSemiAuto || got.StrategyID != StrategyTrendLowBuy {
		t.Errorf("组合字段未持久化: %+v", got)
	}
	if got.TotalLayers != 20 || got.TargetPositionCount != 3 {
		t.Errorf("数值字段未持久化: %+v", got)
	}
	if got.Notes != "备注内容" {
		t.Errorf("备注未持久化: %q", got.Notes)
	}
	// 范围 JSON 往返
	scope := ParseScopeValue(got.ScopeValueJSON)
	if len(scope.Items) != 2 || scope.Items[0] != "银行" || len(scope.Markets) != 1 {
		t.Errorf("范围未持久化: %+v", scope)
	}

	positions, _ := reopened.ListPositions(item.ID)
	if len(positions) != 1 || positions[0].Code != "600519" || positions[0].PositionLayers != 2 {
		t.Errorf("持仓未持久化: %+v", positions)
	}
	if positions[0].EntryPrice != 1500 {
		t.Errorf("成本价未持久化: %v", positions[0].EntryPrice)
	}

	// 汇总信息也要能重算出来
	if got.PositionCount != 1 || got.UsedLayers != 2 || got.AvailableLayers != 18 {
		t.Errorf("重开后汇总错误: 持仓%d 已用%d 可用%d", got.PositionCount, got.UsedLayers, got.AvailableLayers)
	}
}

func TestOverview(t *testing.T) {
	store := newStore(t)

	a, _ := store.Create(PortfolioInput{Name: "活跃组合", TotalLayers: 10})
	store.AddPosition(a.ID, PositionInput{Code: "600519", EntryPrice: 100, Layers: 1})
	b, _ := store.Create(PortfolioInput{Name: "归档组合", Status: "archived", TotalLayers: 20})
	store.AddPosition(b.ID, PositionInput{Code: "000001", EntryPrice: 10, Layers: 2})

	overview, err := store.Overview()
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if overview.PortfolioCount != 2 {
		t.Errorf("组合数应为 2: %d", overview.PortfolioCount)
	}
	// active_count 不含 archived
	if overview.ActiveCount != 1 {
		t.Errorf("活跃组合数应为 1: %d", overview.ActiveCount)
	}
	if overview.PositionCount != 2 {
		t.Errorf("持仓总数应为 2: %d", overview.PositionCount)
	}
	if overview.UsedLayers != 3 || overview.TotalLayers != 30 {
		t.Errorf("层数统计错误: 已用 %d / 总 %d", overview.UsedLayers, overview.TotalLayers)
	}
}

func TestDefaultPath(t *testing.T) {
	got := DefaultPath(`C:\Users\x\AppData\Roaming\FreeStock\config.json`)
	if filepath.Base(got) != "portfolio.db" {
		t.Errorf("组合库文件名应为 portfolio.db: %s", got)
	}
	if filepath.Dir(got) != `C:\Users\x\AppData\Roaming\FreeStock` {
		t.Errorf("组合库应与 config.json 同目录: %s", got)
	}
}
