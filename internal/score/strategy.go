package score

import "math"

// ---------------------------------------------------------------------------
// 市场状态
//
// 1:1 移植自 volume_price_market_state.build_market_state_cache。
//
// **反直觉之处**：市场状态不是用基准指数（沪深300）的趋势判定的，而是用
// **全市场宽度** —— 按日聚合所有股票的平均涨跌幅、上涨/下跌家数占比、成交额，
// 合成一个"代理指数"，再看它最近 20 日 vs 前 20 日的高低点结构。
// ---------------------------------------------------------------------------

// 市场状态取值。
const (
	StateBull  = "bull"
	StateRange = "range"
	StateBear  = "bear"
)

// StateLabels 是市场状态的中文标签（原项目同名常量）。
var StateLabels = map[string]string{
	StateBull:  "牛市/趋势扩张",
	StateRange: "震荡/轮动",
	StateBear:  "熊市/风险释放",
}

// 市场状态判定的窗口参数。
const (
	marketWindow  = 80 // 最长回看交易日
	marketMinDays = 20 // 少于此天数直接判为 range
	marketRecent  = 20 // 结构比较用的近期窗口
	marketShort   = 5  // 近 5 日涨跌占比
)

// MarketDay 是某一天的全市场聚合结果。
type MarketDay struct {
	Date        int64
	AvgPct      float64 // 全市场平均涨跌幅 %
	UpRatio     float64 // 上涨家数占比 0..1
	DownRatio   float64 // 下跌家数占比 0..1
	TotalAmount float64 // 全市场成交额合计
}

// MarketState 是判定结果。
type MarketState struct {
	State      string  `json:"marketState"`
	Label      string  `json:"marketStateLabel"`
	BullScore  float64 `json:"bullScore"`
	BearScore  float64 `json:"bearScore"`
	ProxyRet20 float64 `json:"proxyRet20"`
	Days       int     `json:"days"`
}

// BuildMarketState 用全市场聚合数据判定市场状态。
//
// days 按日期升序。少于 marketMinDays 天时判为 range。
func BuildMarketState(days []MarketDay) MarketState {
	out := MarketState{State: StateRange, Label: StateLabels[StateRange]}
	if len(days) < marketMinDays {
		return out
	}

	window := days
	if len(window) > marketWindow {
		window = window[len(window)-marketWindow:]
	}
	out.Days = len(window)

	// 代理指数：把每日平均涨跌幅累乘成一条净值曲线
	proxy := make([]float64, len(window))
	level := 1.0
	for i, day := range window {
		level *= 1 + day.AvgPct/100
		proxy[i] = level
	}

	recent := marketRecent
	if recent > len(proxy) {
		recent = len(proxy)
	}
	last := proxy[len(proxy)-1]
	previous := proxy[len(proxy)-recent]

	// 后 20 日 vs 前 20 日的高低点结构
	recentHigh := -math.MaxFloat64
	recentLow := math.MaxFloat64
	previousHigh := -math.MaxFloat64
	previousLow := math.MaxFloat64
	for i := len(proxy) - recent; i < len(proxy); i++ {
		recentHigh = math.Max(recentHigh, proxy[i])
		recentLow = math.Min(recentLow, proxy[i])
	}
	for i := len(proxy) - 2*recent; i < len(proxy)-recent; i++ {
		if i < 0 {
			continue
		}
		previousHigh = math.Max(previousHigh, proxy[i])
		previousLow = math.Min(previousLow, proxy[i])
	}
	// 前一段不足时退化为整段
	if previousHigh == -math.MaxFloat64 {
		previousHigh, previousLow = recentHigh, recentLow
	}

	higher := boolToFloat(recentHigh > previousHigh && recentLow > previousLow)
	lower := boolToFloat(recentHigh < previousHigh && recentLow < previousLow)

	// 代理指数 20 日收益
	ret20 := 0.0
	if previous > 0 {
		ret20 = (last/previous - 1) * 100
	}
	out.ProxyRet20 = round(ret20, 4)

	// 近 5 日的涨跌占比
	short := marketShort
	if short > len(window) {
		short = len(window)
	}
	up5 := mean(ratioOf(window, func(d MarketDay) float64 { return d.UpRatio }), len(window)-short, len(window))
	down5 := mean(ratioOf(window, func(d MarketDay) float64 { return d.DownRatio }), len(window)-short, len(window))

	// 成交额增速（近 5 日 vs 前 5 日）
	amounts := ratioOf(window, func(d MarketDay) float64 { return d.TotalAmount })
	amtGrowth := 0.0
	if len(amounts) >= 2*short {
		recentAmt := mean(amounts, len(amounts)-short, len(amounts))
		prevAmt := mean(amounts, len(amounts)-2*short, len(amounts)-short)
		if prevAmt > 0 {
			amtGrowth = (recentAmt/prevAmt - 1) * 100
		}
	}

	bull := 45 + 25*higher + 15*boolToFloat(up5 >= 0.55) + 10*boolToFloat(amtGrowth >= 5)
	if ret20 >= 5 {
		bull += 20
	} else {
		bull += math.Max(0, ret20*2)
	}

	bear := 45 + 25*lower + 15*boolToFloat(down5 >= 0.55)
	if ret20 <= -5 {
		bear += 20
	} else {
		bear += math.Max(0, math.Abs(math.Min(ret20, 0))*2)
	}

	out.BullScore = round(bull, 4)
	out.BearScore = round(bear, 4)

	switch {
	case bull >= 75 && bull >= bear+10:
		out.State = StateBull
	case bear >= 75 && bear >= bull+10:
		out.State = StateBear
	default:
		out.State = StateRange
	}
	out.Label = StateLabels[out.State]
	return out
}

