package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ServiceStartup 是 Wails 的生命周期钩子（服务注册后由框架调用）。
//
// 预热放在这里而不是 main.go：应用已经构造完成、事件通道也就绪，
// 进度事件能正常发到前端（`emit` 自带 nil 判断，最坏情况也只是丢一条事件）。
// **不阻塞启动** —— 只是起一个后台协程，失败也只记日志。
func (s *StockService) ServiceStartup(context.Context, application.ServiceOptions) error {
	goSafe("warmUp", s.warmUp)
	return nil
}

// warmUp 在后台把"首次加载最贵的那几块"提前取好：全量行情快照 + 板块区间基准价。
//
// 为什么要它（实测，本机数据湖）：
//
//	全量行情快照        4~6s   ← 其中"不限起点补抓"一步就要扫整个 daily_bars
//	板块区间基准价      0.6s
//	（两者之后都命中各自的缓存：快照 10 分钟、基准价 6 小时）
//
// 启动时预热掉，用户真正点进「股票列表」/「板块行情」时就不用再等这几秒。
//
// ⚠ **不改变任何数据**：热的是同一份缓存，只是把取数提前了；
// 预热失败也不影响启动（记一条日志，页面自己会再取一次并给出错误）。
func (s *StockService) warmUp() {
	started := time.Now()

	// 「按股票」日线缓存放**最后**，且用 defer 保证前面失败也照样建。
	//
	// ⚠ 顺序是有意为之：它要扫全部日分区（用户湖上是 323 个按月的大文件）、
	// 写几千个股票文件，是进程里最重的后台任务。放在最前面会与首次加载
	// **抢磁盘**，于是股票列表、组合列表、板块行情一起变慢 ——
	// 实测就是这样被反馈的（"组合列表加载很久出不来"）。
	defer s.warmSymbolBars()

	universe, err := s.GetStockUniverse(false)
	if err != nil {
		slog.Info("预热：全量行情快照未就绪（页面会自己重试）", "err", err)
		return
	}
	slog.Info("预热：全量行情快照就绪",
		"total", universe.Total, "withQuote", universe.WithQuote, "tradeDate", universe.TradeDate,
		"elapsed", time.Since(started).Round(time.Millisecond).String())

	// 板块区间基准价：**与分类无关**（按最长周期取一次交易日窗口），
	// 所以预热默认周期集合就够 —— 之后切分类、切周期都不会再取数。
	latest := make(map[string]float64, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if item.HasQuote && item.Close > 0 {
			latest[item.Code] = item.Close
		}
	}

	began := time.Now()
	if _, err := s.boardPeriodData(DefaultBoardPeriods, int(universe.TradeDate), latest); err != nil {
		slog.Info("预热：板块区间基准价未就绪（页面会自己重试）", "err", err)
		return
	}
	slog.Info("预热完成：板块行情首次加载不必再等取数",
		"periods", DefaultBoardPeriods, "elapsed", time.Since(began).Round(time.Millisecond).String())

	// 指数历史（市场温度页）只要十几毫秒，顺手热掉
	if _, _, _, err := s.loadMarketIndices(); err == nil {
		slog.Debug("预热：指数历史就绪")
	}
}

// warmSymbolBars 触发（或确认）「按股票」日线派生缓存。
//
// 它决定 K 线页是毫秒级还是"打开区间内每一个日分区"：
// 缓存就绪时一只股票一个文件；没就绪时走直读，数据越多越慢
// （cne 每多回填一年就多约 250 个日分区）。构建本身在后台跑，不阻塞这里。
func (s *StockService) warmSymbolBars() {
	provider, _ := s.snapshot()
	if provider == nil {
		return
	}
	lake, ok := provider.(*lakeProvider)
	if !ok {
		return
	}
	if lake.EnsureSymbolBars() {
		slog.Info("预热：按股票日线缓存已是最新")
		return
	}
	slog.Info("预热：按股票日线缓存正在后台重建（期间 K 线走直读，较慢）")
}
