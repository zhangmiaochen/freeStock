package valuation

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// 市场隐含预期（反向 DCF）的测试
//
// 这一组用例的重点不是"数字对不对"（那需要外部基准），而是**内部自洽与单调性**：
// 反解是数值过程，最容易出的问题是"解出来的增速代回正向模型不等于当前价"
// 或者"价格越高解出增速越低"这类符号/单调性错误。
//
// ⚠ 测试数据的选取说明（第一版写错过）：
// 现金流折现是严格单调的，所以**存在一个下限市值** —— 低于它，
// 任何增速（哪怕是 −50% 的持续萎缩）折出来的价值都高于市价，反解无解。
// 这个下限大致是 FCF 的 3.5 倍（WACC 9%、稳态增长 2.5% 下）。
// 第一版用了「FCF 50 亿、市值 50 亿」（P/FCF = 1），于是价格 5 元那一档
// 必然无解，测试反而在考"模型拒绝不合理输入"这件事。
// 所以下面统一用 **FCF 20 亿 / 10 亿股 / 40~400 元**（P/FCF 20~200 倍）。
// ---------------------------------------------------------------------------

// impliedParams 是一组稳定的模型参数（不依赖 DefaultParams 的具体取值，
// 免得调参把测试搞红）。
func impliedParams() Params {
	p := DefaultParams()
	p.WACC = 0.09
	p.TerminalGrowth = 0.025
	return p
}

const (
	impliedTestFCF    = 20e8 // 20 亿自由现金流
	impliedTestShares = 10e8 // 10 亿股
)

// priceForImpliedGrowth 反解"想让市场隐含增速等于 g 时，股价该是多少"。
//
// 用它而不是硬编码价格：搜索区间上限是 80%，硬编码的价格很容易撞上上限，
// 于是断言变成在比较两个被夹住的值。用这个辅助函数可以让每条用例都落在
// **区间内部**，测的是反解本身而不是边界行为。
func priceForImpliedGrowth(t *testing.T, g float64, params Params) float64 {
	t.Helper()
	value := dcfValue(impliedTestFCF, g, params.WACC, params.TerminalGrowth)
	if value <= 0 {
		t.Fatalf("目标增速 %.2f 对应非正价值", g)
	}
	return value / impliedTestShares
}

// TestReverseDCFIsSelfConsistent 是**最关键的一条**：反解出的增速代回正向
// DCF，必须重现当前市值。这是"反向与正向同源"的机械验证。
func TestReverseDCFIsSelfConsistent(t *testing.T) {
	params := impliedParams()

	for _, target := range []float64{-0.10, 0.05, 0.20, 0.45} {
		price := priceForImpliedGrowth(t, target, params)
		got := EvaluateImpliedExpectation(
			0, price, impliedTestShares, impliedTestFCF, 4,
			0.15, "近 3 年营收 CAGR", 70, 0.8, params)
		if !got.Available {
			t.Fatalf("目标增速 %.2f 对应价格 %.2f 应可用，实际不可用：%s", target, price, got.Reason)
		}
		if got.Unreachable {
			t.Fatalf("目标增速 %.2f 落在区间内，不该标记 Unreachable", target)
		}
		if math.Abs(got.ImpliedGrowth-target) > 1e-6 {
			t.Errorf("目标增速 %.4f 反解成 %.4f（价格 %.2f）", target, got.ImpliedGrowth, price)
		}

		valueBack := dcfValue(impliedTestFCF, got.ImpliedGrowth, params.WACC, params.TerminalGrowth)
		target := price * impliedTestShares
		relative := math.Abs(valueBack-target) / target
		if relative > 1e-6 {
			t.Errorf("价格 %.2f：反解增速 %.6f 代回正向得 %.6e，与目标市值 %.6e 相差 %.6f%%",
				price, got.ImpliedGrowth, valueBack, target, relative*100)
		}
	}
}

