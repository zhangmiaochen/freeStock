//go:build !windows

package cne

import (
	"os/exec"
	"syscall"
)

// 非 Windows 平台：进程组与 kill 的语义不同，这里给出保守实现。
// 本项目主要面向 Windows 桌面端，这些分支只保证能编译。

func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killProcessTree(pid int) error {
	// 负 pid = 整个进程组
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func foreignCneRunning() (int, bool) {
	return 0, false
}
