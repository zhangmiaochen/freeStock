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
		firstLine := strings.TrimSpace(decodeConsole(out))
		if i := strings.IndexByte(firstLine, '\n'); i > 0 {
			firstLine = firstLine[:i]
		}
		p("  %-46s %s", label, status)
		if firstLine != "" {
			p("      输出: %s", strings.TrimSpace(firstLine))
		}
	}

	// 都不设 Dir、都不设 CREATE_NO_WINDOW —— 最原始的形态
	probe("schtasks（不设Dir/不设标志）", "", false, "schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// 只设 CREATE_NO_WINDOW
	probe("schtasks（只设 CREATE_NO_WINDOW）", "", true, "schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// 只设 Dir
	probe("schtasks（只设 Dir=neutralDir）", neutralDir(), false, "schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// 两个都设（hiddenCommand 的形态）
	probe("schtasks（Dir+标志，即 hiddenCommand）", neutralDir(), true, "schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// Dir 设成 System32
	probe("schtasks（Dir=System32）", filepath.Join(os.Getenv("SystemRoot"), "System32"), false,
		"schtasks", "/query", "/tn", TaskDaily, "/fo", "LIST")
	// 完全显式指定 exe 路径
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	probe("schtasks.exe 全路径（不设Dir/不设标志）", "", false, filepath.Join(sys, "schtasks.exe"),
		"/query", "/tn", TaskDaily, "/fo", "LIST")

	p("")
	// ⑤ 会话与令牌 —— 这两项决定"能不能访问任务计划程序服务"
	p("")
	p("=== 进程上下文 ===")
	p("  会话 ID: %d", processSessionID())
	p("  是否管理员: %v", isElevated())
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
