package service

import (
	"fmt"
	"log/slog"
	"sync"

	"freestock/internal/portfolio"
)

// PortfolioService 是组合管理的对外服务。
//
// 单独一个 service（而不是往 StockService 上挂方法）是因为组合库有独立的
// 生命周期：组合数据量比配置大得多，用单独的 SQLite 文件，且按需打开 ——
// 不打开也能正常用行情/选股功能。
type PortfolioService struct {
	stock *StockService

	// dbPathOverride 非空时用它，忽略从配置推导的路径（测试注入用）
	dbPathOverride string

	mu    sync.Mutex
	store *portfolio.Store
	// opened 记录打开失败过，避免每次调用都重试失败的文件操作
	openErr error

	// ---- 回填 → 回测 的日线复用 ----
	//
	// 回填刚刚把每只股票在 `[窗口起点, 区间终点]` 的日线取回来了；
	// 紧接着跑回测时，回测要的价格区间是**它的子集** —— 实测回测 97% 的
	// 时间都花在重新取这批价上（300 只 / 4.9 秒，引擎本身只要 0.13 秒）。
	//
	// 所以把回填用过的缓存留在这里；回测若发现窗口覆盖得住就直接复用。
	// 数据源一变就不认了（比较 provider 指针）。
	cacheMu       sync.Mutex
	backfillCache *rangeCachingProvider
	cacheProvider Provider
}

// setBackfillCache 记下本次回填用的日线缓存，供紧随其后的回测复用。
func (s *PortfolioService) setBackfillCache(provider Provider, cache *rangeCachingProvider) {
	s.cacheMu.Lock()
	s.backfillCache = cache
	s.cacheProvider = provider
	s.cacheMu.Unlock()
}

// backfillCacheFor 返回能覆盖 [start, end] 的回填缓存；没有则返回 nil。
//
// provider 必须是缓存的同一个来源 —— 换了数据源就不能复用旧缓存。
func (s *PortfolioService) backfillCacheFor(provider Provider, start, end int) *rangeCachingProvider {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.backfillCache == nil || s.cacheProvider != provider {
		return nil
	}
	if start < s.backfillCache.from || end > s.backfillCache.to {
		return nil
	}
	return s.backfillCache
}

// NewPortfolioService 构造组合服务。
func NewPortfolioService(stock *StockService) *PortfolioService {
	return &PortfolioService{stock: stock}
}

// NewPortfolioServiceAt 在指定路径创建组合服务。
//
// 生产代码不用它 —— 它是为了让测试能把组合库隔离到临时目录。
// 没有这个入口时，测试只能把 stock 置空来绕过行情依赖，
// 而那样会让路径推导拿不到配置目录，最终落到**相对路径**（当前工作目录），
// 于是同一个测试进程里的多个用例串用同一个库。
func NewPortfolioServiceAt(stock *StockService, dbPath string) *PortfolioService {
	return &PortfolioService{stock: stock, dbPathOverride: dbPath}
}

// Close 关闭组合库。
func (s *PortfolioService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		_ = s.store.Close()
		s.store = nil
	}
}

// db 惰性打开组合库。
//
// 放在这里而不是启动时打开：组合库所在目录不可写（装在 Program Files 下）时，
// 不该让整个程序起不来 —— 只有用到组合功能的操作才会报错。
func (s *PortfolioService) db() (*portfolio.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		return s.store, nil
	}
	if s.openErr != nil {
		return nil, s.openErr
	}

	path := s.dbPath()
	store, err := portfolio.Open(path)
	if err != nil {
		s.openErr = fmt.Errorf("打开组合库失败（%s）：%w", path, err)
		slog.Error("打开组合库失败", "path", path, "err", err)
		return nil, s.openErr
	}
	s.store = store
	slog.Info("组合库就绪", "path", path)
	return store, nil
}

// dbPath 返回组合库路径：与 config.json 同目录。
//
// 推导不出配置目录时返回空串，由 Open 报错。
// **不能退化成相对路径** —— 那会落到进程的当前工作目录，
// 与其它实例悄悄共用一个库（这正是加 dbPathOverride 的原因）。
func (s *PortfolioService) dbPath() string {
	if s.dbPathOverride != "" {
		return s.dbPathOverride
	}
	configPath := ""
	if s.stock != nil && s.stock.cfg != nil {
		configPath = s.stock.cfg.Path()
	}
	if configPath == "" {
		return ""
	}
	return portfolio.DefaultPath(configPath)
}

// GetDatabasePath 返回组合库文件路径（界面显示用）。
func (s *PortfolioService) GetDatabasePath() string { return s.dbPath() }

// ---------------------------------------------------------------------------
// 现价来源
// ---------------------------------------------------------------------------

