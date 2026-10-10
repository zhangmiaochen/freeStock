//go:build windows

package cne

import (
	"os"
	"strings"
	"testing"
)

// ⚠⚠ **回归测试：子进程的工作目录绝不能设成 System32。**
//
// 实测（打印原始字节）：
//
//	Dir = C:\Windows\System32  -> schtasks 输出**中文 GBK**
//	                              "\xceļ\xfe\xbc\xd0: \\"（"文件夹:" 的 GBK 字节）
//	Dir = 其它有效目录           -> schtasks 输出**英文**
//	                              "Folder: \\" / "TaskName: ..." / "Status: Ready"
//
// 而 decodeConsole 对非 UTF-8 的处理是"剥掉非法字节"（纯 Go 标准库没有
// GBK 解码器），GBK 中文被剥光后字段全空 —— 解析失败、界面一片空白。
//
// 所以 fixed Dir 必须避开 System32。这条只有一位信息量，但踩了就全坏。
func TestWorkingDirIsNotSystem32(t *testing.T) {
	dir := neutralDir()
	lower := strings.ToLower(dir)
	if strings.Contains(lower, "system32") {
		t.Errorf("**工作目录不能是 System32（实际 %q）** —— 会让 schtasks 输出中文 GBK，"+
			"而解码器会把 GBK 剥成空", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("工作目录必须存在：%q（err=%v）", dir, err)
	}
	t.Logf("neutralDir = %s（避开 System32 ✓）", dir)
}

// ⚠ **回归测试**：用户点「开启自动更新」时得到
//
//	/enable cne-daily-update 失败：exit status 1
//	（ERROR: The system cannot find the path specified.）
//
// 而**同一条命令在 PowerShell 里跑是 exit 0** —— 问题不在命令，在进程环境：
// `cmd.exe` 对**当前工作目录**敏感，GUI 进程继承来的 CWD 可能是无效的，
// 于是 cmd 直接报"找不到路径"，schtasks 根本没被执行。
//
// 修法是两条：① 不再为了 chcp 去套 cmd /c；② 所有子进程显式设 Dir。
// 下面两条断言分别钉住它们。
func TestHiddenCommandSetsExistingWorkingDir(t *testing.T) {
	cmd := hiddenCommand("schtasks", "/query")
	if cmd.Dir == "" {
		t.Fatal("**没有设置 Dir** —— 会继承 GUI 进程那个不可控的当前目录，" +
			"cmd 类程序会直接报「找不到路径」")
	}
	info, err := os.Stat(cmd.Dir)
	if err != nil || !info.IsDir() {
		t.Errorf("Dir 必须是一个存在的目录，实际 %q（err=%v）", cmd.Dir, err)
	}
	t.Logf("子进程工作目录 = %s", cmd.Dir)
}

// runSchtasks 必须真的能跑通并解析出字段。
//
// 这次报错（"找不到路径"）的根因是 **CWD 无效**，不是 cmd 本身 ——
// `chcp 65001` 是有用的，所以仍然走 `cmd /c`；修的是 Dir。
func TestRunSchtasksWorks(t *testing.T) {
	out, err := runSchtasks("/query", "/tn", TaskDaily, "/fo", "LIST")
	if err != nil {
		t.Fatalf("**runSchtasks 失败**：%v（%s）", err, decodeConsole(out))
	}
	text := decodeConsole(out)
	if fieldOf(text, "TaskName", "任务名") == "" {
		t.Errorf("没解析出 TaskName —— 输出可能是空的或格式变了：\n%s", text)
	}
	// 明确断言：不再出现 cmd 的报错
	if contains(text, "cannot find the path") || contains(text, "找不到路径") {
		t.Errorf("**仍然是「找不到路径」** —— 说明还在经过 cmd：\n%s", text)
	}
	if !contains(text, TaskDaily) {
		t.Errorf("输出里没有任务名 %s：\n%s", TaskDaily, text)
	}
	t.Logf("runSchtasks 正常，TaskName=%q", fieldOf(text, "TaskName", "任务名"))
}

// 经 runSchtasks 的查询仍能完整解析 —— 去掉 chcp 后不能把解析弄坏。
func TestQueryStillParsesAfterDroppingChcp(t *testing.T) {
	info, err := Query(TaskDaily)
	if err != nil {
		t.Fatalf("Query 失败：%v", err)
	}
	if !info.Registered {
		t.Skip("任务未注册，跳过")
	}
	if info.NextRun == "" {
		t.Error("**取不到下次运行时间** —— 去掉 chcp 后解析坏了？")
	}
	if info.ScheduledState == "" {
		t.Error("取不到 Scheduled Task State —— 自动更新开关的判断依赖它")
	}
	if info.Command == "" {
		t.Error("取不到 Task To Run")
	}
	t.Logf("%s：下次 %s，状态 %s，命令 %s",
		info.Name, info.NextRun, info.ScheduledState, info.Command)
}

// 启用状态的读取也不能因为去掉 chcp 而坏掉。
func TestTaskEnabledStillWorks(t *testing.T) {
	enabled, err := TaskEnabled(TaskDaily)
	if err != nil {
		t.Fatalf("TaskEnabled 失败：%v", err)
	}
	t.Logf("%s 当前 %v", TaskDaily, enabled)
}

// 所有命令类都拿到了有效的 Dir —— 不只是 schtasks。
func TestAllCommandsGetWorkingDir(t *testing.T) {
	for _, name := range []string{"schtasks", "taskkill", "powershell"} {
		cmd := hiddenCommand(name, "/?")
		if cmd.Dir == "" {
			t.Errorf("%s 没有设置 Dir", name)
			continue
		}
		if info, err := os.Stat(cmd.Dir); err != nil || !info.IsDir() {
			t.Errorf("%s 的 Dir 不存在：%q", name, cmd.Dir)
		}
	}
}

// 查不存在的任务必须返回"未注册"，而不是把 schtasks 的业务性非零退出
// 当成调用失败报出来。
func TestQueryMissingTaskStillWorks(t *testing.T) {
	info, err := Query("cne-definitely-not-registered-xyz")
	if err != nil {
		t.Fatalf("**查不存在的任务不该报错**（schtasks 返回非零是正常的业务结果）：%v", err)
	}
	if info.Registered {
		t.Error("不该报告已注册")
	}
}

// ⚠ **GBK 解码是这次修复的核心**，必须钉住。
//
// 中文 Windows 上 schtasks 按 GBK（OEM 936）输出，而且**连字段名都是中文**
// （`任务名:` / `下次运行时间:` / `计划任务状态:`），不解码就取不到任何值。
//
// 之前用 `cmd /c chcp 65001` 强制 UTF-8，引出了 cmd 对工作目录敏感那一堆问题；
// 现在改成正确解码 GBK（用依赖里本来就有的 golang.org/x/text）。
func TestDecodeConsoleHandlesGBK(t *testing.T) {
	// "主机名:       ABC" 的 GBK 编码
	gbk := []byte{
		0xd6, 0xf7, 0xbb, 0xfa, 0xc3, 0xfb, 0xa3, 0xba, // 主机名：
		0x20, 0x20, 0x41, 0x42, 0x43, // "  ABC"
	}
	got := decodeConsole(gbk)
	if !strings.Contains(got, "主机名") {
		t.Errorf("**GBK 没解码成功**：%q —— 字段名是中文，不解码就取不到值", got)
	}
	if !strings.Contains(got, "ABC") {
		t.Errorf("ASCII 部分丢了：%q", got)
	}

	// UTF-8 输入必须原样返回（英文系统、或已经转过码的情况）
	utf8Input := []byte("Folder: \\\r\nTaskName: \\cne-daily-update\r\n")
	if got := decodeConsole(utf8Input); got != string(utf8Input) {
		t.Errorf("UTF-8 输入被改动了：%q", got)
	}

	// BOM 要去掉
	if got := decodeConsole([]byte("\ufeffFolder: \\")); strings.HasPrefix(got, "\ufeff") {
		t.Errorf("BOM 没去掉：%q", got)
	}
}

// **直连 schtasks（不经 cmd）必须能读回中文字段** —— 这是这次修复的落点。
func TestSchtasksDirectReadsChineseFields(t *testing.T) {
	out, err := runSchtasks("/query", "/tn", TaskDaily, "/fo", "LIST", "/v")
	if err != nil {
		// 任务不存在等情况不算失败
		t.Skipf("查询失败（可能任务未注册）：%v", err)
	}
	text := decodeConsole(out)
	// 中英文字段名至少有一个能用
	nextRun := fieldOf(text, "Next Run Time", "下次运行时间")
	state := fieldOf(text, "Scheduled Task State", "计划任务状态")
	t.Logf("下次运行时间=%q  计划任务状态=%q", nextRun, state)
	if nextRun == "" && state == "" {
		t.Errorf("**中英文字段名都没取到** —— GBK 解码或字段匹配有问题：\n%s", text)
	}
	if nextRun != "" && !contains(nextRun, ":") {
		t.Errorf("下次运行时间不像时间：%q", nextRun)
	}
}
