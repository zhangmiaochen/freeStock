// Package rtinfo 提供运行时的内存事实，供启动日志与界面「诊断」共用。
//
// 存在的理由：容器里"响应截断 / ERR_EMPTY_RESPONSE / 时好时坏"最常见的
// 原因是内存被 cgroup 卡死。把这些数字直接摊在界面上，用户不用进容器
// 敲命令也能把证据拿出来。
package rtinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// MemoryLimit 返回容器（cgroup）的内存上限；0 表示没有限制。
//
// 先看 cgroup v2，再回退 v1。
func MemoryLimit() (int64, string) {
	if v, err := readLimitFile("/sys/fs/cgroup/memory.max"); err == nil && v > 0 {
		return v, "cgroup v2"
	}
	if v, err := readLimitFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil && v > 0 {
		return v, "cgroup v1"
	}
	return 0, ""
}

func readLimitFile(path string) (int64, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	return ParseLimit(string(raw)), nil
}

// ParseLimit 把 cgroup 内容解析成字节数；"max"/空/非法 一律返回 0。
//
// ⚠ v1 用 "9223372036854771712" 这类超大值表示"不限制"，这里也当 0 处理：
// 否则会算出一个 7EB 的软上限，等于没设。
func ParseLimit(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "max") {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	const unlimited = int64(1) << 62
	if v >= unlimited {
		return 0
	}
	return v
}

// SetSoftLimit 按容器上限的 80% 设置 Go 软内存上限（GOMEMLIMIT）。
//
// 已用环境变量显式指定时不动它 —— 用户显式配置优先。
// 返回生效的软上限与说明，调用方负责打日志。
func SetSoftLimit() (soft int64, note string) {
	if v := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); v != "" {
		return 0, "GOMEMLIMIT 已由环境变量指定：" + v
	}
	limit, source := MemoryLimit()
	if limit <= 0 {
		return 0, "未检测到容器内存上限（未设 limit 或 cgroup 不可读）"
	}
	soft = limit * 8 / 10
	debug.SetMemoryLimit(soft)
	return soft, fmt.Sprintf("容器内存上限 %s（%s），Go 软上限设为 %s",
		Format(limit), source, Format(soft))
}

// Describe 返回一小段人类可读的内存/协程现状，给界面「诊断」用。
func Describe() string {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	limit, source := MemoryLimit()
	limitText := "未检测到容器上限"
	if limit > 0 {
		limitText = fmt.Sprintf("%s（%s）", Format(limit), source)
	}

	softText := "未设置"
	if v := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); v != "" {
		softText = v + "（环境变量）"
	} else if limit > 0 {
		softText = Format(limit*8/10) + "（按上限 80% 自动设置）"
	}

	return fmt.Sprintf(
		"堆占用: %s（Sys %s，GC 次数 %d）\n协程数: %d\n容器内存上限: %s\nGo 软上限: %s",
		Format(int64(ms.HeapAlloc)), Format(int64(ms.Sys)), ms.NumGC,
		runtime.NumGoroutine(), limitText, softText)
}

// Format 把字节数变成 MiB/GiB，便于读日志和界面。
func Format(v int64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case v >= gib:
		return strconv.FormatFloat(float64(v)/float64(gib), 'f', 1, 64) + "GiB"
	case v >= mib:
		return strconv.FormatFloat(float64(v)/float64(mib), 'f', 0, 64) + "MiB"
	default:
		return strconv.FormatInt(v, 10) + "B"
	}
}
