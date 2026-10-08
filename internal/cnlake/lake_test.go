package cnlake

import (
	"context"
	"path/filepath"
	"testing"
)

// 这些测试针对本机已存在的 cnequity 数据湖；没有数据湖时自动 skip。
func openLocalLake(t *testing.T) *Lake {
	t.Helper()
	lake := Open("")
	if !lake.Available() {
		t.Skip("本机没有 cnequity 数据湖，跳过")
	}
	t.Logf("数据湖: %s", lake.Root())
	return lake
}

func TestDetectAndListDatasets(t *testing.T) {
	lake := openLocalLake(t)

	datasets := lake.Datasets()
	if len(datasets) == 0 {
		t.Fatal("curated 下没有任何数据集")
	}
	t.Logf("已有数据集: %v", datasets)

	// instruments 是 cnequity init 的第一阶段产物，正常情况下一定在
	if !lake.HasDataset("instruments") {
		t.Errorf("缺少 instruments 数据集，实际有 %v", datasets)
	}
}

func TestReadInstrumentsRows(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("instruments") {
		t.Skip("没有 instruments 数据集")
	}

	rows, err := lake.Rows("instruments", 5)
	if err != nil {
		t.Fatalf("读取 instruments 失败: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("期望 5 行，实际 %d 行", len(rows))
	}

	first := rows[0]
	t.Logf("首行: %+v", first)

	// schema 里的列都应被读出来
	for _, key := range []string{"symbol", "name", "exchange", "asset_type", "list_date"} {
		if _, ok := first[key]; !ok {
			t.Errorf("缺少列 %q，实际列: %v", key, keysOf(first))
		}
	}
	if symbol, _ := first["symbol"].(string); symbol == "" {
		t.Error("symbol 不该为空")
	}
	if name, _ := first["name"].(string); name == "" {
		t.Error("name 不该为空")
	}

	// DATE 列应被折算成 YYYYMMDD 整数
	if listDate, ok := first["list_date"].(int64); ok {
		if listDate < 19000101 || listDate > 21001231 {
			t.Errorf("list_date 折算异常: %d", listDate)
		}
		t.Logf("list_date 折算为 %d", listDate)
	} else if first["list_date"] != nil {
		t.Errorf("list_date 类型异常: %T %v", first["list_date"], first["list_date"])
	}
}

func TestReadWholeDatasetWithoutLimit(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("trading_calendar") {
		t.Skip("没有 trading_calendar 数据集")
	}

	rows, err := lake.Rows("trading_calendar", 0)
	if err != nil {
		t.Fatalf("读取 trading_calendar 失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("trading_calendar 读出来是空的")
	}
	t.Logf("trading_calendar 共 %d 行，首行 %+v", len(rows), rows[0])
}

func TestDatasetStatus(t *testing.T) {
	lake := openLocalLake(t)

	statuses := lake.Status(context.Background())
	if len(statuses) == 0 {
		t.Fatal("状态列表为空")
	}

	byName := map[string]DatasetStatus{}
	for _, status := range statuses {
		byName[status.Name] = status
		t.Logf("%-30s exists=%-5v files=%-3d rows=%-8d latest=%d %s",
			status.Name, status.Exists, status.Files, status.Rows, status.LatestDate, status.Error)
	}

	// 当前 cnequity 的 init 卡在 daily_bars，财报还没入库。
	// 这里只断言状态查得出来，不假定一定有（用户把 init 跑通后就该有了）。
	if _, ok := byName["financial_statement_items"]; !ok {
		t.Error("状态列表里应包含 financial_statement_items")
	}
}

func TestStatementItemsWithoutData(t *testing.T) {
	lake := openLocalLake(t)
	if lake.HasDataset("financial_statement_items") {
		t.Skip("财报已入库，这个用例不适用")
	}

	if _, err := lake.StatementItems(context.Background(), "600519", 10); err == nil {
		t.Error("财报未入库时应返回错误而不是空结果")
	} else {
		t.Logf("预期内的错误: %v", err)
	}
}

func TestOpenMissingLake(t *testing.T) {
	lake := Open(`Z:\definitely\not\here`)
	if lake.Available() {
		t.Error("不存在的路径不该被判定为可用")
	}
	if datasets := lake.Datasets(); datasets != nil {
		t.Errorf("不可用时数据集应为空，得到 %v", datasets)
	}
	if _, err := lake.Rows("whatever", 1); err == nil {
		t.Error("不可用时读取应报错")
	}
}

func TestToDateNumber(t *testing.T) {
	cases := []struct {
		in     any
		want   int64
		wantOK bool
	}{
		{20240131, int64(20240131), true},
		{int64(20240131), int64(20240131), true},
		{int32(20240131), int64(20240131), true},
		{"2024-01-31", int64(20240131), true},
		{"2024Q4", int64(2024), false}, // 只有 4 位数字，不足以构成日期
		{"", 0, false},
		{nil, 0, false},
	}
	for _, item := range cases {
		got, ok := toDateNumber(item.in)
		if ok != item.wantOK {
			t.Errorf("toDateNumber(%v) ok=%v，期望 %v", item.in, ok, item.wantOK)
			continue
		}
		if ok && got != item.want {
			t.Errorf("toDateNumber(%v) = %d，期望 %d", item.in, got, item.want)
		}
	}
}

func TestDaysToDate(t *testing.T) {
	// 1970-01-01 是第 0 天
	if got := daysToDate(0); got != 19700101 {
		t.Errorf("daysToDate(0) = %d，期望 19700101", got)
	}
	// 2024-01-31 距 1970-01-01 共 19753 天
	if got := daysToDate(19753); got != 20240131 {
		t.Errorf("daysToDate(19753) = %d，期望 20240131", got)
	}
}

func keysOf(row map[string]any) []string {
	out := make([]string, 0, len(row))
	for key := range row {
		out = append(out, key)
	}
	return out
}

// partitionYear 是未导出函数，只能在内部测试包里验证。
// 各种分区粒度都以年份开头，所以取前 4 位即可 —— 这个约定是分区裁剪的关键。
func TestPartitionYear(t *testing.T) {
	dir := filepath.Join("lake", "curated", "daily_bars")
	cases := []struct {
		path string
		want int
		ok   bool
	}{
		{filepath.Join(dir, "trade_date=2025", "part-merged.parquet"), 2025, true},
		{filepath.Join(dir, "trade_date=2025-03", "part-merged.parquet"), 2025, true},
		{filepath.Join(dir, "trade_date=2025Q1", "part-merged.parquet"), 2025, true},
		{filepath.Join(dir, "trade_date=2025-03-14", "part-merged.parquet"), 2025, true},
		{filepath.Join(dir, "report_period=2024Q4", "part-merged.parquet"), 2024, true},
		{filepath.Join(dir, "part-merged.parquet"), 0, false},
		{filepath.Join(dir, "trade_date=xx", "part-merged.parquet"), 0, false},
	}
	for _, item := range cases {
		got, ok := partitionYear(dir, item.path)
		if ok != item.ok || (ok && got != item.want) {
			t.Errorf("partitionYear(%q) = (%d, %v)，期望 (%d, %v)", item.path, got, ok, item.want, item.ok)
		}
	}
}
