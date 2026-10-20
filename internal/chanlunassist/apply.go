package chanlunassist

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"freestock/internal/chanlun"
	"freestock/internal/score"
)

// Event 是一次缠论辅助动作（用于报告与逐笔归因）。
type Event struct {
	Date         int    `json:"date"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // buy_boost / sell_reduce
	SignalType   string `json:"signalType"`
	SignalTypeCN string `json:"signalTypeCN"`
	ConfirmDate  int    `json:"confirmDate"`
	AgeBars      int    `json:"ageBars"`
	FromLayers   int    `json:"fromLayers"`
	ToLayers     int    `json:"toLayers"`
	DeltaLayers  int    `json:"deltaLayers"`
	Reason       string `json:"reason"`
}

// Stats 是辅助层在整个回测期内的累计统计。
type Stats struct {
	// 买入侧
	BuyRowsExamined int `json:"buyRowsExamined"`
	Buy2Boosts      int `json:"buy2Boosts"`
	Buy3Boosts      int `json:"buy3Boosts"`
	BuyLayersAdded  int `json:"buyLayersAdded"`
	// HoldBoosts 是其中「持仓中被缠论信号加层」的次数（BoostHoldings=true 时才有）。
	HoldBoosts int `json:"holdBoosts"`
	// 卖出侧
	SellRowsExamined int `json:"sellRowsExamined"`
	// SellSignalsSeen 是「持仓行上出现过新鲜卖出信号」的次数（含基计划已清仓的）。
	// 与 SellRowsExamined 对比能看出信号到底少，还是被基计划抢先卖掉了。
	SellSignalsSeen int `json:"sellSignalsSeen"`
	// SellSignalsOnExit 是「有卖出信号、但基计划当天已经清仓/换出」的次数。
	SellSignalsOnExit int `json:"sellSignalsOnExit"`
	Sell1Reduces      int `json:"sell1Reduces"`
	Sell2Reduces      int `json:"sell2Reduces"`
	Sell3Reduces      int `json:"sell3Reduces"`
	SellLayersSold    int `json:"sellLayersSold"`
	// 跳过原因
	SkippedCapped      int     `json:"skippedCapped"`      // 撞单股层数上限
	SkippedConflict    int     `json:"skippedConflict"`    // 同日有反向信号，风险优先
	SkippedDuplicate   int     `json:"skippedDuplicate"`   // 同一信号已执行过
	SkippedAlreadyExit int     `json:"skippedAlreadyExit"` // 基计划已清仓/换出或减得更多
	Events             []Event `json:"events"`
}

// State 是跨交易日共享的辅助层状态：记录已执行过的信号，避免同一信号被反复执行。
//
// 每个回测必须新建一个 State（不同 run 之间不能串）。
type State struct {
	Applied map[string]bool
	Stats   Stats
}

// NewState 构造空状态。
func NewState() *State {
	return &State{Applied: make(map[string]bool)}
}

// Apply 在既有计划上叠加缠论辅助。
//
// 调用时机：**每个交易日的计划生成之后、执行之前**（计划日收盘）。
// asOf 是计划日（YYYYMMDD）；provider 必须只使用 <= asOf 的数据。
//
// 返回的 plan 是浅拷贝后的新计划（Rows 已复制），不会改动调用方的切片。
func Apply(
	plan score.RotationPlan,
	held []score.Row,
	asOf int,
	provider Provider,
	cfg Config,
	state *State,
) score.RotationPlan {
	if provider == nil || state == nil {
		return plan
	}
	cfg = cfg.Sanitized()
	if !cfg.Enable || (!cfg.HasBuyRule() && !cfg.HasSellRule()) {
		return plan
	}
	if state.Applied == nil {
		state.Applied = make(map[string]bool)
	}

	// 复制一份 Rows：基计划可能被调用方复用（例如引擎重建计划），不要在它上面原地改。
	plan.Rows = append([]score.RotationRow(nil), plan.Rows...)
	heldLayers := make(map[string]int, len(held))
	for _, row := range held {
		heldLayers[row.Code] = row.PositionLayers
	}

	eventsBefore := len(state.Stats.Events)

	// ① 卖出优先处理：先把风险降下来，再决定是否加层（同股同日有卖点时买入侧会主动跳过）。
	if cfg.HasSellRule() {
		for i := range plan.Rows {
			applySell(&plan.Rows[i], asOf, provider, cfg, state, heldLayers)
		}
	}

	// ② 买入加层。
	if cfg.HasBuyRule() {
		for i := range plan.Rows {
			applyBuy(&plan.Rows[i], asOf, provider, cfg, state)
		}
	}

	// ③ 计划备注：当天发生过什么，界面上能直接看到。
	if added := len(state.Stats.Events) - eventsBefore; added > 0 {
		buys, sells := 0, 0
		for _, event := range state.Stats.Events[eventsBefore:] {
			if event.Kind == "buy_boost" {
				buys++
			} else {
				sells++
			}
		}
		plan.Notes = append(plan.Notes,
			fmt.Sprintf("缠论辅助：买入加层 %d 笔、卖出减仓 %d 笔", buys, sells))
	}
	return plan
}

// applyBuy 处理一条计划行的买入加层。
func applyBuy(
	row *score.RotationRow,
	asOf int,
	provider Provider,
	cfg Config,
	state *State,
) {
	isPlannedBuy := false
	switch row.Action {
	case score.ActionAdd, score.ActionAddLayer, score.ActionReplaceIn:
		isPlannedBuy = true
	case score.ActionKeep:
		// 只有显式打开「持仓加层」才允许把 Keep 改成 add_layer。
		if !cfg.BoostHoldings {
			return
		}
	default:
		return
	}
	if row.Code == "" || row.TargetLayers < 0 {
		return
	}
	state.Stats.BuyRowsExamined++

	snapshot := provider.Signals(row.Code, asOf)
	best := snapshot.BestBuy()
	if best == nil {
		return
	}
	// 同股同日已有确认卖点：风险优先，不加层。
	if snapshot.BestSell() != nil {
		state.Stats.SkippedConflict++
		return
	}
	extra := 0
	switch best.Type {
	case chanlun.SignalBuy2:
		extra = cfg.Buy2ExtraLayers
	case chanlun.SignalBuy3:
		extra = cfg.Buy3ExtraLayers
	}
	if extra <= 0 {
		return
	}

	key := appliedKey("buy", row.Code, best)
	if state.Applied[key] {
		state.Stats.SkippedDuplicate++
		return
	}

	current := row.CurrentLayers
	if current < 0 {
		current = 0
	}
	target := row.TargetLayers
	if isPlannedBuy {
		if target <= current {
			target = current + 1 // 理论上不会发生；兜底保证加层有意义
		}
	} else {
		// 持仓加层：基准是「当前层数」，不是计划目标（Keep 的目标等于当前）。
		if current <= 0 {
			return
		}
		target = current
	}
	maxLayers := cfg.MaxLayersPerStock
	if maxLayers < 1 {
		maxLayers = 7
	}
	newTarget := target + extra
	if newTarget > maxLayers {
		newTarget = maxLayers
	}
	if newTarget <= target {
		state.Stats.SkippedCapped++
		state.Applied[key] = true
		return
	}

	row.TargetLayers = newTarget
	row.LayersDelta = newTarget - current
	row.DecisionState = "chanlun_buy_boost"
	if !isPlannedBuy {
		row.Action = score.ActionAddLayer
		row.ActionLabel = score.ActionLabel(score.ActionAddLayer)
		state.Stats.HoldBoosts++
	}
	row.Reason = appendReason(row.Reason,
		fmt.Sprintf("缠论%s确认（%d日前）+%d层", best.TypeCN, best.AgeBars, newTarget-target))

	keyPart := best.TypeCN
	switch best.Type {
	case chanlun.SignalBuy2:
		state.Stats.Buy2Boosts++
	case chanlun.SignalBuy3:
		state.Stats.Buy3Boosts++
	}
	added := newTarget - target
	state.Stats.BuyLayersAdded += added
	state.Applied[key] = true
	state.Stats.Events = append(state.Stats.Events, Event{
		Date: asOf, Code: row.Code, Name: row.Name, Kind: "buy_boost",
		SignalType: string(best.Type), SignalTypeCN: keyPart,
		ConfirmDate: best.ConfirmDate, AgeBars: best.AgeBars,
		FromLayers: current, ToLayers: newTarget, DeltaLayers: added,
		Reason: row.Reason,
	})
}

// applySell 处理一条持仓行的卖出减仓。
func applySell(
	row *score.RotationRow,
	asOf int,
	provider Provider,
	cfg Config,
	state *State,
	heldLayers map[string]int,
) {
	current := row.CurrentLayers
	if current <= 0 {
		current = heldLayers[row.Code]
	}
	if current <= 0 || row.Code == "" {
		return
	}
	snapshot := provider.Signals(row.Code, asOf)
	best := snapshot.BestSell()
	if best != nil {
		state.Stats.SellSignalsSeen++
	}
	// 整仓换出 / 已清仓：不重复处理（但把「信号来了、基计划也已卖」记下来，
	// 否则报告里会误读成「缠论从没出过卖点」）。
	if row.Action == score.ActionClear || row.Action == score.ActionReplaceOut {
		if best != nil {
			state.Stats.SellSignalsOnExit++
		}
		return
	}
	state.Stats.SellRowsExamined++

	if best == nil {
		return
	}
	layers := 0
	switch best.Type {
	case chanlun.SignalSell1:
		layers = cfg.Sell1Layers
	case chanlun.SignalSell2:
		layers = cfg.Sell2Layers
	case chanlun.SignalSell3:
		layers = cfg.Sell3Layers
	}
	if layers <= 0 {
		return
	}

	key := appliedKey("sell", row.Code, best)
	if state.Applied[key] {
		state.Stats.SkippedDuplicate++
		return
	}

	target := current - layers
	if target < 0 {
		target = 0
	}
	// 计划行的目标层数可能是「没带」（0）：Keep/Observe 的语义就是保持当前层数。
	baseTarget := row.TargetLayers
	if baseTarget <= 0 {
		switch row.Action {
		case score.ActionKeep, score.ActionObserve, "":
			baseTarget = current
		}
	}
	// 基计划已经减得更多 / 已经清仓：不与其对冲，但把这个信号记为已处理，
	// 避免同一卖点过两天又在「继续持有」的行上再砍一次。
	if baseTarget <= target {
		state.Stats.SkippedAlreadyExit++
		state.Applied[key] = true
		return
	}

	row.TargetLayers = target
	row.LayersDelta = target - current
	if target == 0 {
		row.Action = score.ActionClear
	} else {
		row.Action = score.ActionReduce
	}
	row.ActionLabel = score.ActionLabel(row.Action)
	row.DecisionState = "chanlun_sell_reduce"
	row.Reason = appendReason(row.Reason,
		fmt.Sprintf("缠论%s确认（%d日前）-%d层", best.TypeCN, best.AgeBars, current-target))

	switch best.Type {
	case chanlun.SignalSell1:
		state.Stats.Sell1Reduces++
	case chanlun.SignalSell2:
		state.Stats.Sell2Reduces++
	case chanlun.SignalSell3:
		state.Stats.Sell3Reduces++
	}
	sold := current - target
	state.Stats.SellLayersSold += sold
	state.Applied[key] = true
	state.Stats.Events = append(state.Stats.Events, Event{
		Date: asOf, Code: row.Code, Name: row.Name, Kind: "sell_reduce",
		SignalType: string(best.Type), SignalTypeCN: best.TypeCN,
		ConfirmDate: best.ConfirmDate, AgeBars: best.AgeBars,
		FromLayers: current, ToLayers: target, DeltaLayers: -sold,
		Reason: row.Reason,
	})
}

func appliedKey(kind, code string, sig *Signal) string {
	date := sig.ConfirmDate
	if date <= 0 {
		date = sig.TriggerDate
	}
	return kind + "|" + code + "|" + string(sig.Type) + "|" + strconv.Itoa(date)
}

func appendReason(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "｜" + extra
	}
}

// SortEvents 按日期、代码排序（报告输出用）。
func (s *Stats) SortEvents() {
	sort.SliceStable(s.Events, func(i, j int) bool {
		if s.Events[i].Date != s.Events[j].Date {
			return s.Events[i].Date < s.Events[j].Date
		}
		return s.Events[i].Code < s.Events[j].Code
	})
}
