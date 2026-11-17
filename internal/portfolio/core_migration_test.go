package portfolio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateCoreDatabaseCopiesCoreDataAndLeavesSourceUnchanged(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	targetPath := filepath.Join(dir, "target", "core.db")

	source, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	item, err := source.Create(PortfolioInput{Name: "迁移组合"})
	if err != nil {
		source.Close()
		t.Fatalf("创建组合失败: %v", err)
	}
	if _, err := source.AddPosition(item.ID, PositionInput{
		Code: "600519", Name: "贵州茅台", EntryPrice: 1500, EntryDate: "20261117",
	}); err != nil {
		source.Close()
		t.Fatalf("创建持仓失败: %v", err)
	}
	if err := source.SetMoney(item.ID, Money{InitialCapital: 200000, Cash: 198500}); err != nil {
		source.Close()
		t.Fatalf("写入资金失败: %v", err)
	}
	if err := source.SetShares(item.ID, map[string]int{"600519": 100}); err != nil {
		source.Close()
		t.Fatalf("写入股数失败: %v", err)
	}

	beforeCounts, err := CoreTableCounts(source.DB())
	if err != nil {
		source.Close()
		t.Fatalf("统计源库失败: %v", err)
	}
	if err := source.DB().QueryRow("PRAGMA quick_check").Scan(new(string)); err != nil {
		source.Close()
		t.Fatalf("源库 quick_check 失败: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("关闭源库失败: %v", err)
	}

	report, err := MigrateCoreDatabase(sourcePath, targetPath)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if report.QuickCheck != "ok" {
		t.Fatalf("迁移报告 quick_check = %q, 期望 ok", report.QuickCheck)
	}
	wantCounts := map[string]int{
		"portfolio":              1,
		"portfolio_position":     1,
		"portfolio_position_txn": 1,
		"position_shares":        1,
		"portfolio_money":        1,
	}
	for table, want := range wantCounts {
		if got := report.Tables[table]; got != want {
			t.Errorf("报告中的 %s 行数 = %d, 期望 %d", table, got, want)
		}
	}

	target, err := Open(targetPath)
	if err != nil {
		t.Fatalf("打开目标库失败: %v", err)
	}
	targetCounts, err := CoreTableCounts(target.DB())
	if err != nil {
		target.Close()
		t.Fatalf("统计目标库失败: %v", err)
	}
	for table, want := range wantCounts {
		if got := targetCounts[table]; got != want {
			t.Errorf("目标库 %s 行数 = %d, 期望 %d", table, got, want)
		}
	}
	var targetCheck string
	if err := target.DB().QueryRow("PRAGMA quick_check").Scan(&targetCheck); err != nil {
		t.Errorf("目标库 quick_check 失败: %v", err)
	} else if targetCheck != "ok" {
		t.Errorf("目标库 quick_check = %q, 期望 ok", targetCheck)
	}
	target.Close()

	unchanged, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("重新打开源库失败: %v", err)
	}
	defer unchanged.Close()
	afterCounts, err := CoreTableCounts(unchanged.DB())
	if err != nil {
		t.Fatalf("再次统计源库失败: %v", err)
	}
	for table, before := range beforeCounts {
		if got := afterCounts[table]; got != before {
			t.Errorf("源库 %s 行数从 %d 变为 %d", table, before, got)
		}
	}
	var sourceCheck string
	if err := unchanged.DB().QueryRow("PRAGMA quick_check").Scan(&sourceCheck); err != nil {
		t.Errorf("源库 quick_check 失败: %v", err)
	} else if sourceCheck != "ok" {
		t.Errorf("源库 quick_check = %q, 期望 ok", sourceCheck)
	}
}

func TestMigrateCoreDatabaseRejectsExistingTargetAndSamePath(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	_, err = source.Create(PortfolioInput{Name: "源组合"})
	if err != nil {
		source.Close()
		t.Fatalf("创建源组合失败: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("关闭源库失败: %v", err)
	}

	targetPath := filepath.Join(dir, "existing.db")
	target, err := Open(targetPath)
	if err != nil {
		t.Fatalf("打开已有目标库失败: %v", err)
	}
	marker, err := target.Create(PortfolioInput{Name: "目标保留"})
	if err != nil {
		target.Close()
		t.Fatalf("创建目标标记组合失败: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("关闭已有目标库失败: %v", err)
	}

	if _, err := MigrateCoreDatabase(sourcePath, targetPath); err == nil {
		t.Fatal("目标库已存在时应拒绝迁移")
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("拒绝迁移不应删除已有目标库: %v", err)
	}
	preserved, err := Open(targetPath)
	if err != nil {
		t.Fatalf("重新打开已有目标库失败: %v", err)
	}
	got, err := preserved.Get(marker.ID)
	preserved.Close()
	if err != nil {
		t.Fatalf("已有目标库内容未保留: %v", err)
	}
	if got.Name != "目标保留" {
		t.Fatalf("已有目标库标记被改变: %q", got.Name)
	}

	if _, err := MigrateCoreDatabase(sourcePath, sourcePath); err == nil {
		t.Fatal("源库和目标库相同时应拒绝迁移")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("同路径拒绝不应删除源库: %v", err)
	}
}
