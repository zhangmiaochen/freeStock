package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AnalyticsTables 是从旧组合库迁移到分析库的派生/历史表集合。
var AnalyticsTables = []string{
	"stock_strength", "market_state", "signal_history",
	"portfolio_score_snapshot", "portfolio_candidate_snapshot", "rotation_signal", "hold_compare",
	"portfolio_backtest_run", "portfolio_backtest_daily", "portfolio_backtest_trade",
}

// AnalyticsMigrationReport 是分析库迁移校验结果。
type AnalyticsMigrationReport struct {
	SourcePath string         `json:"sourcePath"`
	TargetPath string         `json:"targetPath"`
	Tables     map[string]int `json:"tables"`
	QuickCheck string         `json:"quickCheck"`
}

// analyticsIndexDDL 是目标分析库在数据复制完成后一次性建立的索引。
// 迁移期间不保留这些索引，否则每行 INSERT 都要维护 B 树，NAS 上会把迁移
// 放大到小时级。失败时目标会被清理，不会留下半成品索引状态。
var analyticsIndexDDL = []string{
	strengthLookupIndexDDL,
	`CREATE INDEX IF NOT EXISTS idx_signal_history_date ON signal_history(trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_signal_history_code ON signal_history(ts_code, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_score_snap_date ON portfolio_score_snapshot(portfolio_id, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_cand_snap_date ON portfolio_candidate_snapshot(portfolio_id, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_rot_signal_date ON rotation_signal(portfolio_id, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_hold_compare ON hold_compare(portfolio_id, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_bt_run_portfolio ON portfolio_backtest_run(portfolio_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_bt_daily_run ON portfolio_backtest_daily(run_id, trade_date)`,
	`CREATE INDEX IF NOT EXISTS idx_bt_trade_run ON portfolio_backtest_trade(run_id, trade_date)`,
}

func dropAnalyticsIndexes(db *sql.DB) error {
	for _, name := range []string{"idx_strength_lookup", "idx_signal_history_date", "idx_signal_history_code", "idx_score_snap_date", "idx_cand_snap_date", "idx_rot_signal_date", "idx_hold_compare", "idx_bt_run_portfolio", "idx_bt_daily_run", "idx_bt_trade_run"} {
		if _, err := db.Exec("DROP INDEX IF EXISTS " + quoteIdent(name)); err != nil {
			return fmt.Errorf("删除分析索引 %s 失败：%w", name, err)
		}
	}
	return nil
}

