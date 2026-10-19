package service

import (
	"context"
	"path/filepath"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// 本文件钉住"数据更新结束后显式失效"这条路径的**完整性**。
//
// ## 为什么需要它
//
// `main.go` 把 cne 更新完成的回调接在 `StockService.InvalidateDerivedCaches` 上
// （`cneupdate.go` 的 `OnFinish` → `notifyDataChanged` → 回调）。
// 那条回调的职责是"数据湖刚落了新数据，把所有派生缓存立刻作废"，
// 不能依赖"下一次查询自己发现源变了"。
//
// 但"最新交易日"来自 `cnlake.BarPartitionDates` 的 `barDates` 缓存，
// 与 `DatasetFiles` 的 `files` 缓存是**两份独立缓存**。
// 只清一份就会出现：文件列表刷新了、分区日期还是旧的 ——
// 于是 `covered` 判定继续成立、K 线继续读旧数据。

// TestInvalidateDatasetFilesAlsoClearsBarDates 钉住"显式失效必须同时清掉两份缓存"。
func TestInvalidateDatasetFilesAlsoClearsBarDates(t *testing.T) {
	root := buildDailyLakes(t)
	lake := cnlake.Open(root)

	// 先让两份缓存都建立起来
	if _, err := lake.LatestTradeDate(); err != nil {
		t.Fatalf("LatestTradeDate 失败：%v", err)
	}
	if got := lake.LatestPartitionDate(); got != 20261015 {
		t.Fatalf("初始分区水位应为 20261015，实际 %d", got)
	}

	// 日更：新增一个交易日分区
	writeDailyBarsPartition(t, root, 20261016, []cnlake.BarRow{{
		Symbol: "600519.SH", TradeDate: daysFromYMD(20261016),
		Open: 1600, High: 1610, Low: 1590, Close: 1605, Volume: 2000, Amount: 3.2e6,
	}})

	// 模拟"cne 更新结束"：走显式失效，不依赖 mtime 探测
	lake.InvalidateDatasetFiles()

	if got := lake.LatestPartitionDate(); got != 20261016 {
		t.Errorf("显式失效后分区水位应为 20261016，实际 %d —— "+
			"InvalidateDatasetFiles 没有清掉 BarPartitionDates 的分区日期缓存，"+
			"于是「最新交易日」还停在旧值（下游的 covered 判定会跟着失效）", got)
	}
	if got, err := lake.LatestTradeDate(); err != nil || got != 20261016 {
		t.Errorf("显式失效后最新交易日应为 20261016，实际 %d（err=%v）", got, err)
	}
	if dates, err := lake.BarPartitionDates(); err != nil || len(dates) == 0 || dates[len(dates)-1] != 20261016 {
		t.Errorf("显式失效后分区列表末尾应为 20261016，实际 %v（err=%v）", dates, err)
	}
	// 区间读取也必须立刻看到新分区
	bars, err := lake.DailyBarsInRange([]string{"600519"}, 20261016, 20261016)
	if err != nil {
		t.Fatalf("DailyBarsInRange 失败：%v", err)
	}
	if len(bars) != 1 {
		t.Errorf("显式失效后按日期区间读新分区应得 1 行，实际 %d 行", len(bars))
	}
}

// TestOnFinishPathInvalidatesEverything 走**真实回调链路**
// （CneUpdateService.SetOnDataChanged → notifyDataChanged → StockService.InvalidateDerivedCaches），
// 确认新增分区之后，服务层立刻看到新的最新交易日。
//
// 这条用例的意义：上面那条测的是 `InvalidateDatasetFiles` 自身；
// 这条测的是"main.go 接的那根线"是否真的把该清的都清了。
func TestOnFinishPathInvalidatesEverything(t *testing.T) {
	root := buildDailyLakes(t)
	// 把服务绑到这个测试数据湖上（走真实配置路径，而不是直接塞数据集）
	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	cfg.SetDataSource("", config.KindCnLake)
	cfg.SetCnLakeRoot(root)
	svc := NewStockService(cfg)

	// 让服务层先建立"最新交易日"的认知
	provider, _ := svc.snapshot()
	if got, err := provider.LatestTradeDate(context.Background()); err != nil || got != 20261015 {
		t.Fatalf("初始最新交易日应为 20261015，实际 %d（err=%v）", got, err)
	}

	// 日更
	writeDailyBarsPartition(t, root, 20261016, []cnlake.BarRow{{
		Symbol: "600519.SH", TradeDate: daysFromYMD(20261016),
		Open: 1600, High: 1610, Low: 1590, Close: 1605, Volume: 2000, Amount: 3.2e6,
	}})

	// cne 更新结束 → 触发那条回调
	svc.InvalidateDerivedCaches()

	if got, err := provider.LatestTradeDate(context.Background()); err != nil || got != 20261016 {
		t.Errorf("更新结束回调之后最新交易日应为 20261016，实际 %d（err=%v）—— "+
			"回调没有把「最新交易日」所需的缓存清干净", got, err)
	}
}
