package portfolio

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// WAL 必须生效：写事务进行中，读不能被阻塞。
//
// 这是"回填完成后页面又卡十几分钟"的根因防线 —— 回填收尾会在 6.9GB 的
// stock_strength 上重建索引，rollback journal 下那几分钟所有读都进不来。
func TestWALAllowsReadsDuringWrite(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q，期望 wal（长写事务会阻塞所有读）", mode)
	}

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 200; i++ {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO market_state (trade_date, state, label, bull_score, bear_score, proxy_ret_20, days, created_at, raw_breadth_ready)
			 VALUES (?, 'range', '', 0, 0, 0, 0, '', -1)`, 20200101+i); err != nil {
			t.Fatal(err)
		}
	}

	// 第二个连接（模拟"页面查询"用的另一条连接）：WAL 下读不该被写事务挡住。
	// 同一条 sql.DB 里只有 1 个连接（SetMaxOpenConns(1)），那是排队而不是锁，
	// 所以这里必须另开一个 *sql.DB 才测的是 WAL 本身。
	reader, err := sql.Open("sqlite", filepath.Join(filepath.Dir(store.Path()), "wal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	done := make(chan error, 1)
	go func() {
		var n int
		done <- reader.QueryRow("SELECT COUNT(*) FROM market_state").Scan(&n)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("写事务进行中读失败：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("写事务进行中读被阻塞超过 3s（WAL 未生效）")
	}
}
