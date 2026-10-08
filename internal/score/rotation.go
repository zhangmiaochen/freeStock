package score

import "math"

// ---------------------------------------------------------------------------
// 轮动决策
//
// 1:1 移植自 portfolio_rotation_logic.build_rotation_plan（:1998-2728）。
//
// **如实照搬的几处反直觉之处**（不要"顺手改好"）：
//
//  1. `hold_for_rebound_exit` **从来不是 action 值** —— 它只是 decision_state
//     （优先级 6，action 仍然是 observe）。原项目在 action 映射表和 SQL 排序里
//     都为它留了位置（排序位 7），属于历史遗留。
//  2. `policy.ReduceScore` **在决策链中从未被使用**。唯一引用它的
//     `should_prefer_repair_hold` 从未被调用（repair_hold 被硬编码为 false）。
//     所以 `reduce` 只在"急涨兑现"和"配对 partial_rotate"两种情形出现。
//  3. `keep` 是**兜底**，不是"评分稳定"的判定 —— 未命中任何一条就落到 keep。
//  4. 低吸策略**永不换股**（配对循环里直接 continue）。
// ---------------------------------------------------------------------------

// 轮动动作。
const (
	ActionKeep       = "keep"
	ActionObserve    = "observe"
	ActionReduce     = "reduce"
	ActionClear      = "clear"
	ActionAdd        = "add"
	ActionAddLayer   = "add_layer"
	ActionReplaceIn  = "replace_in"
	ActionReplaceOut = "replace_out"
)

// 决策状态（落库到 rotation_signal.decision_state，不入 action）。
const (
	StateQuickProfitTake        = "quick_profit_take"
	StatePartialRotate          = "partial_rotate"
	StateFullReplace            = "full_replace"
	StateLowBuyBreadthClear     = "low_buy_breadth_clear"
	StateLowBuyTimingExit       = "low_buy_timing_exit"
	StateOversoldFailedExit     = "oversold_failed_exit"
	StateTrendBreakExit         = "trend_break_exit"
	StateHoldForReboundExit     = "hold_for_rebound_exit"
	StateReboundReduce          = "rebound_reduce"
	StateSingleLayerReboundExit = "single_layer_rebound_exit"
	StateReboundExit            = "rebound_exit"
	StateReboundObserve         = "rebound_observe"
	StateScoreObserve           = "score_observe"
	StateKeep                   = "keep"
)

// 原项目的模块级阈值常量。
const (
	ProfitProtectStartPct = 15.0 // 浮盈保护：涨幅阈值
	ProfitProtectRetrace  = 6.0  // 浮盈保护：从高点回撤阈值
	ExpertProtectStartPct = 8.0  // 高手模拟的更低阈值
	ExpertProtectRetrace  = 4.0
	MaBreakTolerance      = 0.005 // 跌破 MA30 的容差
)

// ActionOrder 是 rotation_rows 的展示顺序（原项目 SQL 的 CASE）。
//
// 注意 "hold_for_rebound_exit" 排在第 7 位 —— 它是**状态**不是动作，
// 这一项永远不会被 action 命中，属于历史遗留，如实保留。
var ActionOrder = map[string]int{
	ActionReplaceOut:        1,
	ActionReplaceIn:         2,
	ActionAdd:               3,
	ActionAddLayer:          4,
	ActionClear:             5,
	ActionReduce:            6,
	StateHoldForReboundExit: 7, // 遗留：永远不会命中
	ActionObserve:           8,
}

// ActionOrderOf 返回动作的展示序号，未知动作排最后。
func ActionOrderOf(action string) int {
	if order, ok := ActionOrder[action]; ok {
		return order
	}
	return 9
}

// ActionLabel 是动作的中文标签（原项目 _rotation_action_label）。
var actionLabels = map[string]string{
	ActionKeep:              "继续持有",
	ActionObserve:           "观察",
	ActionReduce:            "减仓",
	ActionClear:             "清仓",
	ActionAdd:               "建仓",
	ActionAddLayer:          "加仓",
	ActionReplaceIn:         "换入",
	ActionReplaceOut:        "换出",
	StateHoldForReboundExit: "等待反弹后处理", // 遗留映射
}

