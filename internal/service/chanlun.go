package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/chanlun"
	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 缠论：服务层
//
// 这一层只做三件事：
//
//  1. **取数**：把日 K（或指数日线）变成 `chanlun.Bar`，复权方式与 K 线页一致；
//  2. **周期聚合**：把日 K 聚合成周 / 月 / 年，口径与前端 `aggregate()` 完全一致
//     （ISO 周、自然月、自然年；开取首、高取最大、低取最小、收取末、量额求和）；
//  3. **编排**：按级别独立分析（绝不用大级别推小级别），再做多级别印证。
//
// 结构逻辑全部在 `internal/chanlun`，本层不写任何缠论判定。
// ---------------------------------------------------------------------------

// ChanlunRequest 是单只标的缠论分析的入参。
type ChanlunRequest struct {
	Code   string `json:"code"`
	Adjust string `json:"adjust"` // qfq / hfq / none（指数忽略）
	// Level 分析级别：day-stroke / day / week / month / year
	Level string `json:"level"`
	// TradeDate 数据截止日（YYYYMMDD），0 表示最新交易日
	TradeDate int64 `json:"tradeDate"`
	// MultiLevel 是否同时计算更高级别做印证（默认 true）
	MultiLevel bool `json:"multiLevel"`
}

// ChanlunConfigInfo 是缠论配置的读取结果。
type ChanlunConfigInfo struct {
	Config     chanlun.Config `json:"config"`
	Saved      bool           `json:"saved"`
	ConfigPath string         `json:"configPath"`
}

// GetChanlunConfig 读取缠论配置（从未保存过时返回引擎默认值）。
func (s *StockService) GetChanlunConfig() ChanlunConfigInfo {
	cfg := chanlun.DefaultConfig()
	saved := false
	path := ""
	if s.cfg != nil {
		cfg, saved = s.cfg.ChanlunPrefs()
		path = s.cfg.Path()
	}
	return ChanlunConfigInfo{Config: cfg.Sanitize(), Saved: saved, ConfigPath: path}
}

// SaveChanlunConfig 保存缠论配置并返回落盘后的结果。
func (s *StockService) SaveChanlunConfig(prefs chanlun.Config) (ChanlunConfigInfo, error) {
	if s.cfg == nil {
		return ChanlunConfigInfo{}, fmt.Errorf("没有可用的配置文件，缠论配置无法保存")
	}
	sanitized := prefs.Sanitize()
	s.cfg.SetChanlunPrefs(sanitized)
	slog.Info("缠论配置已保存",
		"minLevel", sanitized.MinLevel, "operLevel", sanitized.OperLevel,
		"scoreThreshold", sanitized.ScoreThreshold, "volumeAux", sanitized.VolumeAux)
	return ChanlunConfigInfo{Config: sanitized, Saved: true, ConfigPath: s.cfg.Path()}, nil
}

// ---------------------------------------------------------------------------
// 取数与周期聚合
// ---------------------------------------------------------------------------