// TestReverseDCFRejectsUnreachableLowPrice 钉住"低到无解时如实拒绝，不给假数字"。
//
// ⚠ 这条对应一个真实踩过的 bug：早期版本把无解的增速**夹到区间下限返回**，
// 于是调用方拿到一个看起来正常、但代回正向模型对不上当前价的数字
// （实测相差 575%）。反解出来的数必须能自洽，否则宁可说不可用。
func TestReverseDCFRejectsUnreachableLowPrice(t *testing.T) {
	params := impliedParams()

	// 先算出模型的价值下限（−50% 增速下的折现值），再取一个明显低于它的价格
	floor := dcfValue(impliedTestFCF, impliedGrowthFloor, params.WACC, params.TerminalGrowth)
	belowFloorPrice := floor / impliedTestShares * 0.5
	t.Logf("模型价值下限 %.2f 亿（对应股价 %.2f 元），测试用价 %.2f 元",
		floor/1e8, floor/impliedTestShares, belowFloorPrice)

	got := EvaluateImpliedExpectation(
		0, belowFloorPrice, impliedTestShares, impliedTestFCF, 4,
		0.15, "基准", 70, 0.8, params)
	if got.Available {
		t.Errorf("价格低于模型价值下限时应判不可用，实际可用（隐含增速 %.4f）", got.ImpliedGrowth)
	}
	if got.Reason == "" {
		t.Error("不可用时必须说明原因")
	}
	t.Logf("低于下限：%s", got.Reason)
}

// TestReverseDCFMarksUnreachableHighPrice 钉住"高到超出区间时标记 Unreachable"。
//
// 与低价的处理刻意不同：高价时"市场要求 ≥80% 增速"是有信息量的结论
// （说明定价极端乐观），所以记下限值 + 标记，而不是判不可用。
func TestReverseDCFMarksUnreachableHighPrice(t *testing.T) {
	params := impliedParams()

	ceiling := dcfValue(impliedTestFCF, impliedGrowthCeil, params.WACC, params.TerminalGrowth)
	aboveCeilingPrice := ceiling / impliedTestShares * 1.5

	got := EvaluateImpliedExpectation(
		0, aboveCeilingPrice, impliedTestShares, impliedTestFCF, 4,
		0.15, "基准", 70, 0.8, params)
	if !got.Available {
		t.Fatalf("高价应仍可用（只是标记越界），实际不可用：%s", got.Reason)
	}
	if !got.Unreachable {
		t.Error("价格超出搜索区间上限时应标记 Unreachable")
	}
	if got.ImpliedGrowth != impliedGrowthCeil {
		t.Errorf("越界时应记区间上限 %.2f，实际 %.4f", impliedGrowthCeil, got.ImpliedGrowth)
	}
	if got.DurationCapped != true {
		t.Error("越界时持续时间也应标记为截断")
	}
	t.Logf("高于上限：隐含增速 %.0f%%（标记越界）、需持续 %.0f 年、结论 %s",
		got.ImpliedGrowth*100, got.ImpliedYears, got.Verdict)
}

// TestReverseDCFIsMonotonicInPrice 钉住"价格越高 → 市场要求增速越高"。
//
// 单调性是反解有意义的**前提**：如果更贵的股票反解出更低的增速，
// 说明符号或衰减方向写错了（这是数值反解最典型的错误）。
func TestReverseDCFIsMonotonicInPrice(t *testing.T) {
	params := impliedParams()

	previous := math.Inf(-1)
	for _, target := range []float64{-0.15, 0.0, 0.15, 0.30, 0.50} {
		price := priceForImpliedGrowth(t, target, params)
		got := EvaluateImpliedExpectation(0, price, impliedTestShares, impliedTestFCF, 4,
			0.15, "基准", 70, 0.8, params)
		if !got.Available {
			t.Fatalf("价格 %.2f 不可用：%s", price, got.Reason)
		}
		if got.ImpliedGrowth <= previous {
			t.Errorf("价格涨到 %.2f 时，隐含增速没有上升：%.6f ≤ 前一个 %.6f",
				price, got.ImpliedGrowth, previous)
		}
		t.Logf("价格 %.0f 元 → 隐含增速 %.1f%%", price, got.ImpliedGrowth*100)
		previous = got.ImpliedGrowth
	}
}

