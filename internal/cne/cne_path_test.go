package cne

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 造一个可执行的假 cne（Unix 上需要执行位）。
func writeFakeCNE(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// 设置里填**目录**时必须能用 —— 这是容器/NAS 场景的主要填法：
// 宿主机上的 cne 目录挂进容器后，用户填的就是那个目录。
func TestResolveCNEDirectory(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, cneExeName())
	writeFakeCNE(t, exe)

	got, err := ResolveCNE(dir)
	if err != nil {
		t.Fatalf("填目录应当能解析出 cne：%v", err)
	}
	if got != exe {
		t.Errorf("应解析到 %q，实际 %q", exe, got)
	}
}

// 用户也可能直接填 venv 根目录（/opt/cne-venv），此时 cne 在 bin/ 下。
func TestResolveCNEDirectoryVenvLayout(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", cneExeName())
	writeFakeCNE(t, exe)

	got, err := ResolveCNE(root)
	if err != nil {
		t.Fatalf("venv 根目录应当能解析出 cne：%v", err)
	}
	if got != exe {
		t.Errorf("应解析到 %q，实际 %q", exe, got)
	}
}

// 直接指定可执行文件仍然要工作（老配置、Windows 习惯都靠它）。
func TestResolveCNEFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, cneExeName())
	writeFakeCNE(t, exe)

	got, err := ResolveCNE(exe)
	if err != nil {
		t.Fatalf("填可执行文件应当能用：%v", err)
	}
	if got != exe {
		t.Errorf("应原样返回 %q，实际 %q", exe, got)
	}
}

// 目录里没有 cne 时，错误信息要**指出找过哪些路径** ——
// 否则用户在 NAS 上只能猜"到底该把 cne 放哪"。
func TestResolveCNEDirectoryMissing(t *testing.T) {
	dir := t.TempDir()

	_, err := ResolveCNE(dir)
	if err == nil {
		t.Fatal("空目录应当报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, dir) {
		t.Errorf("错误信息应包含目录本身：%v", msg)
	}
	if !strings.Contains(msg, filepath.Join("bin", cneExeName())) {
		t.Errorf("错误信息应列出找过的候选路径：%v", msg)
	}
}

// 路径不存在时不能误报"在 PATH 上找不到"（那是给裸命令名用的提示）。
func TestResolveCNEMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here", "cne")

	_, err := ResolveCNE(missing)
	if err == nil {
		t.Fatal("不存在的路径应当报错")
	}
	if !strings.Contains(err.Error(), "不可用") {
		t.Errorf("错误信息应说明路径不可用：%v", err)
	}
}

// ⚠ 从宿主机挂进容器的目录经常丢掉执行位（0644）。
// 这时报"权限不足"而不是"找不到"，用户才知道该 chmod +x。
func TestResolveCNENotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 Unix 执行位概念")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, cneExeName())
	writeFakeCNE(t, exe)
	if err := os.Chmod(exe, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveCNE(dir)
	if err == nil {
		t.Fatal("没有执行位应当报错")
	}
	if !strings.Contains(err.Error(), "chmod") {
		t.Errorf("错误信息应提示 chmod：%v", err)
	}
}

// 裸命令名（不含分隔符）仍按 PATH 解析 —— 不能让老配置失效
func TestResolveCNEBareNameFallsBackToPATH(t *testing.T) {
	if _, err := ResolveCNE("definitely-not-a-real-command-xyz"); err == nil {
		t.Fatal("PATH 上不存在的裸命令名应当报错")
	}
}
func TestFindCNECandidatesIncludeFnOSMount(t *testing.T) {
	want := "/vol2/1001/docker/cne-venv/bin/cne"
	for _, candidate := range defaultCNECandidates() {
		if candidate == want {
			return
		}
	}
	t.Fatalf("默认候选路径未包含 compose 中的 fnOS mount 目标 %q", want)
}