// loadChanlunBars 读取一只标的的日 K（已复权），并顺带返回名称与流通市值/成交额。
func (s *StockService) loadChanlunBars(
	ctx context.Context,
	provider Provider,
	code, adjust string,
	tradeDate int,
) (bars []chanlun.Bar, name string, isIndex bool, floatMV, amount float64, err error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, "", false, 0, 0, fmt.Errorf("请先选择股票")
	}
	switch adjust {
	case "none", "qfq", "hfq":
	default:
		adjust = "qfq"
	}

	// 指数：数据湖的 index_bars（free-stockdb 没有指数表），没有复权概念
	if symbol, ok := s.resolveIndexSymbol(code); ok {
		lake := s.lakeHandle()
		if lake == nil || !lake.HasIndexBars() {
			return nil, "", true, 0, 0, fmt.Errorf(
				"指数行情只在本地数据湖的 index_bars 里，请确认数据源与数据集已就绪：%s", symbol)
		}
		rows, err := lake.IndexBars(symbol, 1990, 0)
		if err != nil {
			return nil, "", true, 0, 0, fmt.Errorf("读取指数 %s 的日线失败：%w", symbol, err)
		}
		out := make([]chanlun.Bar, 0, len(rows))
		for _, row := range rows {
			date := int(row.Date())
			if date <= 0 || row.Close <= 0 {
				continue
			}
			if tradeDate > 0 && date > tradeDate {
				continue
			}
			out = append(out, chanlun.Bar{
				Date: date, Open: row.Open, High: row.High, Low: row.Low,
				Close: row.Close, Volume: row.Volume, Amount: row.Amount,
			})
		}
		if len(out) == 0 {
			return nil, "", true, 0, 0, fmt.Errorf("数据湖里没有指数 %s 的日线", symbol)
		}
		return out, indexDisplayName(symbol), true, 0, 0, nil
	}

	quotes, err := provider.KlineHistory(ctx, code, 19900101, tradeDate)
	if err != nil {
		return nil, "", false, 0, 0, fmt.Errorf("读取日K失败：%w", err)
	}
	if len(quotes) == 0 {
		return nil, "", false, 0, 0, fmt.Errorf("没有 %s 的日K数据，请确认数据源已完成同步", code)
	}
	// 与 K 线页走**同一个复权出口**，否则叠加层的价格会和蜡烛对不上
	// （结构本身是尺度不变的，但买卖点/笔的参考价必须是同一尺度）。
	adjusted, adjustErr := provider.AdjustQuotes(ctx, code, quotes, adjust)
	if adjustErr != nil {
		slog.Warn("缠论复权折算失败，按不复权处理", "code", code, "err", adjustErr)
		adjusted = quotes
	}

	out := make([]chanlun.Bar, 0, len(adjusted))
	for _, quote := range adjusted {
		if quote.High <= 0 || quote.Low <= 0 || quote.Close <= 0 {
			continue
		}
		out = append(out, chanlun.Bar{
			Date: int(quote.Date), Open: quote.Open, High: quote.High, Low: quote.Low,
			Close: quote.Close, Volume: quote.Volume, Amount: quote.Amount,
		})
	}
	latest := quotes[len(quotes)-1]
	return out, latest.Name, false, latest.FloatMV, latest.Amount, nil
}

// chanlunLevelPeriod 返回级别对应的 K 线周期。
func chanlunLevelPeriod(level chanlun.Level) string {
	switch level {
	case chanlun.LevelWeek:
		return "week"
	case chanlun.LevelMonth:
		return "month"
	case chanlun.LevelYear:
		return "year"
	default:
		return "day"
	}
}

// aggregateChanlunBars 把日 K 聚合成周 / 月 / 年，口径与前端 `aggregate()` 一致。
func aggregateChanlunBars(bars []chanlun.Bar, period string) []chanlun.Bar {
	if period == "" || period == "day" || len(bars) == 0 {
		return bars
	}
	out := make([]chanlun.Bar, 0, len(bars)/5+1)
	var bucket chanlun.Bar
	bucketKey := ""
	for _, bar := range bars {
		key := chanlunPeriodKey(bar.Date, period)
		if bucketKey == "" {
			bucketKey = key
			bucket = bar
			continue
		}
		if key != bucketKey {
			out = append(out, bucket)
			bucketKey = key
			bucket = bar
			continue
		}
		if bar.High > bucket.High {
			bucket.High = bar.High
		}
		if bar.Low < bucket.Low {
			bucket.Low = bar.Low
		}
		bucket.Close = bar.Close
		bucket.Volume += bar.Volume
		bucket.Amount += bar.Amount
		bucket.Date = bar.Date
	}
	out = append(out, bucket)
	return out
}

// chanlunPeriodKey 返回一根 K 线所属周期的键（与前端 periodKey 同口径）。
func chanlunPeriodKey(date int, period string) string {
	year := date / 10000
	month := (date / 100) % 100
	day := date % 100
	switch period {
	case "month":
		return fmt.Sprintf("%04d-%02d", year, month)
	case "year":
		return fmt.Sprintf("%04d", year)
	case "week":
		isoYear, isoWeek := isoWeekOf(year, month, day)
		return fmt.Sprintf("%04d-W%02d", isoYear, isoWeek)
	default:
		return fmt.Sprintf("%d", date)
	}
}

// isoWeekOf 计算 ISO 年与 ISO 周（与前端 `isoWeekOf`、Python isocalendar 一致）。
func isoWeekOf(year, month, day int) (int, int) {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.ISOWeek()
}

// chanlunLevelChain 返回"从请求级别向上"的级别链（低 → 高）。
//
// 只有日 K 时，高级别只能由日 K 聚合得到；但每个级别仍然**独立**算结构。
func chanlunLevelChain(level chanlun.Level) []chanlun.Level {
	chain := make([]chanlun.Level, 0, len(chanlun.LevelOrder))
	current := level
	for current != "" {
		chain = append(chain, current)
		current = chanlun.ParentLevel(current)
	}
	return chain
}

