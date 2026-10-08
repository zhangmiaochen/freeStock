// Package service 暴露给前端调用的业务服务。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/market"
	"freestock/internal/stockdb"
)

const (
	// universeTTL 股票全量列表的缓存时长。
	universeTTL = 10 * time.Minute
	// boardsTTL 板块数据的缓存时长。
	boardsTTL = 30 * time.Minute
	// fetchWorkers 并发抓取行情的协程数（本机压测 64 并发约 3 秒可完成 7500 只）。
	fetchWorkers = 48
	// fallbackWorkers 补抓停牌股票的协程数。
	fallbackWorkers = 12
	// fallbackWindowDays 补抓时向前回溯的自然日天数。
	fallbackWindowDays = 45
	// progressInterval 每处理多少只股票上报一次进度。
	progressInterval = 200
)

// BoardCategoryOrder 是行业分类的展示顺序。
var BoardCategoryOrder = []string{"申万一级", "申万二级", "申万三级", "概念"}

// ---------------------------------------------------------------------------
// 前端数据结构
// ---------------------------------------------------------------------------

// DataSourceInfo 数据源信息。
type DataSourceInfo struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Current bool   `json:"current"`
}

// SourceStatus 数据源连通性检测结果。
type SourceStatus struct {
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Total   int    `json:"total"`
	Elapsed string `json:"elapsed"`
}

// MarketInfo 市场分类信息。
type MarketInfo struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Group string `json:"group"`
	Count int    `json:"count"`
}

// StockItem 股票列表中的一行。
type StockItem struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Market   string  `json:"market"`
	MarketCN string  `json:"marketCN"`
	Date     int64   `json:"date"`
	Close    float64 `json:"close"`
	PctChg   float64 `json:"pctChg"`
	Amount   float64 `json:"amount"`
	Turnover float64 `json:"turnover"`
	PeTTM    float64 `json:"peTTM"`
	PB       float64 `json:"pb"`
	TotalMV  float64 `json:"totalMV"`
	FloatMV  float64 `json:"floatMV"`
	VolRatio float64 `json:"volRatio"`
	IsST     bool    `json:"isST"`
	HasQuote bool    `json:"hasQuote"`
}

// StockUniverse 是全量股票列表快照。
type StockUniverse struct {
	SourceURL  string       `json:"sourceUrl"`
	SourceName string       `json:"sourceName"`
	TradeDate  int64        `json:"tradeDate"`
	LoadedAt   string       `json:"loadedAt"`
	Elapsed    string       `json:"elapsed"`
	Total      int          `json:"total"`
	WithQuote  int          `json:"withQuote"`
	Markets    []MarketInfo `json:"markets"`
	Stocks     []StockItem  `json:"stocks"`
}

// BoardCategoryInfo 行业分类（申万一级/二级/三级、概念）。
type BoardCategoryInfo struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BoardInfo 单个行业/概念板块。
type BoardInfo struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Type     string   `json:"type"`
	Source   string   `json:"source"`
	Count    int      `json:"count"`
	Symbols  []string `json:"symbols"`
}

// Progress 抓取进度事件负载。
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// ---------------------------------------------------------------------------
// 服务实现
// ---------------------------------------------------------------------------

// StockService 提供股票数据相关能力。
type StockService struct {
	cfg *config.Store

	clientMu sync.RWMutex
	provider Provider
	source   config.DataSource

	universeMu sync.Mutex
	universe   *StockUniverse
	universeAt time.Time

	boardsMu sync.Mutex
	boards   []stockdb.Board
	boardsAt time.Time

	// 搜索索引（代码/名称/拼音/首字母），随股票列表一起失效
	indexMu       sync.Mutex
	searchIdx     []searchEntry
	indexLoadedAt string

	// 最新交易日缓存
	tradeDateMu sync.Mutex
	tradeDate   int
	tradeDateAt time.Time

	// cnequity 数据湖（可选）：本地数据集没有财报，从这里补
	lakeMu sync.Mutex
	lake   *cnlake.Lake
}

