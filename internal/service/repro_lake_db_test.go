package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
)

// **把回测的两半连起来复现**。
//
// 事实链：
//   - 历史回填**成功**（它写 75 万行、大量写事务，也读数据湖）
//   - 跑回测**失败**：`unable to open database file (14)`（SQLITE_CANTOPEN）
//   - 我只测数据库写入时，**在 487MB 的真实库上完全正常、不复现**
//
// 所以漏掉的是**另一件事**：回测在写库之前会先从数据湖批量读日线
// （一次打开成百上千个 parquet 文件）。`SQLITE_CANTOPEN` 对应
// `open()` 返回 EMFILE/ENFILE —— 如果这些读把句柄耗掉，紧接着的
// 数据库写入就会失败。
//
// 这个用例按真实顺序做：**先重读数据湖，再写数据库**。
//
// 设 FREESTOCK_DB_REPRO=<portfolio.db 副本> 与 FREESTOCK_LAKE_ROOT 才跑。
func TestReproLakeReadThenDBWrite(t *testing.T) {
	dbPath := os.Getenv("FREESTOCK_DB_REPRO")
	if dbPath == "" {
		t.Skip("设 FREESTOCK_DB_REPRO=<路径> 才跑")
	}
	lakeRoot := config.NewStore().CnLakeRoot()
	if lakeRoot == "" {
		lakeRoot = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if lakeRoot == "" {
		t.Skip("没有数据湖根目录")
	}

	store, err := portfolio.Open(dbPath)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	defer store.Close()
	t.Logf("① 库已开：%s（句柄 %d）", dbPath, openHandleCount())

	// ---- 模拟回测的第一步：批量读数据湖日线 ----
	lake := cnlake.Open(lakeRoot)
	dates, err := lake.BarPartitionDates()
	if err != nil || len(dates) < 60 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	window := dates
	if len(window) > 320 {
		window = window[len(window)-320:]
	}
	t.Logf("② 准备批量读 %d 个交易日（每个日分区一个 parquet 文件）", len(window))
	t.Logf("   读之前句柄数：%d", openHandleCount())

	began := time.Now()
	rows, err := lake.DailyBarsInRange(nil, window[0], window[len(window)-1])
	if err != nil {
		t.Fatalf("批量读失败：%v", err)
	}
	t.Logf("③ 批量读完成：%d 行，耗时 %s", len(rows), time.Since(began).Round(time.Millisecond))
	t.Logf("   读之后句柄数：%d", openHandleCount())

	// ---- 紧接着做回测的写库（真实顺序） ----
	var realID string
	if err := store.SaveBacktestRun(portfolio.BacktestRun{
		RunID: "repro-lake-run", PortfolioID: reproPortfolioID(t, store), PortfolioName: "复现",
		StrategyID: "trend_growth", StrategyLabel: "趋势成长",
		PortfolioType: "semi_auto", PortfolioTypeLabel: "半自动组合",
		BenchmarkCode: "000300.SH", StartDate: "20260601", EndDate: "20261008",
		RebalanceFrequency: "signal", InitialCapital: 100000, Status: "finished",
		CreatedAt: "2026-10-10T00:00:00Z", UpdatedAt: "2026-10-10T00:00:00Z",
		FinishedAt: "2026-10-10T00:00:00Z",
	}); err != nil {
		t.Errorf("**批量读之后写库失败**：%v（句柄数 %d）", err, openHandleCount())
	} else {
		t.Logf("④ 批量读之后写库成功（句柄数 %d）", openHandleCount())
	}
	_, _ = store.DB().Exec("DELETE FROM portfolio_backtest_run WHERE run_id='repro-lake-run'")
	_ = realID
}

func reproPortfolioID(t *testing.T, store *portfolio.Store) string {
	t.Helper()
	var id string
	if err := store.DB().QueryRow("SELECT portfolio_id FROM portfolio LIMIT 1").Scan(&id); err != nil {
		t.Skipf("库里没有组合：%v", err)
	}
	return id
}
