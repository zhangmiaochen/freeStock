package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CoreMigrationReport 是核心库迁移后的可验证结果。
type CoreMigrationReport struct {
	SourcePath string         `json:"sourcePath"`
	TargetPath string         `json:"targetPath"`
	Tables     map[string]int `json:"tables"`
	QuickCheck string         `json:"quickCheck"`
}

// CoreTables 是组合页面真正依赖的热表。
//
// 分析表（stock_strength、signal_history、market_state、快照、回测）
// 有意不复制；它们留在旧库，后续由 AnalyticsStore 迁移。
var CoreTables = []string{
	"portfolio",
	"portfolio_position",
	"portfolio_position_txn",
	"position_shares",
	"portfolio_money",
}

// MigrateCoreDatabase 把旧单库的核心组合数据复制到新文件。
//
// 安全约束：
//   - source 只读；
//   - target 必须是新路径（已存在直接拒绝）；
//   - 使用 SQLite ATTACH 在同一事务中复制，避免 Go 层逐行搬运；
//   - 复制后校验每张表的行数和 PRAGMA quick_check；
//   - 任意失败删除 target，旧库保持不变。
//
// 注意：当前 Store.Open 会建立完整兼容 schema，目标文件因此包含空的分析表；
// 这一步的目标是先隔离并验证核心数据，下一阶段会把 schema migration 拆成
// core/analytics 两套，届时空分析表不会再创建。
func MigrateCoreDatabase(sourcePath, targetPath string) (CoreMigrationReport, error) {
	return MigrateCoreDatabaseWithProgress(sourcePath, targetPath, nil)
}

// MigrateCoreDatabaseWithProgress 报告当前核心表复制阶段。
func MigrateCoreDatabaseWithProgress(sourcePath, targetPath string, progress func(table string, index, total int)) (report CoreMigrationReport, err error) {
	report = CoreMigrationReport{SourcePath: sourcePath, TargetPath: targetPath, Tables: map[string]int{}}
	if sourcePath == "" || targetPath == "" {
		return report, fmt.Errorf("源库和目标库路径不能为空")
	}
	if filepath.Clean(sourcePath) == filepath.Clean(targetPath) {
		return report, fmt.Errorf("源库和目标库不能是同一个文件")
	}
	if _, statErr := os.Stat(sourcePath); statErr != nil {
		return report, fmt.Errorf("源库不存在：%w", statErr)
	}
	if _, statErr := os.Stat(targetPath); statErr == nil {
		return report, fmt.Errorf("目标库已存在，为避免覆盖数据而拒绝：%s", targetPath)
	} else if !os.IsNotExist(statErr) {
		return report, statErr
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

	target, err := OpenCore(targetPath)
	if err != nil {
		return report, err
	}
	defer target.Close()

	raw := target.DB()
	// ATTACH 不支持把路径作为参数绑定；按 SQLite 字面量规则转义单引号。
	// 路径来自本地配置，不执行 shell，但仍不能直接拼接原文。
	quotedSource := "'" + strings.ReplaceAll(filepath.ToSlash(sourcePath), "'", "''") + "'"
	if _, err = raw.Exec("ATTACH DATABASE " + quotedSource + " AS legacy_core"); err != nil {
		return report, fmt.Errorf("附加旧库失败：%w", err)
	}
	defer raw.Exec("DETACH DATABASE legacy_core") // best effort

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

	for index, table := range CoreTables {
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("清空目标表 %s 失败：%w", table, err)
		}
		if _, err := tx.Exec("INSERT INTO " + quoteIdent(table) + " SELECT * FROM legacy_core." + quoteIdent(table)); err != nil {
			return report, fmt.Errorf("复制核心表 %s 失败：%w", table, err)
		}
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&count); err != nil {
			return report, err
		}
		report.Tables[table] = count
		if progress != nil {
			progress(table, index+1, len(CoreTables))
		}
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("提交核心库迁移失败：%w", err)
	}
	rollback = false

	var check string
	if err := raw.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return report, err
	}
	report.QuickCheck = check
	if check != "ok" {
		return report, fmt.Errorf("目标库 quick_check 失败：%s", check)
	}
	cleanup = false
	return report, nil
}

func quoteIdent(value string) string {
	return `"` + value + `"`
}

// CoreTableCounts 返回核心表行数，用于迁移前后对比和诊断。
// ValidateCoreDatabasePair checks source/target core table counts and both quick_check values.
// It opens existing DBs and performs read-only queries; it never runs schema migration.
func ValidateCoreDatabasePair(sourcePath, targetPath string) (map[string]int, string, error) {
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return nil, "", err
	}
	defer source.Close()
	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		return nil, "", err
	}
	defer target.Close()
	srcCounts, err := CoreTableCounts(source)
	if err != nil {
		return nil, "", err
	}
	dstCounts, err := CoreTableCounts(target)
	if err != nil {
		return nil, "", err
	}
	for _, table := range CoreTables {
		if srcCounts[table] != dstCounts[table] {
			return nil, "", fmt.Errorf("核心表 %s 行数不匹配：源=%d 目标=%d", table, srcCounts[table], dstCounts[table])
		}
	}
	var check string
	if err := target.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return nil, "", err
	}
	if check != "ok" {
		return nil, check, fmt.Errorf("核心目标 quick_check: %s", check)
	}
	return dstCounts, check, nil
}

func CoreTableCounts(db *sql.DB) (map[string]int, error) {
	out := make(map[string]int, len(CoreTables))
	for _, table := range CoreTables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&count); err != nil {
			return nil, fmt.Errorf("统计 %s 失败：%w", table, err)
		}
		out[table] = count
	}
	return out, nil
}
