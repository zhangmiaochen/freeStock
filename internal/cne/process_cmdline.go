package cne

import "strings"

// isUpdateCommand 判断一条 cne 命令行是否是**会写数据的更新**。
//
// 判据：出现在 `run`（`cne run daily` / `cne run ...`）或 `ingest` 上。
// `serve`（常驻服务）、`query`（只读查询）、`--help` 都不算。
//
// ⚠ **必须放在不带 build tag 的文件里**：
// 这个判断本身是纯字符串逻辑，两个平台共用一个口径；
// 而且 `process_test.go` 没有 build tag，把函数塞在 process_windows.go
// 里会让 linux 上的 `go vet ./...` 报 undefined: isUpdateCommand。
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
		if strings.HasSuffix(f, "cne.exe") || f == "cne" || strings.HasSuffix(f, `\cne`) ||
			strings.HasSuffix(f, "/cne") {
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