// ActionLabel 返回动作的中文标签。
func ActionLabel(action string) string {
	if label, ok := actionLabels[action]; ok {
		return label
	}
	return action
}

// ---------------------------------------------------------------------------
// 行数据
// ---------------------------------------------------------------------------

// Row 是一只股票的完整评分快照，持仓行与候选行共用。
//
// 字段名与 stock_strength_history / portfolio_position 合并后的宽表对应。
type Row struct {
	Code string `json:"tsCode"`
	Name string `json:"name"`

	// 评分
	TotalScore         float64 `json:"totalScore"`
	TrendScore         float64 `json:"trendScore"`
	StructureScore     float64 `json:"structureScore"`
	RSScore            float64 `json:"rsScore"`
	CapitalScore       float64 `json:"capitalScore"`
	RepairScore        float64 `json:"repairScore"`
	ResearchScore      float64 `json:"researchScore"`
	BoardResearchScore float64 `json:"boardResearchScore"`
	MainlineScore      float64 `json:"mainlineBoardScore"`

	// 均线与价格
	Close  float64 `json:"closePrice"`
	Open   float64 `json:"openPrice"`
	Ma5    float64 `json:"ma5Price"`
	Ma20   float64 `json:"ma20Price"`
	Ma30   float64 `json:"ma30Price"`
	Ma60   float64 `json:"ma60Price"`
	Ma120  float64 `json:"ma120Price"`
	Ma250  float64 `json:"ma250Price"`
	PctChg float64 `json:"pctChg"`

	// 强度与位置
	RelativeStrength20D float64 `json:"relativeStrength20d"`
	DrawdownFromHigh120 float64 `json:"drawdownFromHigh120"`
	DrawdownFromHigh20  float64 `json:"drawdownFromHigh20"`
	ReboundFromLow60    float64 `json:"reboundFromLow60"`
	StockReturn5D       float64 `json:"stockReturn5d"`
	StockReturn10D      float64 `json:"stockReturn10d"`
	VolumeRatio20       float64 `json:"volumeRatio20"`

	// 持仓专属
	PositionLayers   int     `json:"positionLayers"`
	EntryPrice       float64 `json:"entryPrice"`
	CurrentPrice     float64 `json:"currentPrice"`
	HighestPrice     float64 `json:"highestPrice"`
	ProfitLossPct    float64 `json:"profitLossPct"`
	HoldingTradeDays int     `json:"holdingTradeDays"`

	// 信号（原项目不入库，运行时 merge_asof 回填）
	SignalType  string  `json:"activeSignalType"`
	SignalAge   int     `json:"signalAge"`
	SignalScore float64 `json:"activeSignalScore"`
}

// hasFreshSignal 判断是否有"新鲜信号"。
func (r Row) hasFreshSignal(ageLimit int) bool {
	if r.SignalType != "buy" && r.SignalType != "candidate_buy" {
		return false
	}
	return r.SignalAge <= ageLimit
}

// isLowBuyStrategy 判断是否低吸策略。
func isLowBuyStrategy(strategyID string) bool {
	return strategyID == StrategyTrendLowBuy
}

// ---------------------------------------------------------------------------
// 各判定表达式（3.2.1）
// ---------------------------------------------------------------------------

