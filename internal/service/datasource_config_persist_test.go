package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/cnlake/cnlaketest"
	"freestock/internal/config"
)

// ⚠ **配置必须真的落盘**：从数据湖切回 stockdb 之后，
// 配置文件里的 `dataSourceKind` / `dataSourceUrl` 必须跟着变。
//
// 用户报的现象是"切到数据湖之后再切别的源，配置没写回本地配置文件" ——
// 所以这里不看内存里的 `snapshot()`，**直接读文件**。
func TestSetDataSourcePersistsToFile(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败：%v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.NewStoreAt(cfgPath)
	svc := NewStockService(cfg)

	readFile := func() map[string]any {
		t.Helper()
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("读配置文件失败：%v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("解析配置文件失败：%v（内容：%s）", err, string(raw))
		}
		return out
	}

	// ① 切到数据湖
	if _, err := svc.SetDataSource(config.KindCnLake, lakeRoot); err != nil {
		t.Fatalf("切到数据湖失败：%v", err)
	}
	file := readFile()
	if got := file["dataSourceKind"]; got != config.KindCnLake {
		t.Errorf("切湖后文件里 kind 应为 cnlake，实际 %v", got)
	}
	if got := file["cnLakeRoot"]; got != lakeRoot {
		t.Errorf("切湖后文件里 cnLakeRoot 应为 %s，实际 %v", lakeRoot, got)
	}
	t.Logf("① 切湖后：kind=%v url=%v cnLakeRoot=%v",
		file["dataSourceKind"], file["dataSourceUrl"], file["cnLakeRoot"])

	// ② 切回本机 stockdb —— 这一步必须把 kind/url 写回文件
	const localURL = "http://127.0.0.1:7899"
	if _, err := svc.SetDataSource(config.KindStockDB, localURL); err != nil {
		t.Skipf("本机 stockdb 不可用，跳过（%v）", err)
	}
	file = readFile()
	if got := file["dataSourceKind"]; got != config.KindStockDB {
		t.Errorf("**切回 stockdb 后文件里 kind 仍为 %v（应为 stockdb）—— 配置没写回**", got)
	}
	if got := file["dataSourceUrl"]; got != localURL {
		t.Errorf("切回后文件里 url 应为 %s，实际 %v", localURL, got)
	}
	t.Logf("② 切回后：kind=%v url=%v cnLakeRoot=%v",
		file["dataSourceKind"], file["dataSourceUrl"], file["cnLakeRoot"])

	// ③ 再切回数据湖，同样要落盘
	if _, err := svc.SetDataSource(config.KindCnLake, lakeRoot); err != nil {
		t.Fatalf("再切数据湖失败：%v", err)
	}
	file = readFile()
	if got := file["dataSourceKind"]; got != config.KindCnLake {
		t.Errorf("再切湖后文件里 kind 应为 cnlake，实际 %v", got)
	}
	// ④ 再切回 stockdb（往返两轮）
	if _, err := svc.SetDataSource(config.KindStockDB, localURL); err != nil {
		t.Skipf("本机 stockdb 不可用，跳过（%v）", err)
	}
	file = readFile()
	if got := file["dataSourceKind"]; got != config.KindStockDB {
		t.Errorf("第二轮切回后文件里 kind 应为 stockdb，实际 %v", got)
	}
	t.Logf("③ 往返两轮后：kind=%v url=%v", file["dataSourceKind"], file["dataSourceUrl"])
}

// 切到数据湖之后，**`cnLakeRoot` 不应阻止切回 stockdb** ——
// 两者是独立字段：kind/url 决定当前数据源，cnLakeRoot 只记湖的位置
// （财报等数据始终从湖读，与行情源无关）。
func TestLakeRootDoesNotBlockSwitchBack(t *testing.T) {
	lakeRoot := t.TempDir()
	if err := cnlaketest.Build(lakeRoot); err != nil {
		t.Fatalf("构造数据湖失败：%v", err)
	}
	cfg := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewStockService(cfg)

	if _, err := svc.SetDataSource(config.KindCnLake, lakeRoot); err != nil {
		t.Fatalf("切到数据湖失败：%v", err)
	}
	const localURL = "http://127.0.0.1:7899"
	if _, err := svc.SetDataSource(config.KindStockDB, localURL); err != nil {
		t.Skipf("本机 stockdb 不可用，跳过（%v）", err)
	}

	if got := cfg.DataSourceKind(); got != config.KindStockDB {
		t.Errorf("内存里的 kind 应为 stockdb，实际 %s", got)
	}
	if got := cfg.DataSourceURL(); got != localURL {
		t.Errorf("内存里的 url 应为 %s，实际 %s", localURL, got)
	}
	// 湖根目录仍然保留（财报依赖它），但行情源已经是 stockdb
	if got := cfg.CnLakeRoot(); got != lakeRoot {
		t.Errorf("cnLakeRoot 应保留 %s（财报仍要读湖），实际 %s", lakeRoot, got)
	}
	provider, _ := svc.snapshot()
	if provider == nil || provider.Kind() != SourceKindStockDB {
		t.Errorf("行情源应为 stockdb，实际 %s", provider.Kind())
	}
	t.Logf("切回后：kind=%s url=%s provider=%s cnLakeRoot 保留=%v",
		cfg.DataSourceKind(), cfg.DataSourceURL(), provider.Kind(), cfg.CnLakeRoot() != "")
}
