package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	return NewStoreAt(path), path
}

func readRaw(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("配置文件不是合法 JSON: %v\n%s", err, raw)
	}
	return parsed
}

// 数据源设置必须真的落到文件里，重启后还能读回来。
func TestDataSourcePersistsToFile(t *testing.T) {
	store, path := tempStore(t)
	store.SetDataSource("http://192.168.1.10:7899", KindStockDB)

	raw := readRaw(t, path)
	if raw["dataSourceUrl"] != "http://192.168.1.10:7899" {
		t.Errorf("dataSourceUrl 未写入: %v", raw["dataSourceUrl"])
	}
	if raw["dataSourceKind"] != KindStockDB {
		t.Errorf("dataSourceKind 未写入: %v", raw["dataSourceKind"])
	}

	// 重新打开，模拟重启
	reopened := NewStoreAt(path)
	if got := reopened.DataSourceURL(); got != "http://192.168.1.10:7899" {
		t.Errorf("重启后地址丢失: %q", got)
	}
	if got := reopened.DataSourceKind(); got != KindStockDB {
		t.Errorf("重启后类型丢失: %q", got)
	}
}

func TestDataSourceKindDefaultsToStockDB(t *testing.T) {
	store, _ := tempStore(t)
	// 空文件：老版本配置里没有 kind 字段
	if got := store.DataSourceKind(); got != KindStockDB {
		t.Errorf("缺失时应回退到 %s，实际 %q", KindStockDB, got)
	}
}

func TestCnLakeRootPersists(t *testing.T) {
	store, path := tempStore(t)
	store.SetCnLakeRoot(`D:\workspace\stockTool\CNEquity\data\cnequity`)

	raw := readRaw(t, path)
	if raw["cnLakeRoot"] != `D:\workspace\stockTool\CNEquity\data\cnequity` {
		t.Errorf("cnLakeRoot 未写入: %v", raw["cnLakeRoot"])
	}

	reopened := NewStoreAt(path)
	if got := reopened.CnLakeRoot(); got != `D:\workspace\stockTool\CNEquity\data\cnequity` {
		t.Errorf("重启后丢失: %q", got)
	}
}

// 切换数据源不能把已经存好的其它设置冲掉。
func TestDataSourceAndLakeRootCoexist(t *testing.T) {
	store, path := tempStore(t)

	store.SetCnLakeRoot(`D:\lake`)
	store.SetDataSource("", KindCnLake)

	reopened := NewStoreAt(path)
	if got := reopened.CnLakeRoot(); got != `D:\lake` {
		t.Errorf("切换数据源把数据湖路径冲掉了: %q", got)
	}
	if got := reopened.DataSourceKind(); got != KindCnLake {
		t.Errorf("数据源类型丢失: %q", got)
	}
	if got := reopened.DataSourceURL(); got != "" {
		t.Errorf("数据湖的 URL 应为空（自动探测），实际 %q", got)
	}
}