// IsStructureBroken 判定结构破坏（任一为真即破坏）。
func IsStructureBroken(row Row, strategyID, marketState string, protectStart, protectRetrace float64) bool {
	// 1) 趋势成长总分过低
	if strategyID == StrategyTrendGrowth && row.TotalScore < 45.0 {
		return true
	}
	// 2) 趋势结构双弱，或结构弱且深回撤
	if (row.TrendScore <= 8 && row.StructureScore <= 4) ||
		(row.StructureScore <= 3 && row.DrawdownFromHigh120 <= -18) {
		return true
	}
	// 3) 趋势弱 + 相对弱 + 回撤深
	if row.TrendScore <= 10 && row.RelativeStrength20D <= -8 && row.DrawdownFromHigh120 <= -12 {
		return true
	}
	// 4) 跌破 MA120 且结构弱（MA120 > 0 才判）
	if row.Ma120 > 0 && row.Close < row.Ma120*0.965 && row.StructureScore <= 6 {
		return true
	}
	// 5) 跌破 MA60 且深回撤
	if row.Ma60 > 0 && row.Close < row.Ma60*0.94 && row.DrawdownFromHigh120 <= -18 {
		return true
	}
	// 6) 熊市里三者皆弱
	if marketState == StateBear && row.TrendScore <= 12 && row.StructureScore <= 5 && row.RSScore <= 10 {
		return true
	}
	// 7) 浮盈保护：涨过一波后从高点明显回撤
	if row.EntryPrice > 0 && row.CurrentPrice > 0 && row.HighestPrice > 0 {
		runup := (row.HighestPrice/row.EntryPrice - 1) * 100
		retrace := (row.CurrentPrice/row.HighestPrice - 1) * 100
		if runup >= protectStart && retrace <= -protectRetrace {
			return true
		}
	}
	return false
}

// IsFailedOversoldReboundHolding 判定超跌反弹持仓是否失败（仅超跌反弹策略）。
func IsFailedOversoldReboundHolding(row Row, marketState string) bool {
	if row.ProfitLossPct <= -4.5 {
		return true
	}
	if marketState != StateBull && row.ProfitLossPct <= -3.5 && row.TotalScore < 58 {
		return true
	}
	if row.Ma30 > 0 && row.Close < row.Ma30*0.94 && row.TotalScore < 58 {
		return true
	}
	if row.Ma20 > 0 && row.Close < row.Ma20*0.94 && row.PctChg < 0 && row.RelativeStrength20D < -2 {
		return true
	}
	return false
}

// CanHoldStrategyRepairPullback 判定是否可豁免结构破坏（仅趋势成长 / 高手模拟）。
func CanHoldStrategyRepairPullback(row Row, strategyID, marketState string) bool {
	switch strategyID {
	case StrategyTrendGrowth:
		if !(row.ProfitLossPct >= -6 && row.TotalScore >= 68 && row.TrendScore >= 19 &&
			row.StructureScore >= 9 && row.RSScore >= 7 &&
			row.RelativeStrength20D >= -2 && row.DrawdownFromHigh120 >= -28) {
			return false
		}
		if row.Ma120 > 0 && row.Close < row.Ma120*0.95 {
			return false
		}
		if row.Ma60 > 0 && row.Close < row.Ma60*0.955 {
			return false
		}
		return true
	case StrategyExpert:
		if !(row.ProfitLossPct >= -8 && row.TotalScore >= 62 && row.TrendScore >= 16 &&
			row.StructureScore >= 6 && row.RSScore >= 5 && row.DrawdownFromHigh120 >= -30) {
			return false
		}
		if marketState != StateBear && row.RelativeStrength20D < -8 {
			return false
		}
		if row.Ma120 > 0 && row.Close < row.Ma120*0.94 {
			return false
		}
		if row.Ma60 > 0 && row.Close < row.Ma60*0.945 {
			return false
		}
		return true
	}
	return false
}

// ShouldQuickProfitTake 判定是否急涨兑现。
//
// 注意原实现的写法的确是这样：`holding_days <= 0 or > 2 → False`，
// 也就是说持仓超过 2 个交易日就**不再**急涨兑现（不管第二条规则）。
func ShouldQuickProfitTake(row Row, marketState string) bool {
	if marketState == StateBull {
		return false
	}
	holdingDays := row.HoldingTradeDays
	if holdingDays <= 0 || holdingDays > 2 {
		return false
	}
	profit := row.ProfitLossPct
	ma20Gap := 0.0
	if row.Ma20 > 0 && row.CurrentPrice > 0 {
		ma20Gap = (row.CurrentPrice/row.Ma20 - 1) * 100
	}
	if profit >= 18.0 && ma20Gap >= 14.0 {
		return true
	}
	if holdingDays <= 3 && profit >= 15.0 && row.PctChg >= 9.5 {
		return true
	}
	return false
}

