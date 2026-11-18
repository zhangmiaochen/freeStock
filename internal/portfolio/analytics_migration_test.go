package portfolio

import (
	"path/filepath"
	"testing"
)

// 回测三张表**故意不复制**到分析库：
//   - 它们带 portfolio_id 外键，而分析库没有核心组合表 → 复制必撞 787；
//   - 回测结果仍由旧完整库读写（独立 BacktestStore 是后续阶段），
//     复制过去会立刻变成两份数据、新结果只落一边。
//
// 这条用例把边界钉住：评分/信号/快照复制，回测表留在旧库。
func TestMigrateAnalyticsDatabaseLeavesBacktestTablesInLegacy(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	targetPath := filepath.Join(dir, "analytics.db")
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	item, err := source.Create(PortfolioInput{Name: "测试", Type: TypeManual, StrategyID: StrategyTrendGrowth})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB().Exec(`INSERT INTO portfolio_backtest_run(run_id,portfolio_id,strategy_id,start_date,end_date,created_at,updated_at) VALUES ('run_test',?,'trend_growth','20260101','20260131','2026-02-01','2026-02-01')`, item.ID); err != nil {
		t.Fatal(err)
	}
	rows := []StrengthRow{{TradeDate: 20261009, StrategyID: StrategyTrendGrowth, Code: "600000.SH", Name: "x", TotalScore: 70}}
	if err := source.SaveStrength(20261009, StrategyTrendGrowth, rows); err != nil {
		t.Fatal(err)
	}

	report, err := MigrateAnalyticsDatabase(sourcePath, targetPath)
	if err != nil {
		t.Fatalf("迁移不应因回测外键失败：%v", err)
	}
	if report.Tables["stock_strength"] != 1 {
		t.Fatalf("评分应复制 1 行，报告=%v", report.Tables)
	}
	if _, copied := report.Tables["portfolio_backtest_run"]; copied {
		t.Fatalf("回测表不应进入分析库，报告=%v", report.Tables)
	}

	analytics, err := Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer analytics.Close()
	var n int
	if err := analytics.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("分析库回测表应为空，实际 %d 行", n)
	}
	// 旧库回测结果必须原样保留（回测页仍从这里读）。
	if err := source.DB().QueryRow(`SELECT COUNT(*) FROM portfolio_backtest_run`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("旧库回测记录被破坏：%d 行", n)
	}
	var quick string
	if err := analytics.DB().QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
}
