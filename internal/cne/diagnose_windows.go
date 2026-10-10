//go:build windows

package cne

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Diagnose 从**当前进程**里实测一组事实，用来定位
// "The system cannot find the path specified."（ERROR_PATH_NOT_FOUND）。
//
// ⚠ 这个函数的存在理由：这个错我修了三版都没修对，全都在猜。
// 而同一个调用在 `go test`（有控制台的进程）里永远成功 ——
// 说明差异在**应用进程自身**的环境，只有从应用里实测才看得到。
//
// 返回的是一段纯文本，用户可以直接贴出来。
func Diagnose() string {
	var b strings.Builder
	p := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	p("=== cne 计划任务诊断 ===")
	p("时间: %s", nowText())

	// ① 进程自身
	if wd, err := os.Getwd(); err != nil {
		p("[进程] 当前工作目录: 取不到 (%v)", err)
	} else {
		p("[进程] 当前工作目录: %s", wd)
		if info, err := os.Stat(wd); err != nil {
			p("       ⚠ **这个目录不存在**：%v", err)
		} else if !info.IsDir() {
			p("       ⚠ 这个路径不是目录")
		}
	}
	exe, _ := os.Executable()
	p("[进程] 可执行文件: %s", exe)
	p("[进程] 用户: %s", os.Getenv("USERNAME"))

	// ② 临时目录候选（neutralDir 用的就是这些）
	p("[环境] TEMP=%q TMP=%q USERPROFILE=%q SystemRoot=%q",
		os.Getenv("TEMP"), os.Getenv("TMP"), os.Getenv("USERPROFILE"), os.Getenv("SystemRoot"))
	p("[环境] os.TempDir()=%q", os.TempDir())
	for _, d := range []string{os.TempDir(), os.Getenv("TEMP"), os.Getenv("TMP"), os.Getenv("USERPROFILE"), `C:\`} {
		if strings.TrimSpace(d) == "" {
			continue
		}
		if info, err := os.Stat(d); err != nil {
			p("       ⚠ %q 不存在：%v", d, err)
		} else if !info.IsDir() {
			p("       ⚠ %q 不是目录", d)
		}
	}
	p("[环境] neutralDir() 实际返回: %q", neutralDir())

	// ③ PATH
	pathEnv := os.Getenv("PATH")
	p("[环境] PATH 长度 %d", len(pathEnv))
	for _, name := range []string{"schtasks", "cmd", "powershell"} {
		if pth, err := exec.LookPath(name); err != nil {
			p("       ⚠ %s 在 PATH 上找不到：%v", name, err)
		} else {
			p("       %s -> %s", name, pth)
		}
	}

	// ④ **核心：谁在报"找不到路径"** —— 逐项试，看哪一步开始失败
	p("")
	p("=== 实测（每一步都单独报结果）===")
	probe := func(label string, dir string, hide bool, name string, args ...string) {
		cmd := exec.Command(name, args...)
		if dir != "" {
			cmd.Dir = dir
		}
		if hide {
			setupHiddenProcess(cmd)
		}
		out, err := cmd.CombinedOutput()
		status := "成功"
		if err != nil {
			status = fmt.Sprintf("**失败**: %v", err)
		}
		body := strings.TrimSpace(decodeConsole(out))
		p("  %-46s %s", label, status)
		if body != "" {
			// ⚠ 输出**前 3 行**都要打出来 —— 只看首行会漏掉关键线索
			lines := strings.Split(body, "\n")
			if len(lines) > 3 {
				lines = lines[:3]
			}
			for _, ln := range lines {
				p("      | %s", strings.TrimSpace(ln))
			}
		}
	}

	// ⚠ **区分性探针**。
	//
	// 之前全部用了 `/tn <任务名>`，所以看不出失败是发生在
	// "程序根本跑不起来" 还是 "按名字找不到任务" —— 这两者的修法完全不同。
	//
	// 关键对照：**`sc query Schedule` 能成功**（同一个 System32、同样的调用方式），
	// 说明进程环境、PATH、Dir、标志、权限都不是原因，
	// 问题出在 schtasks 这个程序特有的行为上。
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	schtasksExe := filepath.Join(sys, "schtasks.exe")

	// ① 程序能不能跑：只打帮助，不碰任务
	probe("schtasks /?（只验证程序能否运行）", "", false, "schtasks", "/?")
	probe("schtasks.exe 全路径 /?", "", false, schtasksExe, "/?")
	// ② 列出全部任务（不按名字找）
	probe("schtasks /query（列出全部，不按名字）", "", false, "schtasks", "/query", "/fo", "LIST")
	// ③ 按名字查一个一定不存在的任务 —— 看它的报错是否不同
	probe("schtasks /query /tn 不存在的任务", "", false,
		"schtasks", "/query", "/tn", "cne-definitely-not-exist-xyz", "/fo", "LIST")
	// ④ 按名字查真实任务
	probe("schtasks /query /tn "+TaskDaily, "", false, "schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// ⑤ 经 cmd
	probe("cmd /c schtasks /query", "", false, "cmd", "/c", "schtasks /query")
	// ⑥ 对照：同样在 System32 的另一个工具
	probe("sc query Schedule（对照，已知能成功）", "", false, "sc", "query", "Schedule")
	probe("where schtasks（对照）", "", false, "where", "schtasks")
	// ⑦ PowerShell 的等价物
	probe("powershell Get-ScheduledTask", "", false, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", "Get-ScheduledTask -TaskName '"+TaskDaily+"' | Select-Object -ExpandProperty TaskName")
	probe("powershell Get-Service Schedule（对照）", "", false, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", "(Get-Service Schedule).Status")

	p("")
	// ⑤ **兼容性 shim** —— 这条很容易被忽略，但能解释"同一个 System32 里
	// 有的程序能跑、有的报找不到路径"。
	//
	// 如果 exe 被设了「兼容模式」，Windows 会设置 __COMPAT_LAYER 环境变量，
	// 而它**会被子进程继承**；某些 shim 会做**文件系统虚拟化**，
	// 把 System32 的访问重定向到 %LOCALAPPDATA%\VirtualStore ——
	// 那里当然没有 schtasks，于是报"系统找不到指定的路径"。
	p("")
	p("=== 兼容性 shim（重点）===")
	layer := os.Getenv("__COMPAT_LAYER")
	if layer == "" {
		p("  __COMPAT_LAYER: 未设置（正常）")
	} else {
		p("  ⚠ **__COMPAT_LAYER = %q** —— 进程带着兼容性 shim，子进程会继承", layer)
	}
	// 注册表里对这个 exe 设的兼容性标志
	if exe, err := os.Executable(); err == nil {
		probeAppCompat(p, exe)
	}
	// VirtualStore 里有没有被重定向过去的东西
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		vs := filepath.Join(local, "VirtualStore", "Windows", "System32")
		if _, err := os.Stat(vs); err == nil {
			files, _ := os.ReadDir(vs)
			p("  ⚠ VirtualStore\\Windows\\System32 存在，里面有 %d 项 —— 说明发生过重定向", len(files))
			for i, f := range files {
				if i >= 5 {
					break
				}
				p("      %s", f.Name())
			}
		}
	}

	// ⑥ 会话与令牌 —— 这两项决定"能不能访问任务计划程序服务"
	p("")
	p("=== 进程上下文 ===")
	p("  会话 ID: %d", processSessionID())
	p("  是否管理员: %v", isElevated())
	p("  完整性级别: %s%s", integrityLevel(), integrityHint())
	p("  父进程 PID: %d", os.Getppid())
	if pp, err := parentName(); err == nil {
		p("  父进程: %s", pp)
	}
	// 直接问一句：能不能打开任务计划程序服务
	if err := probeTaskService(); err != nil {
		p("  ⚠ **连不上任务计划程序服务**：%v", err)
		p("     这会让 schtasks / Get-ScheduledTask 全部失败，且错误常是「找不到路径」")
	} else {
		p("  任务计划程序服务: 可访问")
	}

	p("")
	p("=== 说明 ===")
	p("如果上面只有「Dir+标志」那一行失败，问题就在 hiddenCommand 的设置；")
	p("如果全部都失败，那是进程环境本身（PATH/权限/令牌）；")
	p("如果全部都成功，那失败发生在别处，请把这份报告贴出来。")
	return b.String()
}

func nowText() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
