package service

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
)

// 迁移正在跑时再次点击（例如刷新页面后误点）不能排队挂住调用方 ——
// 必须立刻返回"已在进行中"，让页面继续轮询状态。
func TestMigrateStorageRefusesConcurrentCall(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 10)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()

	// 模拟"另一个迁移正在跑"：占住迁移锁。
	storageMigrationMu.Lock()
	done := make(chan struct{})
	var state StorageMigrationState
	var err error
	go func() {
		defer close(done)
		state, err = svc.MigrateStorage()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		storageMigrationMu.Unlock()
		t.Fatal("并发调用被阻塞了 —— 刷新页面后误点会让请求一直挂到迁移结束")
	}
	storageMigrationMu.Unlock()

	if err == nil || !strings.Contains(err.Error(), "已在进行中") {
		t.Fatalf("应返回“已在进行中”，得到 err=%v state=%+v", err, state)
	}
	if state.Status == "succeeded" {
		t.Fatalf("并发调用不应改变迁移状态：%+v", state)
	}
}

// 迁移不挂在调用方身上：发起后即使页面离开/刷新（等价于没人再等结果），
// 后端仍会继续推进，状态文件可以被独立轮询到 succeeded。
func TestMigrationProgressesWithoutCallerWaiting(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 2000)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()

	// 发起后立刻"放弃等待"。
	go func() { _, _ = svc.MigrateStorage() }()

	deadline := time.Now().Add(60 * time.Second)
	sawRunning := false
	for time.Now().Before(deadline) {
		state := svc.GetStorageMigrationState()
		switch state.Status {
		case "running":
			sawRunning = true
		case "succeeded":
			if !sawRunning {
				t.Log("迁移太快，未观察到 running 中间态（不影响结论）")
			}
			if state.CoreQuick != "ok" || state.AnalyticsQuick != "ok" {
				t.Fatalf("完成但校验结果缺失：%+v", state)
			}
			return
		case "failed":
			t.Fatalf("迁移失败：%s", state.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("超时：调用方不再等待后迁移没有继续推进")
}

// 迁移后三个库各司其职，不能串库：
//   - 组合（含迁移后新建的）只在核心库；
//   - 评分/市场状态在分析库；
//   - 回测结果写旧库、也从旧库读回来。
//
// 这条用例直接跑 persistBacktest + ListBacktests + GetBacktestPayload，
// 覆盖"回测结果写错库就读不回来"这类会污染回测页的回归。
func TestSplitStoresKeepBacktestResultsReadable(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analyticsPath := filepath.Join(dir, "analytics.db")

	const strengthRows = 500
	item := buildLegacyFixture(t, legacy, strengthRows)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if got := svc.dbPath(); got != core {
		t.Fatalf("组合库应切到核心库，得到 %q", got)
	}

	// ① 迁移后新建组合：只进核心库，组合列表必须能看到。
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "迁移后新建", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatalf("迁移后新建组合失败：%v", err)
	}
	list, err := svc.ListPortfolios()
	if err != nil {
		t.Fatalf("组合列表失败：%v", err)
	}
	found := false
	for _, p := range list {
		if p.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("迁移后新建的组合没出现在组合列表里（说明组合仍在读旧库）")
	}
	// 它不应出现在旧库里 —— 旧库只是迁移源，不再承担组合读写。
	rawLegacy, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	var inLegacy int
	if err := rawLegacy.QueryRow(`SELECT COUNT(*) FROM portfolio WHERE portfolio_id=?`, created.ID).Scan(&inLegacy); err != nil {
		_ = rawLegacy.Close()
		t.Fatal(err)
	}
	_ = rawLegacy.Close()
	if inLegacy != 0 {
		t.Fatal("迁移后新建的组合被写进了旧库 —— 说明组合写路径没切到核心库")
	}

	// ①b 组合页面本身（详情 + 总览）必须在迁移后照常可用。
	if _, err := svc.GetPortfolioDetail(created.ID); err != nil {
		t.Fatalf("迁移后打开组合详情失败：%v", err)
	}
	if _, err := svc.GetPortfolioOverview(); err != nil {
		t.Fatalf("迁移后组合总览失败：%v", err)
	}
	if _, err := svc.GetPortfolioDetail(item.ID); err != nil {
		t.Fatalf("迁移过来的组合打不开：%v", err)
	}

	// ② 评分与市场状态必须能在分析库里读到（回测取的就是这两样）。
	analyticsStore, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	if got := analyticsStore.Path(); got != analyticsPath {
		t.Fatalf("分析库路径不对：%q", got)
	}
	strengths, err := analyticsStore.LoadStrengthRange(item.StrategyID, 20260101, 20261231)
	if err != nil {
		t.Fatalf("从分析库读评分失败：%v", err)
	}
	if len(strengths) != strengthRows {
		t.Fatalf("分析库评分行数=%d，应为 %d", len(strengths), strengthRows)
	}

	// ③ 回测结果写入 + 读回：写旧库、读旧库，一次成对验证。
	cfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StartDate: "20260101", EndDate: "20260131",
		InitialCapital: 100000,
	}
	result := &backtest.Result{
		Snapshots: []backtest.Snapshot{
			{Date: "20260101", TotalValue: 100000, Cash: 100000},
			{Date: "20260131", TotalValue: 101000, Cash: 50000, PositionValue: 51000, PositionCount: 1},
		},
		Trades: []backtest.Trade{
			{Code: "600000.SH", Name: "测试", Action: "buy", TradeDate: "20260101", Price: 10, Shares: 5000, Amount: 50000},
		},
	}
	resultStore, err := svc.backtestDB()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultStore.Path(), filepath.Join(dir, "backtest.db"); got != want {
		t.Fatalf("迁移后回测库应为 %q，得到 %q", want, got)
	}
	const runID = "run_split_store"
	if err := svc.persistBacktest(resultStore, runID, item, cfg, "000300.SH", result, backtest.Metrics{TotalReturn: 1, TotalTrades: 1}); err != nil {
		t.Fatalf("写回测结果失败：%v", err)
	}

	runs, err := svc.ListBacktests(item.ID)
	if err != nil {
		t.Fatalf("回测列表失败：%v", err)
	}
	foundRun := false
	for _, r := range runs {
		if r.RunID == runID {
			foundRun = true
		}
	}
	if !foundRun {
		t.Fatalf("回测列表读不到刚写入的记录（写/读库不一致）：%+v", runs)
	}
	payload, err := svc.GetBacktestPayload(runID)
	if err != nil {
		t.Fatalf("回测详情失败：%v", err)
	}
	if !payload.Available || len(payload.Daily) != 2 || len(payload.Trades) != 1 {
		t.Fatalf("回测详情不完整：available=%v daily=%d trades=%d",
			payload.Available, len(payload.Daily), len(payload.Trades))
	}

	// ④ 核心库不能出现回测表（否则说明 schema 边界破了）。
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	var n int
	if err := coreStore.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='portfolio_backtest_run'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("核心库里出现了 portfolio_backtest_run —— core-only schema 边界被破坏")
	}

	// ⑤ 删除组合必须连带清掉跨库的引用数据，否则会留下孤儿：
	//    回测库的回测（跨文件不级联）与分析库的四张快照。
	rawBacktestWrite, err := sql.Open("sqlite", resultStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawBacktestWrite.Exec(`INSERT INTO portfolio_backtest_run(run_id, portfolio_id, strategy_id, start_date, end_date, created_at, updated_at) VALUES ('run_orphan_check', ?, 'trend_growth', '20260101', '20260131', 't', 't')`, created.ID); err != nil {
		_ = rawBacktestWrite.Close()
		t.Fatalf("写入待清理回测失败：%v", err)
	}
	_ = rawBacktestWrite.Close()
	if _, err := analyticsStore.DB().Exec(`INSERT INTO portfolio_score_snapshot(portfolio_id, trade_date, created_at, updated_at) VALUES (?, '20261009', 't', 't')`, created.ID); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}

	if err := svc.DeletePortfolio(created.ID); err != nil {
		t.Fatalf("删除组合失败：%v", err)
	}

	rawCheck, err := sql.Open("sqlite", resultStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer rawCheck.Close()
	var leftover int
	if err := rawCheck.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE run_id='run_orphan_check'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatal("删除组合后回测成了孤儿 —— 回测页仍会列出已删除组合的记录")
	}
	// 别的组合的回测不能被动到。
	if err := rawCheck.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE run_id=?`, runID).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 1 {
		t.Fatalf("误删了其他组合的回测记录（剩余 %d）", leftover)
	}
	if err := analyticsStore.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_score_snapshot WHERE portfolio_id=?`, created.ID).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatal("删除组合后分析库快照成了孤儿")
	}
	// 清理成功的删除不该留下墓碑。
	pending, err := coreStore.ListDeletedPortfolios()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("清理成功后墓碑应被清除，仍剩 %v", pending)
	}
}

