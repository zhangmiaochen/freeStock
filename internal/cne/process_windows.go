//go:build windows

package cne

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// neutralDir 返回一个**必定存在、且语言中立**的目录，用作子进程的工作目录。
//
// ⚠ 这是修一个实际报错：用户点「开启自动更新」时得到
//
//	/enable cne-daily-update 失败：exit status 1（ERROR: The system cannot find the path specified.）
//
// 而**同一条命令在 PowerShell 里跑是 exit 0** —— 问题不在命令，在进程环境。
// `cmd.exe` 对**当前工作目录**很敏感：目录无效或不存在时，它报的正是
// "The system cannot find the path specified."。GUI 应用的 CWD 不可控
// （可能是快捷方式的"起始位置"、被删掉的目录、或构建期间的 bin/），
// 所以不能依赖继承来的 CWD。
// ⚠ **不要用 System32**。实测（debug 输出）：
//
//	Dir = C:\Windows\System32  -> schtasks 输出**中文**（GBK）
//	                               "任务名: \cne-daily-update"（字节是 GBK）
//	Dir = 其它有效目录           -> schtasks 输出**英文**
//	                               "TaskName: \cne-daily-update"
//
// 而 decodeConsole 对非 UTF-8 的处理是"剥掉非法字节"，
// GBK 的中文被剥光后字段全空 —— 于是解析失败、界面显示空白。
// 所以这里刻意避开 System32，用临时目录（必定存在、语言中立）。
func neutralDir() string {
	// ⚠ **多级兜底 + 逐级校验存在性**。
	//
	// 这个值会被设成 cmd / schtasks 的工作目录；一旦它是无效路径，
	// 那些程序会直接报 "The system cannot find the path specified." ——
	// 而**错误信息里完全看不出是这个目录的问题**，极难定位。
	// 实测踩过：cmd /c 那条路在用户机器上一直失败。
	//
	// 所以这里不能只"取一个看着合理的值"，必须**确认它真的存在**；
	// 全都不行时退到可执行文件自己所在的目录 —— 那个必定存在。
	candidates := []string{
		os.TempDir(),
		os.Getenv("TEMP"),
		os.Getenv("TMP"),
		os.Getenv("USERPROFILE"),
		`C:\`,
	}
	for _, dir := range candidates {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				return dir
			}
		}
	}
	return `C:\`
}

// hiddenCommand 构造一个**不会弹窗**的外部命令。
//
// 做两件事：
//   - CREATE_NO_WINDOW：不弹黑框（用户实测反馈过 cne 与 powershell 的黑框）
//   - **显式设置 Dir**：不继承 GUI 进程那个不可控的当前目录
//     （见 systemDir 的说明）
//
// ⚠ 需要特定工作目录的调用（例如 cne 必须在工程目录里跑）要在返回后
// 自己覆盖 cmd.Dir —— runner 就是这么做的。
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = neutralDir()
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
