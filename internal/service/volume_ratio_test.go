package service

import (
	"context"
	"testing"

	"freestock/internal/cnlake"
)

// ---------------------------------------------------------------------------
// 量比（数据湖）
//
// 背景：free-stockdb 的日K自带 `vol_ratio` 列，而**数据湖的 daily_bars 只有 OHLCV**，
// 所以数据湖模式下必须自己推导。早先 buildQuote 漏了这一步，
// 于是"本地数据湖"时 K 线页的量比恒为空 —— 换回 free-stockdb 就正常，
// 很容易被误判成"数据缺失"而不是"没算"。
// ---------------------------------------------------------------------------

func TestVolumeRatioBasic(t *testing.T) {
	// 前 5 日均量 200，当日 400 → 2.0
	prior := []float64{100, 150, 200, 250, 300} // 均值 200
	if got := volumeRatio(400, prior); got != 2 {
		t.Errorf("volumeRatio(400, 均值200) = %v，期望 2", got)
	}

	// 当日缩量：前 5 日均量 200，当日 100 → 0.5
	if got := volumeRatio(100, prior); got != 0.5 {
		t.Errorf("volumeRatio(100, 均值200) = %v，期望 0.5", got)
	}
}

// TestVolumeRatioUsesLastFiveOnly 只取**紧邻当日的 5 个**，更早的不参与。
func TestVolumeRatioUsesLastFiveOnly(t *testing.T) {
	// 前面塞一些夸张的历史值；只有末尾 5 个（全是 100）该被用到
	prior := []float64{10000, 10000, 10000, 100, 100, 100, 100, 100}
	if got := volumeRatio(200, prior); got != 2 {
		t.Errorf("应当只用末尾 5 个（均值 100）→ 2.0，实际 %v", got)
	}
}

// TestVolumeRatioExcludesToday 分母**不含当日**。
//
// 这条是量比定义里最容易写错的地方：把当日算进自己的基准，
// 会把倍数系统性压低（放量日尤其明显）。
func TestVolumeRatioExcludesToday(t *testing.T) {
	// prior 里若是"含当日"的 5 个（当日 500 也在内），均值会被拉高、倍数偏小
	excludeToday := []float64{100, 100, 100, 100, 100}
	includeToday := []float64{100, 100, 100, 100, 500}

	excl := volumeRatio(500, excludeToday)
	incl := volumeRatio(500, includeToday)
	if excl != 5 {
		t.Errorf("不含当日时应当是 5，实际 %v", excl)
	}
	if incl >= excl {
		t.Errorf("含当日会把倍数压低：含=%v 不含=%v", incl, excl)
	}
}

// TestVolumeRatioNeedsFullWindow 前 N 日不足时返回 0（界面显示「—」），
// 而不是拿不足的样本硬算。
func TestVolumeRatioNeedsFullWindow(t *testing.T) {
	cases := [][]float64{
		nil,
		{},
		{100},
		{100, 100},
		{100, 100, 100},
		{100, 100, 100, 100}, // 差一个
	}
	for _, prior := range cases {
		if got := volumeRatio(500, prior); got != 0 {
			t.Errorf("样本不足（%d 个）时应当返回 0，实际 %v", len(prior), got)
		}
	}
	// 正好 5 个就可以算
	if got := volumeRatio(500, []float64{100, 100, 100, 100, 100}); got != 5 {
		t.Errorf("正好 5 个应当能算，实际 %v", got)
	}
}

// TestVolumeRatioGuardsZero 当日或分母为 0 时返回 0，不产生 Inf / NaN。
func TestVolumeRatioGuardsZero(t *testing.T) {
	prior := []float64{100, 100, 100, 100, 100}
	if got := volumeRatio(0, prior); got != 0 {
		t.Errorf("当日成交量为 0 时应当返回 0，实际 %v", got)
	}
	if got := volumeRatio(100, []float64{0, 0, 0, 0, 0}); got != 0 {
		t.Errorf("分母均量为 0 时应当返回 0，实际 %v", got)
	}
}

// TestVolumeRatioRounds volumeRatio 的结果保留 4 位小数（与 daily_bars 的其它派生字段一致）。
func TestVolumeRatioRounds(t *testing.T) {
	got := volumeRatio(12345678, []float64{10000000, 9000000, 11000000, 10500000, 9500000})
	if got != 1.2346 {
		t.Errorf("应当四舍五入到 4 位，实际 %v", got)
	}
}

