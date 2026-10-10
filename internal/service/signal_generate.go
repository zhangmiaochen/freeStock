package service

import (
	"context"
	"fmt"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
	"freestock/internal/volprice"
)

// 量价信号的生成入口。
//
// 对应 `research_service` 里"扫描 + `_collect_signal_rows` + 落库"那一段
//（`_refresh_incremental` 的 2360-2384 与 `append_rows("signal_history", ...)`）。
//
// ⚠ **市场状态缓存必须先准备好再喂进去。** 原实现给
// `run_volume_price_backtest(market_state_cache=market_cache)` 传的是
// `build_market_state_cache(market_width, trade_dates)` 的结果；不传的话
// 回测会逐日走 `detect_stock_context_state`（个股上下文状态），
// 实测会把绝大多数信号压掉（同一批形态：带状态 151 条 → 不带状态 12 条）。
// 这里用库里已经物化好的 `market_state` 表当缓存，与 App 其它部分口径一致。

// SignalGenerateOptions 是信号生成的可选参数。
type SignalGenerateOptions struct {
	// Limit 限制处理的股票数（0 表示不限制）。按代码升序取样，便于复现。
	Limit int
	// LookbackDays 是量价特征需要的回溯天数。
	//
	// 对应 `_load_stock_bars`：趋势回踩要 420 个自然日，其余 220 ——
	// 因为 trend_pullback 的评估窗口是 120 根，且特征要 60 根预热。
	LookbackDays int
	// OnlyModelIDs 为空时跑全部模型。
	OnlyModelIDs []string
	// Progress 每处理完一批股票回调一次（可为 nil）。
	Progress func(done, total int)
}

// SignalGenerateResult 是生成结果。
type SignalGenerateResult struct {
	TradeDate int `json:"tradeDate"`
	// Codes 是实际处理的股票数
	Codes int `json:"codes"`
	// Rows 是写入的信号条数
	Rows int `json:"rows"`
	// Skipped 是日线不足 35 根而跳过的股票数
	Skipped int `json:"skipped"`
	// PerModel 是按模型统计的信号条数
	PerModel map[string]int `json:"perModel"`
	// MarketStateDays 是拿到的市场状态天数（0 表示没拿到缓存 —— 信号会显著偏少）
	MarketStateDays int `json:"marketStateDays"`
	// Elapsed 是耗时
	Elapsed time.Duration `json:"elapsed"`
}

// signalLookbackForModel 对应 `_load_stock_bars` 的
// `420 if normalize_model_id(model_id) == "trend_pullback" else 220`。
func signalLookbackForModel(modelID string) int {
	if volprice.NormalizeModelID(modelID) == volprice.ModelTrendPullback {
		return 420
	}
	return 220
}

