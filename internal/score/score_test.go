package score

import (
	"math"
	"testing"
)

// mkBars 造一段升序日线。close 由 closes 给出，high/low/open 由 close 派生。
func mkBars(closes []float64) []Bar {
	bars := make([]Bar, len(closes))
	for i, c := range closes {
		bars[i] = Bar{
			Date:   int64(20260101 + i),
			Open:   c * 0.99,
			High:   c * 1.01,
			Low:    c * 0.98,
			Close:  c,
			Volume: 1000,
			Amount: 1e7,
		}
	}
	return bars
}

// ---------------------------------------------------------------------------
// rolling 语义（对齐 pandas 的 min_periods）
// ---------------------------------------------------------------------------

func TestRollingMeanMinPeriods(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5}

	// 数据够时取最近 period 个
	if got, ok := rollingMean(values, 3, 3); !ok || got != 4 {
		t.Errorf("MA3 = %v (ok=%v)，期望 4", got, ok)
	}
	// 数据不够但满足 min_periods 时用现有全部（pandas 行为）
	if got, ok := rollingMean(values, 10, 3); !ok || got != 3 {
		t.Errorf("数据不足但满足 min_periods 时应全用: %v (ok=%v)", got, ok)
	}
	// 不满足 min_periods → 无值
	if _, ok := rollingMean(values, 10, 8); ok {
		t.Error("不满足 min_periods 应返回 false")
	}
	if _, ok := rollingMean(nil, 5, 5); ok {
		t.Error("空输入应返回 false")
	}
}

func TestRollingExtreme(t *testing.T) {
	values := []float64{5, 3, 9, 1, 7}

	if got, ok := rollingMax(values, 3, 3); !ok || got != 9 {
		t.Errorf("近 3 最大 = %v，期望 9", got)
	}
	if got, ok := rollingMin(values, 3, 3); !ok || got != 1 {
		t.Errorf("近 3 最小 = %v，期望 1", got)
	}
	if _, ok := rollingMax(values, 5, 10); ok {
		t.Error("不满足 min_periods 应返回 false")
	}
}

func TestPctChange(t *testing.T) {
	values := []float64{10, 11, 12, 13}

	if got, ok := pctChange(values, 2); !ok || math.Abs(got-18.1818) > 0.001 {
		t.Errorf("2 期涨幅 = %v，期望约 18.18", got)
	}
	if _, ok := pctChange(values, 10); ok {
		t.Error("期数超过数据长度应返回 false")
	}
	// 基数为 0 不能除
	if _, ok := pctChange([]float64{0, 1}, 1); ok {
		t.Error("基数为 0 应返回 false")
	}
}

func TestRankPct(t *testing.T) {
	values := []float64{10, 20, 30, 40}

	// pandas rank(pct=True) 默认 average：最大的百分位是 1.0
	if got := RankPct(values, 40); got != 1 {
		t.Errorf("最大值百分位 = %v，期望 1", got)
	}
	if got := RankPct(values, 10); got != 0.25 {
		t.Errorf("最小值百分位 = %v，期望 0.25", got)
	}
	// 并列取平均名次
	ties := []float64{10, 10, 20, 20}
	if got := RankPct(ties, 10); math.Abs(got-0.375) > 0.001 {
		t.Errorf("并列百分位 = %v，期望 0.375", got)
	}
	if got := RankPct(nil, 1); got != 0 {
		t.Errorf("空输入应返回 0，实际 %v", got)
	}
}

// ---------------------------------------------------------------------------
// 指标
// ---------------------------------------------------------------------------

