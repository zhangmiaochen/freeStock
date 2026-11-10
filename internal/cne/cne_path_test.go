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