// TestAssembleQuotesFillsVolRatio 端到端：区间行情里每一根都带上量比。
//
// 这是本 bug 的直接回归 —— 数据湖模式下量比曾经恒为 0。
func TestAssembleQuotesFillsVolRatio(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)

	// 用合成湖里的一只标的取一段足够长的日线
	codes, err := provider.StockCodes(context.Background())
	if err != nil {
		t.Fatalf("StockCodes 失败: %v", err)
	}
	var code string
	for _, list := range codes {
		if len(list) > 0 {
			code = list[0]
			break
		}
	}
	if code == "" {
		t.Fatal("合成湖里没有标的")
	}

	quotes, err := provider.KlineHistory(context.Background(), code, 0, 20991231)
	if err != nil {
		t.Fatalf("KlineHistory 失败: %v", err)
	}
	if len(quotes) < volumeRatioDays+1 {
		t.Skipf("合成湖只有 %d 根，不足以验证量比", len(quotes))
	}

	// 前 volumeRatioDays 根必然算不出来（历史不足），从第 volumeRatioDays+1 根起应当有值
	for i, q := range quotes {
		if i < volumeRatioDays {
			continue
		}
		if q.VolRatio <= 0 {
			t.Errorf("第 %d 根（date=%d）量比为 %v —— 前面已经有 %d 根历史，应当能算出来",
				i, q.Date, q.VolRatio, i)
		}
	}

	// 逐个复核：用前 volumeRatioDays 根的量手算
	last := quotes[len(quotes)-1]
	prior := make([]float64, 0, volumeRatioDays)
	for _, q := range quotes[len(quotes)-1-volumeRatioDays : len(quotes)-1] {
		prior = append(prior, q.Volume)
	}
	want := volumeRatio(last.Volume, prior)
	if last.VolRatio != want {
		t.Errorf("最后一根量比 = %v，按前 %d 根手算 = %v", last.VolRatio, volumeRatioDays, want)
	}
}

// TestQuoteRangeKeepsVolRatioAtWindowStart 区间窗口的**头几根**也要有量比。
//
// ⚠ 这条钉的是"回看窗口跨过 start 边界"：`quoteRange` 特意**不把取数裁到
// [start, end]**（裁了会让 `start == end` 时前收变成 0），所以 bars 里带着
// start 之前的行 —— 那些行不进结果，但**必须进量比的分母**。
// 否则"看最近几天的量比"时前几根会全是「—」，而那正是最常看的场景。
func TestQuoteRangeKeepsVolRatioAtWindowStart(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)

	codes, err := provider.StockCodes(context.Background())
	if err != nil {
		t.Fatalf("StockCodes 失败: %v", err)
	}
	var code string
	for _, list := range codes {
		if len(list) > 0 {
			code = list[0]
			break
		}
	}
	if code == "" {
		t.Fatal("合成湖里没有标的")
	}

	all, err := provider.KlineHistory(context.Background(), code, 0, 20991231)
	if err != nil {
		t.Fatalf("KlineHistory 失败: %v", err)
	}
	if len(all) < 2*volumeRatioDays+2 {
		t.Skipf("合成湖只有 %d 根，不足以验证窗口起点", len(all))
	}

	// 取一个后面还留着若干根的窗口，保证"start 之前有足够历史"
	start := int(all[volumeRatioDays+1].Date)
	end := int(all[len(all)-1].Date)
	window, err := provider.QuoteRangeDesc(context.Background(), code, start, end)
	if err != nil {
		t.Fatalf("QuoteRangeDesc 失败: %v", err)
	}
	if len(window) == 0 {
		t.Fatal("窗口里一根都没有")
	}

	// 倒序返回，所以最后一根是最早的那根
	earliest := window[len(window)-1]
	if earliest.VolRatio <= 0 {
		t.Errorf("窗口最早一根（date=%d）量比为 %v —— start 之前的历史没有进分母",
			earliest.Date, earliest.VolRatio)
	}
}

// TestPriorVolumesDropsLast 辅助函数只取"最后一根之前"的成交量。
//
// 它作用于数据湖的原始日线（`cnlake.BarRow`），因为三条取数路径手里拿到的
// 都是这个类型；顺手验证一根 / 零根的边界。
func TestPriorVolumesDropsLast(t *testing.T) {
	if got := priorVolumes(nil); got != nil {
		t.Errorf("空输入应当返回 nil，实际 %v", got)
	}
	if got := priorVolumes([]cnlake.BarRow{{Volume: 1}}); got != nil {
		t.Errorf("只有一根时应当返回 nil（没有「之前」），实际 %v", got)
	}
	got := priorVolumes([]cnlake.BarRow{{Volume: 1}, {Volume: 2}, {Volume: 3}})
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("应当丢掉最后一根，实际 %v", got)
	}
}
