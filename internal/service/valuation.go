package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// ---------------------------------------------------------------------------
// 「A 股多期限性价比评估」服务层
//
// 职责边界（与既有服务的约定一致）：
//
//   - **取数**：全部走 `internal/cnlake`（数据湖直读），不用 Provider。
//     理由与 finance.go 相同：财报 / 估值 / 行业归属 / 复权因子**始终来自数据湖**，
//     与当前选中的行情源无关（见 provider.go 顶部的说明）。
//   - **算分**：全部在 `internal/valuation` 里做纯计算。服务层只负责组装输入、
//     缓存与对外暴露，这样数值口径可以被单测脱离文件系统钉住。
//   - **"不可用"用字段表达，不用 error**：与 FinanceSnapshot / BoardPeriodWindow
//     同一套约定（Available + Message）。
//
// ## 复权（必须写清楚，否则最容易错）
//
// 数据湖 `daily_bars` 是**不复权**行情。本项目在这里的两条取值路径刻意分开：
//
//	收益 / 波动 / 回撤 / 情景收益  → 后复权价（不复权价 × F(t)）
//	市值 / PE / PB / PS            → 不复权价 × 当期股本
//
// 所以下面的组装同时填 `Close`（原始）与 `AdjFactor`（因子），
// 由 valuation 包的两个方法分别取用，绝不共用一个数组。
// ---------------------------------------------------------------------------

// valuationTTL 是评估结果的缓存存活时间。
//
// 取 30 分钟的理由：一次全市场评估要读上千个分区文件（数秒到数十秒），
// 而输入（日线 + 财报 + 股本）在盘后不再变化 —— 所以按"数据会不会变"定，
// 不按"用户会不会连着点两次"。数据源切换与 cne 更新会主动作废它。
const valuationTTL = 30 * time.Minute

// valuationPriceStart 是价格序列起点（YYYYMMDD）。
//
// 为什么是 2021-01-01 而不是数据湖最早的 2016：
//
//	① 历史估值分位最长窗口是 5 年（约 1260 个交易日），2021 起正好够 5 年 + 缓冲；
//	② 数据湖按交易日分区，2016 起是 2616 个文件、2021 起约 1400 个 ——
//	   少读 1200 个文件的 footer，首刷快一大截；
//	③ 2016~2021 每个交易日只有约 200 只标的（实测），那段稀疏历史放进
//	   全市场分位里会系统性失真。
const valuationPriceStart = 20210101

// valuationMinBars 是一只标的参与评估所需的最少日线根数。
//
// 取 252（一年）：不足一年的标的算不出可信的 3 年估值分位与年化波动率，
// 强行给评分只会得到一堆噪声。
const valuationMinBars = 252

// maxValuationIndustries 是行业聚合的上限（防御异常数据）。
const maxValuationIndustries = 500

// valuationCacheEntry 是一次评估结果的缓存条目。
//
// 用 `gen`（universeGen）而不是时间戳判断"数据源是否换了" —— 与
// board_period.go / market_temperature.go 同一套做法。
type valuationCacheEntry struct {
	gen     int64
	limit   int
	builtAt time.Time
	report  *ValuationReport
}

// valuationUniverseEntry 是"全市场行业基准"的缓存条目。
//
// 单独缓存的理由：行业基准只依赖 (评估日, 数据源代次)，与 `limit` 无关。
// 少了它，用户在界面上从"前 150 只"切到"前 80 只"会把全市场那一遍重算一次。
//
// 缓存里同时留着按代码索引的快照（重建行业历史序列要用）与按行业分好的代码表。
type valuationUniverseEntry struct {
	gen      int64
	evalDate int64
	byCode   map[string]companySnapshot
	groups   map[string][]string
}

// ---------------------------------------------------------------------------
// 对外结果模型
// ---------------------------------------------------------------------------

// ValuationReport 是全市场评估结果。
type ValuationReport struct {
	// Available 表示数据湖可用且评估成功。
	Available bool `json:"available"`
	// Message 在不可用时说明原因与下一步。
	Message string `json:"message,omitempty"`

	// Snapshot 是溯源信息（评估日 / 数据截止 / 模型版本 / 覆盖数）。
	Snapshot valuation.Snapshot `json:"snapshot"`

	// Rankings 是五档期限各自的排名。
	Rankings []valuation.HorizonRanking `json:"rankings"`

	// Industries 是行业评估结果。
	Industries []valuation.IndustryValuation `json:"industries"`

	// Indices 是指数评估结果。
	Indices []valuation.IndexEvaluation `json:"indices"`

	// Distributions 是各期限的分数分布（键为期限 Key）。
	Distributions map[string]valuation.ScoreDistribution `json:"distributions"`

	// IndustryDistribution 是"各行业有多少标的进入全市场前 20%"。
	IndustryDistribution []IndustryDistributionRow `json:"industryDistribution"`

	// ElapsedMS 是本次评估耗时（毫秒）。
	ElapsedMS int64 `json:"elapsedMs"`
	// FullMarket 表示本次评估是否覆盖全市场（false 表示按只数上限截取）。
	FullMarket bool `json:"fullMarket"`
	// Requested 是请求的标的数上限（0 = 全市场）。
	Requested int `json:"requested"`
	// PhaseTimings 是各阶段耗时（性能诊断与界面说明用）。
	PhaseTimings []PhaseTiming `json:"phaseTimings"`
	// Notes 是口径说明（界面底部必须显示）。
	Notes []string `json:"notes"`
}

// PhaseTiming 是一个阶段的耗时。
type PhaseTiming struct {
	Name  string `json:"name"`
	MS    int64  `json:"ms"`
	Total int64  `json:"totalMs"`
}

// phaseTimer 累加各阶段耗时；Start 之后每次 Mark 记录一段。
type phaseTimer struct {
	start  time.Time
	last   time.Time
	total  int64
	stages []PhaseTiming
}

func newPhaseTimer() *phaseTimer {
	now := time.Now()
	return &phaseTimer{start: now, last: now}
}

func (t *phaseTimer) mark(name string) {
	now := time.Now()
	stages := PhaseTiming{Name: name, MS: now.Sub(t.last).Milliseconds()}
	t.total += stages.MS
	stages.Total = t.total
	t.stages = append(t.stages, stages)
	t.last = now
}

func (t *phaseTimer) phases() []PhaseTiming { return t.stages }

// IndustryDistributionRow 是总览页的行业分布行。
type IndustryDistributionRow struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Members 是参与评估的标的数。
	Members int `json:"members"`
	// TopCounts 是各期限进入全市场前 20% 的数量（键为期限 Key）。
	TopCounts map[string]int `json:"topCounts"`
	// AverageScore 是各期限的平均分。
	AverageScore map[string]float64 `json:"averageScore"`
}

// StockValuation 是单只标的的完整评估。
type StockValuation struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`

	Stock valuation.StockScore `json:"stock"`

	Snapshot valuation.Snapshot `json:"snapshot"`
	Notes    []string           `json:"notes"`
}

// IndustryValuationDetail 是行业详情。
type IndustryValuationDetail struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`

	Industry valuation.IndustryValuation `json:"industry"`
	// Members 是该行业参与评估的标的（按 12 个月综合分降序，最多 200 条）。
	Members  []valuation.RankRow `json:"members"`
	Snapshot valuation.Snapshot  `json:"snapshot"`
	Notes    []string            `json:"notes"`
}

// IndexValuationResult 是指数评估的对外结果。
type IndexValuationResult struct {
	Available bool                      `json:"available"`
	Message   string                    `json:"message,omitempty"`
	Index     valuation.IndexEvaluation `json:"index"`
	Snapshot  valuation.Snapshot        `json:"snapshot"`
	Notes     []string                  `json:"notes"`
}

// ValuationIndexOption 是指数下拉选项。
type ValuationIndexOption struct {
	Symbol          string `json:"symbol"`
	Name            string `json:"name"`
	HasConstituents bool   `json:"hasConstituents"`
	Constituents    int    `json:"constituents"`
	Note            string `json:"note,omitempty"`
}

// ValuationParamsInfo 是当前生效的模型参数（供界面解释"为什么长期更看重成长"）。
type ValuationParamsInfo struct {
	ModelVersion     string                                          `json:"modelVersion"`
	Horizons         []valuation.Horizon                             `json:"horizons"`
	HorizonWeights   map[string]map[valuation.ScoreDimension]float64 `json:"horizonWeights"`
	ScenarioProb     map[string][3]float64                           `json:"scenarioProb"`
	Wacc             float64                                         `json:"wacc"`
	TerminalGrowth   float64                                         `json:"terminalGrowth"`
	ValueStartDate   int64                                           `json:"valueStartDate"`
	RatingThresholds valuation.RatingThresholds                      `json:"ratingThresholds"`
	Notes            []string                                        `json:"notes"`
}

// ---------------------------------------------------------------------------
// 组装用的中间结构
// ---------------------------------------------------------------------------