// barsForLevel 返回某个级别要用的 K 线序列。
func barsForLevel(daily []chanlun.Bar, level chanlun.Level) []chanlun.Bar {
	return aggregateChanlunBars(daily, chanlunLevelPeriod(level))
}

// ---------------------------------------------------------------------------
// 单只标的的完整分析
// ---------------------------------------------------------------------------

// GetChanlunAnalysis 返回一只标的在指定级别的缠论标注数据包。
//
// 返回结构里同时含：
//   - `Analysis`：**请求级别**的完整标注（分型/笔/线段/中枢/走势/背驰/买卖点）；
//   - `MultiLevel`：更高级别的印证结果（共振 / 区间套 / 大级别压制）。
func (s *StockService) GetChanlunAnalysis(req ChanlunRequest) (*chanlun.Bundle, error) {
	started := time.Now()
	cfg, _ := s.chanlunConfig()

	level := chanlun.Level(strings.TrimSpace(req.Level))
	if !chanlun.ValidLevel(level) {
		level = cfg.OperLevel
	}

	provider, _ := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tradeDate, err := provider.TradeDateAt(ctx, int(req.TradeDate))
	if err != nil {
		return nil, err
	}

	daily, name, isIndex, floatMV, amount, err := s.loadChanlunBars(ctx, provider, req.Code, req.Adjust, tradeDate)
	if err != nil {
		return nil, err
	}

	info := chanlun.AdaptInfo{
		FloatMV:     floatMV,
		Amount:      amount,
		HasIntraday: false, // 本项目没有分钟级数据源
		IsIndex:     isIndex,
	}

	// 多级别印证默认开启；调用方显式关掉时只算请求级别（省掉上级别的计算）
	chain := chanlunLevelChain(level)
	if !req.MultiLevel {
		chain = chain[:1]
	}
	info.Levels = len(chain)
	info.LevelTotal = len(chanlun.LevelOrder)

	adjust := req.Adjust
	if isIndex {
		adjust = "" // 指数没有复权概念，不显示"前复权"以免误解
	}

	analyses := make([]*chanlun.Analysis, 0, len(chain))
	for _, item := range chain {
		bars := barsForLevel(daily, item)
		analysis := chanlun.Analyze(bars, item, cfg, info)
		analysis.Code = req.Code
		analysis.Name = name
		analysis.Adjust = adjust
		analyses = append(analyses, analysis)
	}

	multi := chanlun.BuildMultiLevel(analyses, cfg)
	target := analyses[0]

	bundle := &chanlun.Bundle{
		Code:       req.Code,
		Name:       name,
		Adjust:     req.Adjust,
		TradeDate:  tradeDate,
		Analysis:   target,
		Multi:      &multi,
		Disclaimer: chanlun.Disclaimer,
	}
	bundle.Notes = append(bundle.Notes, multi.Notes...)
	if isIndex {
		bundle.Notes = append(bundle.Notes,
			"指数只有价格与成交量：结构分析只用价量，没有 PE / PB / 市值 / 股本，也没有复权概念。")
	}
	if len(target.Pivots) > 0 {
		pivot := target.Pivots[len(target.Pivots)-1]
		bundle.Notes = append(bundle.Notes, fmt.Sprintf(
			"最后一个中枢：%.2f ~ %.2f（%d 笔，状态：%s）",
			pivot.ZD, pivot.ZG, pivot.StrokeCount, pivotStatusCN(pivot.Status)))
	}

	slog.Info("缠论分析完成",
		"code", req.Code, "level", level, "bars", len(daily),
		"strokes", len(target.Strokes), "pivots", len(target.Pivots),
		"signals", len(target.Signals),
		"elapsed", time.Since(started).Round(time.Millisecond).String())
	return bundle, nil
}

// ReplayChanlun 返回逐根推进的回放快照（无未来数据验证）。
func (s *StockService) ReplayChanlun(req ChanlunRequest) (*chanlun.Replay, error) {
	cfg, _ := s.chanlunConfig()

	level := chanlun.Level(strings.TrimSpace(req.Level))
	if !chanlun.ValidLevel(level) {
		level = cfg.OperLevel
	}

	provider, _ := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tradeDate, err := provider.TradeDateAt(ctx, int(req.TradeDate))
	if err != nil {
		return nil, err
	}
	daily, _, isIndex, floatMV, amount, err := s.loadChanlunBars(ctx, provider, req.Code, req.Adjust, tradeDate)
	if err != nil {
		return nil, err
	}

	info := chanlun.AdaptInfo{FloatMV: floatMV, Amount: amount, IsIndex: isIndex}
	replay := chanlun.BuildReplay(barsForLevel(daily, level), level, cfg, info)
	replay.Code = req.Code
	return replay, nil
}

