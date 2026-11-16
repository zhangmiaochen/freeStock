package service

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// goSafe 跑一个**后台 goroutine**，并兜住它里面的 panic。
//
// 为什么必须兜：
//
//	net/http 只会 recover **它自己调用的 handler**。任何后台 goroutine
//	（预热、派生的重建任务、定时 tick、worker 池里的工作协程）一旦 panic，
//	Go 运行时**直接终止整个进程**。表现是：
//	  - 正在进行中的请求全部收到空响应（浏览器 ERR_EMPTY_RESPONSE）；
//	  - 容器被重启策略拉起后，时好时坏；
//	  - 日志里只有一段 goroutine 堆栈，看不出和哪个页面有关。
//
//	这正是"有时成功、有时失败"的典型形态。
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("后台任务 panic（已兜住，进程继续运行）",
					"task", name,
					"panic", fmt.Sprint(r),
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

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