// lake 返回 cnequity 数据湖句柄（惰性创建）。
func (s *StockService) lakeHandle() *cnlake.Lake {
	s.lakeMu.Lock()
	defer s.lakeMu.Unlock()
	if s.lake == nil {
		root := ""
		if s.cfg != nil {
			root = s.cfg.CnLakeRoot()
		}
		s.lake = cnlake.Open(root)
	}
	return s.lake
}

// NewStockService 创建服务。
func NewStockService(cfg *config.Store) *StockService {
	s := &StockService{cfg: cfg}

	kind := config.KindStockDB
	url := ""
	if cfg != nil {
		kind = cfg.DataSourceKind()
		url = cfg.DataSourceURL()
	}
	// 选的是数据湖但一个数据集都没有时退回 free-stockdb，
	// 否则界面会一片空白而用户不知道为什么
	if kind == config.KindCnLake && !s.lakeHandle().Available() {
		slog.Warn("配置的是本地数据湖，但没有找到可用的数据湖，暂时回退到 free-stockdb")
		kind, url = config.KindStockDB, ""
	}
	if kind == config.KindStockDB && url == "" {
		url = config.Presets[0].URL
	}
	s.applySource(kind, url)
	return s
}

// switchProvider 换掉行情来源并清掉由来源派生的缓存。
func (s *StockService) switchProvider(provider Provider, source config.DataSource) {
	s.clientMu.Lock()
	s.provider = provider
	s.source = source
	s.clientMu.Unlock()
	s.ClearCache()
}

// applySource 按类型装配行情来源。
func (s *StockService) applySource(kind, raw string) {
	if kind == config.KindCnLake {
		lake := s.lakeHandle()
		s.clientMu.Lock()
		s.provider = NewLakeProvider(lake)
		s.source = config.DataSource{
			Key:  "cnlake",
			Name: "本地数据湖（cnequity）",
			URL:  lake.Root(),
			Kind: config.KindCnLake,
		}
		s.clientMu.Unlock()
		return
	}
	s.applyURL(raw)
}

func (s *StockService) applyURL(raw string) {
	normalized := normalizeURL(raw)
	if normalized == "" {
		normalized = stockdb.DefaultLocalURL
	}
	s.clientMu.Lock()
	s.provider = NewStockdbProvider(stockdb.NewClient(normalized), normalized)
	s.source = describeSource(normalized)
	s.clientMu.Unlock()
}

func (s *StockService) snapshot() (Provider, config.DataSource) {
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.provider, s.source
}

func (s *StockService) emit(name string, payload any) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(name, payload)
}

// GetDataSources 返回可选的预设数据源列表。
func (s *StockService) GetDataSources() []DataSourceInfo {
	_, current := s.snapshot()

	// 数据湖预设的 URL 是当前探测到的根目录，界面上要让用户看得到
	lakeRoot := ""
	if s.cfg != nil {
		lakeRoot = s.cfg.CnLakeRoot()
	}
	detected := s.lakeHandle().Root()

	out := make([]DataSourceInfo, 0, len(config.Presets)+1)
	for _, preset := range config.Presets {
		url := preset.URL
		name := preset.Name
		if preset.Kind == config.KindCnLake {
			url = detected
			if url == "" && lakeRoot != "" {
				url = lakeRoot
			}
			if url == "" {
				name = "本地数据湖（未找到）"
			}
		}
		out = append(out, DataSourceInfo{
			Key:     preset.Key,
			Name:    name,
			URL:     url,
			Kind:    preset.Kind,
			Current: current.Key == preset.Key,
		})
	}
	if current.Key == "custom" {
		out = append(out, DataSourceInfo{
			Key:     "custom",
			Name:    "自定义数据源",
			URL:     current.URL,
			Kind:    current.Kind,
			Current: true,
		})
	}
	return out
}

// GetCurrentDataSource 返回当前生效的数据源。
func (s *StockService) GetCurrentDataSource() DataSourceInfo {
	_, current := s.snapshot()
	return DataSourceInfo{
		Key:     current.Key,
		Name:    current.Name,
		URL:     current.URL,
		Kind:    current.Kind,
		Current: true,
	}
}

