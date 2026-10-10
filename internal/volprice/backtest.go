package volprice

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// 对应 `volume_price_backtest.py` 的 `run_volume_price_backtest` 及其辅助函数。
//
// ⚠ 这是 `signal_history` 的**唯一生产者**。原实现只在空仓时把信号记进
// `daily_signals`（非空仓时 `signal` 只取"确认层"结果，且 `candidate` 被丢弃），
// 所以持仓/离场模拟不是可选项 —— 不移植它就无法产出等价的信号表。
//
// 未移植：`auto_model_switch` 那套模型自选（`_model_auto_choice` /
// `_select_auto_switch_model` / `_auto_switch_*`）。生产者
//（`research_service._collect_signal_rows`）**从不开启**它，所以这里显式返回
// 失败而不是悄悄走成另一条路 —— 静默分歧比明确缺口更危险。

// MODEL_REQUIRED_STATES 对应生产者的 `MODEL_REQUIRED_STATES`。
var ModelRequiredStates = map[string]string{
	ModelTrendPullback:   "bull",
	ModelBoxRange:        "range",
	ModelOversoldRebound: "bear",
}

// BacktestOptions 对应 `run_volume_price_backtest` 的关键字参数。
type BacktestOptions struct {
	StartDate           string
	EndDate             string
	MarketStateMode     string
	ManualMarketState   string
	MarketStateResolver func(currentDate string, visible []Bar) MarketStateInfo
	MarketStateCache    map[string]MarketStateInfo
	RequiredMarketState string
	LooseEntryEnabled   bool
	LooseEntryThreshold float64
	AutoModelSwitch     bool
}

// Diagnostics 对应 `_empty_diagnostics` 的字典。
type Diagnostics struct {
	ModelID                         string
	RequiredMarketState             string
	RequiredMarketStateLbl          string
	VisibleDays                     int
	MarketStateCounts               map[string]int
	MarketMatchDays                 int
	MarketMismatchDays              int
	ModelContextUsedDays            int
	EntryPrefilterPassDays          int
	EntryPrefilterMissDays          int
	ModelEvaluatedDays              int
	ModelNoSignalDays               int
	SignalCount                     int
	BuySignalCount                  int
	SellSignalCount                 int
	CandidateCount                  int
	CandidateBuyCount               int
	LooseEntryEnabled               bool
	LooseEntryThreshold             float64
	LooseCandidatePassCount         int
	LooseCandidateMissCount         int
	LooseCandidateThreshMiss        int
	LooseQualityBlockCount          int
	LooseQualityBlockReasons        map[string]int
	LooseBuySignalCount             int
	LoosePendingCount               int
	LooseTriggerConfirmed           int
	LooseTriggerVolumeMiss          int
	LooseTriggeredCount             int
	LooseExpiredCount               int
	LooseInvalidatedCount           int
	LooseUnresolvedCount            int
	LooseTriggerRejectCount         int
	LooseEntryRiskReject            int
	LooseEntryRiskReasons           map[string]int
	LooseIntradayStopCount          int
	TrendIntradayStopIgnored        int
	TrendCloseStopPending           int
	TrendSellSignalIgnored          int
	TrendSellSignalConfirmed        int
	TrendFirstTargetReached         int
	TrendFirstTargetTrailing        int
	TrendTrailingPending            int
	TrendConfirmFailurePending      int
	TrendContinuationFailurePending int
	BoxFailureExitCount             int
	OversoldFailureExitCount        int
	AutoModelSwitch                 bool
	ModelSwitchDays                 int
	ModelRouteCounts                map[string]int
	ModelUsageCounts                map[string]int
	AutoSwitchGateBlock             int
	AutoSwitchGateReasons           map[string]int
	TradeCount                      int
}

func emptyDiagnostics(modelID, requiredMarketState string) *Diagnostics {
	required := ""
	if strings.TrimSpace(requiredMarketState) != "" {
		required = NormalizeMarketState(requiredMarketState, "")
	}
	label := required
	if value, ok := MarketStateLabels[required]; ok {
		label = value
	}
	return &Diagnostics{
		ModelID: NormalizeModelID(modelID), RequiredMarketState: required,
		RequiredMarketStateLbl: label,
		MarketStateCounts:      map[string]int{}, ModelRouteCounts: map[string]int{},
		ModelUsageCounts:         map[string]int{},
		LooseQualityBlockReasons: map[string]int{},
		LooseEntryRiskReasons:    map[string]int{},
		AutoSwitchGateReasons:    map[string]int{},
		LooseEntryThreshold:      85.0,
	}
}

// Position 对应原实现里一路传递的持仓字典。
type Position struct {
	ModelID                     string
	MarketState                 string
	SignalType                  string
	SignalLevel                 string
	SignalLevelLabel            string
	SignalSubtype               string
	SignalSubtypeLabel          string
	EntrySignalSource           string
	ConfirmationProgress        *float64
	LooseEntryScore             *float64
	LooseEntryThreshold         *float64
	CandidateTriggerPrice       *float64
	CandidateTriggerDate        string
	CandidateTriggerVolumeRatio *float64
	SignalDate                  string
	EntryDate                   string
	EntryIndex                  int
	EntryPrice                  float64
	StopPrice                   float64
	TargetPrice                 float64
	FirstTargetReached          bool
	FirstTargetDate             string
	FirstTargetPrice            *float64
	HighestClose                float64
	TrailingStopPrice           *float64
	Score                       float64
	Explanation                 string
	ConditionHits               []string
	BehaviorQuality             BehaviorQuality
	QualityBlockReasons         []string
	BarsHeld                    int
	PendingExitReason           string
	PendingExitSignalDt         string
}

// Trade 对应 `{**position, "exit_date": ..., ...}`。
type Trade struct {
	Position
	ExitDate    string
	ExitPrice   float64
	SellReason  string
	ProfitPct   float64
	HoldingDays int
}

