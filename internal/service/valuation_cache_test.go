package service

import (
	"testing"
	"time"

	"freestock/internal/cnlake"
)

// TestDatasetCacheHits 钉住数据集缓存真的命中。
//
// 这个用例是有来历的：写完之后实测第二次读仍然花了 16 秒、且返回了不同的对象，
// 说明缓存没生效。所以必须有断言把"命中"这件事钉死，而不是靠肉眼看日志
// （测试里 slog 默认是 Info 级，Debug 根本不会打印 —— 我第一版就是被这一点骗了）。
func TestDatasetCacheHits(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	first, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("第一次加载失败: %v", err)
	}
	if first == nil {
		t.Fatal("第一次应返回数据")
	}

	// 断言缓存条目真的被写进去了
	svc.valMu.Lock()
	entry := svc.valDataset
	svc.valMu.Unlock()
	if entry == nil {
		t.Fatal("loadValuationDataset 返回后 valDataset 缓存仍为空 —— 写入逻辑没生效")
	}
	t.Logf("缓存条目：代次 %d / 当前代次 %d / 评估日 %d / 建于 %s",
		entry.gen, svc.universeGen.Load(), entry.evalDate, entry.builtAt.Format("15:04:05"))

	started := time.Now()
	second, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("第二次加载失败: %v", err)
	}
	cost := time.Since(started)

	if second != first {
		t.Errorf("第二次应返回**同一个**数据集对象（缓存命中），实际是不同对象")
	}
	// 命中缓存应该是常数级耗时；读一遍数据湖要十几秒
	if cost > 500*time.Millisecond {
		t.Errorf("第二次加载花了 %s —— 缓存没命中（读一遍数据湖要十几秒）", cost)
	}
	t.Logf("第二次耗时 %s（命中缓存）", cost.Round(time.Microsecond))
}
