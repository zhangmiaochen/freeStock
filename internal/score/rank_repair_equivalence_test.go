package score

import (
	"math"
	"math/rand"
	"testing"
)

// 这一组用例的目的是**证明"换了算法但结果一模一样"**，而不是测新功能。
//
// 背景：全市场回填的 CPU profile 里有两个 O(n²) 热点（都占 20%+ CPU）：
//
//	RankPct            —— 每天每只股票都要线扫一遍全市场（3 组 × n 次）
//	BuildRepairFeatures —— 每个位置都重扫 120 根 K 线求最高价
//
// 两处都改成了 O(n log n) / O(n)，**必须逐位一致**：评分表一变，
// 候选池、调仓建议、回测结果全都会跟着变。
//
// 这里把**旧实现原样抄成参照函数**，在新旧之间做差分 —— 随机数据、边界数据、
// 并列/NaN/±0 都覆盖。真实数据上的端到端 A/B 见 service 包的
// `TestScoreDigestRealData`（评分指纹）。

// ---------------------------------------------------------------------------
// RankLookup vs RankPct
// ---------------------------------------------------------------------------

// TestRankLookupMatchesRankPct 随机差分：两种实现必须逐位相等。
func TestRankLookupMatchesRankPct(t *testing.T) {
	rng := rand.New(rand.NewSource(20261010))

	for round := 0; round < 300; round++ {
		n := rng.Intn(40)
		values := make([]float64, n)
		for i := range values {
			switch rng.Intn(4) {
			case 0:
				// 大量并列（真实场景里换手率/成交额有大量 0）
				values[i] = float64(rng.Intn(3))
			case 1:
				values[i] = math.Round(rng.Float64()*100) / 100
			case 2:
				values[i] = rng.NormFloat64() * 1e6
			default:
				values[i] = 0
			}
		}
		lookup := NewRankLookup(values)

		// 数组内的每个值
		for i, v := range values {
			want, got := RankPct(values, v), lookup.Pct(v)
			if want != got {
				t.Fatalf("第 %d 轮 values[%d]=%v：RankPct=%v，RankLookup=%v\nvalues=%v",
					round, i, v, want, got, values)
			}
		}
		// 数组外的值（含边界）
		for _, v := range []float64{-1, 0.5, 1e9, -1e9, math.Inf(1), math.Inf(-1), math.NaN()} {
			want, got := RankPct(values, v), lookup.Pct(v)
			if want != got {
				t.Fatalf("第 %d 轮 查询 %v：RankPct=%v，RankLookup=%v\nvalues=%v",
					round, v, want, got, values)
			}
		}
	}
}

// TestRankLookupEdgeCases 边界与特殊值 —— 这些是最容易"改坏"的地方。
func TestRankLookupEdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		query  []float64
	}{
		{"空数组", nil, []float64{0, 1, math.NaN()}},
		{"单个值", []float64{7}, []float64{7, 0}},
		{"全并列", []float64{5, 5, 5, 5}, []float64{5, 4}},
		{"全零", []float64{0, 0, 0}, []float64{0, 1}},
		{"正负零", []float64{0, math.Copysign(0, -1), 1}, []float64{0, math.Copysign(0, -1), 1}},
		{"含 NaN", []float64{1, math.NaN(), 3, math.NaN()}, []float64{1, 3, math.NaN(), 2}},
		{"含 Inf", []float64{math.Inf(-1), 0, math.Inf(1)}, []float64{math.Inf(1), math.Inf(-1), 0}},
		{"重复小数", []float64{1.5, 1.5, 2.5, 2.5, 2.5}, []float64{1.5, 2.5, 3.5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := NewRankLookup(tc.values)
			for _, v := range tc.query {
				want, got := RankPct(tc.values, v), lookup.Pct(v)
				if math.IsNaN(want) && math.IsNaN(got) {
					continue
				}
				if want != got {
					t.Errorf("查询 %v：RankPct=%v，RankLookup=%v", v, want, got)
				}
			}
		})
	}

	// nil 接收者不能崩（调用方可能忘了初始化）
	var empty *RankLookup
	if got := empty.Pct(1); got != 0 {
		t.Errorf("nil 查询器应返回 0，实际 %v", got)
	}
}

