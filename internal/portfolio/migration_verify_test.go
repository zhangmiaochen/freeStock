package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 大表必须走"行数 + 首尾行"的抽样校验，而不是整表 quick_check ——
// 后者在 NAS 上要把几 GB 全扫一遍且没有任何进度。
func TestVerifyMigratedTableUsesSamplingForLargeTables(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "src.db")
	targetPath := filepath.Join(dir, "dst.db")
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]StrengthRow, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, StrengthRow{
			TradeDate: 20261009, StrategyID: StrategyTrendGrowth,
			Code: "60000" + string(rune('0'+i)) + ".SH", Name: "x", TotalScore: 60,
		})
	}
	if err := source.SaveStrength(20261009, StrategyTrendGrowth, rows); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateAnalyticsDatabase(sourcePath, targetPath); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := target.Exec("ATTACH DATABASE ? AS legacy", sourcePath); err != nil {
		t.Fatal(err)
	}

	// limit=100 → 5 行属于"小表"，走全量 quick_check。
	mode, err := verifyMigratedTable(target, "legacy", "stock_strength", 5, 100)
	if err != nil || mode != "full" {
		t.Fatalf("小表应全量校验：mode=%q err=%v", mode, err)
	}
	// limit=2 → 同一张表按"大表"处理，走抽样。
	mode, err = verifyMigratedTable(target, "legacy", "stock_strength", 5, 2)
	if err != nil || mode != "sampled" {
		t.Fatalf("大表应抽样校验：mode=%q err=%v", mode, err)
	}
	// 行数对不上必须报错（抽样也要能发现截断）。
	if _, err := verifyMigratedTable(target, "legacy", "stock_strength", 6, 2); err == nil {
		t.Fatal("行数不一致时应报错")
	}
}

// 复制完成但**校验失败**时不能删除目标文件：历史上一次 rowid 误判就让
// 几小时的复制成果被清掉。下次运行应能直接收尾。
func TestMigrateAnalyticsKeepsTargetWhenVerificationFails(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "src.db")
	targetPath := filepath.Join(dir, "dst.db")
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	rows := []StrengthRow{{TradeDate: 20261009, StrategyID: StrategyTrendGrowth, Code: "600000.SH", Name: "x", TotalScore: 60}}
	if err := source.SaveStrength(20261009, StrategyTrendGrowth, rows); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	verifyTableHook = func(*sql.DB, string, string, int, int) (string, error) {
		return "", fmt.Errorf("模拟校验失败")
	}
	defer func() { verifyTableHook = nil }()

	if _, err := MigrateAnalyticsDatabase(sourcePath, targetPath); err == nil {
		t.Fatal("校验失败应返回错误")
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("校验失败时不该删除已复制好的目标文件：%v", err)
	}
	// 数据确实还在，下次可以直接收尾。
	raw, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("目标表行数=%d，应为 1", n)
	}
}

// 源表 rowid 与目标表 rowid 必然不同（目标表是空表，rowid 从 1 重新分配）。
// 之前的实现拿双方 rowid 边界做相等比较，导致复制其实成功却报"边界不一致"。
func TestVerifyMigratedTableIgnoresRowIDDifference(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "src.db")
	targetPath := filepath.Join(dir, "dst.db")
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	// 用显式高 rowid 写入，模拟真实旧库（长期删除/重写后 rowid 从千万级开始）。
	for i := 0; i < 5; i++ {
		if _, err := source.DB().Exec(
			`INSERT INTO stock_strength(rowid, trade_date, strategy_id, ts_code, name, total_score)
			 VALUES (?, 20261009, ?, ?, 'x', 60)`,
			10000000+i, StrategyTrendGrowth, fmt.Sprintf("6%05d.SH", i)); err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateAnalyticsDatabase(sourcePath, targetPath); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	var minID, maxID int64
	if err := target.QueryRow(`SELECT MIN(rowid), MAX(rowid) FROM stock_strength`).Scan(&minID, &maxID); err != nil {
		t.Fatal(err)
	}
	if minID != 1 || maxID != 5 {
		t.Fatalf("目标表 rowid 应从 1 重新分配，实际 [%d,%d]", minID, maxID)
	}
	// 行数一致 → 校验必须通过（不再比较 rowid）。
	if mode, err := verifyMigratedTable(target, "legacy", "stock_strength", 5, 2); err != nil || mode != "sampled" {
		t.Fatalf("rowid 不同不应判为失败：mode=%q err=%v", mode, err)
	}
}