// RefreshSignals 为给定股票集合生成量价信号并落库。
//
// `codes` 为空时取"最新评分日的全部代码"。`startDate/endDate` 为 0 时自动取：
// 起点 = 最新评分日往前 `LookbackDays`，终点 = 最新评分日。
func (s *PortfolioService) RefreshSignals(
	ctx context.Context,
	codes []string,
	startDate, endDate int,
	opts SignalGenerateOptions,
) (*SignalGenerateResult, error) {
	started := time.Now()
	if s.stock == nil {
		return nil, fmt.Errorf("缺少股票服务依赖")
	}
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return nil, fmt.Errorf("行情源不可用")
	}

	tradeDate := endDate
	if tradeDate <= 0 {
		resolved, err := provider.LatestTradeDate(ctx)
		if err != nil {
			return nil, fmt.Errorf("取最新交易日失败：%w", err)
		}
		tradeDate = resolved
	}
	if tradeDate <= 0 {
		return nil, fmt.Errorf("最新交易日为空，无法生成信号")
	}

	lookback := opts.LookbackDays
	if lookback <= 0 {
		// 取所有模型的**最大**回溯，保证每个模型都够用
		lookback = 0
		for _, def := range volprice.ModelDefinitions {
			if days := signalLookbackForModel(def.ID); days > lookback {
				lookback = days
			}
		}
	}
	if startDate <= 0 {
		step := lookback
		anchor, parseErr := time.Parse("20060102", fmt.Sprintf("%d", tradeDate))
		if parseErr != nil {
			return nil, fmt.Errorf("交易日格式异常：%d", tradeDate)
		}
		startDate = atoiDate(anchor.AddDate(0, 0, -step).Format("20060102"))
	}

	universe := codes
	names := map[string]string{}
	if len(universe) == 0 {
		rows, loadErr := store.LoadStrength(int64(tradeDate), "", 0)
		if loadErr != nil {
			return nil, fmt.Errorf("加载评分代码失败：%w", loadErr)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			if row.Code == "" || seen[row.Code] {
				continue
			}
			seen[row.Code] = true
			universe = append(universe, row.Code)
			if row.Name != "" {
				names[row.Code] = row.Name
			}
		}
	} else {
		// 显式传入代码时也要尽量补上名称，但**只查一次**全量评分
		//（原来在循环里逐只查，是 O(股票数²) 次库查询）。
		if rows, loadErr := store.LoadStrength(int64(tradeDate), "", 0); loadErr == nil {
			for _, row := range rows {
				if row.Name != "" {
					names[row.Code] = row.Name
				}
			}
		}
	}
	if len(universe) == 0 {
		return &SignalGenerateResult{TradeDate: tradeDate, PerModel: map[string]int{}, Elapsed: time.Since(started)},
			fmt.Errorf("没有可处理的股票：请先完成区间评分，或显式传入代码列表")
	}

	// ---- 市场状态缓存（对应 build_market_state_cache）----
	marketCache := map[string]volprice.MarketStateInfo{}
	stateRows, err := store.LoadMarketStateRange(int64(startDate), int64(tradeDate))
	if err == nil {
		for _, row := range stateRows {
			marketCache[fmt.Sprintf("%d", row.TradeDate)] = volprice.MarketStateInfo{
				State: row.State, Label: row.Label, Score: row.BullScore,
				Source: "market_state", Reason: "库内物化状态", TradeDate: fmt.Sprintf("%d", row.TradeDate),
			}
		}
	}

	result := &SignalGenerateResult{
		TradeDate: tradeDate, PerModel: map[string]int{},
		MarketStateDays: len(marketCache),
	}
	allRows := make([]portfolio.SignalHistoryRow, 0, 1024)
	for index, code := range universe {
		if opts.Limit > 0 && index >= opts.Limit {
			break
		}
		bars, barsErr := provider.DailyBars(ctx, code, startDate, tradeDate)
		if barsErr != nil || len(bars) < 35 {
			result.Skipped++
			continue
		}
		result.Codes++
		converted := toVolPriceBars(bars)
		name := names[code]
		if name == "" {
			name = code
		}
		rows := volprice.CollectSignalRows(converted, volprice.CollectSignalOptions{
			TsCode: code, Name: name,
			OnlyModelIDs:     opts.OnlyModelIDs,
			StartDate:        fmt.Sprintf("%d", startDate),
			EndDate:          fmt.Sprintf("%d", tradeDate),
			MarketStateCache: marketCache,
			Now:              time.Now().Format(time.RFC3339),
		})
		for _, row := range rows {
			result.PerModel[row.ModelID]++
		}
		allRows = append(allRows, toPortfolioSignalRows(code, rows)...)
		if opts.Progress != nil && (index+1)%50 == 0 {
			opts.Progress(index+1, len(universe))
		}
	}

	if len(allRows) > 0 {
		if err := store.SaveSignalHistory(allRows); err != nil {
			return result, fmt.Errorf("写入信号表失败：%w", err)
		}
	}
	result.Rows = len(allRows)
	result.Elapsed = time.Since(started)
	return result, nil
}

// SignalSummary 是信号表的概览（界面状态栏用）。
type SignalSummary struct {
	// Total 是信号总条数
	Total int `json:"total"`
	// LatestDate 是表里最大的交易日（0 表示空表）
	LatestDate int `json:"latestDate"`
	// Dates 是有信号的交易日数
	Dates int `json:"dates"`
	// Generated 标记本机是否至少跑过一次生成。
	//
	// ⚠ 这个字段要显示出来：`score.HasSignalSource` 已经打开，
	// **表为空时熊市下的入场会被全部挡掉**（与原版一致）。用户需要知道
	// "我不是没有候选，而是还没生成信号"。
	Generated bool `json:"generated"`
}