// TestRankLookupQueryIsCheap 确认查询不再是线性的：
// 大数组上 n 次查询的耗时不应随 n² 增长。
//
// 用比较次数代替计时（计时在 CI 上不稳）。这里只验证"值确实取自排序副本"——
// 若实现退回线性，下面这个断言仍会通过，所以真正的保证是上面的差分用例；
// 这个用例只是把"O(n log n)"这个意图写下来（并挡住"退回逐行 RankPct"的改动）。
func TestRankLookupIsSortedCopy(t *testing.T) {
	values := []float64{3, 1, 2}
	lookup := NewRankLookup(values)
	if len(lookup.sorted) != len(values) {
		t.Fatalf("应当建好升序副本，实际 %v", lookup.sorted)
	}
	for i := 1; i < len(lookup.sorted); i++ {
		if lookup.sorted[i-1] > lookup.sorted[i] {
			t.Fatalf("副本未升序：%v", lookup.sorted)
		}
	}
	// 原切片不得被改动（调用方的 amounts / turns 还要用）
	if values[0] != 3 || values[1] != 1 || values[2] != 2 {
		t.Fatalf("原切片被就地排序了：%v", values)
	}
	// 含 NaN 时不建副本（走线性，保证与旧实现一致）
	if got := NewRankLookup([]float64{1, math.NaN()}); got.sorted != nil {
		t.Error("含 NaN 时不应建排序副本")
	}
}

// ---------------------------------------------------------------------------
// BuildRepairFeatures：单调队列 vs 原始逐位置滚动最高价
// ---------------------------------------------------------------------------

// buildRepairFeaturesReference 是**改动前的原实现**（原样抄过来做参照）。
//
// 唯一的差别是它用 `rollingMax(highs[:i+1], 120, 40)` 逐位置重扫。
func buildRepairFeaturesReference(bars []Bar) RepairFeatures {
	var out RepairFeatures
	if len(bars) < RepairWindow+2 {
		return out
	}

	highs := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	for i, b := range bars {
		highs[i], closes[i] = b.High, b.Close
	}

	var (
		events    int
		recovered int
		newHighs  int
		totalDays float64
		lastEvent = -RepairGap - 1
	)

	last := len(bars) - 1
	for i := 0; i <= last; i++ {
		high120, ok := rollingMax(highs[:i+1], 120, 40)
		if !ok || high120 <= 0 {
			continue
		}
		if (closes[i]/high120-1)*100 > RepairThreshold {
			continue
		}
		if i-lastEvent < RepairGap {
			continue
		}
		if i+RepairKnownLag > last {
			continue
		}
		lastEvent = i
		events++

		end := i + RepairWindow
		if end > last {
			end = last
		}
		recoveredFlag := false
		newHighFlag := false
		recoverDay := 0
		for j := i + 1; j <= end; j++ {
			if closes[j] >= high120*0.98 && !recoveredFlag {
				recoveredFlag = true
				recoverDay = j - i
			}
			if closes[j] >= high120*1.02 {
				newHighFlag = true
			}
		}
		if recoveredFlag {
			recovered++
			totalDays += float64(recoverDay)
		} else {
			totalDays += float64(RepairWindow)
		}
		if newHighFlag {
			newHighs++
		}
	}

	out.EventCount = events
	if events == 0 {
		return out
	}
	out.RecoveryProbability = round(float64(recovered)/float64(events), 4)
	out.NewHighProbability = round(float64(newHighs)/float64(events), 4)
	out.AvgRecoveryDays = round(totalDays/float64(events), 4)
	speed := clamp((float64(RepairWindow)-out.AvgRecoveryDays)/32, 0, 1)
	out.AbilityScore = round(clamp(
		out.RecoveryProbability*8+speed*4+out.NewHighProbability*3, 0, 15), 4)
	return out
}