func TestComputeIndicators(t *testing.T) {
	// 单调上涨：均线多头排列、收益为正
	closes := make([]float64, 60)
	for i := range closes {
		closes[i] = 10 + float64(i)*0.1
	}
	ind, ok := ComputeIndicators(mkBars(closes))
	if !ok {
		t.Fatal("应能算出指标")
	}
	if ind.Close != closes[len(closes)-1] {
		t.Errorf("收盘价错误: %v", ind.Close)
	}
	if ind.Ma5 <= 0 || ind.Ma20 <= 0 || ind.Ma60 <= 0 {
		t.Errorf("均线应有值: ma5=%v ma20=%v ma60=%v", ind.Ma5, ind.Ma20, ind.Ma60)
	}
	// 单调上涨 → 短期均线在上方
	if ind.Ma5 <= ind.Ma20 {
		t.Errorf("单调上涨时 MA5 应大于 MA20: %v vs %v", ind.Ma5, ind.Ma20)
	}
	if ind.StockReturn20D <= 0 {
		t.Errorf("20 日收益应为正: %v", ind.StockReturn20D)
	}
	// 60 日数据不足以算 MA120/MA250 → 保持 0
	if ind.Ma120 != 0 || ind.Ma250 != 0 {
		t.Errorf("数据不足时均线应为 0: ma120=%v ma250=%v", ind.Ma120, ind.Ma250)
	}
}

func TestComputeIndicatorsNeedsTwoBars(t *testing.T) {
	if _, ok := ComputeIndicators(mkBars([]float64{10})); ok {
		t.Error("只有一根 K 线时不该算得出指标")
	}
	if _, ok := ComputeIndicators(nil); ok {
		t.Error("空输入不该算得出指标")
	}
}

func TestDrawdownAndRebound(t *testing.T) {
	// 先涨到 20 再跌回 15：应有回撤
	closes := make([]float64, 60)
	for i := range closes {
		closes[i] = 10 + float64(i)*0.2 // 涨到 21.8
	}
	closes[59] = 15 // 最后一天大跌
	ind, _ := ComputeIndicators(mkBars(closes))

	if ind.DrawdownFromHigh20 >= 0 {
		t.Errorf("从高点回落时回撤应为负: %v", ind.DrawdownFromHigh20)
	}
	if ind.ReboundFromLow60 < 0 || ind.ReboundFromLow60 > 1 {
		t.Errorf("反弹位置应在 0..1: %v", ind.ReboundFromLow60)
	}
	if ind.RangePosition120 < 0 || ind.RangePosition120 > 1 {
		t.Errorf("区间位置应被裁剪到 0..1: %v", ind.RangePosition120)
	}
}

// PrevHigh60 必须是**前一日**为止的最高价（shift(1)），否则突破判定会失真。
//
// 注意 min_periods=20：数据不足 20 根时返回 0（原实现如此）。
func TestPrevHigh60ExcludesToday(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 10
	}
	bars := mkBars(closes)
	// 造一个今日新高
	bars[len(bars)-1].High = 100

	prev := PrevHigh60(bars)
	if prev >= 100 {
		t.Errorf("prev_high60 不该包含今日最高价: %v", prev)
	}
	if prev != bars[len(bars)-2].High {
		t.Errorf("prev_high60 = %v，期望昨日最高 %v", prev, bars[len(bars)-2].High)
	}

	// 数据不足 min_periods(20) 时返回 0
	if got := PrevHigh60(mkBars([]float64{10, 11, 12})); got != 0 {
		t.Errorf("数据不足时应返回 0，实际 %v", got)
	}
}

// ---------------------------------------------------------------------------
// 基础分
// ---------------------------------------------------------------------------

func TestTrendBase(t *testing.T) {
	// 完美多头排列
	perfect := Indicators{Close: 15, Ma20: 14, Ma60: 12, Ma120: 11, Ma250: 10}
	if got := TrendBase(perfect, 13, 11.5); got != 30 {
		t.Errorf("完美多头排列应为满分 30，实际 %v", got)
	}
	// 空头排列
	weak := Indicators{Close: 10, Ma20: 11, Ma60: 12, Ma120: 13, Ma250: 14}
	if got := TrendBase(weak, 12, 13); got != 0 {
		t.Errorf("空头排列应为 0，实际 %v", got)
	}
}