// 迁移后的写入路径同样要在正确的库上：组合/持仓/资金进核心库，
// 并能在详情/资金视图里读回来；删除后不残留。
func TestMigratedFeatureWritePaths(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 100)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "写入验证", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatalf("新建组合失败：%v", err)
	}
	layers := 2
	pos, err := svc.AddPosition(created.ID, portfolio.PositionInput{
		Code: "600000", Name: "浦发银行", EntryPrice: 10.5, EntryDate: "20261009", Layers: &layers,
	})
	if err != nil {
		t.Fatalf("新增持仓失败：%v", err)
	}
	if pos.Code != "600000" || pos.EntryPrice != 10.5 {
		t.Fatalf("持仓写入异常：%+v", pos)
	}
	detail, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("读详情失败：%v", err)
	}
	if len(detail.Positions) != 1 {
		t.Fatalf("详情持仓数=%d，应为 1", len(detail.Positions))
	}
	// 资金账户属于核心库，必须在同一事务边界内可见。
	if _, err := svc.SetPortfolioCapital(created.ID, 100000); err != nil {
		t.Fatalf("设置本金失败：%v", err)
	}
	if _, err := svc.GetPortfolioMoney(created.ID); err != nil {
		t.Fatalf("读取资金失败：%v", err)
	}
	if _, err := svc.UpdatePortfolio(created.ID, portfolio.PortfolioInput{
		Name: "写入验证-改名", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	}); err != nil {
		t.Fatalf("更新组合失败：%v", err)
	}
	updated, err := svc.GetPortfolioDetail(created.ID)
	if err != nil || updated.Portfolio.Name != "写入验证-改名" {
		t.Fatalf("改名未生效：%v %+v", err, updated)
	}
	// 删除后核心库不应再有这个组合（跨库级联已在另一条用例覆盖）。
	if err := svc.DeletePortfolio(created.ID); err != nil {
		t.Fatalf("删除组合失败：%v", err)
	}
	if _, err := svc.GetPortfolioDetail(created.ID); err == nil {
		t.Fatal("删除后仍能读到组合")
	}
}

