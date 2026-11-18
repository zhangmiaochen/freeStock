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

// verifyTableHook 是包内接缝：测试用它模拟"复制成功但校验失败"，
// 验证迁移不会把已经复制好的目标文件删掉。
var verifyTableHook func(raw *sql.DB, srcAlias, table string, wantCount, limit int) (string, error)

// verifyMigratedTable 校验一张已复制完的表。
//
//   - 行数不超过 limit（或强制全量）→ `PRAGMA main.quick_check(table)`；
//   - 行数很大 → 计数与首尾行校验（见下）。
//
// ⚠ **不能比较源/目标的 rowid 边界**：复制用的是
// `INSERT INTO 目标 SELECT * FROM 源 WHERE rowid BETWEEN ...`，
// 目标表是空表，SQLite 会**重新分配 rowid（从 1 连续递增）**，与源库的 rowid
// 完全无关（源库因为历史删除/重写，rowid 可能从一千万开始）。
// 之前按 rowid 相等校验，导致复制明明成功却报"边界不一致"。
//
// 真正能证明完整的是：目标行数 == 已复制行数；再读首尾两行确认页可读
// （文件被截断/损坏时这里会报错）。批内"写入行数=扫描行数"已在复制时断言过，
// 缺行只可能是整批丢失 —— 那必然表现为计数不一致。
//
// 返回 "full"/"sampled" 便于日志说明到底做了哪种检查。
func verifyMigratedTable(raw *sql.DB, srcAlias, table string, wantCount int, limit int) (string, error) {
	if verifyTableHook != nil {
		return verifyTableHook(raw, srcAlias, table, wantCount, limit)
	}
	if limit <= 0 {
		limit = defaultFullQuickCheckRowLimit
	}
	if wantCount <= limit || fullVerifyEnabled() {
		// `main.` 前缀不能省：不带 schema 的 quick_check 会把 ATTACH 进来的
		// 旧库一起扫（见 core_migration.go 里同类注释）。
		var check string
		if err := raw.QueryRow("PRAGMA main.quick_check(" + quoteIdent(table) + ")").Scan(&check); err != nil {
			return "", fmt.Errorf("检查 %s 失败：%w", table, err)
		}
		if check != "ok" {
			return "", fmt.Errorf("%s quick_check 失败：%s", table, check)
		}
		return "full", nil
	}

	// 目标行数必须等于已复制行数（缺行/多行都能发现）。
	targetCount, err := countTable(raw, quoteIdent(table))
	if err != nil {
		return "", fmt.Errorf("统计目标表 %s 失败：%w", table, err)
	}
	if targetCount != wantCount {
		return "", fmt.Errorf("%s 行数不一致：目标=%d 已复制=%d", table, targetCount, wantCount)
	}
	if targetCount == 0 {
		return "sampled", nil
	}
	// 首尾各读一行：确认页真的可读（截断的文件在这里就会报错）。
	var edge int
	if err := raw.QueryRow("SELECT COUNT(*) FROM "+quoteIdent(table)+" WHERE rowid IN (1, ?)", targetCount).Scan(&edge); err != nil {
		return "", fmt.Errorf("读取目标表 %s 首尾行失败：%w", table, err)
	}
	if edge == 0 {
		return "", fmt.Errorf("%s 首尾行缺失（目标行数=%d，但按 rowid 读不到内容）", table, targetCount)
	}
	return "sampled", nil
}