func TestStructureBaseRange(t *testing.T) {
	// 位置在舒服区间、无突破缺口、量能温和有回撤
	good := Indicators{
		Close: 10, Ma20: 9.5, RangePosition120: 0.5,
		VolumeRatio20: 1.0, DrawdownFromHigh120: -8,
	}
	score := StructureBase(good, 0, 0)
	// 位置 8 + 量能配合 4 = 12
	if score != 12 {
		t.Errorf("结构分 = %v，期望 12", score)
	}

	// 位置在边缘区间（0.2..0.35 或 0.75..0.9）得 4 分
	edge := Indicators{RangePosition120: 0.85}
	if got := StructureBase(edge, 0, 0); got != 4 {
		t.Errorf("边缘位置应得 4 分，实际 %v", got)
	}
	// 位置很差（<0.2 或 >0.9）只得 1 分
	bad := Indicators{RangePosition120: 0.05}
	if got := StructureBase(bad, 0, 0); got != 1 {
		t.Errorf("极差位置应得 1 分，实际 %v", got)
	}
	if got := StructureBase(Indicators{RangePosition120: 0.95}, 0, 0); got != 1 {
		t.Errorf("位置 >0.9 应得 1 分，实际 %v", got)
	}

	// 突破缺口分档：-2.5..3 → 6 分；3..8 → 3 分；>8 → 0 分
	inside := Indicators{Close: 100, RangePosition120: 0.05}
	if got := StructureBase(inside, 100, 0); got != 1+6 {
		t.Errorf("缺口在舒适区应加 6 分，实际 %v", got)
	}
	if got := StructureBase(inside, 95, 0); got != 1+3 {
		t.Errorf("缺口 5.3%% 应加 3 分，实际 %v", got)
	}
	if got := StructureBase(inside, 80, 0); got != 1 {
		t.Errorf("缺口过大不该加分，实际 %v", got)
	}
	// 胜率贡献
	if got := StructureBase(Indicators{RangePosition120: 0.05}, 0, 100); got != 3 {
		t.Errorf("胜率 100%% 应加 2 分，实际 %v", got)
	}
}

func TestRSBaseWithOverheatCooldown(t *testing.T) {
	// 公式含**两项**：rs20 项（14 分）+ 行业超额项（6 分）。
	// 行业收益与基准都为 0 时，第二项取中位 0.5*6 = 3 分。
	// rs20 = 0 → (0+10)/20 = 0.5 → 0.5*14 = 7；合计 10
	base := Indicators{RelativeStrength20D: 0}
	if got := RSBase(base); got != 10 {
		t.Errorf("中性 RS 应得 7+3=10 分，实际 %v", got)
	}
	// rs20 = 10 → 满分 14；合计 17
	if got := RSBase(Indicators{RelativeStrength20D: 10}); got != 17 {
		t.Errorf("RS=10 应得 14+3=17 分，实际 %v", got)
	}
	// 过热冷却：rs20>=35 且 dd120>=-6 → 减 1
	hot := Indicators{RelativeStrength20D: 35, DrawdownFromHigh120: -6}
	plain := Indicators{RelativeStrength20D: 35, DrawdownFromHigh120: -20}
	if got := RSBase(hot); got != RSBase(plain)-1 {
		t.Errorf("过热应减 1 分: hot=%v plain=%v", got, plain)
	}
	// rs20>=50 且 dd120>=-3 → **两档同时命中**，共减 3 分
	// （-1 来自 rs20>=35 且 dd120>=-6，-2 来自 rs20>=50 且 dd120>=-3）
	veryHot := Indicators{RelativeStrength20D: 50, DrawdownFromHigh120: -3}
	sameRS := Indicators{RelativeStrength20D: 50, DrawdownFromHigh120: -20}
	if got := RSBase(veryHot); got != RSBase(sameRS)-3 {
		t.Errorf("两档过热共应减 3 分: veryHot=%v sameRS=%v", got, RSBase(sameRS))
	}
}

func TestCapitalBase(t *testing.T) {
	// 全部满分：7+4+2+2 = 15
	full := Indicators{AmountRankPct: 1, TurnRankPct: 1, VolumeRatio20: 2, IndustryStrengthPct: 1}
	if got := CapitalBase(full); got != 15 {
		t.Errorf("资金分满分应为 15，实际 %v", got)
	}
	// 全 0：vr20=0 → clip((0-0.8)/1.2) = 0
	if got := CapitalBase(Indicators{}); got != 0 {
		t.Errorf("资金分应为 0，实际 %v", got)
	}
}

