//go:build !windows

package cne

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Diagnose 在 Linux / NAS / Docker 上给出调度相关的实测信息。
//
// Windows 版那一大堆（完整性级别、会话 ID、schtasks 路径）在这里都无意义 ——
// 这些平台没有任务计划程序。取而代之，真正会出问题的是另外几件事：
//
//   - **时区**：定时任务按本地时间判断到点没有。容器里缺 tzdata / TZ 没设，
//     `time.Now()` 会回落到 UTC，「17:30」变成次日 01:30，而且**不报任何错**。
//   - **数据目录可写性**：config.json 与运行痕迹都落在这里；
//     只读挂载时调度器读得到设置、却写不回"今天跑过了"，会重复触发。
//   - **cne 可执行文件**：容器里默认没有，到点会失败 —— 而失败日志
//     只写一行，容易被忽略。
//
// 所以这里只报这三个真正会导致"到点了但没跑"的事实。
func Diagnose() string {
	var b strings.Builder
	p := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	p("=== FreeStock 定时任务诊断（Linux / NAS / 容器）===")
	p("时间: %s", time.Now().Format("2006-01-02 15:04:05 MST"))
	p("调度方式: 应用进程内调度器（不依赖 cron / systemd）")

	// 1. 时区 —— 容器里最常见、也最隐蔽的坑
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		p("TZ=%s", tz)
	} else {
		p("TZ 未设置（依赖系统 /etc/localtime）")
	}
	_, offset := time.Now().Zone()
	p("当前时区偏移: UTC%+d（北京时间为 UTC+8；不是 +8 说明时区没配对）", offset/3600)

	// 2. 数据目录
	if dir := strings.TrimSpace(os.Getenv("FREESTOCK_DATA_DIR")); dir != "" {
		p("FREESTOCK_DATA_DIR=%s", dir)
		probe := dir + string(os.PathSeparator) + ".write-probe"
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			p("  ⚠ 不可写：%v —— 配置与调度痕迹无法落盘，重启后会重复触发", err)
		} else {
			_ = os.Remove(probe)
			p("  可写 ✓")
		}
	} else {
		p("FREESTOCK_DATA_DIR 未设置（配置落在程序目录/用户配置目录）")
	}

	// 3. cne 可执行文件
	if pth, err := lookPathCne(); err == nil {
		p("cne: %s", pth)
	} else {
		p("⚠ 找不到 cne：%v —— 到点会发起失败（查询功能不受影响）", err)
	}

	p("")
	p("提示：本平台的「计划任务」由应用自己执行，容器/进程停着不会跑；")
	p("重启后若当天计划时间已过且尚未执行，会自动补跑一次。")
	return b.String()
}
