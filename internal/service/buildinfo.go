package service

import (
	"log/slog"
	"runtime/debug"
)

// logBuildInfo 打印当前二进制的版本与 git 提交。
//
// 为什么需要：镜像 tag 是 latest，CI 一旦没跟上，"docker compose pull" 拉到的
// 还是旧构建，但日志/界面看起来和新版一模一样 —— 排查会一直在旧代码里打转。
// 有了这行，`docker compose logs | grep 构建信息` 就能确认到底跑的是哪个提交。
func logBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		slog.Info("构建信息不可用（未嵌入 build info）")
		return
	}
	var revision, buildTime, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			buildTime = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	slog.Info("构建信息",
		"version", info.Main.Version,
		"revision", revision,
		"buildTime", buildTime,
		"modified", modified,
		"go", info.GoVersion)
}