// BacktestResult 对应 `run_volume_price_backtest` 的返回字典。
type BacktestResult struct {
	OK           bool
	Message      string
	Metrics      BacktestMetrics
	Trades       []Trade
	DailySignals []Signal
	Diagnostics  *Diagnostics
}

// BacktestMetrics 对应 `_build_metrics`。
type BacktestMetrics struct {
	TotalTrades         int
	WinRate             float64
	AvgProfitPct        float64
	ProfitLossRatio     float64
	MaxDrawdownPct      float64
	MaxConsecutiveLoses int
	AvgHoldingDays      float64
}

// pendingLooseEntry 对应 `_new_pending_loose_entry` 造出来的待触发字典。
type pendingLooseEntry struct {
	Signal                      Signal
	CandidateExpireBars         int
	PendingSignalIndex          int
	ExpireIndex                 int
	TriggerConfirmed            bool
	CandidateOrderStatus        string
	CandidateTriggerDate        string
	CandidateTriggerClose       *float64
	CandidateTriggerVolumeRatio *float64
}

// RunVolumePriceBacktest 对应 `run_volume_price_backtest`。
func RunVolumePriceBacktest(bars []Bar, modelID string, opts BacktestOptions) BacktestResult {
	prepared, vec := prepareBarSliceAndVector(bars)
	model := NormalizeModelID(modelID)

	looseThreshold := normalizePercent(opts.LooseEntryThreshold, 85.0)
	if len(prepared) < 35 {
		diagnostics := emptyDiagnostics(model, opts.RequiredMarketState)
		diagnostics.LooseEntryEnabled = opts.LooseEntryEnabled
		diagnostics.LooseEntryThreshold = looseThreshold
		diagnostics.AutoModelSwitch = opts.AutoModelSwitch
		applyAutoSwitchDiagnostics(diagnostics)
		return BacktestResult{
			OK: false, Message: "本地日线数据不足，无法回测",
			Trades: []Trade{}, DailySignals: []Signal{}, Diagnostics: diagnostics,
		}
	}

	var trades []Trade
	dailySignals := []Signal{}
	var position *Position
	var pending *pendingLooseEntry

	start := normalizeDateInput(opts.StartDate)
	end := normalizeDateInput(opts.EndDate)
	requiredState := ""
	if strings.TrimSpace(opts.RequiredMarketState) != "" {
		requiredState = NormalizeMarketState(opts.RequiredMarketState, "")
	}
	diagnostics := emptyDiagnostics(model, requiredState)
	diagnostics.LooseEntryEnabled = opts.LooseEntryEnabled
	diagnostics.LooseEntryThreshold = looseThreshold
	diagnostics.AutoModelSwitch = opts.AutoModelSwitch
	applyAutoSwitchDiagnostics(diagnostics)
	modeKey := strings.ToLower(strings.TrimSpace(opts.MarketStateMode))

	dates := make([]string, 0, len(prepared))
	for _, bar := range prepared {
		dates = append(dates, bar.Date)
	}
	manualState := ""
	if modeKey == "manual" {
		manualState = NormalizeMarketState(opts.ManualMarketState, "range")
	}

	loopStart := 25
	if start != "" {
		if idx := searchSortedLeft(dates, start); idx > loopStart {
			loopStart = idx
		}
	}
	loopStop := len(prepared) - 1
	if end != "" {
		if idx := searchSortedRight(dates, end); idx < loopStop {
			loopStop = idx
		}
	}

	for i := loopStart; i < loopStop; i++ {
		currentDate := dates[i]
		if start != "" && currentDate < start {
			continue
		}
		if end != "" && currentDate > end {
			break
		}

		var stateInfo MarketStateInfo
		switch {
		case modeKey == "manual":
			label := manualState
			if value, ok := MarketStateLabels[manualState]; ok {
				label = value
			}
			stateInfo = MarketStateInfo{
				State: manualState, Label: label, Score: 100.0,
				Source: "manual", Reason: "用户手动指定市场状态", TradeDate: currentDate,
			}
		case opts.MarketStateCache != nil:
			if cached, ok := opts.MarketStateCache[currentDate]; ok && cached.State != "" {
				stateInfo = cached
			} else {
				visible := prepared[:i+1]
				if opts.MarketStateResolver != nil {
					stateInfo = opts.MarketStateResolver(currentDate, visible)
				} else {
					stateInfo = DetectStockContextState(visible, currentDate, "")
				}
			}
		default:
			visible := prepared[:i+1]
			if opts.MarketStateResolver != nil {
				stateInfo = opts.MarketStateResolver(currentDate, visible)
			} else {
				stateInfo = DetectStockContextState(visible, currentDate, "")
			}
		}

		state := NormalizeMarketState(stateInfo.State, "range")
		diagnostics.VisibleDays++
		diagnostics.MarketStateCounts[state]++
		// 原实现的 `state_info.get("model_context_used")` **无人写入**，
		// 所以 `model_context_used_days` 恒为 0；这里保持一致。

		activeModel := NormalizeModelID(model)
		if position != nil {
			activeModel = NormalizeModelID(position.ModelID)
		}
		activeRequiredState := requiredState
		if opts.AutoModelSwitch {
			activeRequiredState = RequiredStateForModel(activeModel)
		}

		// ---- 自动模型切换（对应 :123-139）----
		//
		// 原实现的两个分支**都会**给 `model_route_counts` 记一笔，区别只是
		// 自选分支用的是**切换后**的模型、并且 `required_state` 被清空
		//（`active_required_state = ""` → 状态闸门不再拦截）。
		gateReason := ""
		autoUsable := false
		autoChoice := autoChoice{}
		if position == nil && opts.AutoModelSwitch {
			autoChoice = selectAutoSwitchModel(vec.Slice(0, i+1), state, model)
			activeModel = strOrDefault(autoChoice.ModelID, model)
			activeRequiredState = ""
			diagnostics.ModelRouteCounts[activeModel]++
			if activeModel != model {
				diagnostics.ModelSwitchDays++
			}
			gateReason = trimWhitespace(autoChoice.GateReason)
			if gateReason != "" {
				diagnostics.AutoSwitchGateBlock++
				recordBucket(diagnostics.AutoSwitchGateReasons, gateReason)
			}
			autoUsable = true
		} else {
			diagnostics.ModelRouteCounts[activeModel]++
		}

		var signal Signal
		hasSignal := false
		if position == nil && gateReason != "" {
			// 自选质量闸门挡住了 → 当天不出信号
		} else if position == nil && activeRequiredState != "" && state != activeRequiredState {
			diagnostics.MarketMismatchDays++
		} else {
			diagnostics.MarketMatchDays++
			diagnostics.ModelUsageCounts[activeModel]++
			evalWindow := ModelEvalWindow(activeModel)
			var signalVisible *VectorBars
			if evalWindow > 0 && i+1 > evalWindow {
				signalVisible = vec.Slice(i+1-evalWindow, i+1)
			} else {
				signalVisible = vec.Slice(0, i+1)
			}

			// ⚠ 这里的结构必须与原实现一致：**空仓时**才看前置过滤，
			// 非空仓时无条件评估（只取"确认层"，`candidate` 被丢弃）。
			// 把前置过滤也套到非空仓分支上会让持仓期间少评估、少出 sell 信号。
			var confirmed Signal
			confirmedOK := false
			var candidate Signal
			candidateOK := false
			prefilterPassed := false
			if position == nil {
				// 自选分支已经把前置过滤与 layers 都算过了，直接复用；
				// 交给 `autoChoiceLayers` 判断"自选模型是否就是当前有效模型"。
				if reused, reusedLayers := autoChoiceLayers(autoChoice, autoUsable, activeModel); reusedLayers {
					confirmed, confirmedOK = reused.Confirmed, reused.ConfirmedOK
					candidate, candidateOK = reused.Candidate, reused.CandidateOK
					prefilterPassed = autoChoice.PrefilterPassed
				} else {
					prefilterPassed = PassesEntryPrefilter(activeModel, signalVisible)
				}
				candidatePrefilterPassed := false
				if !prefilterPassed {
					candidatePrefilterPassed = PassesCandidatePrefilter(activeModel, signalVisible)
				}
				if prefilterPassed || candidatePrefilterPassed {
					confirmed, confirmedOK, candidate, candidateOK =
						EvaluateModelLayers(signalVisible, activeModel, state)
				}
				if candidateOK {
					diagnostics.CandidateCount++
					if candidate.SignalType == "candidate_buy" {
						diagnostics.CandidateBuyCount++
					}
				}
				looseSignal, looseOK := looseEntrySignal(candidate, candidateOK, opts.LooseEntryEnabled, looseThreshold)
				if opts.LooseEntryEnabled && candidateOK && candidate.SignalType == "candidate_buy" {
					if looseOK {
						diagnostics.LooseCandidatePassCount++
					} else if looseEntryThresholdOK(candidate, looseThreshold) {
						diagnostics.LooseCandidateMissCount++
						diagnostics.LooseQualityBlockCount++
						recordLooseQualityBlock(diagnostics, candidate)
					} else {
						diagnostics.LooseCandidateMissCount++
						diagnostics.LooseCandidateThreshMiss++
					}
				}
				if !prefilterPassed {
					diagnostics.EntryPrefilterMissDays++
					signal, hasSignal = looseSignal, looseOK
				} else {
					diagnostics.EntryPrefilterPassDays++
					diagnostics.ModelEvaluatedDays++
					signal, hasSignal = confirmed, confirmedOK
					if !hasSignal {
						signal, hasSignal = looseSignal, looseOK
					}
					if !hasSignal {
						diagnostics.ModelNoSignalDays++
					}
				}
			} else {
				confirmed, confirmedOK, _, _ = EvaluateModelLayers(signalVisible, activeModel, state)
				diagnostics.ModelEvaluatedDays++
				signal, hasSignal = confirmed, confirmedOK
				if !hasSignal {
					diagnostics.ModelNoSignalDays++
				}
			}
		}

		if isLooseCandidateSignal(signal, hasSignal) {
			annotateLooseCandidateSignal(&signal, vec.High[i], activeModel, i)
		}
		if hasSignal {
			dailySignals = append(dailySignals, signal)
			diagnostics.SignalCount++
			if signal.SignalType == "buy" {
				diagnostics.BuySignalCount++
			} else if signal.SignalType == "sell" {
				diagnostics.SellSignalCount++
			}
		}

		nextDate := dates[i+1]
		nextOpen := vec.Open[i+1]
		nextHigh := vec.High[i+1]
		nextLow := vec.Low[i+1]
		nextClose := vec.Close[i+1]
		nextVolume := vec.Volume[i+1]
		prev2VolumeMean := 0.0
		if i >= 1 {
			prev2VolumeMean = (vec.Volume[i] + vec.Volume[i-1]) / 2.0
		}

		if position != nil {
			stop := position.StopPrice
			target := position.TargetPrice
			sellReason := ""
			sellPrice := 0.0
			if usesTrendExit(position) {
				switch {
				case strings.TrimSpace(position.PendingExitReason) != "":
					sellPrice, sellReason = nextOpen, position.PendingExitReason
				case usesLooseCandidateEntry(position) && nextOpen <= stop:
					sellPrice, sellReason = nextOpen, "候选止损"
					diagnostics.LooseIntradayStopCount++
				case trendRunnerTrailingExitPrice(position, nextOpen, nextLow) > 0:
					sellPrice = trendRunnerTrailingExitPrice(position, nextOpen, nextLow)
					sellReason = "移动止盈"
					diagnostics.TrendFirstTargetTrailing++
				case nextHigh >= target:
					if usesTrendRunnerEntry(position) {
						wasReached := position.FirstTargetReached
						markTrendFirstTargetReached(position, target, nextClose, nextDate)
						if !wasReached {
							diagnostics.TrendFirstTargetReached++
						}
					} else {
						sellPrice, sellReason = target, "止盈"
					}
				case usesLooseCandidateEntry(position) && nextLow <= stop:
					sellPrice, sellReason = stop, "候选止损"
					diagnostics.LooseIntradayStopCount++
				case nextClose <= stop:
					queuePendingExit(position, "收盘止损", nextDate)
					diagnostics.TrendCloseStopPending++
				case hasSignal && signal.SignalType == "sell":
					signalClose := derefFloat(signal.EntryPrice)
					if signalClose > 0 && nextClose < signalClose {
						queuePendingExit(position, "风险确认卖出", nextDate)
						diagnostics.TrendSellSignalConfirmed++
					} else {
						diagnostics.TrendSellSignalIgnored++
					}
				}
				if sellReason == "" {
					updateTrendTrailingState(position, nextClose)
					trailingStop := derefFloat(position.TrailingStopPrice)
					if trailingStop > 0 && nextClose <= trailingStop {
						queuePendingExit(position, "移动止盈", nextDate)
						diagnostics.TrendTrailingPending++
					}
				}
				if sellReason == "" && !hasPendingExit(position) &&
					trendConfirmFailure(position, nextClose) {
					queuePendingExit(position, "确认失败撤退", nextDate)
					diagnostics.TrendConfirmFailurePending++
				}
				if sellReason == "" && !hasPendingExit(position) &&
					trendContinuationFailure(position, nextClose) {
					queuePendingExit(position, "续航失败撤退", nextDate)
					diagnostics.TrendContinuationFailurePending++
				}
				if sellReason == "" && nextLow <= stop && stop < nextClose {
					diagnostics.TrendIntradayStopIgnored++
				}
			} else {
				if usesBoxExit(position) {
					if reason := boxExitReason(position, nextOpen, nextClose, nextLow, nextHigh); reason != "" {
						if reason == "箱体破位撤退" {
							sellPrice = nextOpen
						} else {
							sellPrice = nextClose
						}
						sellReason = reason
						diagnostics.BoxFailureExitCount++
					}
				} else if usesOversoldExit(position) {
					if reason := oversoldExitReason(position, nextOpen, nextClose, nextLow, nextHigh); reason != "" {
						if reason == "反抽破位撤退" {
							sellPrice = nextOpen
						} else {
							sellPrice = nextClose
						}
						sellReason = reason
						diagnostics.OversoldFailureExitCount++
					}
				}
				if sellReason == "" && nextLow <= stop {
					sellPrice, sellReason = stop, "止损"
				} else if sellReason == "" && nextHigh >= target {
					sellPrice, sellReason = target, "止盈"
				} else if sellReason == "" && hasSignal && signal.SignalType == "sell" {
					sellPrice, sellReason = nextOpen, "信号卖出"
				}
				if sellReason == "" {
					position.BarsHeld++
				}
			}

			if sellReason != "" {
				entryPrice := position.EntryPrice
				profitPct := 0.0
				if entryPrice > 0 {
					profitPct = (sellPrice/entryPrice - 1.0) * 100.0
				}
				trade := Trade{
					Position: *position, ExitDate: nextDate,
					ExitPrice: round4(sellPrice), SellReason: sellReason,
					ProfitPct: round4(profitPct), HoldingDays: i + 1 - position.EntryIndex,
				}
				trades = append(trades, trade)
				position = nil
			}
		}

		signalType := ""
		if hasSignal {
			signalType = signal.SignalType
		}

		if position == nil && pending != nil && pending.TriggerConfirmed {
			result, next := resolvePendingLooseEntry(pending, nextOpen, nextLow, nextHigh,
				nextClose, nextDate, nextVolume, i+1, diagnostics, prev2VolumeMean)
			pending = next
			if result != nil {
				position = enterFromLooseTrigger(result, activeModel, state, signalType,
					currentDate, nextDate, i+1, looseThreshold, diagnostics)
			}
		}

		if position == nil && hasSignal && signalType == "buy" {
			built := buildPosition(activeModel, state, signal, signalType, currentDate,
				nextDate, i+1, nextOpen, looseThreshold)
			if built != nil {
				position = built
				pending = nil
			}
		}

		if position == nil && isLooseCandidateSignal(signal, hasSignal) && pending == nil {
			pending = newPendingLooseEntry(signal, i)
			diagnostics.LoosePendingCount++
		}

		if position == nil && pending != nil {
			result, next := resolvePendingLooseEntry(pending, nextOpen, nextLow, nextHigh,
				nextClose, nextDate, nextVolume, i+1, diagnostics, prev2VolumeMean)
			pending = next
			if result != nil {
				position = enterFromLooseTrigger(result, activeModel, state, signalType,
					currentDate, nextDate, i+1, looseThreshold, diagnostics)
			}
		}
	}

	if pending != nil {
		diagnostics.LooseUnresolvedCount++
	}

	if position != nil {
		entryPrice := position.EntryPrice
		exitPrice := vec.Close[vec.Len()-1]
		profitPct := 0.0
		if entryPrice > 0 {
			profitPct = (exitPrice/entryPrice - 1.0) * 100.0
		}
		trades = append(trades, Trade{
			Position: *position, ExitDate: dates[len(dates)-1],
			ExitPrice: round4(exitPrice), SellReason: "回测结束",
			ProfitPct:   round4(profitPct),
			HoldingDays: len(prepared) - 1 - position.EntryIndex,
		})
	}

	metrics := buildBacktestMetrics(trades)
	diagnostics.TradeCount = len(trades)
	return BacktestResult{
		OK: true, Message: "回测完成", Metrics: metrics,
		Trades: trades, DailySignals: dailySignals, Diagnostics: diagnostics,
	}
}

