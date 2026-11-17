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

// MigrateAnalyticsDatabase 复制旧单库中的分析/历史表到独立 analytics.db。
// 目标必须不存在，源库不修改；失败删除目标。
func MigrateAnalyticsDatabase(sourcePath, targetPath string) (report AnalyticsMigrationReport, err error) {
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
		}
	}()

	target, err := Open(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()
	raw := target.DB()
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(sourcePath), "'", "''") + "'"
	if _, err := raw.Exec("ATTACH DATABASE " + quoted + " AS legacy_analytics"); err != nil {
		return report, fmt.Errorf("附加旧库失败：%w", err)
	}
	defer raw.Exec("DETACH DATABASE legacy_analytics")

	tx, err := raw.Begin()
	if err != nil {
		return report, err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	for _, table := range AnalyticsTables {
		var sourceCount int
		if err := tx.QueryRow("SELECT COUNT(*) FROM legacy_analytics." + quoteIdent(table)).Scan(&sourceCount); err != nil {
			return report, fmt.Errorf("统计旧分析表 %s 失败：%w", table, err)
		}
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("清空目标分析表 %s 失败：%w", table, err)
		}
		if _, err := tx.Exec("INSERT INTO " + quoteIdent(table) + " SELECT * FROM legacy_analytics." + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("复制分析表 %s 失败：%w", table, err)
		}
		var targetCount int
		if err := tx.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&targetCount); err != nil {
			return report, err
		}
		if sourceCount != targetCount {
			return report, fmt.Errorf("分析表 %s 行数不一致：源=%d 目标=%d", table, sourceCount, targetCount)
		}
		report.Tables[table] = targetCount
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	rollback = false
	if err := raw.QueryRow("PRAGMA quick_check").Scan(&report.QuickCheck); err != nil {
		return report, err
	}
	if report.QuickCheck != "ok" {
		return report, fmt.Errorf("分析库 quick_check 失败：%s", report.QuickCheck)
	}
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
