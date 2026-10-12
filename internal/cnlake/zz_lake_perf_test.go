package cnlake

import (
	"os"
	"testing"
	"time"
)

// **批量读 vs 逐只读的对比探针。**
//
// 数据湖按**日**分区存放（`DailyBarsInRange` 只打开区间内真正需要的日文件）。
// 而服务层回填走的是 `provider.DailyBars(code, from, to)` —— 逐只查。
// 若逐只查内部也是"打开区间内所有日文件再按代码过滤"，那么
// N 只股票 × M 个日文件 的重复扫描就是冷取数的瓶颈。
//
// 这个探针直接量两边的量级差，用来决定要不要把回填改成"按日批量读"。
//
//	FREESTOCK_LAKE_PERF=1 go test ./internal/cnlake/ -run TestLakeBatchReadPerf -v -count=1
func TestLakeBatchReadPerf(t *testing.T) {
	if os.Getenv("FREESTOCK_LAKE_PERF") != "1" {
		t.Skip("湖读取性能探针：设 FREESTOCK_LAKE_PERF=1 才跑")
	}
	root := Detect()
	if root == "" {
		t.Skip("数据湖未就绪")
	}
	lake := Open(root)

	// ---- 批量读（按日分区，全市场）----
	var sampleCodes []string
	for _, item := range []struct {
		name       string
		start, end int64
	}{
		{"1 年", 20250101, 20251231},
		{"3 年", 20230101, 20251231},
	} {
		began := time.Now()
		rows, err := lake.DailyBarsInRange(nil, item.start, item.end)
		elapsed := time.Since(began)
		if err != nil {
			t.Errorf("%s 批量读失败：%v", item.name, err)
			continue
		}
		codes := map[string]bool{}
		for _, row := range rows {
			codes[row.Symbol] = true
			if len(sampleCodes) < 5 {
				sampleCodes = append(sampleCodes, row.Symbol)
			}
		}
		t.Logf("%s 批量读（全市场）：%d 行 / %d 只，耗时 %s",
			item.name, len(rows), len(codes), elapsed.Round(time.Millisecond))
		if len(codes) > 0 {
			t.Logf("   → 平均每只股票摊到 %s", (elapsed / time.Duration(len(codes))).Round(time.Microsecond))
		}
	}

	if len(sampleCodes) == 0 {
		t.Skip("没有取到样本代码")
	}

	// ---- 逐只读：同一年数区间，量"每只一次窗口读"的成本 ----
	for _, years := range [][2]int{{2025, 2025}, {2020, 2026}} {
		began := time.Now()
		got := 0
		for _, code := range sampleCodes {
			bars, err := lake.DailyBars(code, years[0], years[1])
			if err == nil {
				got += len(bars)
			}
		}
		perCode := time.Since(began) / time.Duration(len(sampleCodes))
		t.Logf("逐只读 %d~%d：%d 只，平均 %s/只（共 %d 根）",
			years[0], years[1], len(sampleCodes), perCode.Round(time.Millisecond), got)
		t.Logf("   → 全市场 7600 只外推：%s", (perCode * 7600).Round(time.Second))
	}
}
