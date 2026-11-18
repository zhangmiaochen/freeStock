package portfolio

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// 大表必须走"行数 + rowid 边界"的抽样校验，而不是整表 quick_check ——
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
