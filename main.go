package main

import (
	"embed"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"freestock/internal/config"
	"freestock/internal/rtinfo"
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
// 先容器/NAS 指定的数据目录，再用户缓存目录，最后退回程序所在目录
// （绿色版/受限环境常常只能写后者）。
func logFilePaths() []string {
	var paths []string
	// ⚠ 容器部署：日志必须落在挂载卷上，否则 docker logs 之外啥都留不下
	if dir := strings.TrimSpace(os.Getenv("FREESTOCK_DATA_DIR")); dir != "" {
		paths = append(paths, filepath.Join(dir, "logs", "freeStock.log"))
	}
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

// serverHost / serverPort 决定 server 模式（-tags server）监听的地址。
//
// 这两个值在桌面构建里**完全被忽略**（只有 server 模式读 Options.Server），
// 所以加在这里对 Windows 桌面包零影响。
// 容器里由 Dockerfile / compose 注入 WAILS_SERVER_HOST=0.0.0.0；
// 这里默认 0.0.0.0 是刻意的：容器内绑定 127.0.0.1 会导致端口映射不通，
// 而这个二进制本来就是拿来当服务跑的。
func serverHost() string {
	if v := strings.TrimSpace(os.Getenv("WAILS_SERVER_HOST")); v != "" {
		return v
	}
	return "0.0.0.0"
}

func serverPort() int {
	if v := strings.TrimSpace(os.Getenv("WAILS_SERVER_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			return p
		}
		log.Printf("[FreeStock] WAILS_SERVER_PORT=%q 不是合法端口，用默认 8080", v)
	}
	return 8080
}

// serverWriteTimeout 是 HTTP 响应写超时。
//
// ⚠ **不能返回 0**：wails 会把 0 替换成它自己的 30 秒默认值
// （application_server.go 里 `if writeTimeout == 0 { ... 30s }`）。
// 传 0 等于什么都没改，而且日志还会骗你说"不限制"。
//
// 默认 30 分钟：覆盖最慢的冷启动（扫数据湖），同时给卡死的请求一个上限。
// 需要收紧时用 FREESTOCK_SERVER_WRITE_TIMEOUT=90s 这类值覆盖。
func serverWriteTimeout() time.Duration {
	const fallback = 30 * time.Minute

	raw := strings.TrimSpace(os.Getenv("FREESTOCK_SERVER_WRITE_TIMEOUT"))
	if raw == "" {
		// 一定要打出来：这条日志是"是否为新构建"的可靠标记，
		// 也是排查"请求被掐断"时第一个要看的事实。
		log.Printf("[FreeStock] HTTP 写超时 = %s（默认，可用 FREESTOCK_SERVER_WRITE_TIMEOUT 覆盖）", fallback)
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		log.Printf("[FreeStock] HTTP 写超时 = %s", d)
		return d
	}
	log.Printf("[FreeStock] FREESTOCK_SERVER_WRITE_TIMEOUT=%q 无法解析（必须为正数），用默认 %s", raw, fallback)
	return fallback
}

// logFrontendBundle 在启动日志里报出**这次运行的是哪一版前端**。
//
// 为什么值得单独一行：容器里前端是**嵌进二进制**的，而浏览器报的错里带着
// bundle 文件名（`index-9t_7-KdC.js`）。如果用户换了镜像但日志里仍是旧名字，
// 说明跑的还是旧镜像/旧缓存 —— 这条能一句话区分"代码没修好"和"镜像没更新"，
// 省掉整轮来回。
func logFrontendBundle() {
	raw, err := assets.ReadFile("frontend/dist/index.html")
	if err != nil {
		return
	}
	// 抓第一个 assets/*.js（Vite 的主入口）
	re := regexp.MustCompile(`assets/([A-Za-z0-9._-]+\.js)`)
	if m := re.FindSubmatch(raw); len(m) == 2 {
		log.Printf("[FreeStock] 前端产物: %s", string(m[1]))
	}
}

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

	// 容器里按 cgroup 上限设 Go 软内存上限：接近上限时主动 GC，
	// 而不是先被内核 OOM kill（那个表现为"响应截断/空响应/时好时坏"）。
	if soft, note := rtinfo.SetSoftLimit(); note != "" {
		log.Printf("[FreeStock] %s", note)
		if soft > 0 {
			log.Printf("[FreeStock] 内存软上限已生效: %s", rtinfo.Format(soft))
		}
	}

	logFrontendBundle()

	store := config.NewStore()
	stockService := service.NewStockService(store)
	// 组合库按需打开（首次用到组合功能时才建文件），这里只构造服务
	portfolioService := service.NewPortfolioService(stockService)
	defer portfolioService.Close()
	// cne 数据更新（手动更新闭环；自动更新见 docs/cne数据更新方案.md 第 3 阶段）
	cneUpdateService := service.NewCneUpdateService(store)
	// 更新一结束就让派生缓存失效重建：数据湖刚落了新数据，
	// 「按股票」日线缓存（cache/bars-by-symbol）必须在用户查询之前跟上。
	// 否则要等下一次查询自己发现源分区指纹变了才发现。
	cneUpdateService.SetOnDataChanged(stockService.InvalidateDerivedCaches)

	// 数据预热（全量行情快照、板块区间基准价）由 stockService 的 ServiceStartup 钩子
	// 在后台发起 —— 数据湖上第一次要 4~6 秒，提前取好可以让首次进页面不用等。
	// 口径与"为什么"见 internal/service/warmup.go。

	app := application.New(application.Options{
		Name:        "FreeStock",
		Description: "A 股本地量化数据工具（数据来自 free-stockdb）",
		Logger:      logger,
		LogLevel:    slog.LevelDebug,
		Services: []application.Service{
			application.NewService(stockService),
			application.NewService(portfolioService),
			application.NewService(cneUpdateService),
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
			//
			// server 构建里该值为空（见 webview_server.go）——
			// 容器没有 WebView2，也不该去写只读的程序目录。
			WebviewUserDataPath: webviewDataPathOption(),
		},
		// Server 只在 `-tags server` 构建（容器/Linux/NAS）时被读取；
		// 桌面构建里 wails 完全忽略这个字段，Windows 行为不变。
		Server: application.ServerOptions{
			Host: serverHost(),
			Port: serverPort(),
			// ⚠⚠ **写超时是这套部署里最坑的一个值**，说明写在这里：
			//
			// wails v3.1.0 的 application_server.go 里是这么处理的：
			//
			//	writeTimeout := opts.WriteTimeout
			//	if writeTimeout == 0 {
			//	    writeTimeout = 30 * time.Second     // ← 传 0 会被换成 30 秒！
			//	}
			//
			// 也就是说 **`WriteTimeout: 0` 不是"不限制"，而是"30 秒"**。
			// 我上一轮就是踩了这个坑：以为传 0 关掉了超时，实际什么都没改。
			// 于是任何超过 30s 的请求（冷启动扫数据湖、板块区间回看）都会被
			// net/http 掐断连接 —— 浏览器看到的是 ERR_EMPTY_RESPONSE /
			// Failed to fetch（一个字节都没收到），而 Go 里的计算还在继续跑，
			// 表现成"CPU 先升后降、时好时坏"。
			//
			// 正确做法是传一个**非 0 的大值**（传负数也能绕过判断，但可读性差）。
			// 这里默认 30 分钟，足够覆盖最慢的冷启动，又不至于让一个卡住的
			// 请求永久占着连接。
			WriteTimeout: serverWriteTimeout(),
			// 读超时保持较短：它只覆盖"读完请求头/体"，与业务耗时无关。
			ReadTimeout: 60 * time.Second,
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