// chanlunConfig 读取配置（读不到时用默认值，绝不让配置问题挡住分析）。
func (s *StockService) chanlunConfig() (chanlun.Config, bool) {
	if s.cfg == nil {
		return chanlun.DefaultConfig(), false
	}
	cfg, saved := s.cfg.ChanlunPrefs()
	return cfg.Sanitize(), saved
}

// ---------------------------------------------------------------------------
// 选股扩展：缠论筛选是**最后一层**
// ---------------------------------------------------------------------------

// ChanlunScreenCondition 是"启用缠论筛选"开关与它的筛选字段（方案 §10.2、§10.3）。
//
// `Enable=false` 时 `ScreenStocks` **一个分支都不进**，原流程与结果完全不变。
type ChanlunScreenCondition struct {
	Enable bool `json:"enable"`
	// OperLevel 操作级别：day-stroke / day / week / month / year
	OperLevel string `json:"operLevel"`
	// MinScore 综合分阈值（0 表示不限制）
	MinScore float64 `json:"minScore"`
	// Types 买卖点类型（buy1/buy2/buy3/sell1/sell2/sell3），空表示不限
	Types []string `json:"types"`
	// States 信号状态（candidate/triggered/confirmed），空表示不限
	States []string `json:"states"`
	// Trends 走势状态（consolidation/up/down/none），空表示不限
	Trends []string `json:"trends"`
	// RequirePivot 要求当前级别已有中枢
	RequirePivot bool `json:"requirePivot"`
	// RequireDivergence 要求当前级别出现背驰
	RequireDivergence bool `json:"requireDivergence"`
	// AllowCandidate 是否允许候选信号
	AllowCandidate bool `json:"allowCandidate"`
	// ConfirmedOnly 只看已确认信号
	ConfirmedOnly bool `json:"confirmedOnly"`
	// ExcludeConflict 排除存在结构矛盾的标的
	ExcludeConflict bool `json:"excludeConflict"`
	// MultiResonance 要求多级别共振
	MultiResonance bool `json:"multiResonance"`
	// MinAdaptability 股票适配性阈值（0 表示不限制）
	MinAdaptability float64 `json:"minAdaptability"`
	// LookbackDays 分析窗口（日 K 根数，默认 500）
	LookbackDays int `json:"lookbackDays"`
}

// ChanlunSignalBrief 是选股结果里的一条买卖点摘要。
type ChanlunSignalBrief struct {
	Type        string   `json:"type"`
	TypeCN      string   `json:"typeCn"`
	Direction   string   `json:"direction"`
	State       string   `json:"state"`
	StateCN     string   `json:"stateCn"`
	Level       string   `json:"level"`
	LevelCN     string   `json:"levelCn"`
	Price       float64  `json:"price"`
	Score       float64  `json:"score"`
	FormDate    int      `json:"formDate"`
	TriggerDate int      `json:"triggerDate"`
	ConfirmDate int      `json:"confirmDate"`
	InvalidDate int      `json:"invalidDate"`
	Conflict    bool     `json:"conflict"`
	Reasons     []string `json:"reasons"`
}

// ChanlunHit 是选股结果里一只股票的缠论命中信息（方案 §10.4）。
type ChanlunHit struct {
	Level      string `json:"level"`
	LevelCN    string `json:"levelCn"`
	State      string `json:"state"`
	StateCN    string `json:"stateCn"`
	Trend      string `json:"trend"`
	TrendCN    string `json:"trendCn"`
	Position   string `json:"position"`
	PositionCN string `json:"positionCn"`

	// 走势终完美：这段走势走到哪一步了、后续只有哪几种可能
	TrendStatus   string             `json:"trendStatus"`
	TrendStatusCN string             `json:"trendStatusCn"`
	CompletedDate int                `json:"completedDate"`
	Outlook       []chanlun.Scenario `json:"outlook"`

	PivotCount int     `json:"pivotCount"`
	PivotZG    float64 `json:"pivotZg"`
	PivotZD    float64 `json:"pivotZd"`

	Divergence string `json:"divergence"`
	// DivergenceOutcome 是背驰后三种可能里已经确认的那一条；未定为 pending
	DivergenceOutcome  string                      `json:"divergenceOutcome"`
	DivergenceOutcomes []chanlun.DivergenceOutcome `json:"divergenceOutcomes"`

	Signals    []ChanlunSignalBrief `json:"signals"`
	BestType   string               `json:"bestType"`
	BestTypeCN string               `json:"bestTypeCn"`
	BestState  string               `json:"bestState"`
	BestScore  float64              `json:"bestScore"`
	// BestDate 是命中信号的关键日期（确认日优先，其次触发/形成日）。
	// 表格里显示它，才知道命中的是哪一条 —— 同一个"三买确认"在不同窗口里
	// 可能是不同年份的两次结构。
	BestDate int `json:"bestDate"`

	Reasons      []string `json:"reasons"`
	Conflict     bool     `json:"conflict"`
	Adaptability float64  `json:"adaptability"`
	AdaptGrade   string   `json:"adaptGrade"`
	Resonance    bool     `json:"resonance"`
	Precision    string   `json:"precision"`
	Risk         string   `json:"risk"`
}