// TestDataSource 探测某个来源是否可用（不改变当前数据源）。
// kind 为 config.KindCnLake 时检测数据湖；否则按 HTTP 地址探测 free-stockdb。
func (s *StockService) TestDataSource(kind, rawURL string) SourceStatus {
	if kind == config.KindCnLake {
		return s.testLake(rawURL)
	}

	candidate := normalizeURL(rawURL)
	status := SourceStatus{URL: candidate, Kind: config.KindStockDB}
	if candidate == "" {
		status.Message = "地址无效，请填写形如 http://127.0.0.1:7899 的地址"
		return status
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	total, err := stockdb.NewClient(candidate).Ping(ctx)
	status.Elapsed = time.Since(started).Round(time.Millisecond).String()
	if err != nil {
		status.Message = err.Error()
		return status
	}
	status.OK = true
	status.Total = total
	status.Message = fmt.Sprintf("连接正常，共 %d 个证券代码", total)
	return status
}

// testLake 检测本地数据湖。
func (s *StockService) testLake(rawRoot string) SourceStatus {
	root := strings.TrimSpace(rawRoot)
	if root == "" && s.cfg != nil && s.cfg.CnLakeRoot() != "" {
		root = s.cfg.CnLakeRoot()
	}
	lake := cnlake.Open(root)
	status := SourceStatus{URL: lake.Root(), Kind: config.KindCnLake}

	if !lake.Available() {
		status.Message = "没有找到数据湖（需要包含 curated 子目录）。请设置数据湖根目录，或设置环境变量 FREESTOCK_CNLAKE。"
		return status
	}

	datasets := lake.Datasets()
	status.OK = true

	// 行情类数据集决定能不能当行情源用；一个都没有就只能当财务数据源
	hasBars := lake.HasDataset("daily_bars")
	hasInstruments := lake.HasDataset("instruments")
	status.Total = len(datasets)

	switch {
	case hasBars && hasInstruments:
		status.Message = fmt.Sprintf("数据湖可用，共 %d 个数据集；行情与标的齐备，可作为行情源", len(datasets))
	case hasInstruments:
		status.Message = fmt.Sprintf("数据湖可用，共 %d 个数据集；但缺少 daily_bars（日线），暂时只能作为财务数据来源", len(datasets))
	default:
		status.Message = fmt.Sprintf("数据湖可用，共 %d 个数据集；缺少 instruments / daily_bars，暂时只能作为财务数据来源", len(datasets))
	}
	return status
}

// lakeCanServeQuotes 判断数据湖能不能当行情源用。
// 只有标的清单不够，还必须有日线，否则列表里全是没行情的空壳。
func lakeCanServeQuotes(lake *cnlake.Lake) bool {
	return lake.HasDataset("instruments") && lake.HasDataset("daily_bars")
}

// SetDataSource 切换数据源；切换前会先做一次连通性检测。
func (s *StockService) SetDataSource(kind, rawURL string) (SourceStatus, error) {
	if kind == "" {
		kind = config.KindStockDB
	}

	status := s.TestDataSource(kind, rawURL)
	if !status.OK {
		return status, fmt.Errorf("数据源不可用：%s", status.Message)
	}

	if kind == config.KindCnLake {
		// 指定了根目录就记住它
		if root := strings.TrimSpace(rawURL); root != "" && s.cfg != nil {
			s.cfg.SetCnLakeRoot(root)
		}
		s.lakeMu.Lock()
		s.lake = nil // 让 lakeHandle 按新根目录重新探测
		s.lakeMu.Unlock()

		lake := s.lakeHandle()
		if !lakeCanServeQuotes(lake) {
			// 不切换：没有日线的话列表会是一片空白，不如直接说清楚。
			// 注意财务数据不受影响 —— 它们本来就一直从数据湖读。
			return status, fmt.Errorf(
				"数据湖缺少 daily_bars（日线），无法作为行情源；" +
					"财报 / 股本结构 / 股东户数等仍会继续从数据湖读取。" +
					"请先让 cnequity 的 init 完成日线回填")
		}

		s.switchProvider(NewLakeProvider(lake), config.DataSource{
			Key:  "cnlake",
			Name: "本地数据湖（cnequity）",
			URL:  lake.Root(),
			Kind: config.KindCnLake,
		})
		if s.cfg != nil {
			s.cfg.SetDataSource(rawURL, config.KindCnLake)
		}
		slog.Info("切换数据源", "kind", config.KindCnLake, "root", lake.Root(), "datasets", status.Total)
		return status, nil
	}

	s.switchProvider(NewStockdbProvider(stockdb.NewClient(status.URL), status.URL), describeSource(status.URL))
	if s.cfg != nil {
		s.cfg.SetDataSource(status.URL, config.KindStockDB)
	}
	slog.Info("切换数据源", "kind", config.KindStockDB, "url", status.URL, "total", status.Total)
	return status, nil
}

// KlinePrefsInfo 把 K 线偏好和它的落盘位置一起返回，
// 这样界面上可以直接告诉用户「设置存在哪」。
type KlinePrefsInfo struct {
	Prefs config.KlinePrefs `json:"prefs"`
	// Saved 为 false 表示从未保存过，前端用默认值即可
	Saved      bool   `json:"saved"`
	ConfigPath string `json:"configPath"`
}

// GetKlinePrefs 读取 K 线页偏好（周期 / 复权 / 指标开关 / 指标参数 / 均线配置）。
func (s *StockService) GetKlinePrefs() KlinePrefsInfo {
	if s.cfg == nil {
		return KlinePrefsInfo{Prefs: config.DefaultKlinePrefs()}
	}
	prefs, saved := s.cfg.KlinePrefs()
	return KlinePrefsInfo{Prefs: prefs, Saved: saved, ConfigPath: s.cfg.Path()}
}

// SaveKlinePrefs 把 K 线页偏好写入配置文件，并回传落盘后的结果。
func (s *StockService) SaveKlinePrefs(prefs config.KlinePrefs) (KlinePrefsInfo, error) {
	if s.cfg == nil {
		return KlinePrefsInfo{}, fmt.Errorf("没有可用的配置文件，设置无法保存")
	}
	s.cfg.SetKlinePrefs(prefs)
	stored, _ := s.cfg.KlinePrefs()
	return KlinePrefsInfo{
		Prefs:      stored,
		Saved:      true,
		ConfigPath: s.cfg.Path(),
	}, nil
}

// GetConfigPath 返回配置文件路径，供界面展示。
func (s *StockService) GetConfigPath() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.Path()
}

