//go:build server

package main

// webviewDataPathOption 在 server 构建里返回空串。
//
// 容器里没有 WebView2 / WebKit，也不需要用户数据目录。刻意不调用
// webviewDataPath()：那个函数会尝试在用户缓存目录或**可执行文件同目录**
// 建目录，而镜像里的 /usr/local/bin 是只读的，会在启动日志里留下一行
// 误导性的"警告：找不到可写的 WebView2 用户数据目录"。
func webviewDataPathOption() string {
	return ""
}
