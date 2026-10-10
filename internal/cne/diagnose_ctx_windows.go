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
	// ⚠ **第一个参数是进程 ID，不是句柄。**
	// 我第一版传了 `GetCurrentProcess()` 的伪句柄（0xFFFFFFFF），
	// 于是永远返回 0xFFFFFFFF —— 用户报告里那个"会话 ID: 4294967295"
	// 是我的 bug，不是真实值。这里改用 GetCurrentProcessId。
	var sid uint32
	pid := uintptr(os.Getpid())
	r, _, _ := procProcessIdToSess.Call(pid, uintptr(unsafe.Pointer(&sid)))
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

// integrityLevel 取当前进程令牌的完整性级别。
//
// ⚠ 为什么查：**COM 激活依赖令牌**。如果进程跑在低完整性、AppContainer
// 或受限令牌下，`CoCreateInstance` 会失败 —— 而报错恰好常是
// `ERROR_PATH_NOT_FOUND`（找不到注册的服务器路径）。
//
// 实测这台机器上的现象与这一条完全吻合：
//
//	schtasks /?            成功   （只打帮助，不连服务）
//	schtasks /query        失败   （走 COM）
//	Get-ScheduledTask      失败   （走 CIM）
//	sc query Schedule      成功   （走 SCM，不经 COM）
//	Get-Service Schedule   成功   （同上）
//
// 用 `whoami /groups` 取，而不是直接调 token API ——
// `x/sys/windows` 没导出 GetTokenInformation，自己拼一遍不划算。
func integrityLevel() string {
	out, err := hiddenCommand("whoami", "/groups").CombinedOutput()
	if err != nil {
		return "取不到"
	}
	for _, line := range strings.Split(decodeConsole(out), "\n") {
		// 形如：Mandatory Label\Medium Mandatory Level  S-1-16-8192
		if !strings.Contains(strings.ToLower(line), "mandatory level") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1]
		}
		return strings.TrimSpace(line)
	}
	return "取不到"
}

// integrityHint 在完整性级别异常时附加一句提示。
func integrityHint() string {
	lv := strings.ToLower(integrityLevel())
	if lv == "" || strings.Contains(lv, "取不到") {
		return ""
	}
	if strings.Contains(lv, "medium") || strings.Contains(lv, "high") || strings.Contains(lv, "system") {
		return ""
	}
	return "  ⚠ **不是 Medium/High 完整性 —— COM 激活很可能因此失败（正是当前症状）**"
}