func ratioOf(days []MarketDay, pick func(MarketDay) float64) []float64 {
	out := make([]float64, len(days))
	for i, day := range days {
		out[i] = pick(day)
	}
	return out
}

// ---------------------------------------------------------------------------
// 策略配置
// ---------------------------------------------------------------------------

// 策略 ID（与 portfolio 包的常量一一对应，这里重复定义是为了不让 score 反向依赖）。
const (
	StrategyTrendGrowth = "trend_growth"
	StrategyTrendLowBuy = "trend_pullback_low_buy"
	StrategyOversold    = "oversold_rebound"
	StrategyExpert      = "expert_simulation"
)

// StrategyConfig 是策略在**特定市场状态**下的运行参数。
//
// 原项目里这些参数不落库，每次由 build_strategy_config() 现算。
type StrategyConfig struct {
	StrategyID  string `json:"strategyId"`
	MarketState string `json:"marketState"`
	TotalLayers int    `json:"totalLayers"`
	TargetCount int    `json:"targetPositionCount"`

	ReadyScore             float64 `json:"readyScore"`
	CandidateScoreFloor    float64 `json:"candidateScoreFloor"`
	MaxLayersPerStock      int     `json:"maxLayersPerStock"`
	AllowNewAdds           bool    `json:"allowNewAdds"`
	RequireCandidateSignal bool    `json:"requireCandidateSignal"`

	// 以下三态同值（原项目如此）
	ReplaceScoreGap float64 `json:"replaceScoreGap"`
	WarningScore    float64 `json:"warningScore"`
	ReduceScore     float64 `json:"reduceScore"`
	ClearScore      float64 `json:"clearScore"`
	MaxReplaceCount int     `json:"maxReplaceCount"`

	// 低吸策略专属
	LowBuyScopeKey                string  `json:"lowBuyScopeKey,omitempty"`
	LowBuySortMode                string  `json:"lowBuySortMode,omitempty"`
	LowBuyPositionMode            string  `json:"lowBuyPositionMode,omitempty"`
	LowBuyExitMa30Ratio           float64 `json:"lowBuyExitMa30Ratio,omitempty"`
	LowBuyClearReadyCount         int     `json:"lowBuyClearReadyCount,omitempty"`
	LowBuyDailyMaxNewAdds         int     `json:"lowBuyDailyMaxNewAdds,omitempty"`
	LowBuyProfitProtectStartPct   float64 `json:"lowBuyProfitProtectStartPct,omitempty"`
	LowBuyProfitProtectRetracePct float64 `json:"lowBuyProfitProtectRetracePct,omitempty"`
}