func TestNearTermRepairScore(t *testing.T) {
	// rebound=1 → 6；dd120=0 → (20+0)/20=1 → 4；close>ma20 → 2；close>ma60 → 1.5
	// prevHigh60=0 时最后一项算不了
	full := Indicators{Close: 12, Ma20: 11, Ma60: 10, ReboundFromLow60: 1, DrawdownFromHigh120: 0}
	if got := NearTermRepairScore(full, 0); got != 13.5 {
		t.Errorf("近期修复分 = %v，期望 13.5", got)
	}
	// **零值也有 4 分**：dd120=0 时 clip((20+0)/20,0,1)=1。
	// 这符合公式，不是 bug —— 一只正在最高点的股票本来就"离修复目标最近"。
	if got := NearTermRepairScore(Indicators{}, 0); got != 4 {
		t.Errorf("全空时 dd120=0 应贡献 4 分，实际 %v", got)
	}
	// 深度回撤时该项归零
	deep := Indicators{DrawdownFromHigh120: -40}
	if got := NearTermRepairScore(deep, 0); got != 0 {
		t.Errorf("深度回撤时修复分应为 0，实际 %v", got)
	}
	// 突破前高配合分：clip((close/prevHigh60 - 0.95)/0.08, 0, 1) * 1.5
	// 要拿满 1.5 分需要 close >= prevHigh60 * 1.03。
	// 注意均线要给非零值，否则会叠上 "close > ma20/ma60" 的 2 + 1.5 分
	near := Indicators{Close: 103, Ma20: 200, Ma60: 200, ReboundFromLow60: 0, DrawdownFromHigh120: -40}
	if got := NearTermRepairScore(near, 100); got != 1.5 {
		t.Errorf("站上前高 3%% 时配合分应满 1.5，实际 %v", got)
	}
	// 正好在前高处（1.0 倍）只得 0.625 * 1.5 = 0.9375
	atHigh := Indicators{Close: 100, Ma20: 200, Ma60: 200, ReboundFromLow60: 0, DrawdownFromHigh120: -40}
	if got := NearTermRepairScore(atHigh, 100); math.Abs(got-0.94) > 0.001 {
		t.Errorf("刚好在前高时应得 0.94（分项按原实现 round(2)），实际 %v", got)
	}
}

// 均线缺失（值为 0）时**不该**跳过加分 —— 原实现 fillna(0) 后直接比较，
// `close > 0` 成立就加分。加了保护会让次新股总分低 10~20 分。
func TestBaseScoresHandleZeroMovingAverages(t *testing.T) {
	// 上市不久：只有 ma20，ma60/ma120/ma250 都还是 0
	young := Indicators{Close: 12, Ma20: 11}

	// close>ma20 (8) + ma20>ma60=0 (8) + ma20>lag5=0 (2) = 18
	// （lag 项在 lag 传 0 时也成立 —— 这正是原实现 fillna(0) 后的行为）
	if got := TrendBase(young, 0, 0); got != 18 {
		t.Errorf("均线为 0 时应照常加分：trend = %v，期望 18", got)
	}
	// dd120=0 → 4；close>ma20 → 2；close>ma60=0 → 1.5 = 7.5
	if got := NearTermRepairScore(young, 0); got != 7.5 {
		t.Errorf("均线为 0 时应照常加分：near = %v，期望 7.5", got)
	}
	// 结构分：位置 fallback 1 + 均线量能块 4（close>=ma20 且 vr20<=1.15 且 dd120 在 -18..-3）
	structure := StructureBase(Indicators{Close: 12, Ma20: 11, DrawdownFromHigh120: -8}, 0, 0)
	if structure != 1+4 {
		t.Errorf("均线为 0 时结构分应含量能块：%v，期望 5", structure)
	}

	// 对照：均线齐全且空头排列 → 只有 close>ma20 (8) + 两个 lag 项 (2+2) = 12
	mature := Indicators{Close: 12, Ma20: 11, Ma60: 12, Ma120: 13, Ma250: 14}
	if got := TrendBase(mature, 0, 0); got != 12 {
		t.Errorf("空头排列时 trend 应为 8+2+2=12，实际 %v", got)
	}
}