// K 线指标设置要写进同一个配置文件，而不是浏览器存储。
func TestKlinePrefsPersistToFile(t *testing.T) {
	store, path := tempStore(t)

	// 首次读取：没存过
	if _, saved := store.KlinePrefs(); saved {
		t.Error("首次读取不该报告已保存")
	}

	prefs := DefaultKlinePrefs()
	prefs.Period = "week"
	prefs.Adjust = "hfq"
	prefs.ShowBoll = true
	prefs.ShowFlow = false
	prefs.Boll = BollPrefs{Period: 60, Mult: 3}
	prefs.Macd = MacdPrefs{Fast: 8, Slow: 17, Signal: 5}
	prefs.MaConfigs = []MaPrefs{
		{Period: 10, Color: "#f5c542", Visible: true},
		{Period: 30, Color: "#ec4899", Visible: false},
	}
	store.SetKlinePrefs(prefs)

	// 文件里要有 klinePrefs 段
	raw := readRaw(t, path)
	if _, ok := raw["klinePrefs"]; !ok {
		t.Fatal("配置文件里没有 klinePrefs 段")
	}

	// 重启后逐项核对
	reopened := NewStoreAt(path)
	got, saved := reopened.KlinePrefs()
	if !saved {
		t.Fatal("重启后应报告已保存")
	}
	if got.Period != "week" || got.Adjust != "hfq" {
		t.Errorf("周期/复权丢失: %q %q", got.Period, got.Adjust)
	}
	if !got.ShowBoll || got.ShowFlow {
		t.Errorf("显示开关丢失: boll=%v flow=%v", got.ShowBoll, got.ShowFlow)
	}
	if got.Boll.Period != 60 || got.Boll.Mult != 3 {
		t.Errorf("布林参数丢失: %+v", got.Boll)
	}
	if got.Macd.Fast != 8 || got.Macd.Slow != 17 || got.Macd.Signal != 5 {
		t.Errorf("MACD 参数丢失: %+v", got.Macd)
	}
	if len(got.MaConfigs) != 2 {
		t.Fatalf("均线配置丢失: %+v", got.MaConfigs)
	}
	if got.MaConfigs[0].Period != 10 || got.MaConfigs[0].Visible != true {
		t.Errorf("第一条均线错误: %+v", got.MaConfigs[0])
	}
	if got.MaConfigs[1].Period != 30 || got.MaConfigs[1].Visible != false {
		t.Errorf("第二条均线错误: %+v", got.MaConfigs[1])
	}
}

// 保存 K 线设置不能把数据源设置冲掉（它们共用一个文件）。
func TestKlinePrefsDoNotClobberDataSource(t *testing.T) {
	store, path := tempStore(t)

	store.SetDataSource("http://127.0.0.1:7899", KindStockDB)
	store.SetCnLakeRoot(`D:\lake`)

	prefs := DefaultKlinePrefs()
	prefs.Boll.Period = 34
	store.SetKlinePrefs(prefs)

	reopened := NewStoreAt(path)
	if got := reopened.DataSourceURL(); got != "http://127.0.0.1:7899" {
		t.Errorf("数据源地址被冲掉: %q", got)
	}
	if got := reopened.CnLakeRoot(); got != `D:\lake` {
		t.Errorf("数据湖路径被冲掉: %q", got)
	}
	if got, _ := reopened.KlinePrefs(); got.Boll.Period != 34 {
		t.Errorf("K 线设置没存上: %+v", got.Boll)
	}
}

func TestKlinePrefsDefaults(t *testing.T) {
	def := DefaultKlinePrefs()
	if def.Period != "day" || def.Adjust != "qfq" {
		t.Errorf("默认应为日线 + 前复权: %q %q", def.Period, def.Adjust)
	}
	if len(def.MaConfigs) != 5 {
		t.Fatalf("默认应有 5 条均线，实际 %d", len(def.MaConfigs))
	}
	want := []int{5, 20, 60, 120, 250}
	for i, ma := range def.MaConfigs {
		if ma.Period != want[i] {
			t.Errorf("第 %d 条均线周期应为 %d，实际 %d", i, want[i], ma.Period)
		}
		if !ma.Visible {
			t.Errorf("第 %d 条均线默认应可见", i)
		}
	}
	if def.Boll.Period != 20 || def.Boll.Mult != 2 {
		t.Errorf("布林默认参数错误: %+v", def.Boll)
	}
	if def.Macd.Fast != 12 || def.Macd.Slow != 26 || def.Macd.Signal != 9 {
		t.Errorf("MACD 默认参数错误: %+v", def.Macd)
	}
}

