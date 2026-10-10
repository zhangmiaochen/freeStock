package service

import (
	"os"
	"sort"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// ⚠ **回归验证**：`shareHistory` 的股本索引必须与原始 `ShareStructures(code)` 一致。
//
// 背景：`ShareStructures(code)` 返回前会按 ChangeDate 排序，而
// `AllShareStructures()`（批量路径）**不保证顺序**。之前把 shareHistory
// 改成批量索引时丢掉了排序，而 `sharesFor` 依赖"截至 date 的最后一行" ——
// 顺序一乱就提前中断、返回错误的股本，进而让换手率与评分都错。
//
// 这个用例逐代码、逐日期比对两条路径，确保排序修复到位。
func TestShareHistoryMatchesOriginal(t *testing.T) {
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	lake := cnlake.Open(root)
	if !lake.HasDataset("share_structure") {
		t.Skip("数据湖没有 share_structure")
	}
	provider := &lakeProvider{lake: lake}

	// 挑一批有股本变更记录的代码
	all, err := lake.AllShareStructures()
	if err != nil {
		t.Fatalf("读股本失败：%v", err)
	}
	countByCode := map[string]int{}
	for _, row := range all {
		countByCode[cnlake.BareSymbol(row.Symbol)]++
	}
	codes := make([]string, 0, len(countByCode))
	for code, n := range countByCode {
		if code != "" && n >= 2 { // 只挑有多次变更的 —— 顺序错了才看得出来
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	if len(codes) > 60 {
		codes = codes[:60]
	}
	if len(codes) == 0 {
		t.Skip("没有多变更多于 1 次的代码")
	}

	checked := 0
	for _, code := range codes {
		// 原始路径：ShareStructures 会排序
		original, err := lake.ShareStructures(code)
		if err != nil {
			t.Fatalf("%s 原始读失败：%v", code, err)
		}
		// 新路径：批量索引
		indexed := provider.shareHistory(code)

		if len(original) != len(indexed) {
			t.Errorf("%s 行数不一致：原始 %d，索引 %d", code, len(original), len(indexed))
			continue
		}

		// 逐个测试日期：用股本变更日、变更日前后各一天
		dates := []int64{0, 19900101}
		for _, row := range original {
			d := row.Date()
			dates = append(dates, d-1, d, d+1)
		}
		dates = append(dates, 20991231)

		for _, date := range dates {
			wantTotal, wantFloat, wantOK := linearSharesFor(original, date)
			gotTotal, gotFloat, gotOK := sharesFor(indexed, date)
			if wantOK != gotOK || wantTotal != gotTotal || wantFloat != gotFloat {
				t.Errorf("%s @%d 不一致：\n  原始 ok=%v total=%v float=%v\n  索引 ok=%v total=%v float=%v",
					code, date, wantOK, wantTotal, wantFloat, gotOK, gotTotal, gotFloat)
				break
			}
			checked++
		}
	}
	t.Logf("股本查找逐日期比对通过：%d 只、%d 个日期点", len(codes), checked)
}

// linearSharesFor 是**原来的实现**，作为参照基准。
func linearSharesFor(rows []cnlake.ShareStructureRow, date int64) (total, float float64, ok bool) {
	for _, row := range rows {
		if row.Date() > date {
			break
		}
		total, float, ok = row.TotalShares, row.FloatShares, true
	}
	return total, float, ok
}