func TestTotalScoreWeights(t *testing.T) {
	// 五项**直接相加**，上限 30+20+20+15+15 = 100。
	// （曾误实现成加权平均 ×0.30/×0.20…，总分上限只有 21.5，
	//  而参考实现的建仓线是 66 —— 阈值永远够不到。）
	if got := TotalScore(30, 20, 20, 15, 15, 0); got != 100 {
		t.Errorf("五项满分应得 100，实际 %v", got)
	}
	// 各分项上限之和必须等于 100，否则阈值体系就是错的
	if sum := maxTrend + maxStructure + maxRS + maxCapital + maxRepair; sum != 100 {
		t.Errorf("分项上限之和应为 100，实际 %v", sum)
	}
	// 中等水平的股票应该落在 60 上下（建仓线附近），而不是个位数
	mid := TotalScore(18, 12, 12, 9, 9, 0)
	if mid != 60 {
		t.Errorf("中等水平应为 60，实际 %v", mid)
	}
	// 调整分可以为负
	if got := TotalScore(18, 12, 12, 9, 9, -9); got != 51 {
		t.Errorf("负调整分应生效: %v", got)
	}
	// 总分封顶 100
	if got := TotalScore(1000, 1000, 1000, 1000, 1000, 0); got != 100 {
		t.Errorf("总分应封顶 100，实际 %v", got)
	}
	// 总分不低于 0
	if got := TotalScore(0, 0, 0, 0, 0, -50); got != 0 {
		t.Errorf("总分应不低于 0，实际 %v", got)
	}
}

// ---------------------------------------------------------------------------
// 修复能力（含防未来函数）
// ---------------------------------------------------------------------------

// 最近 41 天内发生的事件**不能**计入 —— 这是原实现防未来函数的做法。
func TestRepairFeaturesKnownLag(t *testing.T) {
	// 造一段：先跌到触发事件，再在窗口内修复
	closes := make([]float64, 200)
	for i := range closes {
		closes[i] = 100
	}
	// 在第 50 天崩到 80（相对 120 日高点回撤 -20%，触发事件）
	for i := 50; i < 60; i++ {
		closes[i] = 80
	}
	// 之后修复到 100
	for i := 60; i < 200; i++ {
		closes[i] = 100
	}

	got := BuildRepairFeatures(mkBars(closes))
	if got.EventCount == 0 {
		t.Fatal("应该识别出修复事件")
	}
	if got.RecoveryProbability <= 0 {
		t.Errorf("跌到 80 后修复到 100 应记为 recovered: %+v", got)
	}
	if got.AbilityScore <= 0 || got.AbilityScore > 15 {
		t.Errorf("修复能力分应在 (0,15]: %v", got.AbilityScore)
	}
}

// 数据太短（<= 41 根）时全为 0 —— 原实现如此。
func TestRepairFeaturesTooShort(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 100
	}
	got := BuildRepairFeatures(mkBars(closes))
	if got.EventCount != 0 || got.AbilityScore != 0 {
		t.Errorf("数据不足时修复能力应全为 0: %+v", got)
	}
}

// 没有回撤就没有事件。
func TestRepairFeaturesNoDrawdown(t *testing.T) {
	closes := make([]float64, 200)
	for i := range closes {
		closes[i] = 100 + float64(i)*0.1
	}
	got := BuildRepairFeatures(mkBars(closes))
	if got.EventCount != 0 {
		t.Errorf("单调上涨不该有修复事件: %+v", got)
	}
}

// 修复能力公式：clip(prob*8 + speed*4 + newhigh*3, 0, 15)，prob 用 0..1 原始比例。
func TestRepairAbilityFormula(t *testing.T) {
	closes := make([]float64, 200)
	for i := range closes {
		closes[i] = 100
	}
	for i := 50; i < 60; i++ {
		closes[i] = 70 // 深跌，触发事件
	}
	for i := 60; i < 200; i++ {
		closes[i] = 100 // 完全修复
	}
	got := BuildRepairFeatures(mkBars(closes))

	// 全部事件都 recovered + new_high（因为 100 相对 120 日高点不新高，
	// 但相对事件日的 high120 是修复了）→ 手工核对公式
	speed := clamp((float64(RepairWindow)-got.AvgRecoveryDays)/32, 0, 1)
	want := clamp(got.RecoveryProbability*8+speed*4+got.NewHighProbability*3, 0, 15)
	if math.Abs(got.AbilityScore-want) > 0.001 {
		t.Errorf("修复能力分 = %v，按公式应为 %v（prob=%v speed=%v newhigh=%v）",
			got.AbilityScore, want, got.RecoveryProbability, speed, got.NewHighProbability)
	}
}

