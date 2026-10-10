//go:build windows

package cne

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32diag        = syscall.NewLazyDLL("kernel32.dll")
	procProcessIdToSess = kernel32diag.NewProc("ProcessIdToSessionId")
	procGetCurProcess   = kernel32diag.NewProc("GetCurrentProcess")
	shell32diag         = syscall.NewLazyDLL("shell32.dll")
	procIsUserAnAdmin   = shell32diag.NewProc("IsUserAnAdmin")
)

// processSessionID 返回当前进程所在的终端服务会话 ID。
//
// ⚠ 为什么查这个：任务计划程序是**按会话/用户**提供服务的。
// 如果应用跑在一个异常会话里（被服务或计划任务拉起、或换了登录会话），
// `schtasks` 与 CIM 都可能连不上，而报错恰好常是
// "系统找不到指定的路径"（ERROR_PATH_NOT_FOUND）—— 与"任务不存在"极像，
// 所以必须先把这个可能性排除掉。
func processSessionID() uint32 {
	var sid uint32
	h, _, _ := procGetCurProcess.Call()
	r, _, _ := procProcessIdToSess.Call(h, uintptr(unsafe.Pointer(&sid)))
	if r == 0 {
		return 0xFFFFFFFF
	}
	return sid
}

// isElevated 判断当前进程是否以管理员身份运行。
func isElevated() bool {
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

// currentPPID 取父进程 PID。
func currentPPID() int { return os.Getppid() }

// parentName 取父进程的可执行文件名 —— 用来看应用是被谁拉起来的
// （explorer / 终端 / 某个服务 / CNEquity GUI …）。
//
// 用 tasklist 而不是 PowerShell：PowerShell 本身可能就是失败的那一环，
// 用它做诊断会引入干扰。
func parentName() (string, error) {
	out, err := hiddenCommand("tasklist", "/FI",
		fmt.Sprintf("PID eq %d", currentPPID()), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(firstLineOf(decodeConsole(out)))
	// CSV: "name.exe","1234","Console","1","12,345 K"
	if strings.HasPrefix(line, `"`) {
		parts := strings.SplitN(strings.TrimPrefix(line, `"`), `"`, 2)
		if len(parts) > 0 && parts[0] != "" {
			return parts[0], nil
		}
	}
	return line, nil
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// probeTaskService 实测能不能访问任务计划程序服务。
//
// 刻意用 `sc query Schedule` 而不是 `schtasks`：这样能把
// **"连不上服务"** 与 **"任务不存在"** 区分开 —— 两者报错很像，
// 处理方式却完全不同。
func probeTaskService() error {
	out, err := hiddenCommand("sc", "query", "Schedule").CombinedOutput()
	text := decodeConsole(out)
	if err != nil {
		return fmt.Errorf("%v（%s）", err, strings.TrimSpace(firstLineOf(text)))
	}
	if !strings.Contains(text, "RUNNING") && !strings.Contains(text, "运行") {
		return fmt.Errorf("服务不在运行状态：%s", strings.TrimSpace(firstLineOf(text)))
	}
	return nil
}

// probeAppCompat 查注册表里给这个 exe 设的兼容性标志。
//
//	位置（每个用户）：HKCU\Software\Microsoft\Windows NT\CurrentVersion\
//	                  AppCompatFlags\Layers
//
// 值名 = exe 全路径，值 = 形如 "~ WIN8RTM" 的标志串。
//
// 为什么查：设了兼容模式的 exe 会让 Windows 设置 __COMPAT_LAYER，
// 而**子进程会继承它**；某些 shim 做文件系统虚拟化，把 System32 访问
// 重定向到 VirtualStore，于是 schtasks 报"找不到指定的路径"。
// 这能解释"同一个目录里 sc 能跑、schtasks 不能"这种反常现象。
func probeAppCompat(p func(string, ...any), exe string) {
	path := `HKCU\Software\Microsoft\Windows NT\CurrentVersion\AppCompatFlags\Layers`
	out, err := hiddenCommand("reg", "query", path, "/v", exe).CombinedOutput()
	text := decodeConsole(out)
	if err != nil {
		// 查不到通常意味着"没设过"，这是正常的
		if strings.Contains(text, "找不到") || strings.Contains(text, "cannot find") ||
			strings.Contains(text, "unable to find") {
			p("  注册表兼容性标志: 未设置（正常）")
		} else {
			p("  注册表兼容性标志: 查不到（%s）", strings.TrimSpace(firstLineOf(text)))
		}
		return
	}
	p("  ⚠ **该 exe 设了兼容性标志**：%s", strings.TrimSpace(firstLineOf(text)))
	p("     这会让子进程继承 __COMPAT_LAYER，可能触发文件系统虚拟化")
}