// 手工改坏配置文件不该让界面崩掉。
func TestKlinePrefsSanitize(t *testing.T) {
	bad := KlinePrefs{
		Period: "hourly",   // 非法周期
		Adjust: "whatever", // 非法复权
		Boll:   BollPrefs{Period: 0, Mult: 999},
		Macd:   MacdPrefs{Fast: 30, Slow: 10, Signal: 0},
		MaConfigs: []MaPrefs{
			{Period: 0, Color: "red", Visible: true},
		},
	}
	got := bad.Sanitize()

	if got.Period != "day" {
		t.Errorf("非法周期应回退到 day，实际 %q", got.Period)
	}
	if got.Adjust != "qfq" {
		t.Errorf("非法复权应回退到 qfq，实际 %q", got.Adjust)
	}
	if got.Boll.Period < 2 || got.Boll.Mult > 10 {
		t.Errorf("布林参数未修正: %+v", got.Boll)
	}
	// 慢线必须大于快线
	if got.Macd.Slow <= got.Macd.Fast {
		t.Errorf("慢线应大于快线: %+v", got.Macd)
	}
	if got.Macd.Signal < 1 {
		t.Errorf("信号线应至少为 1: %+v", got.Macd)
	}
	// 颜色必须是 #RRGGBB
	if len(got.MaConfigs[0].Color) != 7 || got.MaConfigs[0].Color[0] != '#' {
		t.Errorf("非法颜色未修正: %q", got.MaConfigs[0].Color)
	}
	if got.MaConfigs[0].Period < 1 {
		t.Errorf("非法均线周期未修正: %d", got.MaConfigs[0].Period)
	}
}

func TestKlinePrefsSanitizeFromFile(t *testing.T) {
	// 直接写一份被改坏的配置文件
	path := filepath.Join(t.TempDir(), "config.json")
	broken := `{"klinePrefs":{"period":"bad","adjust":"bad","boll":{"period":-5,"mult":100},"macd":{"fast":50,"slow":10,"signal":0},"maConfigs":[]}}`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStoreAt(path)
	got, saved := store.KlinePrefs()
	if !saved {
		t.Fatal("应识别为已保存")
	}
	if got.Period != "day" || got.Adjust != "qfq" {
		t.Errorf("坏值未回退: %+v", got)
	}
	if got.Macd.Slow <= got.Macd.Fast {
		t.Errorf("MACD 未修正: %+v", got.Macd)
	}
	if len(got.MaConfigs) == 0 {
		t.Error("空均线配置应回落到默认 5 条")
	}
}

func TestWriteCreatesDirectory(t *testing.T) {
	// 配置目录不存在时应自动创建
	path := filepath.Join(t.TempDir(), "nested", "deep", "config.json")
	store := NewStoreAt(path)
	store.SetDataSource("http://127.0.0.1:7899", KindStockDB)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置文件未创建: %v", err)
	}
}

func TestReadOnlyPathDoesNotPanic(t *testing.T) {
	// 不可写路径：内存里仍应生效，不 panic
	store := NewStoreAt(`Z:\definitely\not\here\config.json`)
	store.SetDataSource("http://127.0.0.1:7899", KindStockDB)
	if got := store.DataSourceURL(); got != "http://127.0.0.1:7899" {
		t.Errorf("内存中应生效: %q", got)
	}
	store.SetKlinePrefs(DefaultKlinePrefs())
	if _, saved := store.KlinePrefs(); !saved {
		t.Error("内存中应报告已保存")
	}
}

// ---------------------------------------------------------------------------
// 配置文件位置：优先程序目录（绿色软件），不可写时退回用户配置目录
// ---------------------------------------------------------------------------

func TestResolvePathPrefersExecutableDir(t *testing.T) {
	dir := t.TempDir()
	got := ResolvePath(dir)

	want := filepath.Join(dir, "config.json")
	if got != want {
		t.Errorf("应使用程序目录: %q，期望 %q", got, want)
	}
}

func TestResolvePathSkipsUnwritableDir(t *testing.T) {
	// 用一个不可能创建的路径模拟只读的安装目录
	got := ResolvePath(`Z:\definitely\not\here`)
	if strings.HasPrefix(got, `Z:`) {
		t.Errorf("不可写的目录不该被选用: %q", got)
	}
	if legacy := legacyConfigPath(); legacy != "" && got != legacy {
		t.Errorf("应退回 %q，实际 %q", legacy, got)
	}
}

