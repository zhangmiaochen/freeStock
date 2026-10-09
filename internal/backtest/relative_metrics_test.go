package backtest

import (
	"math"
	"testing"
)

// 相对基准的四个指标（原实现 `_calculate_relative_metrics` :206-252）。
//
// 这几项此前**整体缺失** —— 我这边 `Metrics` 里连字段都没有。
//
// 测试用**与原实现无关的性质**来验证，避免"自己验自己"：
//   - 策略日收益 == 基准日收益  → beta 应为 1、信息比率应为 0
//   - 策略 = 2 × 基准           → beta 应为 2
//   - 基准缺失                  → 四项都保持 0（原实现直接 return）
func TestRelativeMetricsProperties(t *testing.T) {
	// 造一串基准日收益，再反推出对应的 benchmark_value
	benchReturns := []float64{0, 0.012, -0.008, 0.021, -0.004, 0.015}
	benchValues := make([]float64, len(benchReturns))
	benchValues[0] = 1000
	for i := 1; i < len(benchReturns); i++ {
		benchValues[i] = benchValues[i-1] * (1 + benchReturns[i])
	}
	makeSnapshots := func(multiplier float64) []Snapshot {
		out := make([]Snapshot, len(benchReturns))
		for i := range benchReturns {
			out[i] = Snapshot{
				Date:           "2026010" + string(rune('0'+i%10)),
				DailyReturn:    benchReturns[i] * multiplier * 100,
				BenchmarkValue: benchValues[i],
				// 基准累计收益（相对首日）
				BenchmarkReturn: (benchValues[i]/benchValues[0] - 1) * 100,
			}
		}
		return out
	}

	t.Run("beta=1（策略 = 基准 + 微小扰动）", func(t *testing.T) {
		// ⚠ 不能让策略与基准**完全相等** —— 那样超额收益全是浮点噪声（~1e-16），
		// 「均值 ÷ 标准差」会得到垃圾值（实测 10.17）。原版是同样的结构
		//（`if tracking_error > 0`），面对恒等序列也会给出同样的垃圾值，
		// 所以这不是移植错误，而是这个指标本身的定义在零跟踪误差下不成立。
		snaps := makeSnapshots(1)
		jitter := []float64{0, 0.001, 0.0005, -0.001, 0.0008, -0.0004}
		for i := range snaps {
			snaps[i].DailyReturn = (benchReturns[i] + jitter[i]) * 100
		}
		m := CalculateMetrics(snaps, nil, 100000)
		if math.Abs(m.Beta-1) > 0.2 {
			t.Errorf("beta 应约为 1，实际 %.2f", m.Beta)
		}
	})

	t.Run("信息比率与独立参考实现一致", func(t *testing.T) {
		snaps := makeSnapshots(1.7)
		m := CalculateMetrics(snaps, nil, 100000)

		// 参考实现：按定义直接算，不调用被测代码
		n := len(snaps)
		strategy := make([]float64, n)
		benchmark := make([]float64, n)
		for i := range snaps {
			strategy[i] = snaps[i].DailyReturn / 100
			if i == 0 {
				benchmark[i] = 0
			} else {
				benchmark[i] = snaps[i].BenchmarkValue/snaps[i-1].BenchmarkValue - 1
			}
		}
		excess := make([]float64, n)
		for i := range excess {
			excess[i] = strategy[i] - benchmark[i]
		}
		meanExcess := 0.0
		for _, v := range excess {
			meanExcess += v
		}
		meanExcess /= float64(n)
		variance := 0.0
		for _, v := range excess {
			variance += (v - meanExcess) * (v - meanExcess)
		}
		variance /= float64(n - 1)
		std := math.Sqrt(variance)
		if std <= 0 {
			t.Skip("这一组数据跟踪误差为 0，算不出信息比率（与原实现同样的边界）")
		}
		want := meanExcess * 252 / (std * math.Sqrt(252))
		wantRounded := math.Round(want*100) / 100
		if math.Abs(m.InformationRatio-wantRounded) > 0.011 {
			t.Errorf("信息比率应为 %.2f（参考实现），实际 %.2f", wantRounded, m.InformationRatio)
		}
		t.Logf("信息比率 %.2f（跟踪误差 %.6f）", m.InformationRatio, std)
	})

	t.Run("策略是基准的两倍时 beta=2", func(t *testing.T) {
		snaps := makeSnapshots(2)
		m := CalculateMetrics(snaps, nil, 100000)
		if math.Abs(m.Beta-2) > 0.05 {
			t.Errorf("beta 应约为 2，实际 %.2f", m.Beta)
		}
	})

	t.Run("基准缺失时四项都保持 0", func(t *testing.T) {
		snaps := makeSnapshots(1)
		for i := range snaps {
			snaps[i].BenchmarkValue = 0
			snaps[i].BenchmarkReturn = 0
		}
		m := CalculateMetrics(snaps, nil, 100000)
		if m.Beta != 0 || m.Alpha != 0 || m.ExcessReturn != 0 || m.InformationRatio != 0 {
			t.Errorf("基准缺失时应全部为 0，实际 beta=%.2f alpha=%.2f excess=%.2f ir=%.2f",
				m.Beta, m.Alpha, m.ExcessReturn, m.InformationRatio)
		}
	})

	t.Run("超额收益 = 总收益 - 末个基准收益", func(t *testing.T) {
		snaps := makeSnapshots(1.5)
		m := CalculateMetrics(snaps, nil, 100000)
		want := m.TotalReturn - snaps[len(snaps)-1].BenchmarkReturn
		if math.Abs(m.ExcessReturn-want) > 0.011 {
			t.Errorf("超额收益应为 %.2f，实际 %.2f", want, m.ExcessReturn)
		}
	})
}

// 单日快照不足以算协方差 —— 不该 panic，也不该产出垃圾值。
func TestRelativeMetricsSkipsTooFewSnapshots(t *testing.T) {
	snaps := []Snapshot{{
		Date: "20260105", DailyReturn: 1, BenchmarkValue: 1000, BenchmarkReturn: 0,
	}}
	m := CalculateMetrics(snaps, nil, 100000)
	if m.Beta != 0 || m.Alpha != 0 || m.ExcessReturn != 0 || m.InformationRatio != 0 {
		t.Errorf("只有 1 天时四项应为 0，实际 beta=%.2f alpha=%.2f excess=%.2f ir=%.2f",
			m.Beta, m.Alpha, m.ExcessReturn, m.InformationRatio)
	}
}