// GetSignalSummary 返回信号表概览。
func (s *PortfolioService) GetSignalSummary() (*SignalSummary, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	total, err := store.SignalHistoryCount()
	if err != nil {
		return nil, err
	}
	latest, err := store.LatestSignalDate()
	if err != nil {
		return nil, err
	}
	dates, err := store.SignalHistoryDates(0)
	if err != nil {
		return nil, err
	}
	return &SignalSummary{
		Total: total, LatestDate: int(latest), Dates: len(dates), Generated: total > 0,
	}, nil
}

// RefreshSignalsFor 是给界面/命令行用的信号生成入口（无 ctx，便于绑定）。
//
// 对应原项目研究页的"信号扫描"。**不跑它就等于信号表为空**，
// 而 `score.HasSignalSource` 已打开 —— 届时熊市下的入场会被"必须有新鲜信号"
// 的闸门全部挡掉（这是原版行为，不是缺陷）。
func (s *PortfolioService) RefreshSignalsFor(
	codes []string, startDate, endDate, limit int,
) (*SignalGenerateResult, error) {
	return s.RefreshSignals(context.Background(), codes, startDate, endDate,
		SignalGenerateOptions{Limit: limit})
}

// toVolPriceBars 把评分用的 K 线转成量价模型的 K 线。
func toVolPriceBars(bars []score.Bar) []volprice.Bar {
	out := make([]volprice.Bar, 0, len(bars))
	for _, bar := range bars {
		out = append(out, volprice.Bar{
			Date: fmt.Sprintf("%d", bar.Date),
			Open: bar.Open, High: bar.High, Low: bar.Low,
			Close: bar.Close, Volume: bar.Volume,
		})
	}
	return out
}

// toPortfolioSignalRows 把量价层的行转成组合库的行。
//
// ⚠ 代码要保持**原项目的 tushare 格式**（`600519.SH`）：
// 一方面与 Python 侧的表可直接对照，另一方面原实现那条 A 股代码白名单
// 就是按带后缀写的（见 `portfolio.SignalCodeAllowed`）。
func toPortfolioSignalRows(code string, rows []volprice.SignalHistoryRow) []portfolio.SignalHistoryRow {
	tsCode := tsCodeOf(code)
	out := make([]portfolio.SignalHistoryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, portfolio.SignalHistoryRow{
			TradeDate: row.TradeDate, Code: tsCode, Name: row.Name,
			ModelID: row.ModelID, ModelName: row.ModelName,
			MarketState: row.MarketState, MarketStateLabel: row.MarketStateLabel,
			SignalType: row.SignalType, SignalLevel: row.SignalLevel,
			SignalLevelLabel: row.SignalLevelLabel,
			SignalSubtype:    row.SignalSubtype, SignalSubtypeLabel: row.SignalSubtypeLabel,
			EntryPrice: row.EntryPrice, StopPrice: row.StopPrice, TargetPrice: row.TargetPrice,
			Score: row.Score, RiskLevel: row.RiskLevel,
			BehaviorQualityJSON: row.BehaviorQualityJSON, ReasonJSON: row.ReasonJSON,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return out
}

// tsCodeOf 把裸 6 位代码补成 tushare 代码（`600519` → `600519.SH`）。
//
// 交易所判定与原项目一致：6/9 开头沪市，其余深市。
func tsCodeOf(code string) string {
	trimmed := code
	for _, sep := range []string{".SH", ".SZ", ".BJ"} {
		if len(trimmed) > len(sep) && trimmed[len(trimmed)-len(sep):] == sep {
			return trimmed
		}
	}
	if len(trimmed) != 6 {
		return trimmed
	}
	if trimmed[0] == '6' || trimmed[0] == '9' {
		return trimmed + ".SH"
	}
	return trimmed + ".SZ"
}

// atoiDate 解析 YYYYMMDD 文本；失败返回 0。
func atoiDate(text string) int {
	value := 0
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0
		}
		value = value*10 + int(text[i]-'0')
	}
	return value
}