// applyChanlunFilter 对"已通过全部已有条件"的候选执行缠论筛选。
//
// 语义约束（方案 §10.1）：
//   - 只在所有已有条件执行完成后调用；
//   - 不改变已有条件的语义和结果，只对候选集做过滤与排序；
//   - 开关关闭时调用方根本不会走到这里。
func (s *StockService) applyChanlunFilter(
	ctx context.Context,
	provider Provider,
	passed []StockItem,
	cond ScreenCondition,
	tradeDate int,
) ([]StockItem, map[string]*ChanlunHit, []string) {
	hits := make(map[string]*ChanlunHit, len(passed))
	notes := make([]string, 0, 4)
	if !cond.Chanlun.Enable {
		return passed, hits, notes
	}
	if len(passed) == 0 {
		notes = append(notes, "缠论筛选：已有条件没有筛出任何股票，本轮未执行")
		return passed, hits, notes
	}

	cfg, _ := s.chanlunConfig()
	filter := cond.Chanlun
	filter = normalizeChanlunScreenCondition(filter, cfg)
	level := chanlun.Level(filter.OperLevel)

	// 窗口：周/月/年级别需要更长的日 K 才能形成足够的中枢
	lookback := chanlunScreenLookback(level, filter.LookbackDays)
	start := shiftBack(tradeDate, lookback*2+60)

	info := chanlun.AdaptInfo{Levels: 1, LevelTotal: len(chanlun.LevelOrder)}

	s.emit("screen:progress", Progress{Phase: "chanlun", Total: len(passed)})

	var (
		done   atomic.Int64
		mu     sync.Mutex
		kept   = make([]StockItem, 0, len(passed))
		failed int
	)
	runWorkers(ctx, fetchWorkers, len(passed), func(i int) {
		item := passed[i]
		ctxBars := info
		ctxBars.FloatMV = item.FloatMV
		ctxBars.Amount = item.Amount

		quotes, err := provider.DailyHistory(ctx, item.Code, start, tradeDate)
		var hit *ChanlunHit
		if err == nil && len(quotes) > 0 {
			daily := chanlunBarsFromQuotes(quotes)
			hit = evaluateChanlunStock(daily, level, cfg, filter, ctxBars)
		}

		mu.Lock()
		if err != nil || len(quotes) == 0 {
			failed++
		} else if hit != nil {
			kept = append(kept, item)
			hits[item.Code] = hit
		}
		n := done.Add(1)
		mu.Unlock()

		if int(n)%progressInterval == 0 || int(n) == len(passed) {
			s.emit("screen:progress", Progress{Phase: "chanlun", Done: int(n), Total: len(passed)})
		}
	})

	notes = append(notes, fmt.Sprintf(
		"缠论筛选已在全部已有条件之后执行：对 %d 只候选做%s结构分析，命中 %d 只",
		len(passed), chanlun.LevelCN(level), len(kept)))
	notes = append(notes, "缠论筛选是最后一层：它不改变任何已有条件的语义，只对候选集过滤与排序")
	notes = append(notes, fmt.Sprintf(
		"缠论分析窗口：%d ~ %d（配置 %d 个交易日，另加取数缓冲）；K 线页是全历史分析，同一只票的结构可能不同",
		start, tradeDate, lookback))
	if failed > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d 只股票没有取到日线（停牌 / 退市 / 未同步），已排除在缠论筛选之外", failed))
	}
	notes = append(notes, "选股按不复权价计算结构；除权除息日的价格跳空可能形成假分型，建议在 K 线页用复权价复核")
	notes = append(notes, "只有日 K 数据：区间套只能在日线内部做，精度有限；买卖点仅作参考，不构成投资建议")

	slog.Info("缠论筛选完成",
		"level", level, "candidates", len(passed), "matched", len(kept),
		"failed", failed)
	return kept, hits, notes
}