// 迁移后各功能读的主路径都要能跑通，且数据来自正确的库。
// 这是"切换后所有功能正常可用、数据准确"的冒烟证据。
func TestMigratedFeatureReadPaths(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	const strengthRows = 300
	item := buildLegacyFixture(t, legacy, strengthRows)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	// 组合类：列表/详情/总览
	list, err := svc.ListPortfolios()
	if err != nil || len(list) != 1 {
		t.Fatalf("组合列表异常：%v len=%d", err, len(list))
	}
	if _, err := svc.GetPortfolioDetail(item.ID); err != nil {
		t.Fatalf("组合详情失败：%v", err)
	}
	ov, err := svc.GetPortfolioOverview()
	if err != nil || ov.PortfolioCount != 1 {
		t.Fatalf("组合总览异常：%v %+v", err, ov)
	}

	// 评分类：摘要与建议都应由分析库支撑
	summary, err := svc.GetStrengthSummary(item.StrategyID)
	if err != nil {
		t.Fatalf("评分摘要失败：%v", err)
	}
	if summary.LatestDate != 20261009 {
		t.Fatalf("评分摘要最新日=%d，应为 20261009（说明读到了空库）", summary.LatestDate)
	}
	plan, err := svc.BuildPlan(item.ID)
	if err != nil {
		t.Fatalf("生成建议失败：%v", err)
	}
	if plan.StrengthDate != 20261009 {
		t.Fatalf("建议使用的评分日=%d，应为 20261009", plan.StrengthDate)
	}

	// 快照与信号
	if _, err := svc.GetScoreSnapshots(item.ID, 10); err != nil {
		t.Fatalf("读取评分快照失败：%v", err)
	}
	sig, err := svc.GetSignalSummary()
	if err != nil || sig.Total != 1 {
		t.Fatalf("信号概览异常：%v %+v", err, sig)
	}

	// 回测页
	if _, err := svc.ListBacktests(""); err != nil {
		t.Fatalf("回测列表失败：%v", err)
	}

	// 启动维护：迁移后不能因为 schema 不对而报错（只允许日志告警）
	svc.MaintainDatabase()
}

