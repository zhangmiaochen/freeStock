//go:build !windows

package cne

import "os/exec"

// lookPathCne 在 PATH 上找 cne（诊断用）。
//
// 单独抽出来而不是直接调 exec.LookPath：诊断要把"找不到"作为一条
// 可读信息报出来，而不是让调用方处理 error 类型。
func lookPathCne() (string, error) {
	return exec.LookPath("cne")
}
