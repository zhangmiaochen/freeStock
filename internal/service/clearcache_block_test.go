package service

import (
	"context"
	"testing"
	"time"
)

// slowStockCodesProvider 把一个数据源包起来，让 `StockCodes` 变慢 ——
// 模拟"数据湖上按代码逐只读、全量列表要几分钟"的真实情形。
type slowStockCodesProvider struct {
	Provider
	delay time.Duration
}

func (p *slowStockCodesProvider) StockCodes(ctx context.Context) (map[string][]string, error) {
	time.Sleep(p.delay)
	return p.Provider.StockCodes(ctx)
}

// ⚠ 回归测试：**慢加载进行中，切换数据源必须立刻返回**。
//
// 用户报的现象：「切换数据源 → 进股票列表页 → 回数据源设置页 → 再切换，没用」。
//
// 根因：`GetStockUniverse` **全程持 `universeMu`**，而每次切换都会走
// `switchProvider → ClearCache`，那里原来用 `Lock()` 死等这把锁。
// 切到数据湖后全量列表加载极慢，于是切换请求被堵死 = 「点了没反应」。
//
// 这个测试就是钉住"ClearCache 不被慢加载堵住"。
func TestClearCacheDoesNotBlockOnSlowUniverseLoad(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// 换成"加载很慢"的数据源
	inner := newSyntheticLakeProvider(t)
	source := svc.stock.source
	svc.stock.switchProvider(&slowStockCodesProvider{Provider: inner, delay: 3 * time.Second}, source)

	// 后台发起一次全量列表加载（会持锁 3 秒）
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		_, _ = svc.stock.GetStockUniverse(true)
	}()
	time.Sleep(200 * time.Millisecond) // 确保它已经拿到锁、正在加载

	// ★ 此时 ClearCache 必须**立刻**返回，而不是等那 3 秒
	began := time.Now()
	svc.stock.ClearCache()
	elapsed := time.Since(began)
	if elapsed > 500*time.Millisecond {
		t.Errorf("慢加载进行中时 ClearCache 花了 %s —— 切换数据源会被它堵住（应 <500ms）",
			elapsed.Round(time.Millisecond))
	}
	t.Logf("慢加载进行中，ClearCache 用时 %s", elapsed.Round(time.Millisecond))

	// 整个切换路径也必须立刻返回（这就是用户点的那一下）
	began = time.Now()
	if _, err := svc.stock.SetDataSource("stockdb", "http://127.0.0.1:7899"); err != nil {
		t.Logf("切到 stockdb 失败（可能服务未起）：%v", err)
	}
	t.Logf("慢加载进行中，SetDataSource 用时 %s", time.Since(began).Round(time.Millisecond))

	<-loadDone
}

// 慢加载结束后，若期间切换过数据源，**旧源的结果不能留在缓存里**。
func TestStaleUniverseNotCachedAfterSwitch(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	inner := newSyntheticLakeProvider(t)
	source := svc.stock.source
	svc.stock.switchProvider(&slowStockCodesProvider{Provider: inner, delay: 1500 * time.Millisecond}, source)

	loadResult := make(chan *StockUniverse, 1)
	go func() {
		out, _ := svc.stock.GetStockUniverse(true)
		loadResult <- out
	}()
	time.Sleep(200 * time.Millisecond)

	// 加载期间"切换数据源" —— 这次加载的结果属于旧源
	svc.stock.ClearCache()

	// 等这次加载结束
	select {
	case <-loadResult:
	case <-time.After(10 * time.Second):
		t.Fatal("加载没在预期时间内结束")
	}

	// 旧源的缓存不能被留下
	svc.stock.universeMu.Lock()
	cached := svc.stock.universe
	svc.stock.universeMu.Unlock()
	if cached != nil {
		t.Errorf("切换后旧源的列表缓存应被清空，实际仍留有 %d 只", len(cached.Stocks))
	}
	t.Logf("加载期间切换过数据源 → 缓存已被清空（cached=%v）", cached == nil)
}
