//go:build windows

package cne

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// setupProcessGroup 让子进程进入**独立的进程组**。
//
// cne 会派生子进程（python），只有把整棵进程树归到独立进程组，
// 取消时才能一次带走，不留孤儿（方案 §4.2）。
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcessTree 杀掉整棵进程树。
//
// 用 taskkill /T（连子树）/F（强制）—— 这是 Windows 上最可靠的方式；
// 只对父进程 Kill() 会留下 python 孤儿（方案 §11 验收清单明确要求核对）。
func killProcessTree(pid int) error {
	out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput()
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
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive",
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
