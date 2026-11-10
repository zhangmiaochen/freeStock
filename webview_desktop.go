//go:build !server

package main

// webviewDataPathOption 返回桌面包需要的 WebView2 用户数据目录。
//
// 只有桌面构建（Windows / macOS / Linux GTK）才需要它；server 构建
// （容器）里没有 WebView，这个值就是空的（见 server_webview.go）。
func webviewDataPathOption() string {
	return webviewDataPath()
}