// ⚠ 这条是用户实报：Windows 桌面版上更新数据报
// "当前运行环境是容器/NAS；宿主机安装路径对容器不可见" —— 在 Windows 上这句话
// 完全误导，会让人以为程序把系统认错了。找不到 cne 的提示必须按平台说人话。
func TestCNENotFoundMessageMatchesPlatform(t *testing.T) {
	msg := cneNotFoundError([]string{`C:\somewhere\cne.exe`}).Error()
	if inContainer() {
		if !strings.Contains(msg, "容器") {
			t.Errorf("容器里应提示挂载：%v", msg)
		}
		return
	}
	if strings.Contains(msg, "容器") || strings.Contains(msg, "宿主机") {
		t.Errorf("非容器环境不应再提容器/宿主机：%v", msg)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(msg, "pip install cne") {
			t.Errorf("Windows 上应提示 pip install cne：%v", msg)
		}
		if !strings.Contains(msg, "Scripts") {
			t.Errorf("Windows 上应提示 cne.exe 在 Python 的 Scripts 目录：%v", msg)
		}
	}
	if !strings.Contains(msg, `C:\somewhere\cne.exe`) {
		t.Errorf("错误信息应列出找过的路径：%v", msg)
	}
}

// Windows 上未配置时要扫 PATH 里的 cne.exe（pip 的 Scripts 目录常常不在 PATH，
// 但一旦在，就不该让用户再手填）。
func TestWindowsCNECandidatesUsePATH(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上验证 PATH 扫描")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	found := false
	for _, cand := range windowsCNECandidates() {
		if cand == filepath.Join(dir, "cne.exe") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("候选里应包含 %q，实际 %v", filepath.Join(dir, "cne.exe"), windowsCNECandidates())
	}
}

// 容器判定在 Windows 上必须是 false（否则又会给出挂载提示）。
func TestInContainerFalseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上验证")
	}
	if inContainer() {
		t.Fatal("Windows 不该被判成容器")
	}
}

// ⚠ 用户实报的另一半：pip 装 CNEquity 后可执行文件叫 **cnequity**，
// 不是 cne。以前只找 cne/cne.exe，于是"明明装了却报找不到"。
// 这里钉住两种名字都能解析到。
func TestResolveCNEFindsCnequityName(t *testing.T) {
	name := "cnequity"
	if runtime.GOOS == "windows" {
		name = "cnequity.exe"
	}
	t.Run("目录", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, name)
		writeFakeCNE(t, exe)
		got, err := ResolveCNE(dir)
		if err != nil {
			t.Fatalf("目录里只有 %s 时也应解析成功：%v", name, err)
		}
		if got != exe {
			t.Errorf("应解析到 %q，实际 %q", exe, got)
		}
	})
	t.Run("venv布局", func(t *testing.T) {
		root := t.TempDir()
		exe := filepath.Join(root, "bin", name)
		writeFakeCNE(t, exe)
		got, err := ResolveCNE(root)
		if err != nil {
			t.Fatalf("venv 根目录下 bin/%s 也应解析成功：%v", name, err)
		}
		if got != exe {
			t.Errorf("应解析到 %q，实际 %q", exe, got)
		}
	})
	t.Run("PATH", func(t *testing.T) {
		dir := t.TempDir()
		writeFakeCNE(t, filepath.Join(dir, name))
		t.Setenv("PATH", dir)
		got, err := ResolveCNE("cne")
		if err != nil {
			t.Fatalf("填 cne 但 PATH 里只有 %s 时也应回退找到：%v", name, err)
		}
		if filepath.Base(got) != name {
			t.Errorf("应解析到 %s，实际 %q", name, got)
		}
	})
}

// 未配置时按 PATH 找：只要 PATH 里有 cnequity 就该找到（这是 Windows 默认情况）。
func TestFindCNEOnPathFindsCnequity(t *testing.T) {
	name := "cnequity"
	if runtime.GOOS == "windows" {
		name = "cnequity.exe"
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, name)
	writeFakeCNE(t, exe)
	t.Setenv("PATH", dir)
	got, err := findCNEOnPath()
	if err != nil {
		t.Fatalf("PATH 里有 %s 时不应报找不到：%v", name, err)
	}
	if got != exe {
		t.Errorf("应解析到 %q，实际 %q", exe, got)
	}
}
