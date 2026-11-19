package service

import (
	"testing"
	"time"
)

// 回归：K 线页搜索框逐字调 SearchStocks，绝不能为了一次联想触发全市场行情加载。
//
// 实测过一次事故：搜索走 GetStockUniverse → 批量读当期分区 + 估值索引，
// 冷启动 9 分 36 秒、堆增长 200MB。表现就是"输入框卡住、内存飙升"。
func TestSearchStocksDoesNotLoadQuotes(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("instruments") {
		t.Skip("数据湖缺少 instruments")
	}
	if _, err := svc.stock.SetDataSource("cnlake", lake.Root()); err != nil {
		t.Skipf("切湖失败：%v", err)
	}

	began := time.Now()
	hits, err := svc.stock.SearchStocks("茅台", 12)
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	elapsed := time.Since(began)
	if elapsed > 10*time.Second {
		t.Fatalf("搜索用了 %s，说明又去加载行情了", elapsed)
	}
	if len(hits) == 0 {
		t.Fatal("应能搜到股票")
	}

	// 第二次必须命中缓存（毫秒级）
	began = time.Now()
	if _, err := svc.stock.SearchStocks("600", 12); err != nil {
		t.Fatal(err)
	}
	if second := time.Since(began); second > time.Second {
		t.Fatalf("第二次搜索用了 %s，索引没有缓存", second)
	}
}
