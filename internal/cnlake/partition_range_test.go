package cnlake

import (
	"os"
	"path/filepath"
	"testing"
)

// 分区名 → 覆盖区间。用户的数据湖是**按月**分区（trade_date=2026-09），
// 这条解析路径决定"一次查询要读几个文件"，算错就等于每次全表扫描。
func TestPartitionInterval(t *testing.T) {
	cases := []struct {
		name      string
		first     int64
		last      int64
		wantParse bool
	}{
		{"trade_date=2026-11-14", 20261114, 20261114, true},
		{"trade_date=20261114", 20261114, 20261114, true},
		{"trade_date=2026-11", 20261101, 20261130, true},
		{"trade_date=202611", 20261101, 20261130, true},
		{"trade_date=2026-02", 20260201, 20260228, true},  // 平年
		{"trade_date=2024-02", 20240201, 20240229, true},  // 闰年
		{"trade_date=2026Q4", 20261001, 20261231, true},
		{"trade_date=2026q1", 20260101, 20260331, true},
		{"trade_date=2026", 20260101, 20261231, true},
		{"trade_date=xx", 0, 0, false},
		{"trade_date=2026Q9", 0, 0, false},
		{"trade_date=", 0, 0, false},
		{"other=2026-11", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last, ok := partitionInterval(tc.name)
			if ok != tc.wantParse {
				t.Fatalf("可解析应为 %v，实际 %v", tc.wantParse, ok)
			}
			if !ok {
				return
			}
			if first != tc.first || last != tc.last {
				t.Errorf("区间应为 %d..%d，实际 %d..%d", tc.first, tc.last, first, last)
			}
		})
	}
}

// 按月分区的湖：查询 11 月只能命中 11 月那个分区，不能把全年都读进去。
//
// 这是最重要的回归：修复前所有按月分区都会被当成"无法识别"，
// 于是任何区间查询都返回**全部文件**，数据越多越慢。
func TestFilesForDateRangeMonthlyLayout(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "curated", "daily_bars")
	partitions := []string{"2026-09", "2026-10", "2026-11", "2025-12"}
	for _, p := range partitions {
		sub := filepath.Join(dir, "trade_date="+p)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "part-0.parquet"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	lake := Open(root)
	files, err := lake.filesForDateRange(20261101, 20261130)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("11 月查询应只命中 1 个分区，实际 %d 个：%v", len(files), files)
	}
	if filepath.Base(filepath.Dir(files[0])) != "trade_date=2026-11" {
		t.Errorf("命中了错误的分区：%s", files[0])
	}

	// 跨月区间
	files, err = lake.filesForDateRange(20261015, 20261114)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("10 月中到 11 月中应命中 2 个分区，实际 %d 个", len(files))
	}

	// 不设起点（全历史）
	files, err = lake.filesForDateRange(0, 20261130)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("不设起点应命中全部 4 个分区，实际 %d 个", len(files))
	}
}

// 按年分区的湖：同样不能退化成全表。
func TestFilesForDateRangeYearlyLayout(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "curated", "daily_bars")
	for _, p := range []string{"2024", "2025", "2026"} {
		sub := filepath.Join(dir, "trade_date="+p)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "part-0.parquet"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	lake := Open(root)
	files, err := lake.filesForDateRange(20250301, 20250601)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("2025 年查询应只命中 1 个分区，实际 %d 个", len(files))
	}
}