// TestImpliedDurationGrowsWithDemand 钉住"要求越高 → 需要的持续时间越长"。
//
// 这是本轮新增的核心量：单点增速不够，必须知道"要求几年"。
func TestImpliedDurationGrowsWithDemand(t *testing.T) {
	params := impliedParams()

	previous := -1.0
	for _, target := range []float64{0.0, 0.08, 0.16, 0.28} {
		price := priceForImpliedGrowth(t, target, params)
		got := EvaluateImpliedExpectation(0, price, impliedTestShares, impliedTestFCF, 4,
			0.15, "基准", 70, 0.8, params)
		if !got.Available {
			t.Fatalf("价格 %.2f 不可用：%s", price, got.Reason)
		}
		t.Logf("价格 %.0f 元 → 隐含增速 %.1f%%、需持续 %.1f 年（截断 %v）",
			price, got.ImpliedGrowth*100, got.ImpliedYears, got.DurationCapped)
		if got.ImpliedYears < previous {
			t.Errorf("价格 %.2f 的隐含持续时间 %.2f 年短于更低价格的 %.2f 年",
				price, got.ImpliedYears, previous)
		}
		previous = got.ImpliedYears
	}

	// 极端贵的价格必须撞到上限（而不是给出一个荒谬的大年数）
	extreme := EvaluateImpliedExpectation(0, 100000, impliedTestShares, impliedTestFCF, 4,
		0.15, "基准", 70, 0.8, params)
	if !extreme.DurationCapped {
		t.Errorf("极端价格下隐含持续时间应被截断到上限，实际 %.1f 年（未截断）", extreme.ImpliedYears)
	}
	if extreme.ImpliedYears != ImpliedDurationCap {
		t.Errorf("截断时年数应等于上限 %d，实际 %.1f", ImpliedDurationCap, extreme.ImpliedYears)
	}
}

// TestImpliedGrowthFallsBackWhenFCFNegative 钉住降级路径。
//
// ⚠ 这一条是这次替换的**要害**：旧实现在现金流为负时会退回 fairPE 反解
// （那正是循环论证），新实现必须**如实说明不可用**，让调用方降置信度。
func TestImpliedGrowthFallsBackWhenFCFNegative(t *testing.T) {
	params := impliedParams()

	negative := EvaluateImpliedExpectation(0, 50, impliedTestShares, -3e8, 4, 0.15, "基准", 70, 0.8, params)
	if negative.Available {
		t.Error("自由现金流为负时必须判为不可用（不能退回 PE 反解）")
	}
	if negative.Reason == "" {
		t.Error("不可用时必须说明原因")
	}
	t.Logf("FCF 为负：%s", negative.Reason)

	incomplete := EvaluateImpliedExpectation(0, 50, impliedTestShares, 10e8, 2, 0.15, "基准", 70, 0.8, params)
	if incomplete.Available {
		t.Error("现金流科目不完整时必须判为不可用")
	}
	t.Logf("现金流只有 2 个季度：%s", incomplete.Reason)

	noShares := EvaluateImpliedExpectation(0, 50, 0, 10e8, 4, 0.15, "基准", 70, 0.8, params)
	if noShares.Available {
		t.Error("缺少股本时必须判为不可用")
	}

	noPrice := EvaluateImpliedExpectation(0, 0, impliedTestShares, 10e8, 4, 0.15, "基准", 70, 0.8, params)
	if noPrice.Available {
		t.Error("缺少价格时必须判为不可用")
	}

	// 极端价格差 → 永续增长模型发散
	divergent := impliedParams()
	divergent.TerminalGrowth = divergent.WACC - 0.005
	diverge := EvaluateImpliedExpectation(0, 50, impliedTestShares, 10e8, 4, 0.15, "基准", 70, 0.8, divergent)
	if diverge.Available {
		t.Error("WACC 与稳态增长差不足 2 个百分点时必须判为不可用")
	}
}

