package portfolio

import (
	"path/filepath"
	"testing"
)

// 回测运行记录含 portfolio_id，但分析库不含核心 portfolio 表；迁移必须把它
// 当作跨库逻辑引用，而不是让旧库的 SQLite 外键阻止复制。
func TestMigrateAnalyticsDatabaseAllowsCrossStoreBacktestReference(t *testing.T) {
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

	report, err := MigrateAnalyticsDatabase(sourcePath, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tables["portfolio_backtest_run"] != 1 {
		t.Fatalf("回测运行记录应复制 1 行，报告=%v", report.Tables)
	}
	analytics, err := Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer analytics.Close()
	var portfolioID string
	if err := analytics.DB().QueryRow(`SELECT portfolio_id FROM portfolio_backtest_run WHERE run_id='run_test'`).Scan(&portfolioID); err != nil {
		t.Fatal(err)
	}
	if portfolioID != item.ID {
		t.Fatalf("跨库 portfolio_id 丢失：%q，期望 %q", portfolioID, item.ID)
	}
	var quick string
	if err := analytics.DB().QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
}
