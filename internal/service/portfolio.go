package service

import (
	"fmt"
	"log/slog"
	"sort"
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

// LatestClose 实现 portfolio.PriceSource：返回这批股票的最新收盘价。
//
// 原项目是从"最新评分日"的 stock_strength_history.close_price 取现价。
// 本项目阶段一没有评分表，改为取**最新交易日的日 K 收盘价** ——
// 语义一致（都是最近一个有数据的交易日的收盘价）。
//
// 走股票列表的缓存，所以不会为几只持仓单独发请求。
func (s *PortfolioService) LatestClose(codes []string) (map[string]portfolio.PricePoint, error) {
	out := make(map[string]portfolio.PricePoint, len(codes))
	if len(codes) == 0 || s.stock == nil {
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
	sort.SliceStable(detail.Positions, func(i, j int) bool {
		if detail.Positions[i].PositionWeight != detail.Positions[j].PositionWeight {
			return detail.Positions[i].PositionWeight > detail.Positions[j].PositionWeight
		}
		return detail.Positions[i].Code < detail.Positions[j].Code
	})
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
	// 名称留空时从股票列表补全（原项目界面也有这个"自动补全"行为）
	if in.Name == "" && s.stock != nil {
		if universe, err := s.stock.GetStockUniverse(false); err == nil {
			code := portfolio.BareCode(in.Code)
			for _, item := range universe.Stocks {
				if item.Code == code {
					in.Name = item.Name
					break
				}
			}
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