// TestFulfillmentDowngradesOnWideGap 钉住兑现概率的"短板优先"逻辑。
func TestFulfillmentDowngradesOnWideGap(t *testing.T) {
	params := impliedParams()
	// 取一个隐含要求约 40% 的价格（区间内），让强/弱两方的 gap 有区分度
	expensive := priceForImpliedGrowth(t, 0.40, params)

	strong := EvaluateImpliedExpectation(0, expensive, impliedTestShares, impliedTestFCF, 4,
		0.30, "基准", 85, 0.9, params)
	weak := EvaluateImpliedExpectation(0, expensive, impliedTestShares, impliedTestFCF, 4,
		0.05, "基准", 40, 0.2, params)
	if !strong.Available || !weak.Available {
		t.Fatal("两条都应可用")
	}
	t.Logf("强（自身 30%% 增速/质量 85）：gap %.1f%%、兑现 %s", strong.GrowthGap*100, strong.Fulfillment)
	t.Logf("弱（自身 5%% 增速/质量 40）：gap %.1f%%、兑现 %s", weak.GrowthGap*100, weak.Fulfillment)

	if weak.Fulfillment != "low" {
		t.Errorf("自身能力远低于市场要求时兑现概率应为 low，实际 %q", weak.Fulfillment)
	}
	if fulfillmentRank(strong.Fulfillment) <= fulfillmentRank(weak.Fulfillment) {
		t.Errorf("自身能力更强的一方兑现档位应更高：强 %q vs 弱 %q",
			strong.Fulfillment, weak.Fulfillment)
	}

	// 乐观溢价必须体现为"增长快但质量差 → 不敢给 high"
	highGrowthLowQuality := EvaluateImpliedExpectation(0, 60, impliedTestShares, impliedTestFCF, 4,
		0.60, "基准", 30, 0.2, params)
	if highGrowthLowQuality.Fulfillment == "high" {
		t.Error("成长快但质量差、持续性弱时，兑现档位不该是 high")
	}
	t.Logf("自身 60%% 增速但质量 30/持续性 0.2：兑现 %s", highGrowthLowQuality.Fulfillment)

	// 差距很小、质量与持续性都好 → high
	easy := EvaluateImpliedExpectation(0, 40, impliedTestShares, impliedTestFCF, 4,
		0.60, "基准", 85, 0.9, params)
	if easy.Fulfillment != "high" {
		t.Errorf("市场要求明显低于自身能力且质量好时应为 high，实际 %q（gap %.1f%%）",
			easy.Fulfillment, easy.GrowthGap*100)
	}
}

// TestDownsideIfMissIsNegativeWhenPriceExceedsAchievable 钉住"落空跌幅"的符号与数量级。
func TestDownsideIfMissIsNegativeWhenPriceExceedsAchievable(t *testing.T) {
	params := impliedParams()

	// 价格远超"按自身增速实现"的价值 → 落空跌幅必须为负
	rich := EvaluateImpliedExpectation(0, 320, impliedTestShares, impliedTestFCF, 4,
		0.05, "基准", 60, 0.5, params)
	if !rich.Available {
		t.Fatal("应可用")
	}
	if rich.DownsideIfMiss >= 0 {
		t.Errorf("高价 + 低自身增速时，落空跌幅应为负，实际 %.4f", rich.DownsideIfMiss)
	}
	t.Logf("贵（320 元 / 自身 5%%）：按自身增速的合理价值 %.2f 元 → 落空 %.1f%%",
		rich.ValueAtAchievable, rich.DownsideIfMiss*100)

	// 价格低于可实现价值 → 落空反而是正收益（成长只要做到就还有空间）
	cheap := EvaluateImpliedExpectation(0, 40, impliedTestShares, impliedTestFCF, 4,
		0.45, "基准", 80, 0.9, params)
	if cheap.DownsideIfMiss <= 0 {
		t.Errorf("低价 + 高自身增速时，按自身增速的价值应高于现价，实际 %.4f（价值 %.2f）",
			cheap.DownsideIfMiss, cheap.ValueAtAchievable)
	}
	t.Logf("便宜（40 元 / 自身 45%%）：按自身增速的合理价值 %.2f 元 → %.1f%%",
		cheap.ValueAtAchievable, cheap.DownsideIfMiss*100)
}

// TestDecliningCompanyIsNotFlaggedOver 钉住"衰退股不被误判为高估"。
//
// 反例来自真实数据（盐湖股份）：自身增速 −20.4%、市场隐含 −9.1%，
// 差额 +11.3% 满足"差额够大"，但"市场要求它萎缩得少一点"不是高估。
// 只看差额会在自身增速为负时**系统性误判**。
func TestDecliningCompanyIsNotFlaggedOver(t *testing.T) {
	params := impliedParams()

	// 复现真实情形：市场隐含 −10%（在萎缩）、自身增速 −25%（萎缩更快）。
	// 差额 +15 个百分点，稳超 10 个百分点的门槛 —— 旧逻辑在这里会判 over。
	price := priceForImpliedGrowth(t, -0.10, params)
	declining := EvaluateImpliedExpectation(0, price, impliedTestShares, impliedTestFCF, 4,
		-0.25, "近 3 年营收 CAGR", 60, 0.5, params)
	if !declining.Available {
		t.Fatalf("应可用：%s", declining.Reason)
	}
	t.Logf("价格 %.2f 元：隐含 %.1f%% vs 自身 %.1f%% → gap %+.1f%%、结论 %s",
		price, declining.ImpliedGrowth*100, declining.AchievableGrowth*100,
		declining.GrowthGap*100, declining.Verdict)

	if declining.GrowthGap <= 0.10 {
		t.Fatalf("这条用例要覆盖「差额够大但隐含增速为负」，实际 gap 只有 %+.1f%%", declining.GrowthGap*100)
	}
	if declining.Verdict == "over" {
		t.Error("隐含增速为负时不该判为 over（那是衰退定价，不是高估）")
	}

	// 对照：同样的差额，但隐含要求是**实质性正增长** → 必须判 over
	growing := EvaluateImpliedExpectation(0, priceForImpliedGrowth(t, 0.25, params),
		impliedTestShares, impliedTestFCF, 4, 0.05, "近 3 年营收 CAGR", 60, 0.5, params)
	if growing.Verdict != "over" {
		t.Errorf("隐含 25%% 而自身只有 5%% 时应判 over，实际 %q", growing.Verdict)
	}
	t.Logf("对照：隐含 %.1f%% vs 自身 5%% → %s", growing.ImpliedGrowth*100, growing.Verdict)
}