// readyScoreTable 是各策略在三种市场状态下的建仓达标分。
//
// 原项目一个反直觉之处：**熊市里 trend_growth / trend_low_buy 的达标分不升反降**
// （66→68 只是 range/bear 相同），而 allow_new_adds 在熊市被强制覆盖成 true。
var readyScoreTable = map[string]map[string]float64{
	StrategyTrendGrowth: {StateBull: 66, StateRange: 68, StateBear: 68},
	StrategyTrendLowBuy: {StateBull: 68, StateRange: 68, StateBear: 68},
	StrategyOversold:    {StateBull: 70, StateRange: 70, StateBear: 74},
	StrategyExpert:      {StateBull: 62, StateRange: 64, StateBear: 64},
}

// candidateFloorTable 是候选池的分数下限。
//
// **低吸策略是 0** —— 因为它靠自己的结构条件筛选，不靠总分。
var candidateFloorTable = map[string]float64{
	StrategyTrendGrowth: 60,
	StrategyTrendLowBuy: 0,
	StrategyOversold:    68,
	StrategyExpert:      58,
}

// lowBuyProfile 是低吸策略按 market_scope 选的参数档。
type lowBuyProfile struct {
	SortMode        string
	PositionMode    string
	ExitMa30Ratio   float64
	ProtectStartPct float64
	ProtectRetrace  float64
}

// lowBuyProfiles 对应原项目的 LOW_BUY_PROFILES。
// 按 market_scope 选档：main / all / main_star / gem / gem_star / star / main_gem(默认)。
var lowBuyProfiles = map[string]lowBuyProfile{
	"main":      {SortMode: "amount_entry", PositionMode: "slow1000", ExitMa30Ratio: 0.94},
	"all":       {SortMode: "amount_entry", PositionMode: "slow1000", ExitMa30Ratio: 0.94},
	"main_star": {SortMode: "amount_entry", PositionMode: "slow1000", ExitMa30Ratio: 0.94},
	"main_gem":  {SortMode: "amount_entry", PositionMode: "slow1000", ExitMa30Ratio: 0.96},
	"gem":       {SortMode: "deep_amount", PositionMode: "slow1000", ExitMa30Ratio: 0.96},
	"star":      {SortMode: "board_norm_amount", PositionMode: "conservative1200", ExitMa30Ratio: 0.96},
	"gem_star": {SortMode: "deep_amount", PositionMode: "slow1000", ExitMa30Ratio: 0.96,
		ProtectStartPct: 30, ProtectRetrace: 10},
}

// 低吸策略的其他常量。
const (
	LowBuyClearReadyCount = 900
	LowBuyDailyMaxNewAdds = 1
	LowBuyDefaultScopeKey = "main_gem"
)

// BuildStrategyConfig 现算策略参数。
//
// marketScope 为空时用 main_gem（低吸的默认档）。
func BuildStrategyConfig(strategyID, marketState string, targetCount, totalLayers, maxLayersPerStock int, marketScope string) StrategyConfig {
	if _, ok := readyScoreTable[strategyID]; !ok {
		strategyID = StrategyTrendGrowth
	}
	if _, ok := StateLabels[marketState]; !ok {
		marketState = StateRange
	}
	if targetCount < 1 {
		targetCount = 5
	}
	if totalLayers < 1 {
		totalLayers = 10
	}

	cfg := StrategyConfig{
		StrategyID:  strategyID,
		MarketState: marketState,
		TotalLayers: totalLayers,
		TargetCount: targetCount,

		ReadyScore:          readyScoreTable[strategyID][marketState],
		CandidateScoreFloor: candidateFloorTable[strategyID],
		// 三态同值 —— 原项目如此
		ReplaceScoreGap:        20,
		WarningScore:           60,
		ReduceScore:            45,
		ClearScore:             35,
		RequireCandidateSignal: false,
	}
	cfg.MaxReplaceCount = maxReplaceCount(targetCount)

	// 单股最大层数：低吸策略 = total_layers，其余默认 3
	cfg.MaxLayersPerStock = maxLayersPerStock
	if cfg.MaxLayersPerStock <= 0 {
		cfg.MaxLayersPerStock = 3
	}
	if strategyID == StrategyTrendLowBuy {
		cfg.MaxLayersPerStock = totalLayers
	}

	// 是否允许加新仓：熊市里 trend_growth / trend_low_buy 被**强制覆盖成 true**
	switch strategyID {
	case StrategyTrendGrowth, StrategyTrendLowBuy:
		cfg.AllowNewAdds = true
	case StrategyOversold, StrategyExpert:
		cfg.AllowNewAdds = marketState != StateBear
	}

	// 低吸专属参数
	if strategyID == StrategyTrendLowBuy {
		key := marketScope
		profile, ok := lowBuyProfiles[key]
		if !ok {
			key = LowBuyDefaultScopeKey
			profile = lowBuyProfiles[key]
		}
		cfg.LowBuyScopeKey = key
		cfg.LowBuySortMode = profile.SortMode
		cfg.LowBuyPositionMode = profile.PositionMode
		cfg.LowBuyExitMa30Ratio = profile.ExitMa30Ratio
		cfg.LowBuyClearReadyCount = LowBuyClearReadyCount
		cfg.LowBuyDailyMaxNewAdds = LowBuyDailyMaxNewAdds
		cfg.LowBuyProfitProtectStartPct = profile.ProtectStartPct
		cfg.LowBuyProfitProtectRetracePct = profile.ProtectRetrace
	}
	return cfg
}

