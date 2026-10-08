package main

import (
	"embed"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"freestock/internal/config"
	"freestock/internal/service"
)

// Wails 使用 Go 的 embed 把前端产物打进二进制。
// frontend/dist 由 `wails3 build`（或 `task build`）生成。
//
//go:embed all:frontend/dist
var assets embed.FS

// setupLogging 同时把日志写到 stderr 和日志文件。
//
// 打包后的程序带 -H windowsgui，没有控制台，出错时什么都看不到；
// 因此把日志落盘，出问题时可以直接把文件发出来定位。
// multiWriter 逐个写入，并且**忽略单个 writer 的错误**。
//
// 不能用 io.MultiWriter：它在第一个 writer 写出错时就立即返回，
// 而正式版带 -H windowsgui，os.Stderr 是无效句柄，
// 结果就是日志文件一个字节都写不进去（排查时会被误导）。
type multiWriter struct{ writers []io.Writer }

func (m multiWriter) Write(p []byte) (int, error) {
	for _, w := range m.writers {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

func setupLogging() (io.Writer, func()) {
	var reasons []string
	for _, path := range logFilePaths() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			reasons = append(reasons, fmt.Sprintf("创建目录 %s 失败: %v", dir, err))
			continue
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("打开 %s 失败: %v", path, err))
			continue
		}
		sink := multiWriter{writers: []io.Writer{os.Stderr, file}}
		// go-webview2 用的是标准库 log（WebView2 报错也从这里出来），必须一并接管
		log.SetOutput(sink)
		log.Printf("[FreeStock] 启动，日志文件: %s", path)
		return sink, func() { _ = file.Close() }
	}
	// 都写不进去就只留控制台输出；正式版没有控制台，这时确实没地方可看
	log.Printf("[FreeStock] 无法写入日志文件：%s", strings.Join(reasons, "; "))
	return os.Stderr, func() {}
}

// logFilePaths 按优先级给出候选日志路径：
// 先用户缓存目录，再退回程序所在目录（绿色版/受限环境常常只能写后者）。
func logFilePaths() []string {
	var paths []string
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		paths = append(paths, filepath.Join(dir, "FreeStock", "freeStock.log"))
	} else if err != nil {
		log.Printf("[FreeStock] 用户缓存目录不可用: %v", err)
	}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "freeStock.log"))
	} else {
		log.Printf("[FreeStock] 无法获取程序路径: %v", err)
	}
	return paths
}

// webviewDataPath 返回一个**确认可写**的 WebView2 用户数据目录。
//
// WebView2 需要在这个目录里建浏览器进程；目录不可用时窗口根本创建不出来。
// 这里按优先级挑第一个能创建成功的：用户缓存目录 -> 程序目录。
func webviewDataPath() string {
	var candidates []string
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		candidates = append(candidates, filepath.Join(dir, "FreeStock", "webview2"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "webview2-data"))
	}

	for _, path := range candidates {
		if err := os.MkdirAll(path, 0o755); err != nil {
			continue
		}
		// 真正写一个文件，确认不是只读目录
		probe := filepath.Join(path, ".writable")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			continue
		}
		_ = os.Remove(probe)
		log.Printf("[FreeStock] WebView2 用户数据目录: %s", path)
		return path
	}

	log.Println("[FreeStock] 警告：找不到可写的 WebView2 用户数据目录，窗口可能无法创建")
	return ""
}

func main() {
	sink, closeLog := setupLogging()
	defer closeLog()

	logger := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// 服务层用 slog.Info/Error 打点，这里设为默认 logger，日志才能进同一个文件
	slog.SetDefault(logger)

	store := config.NewStore()
	stockService := service.NewStockService(store)
	// 组合库按需打开（首次用到组合功能时才建文件），这里只构造服务
	portfolioService := service.NewPortfolioService(stockService)
	defer portfolioService.Close()

	app := application.New(application.Options{
		Name:        "FreeStock",
		Description: "A 股本地量化数据工具（数据来自 free-stockdb）",
		Logger:      logger,
		LogLevel:    slog.LevelDebug,
		Services: []application.Service{
			application.NewService(stockService),
			application.NewService(portfolioService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Windows: application.WindowsOptions{
			// 必须显式指定：留空时 WebView2 会用 %APPDATA%\<exe 名>，
			// 该目录不可写（或被其它 WebView2 程序占用）时，
			// CreateCoreWebView2Controller 会直接失败（ERROR_BUSY / 0x800700AA），
			// 表现就是「窗口一闪而过 / 完全没有界面」。
			WebviewUserDataPath: webviewDataPath(),
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "FreeStock · A 股数据工具",
		Width:            1440,
		Height:           920,
		MinWidth:         1080,
		MinHeight:        680,
		BackgroundColour: application.NewRGB(9, 11, 16),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		// 保留开发者工具：出问题时按 F12 就能看到控制台报错
		DevToolsEnabled: true,
		URL:             "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
