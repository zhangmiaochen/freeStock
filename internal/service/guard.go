package service

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// recoverAsError 把**绑定方法里的 panic** 变成一个普通 error。
//
// 为什么需要它：
//
//	wails 的 HTTP 传输里，Go 方法 panic 时 net/http 会 recover、**直接关掉连接**
//	—— 浏览器那边看到的是 `net::ERR_EMPTY_RESPONSE`（一个字节都没收到），
//	既没有状态码也没有错误文本。用户只能看到"打不开"，日志里也只有一行
//	`http: panic serving ...` 的堆栈，很难和具体页面联系起来。
//
// 这里在方法入口 defer 一下：panic 时记 **完整堆栈**（进日志文件），
// 并返回一个可读的 error —— 前端至少能把"哪一步炸了"显示出来。
//
// ⚠ 它只是兜底，不改变任何正常路径的行为。
func recoverAsError(method string, errp *error) {
	r := recover()
	if r == nil {
		return
	}
	slog.Error("绑定方法 panic（已兜住，前端会看到错误而不是空响应）",
		"method", method,
		"panic", fmt.Sprint(r),
		"stack", string(debug.Stack()),
	)
	*errp = fmt.Errorf("%s 内部错误：%v（详情见日志 freeStock.log）", method, r)
}