// ---------------------------------------------------------------------------
// 市场状态
// ---------------------------------------------------------------------------

func mkMarketDays(n int, avgPct float64, upRatio float64, amount float64) []MarketDay {
	days := make([]MarketDay, n)
	for i := range days {
		days[i] = MarketDay{
			Date: int64(20260101 + i), AvgPct: avgPct,
			UpRatio: upRatio, DownRatio: 1 - upRatio, TotalAmount: amount,
		}
	}
	return days
}

func TestMarketStateTooShort(t *testing.T) {
	got := BuildMarketState(mkMarketDays(10, 1.0, 0.8, 1e12))
	if got.State != StateRange {
		t.Errorf("不足 20 天应判为 range: %s", got.State)
	}
	if got.Label != StateLabels[StateRange] {
		t.Errorf("标签错误: %s", got.Label)
	}
}

func TestMarketStateBull(t *testing.T) {
	// 每日上涨 1%、八成股票上涨、成交额放大 → 牛市
	days := mkMarketDays(80, 1.0, 0.8, 1e12)
	// 让成交额递增，产生增速
	for i := range days {
		days[i].TotalAmount = 1e12 * (1 + float64(i)*0.02)
	}
	got := BuildMarketState(days)
	if got.State != StateBull {
		t.Errorf("持续上涨应判为牛市，实际 %s（bull=%v bear=%v）", got.State, got.BullScore, got.BearScore)
	}
	if got.Label != "牛市/趋势扩张" {
		t.Errorf("标签错误: %s", got.Label)
	}
	if got.ProxyRet20 <= 0 {
		t.Errorf("上涨时代理指数 20 日收益应为正: %v", got.ProxyRet20)
	}
}

func TestMarketStateBear(t *testing.T) {
	days := mkMarketDays(80, -1.0, 0.2, 1e12)
	for i := range days {
		days[i].DownRatio = 0.8
	}
	got := BuildMarketState(days)
	if got.State != StateBear {
		t.Errorf("持续下跌应判为熊市，实际 %s（bull=%v bear=%v）", got.State, got.BullScore, got.BearScore)
	}
	if got.Label != "熊市/风险释放" {
		t.Errorf("标签错误: %s", got.Label)
	}
}

func TestMarketStateSideways(t *testing.T) {
	// 涨跌交替、占比中性
	days := make([]MarketDay, 80)
	for i := range days {
		pct := 0.2
		if i%2 == 1 {
			pct = -0.2
		}
		days[i] = MarketDay{Date: int64(20260101 + i), AvgPct: pct, UpRatio: 0.5, DownRatio: 0.5, TotalAmount: 1e12}
	}
	got := BuildMarketState(days)
	if got.State != StateRange {
		t.Errorf("横盘应判为震荡，实际 %s（bull=%v bear=%v）", got.State, got.BullScore, got.BearScore)
	}
}

// ---------------------------------------------------------------------------
// 策略配置
// ---------------------------------------------------------------------------

func TestBuildStrategyConfigReadyScores(t *testing.T) {
	cases := []struct {
		strategy, state string
		want            float64
	}{
		{StrategyTrendGrowth, StateBull, 66},
		{StrategyTrendGrowth, StateRange, 68},
		{StrategyTrendLowBuy, StateBear, 68},
		{StrategyOversold, StateBear, 74},
		{StrategyExpert, StateBull, 62},
		{StrategyExpert, StateRange, 64},
	}
	for _, c := range cases {
		cfg := BuildStrategyConfig(c.strategy, c.state, 5, 10, 3, "")
		if cfg.ReadyScore != c.want {
			t.Errorf("%s/%s 的 ready_score = %v，期望 %v", c.strategy, c.state, cfg.ReadyScore, c.want)
		}
	}
}