// 状态文件停在 running、但进程已经不在了：必须报 interrupted，
// 否则界面会一直显示“迁移中”、按钮永久禁用（用户只能手删状态文件）。
func TestStorageMigrationStateMarksInterrupted(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	old := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "analytics", SourcePath: legacy,
		CorePath:      filepath.Join(dir, "portfolio-core.db"),
		AnalyticsPath: filepath.Join(dir, "analytics.db"),
		CurrentTable:  "完整性检查", TablesDone: 10, TablesTotal: 10, UpdatedAt: old,
	})
	svc := &PortfolioService{}
	defer svc.Close()
	if got := svc.GetStorageMigrationState(); got.Status != "interrupted" {
		t.Fatalf("陈旧 running 应报 interrupted，得到 %q", got.Status)
	}

	// 心跳是新的 → 仍视为进行中（可能在另一个请求里跑）。
	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "analytics", SourcePath: legacy,
		UpdatedAt: time.Now().Format(time.RFC3339),
	})
	if got := svc.GetStorageMigrationState(); got.Status != "running" {
		t.Fatalf("新鲜心跳应保持 running，得到 %q", got.Status)
	}
}

// 启动续跑：状态停在 running（上次被重启打断）时，后台应能接着做完。
func TestResumeInterruptedMigrationContinues(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 100)
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	t.Setenv("FREESTOCK_BACKTEST_DB_PATH", "")

	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "core", SourcePath: legacy,
		CorePath:      filepath.Join(dir, "portfolio-core.db"),
		AnalyticsPath: filepath.Join(dir, "analytics.db"),
		BacktestPath:  filepath.Join(dir, "backtest.db"),
		TablesDone:    0, TablesTotal: 5,
		UpdatedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
	})

	svc := &PortfolioService{}
	defer svc.Close()
	svc.ResumeInterruptedMigration()

	state := svc.GetStorageMigrationState()
	if state.Status != "succeeded" {
		t.Fatalf("续跑后应成功，得到 %+v", state)
	}
	if state.CoreQuick != "ok" || state.AnalyticsQuick != "ok" || state.BacktestQuick != "ok" {
		t.Fatalf("三个库都应校验通过：%+v", state)
	}
	for _, name := range []string{"portfolio-core.db", "analytics.db", "backtest.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s 未生成：%v", name, err)
		}
	}
}

