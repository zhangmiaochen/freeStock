package chanlun

// macdPoint 是某一根 K 线的 MACD 取值。
//
// 口径与前端 `frontend/src/lib/indicators.ts` 的 `macd()` 保持一致：
// EMA(12/26) 求 DIF，EMA(DIF, 9) 求 DEA，柱 = 2 × (DIF − DEA)。
// 引擎只用它算"力度"（柱面积、黄白线高度），不参与结构定义。
type macdPoint struct {
	Diff float64
	Dea  float64
	Hist float64
}

// ema 计算指数移动平均，首值取序列首值。
func ema(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	if len(values) == 0 || period <= 0 {
		return out
	}
	alpha := 2.0 / (float64(period) + 1.0)
	out[0] = values[0]
	for i := 1; i < len(values); i++ {
		out[i] = out[i-1] + alpha*(values[i]-out[i-1])
	}
	return out
}

// computeMACD 计算整段 K 线的 MACD。
func computeMACD(bars []Bar, fast, slow, signal int) []macdPoint {
	out := make([]macdPoint, len(bars))
	if len(bars) == 0 {
		return out
	}
	if fast <= 0 {
		fast = 12
	}
	if slow <= fast {
		slow = fast + 1
	}
	if signal <= 0 {
		signal = 9
	}

	closes := make([]float64, len(bars))
	for i, bar := range bars {
		closes[i] = bar.Close
	}
	fastEMA := ema(closes, fast)
	slowEMA := ema(closes, slow)

	diff := make([]float64, len(bars))
	for i := range bars {
		diff[i] = fastEMA[i] - slowEMA[i]
	}
	dea := ema(diff, signal)

	for i := range bars {
		out[i] = macdPoint{
			Diff: diff[i],
			Dea:  dea[i],
			Hist: 2 * (diff[i] - dea[i]),
		}
	}
	return out
}
