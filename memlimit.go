package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// applyContainerMemoryLimit 按**容器内存上限**设置 Go 的软内存上限（GOMEMLIMIT）。
//
// 为什么需要它：
//
//	Go 默认会一直用到"活跃对象的两倍"才回收。在 NAS 容器里，那意味着
//	峰值可能先撞到 cgroup 上限、被内核 OOM kill —— 表现是：
//	  · 响应被截断 → 前端解析失败（HTTP 200 但页面报"数据读取失败"）；
//	  · 或者一个字节都没写 → 浏览器 ERR_EMPTY_RESPONSE；
//	  · 容器被 restart 拉起后又"时好时坏"。
//	三种现象同源，且都不带任何"内存不足"的提示，极难判断。
//
//	设置软上限后，Go 在接近上限时会**主动加速 GC**，用一点 CPU 换不掉进程。
//
// 取值：上限的 80%，给非 Go 内存（parquet 解码的临时缓冲、内核页缓存记账）留余量。
// 已经通过环境变量 GOMEMLIMIT 显式设置时不覆盖 —— 用户显式配置优先。
func applyContainerMemoryLimit() {
	if v := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); v != "" {
		log.Printf("[FreeStock] GOMEMLIMIT 已由环境变量指定：%s", v)
		return
	}

	limit, source := containerMemoryLimit()
	if limit <= 0 {
		return
	}
	soft := limit * 8 / 10
	debug.SetMemoryLimit(soft)
	log.Printf("[FreeStock] 容器内存上限 %s（%s），Go 软上限设为 %s",
		humanBytes(limit), source, humanBytes(soft))
}

// containerMemoryLimit 读取 cgroup 的内存上限（v2 优先，回退 v1）。
//
// 返回 0 表示"没有上限"（未设限、或是无限大）。
func containerMemoryLimit() (int64, string) {
	// cgroup v2
	if v, err := readLimitFile("/sys/fs/cgroup/memory.max"); err == nil {
		if v > 0 {
			return v, "cgroup v2"
		}
	}
	// cgroup v1
	if v, err := readLimitFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		if v > 0 {
			return v, "cgroup v1"
		}
	}
	return 0, ""
}

// readLimitFile 解析 cgroup 的 limit 文件。
//
// 文件里可能是数字，也可能是字面量 "max"（v2 表示不限制）。
func readLimitFile(path string) (int64, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	return parseMemoryLimit(string(raw))
}

// parseMemoryLimit 把 cgroup 内容解析成字节数；"max"/空/非法 一律返回 0。
//
// ⚠ v1 用 "9223372036854771712" 这类超大值表示"不限制"，这里也当 0 处理：
// 否则会算出一个 7EB 的软上限，等于没设。
func parseMemoryLimit(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "max") {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, nil
	}
	if v <= 0 {
		return 0, nil
	}
	// v1 的"接近 int64 上限"表示无限制
	const unlimited = int64(1) << 62
	if v >= unlimited {
		return 0, nil
	}
	return v, nil
}

// humanBytes 把字节数变成 MiB/GiB 便于读日志。
func humanBytes(v int64) string {
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