// ClearCache 清空内存缓存，下次请求会重新拉取。
func (s *StockService) ClearCache() {
	s.universeMu.Lock()
	s.universe = nil
	s.universeAt = time.Time{}
	s.universeMu.Unlock()

	s.boardsMu.Lock()
	s.boards = nil
	s.boardsAt = time.Time{}
	s.boardsMu.Unlock()

	s.indexMu.Lock()
	s.searchIdx = nil
	s.indexLoadedAt = ""
	s.indexMu.Unlock()

	s.tradeDateMu.Lock()
	s.tradeDate = 0
	s.tradeDateAt = time.Time{}
	s.tradeDateMu.Unlock()
}

// GetStockUniverse 返回全量股票列表（含最新行情），refresh=true 时强制刷新。
func (s *StockService) GetStockUniverse(refresh bool) (*StockUniverse, error) {
	s.universeMu.Lock()
	defer s.universeMu.Unlock()

	if !refresh && s.universe != nil && time.Since(s.universeAt) < universeTTL {
		return s.universe, nil
	}

	started := time.Now()
	provider, source := s.snapshot()
	slog.Info("开始读取全量股票列表", "source", source.URL, "refresh", refresh)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	buckets, err := provider.StockCodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取股票代码失败：%w", err)
	}

	type target struct {
		code   string
		market string
	}
	targets := make([]target, 0, 8192)
	for _, list := range buckets {
		for _, code := range list {
			targets = append(targets, target{code: code, market: market.KeyOf(code)})
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("没有获取到任何证券代码，请确认 %s 的数据已同步", source.URL)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].code < targets[j].code })

	tradeDate, err := provider.LatestTradeDate(ctx)
	if err != nil {
		return nil, err
	}

	// ---- 第一轮：按最新交易日取点（走批量接口） ----
	s.emit("stock:progress", Progress{Phase: "quoting", Total: len(targets)})

	codes := make([]string, len(targets))
	for i, item := range targets {
		codes[i] = item.code
	}

	quotes := make([]*stockdb.Quote, len(targets))
	var (
		failed   atomic.Int64
		firstErr string
		errOnce  sync.Once
		done     atomic.Int64
	)

	byCode, bulkErr := provider.LatestQuotes(ctx, codes, tradeDate)
	if bulkErr != nil {
		slog.Warn("批量取行情失败", "err", bulkErr)
	}
	for i, code := range codes {
		if quote, ok := byCode[code]; ok && quote != nil {
			quotes[i] = quote
		}
		if n := done.Add(1); int(n)%progressInterval == 0 || int(n) == len(targets) {
			s.emit("stock:progress", Progress{Phase: "quoting", Done: int(n), Total: len(targets)})
		}
	}
	if bulkErr != nil && len(byCode) == 0 {
		failed.Add(int64(len(targets)))
		errOnce.Do(func() { firstErr = bulkErr.Error() })
	}

	if failed.Load() > 0 && failed.Load() == int64(len(targets)) {
		return nil, fmt.Errorf("获取行情失败：%s", firstErr)
	}

	// ---- 第二轮：为停牌等当日无数据的股票回溯补抓 ----
	misses := make([]int, 0, 256)
	for i, quote := range quotes {
		if quote == nil {
			misses = append(misses, i)
		}
	}
	if len(misses) > 0 {
		s.emit("stock:progress", Progress{Phase: "fallback", Total: len(misses)})
		windowStart := shiftDate(tradeDate, -fallbackWindowDays)
		var fallbackDone atomic.Int64
		runWorkers(ctx, fallbackWorkers, len(misses), func(k int) {
			idx := misses[k]
			list, err := provider.QuoteRangeDesc(ctx, targets[idx].code, windowStart, tradeDate)
			if err == nil && len(list) > 0 {
				quotes[idx] = list[0]
			}
			if n := fallbackDone.Add(1); int(n)%progressInterval == 0 || int(n) == len(misses) {
				s.emit("stock:progress", Progress{Phase: "fallback", Done: int(n), Total: len(misses)})
			}
		})
	}

	// ---- 组装结果 ----
	stocks := make([]StockItem, 0, len(targets))
	counts := make(map[string]int, len(market.Definitions))
	withQuote := 0

	for i, item := range targets {
		counts[item.market]++
		row := StockItem{
			Code:     item.code,
			Market:   item.market,
			MarketCN: market.NameOf(item.market),
			Date:     int64(tradeDate),
		}
		if quote := quotes[i]; quote != nil {
			withQuote++
			row.HasQuote = true
			row.Name = quote.Name
			if quote.Date > 0 {
				row.Date = quote.Date
			}
			row.Close = quote.Close
			row.PctChg = quote.PctChg
			row.Amount = quote.Amount
			row.Turnover = quote.Turnover
			row.PeTTM = quote.PeTTM
			row.PB = quote.PB
			row.TotalMV = quote.TotalMV
			row.FloatMV = quote.FloatMV
			row.VolRatio = quote.VolRatio
			row.IsST = quote.IsST
		}
		stocks = append(stocks, row)
	}

	markets := make([]MarketInfo, 0, len(market.Definitions))
	for _, def := range market.Definitions {
		if count := counts[def.Key]; count > 0 {
			markets = append(markets, MarketInfo{Key: def.Key, Name: def.Name, Group: def.Group, Count: count})
		}
	}

	result := &StockUniverse{
		SourceURL:  source.URL,
		SourceName: source.Name,
		TradeDate:  int64(tradeDate),
		LoadedAt:   time.Now().Format(time.RFC3339),
		Elapsed:    time.Since(started).Round(time.Millisecond).String(),
		Total:      len(targets),
		WithQuote:  withQuote,
		Markets:    markets,
		Stocks:     stocks,
	}

	s.universe = result
	s.universeAt = time.Now()

	// 顺手把最新交易日缓存起来，K 线页面就不用再查一次
	s.tradeDateMu.Lock()
	s.tradeDate = tradeDate
	s.tradeDateAt = time.Now()
	s.tradeDateMu.Unlock()

	slog.Info("股票列表就绪",
		"total", result.Total,
		"withQuote", result.WithQuote,
		"tradeDate", result.TradeDate,
		"markets", len(result.Markets),
		"elapsed", result.Elapsed,
	)
	return result, nil
}