// prepareBarSliceAndVector 对应 `prepare_bars`：过滤/排序/去重 + 附加派生列。
//
// 同时返回 `[]Bar`（供 `detect_stock_context_state` 用）与带派生列的 `*VectorBars`
// （供模型判定与窗口切片用）。两者行序一致、行数相同。
func prepareBarSliceAndVector(bars []Bar) ([]Bar, *VectorBars) {
	prepared := make([]Bar, 0, len(bars))
	for _, bar := range bars {
		date := TrimDate(bar.Date)
		if date == "" {
			continue
		}
		bar.Date = date
		prepared = append(prepared, bar)
	}
	prepared = sortBarsByDate(prepared)
	return prepared, AttachVectorBarsColumns(prepared)
}

func applyAutoSwitchDiagnostics(diagnostics *Diagnostics) {
	if diagnostics == nil || !diagnostics.AutoModelSwitch {
		return
	}
	diagnostics.RequiredMarketState = ""
	diagnostics.RequiredMarketStateLbl = "按个股状态切换"
}

// normalizePercent 对应 `_normalize_percent`。
func normalizePercent(value, def float64) float64 {
	if math.IsNaN(value) {
		return def
	}
	return round4(math.Max(0.0, math.Min(100.0, value)))
}

// normalizeDateInput 对应 `str(start_date or "").replace("-", "").strip()`。
func normalizeDateInput(value string) string {
	return TrimDate(strings.ReplaceAll(value, "-", ""))
}

