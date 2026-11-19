package portfolio

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ErrAnalyticsIncomplete 表示"分析库缺行"——只有这种情况才应该归档重做。
//
// 其它错误（打不开、被占用、磁盘满、校验被 I/O 中断）**不能**当成数据不一致：
// 把几 GB 已复制好的文件归档重来，代价是几小时白跑。
var ErrAnalyticsIncomplete = errors.New("分析库复制未完成")

// IsCorruptDatabaseError 判断错误是不是"这个文件根本不是可用的 SQLite 库"
// （被截断、被别的程序写坏、不是数据库文件）。
//
// 这类目标留着没有意义，归档重做才是对的；与"暂时打不开/被占用"必须区分开，
// 后者归档会白白丢掉几小时的复制成果。
func IsCorruptDatabaseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"not a database", // SQLITE_NOTADB (26)
		"malformed",      // SQLITE_CORRUPT (11)
		"database disk image is malformed",
		"file is encrypted", // SQLITE_NOTADB 的另一条路径
		"file is not a database",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// AnalyticsTables 是从旧组合库迁移到分析库的派生/历史表集合。
//
// ⚠ **不含回测三张表**（portfolio_backtest_run/daily/trade）。它们带
// `portfolio_id` 外键，而分析库里没有核心组合表 → 复制时必然撞 787；
// 而且回测结果目前仍由旧完整库读写（尚未拆出独立 BacktestStore），
// 复制到分析库会立刻变成"两份数据、新结果只落一边"的分叉。
// 回测表留在旧库，等独立回测库那一步再整体搬。
var AnalyticsTables = []string{
	"stock_strength", "market_state", "signal_history",
	"portfolio_score_snapshot", "portfolio_candidate_snapshot", "rotation_signal", "hold_compare",
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
}

