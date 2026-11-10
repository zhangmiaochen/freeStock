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

// Linux 上 cne 的真实形态：~/.config/cnequity/config.toml（平铺、且文件名叫 config.toml）
func TestConfigPathLinuxDefaultName(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(want, []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatalf("config.toml 应当被识别：%v", err)
	}
	if got != want {
		t.Errorf("应为 %q，实际 %q", want, got)
	}
}

// 即使目录里还有别的 toml，也要按已知名字确定性地选 config.toml，
// 而不是靠"唯一 toml"那条兜底去碰运气。
func TestConfigPathPrefersKnownNameOverAmbiguity(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "config.toml")
	for _, n := range []string{"config.toml", "themes.toml"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# t\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatalf("有已知名字时不该报歧义：%v", err)
	}
	if got != want {
		t.Errorf("应确定性地选中 %q，实际 %q", want, got)
	}
}

// 都没找到时，错误信息要把候选路径都列出来 ——
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
		"config.toml",
	} {
		if !strings.Contains(msg, rel) {
			t.Errorf("错误信息应包含候选 %q：%v", rel, msg)
		}
	}
	if !strings.Contains(msg, "空目录") {
		t.Errorf("空目录时要明说，别让人以为是挂载问题：%v", msg)
	}
}

// 目录里只有一个 *.toml（名字不叫 cnequity.toml）时也要认 ——
// cne 不同安装方式/版本的文件名并不统一，硬套名字会让人无从下手。
func TestConfigPathSingleArbitraryToml(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(want, []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ConfigPath(dir)
	if err != nil {
		t.Fatalf("目录里唯一的 toml 应当被采用：%v", err)
	}
	if got != want {
		t.Errorf("应为 %q，实际 %q", want, got)
	}
}

// 有多个 toml 时**不能猜**（可能改错配置），要列出来让人选。
func TestConfigPathMultipleTomlsIsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.toml", "b.toml"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# t\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := ConfigPath(dir); err == nil {
		t.Fatal("多个 toml 时应当报错，不能随便挑一个")
	} else if !strings.Contains(err.Error(), "多个 toml") {
		t.Errorf("错误信息应说明「多个 toml」：%v", err)
	}
}

// 目录不存在时，报"读不了"，而不是伪装成"找不到配置"。
func TestConfigPathDirMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	_, err := ConfigPath(missing)
	if err == nil {
		t.Fatal("目录不存在应当报错")
	}
	if !strings.Contains(err.Error(), "读不了") {
		t.Errorf("应说明目录读不了：%v", err)
	}
}

// 工程目录为空要直接说清楚，不能拼出个 ./cnequity.toml 去误导。
func TestConfigPathEmptyDir(t *testing.T) {
	if _, err := ConfigPath("   "); err == nil {
		t.Fatal("空目录应当报错")
	}
}
