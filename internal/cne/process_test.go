package cne

import "testing"

// ⚠ **回归测试**：跨进程检测必须能区分 `cne serve`（常驻）和 `cne run`（更新）。
//
// 实测这台机器上两者**同时存在**，镜像名都是 cne.exe：
//
//	PID 30684  "cne.exe" serve
//	PID 20420  "cne" run daily --trade-date 2026-10-09 ...
//
// 早期实现只按镜像名匹配、取第一个，于是把常驻的 serve 当成"更新在进行"，
// **会永久挡住所有更新** —— 这是必须钉死的行为。
func TestIsUpdateCommandDistinguishesServeFromRun(t *testing.T) {
	cases := []struct {
		cmdline string
		want    bool
		why     string
	}{
		// 真实抓到的两条
		{`"D:\softInstal\python3.11.3\Scripts\cne.exe" serve`, false, "常驻守护进程，不是更新"},
		{`"cne"  run daily --trade-date 2026-10-09 --config "D:\workspace\stockTool\CNEquity\configs\cnequity.toml"`, true, "真实的日更"},

		// 其他形态
		{`cne run daily --stale-only --config x.toml`, true, "补抓落后"},
		{`cne.exe ingest`, true, "ingest 会写数据"},
		{`cne query "select 1"`, false, "只读查询"},
		{`cne --help`, false, "帮助"},
		{`cne -h`, false, "帮助"},
		{`cne.exe`, false, "只有可执行文件、没有子命令"},
		{``, false, "空命令行"},
	}
	for _, c := range cases {
		if got := isUpdateCommand(c.cmdline); got != c.want {
			t.Errorf("isUpdateCommand(%q) = %v，期望 %v（%s）", c.cmdline, got, c.want, c.why)
		}
	}
}

// 常驻 serve 单独再钉一遍 —— 它一旦被误判，功能就完全不可用。
func TestServeIsNeverTreatedAsUpdate(t *testing.T) {
	for _, cmdline := range []string{
		`"D:\softInstal\python3.11.3\Scripts\cne.exe" serve`,
		`cne serve`,
		`cne.exe serve --host 127.0.0.1`,
		`"C:\Python\Scripts\cne.exe"  serve`,
	} {
		if isUpdateCommand(cmdline) {
			t.Errorf("**常驻 serve 被当成了更新**：%q —— 会导致更新永久被拒", cmdline)
		}
	}
}
