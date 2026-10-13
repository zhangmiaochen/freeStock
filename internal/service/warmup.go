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
	go s.warmUp()
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

	// 「按股票」日线派生缓存：K 线页与股票列表的"不限起点补抓"都靠它。
	//
	// 第一次要在后台重排全市场 700 多万行（本机约十几秒），放在最后做：
	// 股票列表是首页，先让它热起来；这一步跑完之前打开 K 线会退化成
	// "只读这一只股票"的直读（3.97s），比原来灌全市场（11.9s）已经快得多。
	if provider, _ := s.snapshot(); provider != nil {
		if lake, ok := provider.(*lakeProvider); ok {
			if lake.EnsureSymbolBars() {
				slog.Info("预热：按股票日线缓存已是最新")
			} else {
				slog.Info("预热：按股票日线缓存正在后台重建（期间 K 线走直读）")
			}
		}
	}
}
