//go:build windows

package cne

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// createNoWindow 让子进程**不弹控制台窗口**。
//
// ⚠ `cne.exe` 是控制台程序：用 exec.Command 直接起它，Windows 会给它分配
// 一个黑框，用户点一下「手动更新」就弹一个窗出来。用户实测反馈过这一点。
//
// 值取自 Win32 的 CREATE_NO_WINDOW（0x08000000）。Go 的 syscall 包里
// 在部分版本上没导出这个常量，所以显式定义 —— 它是稳定的 ABI 常量。
const createNoWindow = 0x08000000

// setupHiddenProcess 让子进程**既没有控制台窗口、又在独立进程组里**。
//
//   - CREATE_NO_WINDOW：不弹黑框（用户可见的体验问题）
//   - CREATE_NEW_PROCESS_GROUP：cne 会派生 python 子进程，
//     归到独立进程组便于取消时整棵树带走（方案 §4.2）
//
// ⚠ 即使 CREATE_NO_WINDOW 让 CREATE_NEW_PROCESS_GROUP 失效也没关系：
// 取消走的是 `taskkill /T /PID`，它按**父子关系**遍历进程树，不依赖进程组。
func setupHiddenProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP
}

// hiddenCommand 构造一个**不会弹窗**的外部命令。
//
// 本项目所有对外部程序的调用都必须走这里 —— 包括 taskkill / powershell /
// schtasks(经 cmd /c) —— 否则每调一次就闪一个黑框（实测 powershell 那个
// 尤其明显，因为它启动慢，窗口会停留可见的一段时间）。
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	setupHiddenProcess(cmd)
	return cmd
}

// setupProcessGroup 保留旧名，供内部与测试使用。
func setupProcessGroup(cmd *exec.Cmd) { setupHiddenProcess(cmd) }

// killProcessTree 杀掉整棵进程树。
//
// 用 taskkill /T（连子树）/F（强制）—— 这是 Windows 上最可靠的方式；
// 只对父进程 Kill() 会留下 python 孤儿（方案 §11 验收清单明确要求核对）。
func killProcessTree(pid int) error {
	out, err := hiddenCommand("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		// 进程可能已经自己结束了 —— 这种情况不算失败
		msg := string(out)
		if strings.Contains(msg, "not found") || strings.Contains(msg, "没有找到") ||
			strings.Contains(msg, "找不到") {
			return nil
		}
		return fmt.Errorf("taskkill 失败：%v（%s）", err, strings.TrimSpace(msg))
	}
	return nil
}

// foreignCneRunning 检测**本进程之外**是否已有一个 cne **更新**在跑。
//
// ⚠ **不能只看镜像名**。实测这台机器上同时存在：
//
//	PID 30684  "cne.exe" serve                                   <- 常驻守护进程
//	PID 20420  "cne" run daily --trade-date 2026-10-09 ...       <- 一次真实更新
//
// 两者镜像名都是 `cne.exe`，只按名字匹配、再取第一个，会把**常驻的 serve
// 当成"更新在进行"**，于是**永久挡住所有更新**。必须看命令行，
// 只把 `run` / `ingest` 这类真正会写数据的子命令算作"忙"。
//
// 用 PowerShell 取命令行（tasklist 不提供）；失败时返回 false ——
// 这个检查是"体验优化"，不该因为它查不到就拦住用户（方案 §5.4 第 3 道闸
// 是补充手段，前两道才是主防线）。
func foreignCneRunning() (int, bool) {
	const script = `Get-CimInstance Win32_Process -Filter "Name='cne.exe'" | ` +
		`ForEach-Object { "$($_.ProcessId)|$($_.CommandLine)" }`
	out, err := hiddenCommand("powershell", "-NoProfile", "-NonInteractive",
		"-Command", script).Output()
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || pid <= 0 {
			continue
		}
		if isUpdateCommand(parts[1]) {
			return pid, true
		}
	}
	return 0, false
}

// isUpdateCommand 判断一条 cne 命令行是否是**会写数据的更新**。
//
// 判据：出现在 `run`（`cne run daily` / `cne run ...`）或 `ingest` 上。
// `serve`（常驻服务）、`query`（只读查询）、`--help` 都不算。
func isUpdateCommand(cmdline string) bool {
	lower := strings.ToLower(cmdline)
	if lower == "" {
		return false
	}
	// 只读/常驻的子命令先排除
	for _, ignore := range []string{" serve", " query", "--help", "-h "} {
		if strings.Contains(lower, ignore) {
			return false
		}
	}
	// 取可执行文件之后的第一个参数做判据
	fields := strings.Fields(lower)
	for i, f := range fields {
		if strings.HasSuffix(f, "cne.exe") || f == "cne" || strings.HasSuffix(f, `\cne`) {
			if i+1 < len(fields) {
				sub := strings.Trim(fields[i+1], `"'`)
				return sub == "run" || sub == "ingest"
			}
			return false
		}
	}
	// 认不出可执行文件位置时，退一步：出现 `run daily` 就算
	return strings.Contains(lower, "run daily")
}
