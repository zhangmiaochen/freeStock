package score

// 加第二层（add_layer）的判定。
//
// 1:1 移植自 portfolio_rotation_logic.py：
//   - `_is_confirmed_pullback_rebound` (639-660)
//   - `is_strong_stock_for_layering`      (1293-1356)
//   - `_is_bull_trend_continuation_add_signal` (1010-1097)
//   - `can_add_second_layer`              (1376-1409)
//   - 调用阶段                            (2527-2573)
//
// **此前整段缺失** —— Go 永远不会给出「加1层」的建议。

// 回踩确认相关常量（原实现 :38-43）。
const (
	pullbackTouchTolerance  = 0.035
	pullbackConfirmCloseTol = 0.003
	pullbackConfirmBreakTol = 0.012
	pullbackConfirmRecovery = 0.55
	pullbackConfirmMinPct   = 0.5
)

// maPullbackTouched 判定当日是否触及均线（原实现 :616-620）。
func maPullbackTouched(priceLow, priceClose, maValue float64) bool {
	if maValue <= 0 {
		return false
	}
	lowOK := priceLow > 0 && priceLow <= maValue*(1.0+pullbackTouchTolerance)
	closeOK := priceClose > 0 && priceClose >= maValue*(1.0-pullbackTouchTolerance)
	return lowOK && closeOK
}

// closeRecoveryRatio 收盘价在当日振幅中的位置（原实现 :624-629）。
func closeRecoveryRatio(priceHigh, priceLow, priceClose float64) float64 {
	span := priceHigh - priceLow
	if span <= 0 {
		if priceClose >= priceLow {
			return 1.0
		}
		return 0.0
	}
	return clamp((priceClose-priceLow)/span, 0, 1)
}

// isConfirmedPullbackRebound 判定是否"回踩均线后确认企稳"（原实现 :639-660）。
func isConfirmedPullbackRebound(priceOpen, priceHigh, priceLow, priceClose, pctChg, maValue float64) bool {
	if maValue <= 0 || priceClose <= 0 {
		return false
	}
	if !maPullbackTouched(priceLow, priceClose, maValue) {
		return false
	}
	if priceClose < maValue*(1.0-pullbackConfirmBreakTol) {
		return false
	}
	if priceClose < maValue*(1.0-pullbackConfirmCloseTol) {
		return false
	}
	recoveryRatio := closeRecoveryRatio(priceHigh, priceLow, priceClose)
	bullishBody := priceOpen > 0 && priceClose >= priceOpen
	reclaimFromBelow := priceOpen > 0 && priceOpen < maValue && priceClose >= maValue
	strongFinish := pctChg >= pullbackConfirmMinPct && recoveryRatio >= pullbackConfirmRecovery
	return bullishBody || reclaimFromBelow || strongFinish
}

// signalMaxAgeForState 候选信号的最大年龄（原实现 :20-25）。
func signalMaxAgeForState(marketState string) int {
	switch marketState {
	case StateBull:
		return 4
	case StateBear:
		return 2
	}
	return 3
}

// isStrongStockForLayering 判定是否强势到可以加层（原实现 :1293-1356）。
func isStrongStockForLayering(row Row, policy StrategyConfig, currentLayers int) bool {
	if IsStructureBroken(row, policy.StrategyID, policy.MarketState, ProfitProtectStartPct, ProfitProtectRetrace) {
		return false
	}
	if isOverheatedCandidate(row, policy.MarketState) {
		return false
	}
	state := policy.MarketState
	if row.Close > 0 && row.Ma30 > 0 && row.Close < row.Ma30*0.997 {
		return false
	}

	if currentLayers >= 2 {
		if state != StateBull {
			return false
		}
		if row.TotalScore < maxFloat(policy.ReadyScore, 76.0) {
			return false
		}
		if row.TrendScore < 21.0 || row.StructureScore < 12.0 || row.RSScore < 10.0 {
			return false
		}
		if row.RelativeStrength20D < 4.0 {
			return false
		}
		if row.Close > 0 && row.Ma20 > 0 && row.Close < row.Ma20*0.995 {
			return false
		}
		if row.EntryPrice > 0 && row.CurrentPrice > 0 && row.CurrentPrice < row.EntryPrice*0.995 {
			return false
		}
		return true
	}

	minScore := maxFloat(policy.ReadyScore-4.0, 70.0)
	if row.TotalScore < minScore {
		return false
	}
	if row.TrendScore < 20.0 || row.StructureScore < 11.0 || row.RSScore < 9.0 {
		return false
	}
	if row.RelativeStrength20D < -1.0 {
		return false
	}
	if row.Close > 0 && row.Ma20 > 0 && row.Close < row.Ma20*0.992 {
		return false
	}
	if row.DrawdownFromHigh120 < -22.0 {
		return false
	}
	if row.ProfitLossPct < -2.5 {
		return false
	}
	if row.EntryPrice > 0 && row.CurrentPrice > 0 && row.CurrentPrice < row.EntryPrice*0.98 {
		return false
	}
	if row.SignalType == "buy" && row.SignalAge <= signalMaxAgeForState(state) {
		return true
	}
	return row.TotalScore >= maxFloat(policy.ReadyScore-2.0, 72.0) &&
		row.TrendScore >= 21.0 && row.StructureScore >= 12.0 && row.RSScore >= 10.0
}