// TestImpliedVerdictThresholds 钉住结论阈值与旧实现一致（±10 个百分点）。
//
// 换实现不该让"偏高/偏低"的判定口径悄悄变化 —— 否则界面上同类股票的
// 标签会成片翻转，而没人知道是为什么。
func TestImpliedVerdictThresholds(t *testing.T) {
	params := impliedParams()

	// 极高价必然 over
	over := EvaluateImpliedExpectation(0, 600, impliedTestShares, impliedTestFCF, 4,
		0.12, "基准", 70, 0.8, params)
	if !over.Available || over.Verdict != "over" {
		t.Errorf("极端高价应判为 over，实际 %q（隐含 %.1f%% vs 自身 12%%）",
			over.Verdict, over.ImpliedGrowth*100)
	}
	t.Logf("高价：隐含 %.1f%% vs 自身 12%% → %s", over.ImpliedGrowth*100, over.Verdict)

	// 把自身增速设得远高于隐含要求 → under
	under := EvaluateImpliedExpectation(0, 60, impliedTestShares, impliedTestFCF, 4,
		0.80, "基准", 70, 0.8, params)
	if !under.Available || under.Verdict != "under" {
		t.Errorf("自身增速远高于市场要求时应判为 under，实际 %q（隐含 %.1f%% vs 自身 80%%）",
			under.Verdict, under.ImpliedGrowth*100)
	}
	t.Logf("自身高增：隐含 %.1f%% vs 自身 80%% → %s", under.ImpliedGrowth*100, under.Verdict)

	// 二者接近 → fair（用反解出来的隐含增速当作"自身增速"，必然落入 fair 区间）
	anchor := EvaluateImpliedExpectation(0, 120, impliedTestShares, impliedTestFCF, 4,
		0.15, "基准", 70, 0.8, params)
	if !anchor.Available {
		t.Fatal("锚点应可用")
	}
	matched := EvaluateImpliedExpectation(0, 120, impliedTestShares, impliedTestFCF, 4,
		anchor.ImpliedGrowth, "基准", 70, 0.8, params)
	if matched.Verdict != "fair" {
		t.Errorf("自身增速等于隐含要求时应判为 fair，实际 %q（隐含 %.4f vs 自身 %.4f）",
			matched.Verdict, matched.ImpliedGrowth, anchor.ImpliedGrowth)
	}
	t.Logf("匹配：隐含 %.2f%% = 自身 %.2f%% → %s",
		matched.ImpliedGrowth*100, anchor.ImpliedGrowth*100, matched.Verdict)
}

// TestAchievableFallsBackToYoY 钉住"自身可实现增速"的代理口径与说明文案。
func TestAchievableFallsBackToYoY(t *testing.T) {
	params := impliedParams()

	got := EvaluateImpliedExpectation(0, 100, impliedTestShares, impliedTestFCF, 4,
		0, "最近一期营收同比", 70, 0.8, params)
	if !got.Available {
		t.Fatal("应可用")
	}
	if got.AchievableGrowth != 0 {
		t.Errorf("传入的 achievable 为 0 时应原样保留 0，实际 %.4f", got.AchievableGrowth)
	}
	if got.AchievableBasis == "" {
		t.Error("必须说明自身增速取自哪个口径")
	}
}

func fulfillmentRank(level string) int {
	switch level {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}
