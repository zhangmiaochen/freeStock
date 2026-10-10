package portfolio

import (
	"os"
	"path/filepath"
	"testing"
)

// **诊断：组合库为什么这么大 / 各表多少行。**
//
// 用户报「历史回填报错 unable to open database file (14)」（SQLITE_CANTOPEN），
// 而 bin/portfolio.db 实测有 **493 MB** —— 对一个只有两个组合的库是病态的。
//
// 这个用例只读不写，用来定位膨胀来源。设 FREESTOCK_DB_DIAG=<路径> 才跑。
func TestPortfolioDBDiagnostics(t *testing.T) {
	path := os.Getenv("FREESTOCK_DB_DIAG")
	if path == "" {
		t.Skip("设 FREESTOCK_DB_DIAG=<portfolio.db 路径> 才跑")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("库不存在：%v", err)
	}

	info, _ := os.Stat(path)
	t.Logf("库文件：%s（%.1f MB）", path, float64(info.Size())/(1<<20))
	// WAL 附属文件也看一下 —— CANTOPEN 有时是它们创建不出来
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if st, err := os.Stat(path + suffix); err == nil {
			t.Logf("附属文件 %s：%.1f MB", filepath.Base(path+suffix), float64(st.Size())/(1<<20))
		}
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatalf("列表失败：%v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}
	rows.Close()

	t.Logf("共 %d 张表：", len(tables))
	for _, table := range tables {
		var count int64
		// 表名来自 sqlite_master，不是外部输入
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Logf("  %-28s 统计失败：%v", table, err)
			continue
		}
		t.Logf("  %-28s %10d 行", table, count)
	}

	// 页面占用情况：SQLite 是页式存储，膨胀常常来自"删了没回收"
	var pageSize, pageCount, freelist int64
	_ = store.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	_ = store.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
	_ = store.db.QueryRow("PRAGMA freelist_count").Scan(&freelist)
	t.Logf("页大小 %d B，共 %d 页（%.1f MB），空闲页 %d（%.1f MB）",
		pageSize, pageCount, float64(pageSize*pageCount)/(1<<20),
		freelist, float64(pageSize*freelist)/(1<<20))
	// **库健康度**：损坏会直接表现为 CANTOPEN / 各种奇怪的 IO 错
	var check string
	if err := store.db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		t.Errorf("quick_check 执行失败：%v", err)
	} else if check != "ok" {
		t.Errorf("**库可能已损坏**：quick_check = %s", check)
	} else {
		t.Logf("quick_check = ok")
	}

	// 回测相关表的**实际结构**（回测落库只碰这几张，回填不碰）
	for _, table := range []string{
		"portfolio_backtest_run", "portfolio_backtest_daily", "portfolio_backtest_trade",
		"stock_strength",
	} {
		var sql string
		if err := store.db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&sql); err != nil {
			t.Errorf("**表 %s 不存在**：%v", table, err)
			continue
		}
		t.Logf("表 %s 定义 %d 字符", table, len(sql))
	}

	// ⚠ 列数检查：**必须先把上一批 Rows 收干净再开新查询**。
	// `SetMaxOpenConns(1)` 时，在遍历一个 Rows 的过程中再开查询会**死锁**
	//（等连接等到超时）—— 这里第一次写就踩了，600 秒没跑完。
	names := []string{}
	rows2, err := store.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'portfolio_backtest%'")
	if err == nil {
		for rows2.Next() {
			var name string
			if err := rows2.Scan(&name); err == nil {
				names = append(names, name)
			}
		}
		rows2.Close()
	}
	for _, name := range names {
		var n int
		if err := store.db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?)", name).Scan(&n); err == nil {
			t.Logf("  %s：%d 列", name, n)
		}
	}
	var autoVacuum int64
	_ = store.db.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum)
	t.Logf("auto_vacuum = %d（0=关闭，空闲页不会被回收）", autoVacuum)
}
