// Package cne 负责**调用外部 cne 命令**（数据更新），与本项目的
// `internal/cnlake`（直读 parquet 做查询）分开：更新和查询是两件事。
//
// 本包**不修改 cne 源码**，一律通过它自己的官方命令 `cne run daily` 完成。
package cne

import (
	"os"
	"strings"
)

// proxyVars 是要从子进程环境里**彻底删掉**的代理变量（按大小写不敏感匹配）。
//
// ⚠ 这不是"顺手清理"，是必须的 —— 本机环境里同时存在 `NO_PROXY` 与 `no_proxy`，
// 只清大写那份时，小写那份的 `::1,[::1]` 仍会被 httpx 读成代理挂载点，
// 于是每个 step 都崩在构造 HTTP 客户端：
//
//	httpx.InvalidURL: Invalid port: ':1]'
//
// 更阴的是 **Python 的 `os.environ` 在 Windows 上会把键名统一成大写**，
// 所以从 Python 侧根本看不出有重复项 —— 排查时极易误判"已经清干净了"。
// 实测补上小写清理后，同一命令立刻正常取数。
var proxyVars = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	// 下面这些是常见变体，一并按大小写不敏感过滤
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"FTP_PROXY", "ftp_proxy", "SOCKS_PROXY", "socks_proxy",
}

// isProxyVar 判断某个环境变量名是否是代理变量（**大小写不敏感**）。
//
// 用 EqualFold 而不是直接查表：Windows 环境变量的键名大小写不可靠，
// 表里列全所有大小写组合是不可能的，也没必要。
func isProxyVar(key string) bool {
	for _, name := range proxyVars {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// BuildEnv 返回给 cne 子进程用的环境变量。
//
// 做两件事：
//  1. **按大小写不敏感删掉所有代理变量**（见 proxyVars 的说明，§2.4 的坑）；
//  2. 打开 UTF-8 输出，让 cne 的中文进度能正常落进日志文件。
//     （不设的话中文日志在重定向到文件时会按 GBK 写入，变乱码。）
func BuildEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+3)
	for _, kv := range src {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if isProxyVar(key) {
			continue
		}
		out = append(out, kv)
	}

	out = append(out,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		// 不缓冲，日志才能实时 tail 出进度（cne 会定期打 still working）
		"PYTHONUNBUFFERED=1",
	)
	return out
}