// evaluateChanlunStock 分析一只股票并判断是否命中筛选条件。
func evaluateChanlunStock(
	daily []chanlun.Bar,
	level chanlun.Level,
	cfg chanlun.Config,
	filter ChanlunScreenCondition,
	info chanlun.AdaptInfo,
) *ChanlunHit {
	// 筛选用的配置不裁窗口（窗口已经由取数区间决定）
	cfg.LookbackDays = 0

	bars := barsForLevel(daily, level)
	if len(bars) == 0 {
		return nil
	}
	analysis := chanlun.Analyze(bars, level, cfg, info)

	// 多级别共振：同时算上一级，并做印证
	resonance := false
	if filter.MultiResonance {
		parentLevel := chanlun.ParentLevel(level)
		if parentLevel == "" {
			return nil
		}
		parentBars := barsForLevel(daily, parentLevel)
		parentInfo := info
		parentInfo.Levels = 2
		parent := chanlun.Analyze(parentBars, parentLevel, cfg, parentInfo)
		multi := chanlun.BuildMultiLevel([]*chanlun.Analysis{analysis, parent}, cfg)
		resonance = multi.Resonance
		if !resonance {
			return nil
		}
	}

	if filter.RequirePivot && len(analysis.Pivots) == 0 {
		return nil
	}
	if filter.RequireDivergence && len(analysis.Divergences) == 0 {
		return nil
	}
	if len(filter.Trends) > 0 && !chanlunContains(filter.Trends, string(analysis.Trend.Type)) {
		return nil
	}
	if filter.MinAdaptability > 0 && analysis.Adaptability.Score < filter.MinAdaptability {
		return nil
	}

	// ---- 挑选符合条件的信号 ----
	selected := make([]chanlun.Signal, 0, len(analysis.Signals))
	conflict := false
	for _, sig := range analysis.Signals {
		// 失效/过期只用来"看历史"，不该让一只票因为三年前失效的买点被选进来；
		// 只有用户显式勾选了这些状态才纳入
		if sig.State == chanlun.StateInvalid || sig.State == chanlun.StateExpired {
			if !chanlunContains(filter.States, string(sig.State)) {
				continue
			}
		}
		if sig.Conflict {
			conflict = true
			if filter.ExcludeConflict {
				continue
			}
		}
		if !filter.AllowCandidate && sig.State != chanlun.StateConfirmed && sig.State != chanlun.StateTriggered {
			continue
		}
		if filter.ConfirmedOnly && sig.State != chanlun.StateConfirmed {
			continue
		}
		if len(filter.Types) > 0 && !chanlunContains(filter.Types, string(sig.Type)) {
			continue
		}
		if len(filter.States) > 0 && !chanlunContains(filter.States, string(sig.State)) {
			continue
		}
		selected = append(selected, sig)
	}

	// 要求指定类型/状态时必须命中；否则只按走势状态/中枢/背驰也能命中
	if (len(filter.Types) > 0 || len(filter.States) > 0) && len(selected) == 0 {
		return nil
	}
	if filter.ExcludeConflict && conflict {
		return nil
	}

	best := chanlun.Signal{}
	for _, sig := range selected {
		if sig.Score > best.Score {
			best = sig
		}
	}
	if filter.MinScore > 0 {
		if best.Score < filter.MinScore {
			return nil
		}
	}

	hit := &ChanlunHit{
		Level:         string(analysis.Level),
		LevelCN:       analysis.LevelCN,
		State:         string(analysis.State.Name),
		StateCN:       analysis.State.NameCN,
		Trend:         string(analysis.Trend.Type),
		TrendCN:       analysis.Trend.TypeCN,
		Position:      string(analysis.Position.Phase),
		PositionCN:    analysis.Position.PhaseCN,
		TrendStatus:   string(analysis.Trend.Status),
		TrendStatusCN: analysis.Trend.StatusCN,
		CompletedDate: analysis.Trend.CompletedDate,
		Outlook:       analysis.Trend.Outlook,
		PivotCount:    len(analysis.Pivots),
		Divergence:    summarizeDivergence(analysis.Divergences),
		Conflict:      conflict,
		Adaptability:  analysis.Adaptability.Score,
		AdaptGrade:    analysis.Adaptability.Grade,
		Resonance:     resonance,
		Precision:     analysis.Data.PrecisionNote,
		Risk:          chanlun.Disclaimer,
	}
	if len(analysis.Divergences) > 0 {
		latest := analysis.Divergences[len(analysis.Divergences)-1]
		hit.DivergenceOutcome = latest.OutcomeCN
		hit.DivergenceOutcomes = latest.Outcomes
	}
	if len(analysis.Pivots) > 0 {
		pivot := analysis.Pivots[len(analysis.Pivots)-1]
		hit.PivotZG = pivot.ZG
		hit.PivotZD = pivot.ZD
	}
	hit.Signals = make([]ChanlunSignalBrief, 0, len(selected))
	for _, sig := range selected {
		hit.Signals = append(hit.Signals, ChanlunSignalBrief{
			Type: string(sig.Type), TypeCN: SignalTypeCNForService(sig.Type),
			Direction: sig.Direction, State: string(sig.State), StateCN: sig.StateCN,
			Level: string(sig.Level), LevelCN: chanlun.LevelCN(sig.Level),
			Price: sig.Price, Score: sig.Score,
			FormDate: sig.FormDate, TriggerDate: sig.TriggerDate,
			ConfirmDate: sig.ConfirmDate, InvalidDate: sig.InvalidDate,
			Conflict: sig.Conflict, Reasons: sig.Reasons,
		})
	}
	if len(selected) > 0 {
		hit.BestType = string(best.Type)
		hit.BestTypeCN = SignalTypeCNForService(best.Type)
		hit.BestState = string(best.State)
		hit.BestScore = best.Score
		hit.BestDate = best.ConfirmDate
		if hit.BestDate == 0 {
			hit.BestDate = best.TriggerDate
		}
		if hit.BestDate == 0 {
			hit.BestDate = best.FormDate
		}
	}

	hit.Reasons = buildChanlunHitReasons(analysis, best, resonance)
	return hit
}

