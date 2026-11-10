//go:build !windows

package cne

// lookPathCne 给诊断用：报出**当前实际会用哪一个 cne**。
//
// 早先它只是 exec.LookPath("cne")，于是容器里"把宿主机 cne 挂进来并
// 在设置里填了目录"这种情况，诊断仍报"找不到 cne"—— 与事实矛盾。
// 现在走和运行期完全相同的解析逻辑（ResolveCNE），诊断结论才可信。
//
// 注意：诊断拿不到用户设置里的 CNEPath，所以这里只能报"没配置时会用哪个"。
// 设置里填了目录的情况下，正确的判断入口是设置页的校验提示。
func lookPathCne() (string, error) {
	return ResolveCNE("")
}
