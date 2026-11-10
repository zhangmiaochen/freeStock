package cne

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 传统布局：<工程>/configs/cnequity.toml（Windows 上一直是这个）
func TestConfigPathConfigsLayout(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "configs", "cnequity.toml")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatalf("应当找到配置：%v", err)
	}
	if got != want {
		t.Errorf("应为 %q，实际 %q", want, got)
	}
}

// Linux 布局：配置**直接**在目录里，没有 configs/ 这一层。
// 用户把「工程目录」指到 ~/.config/cnequity 时就是这种形态。
func TestConfigPathFlatLayout(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "cnequity.toml")
	if err := os.WriteFile(want, []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatalf("平铺布局也应当能找到配置：%v", err)
	}
	if got != want {
		t.Errorf("应为 %q，实际 %q", want, got)
	}
}

// 两种都在时优先 configs/（保持 Windows 行为不变）
func TestConfigPathPrefersConfigsDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "configs", "cnequity.toml")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("# nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cnequity.toml"), []byte("# flat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != nested {
		t.Errorf("应优先 configs/ 下的那份，实际 %q", got)
	}
}

// 都没找到时，错误信息要把两条候选路径都列出来 ——
// 否则用户不知道该把 toml 放哪，容器里更难判断是"没挂上"还是"位置不对"。
func TestConfigPathMissingListsCandidates(t *testing.T) {
	dir := t.TempDir()

	_, err := ConfigPath(dir)
	if err == nil {
		t.Fatal("空目录应当报错")
	}
	msg := err.Error()
	for _, rel := range []string{
		filepath.Join("configs", "cnequity.toml"),
		"cnequity.toml",
	} {
		if !strings.Contains(msg, rel) {
			t.Errorf("错误信息应包含候选 %q：%v", rel, msg)
		}
	}
}

// 工程目录为空要直接说清楚，不能拼出个 ./cnequity.toml 去误导。
func TestConfigPathEmptyDir(t *testing.T) {
	if _, err := ConfigPath("   "); err == nil {
		t.Fatal("空目录应当报错")
	}
}
