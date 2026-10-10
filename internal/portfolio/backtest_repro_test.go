package portfolio

import (
	"fmt"
	"os"
	"testing"
)

// **复现"回填成功、跑回测报 unable to open database file (14)"**。
//
// 事实：
//   - 历史回填**成功**（它写 75 万行、大量写事务）
//   - 跑回测**失败**（它只写 242 行）
//
// 如果问题是"库太大 / journal 太大"，失败的该是回填。所以先在这里
// 把**回测那条写入路径**单独跑一遍，看在 487MB 的真实库上会不会复现。
//
// 设 FREESTOCK_DB_REPRO=<portfolio.db 路径> 才跑（**只在副本上跑**）。
func TestReproBacktestPersist(t *testing.T) {
	path := os.Getenv("FREESTOCK_DB_REPRO")
	if path == "" {
		t.Skip("设 FREESTOCK_DB_REPRO=<路径> 才跑")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("库不存在：%v", err)
	} else {
		t.Logf("库：%s（%.1f MB）", path, float64(info.Size())/(1<<20))
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("**打开就失败了**：%v", err)
	}
	defer store.Close()
	t.Logf("① Open + Ping 通过")

	// ★ 必须用**真实存在**的 portfolio_id —— 回测表有外键约束
	var realID string
	if err := store.db.QueryRow("SELECT portfolio_id FROM portfolio LIMIT 1").Scan(&realID); err != nil {
		t.Skipf("库里没有组合，无法复现：%v", err)
	}
	t.Logf("用真实组合 %s", realID)

	// 回测落库第一步：run
	run := BacktestRun{
		RunID: "repro-run", PortfolioID: realID, PortfolioName: "复现",
		StrategyID: "trend_growth", StrategyLabel: "趋势成长",
		PortfolioType: "semi_auto", PortfolioTypeLabel: "半自动组合",
		BenchmarkCode: "000300.SH", StartDate: "20260601", EndDate: "20261008",
		RebalanceFrequency: "signal", InitialCapital: 100000,
		Status: "finished", CreatedAt: "2026-10-10T00:00:00Z",
		UpdatedAt: "2026-10-10T00:00:00Z", FinishedAt: "2026-10-10T00:00:00Z",
	}
	if err := store.SaveBacktestRun(run); err != nil {
		t.Fatalf("**SaveBacktestRun 失败**：%v", err)
	}
	t.Logf("② SaveBacktestRun 通过")

	// 第二步：240 天的净值
	daily := make([]BacktestDaily, 0, 240)
	for i := 0; i < 240; i++ {
		daily = append(daily, BacktestDaily{
			RunID: "repro-run", TradeDate: fmt.Sprintf("2026%04d", 601+i),
			TotalValue: 100000 + float64(i), Cash: 50000, PositionValue: 50000 + float64(i),
			DailyReturn: 0.001, CumulativeReturn: 0.01, PositionCount: 5,
		})
	}
	if err := store.SaveBacktestDaily(daily); err != nil {
		t.Fatalf("**SaveBacktestDaily 失败**：%v", err)
	}
	t.Logf("③ SaveBacktestDaily（240 行）通过")

	if err := store.SaveBacktestTrades(nil); err != nil {
		t.Fatalf("**SaveBacktestTrades 失败**：%v", err)
	}
	t.Logf("④ SaveBacktestTrades 通过")

	// 回测跑之前还会读评分（LoadStrengthRange）
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM stock_strength WHERE trade_date >= 20260601").Scan(&count); err != nil {
		t.Fatalf("**读评分失败**：%v", err)
	}
	t.Logf("⑤ 读区间评分通过（%d 行）", count)

	// 清理复现数据
	_, _ = store.db.Exec("DELETE FROM portfolio_backtest_daily WHERE run_id='repro-run'")
	_, _ = store.db.Exec("DELETE FROM portfolio_backtest_run WHERE run_id='repro-run'")
	t.Logf("**回测落库路径全部通过 —— 在 487MB 的库上没有复现**")
}