func TestBuildStrategyConfigCandidateFloor(t *testing.T) {
	// 低吸的候选下限是 0（靠自己的结构条件筛，不靠总分）
	if got := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "").CandidateScoreFloor; got != 0 {
		t.Errorf("低吸候选下限应为 0，实际 %v", got)
	}
	if got := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 5, 10, 3, "").CandidateScoreFloor; got != 60 {
		t.Errorf("趋势成长候选下限应为 60，实际 %v", got)
	}
	if got := BuildStrategyConfig(StrategyOversold, StateRange, 5, 10, 3, "").CandidateScoreFloor; got != 68 {
		t.Errorf("超跌候选下限应为 68，实际 %v", got)
	}
}

// 低吸的单股最大层数 = total_layers（其余策略默认 3）。
func TestBuildStrategyConfigMaxLayers(t *testing.T) {
	if got := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "").MaxLayersPerStock; got != 10 {
		t.Errorf("低吸单股最大层数应等于总层数 10，实际 %d", got)
	}
	if got := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 5, 10, 3, "").MaxLayersPerStock; got != 3 {
		t.Errorf("趋势成长单股最大层数应为 3，实际 %d", got)
	}
}

// 熊市里 trend_growth / trend_low_buy 的 allow_new_adds 被**强制覆盖成 true**（原项目如此）。
func TestBuildStrategyConfigAllowNewAdds(t *testing.T) {
	for _, strategy := range []string{StrategyTrendGrowth, StrategyTrendLowBuy} {
		if !BuildStrategyConfig(strategy, StateBear, 5, 10, 3, "").AllowNewAdds {
			t.Errorf("%s 在熊市里 allow_new_adds 应为 true（原项目强制覆盖）", strategy)
		}
	}
	for _, strategy := range []string{StrategyOversold, StrategyExpert} {
		if BuildStrategyConfig(strategy, StateBear, 5, 10, 3, "").AllowNewAdds {
			t.Errorf("%s 在熊市里 allow_new_adds 应为 false", strategy)
		}
		if !BuildStrategyConfig(strategy, StateRange, 5, 10, 3, "").AllowNewAdds {
			t.Errorf("%s 在震荡市里 allow_new_adds 应为 true", strategy)
		}
	}
}

func TestMaxReplaceCount(t *testing.T) {
	// max(1, min(3, target//2))，target<4 时固定 1
	cases := map[int]int{1: 1, 3: 1, 4: 2, 6: 3, 8: 3, 10: 3, 20: 3}
	for target, want := range cases {
		if got := maxReplaceCount(target); got != want {
			t.Errorf("target=%d 的 max_replace_count = %d，期望 %d", target, got, want)
		}
	}
}

// 低吸按 market_scope 选档；默认 main_gem。
func TestLowBuyProfiles(t *testing.T) {
	def := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "")
	if def.LowBuyScopeKey != "main_gem" {
		t.Errorf("默认档应为 main_gem，实际 %s", def.LowBuyScopeKey)
	}
	if def.LowBuyExitMa30Ratio != 0.96 {
		t.Errorf("main_gem 的 MA30 退出比例应为 0.96，实际 %v", def.LowBuyExitMa30Ratio)
	}
	// gem_star 额外开利润保护
	gemStar := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "gem_star")
	if gemStar.LowBuyProfitProtectStartPct != 30 || gemStar.LowBuyProfitProtectRetracePct != 10 {
		t.Errorf("gem_star 应开启 30/10 利润保护: %+v", gemStar)
	}
	// main 档的 MA30 是 0.94
	main := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "main")
	if main.LowBuyExitMa30Ratio != 0.94 {
		t.Errorf("main 档的 MA30 退出比例应为 0.94，实际 %v", main.LowBuyExitMa30Ratio)
	}
	// 未知 scope 回退默认档
	unknown := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 3, "不存在")
	if unknown.LowBuyScopeKey != "main_gem" {
		t.Errorf("未知 scope 应回退 main_gem，实际 %s", unknown.LowBuyScopeKey)
	}
}