func TestResolvePathWithoutExecutableDir(t *testing.T) {
	got := ResolvePath("")
	if legacy := legacyConfigPath(); legacy != "" && got != legacy {
		t.Errorf("应退回 %q，实际 %q", legacy, got)
	}
}

func TestResolvePathAlwaysReturnsSomething(t *testing.T) {
	// 任何情况下都不能返回空字符串，否则写配置会失败得莫名其妙
	if got := ResolvePath(t.TempDir()); got == "" {
		t.Error("不该返回空路径")
	}
	if got := ResolvePath(`Z:\nope`); got == "" {
		t.Error("不可写时也应返回一个路径（写入失败会被忽略）")
	}
}

// 从旧版本升级时 %AppData% 里的配置要自动搬到程序目录，
// 否则用户会看到「设置全丢了」。
func TestMigrateLegacyCopiesConfig(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	oldPath := filepath.Join(oldDir, "config.json")
	newPath := filepath.Join(newDir, "config.json")

	legacyContent := `{"dataSourceUrl":"http://10.0.0.5:7899","dataSourceKind":"stockdb","cnLakeRoot":"D:\\lake"}`
	if err := os.WriteFile(oldPath, []byte(legacyContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if !migrateLegacy(newPath, oldPath) {
		t.Fatal("应执行迁移")
	}

	raw, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("迁移后新配置不存在: %v", err)
	}
	if string(raw) != legacyContent {
		t.Errorf("迁移内容不一致:\n%s", raw)
	}
	// 旧文件保留作为备份
	if _, err := os.Stat(oldPath); err != nil {
		t.Error("旧配置应保留备份")
	}

	store := NewStoreAt(newPath)
	if got := store.DataSourceURL(); got != "http://10.0.0.5:7899" {
		t.Errorf("迁移后读不到设置: %q", got)
	}
	if got := store.CnLakeRoot(); got != `D:\lake` {
		t.Errorf("迁移后读不到数据湖路径: %q", got)
	}
}

func TestMigrateLegacyDoesNotOverwrite(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	oldPath := filepath.Join(oldDir, "config.json")
	newPath := filepath.Join(newDir, "config.json")

	os.WriteFile(oldPath, []byte(`{"dataSourceUrl":"http://old"}`), 0o644)
	os.WriteFile(newPath, []byte(`{"dataSourceUrl":"http://new"}`), 0o644)

	if migrateLegacy(newPath, oldPath) {
		t.Error("新位置已有配置时不该迁移")
	}
	raw, _ := os.ReadFile(newPath)
	if string(raw) != `{"dataSourceUrl":"http://new"}` {
		t.Errorf("新配置被覆盖了: %s", raw)
	}
}

func TestMigrateLegacyNoops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if migrateLegacy(path, path) {
		t.Error("路径相同时不该迁移")
	}
	if migrateLegacy("", filepath.Join(dir, "a.json")) {
		t.Error("空路径不该迁移")
	}
	if migrateLegacy(path, "") {
		t.Error("旧路径为空时不该迁移")
	}
	if migrateLegacy(path, filepath.Join(dir, "not-exist.json")) {
		t.Error("旧配置不存在时不该迁移")
	}
}

// 这次改动的核心诉求：程序目录可写时，配置就落在程序目录。
func TestConfigLandsNextToExecutable(t *testing.T) {
	exeDir := t.TempDir()
	path := ResolvePath(exeDir)
	if filepath.Dir(path) != exeDir {
		t.Fatalf("配置目录应为程序目录: %q", path)
	}

	store := NewStoreAt(path)
	store.SetDataSource("http://127.0.0.1:7899", KindStockDB)
	prefs := DefaultKlinePrefs()
	prefs.Period = "month"
	store.SetKlinePrefs(prefs)

	if _, err := os.Stat(filepath.Join(exeDir, "config.json")); err != nil {
		t.Fatalf("配置文件没有落在程序目录: %v", err)
	}
	reopened := NewStoreAt(path)
	if got, _ := reopened.KlinePrefs(); got.Period != "month" {
		t.Errorf("重启后读不回: %+v", got)
	}
}