// searchSortedLeft 对应 numpy `searchsorted(side="left")`。
func searchSortedLeft(sorted []string, value string) int {
	return sort.SearchStrings(sorted, value)
}

// searchSortedRight 对应 numpy `searchsorted(side="right")`。
func searchSortedRight(sorted []string, value string) int {
	idx := sort.SearchStrings(sorted, value)
	for idx < len(sorted) && sorted[idx] == value {
		idx++
	}
	return idx
}

func strOrDefault(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

func recordBucket(bucket map[string]int, reason string) {
	if bucket == nil {
		return
	}
	text := strings.TrimSpace(reason)
	if text == "" {
		text = "未通过质量闸门"
	}
	bucket[text]++
}

func recordLooseQualityBlock(diagnostics *Diagnostics, candidate Signal) {
	reasons := candidate.LooseBlockReasons
	if len(reasons) == 0 {
		reasons = []string{"宽松交易质量门槛未通过"}
	}
	for _, reason := range reasons {
		recordBucket(diagnostics.LooseQualityBlockReasons, reason)
	}
}

// ---------------------------------------------------------------------------
// 宽松入场（loose entry）
// ---------------------------------------------------------------------------

func looseEntrySignal(candidate Signal, candidateOK bool, enabled bool, threshold float64) (Signal, bool) {
	if !enabled || !candidateOK {
		return Signal{}, false
	}
	if candidate.SignalType != "candidate_buy" {
		return Signal{}, false
	}
	if !looseEntryThresholdOK(candidate, threshold) {
		return Signal{}, false
	}
	if !candidate.LooseEntryAllowed {
		return Signal{}, false
	}
	signal := candidate
	signal.EntrySignalSource = "loose_candidate"
	// 原实现 `signal["loose_entry_score"] = _loose_entry_score(candidate)`：
	// 读同一键、归一化后写回，所以原地覆盖即可。
	signal.LooseEntryScore = normalizePercent(candidate.LooseEntryScore, 0.0)
	signal.LooseEntryThreshold = threshold
	return signal, true
}

func looseEntryThresholdOK(candidate Signal, threshold float64) bool {
	return normalizePercent(candidate.LooseEntryScore, 0.0) > threshold
}

func isLooseCandidateSignal(signal Signal, hasSignal bool) bool {
	if !hasSignal {
		return false
	}
	return signal.SignalType == "candidate_buy" && signal.EntrySignalSource == "loose_candidate"
}

// annotateLooseCandidateSignal 对应 `_annotate_loose_candidate_signal`。
//
// ⚠ 它会**覆盖**模型自己给的 `candidate_trigger_price`（取 `max(信号当日最高, 入场价)`）。
func annotateLooseCandidateSignal(signal *Signal, signalHigh float64, modelID string, signalIndex int) {
	trigger := math.Max(signalHigh, derefFloat(signal.EntryPrice))
	if trigger > 0 {
		value := round4(trigger)
		signal.CandidateTriggerPrice = &value
	} else {
		signal.CandidateTriggerPrice = nil
	}
	signal.CandidateExpireBars = looseCandidateExpireBars(modelID)
	signal.CandidateOrderStatus = "pending"
	signal.CandidateSignalIndex = signalIndex
	signal.ExecutionMode = "trigger"
}

func looseCandidateExpireBars(modelID string) int {
	switch NormalizeModelID(modelID) {
	case ModelTrendPullback:
		return 5
	case ModelBoxRange:
		return 3
	case ModelOversoldRebound:
		return 2
	}
	return 3
}

func newPendingLooseEntry(signal Signal, signalIndex int) *pendingLooseEntry {
	expireBars := signal.CandidateExpireBars
	if expireBars == 0 {
		expireBars = looseCandidateExpireBars(signal.ModelID)
	}
	expire := expireBars
	if expire < 1 {
		expire = 1
	}
	return &pendingLooseEntry{
		Signal: signal, CandidateExpireBars: expireBars,
		PendingSignalIndex: signalIndex, ExpireIndex: signalIndex + expire,
	}
}

// looseTriggerResult 对应 `_resolve_pending_loose_entry` 的第一个返回值。
type looseTriggerResult struct {
	Signal     Signal
	EntryPrice float64
}

func resolvePendingLooseEntry(
	pending *pendingLooseEntry, nextOpen, nextLow, nextHigh, nextClose float64,
	nextTradeDate string, nextVolume float64, nextIndex int,
	diagnostics *Diagnostics, prev2VolumeMean float64,
) (*looseTriggerResult, *pendingLooseEntry) {
	if pending.TriggerConfirmed {
		return &looseTriggerResult{Signal: pending.Signal, EntryPrice: round4(nextOpen)}, nil
	}
	if pending.ExpireIndex > 0 && nextIndex > pending.ExpireIndex {
		diagnostics.LooseExpiredCount++
		return nil, nil
	}
	stop := derefFloat(pending.Signal.StopPrice)
	trigger := derefFloat(pending.Signal.CandidateTriggerPrice)
	if trigger == 0 {
		trigger = derefFloat(pending.Signal.EntryPrice)
	}
	if stop > 0 && nextLow <= stop {
		diagnostics.LooseInvalidatedCount++
		return nil, nil
	}
	midPrice := (nextHigh + nextLow) / 2.0
	if trigger > 0 && nextClose >= trigger && nextClose >= midPrice {
		if !looseCandidateTriggerVolumeOK(pending, nextVolume, prev2VolumeMean, diagnostics) {
			return nil, pending
		}
		value := round4(trigger)
		closeValue := round4(nextClose)
		pending.CandidateOrderStatus = "trigger_confirmed"
		pending.CandidateTriggerDate = nextTradeDate
		pending.Signal.CandidateTriggerPrice = &value
		pending.CandidateTriggerClose = &closeValue
		pending.Signal.EntrySignalSource = "loose_candidate"
		pending.Signal.CandidateTriggerDate = nextTradeDate
		pending.TriggerConfirmed = true
		diagnostics.LooseTriggerConfirmed++
		return nil, pending
	}
	if pending.ExpireIndex > 0 && nextIndex >= pending.ExpireIndex {
		diagnostics.LooseExpiredCount++
		return nil, nil
	}
	return nil, pending
}

func looseCandidateTriggerVolumeOK(
	pending *pendingLooseEntry, nextVolume, prev2VolumeMean float64, diagnostics *Diagnostics,
) bool {
	if NormalizeModelID(pending.Signal.ModelID) != ModelTrendPullback {
		return true
	}
	if prev2VolumeMean <= 0 {
		return true
	}
	ratio := nextVolume / prev2VolumeMean
	value := round4(ratio)
	pending.CandidateTriggerVolumeRatio = &value
	pending.Signal.CandidateTriggerVolumeRatio = &value
	if ratio >= 1.25 {
		return true
	}
	diagnostics.LooseTriggerVolumeMiss++
	return false
}

// enterFromLooseTrigger 是主循环里出现两次的那段建仓逻辑（原实现是重复的两块）：
// 先做"实际入场风险校验"，通过才建仓，并各自记账。
func enterFromLooseTrigger(
	result *looseTriggerResult, activeModel, state, signalType, currentDate, nextDate string,
	nextIndex int, looseThreshold float64, diagnostics *Diagnostics,
) *Position {
	signal := result.Signal
	if !looseCandidateActualEntryOK(signal, result.EntryPrice, diagnostics) {
		diagnostics.LooseTriggerRejectCount++
		return nil
	}
	built := buildPosition(
		strOrDefault(signal.ModelID, activeModel), state, signal,
		strOrDefault(signal.SignalType, signalType),
		strOrDefault(signal.SignalDate, currentDate),
		nextDate, nextIndex, result.EntryPrice, looseThreshold)
	if built != nil {
		diagnostics.LooseBuySignalCount++
		diagnostics.LooseTriggeredCount++
		return built
	}
	diagnostics.LooseTriggerRejectCount++
	return nil
}

// looseCandidateActualEntryOK 对应 `_loose_candidate_actual_entry_ok`。
func looseCandidateActualEntryOK(signal Signal, entryPrice float64, diagnostics *Diagnostics) bool {
	entry := entryPrice
	stop := derefFloat(signal.StopPrice)
	target := derefFloat(signal.TargetPrice)
	var reasons []string
	if entry <= 0 || stop <= 0 || target <= entry || entry <= stop {
		reasons = append(reasons, "价格结构无效")
	} else {
		riskPct := (entry - stop) / entry * 100.0
		rewardRisk := (target - entry) / (entry - stop)
		if riskPct > 10.0 {
			reasons = append(reasons, "实际止损距离超过10%")
		}
		if rewardRisk < 1.15 {
			reasons = append(reasons, "实际盈亏比低于1.15")
		}
	}
	trigger := derefFloat(signal.CandidateTriggerPrice)
	if trigger > 0 && entry > trigger*1.04 {
		reasons = append(reasons, "开盘价高于触发价4%以上")
	}
	if len(reasons) > 0 {
		diagnostics.LooseEntryRiskReject++
		for _, reason := range reasons {
			recordBucket(diagnostics.LooseEntryRiskReasons, reason)
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 建仓 / 离场
// ---------------------------------------------------------------------------

func buildPosition(
	model, state string, signal Signal, signalType, signalDate, entryDate string,
	entryIndex int, entryPrice, looseThreshold float64,
) *Position {
	entry := entryPrice
	stopPrice := derefFloat(signal.StopPrice)
	targetPrice := derefFloat(signal.TargetPrice)
	if entry <= 0 || stopPrice <= 0 || entry <= stopPrice || targetPrice <= entry {
		return nil
	}
	isLoose := signal.EntrySignalSource == "loose_candidate"
	position := &Position{
		ModelID: model, MarketState: state, SignalType: signalType,
		SignalLevel: signal.SignalLevel, SignalLevelLabel: signal.SignalLevelLabel,
		SignalSubtype: signal.SignalSubtype, SignalSubtypeLabel: signal.SignalSubtypeLabel,
		EntrySignalSource: strOrDefault(signal.EntrySignalSource, "confirmed"),
		SignalDate:        signalDate, EntryDate: entryDate, EntryIndex: entryIndex,
		EntryPrice: round4(entry), StopPrice: round4(stopPrice), TargetPrice: round4(targetPrice),
		HighestClose: round4(entry), Score: signal.Score, Explanation: signal.Explanation,
		ConditionHits:         append([]string(nil), signal.ConditionHits...),
		BehaviorQuality:       signal.BehaviorQuality,
		QualityBlockReasons:   append([]string(nil), signal.QualityBlockReasons...),
		CandidateTriggerPrice: signal.CandidateTriggerPrice,
	}
	// 原实现直接取 `signal.get("confirmation_progress")` / `signal.get("loose_entry_score")`：
	// 只有**候选层**信号才有这两个键（确认层没有），所以用 SignalLevel 区分 ——
	// 否则会把"确认信号没有该字段"错写成"该字段为 0"。
	if signal.SignalLevel == "candidate" {
		progress := signal.ConfirmProgress
		position.ConfirmationProgress = &progress
		loose := signal.LooseEntryScore
		position.LooseEntryScore = &loose
	}
	if isLoose {
		value := looseThreshold
		position.LooseEntryThreshold = &value
	}
	if signal.CandidateTriggerDate != "" {
		position.CandidateTriggerDate = signal.CandidateTriggerDate
	}
	if signal.CandidateTriggerVolumeRatio != nil {
		value := *signal.CandidateTriggerVolumeRatio
		position.CandidateTriggerVolumeRatio = &value
	}
	return position
}

func usesTrendExit(position *Position) bool {
	return position != nil && NormalizeModelID(position.ModelID) == ModelTrendPullback
}

func usesBoxExit(position *Position) bool {
	return position != nil && NormalizeModelID(position.ModelID) == ModelBoxRange
}

func usesOversoldExit(position *Position) bool {
	return position != nil && NormalizeModelID(position.ModelID) == ModelOversoldRebound
}

func usesLooseCandidateEntry(position *Position) bool {
	return position != nil && position.EntrySignalSource == "loose_candidate"
}

func usesTrendContinuationEntry(position *Position) bool {
	if position == nil {
		return false
	}
	switch position.SignalSubtype {
	case "trend_continuation_reentry", "trend_platform_reentry", "early_maintrend_start":
		return true
	}
	return false
}

func usesTrendRunnerEntry(position *Position) bool {
	return usesTrendExit(position) && !usesLooseCandidateEntry(position)
}

func boxExitReason(position *Position, openPrice, closePrice, lowPrice, highPrice float64) string {
	entry := position.EntryPrice
	stop := position.StopPrice
	target := position.TargetPrice
	age := position.BarsHeld + 1
	if stop <= 0 || entry <= 0 {
		return ""
	}
	support := stop / 0.99
	switch {
	case openPrice <= support*0.985:
		return "箱体破位撤退"
	case age <= 3 && closePrice < support*1.005:
		return "下沿承接失败"
	case age >= 4 && highPrice < entry*1.025 && closePrice <= entry*0.995:
		return "弱反抽撤退"
	case target > entry && highPrice >= entry+(target-entry)*0.65 &&
		closePrice < entry+(target-entry)*0.35:
		return "箱体冲高回落"
	}
	return ""
}

func oversoldExitReason(position *Position, openPrice, closePrice, lowPrice, highPrice float64) string {
	entry := position.EntryPrice
	stop := position.StopPrice
	target := position.TargetPrice
	age := position.BarsHeld + 1
	if entry <= 0 {
		return ""
	}
	switch {
	case age <= 2 && closePrice <= entry*0.985:
		return "反抽失败撤退"
	case stop > 0 && openPrice <= stop*1.01:
		return "反抽破位撤退"
	case target > entry && highPrice >= entry+(target-entry)*0.7 &&
		closePrice < entry+(target-entry)*0.4:
		return "反抽冲高回落"
	case age >= 5 && closePrice < entry*1.03:
		return "反抽无力撤退"
	}
	return ""
}

func trendRunnerTrailingExitPrice(position *Position, openPrice, lowPrice float64) float64 {
	if !usesTrendRunnerEntry(position) || !position.FirstTargetReached {
		return 0.0
	}
	trailingStop := derefFloat(position.TrailingStopPrice)
	if trailingStop <= 0 {
		return 0.0
	}
	if openPrice <= trailingStop {
		return round4(openPrice)
	}
	if lowPrice <= trailingStop {
		return round4(trailingStop)
	}
	return 0.0
}

func trendContinuationFailure(position *Position, closePrice float64) bool {
	if !usesTrendContinuationEntry(position) {
		return false
	}
	entry := position.EntryPrice
	if entry <= 0 || closePrice <= 0 {
		return false
	}
	age := position.BarsHeld
	highest := math.Max(position.HighestClose, closePrice)
	switch position.SignalSubtype {
	case "early_maintrend_start":
		if age >= 1 && age <= 4 && closePrice <= entry*0.985 {
			return true
		}
		if age >= 3 && age <= 10 && highest < entry*1.045 && closePrice < entry*1.005 {
			return true
		}
		if age >= 5 && age <= 14 && highest >= entry*1.08 && closePrice <= entry*1.015 {
			return true
		}
		return false
	case "trend_platform_reentry":
		if age >= 1 && age <= 6 && closePrice <= entry*0.982 {
			return true
		}
		if age >= 4 && age <= 12 && highest < entry*1.035 && closePrice < entry*0.99 {
			return true
		}
		if age >= 6 && age <= 16 && highest >= entry*1.05 && closePrice <= entry*0.995 {
			return true
		}
		return false
	}
	if age >= 1 && age <= 8 && closePrice <= entry*0.985 {
		return true
	}
	if age >= 4 && age <= 12 && highest < entry*1.04 && closePrice < entry {
		return true
	}
	if age >= 5 && age <= 15 && highest >= entry*1.04 && closePrice <= entry*1.005 {
		return true
	}
	return false
}

func trendConfirmFailure(position *Position, closePrice float64) bool {
	if !usesTrendExit(position) {
		return false
	}
	if usesTrendContinuationEntry(position) || usesLooseCandidateEntry(position) {
		return false
	}
	entry := position.EntryPrice
	if entry <= 0 || closePrice <= 0 {
		return false
	}
	age := position.BarsHeld
	highest := math.Max(position.HighestClose, closePrice)
	if age >= 4 && age <= 10 && highest < entry*1.035 && closePrice < entry {
		return true
	}
	if age >= 8 && age <= 15 && highest < entry*1.06 && closePrice <= entry*0.98 {
		return true
	}
	return false
}

func markTrendFirstTargetReached(position *Position, targetPrice, closePrice float64, tradeDate string) {
	if position == nil {
		return
	}
	position.FirstTargetReached = true
	if strings.TrimSpace(position.FirstTargetDate) == "" {
		position.FirstTargetDate = strings.TrimSpace(tradeDate)
	}
	if targetPrice > 0 {
		value := round4(targetPrice)
		position.FirstTargetPrice = &value
	}
	highest := math.Max(math.Max(position.HighestClose, targetPrice), closePrice)
	if highest > 0 {
		position.HighestClose = round4(highest)
	}
}

func updateTrendTrailingState(position *Position, closePrice float64) {
	entry := position.EntryPrice
	if entry <= 0 || closePrice <= 0 {
		return
	}
	highest := math.Max(position.HighestClose, closePrice)
	position.HighestClose = round4(highest)
	position.BarsHeld++
	subtype := position.SignalSubtype
	target := position.TargetPrice
	if position.FirstTargetReached {
		var trailingStop float64
		switch subtype {
		case "trend_platform_reentry":
			trailingStop = math.Max(math.Max(entry*1.045, targetOrZero(target, 0.965)), highest*0.92)
		case "early_maintrend_start":
			trailingStop = math.Max(math.Max(entry*1.035, targetOrZero(target, 0.965)), highest*0.93)
		default:
			trailingStop = math.Max(math.Max(entry*1.04, targetOrZero(target, 0.97)), highest*0.94)
		}
		value := round4(trailingStop)
		position.TrailingStopPrice = &value
		return
	}
	switch subtype {
	case "early_maintrend_start":
		if highest < entry*1.09 {
			return
		}
		value := round4(math.Max(entry*1.015, highest*0.93))
		position.TrailingStopPrice = &value
		return
	case "trend_platform_reentry":
		if highest < entry*1.10 {
			return
		}
		value := round4(math.Max(entry*1.025, highest*0.92))
		position.TrailingStopPrice = &value
		return
	}
	if highest < entry*1.08 {
		return
	}
	value := round4(math.Max(entry*1.02, highest*0.94))
	position.TrailingStopPrice = &value
}

func targetOrZero(target, factor float64) float64 {
	if target > 0 {
		return target * factor
	}
	return 0.0
}

func hasPendingExit(position *Position) bool {
	return position != nil && strings.TrimSpace(position.PendingExitReason) != ""
}

func queuePendingExit(position *Position, reason, signalDate string) {
	if position == nil || hasPendingExit(position) {
		return
	}
	position.PendingExitReason = strings.TrimSpace(reason)
	position.PendingExitSignalDt = strings.TrimSpace(signalDate)
}

// ---------------------------------------------------------------------------
// 指标
// ---------------------------------------------------------------------------

// buildBacktestMetrics 对应 `_build_metrics`。
func buildBacktestMetrics(trades []Trade) BacktestMetrics {
	total := len(trades)
	if total <= 0 {
		return BacktestMetrics{}
	}
	profits := make([]float64, 0, total)
	var wins, losses []float64
	for _, trade := range trades {
		profits = append(profits, trade.ProfitPct)
		if trade.ProfitPct > 0 {
			wins = append(wins, trade.ProfitPct)
		} else {
			losses = append(losses, trade.ProfitPct)
		}
	}
	avgWin := 0.0
	if len(wins) > 0 {
		avgWin = sumFloats(wins) / float64(len(wins))
	}
	avgLoss := 0.0
	if len(losses) > 0 {
		avgLoss = math.Abs(sumFloats(losses) / float64(len(losses)))
	}
	equity, peak, maxDD := 1.0, 1.0, 0.0
	maxConsec, curConsec := 0, 0
	holdingSum := 0
	for _, profit := range profits {
		equity *= 1.0 + profit/100.0
		if equity > peak {
			peak = equity
		}
		if peak > 0 {
			maxDD = math.Min(maxDD, equity/peak-1.0)
		}
		if profit <= 0 {
			curConsec++
			if curConsec > maxConsec {
				maxConsec = curConsec
			}
		} else {
			curConsec = 0
		}
	}
	for _, trade := range trades {
		holdingSum += trade.HoldingDays
	}
	profitLossRatio := 0.0
	if avgLoss > 0 {
		profitLossRatio = round4(avgWin / avgLoss)
	}
	return BacktestMetrics{
		TotalTrades:         total,
		WinRate:             round2(float64(len(wins)) / float64(total) * 100.0),
		AvgProfitPct:        round4(sumFloats(profits) / float64(total)),
		ProfitLossRatio:     profitLossRatio,
		MaxDrawdownPct:      round4(math.Abs(maxDD) * 100.0),
		MaxConsecutiveLoses: maxConsec,
		AvgHoldingDays:      round2(float64(holdingSum) / float64(total)),
	}
}

func sumFloats(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total
}

// FormatBacktestMessage 便于上层拼接日志（保留原实现的文案）。
func FormatBacktestMessage(result BacktestResult) string {
	return fmt.Sprintf("%s（成交 %d 笔，信号 %d 条）",
		result.Message, len(result.Trades), len(result.DailySignals))
}