func TestBuildStrategyConfigFallbacks(t *testing.T) {
	// 未知策略 → 趋势成长
	cfg := BuildStrategyConfig("不存在", StateRange, 5, 10, 3, "")
	if cfg.StrategyID != StrategyTrendGrowth {
		t.Errorf("未知策略应回退趋势成长: %s", cfg.StrategyID)
	}
	// 未知市场状态 → 震荡
	cfg = BuildStrategyConfig(StrategyTrendGrowth, "不存在", 5, 10, 3, "")
	if cfg.MarketState != StateRange {
		t.Errorf("未知市场状态应回退震荡: %s", cfg.MarketState)
	}
	// 非法数值 → **1**（原实现是 `max(1, int(x or 1))`，不是回退到 5/10）
	cfg = BuildStrategyConfig(StrategyTrendGrowth, StateRange, 0, 0, 0, "")
	if cfg.TargetCount != 1 || cfg.TotalLayers != 1 || cfg.MaxLayersPerStock != 3 {
		t.Errorf("非法数值应归到 1（单股层数除外）: %+v", cfg)
	}
	// 空 scope → "all"（原实现 `resolve_strategy_market_scope_key("")`）
	if cfg.MarketScopeKey != "all" {
		t.Errorf("空 scope 应为 all，实际 %s", cfg.MarketScopeKey)
	}
}

// 兜底轮动策略的阈值与 StrategyConfig **不一样**（原项目如此，如实保留）。
func TestBuildRotationPolicy(t *testing.T) {
	bull := BuildRotationPolicy(StateBull, 8)
	if bull.ReadyScore != 76 || bull.ReplaceScoreGap != 18 || bull.ClearScore != 45 {
		t.Errorf("牛市阈值错误: %+v", bull)
	}
	if bull.MaxReplaceCount != 2 || bull.DesiredPosition != 8 {
		t.Errorf("牛市目标仓位应为原始目标: %+v", bull)
	}
	bear := BuildRotationPolicy(StateBear, 8)
	if bear.ReadyScore != 78 || bear.ClearScore != 52 {
		t.Errorf("熊市阈值错误: %+v", bear)
	}
	// 熊市目标仓位砍半（向上取整）
	if bear.DesiredPosition != 4 {
		t.Errorf("熊市目标仓位应为 (8+1)/2=4，实际 %d", bear.DesiredPosition)
	}
	// 与 StrategyConfig 的差异（这就是为什么两者不能合并）
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateBear, 8, 10, 3, "")
	if cfg.ReadyScore == bear.ReadyScore {
		t.Error("兜底策略与策略配置的 ready_score 本该不同，原项目就是两套")
	}
}

func TestLowBuyPositionLayers(t *testing.T) {
	cases := []struct {
		mode  string
		count int
		want  int
	}{
		{"slow1000", 500, 0},
		{"slow1000", 1000, 1},
		{"slow1000", 1500, 2},
		{"slow1000", 2000, 3},
		{"slow1000", 2600, 5},
		{"slow1000", 9999, 5},
		{"conservative1200", 1200, 1},
		{"conservative1200", 1800, 2},
		{"conservative1200", 2400, 3},
		{"conservative1200", 3200, 5},
	}
	for _, c := range cases {
		if got := LowBuyPositionLayers(c.mode, c.count); got != c.want {
			t.Errorf("%s/%d → %d 层，期望 %d", c.mode, c.count, got, c.want)
		}
	}
}

func TestClampAndRound(t *testing.T) {
	if got := clamp(5, 0, 3); got != 3 {
		t.Errorf("clamp 上限错误: %v", got)
	}
	if got := clamp(-5, 0, 3); got != 0 {
		t.Errorf("clamp 下限错误: %v", got)
	}
	// NaN / Inf 要归到下限，不能污染后续计算
	if got := clamp(math.NaN(), 1, 3); got != 1 {
		t.Errorf("NaN 应归到下限: %v", got)
	}
	if got := clamp(math.Inf(1), 1, 3); got != 3 {
		t.Errorf("Inf 应归到上限: %v", got)
	}
	if got := round(1.23456, 4); got != 1.2346 {
		t.Errorf("round 错误: %v", got)
	}
	if got := round(math.NaN(), 2); got != 0 {
		t.Errorf("NaN 舍入应为 0: %v", got)
	}
}