// GetBoardCategories 返回可用的行业分类。
func (s *StockService) GetBoardCategories() ([]BoardCategoryInfo, error) {
	boards, err := s.loadBoards()
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	for _, board := range boards {
		counts[board.Category]++
	}

	out := make([]BoardCategoryInfo, 0, len(counts))
	for _, name := range BoardCategoryOrder {
		if count := counts[name]; count > 0 {
			out = append(out, BoardCategoryInfo{Key: name, Name: name, Count: count})
			delete(counts, name)
		}
	}
	if len(counts) > 0 {
		rest := make([]string, 0, len(counts))
		for name := range counts {
			rest = append(rest, name)
		}
		sort.Strings(rest)
		for _, name := range rest {
			out = append(out, BoardCategoryInfo{Key: name, Name: name, Count: counts[name]})
		}
	}
	return out, nil
}

// GetBoards 返回指定行业分类下的全部板块，category 为空时返回所有板块。
func (s *StockService) GetBoards(category string) ([]BoardInfo, error) {
	boards, err := s.loadBoards()
	if err != nil {
		return nil, err
	}

	out := make([]BoardInfo, 0, len(boards))
	for _, board := range boards {
		if category != "" && board.Category != category {
			continue
		}
		out = append(out, BoardInfo{
			Code:     board.Code,
			Name:     board.Name,
			Category: board.Category,
			Type:     board.Type,
			Source:   board.Source,
			Count:    len(board.Symbols),
			Symbols:  board.Symbols,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *StockService) loadBoards() ([]stockdb.Board, error) {
	s.boardsMu.Lock()
	defer s.boardsMu.Unlock()

	if s.boards != nil && time.Since(s.boardsAt) < boardsTTL {
		return s.boards, nil
	}

	provider, _ := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	boards, err := provider.Boards(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取板块数据失败：%w", err)
	}
	s.boards = boards
	s.boardsAt = time.Now()
	slog.Info("板块数据就绪", "boards", len(boards))
	return boards, nil
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func runWorkers(ctx context.Context, workers, total int, fn func(int)) {
	if total <= 0 {
		return
	}
	if workers > total {
		workers = total
	}
	if workers < 1 {
		workers = 1
	}

	var (
		wg   sync.WaitGroup
		next atomic.Int64
	)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= total || ctx.Err() != nil {
					return
				}
				fn(index)
			}
		}()
	}
	wg.Wait()
}

// shiftDate 在 YYYYMMDD 形式的日期上增减自然日。
func shiftDate(date, days int) int {
	parsed, err := time.Parse("20060102", strconv.Itoa(date))
	if err != nil {
		return date
	}
	shifted, err := strconv.Atoi(parsed.AddDate(0, 0, days).Format("20060102"))
	if err != nil {
		return date
	}
	return shifted
}

func normalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "http://" + trimmed
	}
	return strings.TrimRight(trimmed, "/")
}

func sameURL(a, b string) bool {
	return strings.EqualFold(normalizeURL(a), normalizeURL(b))
}

func describeSource(url string) config.DataSource {
	for _, preset := range config.Presets {
		if sameURL(preset.URL, url) {
			return preset
		}
	}
	kind := "custom"
	lower := strings.ToLower(url)
	if strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") {
		kind = "local"
	}
	return config.DataSource{Key: "custom", Name: "自定义数据源", URL: url, Kind: kind}
}
