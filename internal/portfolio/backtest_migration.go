package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// BacktestTables 是从旧单库搬到独立回测库的三张表。
//
// 为什么单独一个文件：回测行带 `portfolio_id`，而迁移后组合住在核心库。
// 放在旧库里会出两个问题：
//  1. 旧库的 `portfolio` 里没有"迁移后新建的组合" → 写回测直接撞外键 787
//     （已复现）；跨文件也没法靠 SQLite 外键表达这层引用；
//  2. 回测页每次打开都要去碰那个 4.9GB 的旧库。
//
// 回测库用 backtestSchemaStandalone：保留 run_id 两条外键（同文件内删除回测
// 仍要级联清净值/成交），去掉 portfolio 外键，组合引用由应用层维护。
var BacktestTables = []string{
	"portfolio_backtest_run", "portfolio_backtest_daily", "portfolio_backtest_trade",
}

// BacktestMigrationReport 是回测库迁移的校验结果。
type BacktestMigrationReport struct {
	SourcePath string         `json:"sourcePath"`
	TargetPath string         `json:"targetPath"`
	Tables     map[string]int `json:"tables"`
	QuickCheck string         `json:"quickCheck"`
}

// MigrateBacktestDatabase 把旧单库的回测三张表复制到独立回测库。
//
// 与其它迁移一致：源库只读、目标必须不存在、逐表按 rowid 分批提交、
// 复制后重建索引并逐表 quick_check、失败删除不完整目标。
func MigrateBacktestDatabase(sourcePath, targetPath string) (report BacktestMigrationReport, err error) {
	return migrateBacktestDatabase(sourcePath, targetPath, nil)
}

// MigrateBacktestDatabaseWithProgress 同上，带逐表/逐批进度。
func MigrateBacktestDatabaseWithProgress(sourcePath, targetPath string, progress func(table string, index, total int, rowsDone int64)) (BacktestMigrationReport, error) {
	return migrateBacktestDatabase(sourcePath, targetPath, progress)
}

func migrateBacktestDatabase(sourcePath, targetPath string, progress func(table string, index, total int, rowsDone int64)) (report BacktestMigrationReport, err error) {
	report = BacktestMigrationReport{SourcePath: sourcePath, TargetPath: targetPath, Tables: map[string]int{}}
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

	target, err := OpenBacktest(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()
	raw := target.DB()
	detach, err := attachSource(raw, sourcePath, "legacy_backtest")
	if err != nil {
		return report, err
	}
	defer detach()

	const batchSize = 10000
	for index, table := range BacktestTables {
		if progress != nil {
			progress(table, index, len(BacktestTables), 0)
		}
		if _, err := raw.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("清空目标表 %s 失败：%w", table, err)
		}
		copied, err := copyTableByRowID(raw, "legacy_backtest", table, batchSize, func(done int64) {
			if progress != nil {
				progress(table, index, len(BacktestTables), done)
			}
		})
		if err != nil {
			return report, err
		}
		report.Tables[table] = int(copied)
		if progress != nil {
			progress(table, index+1, len(BacktestTables), copied)
		}
	}

	// 逐表 quick_check，进度可见。
	for index, table := range BacktestTables {
		if progress != nil {
			progress("检查 "+table, index, len(BacktestTables), 0)
		}
		var check string
		if err := raw.QueryRow("PRAGMA quick_check(" + quoteIdent(table) + ")").Scan(&check); err != nil {
			return report, fmt.Errorf("检查回测表 %s 失败：%w", table, err)
		}
		if check != "ok" {
			return report, fmt.Errorf("回测表 %s quick_check 失败：%s", table, check)
		}
		if progress != nil {
			progress("检查 "+table, index+1, len(BacktestTables), 0)
		}
	}
	report.QuickCheck = "ok"
	cleanup = false
	return report, nil
}

// BacktestTableCounts 返回回测三张表行数，用于迁移前后对比。
func BacktestTableCounts(db *sql.DB) (map[string]int, error) {
	out := make(map[string]int, len(BacktestTables))
	for _, table := range BacktestTables {
		count, err := countTable(db, quoteIdent(table))
		if err != nil {
			return nil, fmt.Errorf("统计 %s 失败：%w", table, err)
		}
		out[table] = count
	}
	return out, nil
}
