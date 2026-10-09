package service

import (
	"fmt"
	"path/filepath"
	"testing"

	"freestock/internal/cnlake/cnlaketest"
	"freestock/internal/config"
)

// 完全照前端的做法：用 GetDataSources() 返回的 kind/url 去切，
// 并打印列表，看界面实际拿到的是什么。
func TestSetDataSourceUsingListedValues(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败：%v", err)
	}
	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewStockService(cfg)

	dump := func(label string) {
		for _, info := range svc.GetDataSources() {
			t.Logf("  [%s] key=%-8s kind=%-8s current=%-5v url=%q name=%q",
				label, info.Key, info.Kind, info.Current, info.URL, info.Name)
		}
		cur := svc.GetCurrentDataSource()
		t.Logf("  [%s] GetCurrentDataSource → key=%s kind=%s url=%q name=%q",
			label, cur.Key, cur.Kind, cur.URL, cur.Name)
	}

	dump("初始")

	// ① 切数据湖：用列表里的 cnlake 条目
	var lakeInfo DataSourceInfo
	for _, info := range svc.GetDataSources() {
		if info.Key == "cnlake" {
			lakeInfo = info
		}
	}
	if _, err := svc.SetDataSource(lakeInfo.Kind, lakeInfo.URL); err != nil {
		t.Fatalf("按列表值切数据湖失败：kind=%s url=%q err=%v", lakeInfo.Kind, lakeInfo.URL, err)
	}
	dump("切湖后")

	// ② 切回本机 stockdb：用列表里的 local 条目
	var localInfo DataSourceInfo
	for _, info := range svc.GetDataSources() {
		if info.Key == "local" {
			localInfo = info
		}
	}
	t.Logf("准备用 kind=%q url=%q 切回本机 stockdb", localInfo.Kind, localInfo.URL)

	status, err := svc.SetDataSource(localInfo.Kind, localInfo.URL)
	if err != nil {
		t.Skipf("本机 free-stockdb 不可用，跳过（%v）", err)
	}
	t.Logf("切换返回：ok=%v kind=%s url=%q msg=%q", status.OK, status.Kind, status.URL, status.Message)
	dump("切回后")

	provider, source := svc.snapshot()
	if provider.Kind() != SourceKindStockDB {
		t.Errorf("provider 应切回 stockdb，实际 %s", provider.Kind())
	}
	if source.Key != "local" {
		t.Errorf("source.Key 应为 local，实际 %q（会导致界面标错「使用中」）", source.Key)
	}
	for _, info := range svc.GetDataSources() {
		if info.Key == "local" && !info.Current {
			t.Errorf("「本机 stockdb」应标为使用中，实际 current=false")
		}
		if info.Key == "cnlake" && info.Current {
			t.Errorf("数据湖不该还是使用中")
		}
	}
}

