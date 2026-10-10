package cnlake

import (
	"context"
	"testing"
	"time"
)

// 并发读必须**结果与串行一致**（按文件顺序合并）。
func TestReadTypedParallelKeepsOrder(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars，跳过")
	}
	all := lake.DatasetFiles("daily_bars")
	if len(all) < 2 {
		t.Skipf("文件太少（%d），测不出并发", len(all))
	}
	// 只取前 30 个文件，够验证顺序又不会太慢
	files := all
	if len(files) > 30 {
		files = files[:30]
	}

	beganA := time.Now()
	first, err := readTyped[BarRow](files, nil)
	if err != nil {
		t.Fatalf("第一次读失败：%v", err)
	}
	elapsedA := time.Since(beganA)

	second, err := readTyped[BarRow](files, nil)
	if err != nil {
		t.Fatalf("第二次读失败：%v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("两次读取条数不同：%d vs %d", len(first), len(second))
	}
	if len(first) > 1 {
		if first[0] != second[0] {
			t.Errorf("首行不一致：%+v vs %+v", first[0], second[0])
		}
		if first[len(first)-1] != second[len(second)-1] {
			t.Errorf("末行不一致")
		}
	}
	t.Logf("daily_bars 前 %d 个文件 / %d 行，耗时 %s（两次结果一致）",
		len(files), len(first), elapsedA.Round(time.Millisecond))
}

// 并发度上限为 8，且不超过文件数。
// lakeReadWorkers 的边界。
//
// ⚠ 上限原先是硬编码 8，理由是"磁盘 IO 密集"。那个判断是错的 ——
// parquet 读取里占大头的是 zstd 解压 + thrift 解码 + 列转换（CPU profile 全是热点），
// 属于 **CPU 密集**，压在 8 等于在 16 核机器上白扔一半算力。
// 现在上限是 lakeReadMaxWorkers（32），这里跟着常量走，改上限时不会再挂。
func TestLakeReadWorkersBounds(t *testing.T) {
	if got := lakeReadWorkers(0); got != 1 {
		t.Errorf("0 个文件应回落到 1，实际 %d", got)
	}
	if got := lakeReadWorkers(1); got != 1 {
		t.Errorf("1 个文件应为 1，实际 %d", got)
	}
	if got := lakeReadWorkers(1000); got > lakeReadMaxWorkers {
		t.Errorf("上限应为 %d，实际 %d", lakeReadMaxWorkers, got)
	}
	if got := lakeReadWorkers(3); got != 3 {
		t.Errorf("3 个文件应为 3，实际 %d", got)
	}
}

// 单股财报查询的端到端耗时（对照文档里 DuckDB 的 0.38s）。
func TestStatementItemsSingleSymbolTiming(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("financial_statement_items") {
		t.Skip("数据湖没有 financial_statement_items，跳过")
	}

	began := time.Now()
	items, err := lake.StatementItems(context.Background(), "600519.SH", 0)
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	t.Logf("600519.SH 财报科目 %d 条，耗时 %s（文档里 DuckDB 视图就绪后是 0.38s）",
		len(items), elapsed.Round(time.Millisecond))
}