// valuationDataset 是一次评估所需的全部原始数据（评估期间只读）。
type valuationDataset struct {
	lake *cnlake.Lake

	bars       map[string][]cnlake.BarRow
	adjPoints  map[string][]cnlake.AdjFactorPoint
	shares     map[string]cnlake.ShareTimeline
	reports    map[string][]valuation.Report
	names      map[string]string
	assetType  map[string]string
	industries map[string]industryTag

	// dividendPaid 是近 12 个月每股派息合计。
	dividendPaid map[string]float64
	// focus 非空时表示"这是详情页在为某一只标的加载数据"，
	// 只用于统计口径（skipped 只算关注的那一只），**不做数据过滤**。
	// 理由见 loadValuationDataset 的说明。
	focus map[string]bool

	// industrySystem 是本次评估实际采用的行业分类体系（sw / eastmoney）。
	// industryExcluded 列出了其它体系及"为什么没用"，两者都进溯源信息。
	industrySystem   string
	industryExcluded []string

	evalDate      int64
	latestBarDate int64
	skipped       int
}

// industryTag 是一只标的的行业归属。
//
// Code 带**体系前缀**（如 "sw:340501"），因为本机的 industry_members 是
// eastmoney 与申万两套体系混存的，两边的数字代码会撞车（"475" 在两边是两个
// 完全不同的行业）。前缀让行业聚合天然成为 (体系, 代码) 的命名空间，
// 不需要额外的防护逻辑。
type industryTag struct {
	// System 是所属分类体系（sw / eastmoney / ...）。
	System string
	// Code 是带体系前缀的行业代码，**只用于分组与排名的标识**。
	Code string
	// Name 是展示名（体系 + 代码，或体系给的可读名称）。
	Name string
}

// industryCodeOf 从带前缀的行业代码里取出**不含前缀**的原始代码。
//
// 用于给界面显示（"sw:340501" 显示成 "340501"），以及调用方按裸代码查行业。
func industryCodeOf(prefixed string) string {
	if idx := strings.IndexByte(prefixed, ':'); idx >= 0 {
		return prefixed[idx+1:]
	}
	return prefixed
}

// industryDisplayName 构造行业的展示名。
//
// 为什么需要它：申万回填那一路的 `industry_name` **就是代码本身**
// （申万那个 xls 只有代码列，cne 把 name 填成了 code），直接显示会是
// "340501" 这种没有任何可读性的东西。所以这里：
//
//   - 名称与代码实质相同（申万）→ 用「申万 340501」这种体系+代码的写法；
//   - 名称是可读的（eastmoney"白酒Ⅲ"）→ 原样使用。
func industryDisplayName(system, code, name string) string {
	if sameIndustryText(name, code) {
		return industrySystemLabel(system) + " " + code
	}
	return name
}

// industrySystemLabel 把体系标识翻成中文。
func industrySystemLabel(system string) string {
	switch strings.ToLower(strings.TrimSpace(system)) {
	case "sw":
		return "申万"
	case "eastmoney":
		return "东财"
	case "":
		return "行业"
	}
	return system
}