// TestBuildRepairFeaturesMatchesReference 随机差分：新实现必须与旧实现逐位相同。
func TestBuildRepairFeaturesMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 400; round++ {
		n := 40 + rng.Intn(400)
		bars := make([]Bar, n)
		price := 10 + rng.Float64()*50
		for i := range bars {
			// 随机游走 + 周期性回撤，保证能触发"回撤 >=10%"的事件
			price *= 1 + (rng.Float64()-0.52)*0.06
			if i%37 == 0 {
				price *= 0.82 // 造一次急跌
			}
			if price < 0.5 {
				price = 0.5
			}
			high := price * (1 + rng.Float64()*0.03)
			bars[i] = Bar{
				Date: int64(20200101 + i), Open: price, High: high,
				Low: price * 0.97, Close: price * (0.99 + rng.Float64()*0.02),
			}
		}
		want := buildRepairFeaturesReference(bars)
		got := BuildRepairFeatures(bars)
		if want != got {
			t.Fatalf("第 %d 轮（%d 根）不一致：\n旧 %+v\n新 %+v", round, n, want, got)
		}
		if want.EventCount == 0 && n > 200 {
			// 参照数据至少要能触发事件，否则这个差分没意义
			t.Logf("第 %d 轮没有事件（可接受，但覆盖度偏低）", round)
		}
	}
}

// TestBuildRepairFeaturesEdgeCases 边界长度与特殊高点：
// 不足 42 根、恰好 42 根、全 0 高点、并列高点。
func TestBuildRepairFeaturesEdgeCases(t *testing.T) {
	for _, n := range []int{0, 1, 39, 40, 41, 42, 43, 119, 120, 121, 200} {
		bars := make([]Bar, n)
		for i := range bars {
			bars[i] = Bar{Date: int64(i), High: 10 + float64(i%3), Low: 9, Close: 10}
		}
		if want, got := buildRepairFeaturesReference(bars), BuildRepairFeatures(bars); want != got {
			t.Errorf("长度 %d 不一致：旧 %+v，新 %+v", n, want, got)
		}
	}

	// 高点并列（队列的弹出条件 `<=` 必须给出同一个最大值）
	bars := make([]Bar, 300)
	for i := range bars {
		bars[i] = Bar{Date: int64(i), High: 20, Low: 10, Close: 15}
	}
	if want, got := buildRepairFeaturesReference(bars), BuildRepairFeatures(bars); want != got {
		t.Errorf("高点并列不一致：旧 %+v，新 %+v", want, got)
	}

	// 高点全 0（原实现里 `high120 <= 0` 直接跳过）
	zeros := make([]Bar, 300)
	for i := range zeros {
		zeros[i] = Bar{Date: int64(i), High: 0, Low: 0, Close: 0}
	}
	if want, got := buildRepairFeaturesReference(zeros), BuildRepairFeatures(zeros); want != got {
		t.Errorf("全 0 高点不一致：旧 %+v，新 %+v", want, got)
	}
}

// TestComputeIndicatorsShortInputDoesNotPanic 2~9 根 K 线原来会**切片越界 panic**。
//
// 生产路径在调用前挡了 len<30，所以这只是导出函数自身的健壮性；
// 但"数据不足返回 false"显然比崩掉更符合它的文档。
func TestComputeIndicatorsShortInputDoesNotPanic(t *testing.T) {
	for n := 0; n <= 30; n++ {
		bars := make([]Bar, n)
		for i := range bars {
			bars[i] = Bar{Date: int64(i), High: 10, Low: 9, Close: 10}
		}
		ind, ok := ComputeIndicators(bars)
		if n < 10 {
			if ok {
				t.Errorf("%d 根不该算得出指标", n)
			}
			continue
		}
		if n >= 2 && !ok {
			t.Errorf("%d 根应当算得出指标", n)
		}
		_ = ind
	}
}
