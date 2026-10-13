package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// 数据更新结束后必须**主动**让派生缓存失效 —— 不能只靠"下一次查询自己发现
// 源分区指纹变了"。这条用例钉住这条链路：
//
//	CneUpdateService.SetOnDataChanged → notifyDataChanged → 回调
//	StockService.InvalidateDerivedCaches → lakeProvider.InvalidateSymbolBars
func TestCneUpdateNotifiesDataChanged(t *testing.T) {
	svc := NewCneUpdateService(config.NewStoreAt(filepath.Join(t.TempDir(), "config.json")))

	var calls atomic.Int32
	svc.SetOnDataChanged(func() { calls.Add(1) })

	// Runner 的选项里必须带上这个回调 —— 否则任务结束根本不会通知
	if svc.runnerOptions().OnFinish == nil {
		t.Fatal("Runner 选项里必须有 OnFinish 回调")
	}
	svc.notifyDataChanged(nil, nil)
	if calls.Load() != 1 {
		t.Fatalf("回调应当被调用一次，实际 %d", calls.Load())
	}

	// 更新失败（被取消 / 中途报错）也要失效：可能已经落了一半数据
	svc.notifyDataChanged(nil, os.ErrDeadlineExceeded)
	if calls.Load() != 2 {
		t.Errorf("失败的任务同样要通知，实际 %d 次", calls.Load())
	}

	// 没有注册回调时不能 panic
	empty := NewCneUpdateService(config.NewStoreAt(filepath.Join(t.TempDir(), "config.json")))
	empty.notifyDataChanged(nil, nil)
}

// StockService.InvalidateDerivedCaches 必须落到数据湖行情源的按股票缓存上，
// 并且真的把"下次重建允许时间"清空（否则更新刚结束时会因为限流而不重建）。
func TestInvalidateDerivedCachesClearsThrottle(t *testing.T) {
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	provider, _ := svc.snapshot()
	lake, ok := provider.(*lakeProvider)
	if !ok {
		t.Fatalf("数据湖行情源类型不对：%T", provider)
	}
	if lake.symbolCache() == nil {
		t.Fatal("按股票缓存应当已挂上")
	}

	// 人为把限流与退避都推到未来：失效之后它们必须被清掉
	lake.symbolMu.Lock()
	lake.symbolBuildNextAt = farFuture()
	lake.symbolBuildRetryAt = farFuture()
	lake.symbolMu.Unlock()

	svc.InvalidateDerivedCaches()

	lake.symbolMu.Lock()
	next, retry := lake.symbolBuildNextAt, lake.symbolBuildRetryAt
	lake.symbolMu.Unlock()
	if !next.IsZero() || !retry.IsZero() {
		t.Errorf("失效后应当清掉限流与退避：next=%v retry=%v", next, retry)
	}
}

// 缓存目录必须落在配置目录下（而不是写进用户的数据湖里）：
// freeStock 自己的缓存要能随手删掉，不去动 cne 的产物。
func TestSymbolCacheLivesUnderConfigDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	svc := NewStockService(config.NewStoreAt(configPath))

	root := svc.symbolBarsCacheRoot(`D:\workspace\stockTool\CNEquity\data\cnequity`)
	if root == "" {
		t.Fatal("应当给出缓存目录")
	}
	if want := filepath.Join(dir, "cache", "bars-by-symbol"); !strings.HasPrefix(root, want) {
		t.Errorf("缓存目录应当在配置目录下：got %q want 前缀 %q", root, want)
	}
	if strings.HasPrefix(root, `D:\workspace\stockTool\CNEquity`) {
		t.Errorf("缓存目录不该写进数据湖：%q", root)
	}
	_ = cnlake.SymbolBarsVersion // 版本号参与目录/清单约定，别被误删
}

func farFuture() time.Time { return time.Now().Add(24 * time.Hour) }