// NeedsReboundExit 判定是否需要等反弹再处理。
func NeedsReboundExit(row Row, strategyID, marketState string, protectStart, protectRetrace float64) bool {
	if isLowBuyStrategy(strategyID) {
		return false
	}
	if IsStructureBroken(row, strategyID, marketState, protectStart, protectRetrace) {
		return false
	}
	return row.Ma30 > 0 && row.Close < row.Ma30*(1-MaBreakTolerance)
}

// IsBigReboundCandle 判定是否大反弹阳线。
func IsBigReboundCandle(row Row) bool {
	if row.Open <= 0 {
		return false
	}
	bodyPct := (row.Close/row.Open - 1) * 100
	return row.PctChg >= 4.0 && bodyPct >= 2.5
}

// IsWeakSingleLayerStock 判定单层持仓是否是弱势股。
func IsWeakSingleLayerStock(row Row, policy StrategyConfig) bool {
	if row.PositionLayers != 1 {
		return false
	}
	threshold := math.Max(policy.ReadyScore, 78)
	strong := row.TotalScore >= threshold && row.TrendScore >= 23 &&
		row.StructureScore >= 13 && row.RSScore >= 11
	protected := row.EntryPrice <= 0 || row.CurrentPrice >= row.EntryPrice*0.995 || row.ProfitLossPct >= -0.5
	if strong && protected {
		return false
	}
	return true
}

// LowBuyExitReason 返回低吸策略的退出理由，空串表示不退出。
func LowBuyExitReason(row Row, cfg StrategyConfig) string {
	ratio := math.Max(0, cfg.LowBuyExitMa30Ratio)
	if row.Close > 0 && row.Ma30 > 0 && row.Close < row.Ma30*ratio {
		return "跌破MA30*" + trimFloat(ratio, 2) + "，低吸结构失效"
	}
	start := cfg.LowBuyProfitProtectStartPct
	retraceLimit := cfg.LowBuyProfitProtectRetracePct
	if start > 0 && retraceLimit > 0 &&
		row.EntryPrice > 0 && row.CurrentPrice > 0 && row.HighestPrice > 0 {
		runup := (row.HighestPrice/row.EntryPrice - 1) * 100
		retrace := (row.CurrentPrice/row.HighestPrice - 1) * 100
		if runup >= start && retrace <= -retraceLimit {
			return "低吸浮盈" + trimFloat(start, 0) + "%后从高点回撤" +
				trimFloat(retrace, 0) + "%，保护利润"
		}
	}
	return ""
}

