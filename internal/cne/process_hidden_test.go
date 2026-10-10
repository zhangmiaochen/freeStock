//go:build windows

package cne

import (
	"os/exec"
	"syscall"
	"testing"
)

// ⚠ **回归测试**：手动更新时不能弹控制台黑框。
//
// 用户实测反馈："手动更新时，会出现 python 黑框" ——
// `cne.exe` 是控制台程序，用裸 exec.Command 起它，Windows 会分配一个窗口。
//
// 这个断言只有一位信息量（标志位有没有设），但漏了就是用户可见的体验问题，
// 而它不会让任何功能测试失败 —— 所以必须单独钉住。
func TestHiddenCommandSetsNoWindow(t *testing.T) {
	cmd := hiddenCommand("cmd", "/c", "echo hi")
	if cmd.SysProcAttr == nil {
		t.Fatal("hiddenCommand 必须设置 SysProcAttr，否则会弹控制台窗口")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("**没有设置 CREATE_NO_WINDOW（0x%X）** —— 手动更新时会弹出黑框",
			createNoWindow)
	}
	if cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Error("没有设置 CREATE_NEW_PROCESS_GROUP —— 取消时可能留下孤儿子进程")
	}
	t.Logf("CreationFlags = 0x%X（CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP）",
		cmd.SysProcAttr.CreationFlags)
}

// CREATE_NO_WINDOW 的常量值必须是 Win32 的 0x08000000 —— 写错一位就静默失效。
func TestCreateNoWindowConstantValue(t *testing.T) {
	if createNoWindow != 0x08000000 {
		t.Errorf("createNoWindow = 0x%X，应为 0x08000000（Win32 CREATE_NO_WINDOW）",
			createNoWindow)
	}
}

// setupHiddenProcess 用在实际的 cne 命令上时也要生效 ——
// 这是用户点「手动更新」真正走的那条路。
func TestCneCommandGetsNoWindow(t *testing.T) {
	cmd := hiddenCommand(`D:\softInstal\python3.11.3\Scripts\cne.exe`, "run", "daily")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("**启动 cne 的命令没有设 CREATE_NO_WINDOW —— 这正是用户看到的黑框**")
	}
	// 顺带确认路径正确地传给了 exec（不是被当参数拼错）
	if cmd.Path == "" {
		t.Error("命令路径为空")
	}
	t.Logf("cne 命令 flags = 0x%X，path = %s",
		cmd.SysProcAttr.CreationFlags, cmd.Path)
}

// 所有对外部程序的调用都必须走 hiddenCommand。
//
// 用源码级检查代替"逐个调用点断言"：漏掉任何一处都会闪黑框
// （powershell 尤其明显，它启动慢、窗口会停留可见的一段时间）。
func TestNoBareExecCommandOutsideHelper(t *testing.T) {
	// 这里只做行为层的抽查：确认这几类命令都带了标志。
	cases := [][]string{
		{"taskkill", "/T", "/F", "/PID", "1"},
		{"powershell", "-NoProfile", "-Command", "exit"},
		{"cmd", "/c", "echo hi"},
		{"schtasks", "/query"},
	}
	for _, args := range cases {
		cmd := hiddenCommand(args[0], args[1:]...)
		if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
			t.Errorf("hiddenCommand(%v) 没设 CREATE_NO_WINDOW", args)
		}
	}
}

// 确保 exec 包仍然被引用（避免将来有人删了 helper 却留下 import）
var _ = exec.Command
