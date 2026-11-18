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
	legacyStore, err := svc.legacyDB()
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run_split_store"
	if err := svc.persistBacktest(legacyStore, runID, item, cfg, "000300.SH", result, backtest.Metrics{TotalReturn: 1, TotalTrades: 1}); err != nil {
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
	//    旧库的回测（FK 不跨文件，不会级联）与分析库的四张快照。
	rawLegacyWrite, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawLegacyWrite.Exec(`INSERT INTO portfolio_backtest_run(run_id, portfolio_id, strategy_id, start_date, end_date, created_at, updated_at) VALUES ('run_orphan_check', ?, 'trend_growth', '20260101', '20260131', 't', 't')`, created.ID); err != nil {
		_ = rawLegacyWrite.Close()
		t.Fatalf("写入待清理回测失败：%v", err)
	}
	_ = rawLegacyWrite.Close()
	if _, err := analyticsStore.DB().Exec(`INSERT INTO portfolio_score_snapshot(portfolio_id, trade_date, created_at, updated_at) VALUES (?, '20261009', 't', 't')`, created.ID); err != nil {
		t.Fatalf("写入快照失败：%v", err)
	}

	if err := svc.DeletePortfolio(created.ID); err != nil {
		t.Fatalf("删除组合失败：%v", err)
	}

	rawCheck, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer rawCheck.Close()
	var leftover int
	if err := rawCheck.QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run WHERE run_id='run_orphan_check'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatal("删除组合后旧库回测成了孤儿 —— 回测页仍会列出已删除组合的记录")
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