// isBullTrendContinuationAddSignal 牛市趋势延续的加层信号（原实现 :1010-1097）。
func isBullTrendContinuationAddSignal(row Row, policy StrategyConfig, currentLayers int) bool {
	if policy.MarketState != StateBull {
		return false
	}
	closePrice := row.Close
	currentPrice := row.CurrentPrice
	if currentPrice <= 0 {
		currentPrice = closePrice
	}
	openPrice := row.Open
	if openPrice <= 0 {
		openPrice = closePrice
	}
	highPrice := row.High
	if highPrice <= 0 {
		highPrice = closePrice
	}
	lowPrice := row.Low
	if lowPrice <= 0 {
		lowPrice = closePrice
	}
	highestPrice := row.HighestPrice
	if highestPrice <= 0 {
		highestPrice = closePrice
	}
	recoveryRatio := closeRecoveryRatio(highPrice, lowPrice, closePrice)
	retraceFromHigh := 0.0
	if currentPrice > 0 && highestPrice > 0 {
		retraceFromHigh = (currentPrice/highestPrice - 1) * 100
	}
	intradaySpan := 0.0
	if highPrice > 0 && lowPrice > 0 {
		intradaySpan = (highPrice/lowPrice - 1) * 100
	}

	var minScore, minTrend, minStructure, minRS, minRS20, minPctChg float64
	var minHoldingDays int
	var ma20Floor, ma30Floor, maxRetrace, maxTightSpan float64
	if currentLayers >= 2 {
		minScore, minTrend, minStructure, minRS = maxFloat(policy.ReadyScore+4.0, 80.0), 23, 13, 11
		minRS20, minPctChg, minHoldingDays = 8, 1.0, 4
		ma20Floor, ma30Floor, maxRetrace, maxTightSpan = 1.004, 1.012, -3.2, 3.8
	} else {
		minScore, minTrend, minStructure, minRS = maxFloat(policy.ReadyScore+1.0, 77.0), 22, 12, 10
		minRS20, minPctChg, minHoldingDays = 5, 0.8, 2
		ma20Floor, ma30Floor, maxRetrace, maxTightSpan = 1.002, 1.008, -4.2, 4.5
	}

	if row.HoldingTradeDays < minHoldingDays {
		return false
	}
	if row.TotalScore < minScore || row.TrendScore < minTrend ||
		row.StructureScore < minStructure || row.RSScore < minRS {
		return false
	}
	if row.RelativeStrength20D < minRS20 {
		return false
	}
	if row.VolumeRatio20 > 2.9 {
		return false
	}
	if closePrice <= 0 {
		return false
	}
	if row.Ma20 > 0 && closePrice < row.Ma20*ma20Floor {
		return false
	}
	if row.Ma30 > 0 && closePrice < row.Ma30*ma30Floor {
		return false
	}

	tightSideways := intradaySpan > 0 && intradaySpan <= maxTightSpan &&
		recoveryRatio >= 0.58 && openPrice > 0 && closePrice >= openPrice*0.995
	if row.PctChg < minPctChg && !tightSideways {
		return false
	}
	if row.Ma20 > 0 {
		if tightSideways {
			if lowPrice < row.Ma20*0.998 {
				return false
			}
		} else if lowPrice <= row.Ma20*1.001 {
			return false
		}
	}
	if currentPrice > 0 && row.EntryPrice > 0 && currentPrice < row.EntryPrice*1.01 {
		return false
	}
	if row.ProfitLossPct < 0.8 {
		return false
	}
	if highestPrice > 0 && retraceFromHigh < maxRetrace {
		return false
	}
	if openPrice > 0 && closePrice < openPrice && recoveryRatio < 0.72 {
		return false
	}
	return true
}

// canAddSecondLayer 判定该持仓能否加第二层（原实现 :1376-1409）。
func canAddSecondLayer(row Row, policy StrategyConfig) bool {
	if isLowBuyStrategy(policy.StrategyID) {
		return false
	}
	currentLayers := row.PositionLayers
	if currentLayers < 1 {
		currentLayers = 1
	}
	if currentLayers >= policy.MaxLayersPerStock {
		return false
	}
	if IsStructureBroken(row, policy.StrategyID, policy.MarketState,
		ProfitProtectStartPct, ProfitProtectRetrace) {
		return false
	}
	if NeedsReboundExit(row, policy.StrategyID, policy.MarketState,
		ProfitProtectStartPct, ProfitProtectRetrace) {
		return false
	}
	if isOverheatedCandidate(row, policy.MarketState) {
		return false
	}
	if !isStrongStockForLayering(row, policy, currentLayers) {
		return false
	}

	closePrice := row.Close
	openPrice := row.Open
	highPrice := row.High
	if highPrice <= 0 {
		highPrice = closePrice
	}
	lowPrice := row.Low
	if lowPrice <= 0 {
		lowPrice = closePrice
	}

	ma20Confirmed := isConfirmedPullbackRebound(openPrice, highPrice, lowPrice, closePrice, row.PctChg, row.Ma20)
	ma30Confirmed := isConfirmedPullbackRebound(openPrice, highPrice, lowPrice, closePrice, row.PctChg, row.Ma30)
	continuationAdd := isBullTrendContinuationAddSignal(row, policy, currentLayers)

	if currentLayers >= 2 {
		if row.HoldingTradeDays < 3 {
			return false
		}
		if row.PctChg < 0.3 {
			return false
		}
		return ma20Confirmed || continuationAdd
	}
	return ma20Confirmed || ma30Confirmed || continuationAdd
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