// LatestClose 实现 portfolio.PriceSource。
//
// **优先取"该策略最新评分日"的收盘价**（原实现 portfolio_service.py:3199），
// 与建议页同源 —— 两处取不同来源会让用户看到"详情页 +8%、系统按 +3% 判定"。
//
// 评分表里没有该股时**不返回**（调用方按 0 处理，与原实现的左连接落空一致）。
//
// 唯一的偏离：该策略**完全没有评分数据**时退回到最新交易日收盘价。
// 否则详情页在首次刷新评分之前一个价格都显示不出来（原实现会显示 0）。
func (s *PortfolioService) LatestClose(codes []string, strategyID string) (map[string]portfolio.PricePoint, error) {
	out := make(map[string]portfolio.PricePoint, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	if strategyID == "" {
		strategyID = portfolio.StrategyTrendGrowth
	}

	// 1) 评分日收盘价（首选，与建议页同源）
	if store, err := s.db(); err == nil {
		if date, err := store.LatestStrengthDate(strategyID); err == nil && date > 0 {
			rows, err := store.LoadStrengthForCodes(date, strategyID, codes)
			if err == nil {
				for code, row := range rows {
					if row.Close > 0 {
						out[code] = portfolio.PricePoint{
							Close: row.Close,
							Date:  fmt.Sprintf("%d", date),
						}
					}
				}
				if len(out) > 0 {
					return out, nil
				}
			}
		}
	}

	// 2) 兜底：该策略还没有评分数据时用最新交易日收盘价
	if s.stock == nil {
		return out, nil
	}
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil {
		return out, fmt.Errorf("取行情失败：%w", err)
	}
	want := make(map[string]bool, len(codes))
	for _, code := range codes {
		want[code] = true
	}
	for _, item := range universe.Stocks {
		if !want[item.Code] || !item.HasQuote || item.Close <= 0 {
			continue
		}
		date := ""
		if item.Date > 0 {
			date = fmt.Sprintf("%d", item.Date)
		}
		out[item.Code] = portfolio.PricePoint{Close: item.Close, Date: date}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 组合
// ---------------------------------------------------------------------------

// ListPortfolios 返回全部组合（含持仓数与层数占用）。
func (s *PortfolioService) ListPortfolios() ([]portfolio.Portfolio, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	return store.List()
}

// CreatePortfolio 新建组合。
func (s *PortfolioService) CreatePortfolio(in portfolio.PortfolioInput) (*portfolio.Portfolio, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Create(in)
	if err != nil {
		return nil, err
	}
	slog.Info("组合已创建", "id", item.ID, "name", item.Name, "type", item.Type)
	return &item, nil
}

// UpdatePortfolio 更新组合。
func (s *PortfolioService) UpdatePortfolio(id string, in portfolio.PortfolioInput) (*portfolio.Portfolio, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Update(id, in)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// DeletePortfolio 删除组合（持仓与流水级联清理）。
func (s *PortfolioService) DeletePortfolio(id string) error {
	store, err := s.db()
	if err != nil {
		return err
	}
	if err := store.Delete(id); err != nil {
		return err
	}
	slog.Info("组合已删除", "id", id)
	return nil
}

// GetPortfolioDetail 返回组合详情：持仓（含现价与盈亏）、流水、汇总。
func (s *PortfolioService) GetPortfolioDetail(id string) (*portfolio.Detail, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	detail, err := store.Detail(id, s)
	if err != nil {
		return nil, err
	}
	// ⚠ **不要在这里重排**。原实现（portfolio_service.py:745-755）直接用 SQL
	// 的排序结果，SQL 里是 `position_weight DESC, entry_date ASC, ts_code ASC`。
	// 这里再排一次会把 `entry_date` 那一维丢掉 ——
	// 同权重、不同建仓日的持仓顺序将与原版不同。
	return &detail, nil
}

// GetPortfolioOverview 返回所有组合的总览统计。
func (s *PortfolioService) GetPortfolioOverview() (*portfolio.Overview, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	overview, err := store.Overview()
	if err != nil {
		return nil, err
	}
	return &overview, nil
}

// ---------------------------------------------------------------------------
// 持仓
// ---------------------------------------------------------------------------

// AddPosition 新增持仓。
func (s *PortfolioService) AddPosition(portfolioID string, in portfolio.PositionInput) (*portfolio.Position, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}

	code := portfolio.BareCode(in.Code)
	// 名称留空时从股票列表补全（原项目界面也有这个"自动补全"行为）
	if in.Name == "" && s.stock != nil {
		if universe, err := s.stock.GetStockUniverse(false); err == nil {
			for _, item := range universe.Stocks {
				if item.Code == code {
					in.Name = item.Name
					break
				}
			}
		}
	}

	// 建仓价没给时用最新收盘价补（对应 Python `entry_price <= 0` 时取当日 close_price）。
	// 仍取不到就让 store 层报错 —— 0 成本会让浮盈保护永不触发。
	//
	// ⚠ **必须传组合自己的策略**（原实现 portfolio_service.py:514
	// `self._strength_rows_for_codes(latest_trade_date, [code], portfolio.get("strategy_id"))`）。
	// 传空串会被 LatestClose 硬编码成 trend_growth —— 那非趋势成长的组合
	// 会拿到**别的策略**评分日的收盘价，且与同组合详情页取价不同源，浮盈立刻失真。
	if in.EntryPrice <= 0 && code != "" && s.stock != nil {
		strategyID := ""
		if item, err := store.Get(portfolioID); err == nil {
			strategyID = item.StrategyID
		}
		if prices, err := s.LatestClose([]string{code}, strategyID); err == nil {
			if point, ok := prices[code]; ok && point.Close > 0 {
				in.EntryPrice = point.Close
				slog.Info("建仓价缺省，已用最新收盘价补",
					"code", code, "price", point.Close, "strategy", strategyID)
			}
		}
	}
	// 建仓日期没给时用最新交易日补（对应 Python `entry_date or latest_trade_date`），
	// 而不是"今天" —— 周末/盘前新增会算错持有交易日数
	if in.EntryDate == "" && s.stock != nil {
		if universe, err := s.stock.GetStockUniverse(false); err == nil && universe.TradeDate > 0 {
			in.EntryDate = fmt.Sprintf("%d", universe.TradeDate)
		}
	}

	position, err := store.AddPosition(portfolioID, in)
	if err != nil {
		return nil, err
	}
	slog.Info("持仓已新增",
		"portfolio", portfolioID, "code", position.Code,
		"layers", position.PositionLayers, "weight", position.PositionWeight)
	return &position, nil
}

// DeletePosition 删除持仓（会补一条 clear 流水）。
func (s *PortfolioService) DeletePosition(portfolioID, code string) error {
	store, err := s.db()
	if err != nil {
		return err
	}
	return store.DeletePosition(portfolioID, code)
}

// GetPortfolioOptions 返回界面需要的元信息（策略/类型/范围的取值清单）。
func (s *PortfolioService) GetPortfolioOptions() *PortfolioOptions {
	return &PortfolioOptions{
		Types: []PortfolioOption{
			{Key: portfolio.TypeManual, Label: "手动组合", Description: "仅生成分析提醒，不支持一键应用建议"},
			{Key: portfolio.TypeSemiAuto, Label: "半自动组合", Description: "生成建议后需人工确认再执行"},
			{Key: portfolio.TypeAuto, Label: "自动组合", Description: "可直接应用建议"},
		},
		Strategies: []PortfolioOption{
			{Key: portfolio.StrategyTrendGrowth, Label: "趋势成长"},
			{Key: portfolio.StrategyTrendLowBuy, Label: "趋势低吸"},
			{Key: portfolio.StrategyOversold, Label: "超跌反弹"},
			{Key: portfolio.StrategyExpert, Label: "高手模拟"},
		},
		Statuses: []PortfolioOption{
			{Key: portfolio.StatusActive, Label: "运行中"},
			{Key: portfolio.StatusPaused, Label: "已暂停"},
			{Key: portfolio.StatusArchived, Label: "已归档"},
		},
		ScopeTypes: []PortfolioOption{
			{Key: portfolio.ScopeAll, Label: "全市场"},
			{Key: portfolio.ScopeManualCodes, Label: "指定股票"},
			{Key: portfolio.ScopeIndustry, Label: "按行业"},
			{Key: portfolio.ScopeConcept, Label: "按概念"},
		},
		Markets: []PortfolioOption{
			{Key: "main", Label: "主板"},
			{Key: "gem", Label: "创业板"},
			{Key: "star", Label: "科创板"},
		},
		HoldingTypes: []PortfolioOption{
			{Key: "", Label: "未指定"},
			{Key: portfolio.HoldingSwing, Label: "波段仓"},
			{Key: portfolio.HoldingBase, Label: "底仓"},
		},
		DefaultBenchmark: portfolio.DefaultBenchmark,
		DefaultLayers:    portfolio.DefaultTotalLayers,
	}
}

// PortfolioOption 是一个可选值。
type PortfolioOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PortfolioOptions 是组合界面的元信息。
type PortfolioOptions struct {
	Types        []PortfolioOption `json:"types"`
	Strategies   []PortfolioOption `json:"strategies"`
	Statuses     []PortfolioOption `json:"statuses"`
	ScopeTypes   []PortfolioOption `json:"scopeTypes"`
	Markets      []PortfolioOption `json:"markets"`
	HoldingTypes []PortfolioOption `json:"holdingTypes"`
	// DefaultBenchmark 原项目默认基准（000300.SH）
	DefaultBenchmark string `json:"defaultBenchmark"`
	DefaultLayers    int    `json:"defaultLayers"`
}