// buildChanlunHitReasons 生成"它为什么被选出来"的命中原因。
func buildChanlunHitReasons(analysis *chanlun.Analysis, best chanlun.Signal, resonance bool) []string {
	reasons := make([]string, 0, 8)
	reasons = append(reasons, "当前状态："+analysis.State.NameCN+"；走势类型："+analysis.Trend.TypeCN)

	// 走势终完美：走到哪一步、之后只有哪几种可能
	lifecycle := "走势生命周期：" + analysis.Trend.StatusCN
	if analysis.Trend.CompletedDate > 0 {
		lifecycle += fmt.Sprintf("（%s，%d）", analysis.Trend.CompletionReason, analysis.Trend.CompletedDate)
	}
	reasons = append(reasons, lifecycle)
	if len(analysis.Trend.Outlook) > 0 {
		parts := make([]string, 0, len(analysis.Trend.Outlook))
		for _, scenario := range analysis.Trend.Outlook {
			parts = append(parts, scenario.Label+"["+scenario.StatusCN+"]")
		}
		reasons = append(reasons, "完全分类："+strings.Join(parts, " / "))
	}

	if len(analysis.Pivots) > 0 {
		pivot := analysis.Pivots[len(analysis.Pivots)-1]
		reasons = append(reasons, fmt.Sprintf(
			"中枢：%.2f ~ %.2f（共 %d 个，位置：%s）",
			pivot.ZD, pivot.ZG, len(analysis.Pivots), analysis.Position.PhaseCN))
	} else {
		reasons = append(reasons, "中枢：当前级别尚未形成中枢")
	}
	for _, div := range analysis.Divergences {
		direction := "顶背驰"
		if div.Direction == "bottom" {
			direction = "底背驰"
		}
		reasons = append(reasons,
			fmt.Sprintf("%s：%s，力度比 %.2f（后段/前段）→ 背驰后判定：%s",
				div.KindCN, direction, div.Ratio, div.OutcomeCN))
	}
	if best.Type != "" {
		when := ""
		switch {
		case best.ConfirmDate > 0:
			when = fmt.Sprintf("（确认 %d）", best.ConfirmDate)
		case best.TriggerDate > 0:
			when = fmt.Sprintf("（触发 %d）", best.TriggerDate)
		case best.FormDate > 0:
			when = fmt.Sprintf("（形成 %d）", best.FormDate)
		}
		reasons = append(reasons, fmt.Sprintf("信号：%s %s%s，综合分 %.1f",
			SignalTypeCNForService(best.Type), best.StateCN, when, best.Score))
	}
	if resonance {
		reasons = append(reasons, "多级别共振：更高级别方向与它一致")
	}
	reasons = append(reasons, fmt.Sprintf("股票适配性：%s（%.1f）",
		analysis.Adaptability.Grade, analysis.Adaptability.Score))
	// 选股与 K 线页的口径差异：不写清楚，用户会拿两个不同窗口的结论互相打架
	reasons = append(reasons,
		"口径：选股按配置的分析窗口计算，K 线页是全历史；同一只票的当前结构可能不同")
	return reasons
}