// sameIndustryText 判断名称是否只是把代码抄了一遍（忽略前导零与大小写）。
func sameIndustryText(name, code string) bool {
	trim := func(s string) string { return strings.TrimLeft(strings.TrimSpace(s), "0") }
	a, b := trim(name), trim(code)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// stockEvaluation 是单只标的的评估中间结果。
type stockEvaluation struct {
	code string
	name string

	company valuation.CompanyValuation
	series  valuation.PriceSeries
	shares  cnlake.ShareTimeline
	asOf    valuation.AsOf
	growth  valuation.Growth
	quality valuation.Quality
	self    valuation.SelfValuation
	// fair 是各期限的合理价值（全市场评估时只填 12 个月）。
	fair map[string]valuation.FairValue
	// confidence 是估值置信度（12 个月口径，作为整体置信度的代表）。
	confidence valuation.Confidence
	volatility float64
}

// ---------------------------------------------------------------------------
// Wails 方法
// ---------------------------------------------------------------------------

// GetValuationReport 返回全市场多期限性价比评估。
//
// limit > 0 时只评估市值最大的前 limit 只（首刷用），0 表示全市场。
// 结果按 (评估日, 数据源代次, limit) 缓存 30 分钟；refresh=true 强制重算。
//
// ⚠ limit **只影响"评估多少只"**，不影响"行业基准怎么算"：
// 行业基准永远用全市场候选算（见 buildValuationReport）。
func (s *StockService) GetValuationReport(limit int, refresh bool) (*ValuationReport, error) {
	started := time.Now()
	lake := s.lakeHandle()
	if lake == nil || !lake.Available() {
		return unavailableValuationReport(
			"未找到 cnequity 数据湖。本功能需要财报、股本、行业归属与复权因子，" +
				"这些只在数据湖里；请在「数据源设置」里指定数据湖根目录（curated 的上一级），" +
				"或设置环境变量 FREESTOCK_CNLAKE。"), nil
	}
	for _, dataset := range []string{"daily_bars", "financial_statement_items", "share_structure"} {
		if !lake.HasDataset(dataset) {
			return unavailableValuationReport(fmt.Sprintf(
				"数据湖缺少必需数据集 %s（本功能需要 daily_bars / financial_statement_items / "+
					"share_structure 三个数据集）；可在「数据源设置」页查看各数据集的入库情况。", dataset)), nil
		}
	}
	if limit < 0 {
		limit = 0
	}

	gen := s.universeGen.Load()
	if !refresh {
		if cached := s.cachedValuationReport(gen, limit); cached != nil {
			return cached, nil
		}
	}

	params := valuation.DefaultParams()
	report, err := s.buildValuationReport(lake, params, limit)
	if err != nil {
		return nil, err
	}
	report.ElapsedMS = time.Since(started).Milliseconds()
	s.storeValuationReport(gen, limit, report)
	return report, nil
}

// GetStockValuation 返回单只标的的完整评估（按需计算，不等全市场评估）。
//
// 代价是**拿不到排名**：排名需要横截面。行业与同行的比较则是完整的 ——
// 虽然只评估这一只，但行情/财报/股本都是整段读进来的，行业成分不缺
// （见 loadValuationDataset 的说明）。
func (s *StockService) GetStockValuation(code string) (*StockValuation, error) {
	lake := s.lakeHandle()
	if lake == nil || !lake.Available() {
		return unavailableStockValuation(
			"未找到 cnequity 数据湖，无法评估该标的；请先在「数据源设置」里配置数据湖。"), nil
	}
	bare := cnlake.BareSymbol(strings.TrimSpace(code))
	if bare == "" {
		return unavailableStockValuation("证券代码为空"), nil
	}

	params := valuation.DefaultParams()
	data, err := s.loadValuationDataset(lake, []string{bare})
	if err != nil {
		return nil, err
	}
	eval, ok := s.evaluateOneStock(data, bare, params, true)
	if !ok {
		return unavailableStockValuation(fmt.Sprintf(
			"%s 的数据不足以评估：可能是行情不足 %d 根（新股 / 长期停牌），或财报科目缺失。"+
				"数据不足时本功能不会强行给评级。", bare, valuationMinBars)), nil
	}

	// 该标的所属行业的聚合（遍历全市场同行业成分，见 aggregateIndustryOf）
	industry, members := s.aggregateIndustryOf(data, eval, params)
	peers, peerNote := valuation.SelectComparables(eval.company, members, params)
	relative := valuation.EvaluateRelativeValuation(eval.company, industry, members, peers, params)
	relative.ComparableNote = peerNote

	// 用行业环境重算合理价值，再评分
	eval.fair = s.applyIndustry(eval, industry, params)
	sharePoint, _ := eval.shares.Latest()

	score, scoreErr := valuation.EvaluateScore(valuation.ScoreInput{
		Code:          eval.code,
		Name:          eval.name,
		Industry:      industry,
		Self:          eval.self,
		Relative:      relative,
		Growth:        eval.growth,
		Quality:       eval.quality,
		AsOf:          eval.asOf,
		Company:       eval.company,
		Price:         eval.series.LastClose(),
		Shares:        sharePoint.TotalShares,
		Volatility:    eval.volatility,
		PaidLast12M:   data.dividendPaid[bare],
		PriceLength:   eval.series.Len(),
		PriceAdjusted: eval.series.Adjusted,
		Confidence:    eval.confidence,
		FairByHorizon: eval.fair,
		Params:        params,
	})
	if scoreErr != nil {
		return nil, scoreErr
	}
	out := &StockValuation{
		Available: score.Available,
		Message:   score.UnavailableReason,
		Stock:     score,
		Snapshot:  data.snapshot(),
		Notes:     valuationNotes(),
	}
	if !industry.Available {
		out.Notes = append(out.Notes, "行业与同行比较的可信度下降："+industry.Note)
	}
	return out, nil
}

// GetIndustryValuation 返回某个行业的估值环境与行业内排名。
func (s *StockService) GetIndustryValuation(codeOrName string) (*IndustryValuationDetail, error) {
	report, err := s.GetValuationReport(0, false)
	if err != nil {
		return nil, err
	}
	if !report.Available {
		return &IndustryValuationDetail{Available: false, Message: report.Message}, nil
	}
	key := strings.TrimSpace(codeOrName)
	bareKey := industryCodeOf(key)
	var found *valuation.IndustryValuation
	// 行业码带体系前缀（"sw:340501"），但用户/界面很可能只给裸代码。
	// 两种都认，并把"哪个体系"一并回传，避免含义不明。
	for i := range report.Industries {
		item := &report.Industries[i]
		if item.Code == key || item.Name == key || industryCodeOf(item.Code) == bareKey {
			found = item
			break
		}
	}
	if found == nil {
		return &IndustryValuationDetail{
			Available: false,
			Message: fmt.Sprintf("没有找到行业「%s」；本机的行业分类来自数据湖的 industry_members，"+
				"它是 eastmoney（日更快照）与申万（2020 起历史回填）两套体系混存的", key),
		}, nil
	}
	out := &IndustryValuationDetail{
		Available: true,
		Industry:  *found,
		Snapshot:  report.Snapshot,
		Notes:     report.Notes,
	}
	for _, ranking := range report.Rankings {
		if ranking.Horizon.Key != "12m" {
			continue
		}
		for _, row := range ranking.Rows {
			if row.IndustryCode != found.Code {
				continue
			}
			if len(out.Members) >= 200 {
				break
			}
			out.Members = append(out.Members, row)
		}
	}
	return out, nil
}

// GetIndexValuation 返回指数性价比评估（独立模型）。
func (s *StockService) GetIndexValuation(code string) (*IndexValuationResult, error) {
	report, err := s.GetValuationReport(0, false)
	if err != nil {
		return nil, err
	}
	if !report.Available {
		return &IndexValuationResult{Available: false, Message: report.Message}, nil
	}
	key := strings.ToUpper(strings.TrimSpace(code))
	bare := cnlake.BareSymbol(key)
	for i := range report.Indices {
		item := report.Indices[i]
		if item.Symbol == key || cnlake.BareSymbol(item.Symbol) == bare {
			return &IndexValuationResult{
				Available: true,
				Index:     item,
				Snapshot:  report.Snapshot,
				Notes:     report.Notes,
			}, nil
		}
	}
	return &IndexValuationResult{
		Available: false,
		Message: fmt.Sprintf("没有找到指数「%s」。本机 index_bars 有 8 个指数"+
			"（000001.SH / 399001.SZ / 399006.SZ / 000688.SH / 000300.SH / 000016.SH / 000905.SH / 000852.SH），"+
			"其中只有 5 个在 index_constituents 里有成分与权重。", key),
	}, nil
}

// ListValuationIndices 返回可用于评估的指数清单（供界面下拉）。
func (s *StockService) ListValuationIndices() ([]ValuationIndexOption, error) {
	report, err := s.GetValuationReport(0, false)
	if err != nil {
		return nil, err
	}
	out := make([]ValuationIndexOption, 0, len(report.Indices))
	for _, item := range report.Indices {
		out = append(out, ValuationIndexOption{
			Symbol:          item.Symbol,
			Name:            item.Name,
			HasConstituents: item.Available,
			Constituents:    len(item.IndustryWeights),
			Note:            item.Note,
		})
	}
	return out, nil
}

// GetValuationParams 返回当前生效的模型参数。
func (s *StockService) GetValuationParams() ValuationParamsInfo {
	params := valuation.DefaultParams()
	return ValuationParamsInfo{
		ModelVersion:     params.ModelVersion,
		Horizons:         valuation.Horizons,
		HorizonWeights:   params.HorizonWeights,
		ScenarioProb:     params.ScenarioProb,
		Wacc:             params.WACC,
		TerminalGrowth:   params.TerminalGrowth,
		ValueStartDate:   params.ValueStartDate,
		RatingThresholds: params.RatingThresholds,
		Notes:            valuationNotes(),
	}
}

// ---------------------------------------------------------------------------
// 数据加载
// ---------------------------------------------------------------------------

// loadValuationDataset 一次读出评估所需的全部数据。
//
// focus 非空时表示"这是详情页在为某一只标的加载数据"。
//
// ⚠ 关键：**即使只想看一只，也必须把全市场的行情/股本/财报都留在内存里**。
// 第一版按 focus 把其它标的全过滤掉了，结果是详情页算行业基准时
// 「同行业只找到 1 只（自己）」→ 行业聚合不可用 → 相对估值整块失效
// （实测：贵州茅台落在申万 340501，该行业实际有 20 只，但页面上只看到自己 1 只）。
//
// 既然 `DailyBarsInRange` 按交易日分区读，一天的全市场本来就在同一个文件里，
// "只读一只"省不下任何 I/O —— 过滤掉只是在内存里把数据删掉，然后把行业基准毁掉。
// 所以这里全留，`focus` 只用于统计口径。
func (s *StockService) loadValuationDataset(lake *cnlake.Lake, focus []string) (*valuationDataset, error) {
	data := &valuationDataset{
		lake:         lake,
		bars:         map[string][]cnlake.BarRow{},
		adjPoints:    map[string][]cnlake.AdjFactorPoint{},
		shares:       map[string]cnlake.ShareTimeline{},
		reports:      map[string][]valuation.Report{},
		names:        map[string]string{},
		assetType:    map[string]string{},
		industries:   map[string]industryTag{},
		dividendPaid: map[string]float64{},
		focus:        map[string]bool{},
	}
	for _, code := range focus {
		if bare := cnlake.BareSymbol(code); bare != "" {
			data.focus[bare] = true
		}
	}

	// ---- 标的清单（名称 + 资产类型）----
	instruments, err := lake.Instruments()
	if err != nil {
		return nil, fmt.Errorf("读取 instruments 失败：%w", err)
	}
	for _, item := range instruments {
		bare := cnlake.BareSymbol(item.Symbol)
		data.names[bare] = item.Name
		data.assetType[bare] = item.AssetType
	}

	// ---- 评估日：数据湖里最后一根 K 线的日期 ----
	//
	// ⚠ 必须在"行业归属"之前定下来：行业快照要按 asOf 取，
	// 顺序反了就会拿 0 去查，结果是一条归属都取不到（全部落进"未分类"）。
	latestBar, err := lake.LatestTradeDate()
	if err != nil {
		return nil, fmt.Errorf("无法确定最新交易日：%w", err)
	}
	data.evalDate = latestBar
	data.latestBarDate = latestBar

	// ---- 行业归属（时点正确，且**只用一套体系**）----
	//
	// ⚠ 不能用 `lake.IndustryMembers()`：那个只返回"每个行业最新一份快照"，
	// 会把这 82 个申万快照压成一天，"某一天它属于哪个行业"这个问题就答不了。
	//
	// 本机的 industry_members 是**两套体系混存**：
	//   · `eastmoney` 日更快照，industry_name 是中文名（"白酒Ⅲ"）
	//   · `sw` 申万回填，2020-01-23 起 82 个月末快照，**industry_name 就是代码**
	// 两套体系的行业代码互不相通（6 位 vs 4 位），混在时间轴上取会让代码跳变，
	// 所以这里**选定一套体系**再用，并把体系名带进溯源信息。
	//
	// 选体系交给 cnlake.PickIndustrySystem（快照数多的优先 → 历史最长），
	// 实测本机会选中 sw（82 个快照 ⊇ eastmoney 的 5574 只，覆盖 5934 只）。
	snapshot := lake.IndustryMembersAsOf(data.evalDate, "")
	data.industrySystem = snapshot.System
	data.industryExcluded = snapshot.ExcludedSystems
	for _, row := range snapshot.Members {
		bare := cnlake.BareSymbol(row.Symbol)
		if bare == "" {
			continue
		}
		// ⚠ 键带体系前缀：防止两个体系里数字相同的代码互相覆盖
		// （eastmoney 的 "475" 与 sw 的 "475" 是两个完全不同的行业）。
		data.industries[bare] = industryTag{
			System: snapshot.System,
			Code:   snapshot.System + ":" + row.IndustryCode,
			Name:   industryDisplayName(snapshot.System, row.IndustryCode, row.IndustryName),
		}
	}
	slog.Debug("估值评估：行业归属就绪",
		"体系", snapshot.System, "归属标的数", len(data.industries), "评估日", data.evalDate)

	// ---- 行情（一次读完区间内全市场）----
	bars, err := lake.DailyBarsInRange(nil, valuationPriceStart, 0)
	if err != nil {
		return nil, fmt.Errorf("读取日线失败：%w", err)
	}
	for _, bar := range bars {
		bare := cnlake.BareSymbol(bar.Symbol)
		if bare == "" {
			continue
		}
		data.bars[bare] = append(data.bars[bare], bar)
	}

	// ---- 复权因子（每只一条阶梯，lake 内部带缓存）----
	for code := range data.bars {
		if points := lake.AdjFactorPoints(code); len(points) > 0 {
			data.adjPoints[code] = points
		}
	}

	// ---- 股本结构 ----
	if shareRows, err := lake.AllShareStructures(); err == nil {
		grouped := map[string][]cnlake.SharePoint{}
		for _, row := range shareRows {
			bare := cnlake.BareSymbol(row.Symbol)
			if bare == "" {
				continue
			}
			grouped[bare] = append(grouped[bare], cnlake.SharePoint{
				Date:        cnlake.StatementDate(int64(row.ChangeDate)),
				TotalShares: row.TotalShares,
				FloatShares: row.FloatShares,
			})
		}
		for code, rows := range grouped {
			data.shares[code] = cnlake.NewShareTimeline(rows)
		}
	}

	// ---- 财报科目 ----
	statementRows, err := lake.AllStatementItems(valuation.TrackedItems)
	if err != nil {
		return nil, fmt.Errorf("读取财报科目失败：%w", err)
	}
	type periodKey struct{ code, period string }
	values := map[periodKey]map[string]float64{}
	announce := map[periodKey]int64{}
	for _, row := range statementRows {
		bare := cnlake.BareSymbol(row.Symbol)
		if bare == "" || row.ReportPeriod == "" || row.ItemCode == "" {
			continue
		}
		key := periodKey{bare, row.ReportPeriod}
		if values[key] == nil {
			values[key] = map[string]float64{}
		}
		// 同一报告期可能有多条修订：**整期**保留 announce_date 最大的那一批。
		// 逐科目各留各的会让一个报告期里混着两次修订的数值。
		previous := announce[key]
		if row.AnnounceDate >= previous || previous == 0 {
			if row.AnnounceDate > previous {
				// 出现了更新的修订 → 清掉旧的一批
				values[key] = map[string]float64{}
				announce[key] = row.AnnounceDate
			}
			values[key][row.ItemCode] = row.ItemValue
		}
	}
	byCode := map[string][]valuation.Report{}
	for key, items := range values {
		byCode[key.code] = append(byCode[key.code], valuation.Report{
			Period:       key.period,
			AnnounceDate: announce[key],
			Values:       items,
		})
	}
	for code, reports := range byCode {
		data.reports[code] = reports
	}

	// ---- 分红（近 12 个月每股派息合计）----
	from := dateMinusDays(data.evalDate, 400) // 略宽于 12 个月，覆盖年报派息日
	for date, actions := range lake.ActionsByDate(from, data.evalDate) {
		if date <= 0 {
			continue
		}
		for _, action := range actions {
			bare := cnlake.BareSymbol(action.Symbol)
			if bare == "" {
				continue
			}
			if cash := action.CashPerShare(); cash > 0 {
				data.dividendPaid[bare] += cash
			}
		}
	}

	slog.Debug("估值评估：数据加载完成",
		"行情标的数", len(data.bars), "财报标的数", len(data.reports), "评估日", data.evalDate)
	return data, nil
}

// snapshot 构造溯源信息。
func (d *valuationDataset) snapshot() valuation.Snapshot {
	cutoff := fmt.Sprintf("行情 %s 收盘；财务只使用公告日 ≤ %s 的报告期",
		formatDate(d.latestBarDate), formatDate(d.evalDate))
	if d.industrySystem != "" {
		cutoff += fmt.Sprintf("；行业分类体系 %s（%s）",
			d.industrySystem, industrySystemLabel(d.industrySystem))
		if len(d.industryExcluded) > 0 {
			cutoff += "；其它体系仅用于补缺：" + strings.Join(d.industryExcluded, "、")
		}
	}
	return valuation.Snapshot{
		EvalDate:     d.evalDate,
		DataCutoff:   cutoff,
		ModelVersion: valuation.ModelVersion,
		Universe:     len(d.bars),
	}
}

// ---------------------------------------------------------------------------
// 单只标的评估
//
// ⚠ 这里刻意拆成两步（snapshotCompany → evaluateOneStock），原因是**行业基准必须
// 用全市场算，而不是用"这次评估了多少只"算**。
//
// 实测过的错误做法（第一版就是这个）：行业聚合只拿被评估的那批标的当成分。
// 默认只评估市值前 150 只，150 只摊到 61 个行业 → 56 个行业不足
// IndustryMinMembers(5) → 行业聚合不可用 → **相对估值整块失效**：
// 榜上"对行业中位"80/128 行恒为 0，而且「行业估值环境」「相对同行估值」
// 两个维度的分全部退化成常数 50（维度白设）。
//
// 正确做法：先用**全市场**候选算出每个行业的成分（只需要市值/盈利/净资产/
// 负债率这些可从财报直接得到的量），再只对被选中的标的做昂贵的评分。
// 这样 `limit` 只影响"评估多少只"，不影响"行业基准怎么算"。
//
//	修前：行业聚合 68 个，可用 7 个；12 个月榜 128 行里 80 行"对行业中位"= 0
//	修后：行业聚合 396 个，可用 271 个；同榜 128 行里只有 17 行为 0
//	     （剩下 17 行是成分确实不足 5 只的行业，属正确行为）
//      由 TestIndustryBenchmarkUsesFullMarket 钉住。
// ---------------------------------------------------------------------------

// companySnapshot 是"算行业基准所需的最小信息集"。
//
// 刻意只装行业聚合与相对估值要用到的字段：它能对全市场 5000+ 只标的算一遍
// 而不至于太慢（不需要跑七类估值方法）。
//
// ids 里的 valSeries 是**惰性填充**的：只有需要重建行业历史序列时才建，
// 不然后台每只标的都白建一条 1400 天的估值序列。
type companySnapshot struct {
	company valuation.CompanyValuation
	series  valuation.PriceSeries
	asOf    valuation.AsOf
	shares  cnlake.ShareTimeline
	tag     industryTag

	ids snapshotIDs
}

// snapshotIDs 是"行业历史重建"额外需要的东西（惰性）。
type snapshotIDs struct {
	AsOf valuation.AsOf
	// valSeries 是按**行业快照日**采样出来的估值点（不是逐日）。
	valSeries []valuation.ValuationPoint
}

// snapshotCompany 算出一只标的的公司快照（行业基准用）。
//
// 与 evaluateOneStock 的关系：这个函数是它的**前半段**，
// evaluateOneStock 先调它、再往后接成长与质量评分。
// 抽出来是为了让"全市场行业基准"这一遍不必付昂贵方法的代价。
func (s *StockService) snapshotCompany(data *valuationDataset, code string, params valuation.Params) (companySnapshot, bool) {
	bars := data.bars[code]
	if len(bars) < valuationMinBars {
		return companySnapshot{}, false
	}

	// ---- 价格序列（原始价 + 后复权因子）----
	//
	// ⚠ 两个价格口径在这里同时落位，绝不能混：
	//   Close      = 不复权收盘价 → 市值、PE/PB/PS、每股价值
	//   AdjFactor  = 后复权乘数   → 收益、波动、回撤（valuation 包里按方法名分开取）
	points := data.adjPoints[code]
	vbars := make([]valuation.Bar, 0, len(bars))
	for _, bar := range bars {
		date := bar.Date()
		vbars = append(vbars, valuation.Bar{
			Date:      date,
			Open:      bar.Open,
			High:      bar.High,
			Low:       bar.Low,
			Close:     bar.Close,
			Volume:    float64(bar.Volume),
			Amount:    bar.Amount,
			AdjFactor: valuation.AdjScaleAt(adjPointsFor(points), date),
		})
	}
	series := valuation.NewPriceSeries(code, vbars)
	if series.Len() < valuationMinBars {
		return companySnapshot{}, false
	}
	current := series.Bars[series.Len()-1]

	// ---- 财报时点视图 ----
	asOf := valuation.BuildAsOf(data.reports[code], data.evalDate)
	if asOf.LatestPeriod == "" {
		return companySnapshot{}, false
	}

	// ---- 历史估值重建（市值 = 当期股本 × 不复权价）----
	shares := data.shares[code]
	if len(shares) == 0 {
		return companySnapshot{}, false
	}
	valuationSeries := valuation.BuildValuationSeries(series, shareTimelineFor(shares),
		valuation.FundamentalsSchedule(asOf))
	if len(valuationSeries) == 0 {
		return companySnapshot{}, false
	}
	currentPoint := valuationPointAt(valuationSeries, current.Date)
	if currentPoint.MarketCap <= 0 {
		return companySnapshot{}, false
	}

	// ---- 行业归属 ----
	tag := data.industries[code]
	if tag.Code == "" {
		tag = industryTag{Code: "unclassified", Name: "未分类"}
	}

	out := companySnapshot{
		series: series,
		asOf:   asOf,
		shares: shares,
		tag:    tag,
		ids:    snapshotIDs{AsOf: asOf},
		company: valuation.CompanyValuation{
			Code:           code,
			Name:           data.names[code],
			IndustryCode:   tag.Code,
			IndustryName:   tag.Name,
			MarketCap:      currentPoint.MarketCap,
			FloatMarketCap: currentPoint.FloatMarketCap,
			PE:             currentPoint.PE,
			PB:             currentPoint.PB,
			PS:             currentPoint.PS,
			NetProfit:      asOf.TTM.NetProfit,
			Revenue:        asOf.TTM.Revenue,
			Equity:         asOf.Items[valuation.ItemTotalEquity],
		},
	}
	// 负债率：同行可比筛选要用，且它能直接从财报拿到（不必跑质量评分）
	if assets := asOf.Items[valuation.ItemTotalAssets]; assets > 0 {
		out.company.DebtRatio = asOf.Items[valuation.ItemTotalLiabilities] / assets
	}
	return out, true
}

// buildSnapshotUniverse 对**全部候选标的**算公司快照，并按行业分组。
//
// 这就是"行业基准用全市场算"的落点。返回：
//   - byCode：按代码索引的全市场快照（行业历史重建要用）；
//   - groups：行业码（带体系前缀）-> 该行业的成分代码（行业聚合要用）。
//
// 成本：只读日线 + 财报 + 股本，不跑估值方法，所以对 5000 只标的是可接受的
// （实测全市场这一遍约 20~25 秒，比整份评估贵的部分在评分而不是这里）。
// 结果按 (评估日, 数据源代次) 缓存在 StockService 上，改 limit 不重算。
func (s *StockService) buildSnapshotUniverse(data *valuationDataset, params valuation.Params) (map[string]companySnapshot, map[string][]string) {
	byCode := make(map[string]companySnapshot, len(data.bars))
	groups := map[string][]string{}
	skipped := 0
	for code := range data.bars {
		// 只算股票：ETF/基金没有财报，估值模型不适用
		if kind := data.assetType[code]; kind != "" && kind != "stock" && kind != "cdr" {
			continue
		}
		if _, ok := data.reports[code]; !ok {
			continue
		}
		snapshot, ok := s.snapshotCompany(data, code, params)
		if !ok {
			skipped++
			continue
		}
		byCode[code] = snapshot
		groups[snapshot.tag.Code] = append(groups[snapshot.tag.Code], code)
	}
	slog.Debug("估值评估：全市场行业基准就绪",
		"标的数", len(byCode), "行业数", len(groups), "跳过", skipped)
	return byCode, groups
}

// evaluateOneStock 评估一只标的。
//
// withFair=true 时算五档合理价值（详情页需要）；false 时只算 12 个月一档
// （全市场评估只需要一个安全边际代表值，算五档纯属浪费 —— 一档里就有
// 7 个方法 + 反向 DCF + 冲突检测）。
func (s *StockService) evaluateOneStock(data *valuationDataset, code string, params valuation.Params, withFair bool) (stockEvaluation, bool) {
	out := stockEvaluation{code: code, name: data.names[code]}

	snapshot, ok := s.snapshotCompany(data, code, params)
	if !ok {
		data.skipped++
		return out, false
	}
	out.series = snapshot.series
	out.asOf = snapshot.asOf
	out.shares = snapshot.shares
	out.company = snapshot.company
	current := snapshot.series.Bars[snapshot.series.Len()-1]

	// ---- 上年同期时点视图（ROE 平均值、负债率变化用）----
	asOf := snapshot.asOf
	previousAsOf := asOf.Retained(dateMinusDays(asOf.LatestAnnounce, 365))

	// ---- 成长与质量 ----
	singles := valuation.SingleQuarters(asOf.Reports, []string{
		valuation.ItemRevenue, valuation.ItemNetProfit,
		valuation.ItemNetProfitDeduct, valuation.ItemNetCashOperate,
	})
	growth, quality := valuation.EvaluateGrowth(valuation.GrowthInput{
		AsOf:         asOf,
		Singles:      singles,
		PrevYearAsOf: previousAsOf,
		Params:       params,
	})
	out.growth = growth
	out.quality = quality

	// 把成长/质量的结论回填进公司快照 —— 相对估值要判断"溢价有没有基本面支撑"，
	// 同行可比筛选要用"成长档位"，两者都需要这两个分。
	out.company.ROE = growth.ROE
	out.company.GrowthScore = growth.Score
	out.company.QualityScore = quality.Score
	out.company.DebtRatio = quality.DebtRatio
	out.company.RevenueYoY = growth.RevenueYoY
	out.company.ProfitYoY = growth.ProfitYoY

	// ---- 自身历史估值分位 ----
	//
	// ⚠ 这里重建一次序列只为拿分位；快照那一步已经建过一次。
	// 没有把它塞进 companySnapshot 是因为"自身分位"只有被评估的标的才需要，
	// 而全市场那一遍（buildSnapshotUniverse）不需要 —— 为 5000 只标的算
	// 5 年分位是纯粹的浪费。
	valuationSeries := valuation.BuildValuationSeries(snapshot.series, shareTimelineFor(snapshot.shares),
		valuation.FundamentalsSchedule(asOf))
	currentPoint := valuationPointAt(valuationSeries, current.Date)
	out.self = valuation.EvaluateSelfValuation(valuationSeries, currentPoint, params, valuation.HistoryAnchors{
		ROE3Y:    annualROE(asOf, 3),
		ROE5Y:    annualROE(asOf, 5),
		Growth3Y: growth.RevenueCAGR3,
		Growth5Y: growth.RevenueCAGR5,
	})
	out.company.SelfPercentile = out.self.PEPercentile5Y
	out.company.SelfAvailable = out.self.Available

	// ---- 波动率 ----
	if volatility, ok := snapshot.series.AnnualizedVolatility(252); ok {
		out.volatility = volatility
	}

	// ---- 合理价值 ----
	horizons := valuation.Horizons
	if !withFair {
		horizons = []valuation.Horizon{valuation.Horizons[3]} // 只算 12 个月
	}
	out.fair = map[string]valuation.FairValue{}
	sharePoint, _ := snapshot.shares.Latest()
	for _, horizon := range horizons {
		fair := valuation.EvaluateFairValue(valuation.FairValueInput{
			Company: out.company,
			AsOf:    asOf,
			// ⚠ 不复权价：市值与每股价值口径必须是真实成交价
			Price:       current.Close,
			Shares:      sharePoint.TotalShares,
			PrevShares:  previousShares(current.Date, snapshot.shares),
			Growth:      growth,
			Quality:     quality,
			Horizon:     horizon,
			Params:      params,
			PriceSeries: snapshot.series,
		})
		out.fair[horizon.Key] = fair
		if horizon.Key == "12m" {
			out.confidence = fair.Confidence
		}
	}
	if !withFair {
		// 全市场评估只算了 12 个月；其它档位用同一份结果占位，
		// 保证 ScoreInput 里取 fair 不会拿到零值（评分里只有 DimMargin 用到 12m）。
		for _, horizon := range valuation.Horizons {
			if _, ok := out.fair[horizon.Key]; !ok {
				out.fair[horizon.Key] = out.fair["12m"]
			}
		}
	}
	if out.confidence.Score == 0 {
		out.confidence = valuation.Confidence{
			Score: 40, Level: "low",
			Reasons: []string{"合理价值测算未给出置信度（数据不足）"},
		}
	}
	return out, true
}

// applyIndustry 用行业环境重算各期限的合理价值。
//
// 方案要求"行业估值要参与多期限风险收益分析"，所以第二遍拿到行业结果后必须重算，
// 而不是留一个"未做行业修正"的值。重算走 ApplyIndustryEnvironment（内部是完整
// 重跑 EvaluateFairValue），保证行业修正只乘一次。
func (s *StockService) applyIndustry(eval stockEvaluation, industry valuation.IndustryValuation, params valuation.Params) map[string]valuation.FairValue {
	out := make(map[string]valuation.FairValue, len(eval.fair))
	for key, item := range eval.fair {
		horizon, ok := valuation.HorizonByKey(key)
		if !ok {
			out[key] = item
			continue
		}
		out[key] = valuation.ApplyIndustryEnvironment(item, eval.company, eval.asOf,
			eval.growth, eval.quality, industry, params, horizon, eval.series)
	}
	return out
}

// ---------------------------------------------------------------------------
// 全市场评估编排
// ---------------------------------------------------------------------------

func (s *StockService) buildValuationReport(lake *cnlake.Lake, params valuation.Params, limit int) (*ValuationReport, error) {
	timer := newPhaseTimer()
	data, err := s.loadValuationDataset(lake, nil)
	if err != nil {
		return nil, err
	}
	timer.mark("加载数据湖")
	codes := s.selectValuationCodes(data, limit)
	timer.mark("筛选评估范围")

	report := &ValuationReport{
		Available:  true,
		Snapshot:   data.snapshot(),
		Notes:      valuationNotes(),
		Requested:  limit,
		FullMarket: limit <= 0,
	}

	// ---- 第一遍：**全市场**行业基准 ----
	//
	// ⚠ 这一步必须用全市场候选，不能用 `codes`（被评估的那批）。
	// 用 `codes` 会让 150 只摊到 61 个行业、56 个行业不足 5 只成分，
	// 于是行业聚合不可用 → 相对估值整块恒为 0、行业维度退化成常数。
	// 详见 snapshotCompany / buildSnapshotUniverse 的注释。
	universe := s.snapshotUniverse(data, params)
	timer.mark("全市场行业基准")

	// ---- 逐只评估被选中的标的 ----
	//
	// ⚠ 顺序上这一步**必须在行业聚合之前**：行业阶段判定（成长/成熟/衰退/周期）
	// 与"基本面行业分位"都依赖成分股的营收增速与成长/质量分，而那些分只有
	// 跑过评分才存在。第一版把聚合放在评分之前，结果是所有成分的
	// `RevenueYoY` / `GrowthScore` 都是 0 → 阶段恒为 "unknown"、
	// 相对估值的四象限永远退化成"数据不足"。
	evaluations := make([]stockEvaluation, 0, len(codes))
	for _, code := range codes {
		eval, ok := s.evaluateOneStock(data, code, params, false)
		if !ok {
			continue
		}
		evaluations = append(evaluations, eval)
	}
	timer.mark("逐只评估")

	// 把评估后的**充实版公司快照**写回全市场快照表，让行业聚合看到真实的基本面。
	//
	// 只有被评估的那批标的有充实版（其余标的没跑评分），所以行业聚合里
	// 未被评估的成分仍缺成长/质量分 —— 这是"只评估市值前 N 只"的固有限制，
	// 由行业说明文案按覆盖率如实交代（见 buildIndustryAggregates）。
	mergeEvaluated(universe.byCode, evaluations)

	// ---- 行业历史加权 PE 序列（时点正确重建）----
	//
	// 这是"行业整体比它自己历史上贵还是便宜"的唯一来源。没有它，
	// 行业估值环境只能退化成"加权与中位 PE 的背离"这个横截面口径。
	history := s.buildIndustryHistory(data, universe.byCode, data.industrySystem, universe.groups, params)
	timer.mark("行业历史序列重建")

	industries, members := s.buildIndustryAggregates(universe.byCode, universe.groups, history, len(codes), params)
	for key, result := range history {
		if industry, ok := industries[key]; ok {
			industries[key] = attachIndustryHistoryNote(industry, result)
		}
	}
	timer.mark("行业聚合")

	// ---- 评分（含行业环境重算）----

	// ---- 第三遍：逐只评分（含行业环境重算）----
	rankInputs := map[string][]valuation.RankInput{}
	for _, horizon := range valuation.Horizons {
		rankInputs[horizon.Key] = make([]valuation.RankInput, 0, len(evaluations))
	}
	for i := range evaluations {
		eval := &evaluations[i]
		industry := industries[eval.company.IndustryCode]
		industryMembers := members[eval.company.IndustryCode]
		peers, peerNote := valuation.SelectComparables(eval.company, industryMembers, params)
		relative := valuation.EvaluateRelativeValuation(eval.company, industry, industryMembers, peers, params)
		relative.ComparableNote = peerNote

		fair := s.applyIndustry(*eval, industry, params)
		sharePoint, _ := eval.shares.Latest()

		score, scoreErr := valuation.EvaluateScore(valuation.ScoreInput{
			Code:          eval.code,
			Name:          eval.name,
			Industry:      industry,
			Self:          eval.self,
			Relative:      relative,
			Growth:        eval.growth,
			Quality:       eval.quality,
			AsOf:          eval.asOf,
			Company:       eval.company,
			Price:         eval.series.LastClose(),
			Shares:        sharePoint.TotalShares,
			Volatility:    eval.volatility,
			PaidLast12M:   data.dividendPaid[eval.code],
			PriceLength:   eval.series.Len(),
			PriceAdjusted: eval.series.Adjusted,
			Confidence:    eval.confidence,
			FairByHorizon: fair,
			Params:        params,
		})
		if scoreErr != nil {
			slog.Warn("估值评估：跳过一只标的", "code", eval.code, "err", scoreErr)
			continue
		}
		for _, horizonScore := range score.Scores {
			rankInputs[horizonScore.Horizon.Key] = append(rankInputs[horizonScore.Horizon.Key],
				valuation.RankInput{Code: eval.code, Name: eval.name, Score: horizonScore, Stock: score})
		}
	}

	// ---- 排名 ----
	report.Distributions = map[string]valuation.ScoreDistribution{}
	for _, horizon := range valuation.Horizons {
		ranking := valuation.BuildRanking(horizon, rankInputs[horizon.Key])
		report.Rankings = append(report.Rankings, ranking)
		report.Distributions[horizon.Key] = ranking.Distribution
	}

	// ---- 行业与指数 ----
	report.Industries = sortedIndustries(industries)
	report.Indices = s.evaluateIndices(evaluations, params)
	report.IndustryDistribution = buildIndustryDistribution(report.Rankings)
	timer.mark("排名与指数")
	report.PhaseTimings = timer.phases()

	// ---- 覆盖统计 ----
	report.Snapshot.Universe = len(data.bars)
	if len(report.Rankings) > 0 {
		report.Snapshot.Evaluated = report.Rankings[0].Scored
		report.Snapshot.Unavailable = report.Rankings[0].ExcludedCount
	}
	if data.skipped > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"%d 只标的因数据不足被跳过（行情不足 %d 根、缺少财报或缺少股本记录）；"+
				"它们不会以「中性」的分数出现在榜上", data.skipped, valuationMinBars))
	}
	// 行业基准的覆盖面如实说明（方案 §5.2 要求行业估值口径可核对）
	availableIndustries := 0
	for _, industry := range report.Industries {
		if industry.Available {
			availableIndustries++
		}
	}
	report.Notes = append(report.Notes, fmt.Sprintf(
		"行业基准用**全市场**候选计算（%d 个行业、其中 %d 个成分足够给出中位数与加权值），"+
			"与「本次评估只数」无关 —— 否则 150 只摊到几十个行业会让相对估值整块失效",
		len(report.Industries), availableIndustries))
	return report, nil
}

