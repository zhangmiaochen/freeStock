package service

import (
	"log/slog"
	"runtime/debug"
)

// 下面三个变量由构建时注入（见 Dockerfile / .github/workflows/docker.yml）：
//
//	go build -ldflags "-X freestock/internal/service.BuildCommit=<sha> ..."
//
// 为什么需要：镜像 tag 是 latest，CI 一旦没跟上，"docker compose pull" 拉到的
// 还是旧构建，但日志/界面看起来和新版一模一样 —— 排查会一直在旧代码里打转。
// 另外容器里通常没有 .git，`debug.ReadBuildInfo()` 的 vcs.revision 是空的，
// 只靠它无法确认版本。
var (
	BuildVersion = ""
	BuildCommit  = ""
	BuildTime    = ""
)

// logBuildInfo 打印当前二进制的版本与 git 提交。
func logBuildInfo() {
	version, commit, buildTime := BuildVersion, BuildCommit, BuildTime

	info, ok := debug.ReadBuildInfo()
	if ok {
		var vcsRevision, vcsTime string
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRevision = setting.Value
			case "vcs.time":
				vcsTime = setting.Value
			}
		}
		if commit == "" {
			commit = vcsRevision
		}
		if buildTime == "" {
			buildTime = vcsTime
		}
		if version == "" {
			version = info.Main.Version
		}
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		commit = "unknown（镜像未注入提交号）"
	}
	slog.Info("构建信息",
		"version", version,
		"revision", commit,
		"buildTime", buildTime,
		"go", goVersion(info, ok))
}

func goVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "unknown"
	}
	return info.GoVersion
}