func ensureAnalyticsIndexes(db *sql.DB) error {
	for _, ddl := range analyticsIndexDDL {
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// MigrateAnalyticsDatabase 复制旧单库中的分析/历史表到独立 analytics.db。
// 目标必须不存在，源库不修改；失败删除目标。
func MigrateAnalyticsDatabase(sourcePath, targetPath string) (AnalyticsMigrationReport, error) {
	return migrateAnalyticsDatabase(sourcePath, targetPath, nil)
}

// MigrateAnalyticsDatabaseWithProgress 与上面相同，但每完成一张表调用 progress。
// 回调只接收只读状态信息，不参与迁移事务；UI/日志可以安全更新。
func MigrateAnalyticsDatabaseWithProgress(sourcePath, targetPath string, progress func(table string, index, total int)) (AnalyticsMigrationReport, error) {
	return migrateAnalyticsDatabase(sourcePath, targetPath, progress)
}

func migrateAnalyticsDatabase(sourcePath, targetPath string, progress func(table string, index, total int)) (report AnalyticsMigrationReport, err error) {
	report = AnalyticsMigrationReport{SourcePath: sourcePath, TargetPath: targetPath, Tables: map[string]int{}}
	if sourcePath == "" || targetPath == "" {
		return report, fmt.Errorf("源库和目标库路径不能为空")
	}
	if filepath.Clean(sourcePath) == filepath.Clean(targetPath) {
		return report, fmt.Errorf("源库和目标库不能是同一个文件")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return report, fmt.Errorf("源库不存在：%w", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return report, fmt.Errorf("目标库已存在，为避免覆盖数据而拒绝：%s", targetPath)
	} else if !os.IsNotExist(err) {
		return report, err
	}
	if dir := filepath.Dir(targetPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return report, err
		}
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(targetPath)
			_ = os.Remove(targetPath + "-wal")
			_ = os.Remove(targetPath + "-shm")
		}
	}()

	target, err := Open(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()
	raw := target.DB()
	// 分析库不包含核心 portfolio 表；回测表里的 portfolio_id 是跨库逻辑引用，
	// 不能继续使用旧库里的 SQLite 外键约束，否则复制 portfolio_backtest_run
	// 会因核心库不在分析库而失败（787）。核心库与分析库之间的引用由应用层维护。
	if _, err := raw.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return report, fmt.Errorf("关闭分析库迁移期间外键检查失败：%w", err)
	}
	defer raw.Exec("PRAGMA foreign_keys = ON")
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(sourcePath), "'", "''") + "'"
	if _, err := raw.Exec("ATTACH DATABASE " + quoted + " AS legacy_analytics"); err != nil {
		return report, fmt.Errorf("附加旧库失败：%w", err)
	}
	defer raw.Exec("DETACH DATABASE legacy_analytics")
	if err := dropAnalyticsIndexes(raw); err != nil {
		return report, err
	}

	// 每张表单独提交。INSERT...SELECT 由 SQLite 引擎 VM 流式执行，不会把整表
	// 装入 Go 内存；逐表事务限制 journal 和锁的持续时间。失败由外层 cleanup
	// 删除不完整目标，源库始终不变。
	for index, table := range AnalyticsTables {
		tx, err := raw.Begin()
		if err != nil {
			return report, err
		}
		var sourceCount int
		if err := tx.QueryRow("SELECT COUNT(*) FROM legacy_analytics." + quoteIdent(table)).Scan(&sourceCount); err != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("统计旧分析表 %s 失败：%w", table, err)
		}
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("清空目标分析表 %s 失败：%w", table, err)
		}
		if _, err := tx.Exec("INSERT INTO " + quoteIdent(table) + " SELECT * FROM legacy_analytics." + quoteIdent(table)); err != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("复制分析表 %s 失败：%w", table, err)
		}
		var targetCount int
		if err := tx.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&targetCount); err != nil {
			_ = tx.Rollback()
			return report, err
		}
		if sourceCount != targetCount {
			_ = tx.Rollback()
			return report, fmt.Errorf("分析表 %s 行数不一致：源=%d 目标=%d", table, sourceCount, targetCount)
		}
		if err := tx.Commit(); err != nil {
			return report, fmt.Errorf("提交分析表 %s 失败：%w", table, err)
		}
		report.Tables[table] = targetCount
		if progress != nil {
			progress(table, index+1, len(AnalyticsTables))
		}
	}
	if err := raw.QueryRow("PRAGMA quick_check").Scan(&report.QuickCheck); err != nil {
		return report, err
	}
	if report.QuickCheck != "ok" {
		return report, fmt.Errorf("分析库 quick_check 失败：%s", report.QuickCheck)
	}
	if err := ensureAnalyticsIndexes(raw); err != nil {
		return report, fmt.Errorf("重建分析索引失败：%w", err)
	}
	// 迁移后核心库外键无法跨文件验证；检查分析库内部结构并记录跨库引用
	// 由应用层维护，避免误把 787 当成数据损坏。
	cleanup = false
	return report, nil
}

// AnalyticsTableCounts 返回分析表行数，用于迁移前后对比。
func AnalyticsTableCounts(db *sql.DB) (map[string]int, error) {
	out := make(map[string]int, len(AnalyticsTables))
	for _, table := range AnalyticsTables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&count); err != nil {
			return nil, fmt.Errorf("统计 %s 失败：%w", table, err)
		}
		out[table] = count
	}
	return out, nil
}
