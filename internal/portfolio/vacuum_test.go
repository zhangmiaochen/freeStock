package portfolio

import (
	"os"
	"testing"
)

// **一次性的库维护：VACUUM**。
//
// 背景：`bin/portfolio.db` 470.7 MB 里有 **338 MB 是空闲页** ——
// 回填的 `DELETE FROM stock_strength WHERE trade_date=? AND strategy_id=?`
// 一次删掉约 75 万行，而 `auto_vacuum=0` 让这些页留在文件里不回收。
//
// 危害不只是占空间：SQLite 默认用 rollback journal，一次大改动涉及的页
// 会先写进 `portfolio.db-journal`（最大可接近这些空闲页的体量）。
// 在 470MB 的文件旁边再创建这么大的 journal，失败就会报
// `unable to open database file (14)` —— 与"回填成功、紧接着回测失败"吻合。
//
// **运行前必须关闭应用**（VACUUM 需要独占访问）。
// 设 FREESTOCK_DB_VACUUM=<路径> 才跑。
func TestVacuumPortfolioDB(t *testing.T) {
	path := os.Getenv("FREESTOCK_DB_VACUUM")
	if path == "" {
		t.Skip("设 FREESTOCK_DB_VACUUM=<portfolio.db 路径> 才跑（会改动文件）")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("库不存在：%v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer store.Close()

	stats := func(label string) (sizeMB, freeMB float64) {
		var pageSize, pageCount, freelist int64
		_ = store.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
		_ = store.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
		_ = store.db.QueryRow("PRAGMA freelist_count").Scan(&freelist)
		sizeMB = float64(pageSize*pageCount) / (1 << 20)
		freeMB = float64(pageSize*freelist) / (1 << 20)
		t.Logf("%s：文件 %.1f MB，空闲页 %.1f MB（%d 页）", label, sizeMB, freeMB, freelist)
		return
	}

	stats("VACUUM 前")

	var rows int64
	_ = store.db.QueryRow("SELECT COUNT(*) FROM stock_strength").Scan(&rows)
	t.Logf("stock_strength 有 %d 行（VACUUM 不会改变数据，只是整理页）", rows)

	// ⚠ 执行前先把所有语句收干净：`SetMaxOpenConns(1)` 下，
	// 还有未关闭的 Rows 时执行 VACUUM 会一直等连接。
	if _, err := store.db.Exec("VACUUM"); err != nil {
		t.Fatalf("VACUUM 失败：%v", err)
	}
	t.Logf("VACUUM 执行完成")

	stats("VACUUM 后")

	// 校验：数据没变、库没坏
	var check string
	if err := store.db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Errorf("**VACUUM 后库不健康**：%s（err=%v）", check, err)
	} else {
		t.Logf("quick_check = ok")
	}
	var after int64
	_ = store.db.QueryRow("SELECT COUNT(*) FROM stock_strength").Scan(&after)
	if after != rows {
		t.Errorf("**数据行数变了**：%d → %d", rows, after)
	} else {
		t.Logf("行数一致（%d），数据未被改变", after)
	}

	after2, _ := os.Stat(path)
	t.Logf("文件实际大小：%.1f MB → %.1f MB（省下 %.1f MB）",
		float64(before.Size())/(1<<20), float64(after2.Size())/(1<<20),
		float64(before.Size()-after2.Size())/(1<<20))
}