// 删除组合时若跨库清理失败（崩溃/占用），墓碑必须留下；下次启动的
// RetryPendingCleanup 要能补完并清掉墓碑 —— 不依赖"一次删成功"。
func TestRetryPendingCleanupClearsLeftovers(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	item := buildLegacyFixture(t, legacy, 50)
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	t.Setenv("FREESTOCK_BACKTEST_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	// 造一个"已删组合"的现场：组合已从核心库消失、墓碑还在、
	// 回测库与分析库里仍留着它（模拟上次清理失败）。
	coreStore, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "待清理", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatal(err)
	}
	backtestStore, err := svc.backtestDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backtestStore.DB().Exec(`INSERT INTO portfolio_backtest_run(run_id, portfolio_id, strategy_id, start_date, end_date, created_at, updated_at) VALUES ('run_leftover', ?, 'trend_growth', '20260101', '20260131', 't', 't')`, gone.ID); err != nil {
		t.Fatal(err)
	}
	analyticsStore, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyticsStore.DB().Exec(`INSERT INTO portfolio_score_snapshot(portfolio_id, trade_date, created_at, updated_at) VALUES (?, '20261009', 't', 't')`, gone.ID); err != nil {
		t.Fatal(err)
	}
	// 手动做"删组合 + 留墓碑"，但不做外部清理。
	if _, err := coreStore.DB().Exec(`DELETE FROM portfolio WHERE portfolio_id=?`, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := coreStore.DB().Exec(`INSERT OR REPLACE INTO portfolio_deleted(portfolio_id, deleted_at) VALUES (?, 't')`, gone.ID); err != nil {
		t.Fatal(err)
	}

	svc.RetryPendingCleanup()

	var n int
	if err := backtestStore.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE portfolio_id=?`, gone.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("重试后回测库仍有 %d 条孤儿", n)
	}
	if err := analyticsStore.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_score_snapshot WHERE portfolio_id=?`, gone.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("重试后分析库仍有 %d 条孤儿快照", n)
	}
	pending, err := coreStore.ListDeletedPortfolios()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("清理完成后墓碑应被清除，仍剩 %v", pending)
	}
	// 其它组合的数据不能被波及。
	if err := backtestStore.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE portfolio_id=?`, item.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("误删了其它组合的回测（剩余 %d）", n)
	}
}

// 老版本已经"迁移成功"，但那时没有回测库（回测表还在旧库）。升级后再次点击
// 迁移必须只补 backtest.db，不能重搬核心/分析库，也不能什么都不做
// （否则 backtestDB 回落旧库 → 新建组合的回测又撞 787）。
func TestMigrateStorageUpgradesSucceededStateWithBacktestDB(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	backtestPath := filepath.Join(dir, "backtest.db")
	buildLegacyFixture(t, legacy, 200)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	t.Setenv("FREESTOCK_BACKTEST_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	coreBefore, err := os.Stat(core)
	if err != nil {
		t.Fatal(err)
	}
	analyticsBefore, err := os.Stat(analytics)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟"老版本状态"：成功状态里没有 backtestPath，回测库也不存在。
	if err := os.Remove(backtestPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "succeeded", SourcePath: legacy, CorePath: core,
		AnalyticsPath: analytics, CoreQuick: "ok", AnalyticsQuick: "ok",
		CompletedAt: time.Now().Format(time.RFC3339),
	})

	state, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("升级迁移失败：%v", err)
	}
	if state.Status != "succeeded" || state.BacktestQuick != "ok" {
		t.Fatalf("升级后应成功且回测库校验通过：%+v", state)
	}
	if got := state.BacktestPath; got != backtestPath {
		t.Fatalf("状态里的回测库路径=%q，应为 %q", got, backtestPath)
	}
	// 核心库/分析库不能被动到（同一个文件）。
	coreAfter, err := os.Stat(core)
	if err != nil {
		t.Fatal(err)
	}
	analyticsAfter, err := os.Stat(analytics)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(coreBefore, coreAfter) {
		t.Fatal("升级补齐时核心库被重建了")
	}
	if !os.SameFile(analyticsBefore, analyticsAfter) {
		t.Fatal("升级补齐时分析库被重搬了")
	}
	// 回测记录确实搬进新库。
	raw, err := sql.Open("sqlite", backtestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var runs int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("回测库运行记录=%d，应为 1", runs)
	}
}

// 未迁移的旧部署必须完全保持原行为：回测库回退到旧单库，读写照旧。
func TestBacktestDBFallsBackToLegacyBeforeMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	item := buildLegacyFixture(t, legacy, 20)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	t.Setenv("FREESTOCK_BACKTEST_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	store, err := svc.backtestDB()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Path(); got != legacy {
		t.Fatalf("未迁移时应回退旧库 %q，得到 %q", legacy, got)
	}
	// 旧库里的回测记录照常可读。
	runs, err := svc.ListBacktests(item.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("旧库回测列表异常：%v len=%d", err, len(runs))
	}
}

// 迁移后为**新建组合**写回测必须成功。旧实现把回测表留在旧库，而旧库里没有
// 迁移后新建的 portfolio 行 → 外键 787（已复现）。回测库去掉 portfolio 外键后
// 这层引用由应用层维护，写入不再依赖旧库有没有那行。
func TestBacktestWriteForNewPortfolioAfterMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 20)
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	t.Setenv("FREESTOCK_BACKTEST_DB_PATH", "")
	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "新组合", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := svc.backtestDB()
	if err != nil {
		t.Fatal(err)
	}
	cfg := backtest.Config{
		PortfolioID: created.ID, PortfolioName: created.Name, StrategyID: created.StrategyID,
		StartDate: "20260101", EndDate: "20260131", InitialCapital: 100000,
	}
	res := &backtest.Result{Snapshots: []backtest.Snapshot{{Date: "20260101", TotalValue: 100000}}}
	if err := svc.persistBacktest(store, "run_new_pf", *created, cfg, "000300.SH", res, backtest.Metrics{}); err != nil {
		t.Fatalf("迁移后为新组合写回测失败（跨库外键未解除？）：%v", err)
	}
	if _, err := svc.GetBacktestPayload("run_new_pf"); err != nil {
		t.Fatalf("新组合的回测读不回来：%v", err)
	}
}

// 迁移后三个库的表所有权必须**互不重叠**。这是"切换数据库不会错位"的结构性
// 保证：任何一处把查询发到错库，都会得到 no such table，而不是静默 0 行。
func TestMigratedDatabasesOwnDisjointTables(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	buildLegacyFixture(t, legacy, 100)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	coreTables := listUserTables(t, core)
	// `portfolio_deleted` 是核心库自己的"跨库清理墓碑"，属于核心库记账数据。
	wantCore := map[string]bool{
		"portfolio": true, "portfolio_position": true, "portfolio_position_txn": true,
		"position_shares": true, "portfolio_money": true, "portfolio_deleted": true,
	}
	analyticsTables := listUserTables(t, analytics)
	wantAnalytics := map[string]bool{}
	for _, table := range portfolio.AnalyticsTables {
		wantAnalytics[table] = true
	}

	if got, want := len(coreTables), len(wantCore); got != want {
		t.Fatalf("核心库表数=%d，应为 %d：%v", got, want, coreTables)
	}
	for name := range coreTables {
		if !wantCore[name] {
			t.Errorf("核心库多出表 %q —— 说明有分析/回测 schema 混进来了", name)
		}
	}
	if got, want := len(analyticsTables), len(wantAnalytics); got != want {
		t.Fatalf("分析库表数=%d，应为 %d：%v", got, want, analyticsTables)
	}
	for name := range analyticsTables {
		if !wantAnalytics[name] {
			t.Errorf("分析库多出表 %q —— 误查该表会返回空结果而不是报错", name)
		}
	}
	// 两个库不能有任何同名表。
	for name := range coreTables {
		if analyticsTables[name] {
			t.Errorf("表 %q 同时存在于核心库和分析库 —— 所有权重叠，切换时会错位", name)
		}
	}

	// 回测库：恰好三张回测表，且与另外两个库不重叠。
	backtestPath := filepath.Join(dir, "backtest.db")
	backtestTables := listUserTables(t, backtestPath)
	wantBacktest := map[string]bool{}
	for _, table := range portfolio.BacktestTables {
		wantBacktest[table] = true
	}
	if got, want := len(backtestTables), len(wantBacktest); got != want {
		t.Fatalf("回测库表数=%d，应为 %d：%v", got, want, backtestTables)
	}
	for name := range backtestTables {
		if !wantBacktest[name] {
			t.Errorf("回测库多出表 %q", name)
		}
		if coreTables[name] || analyticsTables[name] {
			t.Errorf("表 %q 在多个库中重复出现", name)
		}
	}
}

// listUserTables 返回库里的用户表集合（不含 sqlite_ 内部表）。
func listUserTables(t *testing.T, path string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// 迁移后 BuildPlan 的低吸广度必须查分析库。查核心库会直接
// "no such table: stock_strength"，整个建议页报错。
func TestBuildPlanAfterMigrationReadsBreadthFromAnalytics(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	const strengthRows = 300
	item := buildLegacyFixture(t, legacy, strengthRows)

	t.Setenv("FREESTOCK_PORTDB_PATH", "")
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	plan, err := svc.BuildPlan(item.ID)
	if err != nil {
		t.Fatalf("迁移后生成建议失败（广度可能查了核心库）：%v", err)
	}
	if plan == nil || plan.StrengthDate != 20261009 {
		t.Fatalf("建议结果异常：%+v", plan)
	}
}

// 迁移后信号概览必须读分析库（signal_history 在分析库）。
func TestSignalSummaryAfterMigrationReadsAnalytics(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	buildLegacyFixture(t, legacy, 50)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	summary, err := svc.GetSignalSummary()
	if err != nil {
		t.Fatalf("迁移后信号概览失败（可能查了核心库）：%v", err)
	}
	if !summary.Generated || summary.Total != 1 {
		t.Fatalf("信号概览应从分析库读到 1 条，得到 %+v", summary)
	}
}

// 迁移状态说成功、但文件被人删了时，必须回退旧库并报警，
// 不能静默新建一个空库（那正是"组合列表为空"的成因）。
func TestMissingMigratedFilesFallBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	buildLegacyFixture(t, legacy, 20)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")

	svc := &PortfolioService{}
	defer svc.Close()
	if _, err := svc.MigrateStorage(); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if got := svc.dbPath(); got != core {
		t.Fatalf("迁移成功后应使用核心库，得到 %q", got)
	}
	// 删掉两个迁移产出的文件（模拟误删/磁盘回滚）。
	if err := os.Remove(core); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(analytics); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if got := svc.dbPath(); got != legacy {
		t.Fatalf("核心库缺失应回退旧库，得到 %q", got)
	}
	store, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Path(); got != legacy {
		t.Fatalf("分析库缺失应回退旧库，得到 %q", got)
	}
}
