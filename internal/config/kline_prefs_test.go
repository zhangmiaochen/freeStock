package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// K 线页的显示开关都要能落盘、能读回。
//
// 这一条把「新增一个开关」的**契约**钉住：字段名（json tag）是前后端之间的约定，
// 改了名字界面就会静默读不到值 —— 那种故障不会报错，只会让设置"每次都要重开"。
func TestKlineDisplayTogglesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStoreAt(path)

	prefs := DefaultKlinePrefs()
	prefs.ShowBoll = true
	prefs.ShowFibonacci = true
	prefs.ShowRangeSelect = true
	prefs.ShowFlow = false
	store.SetKlinePrefs(prefs)

	got, saved := NewStoreAt(path).KlinePrefs()
	if !saved {
		t.Fatal("重新打开后应当报告已保存")
	}
	if !got.ShowFibonacci {
		t.Error("斐波那契开关未持久化")
	}
	if !got.ShowRangeSelect {
		t.Error("框选统计开关未持久化")
	}
	if !got.ShowBoll {
		t.Error("布林线开关未持久化")
	}
	if got.ShowFlow {
		t.Error("资金流开关未持久化（关着的值被写成了开）")
	}
}

// 两个"区间读法"开关是**互相独立**的：可以只开一个、也可以同时开。
func TestKlineRangeTogglesAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStoreAt(path)

	// 只开框选统计
	only := DefaultKlinePrefs()
	only.ShowRangeSelect = true
	only.ShowFibonacci = false
	store.SetKlinePrefs(only)

	got, _ := store.KlinePrefs()
	if !got.ShowRangeSelect || got.ShowFibonacci {
		t.Errorf("只开框选时读回 = range=%v fib=%v", got.ShowRangeSelect, got.ShowFibonacci)
	}

	// 两个都开
	both := DefaultKlinePrefs()
	both.ShowRangeSelect = true
	both.ShowFibonacci = true
	store.SetKlinePrefs(both)

	got, _ = store.KlinePrefs()
	if !got.ShowRangeSelect || !got.ShowFibonacci {
		t.Errorf("两个都开时应当都读回 true，实际 range=%v fib=%v",
			got.ShowRangeSelect, got.ShowFibonacci)
	}
}

// 框选统计默认**关着**：开启后左键拖拽从"平移"变成"框选"，
// 改变默认手感必须由用户显式打开。
func TestShowRangeSelectDefaultsOff(t *testing.T) {
	if DefaultKlinePrefs().ShowRangeSelect {
		t.Error("框选统计默认必须是关闭的")
	}
	// 老配置文件里没有这个字段 → 零值 false，行为与"默认关"一致
	var legacy KlinePrefs
	if legacy.Sanitize().ShowRangeSelect {
		t.Error("缺少字段的旧配置不该被当成已开启")
	}
}

// Sanitize 不能动这两个开关（它们只是布尔位，没有"非法值"）。
func TestSanitizeKeepsRangeToggles(t *testing.T) {
	got := KlinePrefs{
		Period:          "nonsense", // 会被纠正
		ShowRangeSelect: true,
		ShowFibonacci:   true,
	}.Sanitize()
	if !got.ShowRangeSelect || !got.ShowFibonacci {
		t.Error("Sanitize 不该把合法的显示开关改成 false")
	}
}

// json tag 是前后端契约：字段名写错界面会静默读不到。
func TestKlinePrefsJSONFieldNames(t *testing.T) {
	raw, err := json.Marshal(DefaultKlinePrefs())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"showFibonacci"`, `"showRangeSelect"`, `"showBoll"`, `"showMacd"`,
		`"showVolume"`, `"showFlow"`, `"maConfigs"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("KlinePrefs 序列化结果里缺少字段 %s", want)
		}
	}
}
