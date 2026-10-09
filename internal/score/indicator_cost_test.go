package score

import (
	"fmt"
	"testing"
	"time"
)

// 基准：320 根 K 线算一次指标要多久？
//
// 回填里 99% 的时间都在这一步（实测 4 天 × 300 只 = 15.2s），
// 换算下来每次约 12.7ms —— 对 320 根 K 线来说这个数字高得离谱，
// 先用基准把真实数字量出来，再决定优化什么。
func BenchmarkComputeIndicators(b *testing.B) {
	for _, n := range []int{320, 500} {
		bars := syntheticBars(n)
		b.Run(fmt.Sprintf("bars=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, ok := ComputeIndicators(bars); !ok {
					b.Fatal("算不出指标")
				}
			}
		})
	}
}

// 基准：一次完整的"每只股票"计算（指标 + 修复特征 + 前高）。
func BenchmarkPerCodeComputation(b *testing.B) {
	bars := syntheticBars(320)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ind, ok := ComputeIndicators(bars)
		if !ok {
			b.Fatal("算不出指标")
		}
		_ = BuildRepairFeatures(bars)
		_ = PrevHigh60(bars)
		_ = ind
	}
}

// 单次计时（比基准更直观，能直接对照回填里的 12.7ms）。
func TestComputeIndicatorsCost(t *testing.T) {
	bars := syntheticBars(320)
	const rounds = 200
	began := time.Now()
	for i := 0; i < rounds; i++ {
		if _, ok := ComputeIndicators(bars); !ok {
			t.Fatal("算不出指标")
		}
	}
	per := time.Since(began) / rounds
	t.Logf("ComputeIndicators（320 根）：单次 %s", per.Round(time.Microsecond))

	began = time.Now()
	for i := 0; i < rounds; i++ {
		_, _ = ComputeIndicators(bars)
		_ = BuildRepairFeatures(bars)
		_ = PrevHigh60(bars)
	}
	perAll := time.Since(began) / rounds
	t.Logf("整只股票（指标+修复+前高）：单次 %s", perAll.Round(time.Microsecond))

	// 对照：回填实测平均 12.7ms/次（含取数）
	t.Logf("回填实测平均 12.7ms/（只·天）—— 若上面远小于它，说明瓶颈不在纯计算")
}

func syntheticBars(n int) []Bar {
	bars := make([]Bar, n)
	price := 10.0
	for i := range bars {
		price *= 1 + float64((i%7)-3)/1000
		bars[i] = Bar{
			Date:   int64(20200101 + i),
			Open:   price * 0.995,
			High:   price * 1.01,
			Low:    price * 0.99,
			Close:  price,
			Volume: float64(1000000 + i*1000),
			Amount: float64(10000000 + i*10000),
		}
	}
	return bars
}