// trimFloat 把浮点格式化成不带多余尾零的字符串（对齐 Python 的 :.2f / :.0f）。
func trimFloat(value float64, digits int) string {
	factor := math.Pow(10, float64(digits))
	rounded := math.Round(value*factor) / factor
	text := ""
	if digits == 0 {
		text = itoa(int(rounded))
	} else {
		text = ftoa(rounded, digits)
	}
	return text
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(v float64, digits int) string {
	factor := math.Pow(10, float64(digits))
	scaled := int(math.Round(v * factor))
	neg := scaled < 0
	if neg {
		scaled = -scaled
	}
	whole := scaled / int(factor)
	frac := scaled % int(factor)
	text := itoa(whole) + "."
	divisor := int(factor) / 10
	for divisor > 0 {
		text += string(rune('0' + (frac/divisor)%10))
		divisor /= 10
	}
	if neg {
		text = "-" + text
	}
	return text
}

// ---------------------------------------------------------------------------
// 计划
// ---------------------------------------------------------------------------

// ReplacementPair 是一次换股配对。
type ReplacementPair struct {
	HoldingCode   string
	HoldingName   string
	CandidateCode string
	CandidateName string
	Mode          string // partial_rotate / full_replace
	// 对比数据
	HoldingScore   float64
	CandidateScore float64
	TotalScoreGap  float64
	RSScoreGap     float64
	RepairScoreGap float64
	MainReason     string
	ExpectedUplift float64
	RankNo         int
}

// RotationRow 是一条调仓动作。
type RotationRow struct {
	Code        string  `json:"tsCode"`
	Name        string  `json:"name"`
	Action      string  `json:"action"`
	ActionLabel string  `json:"actionLabel"`
	Score       float64 `json:"score"`
	ScoreGap    float64 `json:"scoreGap"`
	RankNo      int     `json:"rankNo"`

	CandidateCode       string  `json:"candidateTsCode"`
	CandidateName       string  `json:"candidateName"`
	CandidateScore      float64 `json:"candidateScore"`
	ExpectedImprovement float64 `json:"expectedImprovement"`

	Reason        string `json:"reason"`
	SourceType    string `json:"sourceType"`
	CurrentLayers int    `json:"currentLayers"`
	TargetLayers  int    `json:"targetLayers"`
	LayersDelta   int    `json:"layersDelta"`
	DecisionState string `json:"decisionState"`

	// 执行语义
	IsExecutable         bool   `json:"isExecutable"`
	RequiresConfirmation bool   `json:"requiresConfirmation"`
	ExecutionMode        string `json:"executionMode"`
	ExecutionModeLabel   string `json:"executionModeLabel"`
	ActionPrefix         string `json:"actionPrefix"`
}

// RotationPlan 是一次完整的轮动计划。
type RotationPlan struct {
	Rows         []RotationRow     `json:"rows"`
	Compare      []ReplacementPair `json:"compare"`
	Candidates   []Row             `json:"candidates"`
	MarketState  string            `json:"marketState"`
	StrategyID   string            `json:"strategyId"`
	EntryAllowed bool              `json:"entryAllowed"`
	Notes        []string          `json:"notes"`
}

// RotationInput 是轮动计划的输入。
type RotationInput struct {
	Holdings    []Row
	Candidates  []Row
	Config      StrategyConfig
	MarketState string
	// MaxStockLimit 是持仓只数上限（对应组合的 max_stock_count）
	MaxStockLimit int
	// LowBuyBreadthReadyCount 是低吸广度就绪数
	LowBuyBreadthReadyCount int
}

// BuildRotationPlan 生成轮动计划。
func BuildRotationPlan(in RotationInput) RotationPlan {
	cfg := in.Config
	state := in.MarketState
	if _, ok := StateLabels[state]; !ok {
		state = StateRange
	}
	strategyID := cfg.StrategyID
	lowBuy := isLowBuyStrategy(strategyID)

	plan := RotationPlan{
		MarketState: state,
		StrategyID:  strategyID,
	}

	// 浮盈保护阈值：高手模拟用更低的 8/4
	protectStart, protectRetrace := ProfitProtectStartPct, ProfitProtectRetrace
	if strategyID == StrategyExpert {
		protectStart, protectRetrace = ExpertProtectStartPct, ExpertProtectRetrace
	}

	// ---- 换股配对（3.1）----
	pairs, usedCandidate := buildReplacementPairs(in, protectStart, protectRetrace)
	plan.Compare = pairs

	// 持仓 → 配对 的索引
	pairByHolding := map[string]int{}
	for i, pair := range pairs {
		pairByHolding[pair.HoldingCode] = i
	}
	// rank_no 从配对数之后开始给后续的 add 用
	nextRank := len(pairs) + 1

	// ---- 逐持仓判定（3.2）----
	lowBuyForceClear := lowBuy &&
		in.LowBuyBreadthReadyCount < cfg.LowBuyClearReadyCount

	for _, row := range in.Holdings {
		out := RotationRow{
			Code: row.Code, Name: row.Name,
			Score:              row.TotalScore,
			CurrentLayers:      row.PositionLayers,
			TargetLayers:       row.PositionLayers,
			SourceType:         "holding",
			ExecutionMode:      "",
			ExecutionModeLabel: "",
		}

		pairIdx, paired := pairByHolding[row.Code]

		switch {
		// 优先级 1：急涨兑现
		case ShouldQuickProfitTake(row, state):
			if row.PositionLayers >= 2 {
				out.Action = ActionReduce
				out.TargetLayers = row.PositionLayers - 1
			} else {
				out.Action = ActionClear
				out.TargetLayers = 0
			}
			out.DecisionState = StateQuickProfitTake
			out.Reason = "急涨兑现，锁定利润"

		// 优先级 2：配对 → 减仓 / 换出
		case paired:
			pair := pairs[pairIdx]
			if pair.Mode == "partial_rotate" {
				out.Action = ActionReduce
				out.TargetLayers = int(math.Max(1, float64(row.PositionLayers-1)))
				out.DecisionState = StatePartialRotate
			} else {
				out.Action = ActionReplaceOut
				out.TargetLayers = 0
				out.DecisionState = StateFullReplace
			}
			out.CandidateCode = pair.CandidateCode
			out.CandidateName = pair.CandidateName
			out.CandidateScore = pair.CandidateScore
			out.ScoreGap = pair.TotalScoreGap
			out.ExpectedImprovement = pair.ExpectedUplift
			out.RankNo = pair.RankNo
			out.Reason = pair.MainReason

		// 优先级 3：低吸广度不足，强制清仓
		case lowBuyForceClear:
			out.Action = ActionClear
			out.TargetLayers = 0
			out.DecisionState = StateLowBuyBreadthClear
			out.Reason = "低吸广度不足（就绪数 " + itoa(in.LowBuyBreadthReadyCount) +
				" < " + itoa(cfg.LowBuyClearReadyCount) + "），清仓"

		// 优先级 4：低吸专属退出
		case lowBuy && LowBuyExitReason(row, cfg) != "":
			out.Action = ActionClear
			out.TargetLayers = 0
			out.DecisionState = StateLowBuyTimingExit
			out.Reason = LowBuyExitReason(row, cfg)

		// 优先级 5：严重破坏
		case severeBreak(row, strategyID, state, protectStart, protectRetrace):
			out.Action = ActionClear
			out.TargetLayers = 0
			if strategyID == StrategyOversold {
				out.DecisionState = StateOversoldFailedExit
				out.Reason = "超跌反弹失败，清仓"
			} else {
				out.DecisionState = StateTrendBreakExit
				out.Reason = "结构破坏且不可修复，清仓"
			}

		// 优先级 6：需要等反弹，但今天不是大反弹阳线 → 观察
		case NeedsReboundExit(row, strategyID, state, protectStart, protectRetrace) &&
			!IsBigReboundCandle(row):
			out.Action = ActionObserve
			out.DecisionState = StateHoldForReboundExit
			out.Reason = "跌破MA30，等待反弹后处理"

		// 优先级 7：大反弹阳线
		case NeedsReboundExit(row, strategyID, state, protectStart, protectRetrace) &&
			IsBigReboundCandle(row):
			switch {
			case row.PositionLayers >= 2:
				out.Action = ActionReduce
				out.TargetLayers = row.PositionLayers - 1
				out.DecisionState = StateReboundReduce
				out.Reason = "大反弹阳线，减一层"
			case IsWeakSingleLayerStock(row, cfg):
				out.Action = ActionClear
				out.TargetLayers = 0
				out.DecisionState = StateSingleLayerReboundExit
				out.Reason = "大反弹阳线但个股弱势，清仓"
			case row.TotalScore < cfg.ClearScore:
				out.Action = ActionClear
				out.TargetLayers = 0
				out.DecisionState = StateReboundExit
				out.Reason = "大反弹阳线但评分低于清仓线，清仓"
			default:
				out.Action = ActionObserve
				out.DecisionState = StateReboundObserve
				out.Reason = "大反弹阳线，观察"
			}

		// 优先级 8：评分低于预警线 → 观察
		case row.TotalScore < cfg.WarningScore:
			out.Action = ActionObserve
			out.DecisionState = StateScoreObserve
			out.Reason = "评分低于预警线，观察"

		// 优先级 9：兜底
		default:
			out.Action = ActionKeep
			out.DecisionState = StateKeep
			out.Reason = "评分稳定，继续持有"
		}

		out.LayersDelta = out.TargetLayers - out.CurrentLayers
		plan.Rows = append(plan.Rows, out)
	}

	// ---- 换入行（与换出行同号，保证同组排序相邻）----
	for _, pair := range pairs {
		plan.Rows = append(plan.Rows, RotationRow{
			Code: pair.CandidateCode, Name: pair.CandidateName,
			Action: ActionReplaceIn, ActionLabel: ActionLabel(ActionReplaceIn),
			Score:               pair.CandidateScore,
			ScoreGap:            pair.TotalScoreGap,
			RankNo:              pair.RankNo,
			CandidateCode:       pair.HoldingCode,
			CandidateName:       pair.HoldingName,
			CandidateScore:      pair.HoldingScore,
			ExpectedImprovement: pair.ExpectedUplift,
			Reason:              "换入更强标的",
			SourceType:          "candidate",
			CurrentLayers:       0,
			TargetLayers:        pair.LayersForCandidate(),
			DecisionState:       StateFullReplace,
		})
	}

	// ---- 建仓（候选池里没被换股用掉的）----
	sortCandidates(in.Candidates)
	for _, candidate := range in.Candidates {
		if usedCandidate[candidate.Code] {
			continue
		}
		ok, reason := CandidateQualityOK(candidate, cfg, state, false)
		if !ok {
			continue
		}
		target := initialEntryLayers(candidate, cfg, 0)
		plan.Rows = append(plan.Rows, RotationRow{
			Code: candidate.Code, Name: candidate.Name,
			Action: ActionAdd, ActionLabel: ActionLabel(ActionAdd),
			Score:         candidate.TotalScore,
			RankNo:        nextRank,
			Reason:        reason,
			SourceType:    "candidate",
			CurrentLayers: 0,
			TargetLayers:  target,
			DecisionState: "new_entry",
		})
		nextRank++
	}

	// ---- 广度闸门（3.7）----
	plan.EntryAllowed = true
	if !lowBuy && len(in.Candidates) > 0 {
		plan.EntryAllowed = breadthEntryAllowed(in.Candidates, strategyID, len(in.Candidates))
	}
	if !plan.EntryAllowed {
		plan.Rows = applyBreadthGate(plan.Rows, &plan)
	}

	// ---- 排序 + 执行语义 ----
	sortRotationRows(plan.Rows)
	applyBehavior(&plan.Rows, cfg)
	plan.Notes = buildNotes(plan, in)
	return plan
}

// LayersForCandidate 返回换入时应该给几层。
func (p ReplacementPair) LayersForCandidate() int { return 1 }

// severeBreak 对应优先级 5 的 severe_break。
func severeBreak(row Row, strategyID, state string, protectStart, protectRetrace float64) bool {
	if isLowBuyStrategy(strategyID) {
		// 低吸的严重破坏由低吸自己的退出逻辑负责（优先级 3/4）
		return false
	}
	if strategyID == StrategyOversold {
		// 超跌反弹：只有"反弹失败"才算严重破坏
		return IsFailedOversoldReboundHolding(row, state)
	}
	broken := IsStructureBroken(row, strategyID, state, protectStart, protectRetrace)
	if !broken {
		return false
	}
	// 结构破坏但满足"可修复回踩"条件时豁免
	return !CanHoldStrategyRepairRepair(row, strategyID, state)
}

// CanHoldStrategyRepairRepair 是内部包装（名称避免与公开函数混淆）。
func CanHoldStrategyRepairRepair(row Row, strategyID, state string) bool {
	return CanHoldStrategyRepairPullback(row, strategyID, state)
}

// buildNotes 生成计划说明（对应原项目的 note/notes）。
func buildNotes(plan RotationPlan, in RotationInput) []string {
	var notes []string
	counts := map[string]int{}
	for _, row := range plan.Rows {
		counts[row.Action]++
	}
	if counts[ActionReplaceOut] > 0 {
		notes = append(notes, "换股 "+itoa(counts[ActionReplaceOut])+" 组")
	}
	if counts[ActionClear] > 0 {
		notes = append(notes, "清仓 "+itoa(counts[ActionClear])+" 只")
	}
	if counts[ActionAdd] > 0 {
		notes = append(notes, "建仓 "+itoa(counts[ActionAdd])+" 只")
	}
	if !plan.EntryAllowed {
		notes = append(notes, "市场广度不足，已暂停建仓与加仓")
	}
	return notes
}

// applyBehavior 填充执行语义（3.8）。
func applyBehavior(rows *[]RotationRow, cfg StrategyConfig) {
	mode := executionModesFor(cfg.StrategyID)
	for i := range *rows {
		row := &(*rows)[i]
		row.ActionLabel = ActionLabel(row.Action)
		row.ExecutionMode = mode.mode
		row.ExecutionModeLabel = mode.modeLabel
		row.IsExecutable = mode.allowApply && isActionExecutable(row.Action)
		row.RequiresConfirmation = mode.requireConfirm && row.IsExecutable
		if row.Action != ActionKeep && row.Action != ActionObserve {
			row.ActionPrefix = mode.prefix
		}
	}
}

// isActionExecutable 判断动作是否可被"一键应用"执行。
func isActionExecutable(action string) bool {
	switch action {
	case ActionClear, ActionReplaceOut, ActionReduce, ActionAdd, ActionAddLayer, ActionReplaceIn:
		return true
	}
	return false
}

type behaviorMode struct {
	mode           string
	modeLabel      string
	prefix         string
	allowApply     bool
	requireConfirm bool
}

// executionModesFor 按组合类型给出执行语义。
//
// 注意：原项目这里读的是**组合的 portfolio_type**，不是策略 ID。
// 因为在 score 包里拿不到组合类型，这里退化用策略 ID 无法区分 ——
// 调用方（service 层）应当在拿到行之后用组合类型覆盖这三个字段。
func executionModesFor(strategyID string) behaviorMode {
	_ = strategyID
	// 默认按"半自动"给（最保守的可执行组合），调用方会用组合类型覆盖
	return behaviorMode{
		mode: "confirm_required", modeLabel: "人工确认后执行",
		prefix: "待确认", allowApply: true, requireConfirm: true,
	}
}

// sortRotationRows 按动作顺序 + rank_no + 分数排序（原项目 SQL 的排序键）。
func sortRotationRows(rows []RotationRow) {
	// 稳定的插入排序，避免引入 sort 包的开销（行数很小）
	for i := 1; i < len(rows); i++ {
		current := rows[i]
		j := i - 1
		for j >= 0 && lessRotation(current, rows[j]) {
			rows[j+1] = rows[j]
			j--
		}
		rows[j+1] = current
	}
}

// lessRotation 判断 a 是否应排在 b 前面。
func lessRotation(a, b RotationRow) bool {
	oa, ob := ActionOrderOf(a.Action), ActionOrderOf(b.Action)
	if oa != ob {
		return oa < ob
	}
	switch {
	case a.RankNo != b.RankNo:
		return a.RankNo < b.RankNo
	case a.Score != b.Score:
		return a.Score > b.Score
	}
	return a.Code < b.Code
}