// summarizeDivergence 把背驰压成一句话（带背驰后的判定）。
func summarizeDivergence(divs []chanlun.Divergence) string {
	if len(divs) == 0 {
		return ""
	}
	div := divs[len(divs)-1]
	direction := "顶背驰"
	if div.Direction == "bottom" {
		direction = "底背驰"
	}
	return fmt.Sprintf("%s %s 力度比 %.2f → %s", div.KindCN, direction, div.Ratio, div.OutcomeCN)
}

// normalizeChanlunScreenCondition 补默认值（级别、窗口、允许候选）。
func normalizeChanlunScreenCondition(
	filter ChanlunScreenCondition,
	cfg chanlun.Config,
) ChanlunScreenCondition {
	level := chanlun.Level(filter.OperLevel)
	if !chanlun.ValidLevel(level) {
		level = cfg.OperLevel
	}
	filter.OperLevel = string(level)
	if filter.LookbackDays <= 0 {
		filter.LookbackDays = 500
	}
	if filter.MinScore < 0 {
		filter.MinScore = 0
	}
	if filter.MinAdaptability < 0 {
		filter.MinAdaptability = 0
	}
	// 没显式要求"只看确认"时，候选信号默认允许（与方案 §10.2 的开关一致）
	if !filter.ConfirmedOnly && !filter.AllowCandidate {
		filter.AllowCandidate = true
	}
	return filter
}

// chanlunScreenLookback 返回筛选需要取多少根日 K。
func chanlunScreenLookback(level chanlun.Level, configured int) int {
	if configured <= 0 {
		configured = 500
	}
	switch level {
	case chanlun.LevelWeek:
		if configured < 800 {
			configured = 800
		}
	case chanlun.LevelMonth, chanlun.LevelYear:
		if configured < 1600 {
			configured = 1600
		}
	}
	return configured
}

// chanlunBarsFromQuotes 把行情转换成引擎输入。
func chanlunBarsFromQuotes(quotes []*stockdb.Quote) []chanlun.Bar {
	out := make([]chanlun.Bar, 0, len(quotes))
	for _, quote := range quotes {
		if quote == nil || quote.Close <= 0 {
			continue
		}
		out = append(out, chanlun.Bar{
			Date: int(quote.Date), Open: quote.Open, High: quote.High, Low: quote.Low,
			Close: quote.Close, Volume: quote.Volume, Amount: quote.Amount,
		})
	}
	return out
}

// chanlunContains 判断字符串切片是否包含某个值。
func chanlunContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// sortScreenItemsByChanlun 按缠论综合分降序（同分按代码升序）。
func sortScreenItemsByChanlun(items []ScreenItem, hits map[string]*ChanlunHit) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := hits[items[i].Code], hits[items[j].Code]
		scoreA, scoreB := 0.0, 0.0
		if a != nil {
			scoreA = a.BestScore
		}
		if b != nil {
			scoreB = b.BestScore
		}
		if scoreA != scoreB {
			return scoreA > scoreB
		}
		return items[i].Code < items[j].Code
	})
}

// SignalTypeCNForService 是 chanlun.SignalTypeCN 的导出包装。
//
// 引擎里的函数名是 `SignalTypeCN`，但它在 chanlun 包内；服务层做一层薄包装，
// 避免在多个文件里重复写映射表。
func SignalTypeCNForService(t chanlun.SignalType) string {
	return chanlun.SignalTypeCN(t)
}

// pivotStatusCN 把中枢状态翻成中文。
func pivotStatusCN(status chanlun.PivotStatus) string {
	switch status {
	case chanlun.PivotForming:
		return "形成"
	case chanlun.PivotExtension:
		return "延伸"
	case chanlun.PivotExpanded:
		return "扩展"
	case chanlun.PivotNew:
		return "新生"
	}
	return string(status)
}
