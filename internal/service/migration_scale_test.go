package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/portfolio"
)

// buildLegacyFixture 造一个"旧单库"：组合 + 大量评分 + 信号 + 市场状态 + 回测结果。
//
// 行数刻意跨过多个 10k 批次，用来验证 rowid 分批复制真的在推进，
// 而不是靠一次大事务吃满 NAS。
func buildLegacyFixture(t *testing.T, path string, strengthRows int) portfolio.Portfolio {
	t.Helper()
	store, err := portfolio.Open(path)
	if err != nil {
		t.Fatalf("打开旧库失败：%v", err)
	}
	defer store.Close()

	item, err := store.Create(portfolio.PortfolioInput{
		Name: "迁移测试", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}

	const tradeDate int64 = 20261009
	rows := make([]portfolio.StrengthRow, 0, strengthRows)
	for i := 0; i < strengthRows; i++ {
		rows = append(rows, portfolio.StrengthRow{
			TradeDate: tradeDate, StrategyID: portfolio.StrategyTrendGrowth,
			Code: fmt.Sprintf("60%04d.SH", i), Name: fmt.Sprintf("测试%d", i),
			Industry: "测试行业", Market: "sh-main", TotalScore: 50 + float64(i%50),
			Close: 10, Amount: 1e7,
		})
	}
	if err := store.SaveStrength(tradeDate, portfolio.StrategyTrendGrowth, rows); err != nil {
		t.Fatalf("写入评分失败：%v", err)
	}
	if err := store.SaveSignalHistory([]portfolio.SignalHistoryRow{{
		TradeDate: tradeDate, Code: "600000.SH", Name: "测试", ModelID: "m",
		SignalType: "buy", Score: 88, CreatedAt: "t", UpdatedAt: "t",
	}}); err != nil {
		t.Fatalf("写入信号失败：%v", err)
	}
	if err := store.SaveBacktestRun(portfolio.BacktestRun{
		RunID: "run_scale", PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StartDate: "20260101", EndDate: "20260131",
		Status: "finished", CreatedAt: "t", UpdatedAt: "t",
	}); err != nil {
		t.Fatalf("写入回测失败：%v", err)
	}
	return item
}

// 端到端：旧单库 → 核心库 + 分析库，验证行数/索引/完整性/幂等，并给出真实耗时。
func TestMigrateStorageEndToEndWithLargeAnalytics(t *testing.T) {
	if testing.Short() {
		t.Skip("端到端迁移测试在 -short 下跳过")
	}
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")

	const strengthRows = 60000
	item := buildLegacyFixture(t, legacy, strengthRows)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	began := time.Now()
	state, err := svc.MigrateStorage()
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("迁移失败：%v\nstate=%+v", err, state)
	}
	if state.Status != "succeeded" {
		t.Fatalf("迁移未成功：%+v", state)
	}
	if state.CoreQuick != "ok" || state.AnalyticsQuick != "ok" {
		t.Fatalf("quick_check 未通过：core=%q analytics=%q", state.CoreQuick, state.AnalyticsQuick)
	}
	if state.TablesDone != len(portfolio.AnalyticsTables) {
		t.Fatalf("分析表完成数=%d，应为 %d", state.TablesDone, len(portfolio.AnalyticsTables))
	}
	t.Logf("迁移 %d 行评分耗时 %s", strengthRows, elapsed.Round(time.Millisecond))

	// 核心库：组合在，且**没有**分析表（core-only schema 的边界）。
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatalf("打开核心库失败：%v", err)
	}
	defer coreStore.Close()
	migrated, err := coreStore.Get(item.ID)
	if err != nil {
		t.Fatalf("核心库缺少组合：%v", err)
	}
	if migrated.Name != item.Name {
		t.Fatalf("组合名丢失：%q", migrated.Name)
	}
	var n int
	if err := coreStore.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='stock_strength'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("核心库不应包含 stock_strength 表")
	}

	// 分析库：行数一致、索引已重建、quick_check 通过。
	raw, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatalf("打开分析库失败：%v", err)
	}
	defer raw.Close()
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatalf("统计分析库评分失败：%v", err)
	}
	if n != strengthRows {
		t.Fatalf("评分行数=%d，应为 %d", n, strengthRows)
	}
	var signals int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM signal_history`).Scan(&signals); err != nil {
		t.Fatal(err)
	}
	if signals != 1 {
		t.Fatalf("信号行数=%d，应为 1", signals)
	}
	var runs int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("回测表不应复制进分析库（未迁移阶段仍由旧库负责），实际 %d 行", runs)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_strength_lookup'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("分析库缺少 idx_strength_lookup —— 迁移后必须重建索引，否则查询退化全表扫描")
	}
	var check string
	if err := raw.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("分析库 quick_check=%q err=%v", check, err)
	}

	// 源库保持不变（组合与评分都还在）。
	source, err := portfolio.Open(legacy)
	if err != nil {
		t.Fatalf("重新打开源库失败：%v", err)
	}
	defer source.Close()
	if _, err := source.Get(item.ID); err != nil {
		t.Fatalf("源库组合被破坏：%v", err)
	}
	if err := source.DB().QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != strengthRows {
		t.Fatalf("源库评分行数=%d，应为 %d（迁移必须是只读的）", n, strengthRows)
	}

	// 幂等：再次调用直接返回 succeeded，不重复搬数据。
	before, err := os.Stat(analytics)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("重复调用失败：%v", err)
	}
	after, err := os.Stat(analytics)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "succeeded" || again.CompletedAt != state.CompletedAt {
		t.Fatalf("重复调用应幂等返回同一成功状态，得到 %+v", again)
	}
	if before.ModTime() != after.ModTime() || before.Size() != after.Size() {
		t.Fatal("重复调用不应再次写入分析库")
	}

	// 迁移成功后，服务自动启用新库。
	if got := svc.dbPath(); got != core {
		t.Fatalf("迁移成功后组合库应指向核心库，得到 %q", got)
	}
	analyticStore, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	if got := analyticStore.Path(); got != analytics {
		t.Fatalf("迁移成功后分析库应指向 analytics.db，得到 %q", got)
	}
	svc.Close()
}

// 复刻 NAS 上真实卡住的那个状态：核心库已经复制并校验过，但状态文件仍停在
// `phase=core / 5-of-5`，分析库还没生成。恢复时必须**跳过核心复制**
// （不再重扫 4.9GB 源库），直接进分析阶段。
func TestMigrateStorageResumesFromVerifiedCoreWithoutRecopy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")

	const strengthRows = 5000
	item := buildLegacyFixture(t, legacy, strengthRows)

	// 先把核心库复制好（模拟上一次迁移已完成核心阶段）。
	if _, err := portfolio.MigrateCoreDatabase(legacy, core); err != nil {
		t.Fatalf("预置核心库失败：%v", err)
	}
	// 给核心库一个"很久以前"的 mtime：恢复过程若重写它，mtime 会变。
	old := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(core, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(core)
	if err != nil {
		t.Fatal(err)
	}

	// 旧版本留下的状态：卡在 core 5/5。
	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "core", SourcePath: legacy, CorePath: core,
		AnalyticsPath: analytics, CurrentTable: "portfolio_money",
		TablesDone: 5, TablesTotal: 5, StartedAt: time.Now().Format(time.RFC3339),
	})

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	state, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("从 core 5/5 恢复失败：%v", err)
	}
	if state.Status != "succeeded" {
		t.Fatalf("恢复后应 succeeded，得到 %+v", state)
	}

	after, err := os.Stat(core)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("核心库被重新写入（mtime %s → %s）—— 恢复不应重做核心复制",
			before.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(analytics); err != nil {
		t.Fatalf("分析库未生成：%v", err)
	}
	raw, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != strengthRows {
		t.Fatalf("分析库评分行数=%d，应为 %d", n, strengthRows)
	}
	// 核心库里的组合仍在（恢复没有覆盖它）。
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	if _, err := coreStore.Get(item.ID); err != nil {
		t.Fatalf("核心库组合丢失：%v", err)
	}
}

// 上次迁移失败会留下 analytics.db / 空的核心库。旧版本会因此直接拒绝迁移，
// 用户只能手删。这里验证：遗留目标被改名归档，迁移照常完成。
func TestMigrateStorageArchivesStaleTargetsAndRedoes(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")

	const strengthRows = 2000
	item := buildLegacyFixture(t, legacy, strengthRows)

	// 模拟旧版本留下的半成品：schema 已建但没数据的核心库 + 残缺分析库。
	emptyCore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	_ = emptyCore.Close()
	if err := os.WriteFile(analytics, []byte("partial-analytics"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	state, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("遗留目标应被归档而不是让迁移失败：%v", err)
	}
	if state.Status != "succeeded" {
		t.Fatalf("迁移未成功：%+v", state)
	}
	if state.Archived == "" {
		t.Fatal("应记录被归档的遗留文件，否则用户不知道旧文件去了哪")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.stale-*"))
	if len(matches) < 2 {
		t.Fatalf("应归档 2 个遗留文件，实际 %v", matches)
	}
	raw, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatalf("重新迁移后分析库不可用：%v", err)
	}
	if n != strengthRows {
		t.Fatalf("分析库评分行数=%d，应为 %d", n, strengthRows)
	}
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	if _, err := coreStore.Get(item.ID); err != nil {
		t.Fatalf("重新迁移后核心库缺少组合：%v", err)
	}
}

// 上一次已经把 10/10 张表复制完、只卡在收尾时，恢复必须**接手校验**，
// 而不是把几小时的复制成果归档重做。
func TestMigrateStorageFinalizesAlreadyCopiedAnalytics(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")

	const strengthRows = 3000
	item := buildLegacyFixture(t, legacy, strengthRows)

	// 预置"上次已复制完"的结果：核心库 + 分析库都在。
	if _, err := portfolio.MigrateCoreDatabase(legacy, core); err != nil {
		t.Fatal(err)
	}
	if _, err := portfolio.MigrateAnalyticsDatabase(legacy, analytics); err != nil {
		t.Fatalf("预置分析库失败：%v", err)
	}
	// 打一个哨兵：回测表**不在**分析迁移的复制清单里，所以只做收尾时它必然还在；
	// 一旦走"归档 + 重新复制"，新 analytics.db 里不会有这条记录。
	rawSentinel, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawSentinel.Exec(`INSERT INTO portfolio_backtest_run(run_id, portfolio_id, strategy_id, start_date, end_date, created_at, updated_at) VALUES ('run_sentinel', 'pf_x', 'trend_growth', '20260101', '20260131', 't', 't')`); err != nil {
		_ = rawSentinel.Close()
		t.Fatalf("写入哨兵失败：%v", err)
	}
	_ = rawSentinel.Close()

	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "analytics", SourcePath: legacy, CorePath: core,
		AnalyticsPath: analytics, CurrentTable: "完整性检查",
		TablesDone: len(portfolio.AnalyticsTables), TablesTotal: len(portfolio.AnalyticsTables),
		StartedAt: time.Now().Format(time.RFC3339),
	})

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	state, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("接手已完成的分析库失败：%v", err)
	}
	if state.Status != "succeeded" || state.AnalyticsQuick != "ok" {
		t.Fatalf("应收尾成功：%+v", state)
	}
	after, err := os.Stat(analytics)
	if err != nil {
		t.Fatal(err)
	}
	_ = after
	if state.Archived != "" {
		t.Fatalf("不应归档已复制完的分析库：%s", state.Archived)
	}
	// 哨兵还在 → 没有重新复制（复制会先 DELETE 全表）。
	rawCheck, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	var sentinel int
	if err := rawCheck.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE run_id='run_sentinel'`).Scan(&sentinel); err != nil {
		_ = rawCheck.Close()
		t.Fatal(err)
	}
	_ = rawCheck.Close()
	if sentinel != 1 {
		t.Fatal("分析库被重新复制了（哨兵行丢失）—— 收尾应只做校验")
	}
	// 核心库数据仍在，分析库行数正确。
	raw, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != strengthRows {
		t.Fatalf("评分行数=%d，应为 %d", n, strengthRows)
	}
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	if _, err := coreStore.Get(item.ID); err != nil {
		t.Fatalf("核心库组合丢失：%v", err)
	}
}