// selectValuationCodes 选出参与评估的代码。
//
// limit > 0 时按市值降序取前 limit 只 —— 用**市值**而不是代码顺序：
// 小市值标的（次新、微盘）数据质量普遍更差，先把大票评估出来，
// 界面上"首刷看到的东西"才是有意义的。
func (s *StockService) selectValuationCodes(data *valuationDataset, limit int) []string {
	type candidate struct {
		code string
		cap  float64
	}
	candidates := make([]candidate, 0, len(data.bars))
	for code, bars := range data.bars {
		if len(bars) < valuationMinBars {
			continue
		}
		// 只评估股票：ETF / 基金没有财报，估值模型不适用
		if kind := data.assetType[code]; kind != "" && kind != "stock" && kind != "cdr" {
			continue
		}
		if _, ok := data.reports[code]; !ok {
			continue
		}
		if len(data.shares[code]) == 0 {
			continue
		}
		candidates = append(candidates, candidate{code: code, cap: marketCapOf(data, code)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].cap != candidates[j].cap {
			return candidates[i].cap > candidates[j].cap
		}
		return candidates[i].code < candidates[j].code
	})
	out := make([]string, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, item.code)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// marketCapOf 用最新股本 × 最新收盘价估一个市值（仅用于评估顺序）。
func marketCapOf(data *valuationDataset, code string) float64 {
	bars := data.bars[code]
	if len(bars) == 0 {
		return 0
	}
	point, ok := data.shares[code].At(bars[len(bars)-1].Date())
	if !ok {
		return 0
	}
	return bars[len(bars)-1].Close * point.TotalShares
}

// mergeEvaluated 把评估后的充实公司快照写回全市场快照表。
//
// 为什么需要：`snapshotCompany` 只算得出"行业聚合必需的市值/盈利/净资产/负债率"，
// 而行业阶段判定要用成分股的 `RevenueYoY`、相对估值要用 `GrowthScore` /
// `QualityScore` —— 那些分只有 `evaluateOneStock` 跑完才有。
//
// 所以被评估的标的（`limit` 只数）用充实版覆盖裸版；其余标的不动。
// 覆盖率不足会由行业说明文案交代，而不是假装所有成分都有基本面分。
func mergeEvaluated(byCode map[string]companySnapshot, evaluations []stockEvaluation) {
	merged := 0
	for i := range evaluations {
		eval := &evaluations[i]
		entry, ok := byCode[eval.code]
		if !ok {
			continue
		}
		entry.company = eval.company
		byCode[eval.code] = entry
		merged++
	}
	slog.Debug("估值评估：充实版公司快照已合并", "合并数", merged, "全市场", len(byCode))
}

// buildIndustryAggregates 聚合每个行业。
//
// 输入是**全市场的公司快照**（由 buildSnapshotUniverse 产出）按行业码分好的组，
// 不是"被评估的那批标的" —— 这是行业基准正确性的关键，理由见
// snapshotCompany 上方的说明。
//
// history 是该体系重建出来的行业历史加权 PE 序列（键为行业码）。
// **有这个序列，`PEPercentile` 才是真正的分位**；没有就退化成横截面结构口径。
func (s *StockService) buildIndustryAggregates(
	snapshots map[string]companySnapshot,
	groups map[string][]string,
	history map[string]industryHistoryResult,
	evaluated int,
	params valuation.Params,
) (map[string]valuation.IndustryValuation, map[string][]valuation.CompanyValuation) {

	if len(groups) > maxValuationIndustries {
		slog.Warn("估值评估：行业数异常偏多", "行业数", len(groups))
	}
	industries := make(map[string]valuation.IndustryValuation, len(groups))
	members := make(map[string][]valuation.CompanyValuation, len(groups))
	for key, codes := range groups {
		group := make([]valuation.CompanyValuation, 0, len(codes))
		name := key
		withFundamentals := 0
		for _, code := range codes {
			snapshot, ok := snapshots[code]
			if !ok {
				continue
			}
			group = append(group, snapshot.company)
			if snapshot.company.GrowthScore > 0 || snapshot.company.RevenueYoY != 0 {
				withFundamentals++
			}
			if snapshot.company.IndustryName != "" {
				name = snapshot.company.IndustryName
			}
		}
		members[key] = group
		industry := valuation.AggregateIndustry(key, name, group, history[key].Series, params)
		// 如实交代"有多少成分带基本面分"：行业阶段与基本面分位只对有分的那些有效。
		// 评估范围越窄这个比例越低，界面上必须能看见，否则会误以为整个行业都被评过。
		if len(group) > 0 {
			ratio := float64(withFundamentals) / float64(len(group))
			if ratio < 0.99 {
				industry.Note = appendNote(industry.Note, fmt.Sprintf(
					"本行业 %d 个成分里只有 %d 个（%.0f%%）在本轮评估范围内跑过基本面评分"+
						"（本轮共评估 %d 只，按市值取前 N）——行业阶段与基本面分位仅对这 %d 个有效；"+
						"把评估范围调到「全市场」可消除这一限制",
					len(group), withFundamentals, ratio*100, evaluated, withFundamentals))
			}
		}
		industries[key] = industry
	}
	return industries, members
}

// appendNote 把一段说明追加到已有 Note 后面（为空时就是它本身）。
func appendNote(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "；" + extra
}

// attachIndustryHistoryNote 在行业上补写"历史序列覆盖情况"的说明。
//
// 方案 §5.2 要求行业估值口径可核对，所以历史序列的起止与样本数必须能看见 ——
// 只有 3 个样本的"分位"和 80 个样本的分位不是一回事。
func attachIndustryHistoryNote(industry valuation.IndustryValuation, result industryHistoryResult) valuation.IndustryValuation {
	if result.UsedDays == 0 {
		return industry
	}
	note := fmt.Sprintf("历史加权 PE 序列由**时点正确**重建：%s 起 %d 个行业快照日里算出 %d 个"+
		"（每个快照日用该日生效的成分与股本、以及该日已披露的 TTM 业绩）",
		formatDate(result.First), result.SnapshotDays, result.UsedDays)
	if industry.Note != "" {
		industry.Note += "；" + note
	} else {
		industry.Note = note
	}
	return industry
}

// snapshotUniverse 返回全市场行业基准（带缓存）。
//
// 缓存理由：它只依赖 (评估日, 数据源代次)，与 `limit` 无关。用户在界面上
// 切换"前 80 只 / 前 150 只 / 全市场"时不应该把全市场那一遍重算一次。
func (s *StockService) snapshotUniverse(data *valuationDataset, params valuation.Params) *valuationUniverseEntry {
	gen := s.universeGen.Load()
	s.valMu.Lock()
	cached := s.valUniverse
	s.valMu.Unlock()
	if cached != nil && cached.gen == gen && cached.evalDate == data.evalDate {
		slog.Debug("估值评估：复用全市场行业基准", "评估日", data.evalDate, "行业数", len(cached.groups))
		return cached
	}

	byCode, groups := s.buildSnapshotUniverse(data, params)
	entry := &valuationUniverseEntry{gen: gen, evalDate: data.evalDate, byCode: byCode, groups: groups}

	s.valMu.Lock()
	s.valUniverse = entry
	s.valMu.Unlock()
	return entry
}

// aggregateIndustryOf 为详情页的单只标的现场聚合其行业。
//
// 做法与全市场评估一致：**遍历全市场同行业成分**（走 snapshotCompany，
// 不算昂贵的评分），保证详情页看到的行业基准与总览页是同一套口径。
//
// 与全市场评估的唯一差别：这里没有"其它标的的成长/质量分"，所以
// `GrowthPercentileInIndustry` / `QualityPercentileInIndustry` 会是 0 ——
// 相对估值的四象限判定会因此退化为"估值与行业基本持平"。
// 这一点由返回的 Note 如实说明，不假装做过基本面排序。
func (s *StockService) aggregateIndustryOf(data *valuationDataset, eval stockEvaluation, params valuation.Params) (valuation.IndustryValuation, []valuation.CompanyValuation) {
	tag := eval.company.IndustryCode
	members := make([]valuation.CompanyValuation, 0, 128)
	sameIndustry := 0
	for code := range data.bars {
		if data.industries[code].Code != tag {
			continue
		}
		sameIndustry++
		if code == eval.code {
			continue
		}
		snapshot, ok := s.snapshotCompany(data, code, params)
		if !ok {
			continue
		}
		members = append(members, snapshot.company)
	}
	// 把标的自己也算进行业横截面（分位必须在含自己的完整横截面上算）
	members = append(members, eval.company)

	out := valuation.AggregateIndustry(tag, eval.company.IndustryName, members, nil, params)
	if !out.Available {
		out.Note = fmt.Sprintf("同行业在数据湖里找到 %d 只、其中数据可用 %d 只（聚合至少需要 %d 只）；"+
			"详情页是按需计算的，行业与同行比较的可信度低于总览页",
			sameIndustry, len(members), params.IndustryMinMembers)
	}
	return out, members
}

// ---------------------------------------------------------------------------
// 指数评估
// ---------------------------------------------------------------------------

// evaluateIndices 评估有成分数据的指数（独立模型）。
func (s *StockService) evaluateIndices(evaluations []stockEvaluation, params valuation.Params) []valuation.IndexEvaluation {
	constituents, err := s.lakeHandle().AllIndexConstituents()
	if err != nil {
		slog.Debug("估值评估：指数成分不可用", "err", err)
	}

	indexBars, err := s.lakeHandle().AllIndexBars()
	if err != nil {
		slog.Debug("估值评估：指数行情不可用", "err", err)
	}
	barsBySymbol := map[string][]cnlake.IndexBarRow{}
	for _, bar := range indexBars {
		barsBySymbol[bar.Symbol] = append(barsBySymbol[bar.Symbol], bar)
	}

	byCode := make(map[string]*stockEvaluation, len(evaluations))
	for i := range evaluations {
		byCode[evaluations[i].code] = &evaluations[i]
	}

	names := indexNames()
	symbols := make([]string, 0, len(barsBySymbol))
	for symbol := range barsBySymbol {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	out := make([]valuation.IndexEvaluation, 0, len(symbols))
	for _, symbol := range symbols {
		rows := constituents[symbol]
		items := make([]valuation.IndexConstituent, 0, len(rows))
		for _, row := range rows {
			bare := cnlake.BareSymbol(row.Symbol)
			eval := byCode[bare]
			if eval == nil {
				continue
			}
			fair := eval.fair["12m"]
			items = append(items, valuation.IndexConstituent{
				Code:                  bare,
				Name:                  eval.name,
				Weight:                row.Weight,
				Industry:              eval.company.IndustryName,
				MarketCap:             eval.company.MarketCap,
				PE:                    eval.company.PE,
				PB:                    eval.company.PB,
				NetProfit:             eval.company.NetProfit,
				Revenue:               eval.company.Revenue,
				Equity:                eval.company.Equity,
				ROE:                   eval.company.ROE,
				RevenueYoY:            eval.company.RevenueYoY,
				GrowthScore:           eval.company.GrowthScore,
				QualityScore:          eval.company.QualityScore,
				Price:                 eval.series.LastClose(),
				FairBase:              fair.Base,
				SafetyMarginAvailable: fair.Available,
				SelfPercentile:        eval.self.PEPercentile5Y,
			})
		}
		out = append(out, valuation.EvaluateIndex(symbol, names[symbol], items, valuation.IndexParams{
			Params:     params,
			Volatility: indexVolatility(barsBySymbol[symbol]),
		}))
	}
	return out
}

// indexVolatility 用指数自身的日线算年化波动率。
func indexVolatility(bars []cnlake.IndexBarRow) float64 {
	vbars := make([]valuation.Bar, 0, len(bars))
	for _, bar := range bars {
		vbars = append(vbars, valuation.Bar{
			Date:  indexBarDate(bar),
			Open:  bar.Open,
			High:  bar.High,
			Low:   bar.Low,
			Close: bar.Close,
		})
	}
	series := valuation.NewPriceSeries("index", vbars)
	if volatility, ok := series.AnnualizedVolatility(252); ok {
		return volatility
	}
	// 兜底：A 股宽基指数的长期年化波动大致在 18~25%，取中间值
	return 0.20
}

// indexNames 是 8 个指数的名字（数据湖的 index_bars 没有名称列）。
func indexNames() map[string]string {
	return map[string]string{
		"000001.SH": "上证指数",
		"399001.SZ": "深证成指",
		"399006.SZ": "创业板指",
		"000688.SH": "科创50",
		"000300.SH": "沪深300",
		"000016.SH": "上证50",
		"000905.SH": "中证500",
		"000852.SH": "中证1000",
	}
}

// buildIndustryDistribution 统计"各行业有多少标的进入全市场前 20%"。
func buildIndustryDistribution(rankings []valuation.HorizonRanking) []IndustryDistributionRow {
	type acc struct {
		name      string
		members   int
		topCounts map[string]int
		sumScore  map[string]float64
	}
	groups := map[string]*acc{}
	for index, ranking := range rankings {
		threshold := int(float64(len(ranking.Rows)) * 0.20)
		for rowIndex, row := range ranking.Rows {
			key := row.IndustryCode
			if key == "" {
				key = "unclassified"
			}
			item := groups[key]
			if item == nil {
				item = &acc{name: row.IndustryName, topCounts: map[string]int{}, sumScore: map[string]float64{}}
				groups[key] = item
			}
			// 成分数只在第一档期限里数一次（五档的标的集合是同一个）
			if index == 0 {
				item.members++
			}
			item.sumScore[ranking.Horizon.Key] += row.Score
			if rowIndex < threshold {
				item.topCounts[ranking.Horizon.Key]++
			}
		}
	}
	out := make([]IndustryDistributionRow, 0, len(groups))
	for code, item := range groups {
		row := IndustryDistributionRow{
			Code:         code,
			Name:         item.name,
			Members:      item.members,
			TopCounts:    item.topCounts,
			AverageScore: map[string]float64{},
		}
		for key, sum := range item.sumScore {
			if row.Members > 0 {
				row.AverageScore[key] = sum / float64(row.Members)
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Members != out[j].Members {
			return out[i].Members > out[j].Members
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func sortedIndustries(industries map[string]valuation.IndustryValuation) []valuation.IndustryValuation {
	out := make([]valuation.IndustryValuation, 0, len(industries))
	for _, item := range industries {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Members != out[j].Members {
			return out[i].Members > out[j].Members
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// ---------------------------------------------------------------------------
// 缓存
// ---------------------------------------------------------------------------

func (s *StockService) cachedValuationReport(gen int64, limit int) *ValuationReport {
	s.valMu.Lock()
	defer s.valMu.Unlock()
	entry := s.valCache
	if entry == nil || entry.gen != gen || entry.limit != limit || entry.builtAt.IsZero() {
		return nil
	}
	if time.Since(entry.builtAt) >= valuationTTL {
		return nil
	}
	return entry.report
}

func (s *StockService) storeValuationReport(gen int64, limit int, report *ValuationReport) {
	s.valMu.Lock()
	s.valCache = &valuationCacheEntry{gen: gen, limit: limit, builtAt: time.Now(), report: report}
	s.valMu.Unlock()
}

// invalidateValuationCache 让评估缓存失效（数据源切换、数据更新时调用）。
func (s *StockService) invalidateValuationCache() {
	s.valMu.Lock()
	s.valCache = nil
	s.valUniverse = nil
	s.valMu.Unlock()
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func unavailableValuationReport(message string) *ValuationReport {
	return &ValuationReport{
		Available: false,
		Message:   message,
		Snapshot: valuation.Snapshot{
			ModelVersion: valuation.ModelVersion,
			DataCutoff:   "数据湖不可用",
		},
		Notes: valuationNotes(),
	}
}

func unavailableStockValuation(message string) *StockValuation {
	return &StockValuation{
		Available: false,
		Message:   message,
		Snapshot: valuation.Snapshot{
			ModelVersion: valuation.ModelVersion,
			DataCutoff:   "数据湖不可用",
		},
		Notes: valuationNotes(),
	}
}

// valuationNotes 是界面底部必须显示的口径说明。
func valuationNotes() []string {
	return []string{
		"复权口径：收益、波动、回撤与情景收益一律用**后复权价**（不复权价 × 复权因子）；" +
			"市值、PE、PB、PS 一律用**不复权价 × 当期股本**。两者绝不混用 —— " +
			"数据湖的日线是不复权行情，混用会让除权日的收益凭空变成 −60% 级别的暴跌。",
		"历史估值是**自建重建**的：数据湖的 valuation_metrics 在本机只有 5 个交易日，" +
			"所以历史 PE/PB/PS 由「当期股本 × 当日不复权价 ÷ 当时已披露的 TTM 业绩」算出，起点 " +
			formatDate(valuationPriceStart) + "。",
		"财务数值是**年内累计**口径，且 Q4 是**全年值**（不是第四季度单季）：" +
			"TTM = 上年全年 + 本年至今 − 去年同期。这条公式已用全市场 3941 只对拍供应商 PE_TTM，" +
			"99.87% 落在 3% 以内。",
		"时点正确性靠 announce_date 门控：只使用公告日 ≤ 评估日的报告期。" +
			"本机该列 100% 有值，所以这不是近似。",
		"行业分类用的是数据湖的**混体系** industry_members：日更写的是 eastmoney（最近几个交易日、有中文名），" +
			"2020 起的历史是申万回填的（代码即名称）。本功能**选定一套体系**（默认取历史更长的申万）并用，" +
			"行业码统一带体系前缀（如 sw:340501）—— 两套体系的数字代码会撞车，混在时间轴上取会让行业序列断裂。",
		"行业历史分位是**时点正确重建**的：对每个行业快照日，用**该日生效的成分、该日生效的股本、" +
			"该日已披露的 TTM 业绩**重算行业加权 PE（Σ市值 ÷ Σ盈利）。" +
			"申万回填提供 2021 起 69 个可用快照日，所以「行业比它自己历史上贵还是便宜」现在真的是分位，不是横截面替代。",
		"行业阶段（成长/成熟/衰退/周期）与基本面行业分位依赖成分股的**营收增速与成长/质量分**，" +
			"而那些分只有被评估到的标的才有。所以评估范围取「前 N 只」时，多数行业只有少数成分带基本面分，" +
			"行业阶段会判不出来 —— 每个行业的说明里会写出「132 个成分里只有 2 个跑过评分」这类实话，" +
			"把范围调到「全市场」即可消除。",
		"行业基准（加权 PE / 中位 PE / 亏损占比 / 集中度）用**全市场**候选计算，" +
			"与「本次评估多少只」无关。否则把评估范围调成前 150 只时，" +
			"150 只摊到几十个行业会让多数行业成分不足而整块失效（相对估值恒为 0）。",
		"七类估值方法按行业与公司特征自动选择（PE / PB / PS / EV/EBITDA / DCF / PEG / 周期调整），" +
			"不适用时输出原因而不是硬算；EV/EBITDA 因本机没有折旧摊销科目而用营业利润近似。",
		"不同估值方法结论冲突时**不取平均**：会解释冲突原因并下调置信度。",
		"25 个需求点里，第 9 条（历史评估可追溯）与第 10 条（历史验证）在本机只能**部分满足**：" +
			"财务与行情可以完全时点正确，行业归属受 5 个快照限制。数据补齐后接口不用改，结论会自动升级。",
		"本评估不构成任何投资建议；评分低不等于不能涨，评分高也不等于不会跌。",
	}
}

func formatDate(date int64) string {
	if date <= 0 {
		return "—"
	}
	text := fmt.Sprintf("%08d", date)
	return text[:4] + "-" + text[4:6] + "-" + text[6:]
}

// dateMinusDays 把 YYYYMMDD 往前推 n 天。
func dateMinusDays(date int64, days int) int64 {
	if date <= 0 {
		return 0
	}
	t := time.Date(int(date/10000), time.Month(date/100%100), int(date%100), 0, 0, 0, 0, time.UTC)
	t = t.AddDate(0, 0, -days)
	return int64(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// valuationPointAt 取估值序列里日期 <= date 的最后一点。
func valuationPointAt(series []valuation.ValuationPoint, date int64) valuation.ValuationPoint {
	for i := len(series) - 1; i >= 0; i-- {
		if series[i].Date <= date {
			return series[i]
		}
	}
	return valuation.ValuationPoint{}
}

// annualROE 用最近 n 个年报期算平均 ROE（年报口径，避免用 YTD 的 ROE 列）。
func annualROE(asOf valuation.AsOf, years int) float64 {
	profits := valuation.AnnualValues(asOf.Reports, valuation.ItemNetProfit, years)
	equities := valuation.AnnualValues(asOf.Reports, valuation.ItemTotalEquity, years)
	count := len(profits)
	if len(equities) < count {
		count = len(equities)
	}
	if count == 0 {
		return 0
	}
	total := 0.0
	used := 0
	for i := 0; i < count; i++ {
		if equities[i] <= 0 {
			continue
		}
		total += profits[i] / equities[i]
		used++
	}
	if used == 0 {
		return 0
	}
	return total / float64(used)
}

// previousShares 取一年前的总股本（识别股本摊薄）。
func previousShares(currentDate int64, timeline cnlake.ShareTimeline) float64 {
	point, ok := timeline.At(dateMinusDays(currentDate, 365))
	if !ok {
		return 0
	}
	return point.TotalShares
}

// adjPointsFor 把数据湖的复权因子阶梯转成 valuation 的同形结构。
//
// 两者字段名一致，但类型不同包 —— Go 不允许跨包的命名类型直接赋值，
// 所以必须显式拷贝。放在这里是为了让 valuation 包不认识 cnlake。
func adjPointsFor(points []cnlake.AdjFactorPoint) []valuation.AdjFactorPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]valuation.AdjFactorPoint, len(points))
	for i, point := range points {
		out[i] = valuation.AdjFactorPoint{Date: point.Date, Factor: point.Factor}
	}
	return out
}

// shareTimelineFor 把数据湖的股本时间线转成 valuation 的同形结构。
func shareTimelineFor(timeline cnlake.ShareTimeline) valuation.ShareTimeline {
	if len(timeline) == 0 {
		return nil
	}
	out := make(valuation.ShareTimeline, len(timeline))
	for i, point := range timeline {
		out[i] = valuation.SharePoint{
			Date:        point.Date,
			TotalShares: point.TotalShares,
			FloatShares: point.FloatShares,
		}
	}
	return out
}

// indexBarDate 取指数行情行的交易日（YYYYMMDD）。
func indexBarDate(bar cnlake.IndexBarRow) int64 { return cnlake.BarDateValue(bar.TradeDate) }

var _ = context.Background
var _ sync.Mutex
