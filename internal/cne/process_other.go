//go:build !windows

package cne

import (
	"os/exec"
	"syscall"
)

// 非 Windows 平台（Linux / NAS / Docker）的进程封装。
//
// ⚠ 这里保留与 Windows 版**同名同签名**的 `hiddenCommand` /
// `setupHiddenProcess`：`runner.go` 是跨平台的，调用点不能分叉。
// 区别只在语义上：
//   - Windows 需要 CREATE_NO_WINDOW 防止弹黑框；这里是守护进程，没有黑框问题
//   - 两边都要**独立进程组**，取消时才能把 cne 派生的 python 子进程一起带走

// setupHiddenProcess 让子进程进入独立进程组。
//
// Linux/NAS 上没有"窗口"这个概念，需要的只是进程组：cne 会派生 python
// 子进程，取消时按负 pid（整组）杀，才能不留孤儿。
func setupHiddenProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// setupProcessGroup 保留旧名，供内部与测试使用。
func setupProcessGroup(cmd *exec.Cmd) { setupHiddenProcess(cmd) }

// hiddenCommand 构造一个外部命令。
//
// ⚠ **不设置 cmd.Dir**：Windows 版必须设一个"必定存在"的目录，
// 因为 cmd.exe 对当前目录敏感且 GUI 进程的 CWD 不可控。
// Linux 上没有这个问题，而且**盲目设 Dir 会有害**：
// 调用方（runner）随后要覆盖成工程目录，若这里先设成临时目录，
// 一旦某条路径忘了覆盖，cne 就会在 /tmp 里找不到 configs/cnequity.toml。
// 不设就是继承父进程的 CWD，与在 shell 里手敲命令一致。
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	setupHiddenProcess(cmd)
	return cmd
}

// killProcessTree 杀掉整棵进程树。
//
// 用负 pid = 整个进程组（配合 setupHiddenProcess 的 Setpgid）。
// 进程已经结束时返回 nil —— 与 Windows 版口径一致：
// "取消时它已经不在了"不该算失败。
func killProcessTree(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	return nil
}

// foreignCneRunning 在非 Windows 上暂不做跨进程检测。
//
// Windows 版靠 PowerShell 的 Win32_Process 查命令行；Linux 上要读 /proc，
// 而 /proc 在容器里可用、在 NAS 的某些发行版里也能用 —— 但这属于
// "锦上添花"的第 3 道闸，先返回 false 不阻断更新。真正的互斥仍由
// Runner 的同进程锁 + cne 自己的 ingest 锁保证。
func foreignCneRunning() (int, bool) {
	return 0, false
}