func dropAnalyticsIndexes(db *sql.DB) error {
	for _, name := range []string{"idx_strength_lookup", "idx_signal_history_date", "idx_signal_history_code", "idx_score_snap_date", "idx_cand_snap_date", "idx_rot_signal_date", "idx_hold_compare"} {
		if _, err := db.Exec("DROP INDEX IF EXISTS " + quoteIdent(name)); err != nil {
			return fmt.Errorf("删除分析索引 %s 失败：%w", name, err)
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func ensureAnalyticsIndexes(db *sql.DB) error {
	for _, ddl := range analyticsIndexDDL {
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// FinalizeAnalyticsDatabase 接手一个"数据已复制完、只差收尾"的分析库：
// 逐表核对源/目标行数 → 重建索引 → 逐表 quick_check。
//
// 为什么要它：旧版本可能已经把几 GB 复制完，却卡在整库 quick_check 上。
// 直接把目标归档重做等于再花几小时；这里只做只读校验，通过就继续。
// 任一行数不一致（说明复制被中断）或校验失败都返回错误，由调用方决定重做。
func FinalizeAnalyticsDatabase(sourcePath, targetPath string, progress func(table string, index, total int)) (report AnalyticsMigrationReport, err error) {
	report = AnalyticsMigrationReport{SourcePath: sourcePath, TargetPath: targetPath, Tables: map[string]int{}}
	if sourcePath == "" || targetPath == "" {
		return report, fmt.Errorf("源库和目标库路径不能为空")
	}
	if _, err := os.Stat(targetPath); err != nil {
		return report, fmt.Errorf("分析库不存在，无法收尾：%w", err)
	}
	target, err := openAnalyticsOnly(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()
	raw := target.DB()
	if _, err := raw.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return report, err
	}
	defer raw.Exec("PRAGMA foreign_keys = ON")
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(sourcePath), "'", "''") + "'"
	if _, err := raw.Exec("ATTACH DATABASE " + quoted + " AS legacy_analytics"); err != nil {
		return report, fmt.Errorf("附加旧库失败：%w", err)
	}
	defer raw.Exec("DETACH DATABASE legacy_analytics")

	// ① 行数对齐：任何一个表少了就说明上次复制没做完，交给调用方重做。
	for i, table := range AnalyticsTables {
		if progress != nil {
			progress("核对 "+table, i, len(AnalyticsTables))
		}
		var sourceCount, targetCount int
		if err := raw.QueryRow("SELECT COUNT(*) FROM legacy_analytics." + quoteIdent(table)).Scan(&sourceCount); err != nil {
			return report, fmt.Errorf("统计旧分析表 %s 失败：%w", table, err)
		}
		if err := raw.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&targetCount); err != nil {
			return report, fmt.Errorf("统计目标分析表 %s 失败：%w", table, err)
		}
		// ⚠ 只判"目标比源少"。迁移成功后用户会继续刷新评分/生成信号，分析库
		// 里有**更新**的数据（行数比旧库多）—— 用相等判断会把这份新数据判成
		// 不一致并归档重做，等于把用户后来跑出来的评分全丢掉。
		if targetCount < sourceCount {
			return report, fmt.Errorf("%w：分析表 %s 目标=%d 少于源=%d，上次复制未完成",
				ErrAnalyticsIncomplete, table, targetCount, sourceCount)
		}
		if targetCount > sourceCount {
			slog.Info("分析表目标行数多于旧库，保留现有数据", "table", table,
				"target", targetCount, "source", sourceCount)
		}
		report.Tables[table] = targetCount
	}

	// ② 重建索引（幂等）+ ③ 逐表 quick_check，进度按表推进。
	if progress != nil {
		progress("索引重建", len(AnalyticsTables), len(AnalyticsTables))
	}
	if err := ensureAnalyticsIndexes(raw); err != nil {
		return report, fmt.Errorf("重建分析索引失败：%w", err)
	}
	for i, table := range AnalyticsTables {
		if progress != nil {
			progress("检查 "+table, i, len(AnalyticsTables))
		}
		// 同上：大表用行数+rowid 边界校验，避免整表全扫。
		if _, err := verifyMigratedTable(raw, "legacy_analytics", table, report.Tables[table], 0); err != nil {
			return report, err
		}
	}
	report.QuickCheck = "ok"
	return report, nil
}

// MigrateAnalyticsDatabase 复制旧单库中的分析/历史表到独立 analytics.db。
// 目标必须不存在，源库不修改；失败删除目标。
func MigrateAnalyticsDatabase(sourcePath, targetPath string) (AnalyticsMigrationReport, error) {
	return migrateAnalyticsDatabase(sourcePath, targetPath, nil)
}

// MigrateAnalyticsDatabaseWithProgress 与上面相同，但按批次调用 progress。
// 回调只接收只读状态信息，不参与迁移事务；UI/日志可以安全更新。
func MigrateAnalyticsDatabaseWithProgress(sourcePath, targetPath string, progress func(table string, index, total int, rowsDone, rowsTotal int64)) (AnalyticsMigrationReport, error) {
	return migrateAnalyticsDatabase(sourcePath, targetPath, progress)
}

func migrateAnalyticsDatabase(sourcePath, targetPath string, progress func(table string, index, total int, rowsDone, rowsTotal int64)) (report AnalyticsMigrationReport, err error) {
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

	target, err := openAnalyticsOnly(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()
	raw := target.DB()
	// 分析库只有分析表，本不该有外键；这里仍关闭外键检查是为了兼容
	// 早期用完整 schema 建出来的旧分析库（里面可能残留指向空 portfolio 表的
	// 回测表）。跨库引用由应用层维护。
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

	// 每张表按 rowid 分批复制（见 copyTableByRowID）：不预扫 COUNT，
	// 每批单独提交，进度随时可读。
	const batchSize = 10000
	for index, table := range AnalyticsTables {
		if progress != nil {
			progress(table, index, len(AnalyticsTables), 0, -1) // 总行数未知，不做全表预扫
		}
		if _, err := raw.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("清空目标表 %s 失败：%w", table, err)
		}
		copied, err := copyTableByRowID(raw, "legacy_analytics", table, batchSize, func(done int64) {
			if progress != nil {
				progress(table, index, len(AnalyticsTables), done, -1)
			}
		})
		if err != nil {
			return report, err
		}
		report.Tables[table] = int(copied)
		if progress != nil {
			progress(table, index+1, len(AnalyticsTables), copied, copied)
		}
	}
	// 数据复制完 = 目标已经可用于"下次收尾"。从这里开始**失败也不删目标**：
	// 校验失败可能只是一次误报（历史上就出过 rowid 误判），删掉等于几小时白跑。
	// 下次运行会先走 FinalizeAnalyticsDatabase，按行数判断能不能直接收尾。
	cleanup = false
	if progress != nil {
		progress("索引重建", len(AnalyticsTables), len(AnalyticsTables), 0, -1)
	}
	if err := ensureAnalyticsIndexes(raw); err != nil {
		return report, fmt.Errorf("重建分析索引失败：%w", err)
	}
	// 逐表校验。大表不再整表 quick_check（4.9GB 全扫在 NAS 上要几十分钟且
	// 没有任何进度），改用行数 + 首尾行校验；阈值见 verifyMigratedTable。
	// 这里的失败**不会删除目标**（cleanup 已置 false）：下次运行先按行数判断
	// 能否直接收尾，避免一次误报就重搬几小时。
	for index, table := range AnalyticsTables {
		if progress != nil {
			progress("检查 "+table, index, len(AnalyticsTables), 0, -1)
		}
		mode, err := verifyMigratedTable(raw, "legacy_analytics", table, report.Tables[table], 0)
		if err != nil {
			return report, err
		}
		if mode == "sampled" {
			slog.Info("大表跳过整表 quick_check，已做行数+首尾行校验",
				"table", table, "rows", report.Tables[table],
				"hint", "需要全量校验可设 FREESTOCK_MIGRATION_FULL_VERIFY=1")
		}
		if progress != nil {
			progress("检查 "+table, index+1, len(AnalyticsTables), 0, -1)
		}
	}
	report.QuickCheck = "ok"
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