// maxReplaceCount = max(1, min(3, target//2))，目标数 <4 时为 1。
func maxReplaceCount(target int) int {
	if target < 4 {
		return 1
	}
	count := target / 2
	if count > 3 {
		count = 3
	}
	if count < 1 {
		count = 1
	}
	return count
}

// RotationPolicy 是**没有** strategy_config 时使用的兜底轮动阈值。
//
// 注意：正常链路走的是 StrategyConfig，这个策略只在 strategy_config 为空时用。
// 两者的阈值**不一样**（例如 bear 的 ready 是 78 而 StrategyConfig 是 68），
// 原项目如此，如实保留。
type RotationPolicy struct {
	MarketState     string  `json:"marketState"`
	ReadyScore      float64 `json:"readyScore"`
	ReplaceScoreGap float64 `json:"replaceScoreGap"`
	WarningScore    float64 `json:"warningScore"`
	ReduceScore     float64 `json:"reduceScore"`
	ClearScore      float64 `json:"clearScore"`
	MaxReplaceCount int     `json:"maxReplaceCount"`
	DesiredPosition int     `json:"desiredPosition"`
}

type policyRow struct {
	ready, gap, warning, reduce, clear float64
	maxReplace                         int
}

var policyTable = map[string]policyRow{
	StateBull:  {ready: 76, gap: 18, warning: 60, reduce: 52, clear: 45, maxReplace: 2},
	StateRange: {ready: 74, gap: 24, warning: 60, reduce: 55, clear: 48, maxReplace: 1},
	StateBear:  {ready: 78, gap: 26, warning: 65, reduce: 58, clear: 52, maxReplace: 1},
}

// BuildRotationPolicy 现算兜底轮动阈值。targetCount 仅用于算熊市的目标仓位。
func BuildRotationPolicy(marketState string, targetCount int) RotationPolicy {
	row, ok := policyTable[marketState]
	if !ok {
		marketState = StateRange
		row = policyTable[StateRange]
	}
	if targetCount < 1 {
		targetCount = 5
	}
	desired := targetCount
	if marketState == StateBear {
		// 熊市目标仓位 = (target+1)//2，即砍一半（向上取整）
		desired = (targetCount + 1) / 2
	}
	return RotationPolicy{
		MarketState: marketState, ReadyScore: row.ready, ReplaceScoreGap: row.gap,
		WarningScore: row.warning, ReduceScore: row.reduce, ClearScore: row.clear,
		MaxReplaceCount: row.maxReplace, DesiredPosition: desired,
	}
}

// LowBuyPositionLayers 按低吸的仓位档把"广度就绪数"映射成建仓层数。
//
// slow1000:       1000→1, 1500→2, 2000→3, 2600→5
// conservative1200: 1200→1, 1800→2, 2400→3, 3200→5
func LowBuyPositionLayers(mode string, breadthReadyCount int) int {
	var steps [][2]int
	switch mode {
	case "conservative1200":
		steps = [][2]int{{1200, 1}, {1800, 2}, {2400, 3}, {3200, 5}}
	default: // slow1000
		steps = [][2]int{{1000, 1}, {1500, 2}, {2000, 3}, {2600, 5}}
	}
	layers := 0
	for _, step := range steps {
		if breadthReadyCount >= step[0] {
			layers = step[1]
		}
	}
	return layers
}
