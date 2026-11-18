package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// fullQuickCheckRowLimit 超过这个行数就不再整表 `quick_check`。
//
// `PRAGMA quick_check(table)` 会把整张表的 B 树页全读一遍 —— 对 4.9GB 的
// stock_strength，在 NAS（顺序读 ~7.9MB/s）上就是十几到几十分钟的纯等待，
// 期间没有任何进度可报，用户看到的就是"卡住"。
//
// 复制阶段已经逐批核对过 `写入行数 == 扫描行数`，收尾阶段又比对了源/目标行数，
// 所以大表这里再补一个**边界校验**（目标行数、最小/最大 rowid、以及两端 rowid
// 是否可读）就足以发现截断/半写；真正的页级损坏由日常查询或下次打开自然暴露。
//
// 想强制全量校验：`FREESTOCK_MIGRATION_FULL_VERIFY=1`。
const defaultFullQuickCheckRowLimit = 1_000_000

func fullVerifyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FREESTOCK_MIGRATION_FULL_VERIFY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// verifyMigratedTable 校验一张已复制完的表。
//
//   - 行数不超过 limit（或强制全量）→ `PRAGMA quick_check(table)`；
//   - 行数很大 → 边界校验：行数、最小/最大 rowid、首尾行可读。
//
// 返回 "full"/"sampled" 便于日志说明到底做了哪种检查。
func verifyMigratedTable(raw *sql.DB, srcAlias, table string, wantCount int, limit int) (string, error) {
	if limit <= 0 {
		limit = defaultFullQuickCheckRowLimit
	}
	if wantCount <= limit || fullVerifyEnabled() {
		var check string
		if err := raw.QueryRow("PRAGMA quick_check(" + quoteIdent(table) + ")").Scan(&check); err != nil {
			return "", fmt.Errorf("检查 %s 失败：%w", table, err)
		}
		if check != "ok" {
			return "", fmt.Errorf("%s quick_check 失败：%s", table, check)
		}
		return "full", nil
	}

	// 边界校验：目标行数必须等于已复制行数。
	targetCount, err := countTable(raw, quoteIdent(table))
	if err != nil {
		return "", fmt.Errorf("统计目标表 %s 失败：%w", table, err)
	}
	if targetCount != wantCount {
		return "", fmt.Errorf("%s 行数不一致：目标=%d 已复制=%d", table, targetCount, wantCount)
	}
	quotedSrc := srcAlias + "." + quoteIdent(table)
	var srcMin, srcMax, dstMin, dstMax sql.NullInt64
	if err := raw.QueryRow("SELECT MIN(rowid), MAX(rowid) FROM "+quotedSrc).Scan(&srcMin, &srcMax); err != nil {
		return "", fmt.Errorf("读取源表 %s 的 rowid 边界失败：%w", table, err)
	}
	if err := raw.QueryRow("SELECT MIN(rowid), MAX(rowid) FROM "+quoteIdent(table)).Scan(&dstMin, &dstMax); err != nil {
		return "", fmt.Errorf("读取目标表 %s 的 rowid 边界失败：%w", table, err)
	}
	if srcMin.Int64 != dstMin.Int64 || srcMax.Int64 != dstMax.Int64 {
		return "", fmt.Errorf("%s rowid 边界不一致：源=[%d,%d] 目标=[%d,%d]",
			table, srcMin.Int64, srcMax.Int64, dstMin.Int64, dstMax.Int64)
	}
	// 首尾各读一行：确认页真的可读（截断的文件在这里就会报错）。
	if _, err := raw.Exec("SELECT COUNT(*) FROM "+quoteIdent(table)+" WHERE rowid IN (?, ?)", srcMin.Int64, srcMax.Int64); err != nil {
		return "", fmt.Errorf("读取目标表 %s 首尾行失败：%w", table, err)
	}
	return "sampled", nil
}