// ⚠ 这个用例覆盖的是**真实的用户路径** `SetDataSource`。
//
// 之前的测试都走 `applySource`（内部函数），于是
// 「先切到数据湖、再切回本机 stockdb」这条路**从来没被验证过** ——
// 而这正是用户报的问题。
//
// 本机没有 free-stockdb 时（Ping 失败）自动跳过 stockdb 那半段。
func TestSetDataSourceLakeBackToStockdb(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败：%v", err)
	}

	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewStockService(cfg)

	// ---- ① 切到数据湖 ----
	status, err := svc.SetDataSource(config.KindCnLake, lakeRoot)
	if err != nil {
		t.Fatalf("切到数据湖失败：%v", err)
	}
	if !status.OK || status.Kind != config.KindCnLake {
		t.Fatalf("状态不对：ok=%v kind=%s", status.OK, status.Kind)
	}
	if _, source := svc.snapshot(); source.Kind != config.KindCnLake {
		t.Fatalf("切到数据湖后 source.Kind 应为 cnlake，实际 %s", source.Kind)
	}
	if cfg.DataSourceKind() != config.KindCnLake {
		t.Fatalf("配置里应是 cnlake，实际 %s", cfg.DataSourceKind())
	}
	t.Logf("① 已切到数据湖 %s", lakeRoot)

	// ---- ② 切回本机 stockdb ----
	const localURL = "http://127.0.0.1:7899"
	status2, err := svc.SetDataSource(config.KindStockDB, localURL)
	if err != nil {
		t.Skipf("本机 free-stockdb 不可用，跳过（%v）", err)
	}
	if status2.Kind != config.KindStockDB {
		t.Errorf("状态 Kind 应为 stockdb，实际 %s", status2.Kind)
	}

	// ★ 关键一：provider 必须真的换回 stockdb
	provider, source := svc.snapshot()
	if provider == nil {
		t.Fatal("切换后 provider 不应为 nil")
	}
	if provider.Kind() != SourceKindStockDB {
		t.Errorf("provider 应切回 stockdb，实际 %s", provider.Kind())
	}
	if source.Kind != config.KindStockDB {
		t.Errorf("source.Kind 应为 stockdb，实际 %s", source.Kind)
	}
	t.Logf("② 已切回 stockdb：provider=%s source=%s(%s)",
		provider.Kind(), source.Key, source.Kind)

	// ★ 关键二：配置必须落盘成 stockdb
	if got := cfg.DataSourceKind(); got != config.KindStockDB {
		t.Errorf("配置里的 kind 应为 stockdb，实际 %s", got)
	}
	if got := cfg.DataSourceURL(); got != localURL {
		t.Errorf("配置里的 url 应为 %s，实际 %s", localURL, got)
	}

	// ★ 关键三：界面列表里必须是 local 使用中、cnlake 不再使用中
	var sawLocal, sawLake bool
	for _, info := range svc.GetDataSources() {
		switch info.Key {
		case "local":
			sawLocal = true
			if !info.Current {
				t.Errorf("「本机 stockdb」(local) 应标为使用中，实际 current=false（url=%s kind=%s）",
					info.URL, info.Kind)
			}
		case "cnlake":
			sawLake = true
			if info.Current {
				t.Error("数据湖不该还是「使用中」")
			}
		}
	}
	if !sawLocal {
		t.Error("列表里没有 local 预设")
	}
	if !sawLake {
		t.Error("列表里没有 cnlake 预设")
	}

	// ★ 关键四：再切回数据湖也要能生效（来回切不能卡住）
	if _, err := svc.SetDataSource(config.KindCnLake, lakeRoot); err != nil {
		t.Fatalf("再切回数据湖失败：%v", err)
	}
	if provider, _ := svc.snapshot(); provider.Kind() != SourceKindCnLake {
		t.Errorf("再切回数据湖后 provider 应为 cnlake，实际 %s", provider.Kind())
	}
}

// 来回切换多次都要稳定（覆盖缓存清理与 lake 句柄复用）。
func TestSetDataSourceRoundTrip(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败：%v", err)
	}
	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewStockService(cfg)

	const localURL = "http://127.0.0.1:7899"
	if _, err := svc.SetDataSource(config.KindStockDB, localURL); err != nil {
		t.Skipf("本机 free-stockdb 不可用，跳过（%v）", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := svc.SetDataSource(config.KindCnLake, lakeRoot); err != nil {
			t.Fatalf("第 %d 轮切数据湖失败：%v", i+1, err)
		}
		if provider, _ := svc.snapshot(); provider.Kind() != SourceKindCnLake {
			t.Fatalf("第 %d 轮切数据湖后 provider 错：%s", i+1, provider.Kind())
		}
		if _, err := svc.SetDataSource(config.KindStockDB, localURL); err != nil {
			t.Fatalf("第 %d 轮切回 stockdb 失败：%v", i+1, err)
		}
		if provider, _ := svc.snapshot(); provider.Kind() != SourceKindStockDB {
			t.Fatalf("第 %d 轮切回 stockdb 后 provider 错：%s", i+1, provider.Kind())
		}
	}
	t.Log(fmt.Sprintf("来回切 3 轮均正常，最终 kind=%s", cfg.DataSourceKind()))
}
