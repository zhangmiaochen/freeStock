package valuation

import (
	"fmt"
)

// ---------------------------------------------------------------------------
// 市场隐含预期（反解"当前价格在要求什么"）
//
// ## 为什么需要它，以及它与原实现的关系
//
// 原 `impliedGrowth` 是**循环论证**：
//
//	impliedScore := ((currentPE-8)/22*100 - 0.45*quality) / 0.55
//	implied     := (impliedScore - 50) / 50 * 0.30
//
// 它把 `fairPE`（成长+质量 → 合理 PE）反解回去，于是"高 PE"**必然**推出
// "高隐含成长"。这不是在问市场要求多少增长，而是把 fairPE 的假设换个说法
// 讲一遍 —— 提供不了安全边际之外的任何新信息。
//
// 本文件用**真正的反向 DCF** 替换它：复用正向 DCF 的同一个两阶段模型
// （`dcfValue`：5 年显式 + 永续终值），二分求出"让模型价值等于当前市值的增速"。
// 与正向同源，所以两者的差就是**定价分歧**本身。
//
// ## 为什么"持续时间"必须单独算
//
// 单点增速不够用：市场要求 25% 的增速 —— 要求**几年**？
// 3 年还是 10 年，风险差别是数量级的。PEG 类指标的经典缺陷正是"忽略持续时间"。
// 所以这里另外算 `ImpliedYears`：在 g* 的路径上，累计折现现金流
// 何时才能覆盖当前市值（按线性插值给出小数年）。
//
// ## 为什么还要"兑现概率"与"落空后的跌幅"
//
// 知道"市场要求 30%"还不够，必须回答"它做得到吗"和"做不到会跌多少"
// —— 这两个才是能接进风险约束的东西。
// ---------------------------------------------------------------------------

// ImpliedDurationCap 是"隐含持续时间"的上限（年）。
//
// 超过它就统一报这个值并注明"≥N 年"：再往上算没有意义
// （20 年和 30 年的高增长假设在现实中同样不可信），而且能让界面文案收敛。
const ImpliedDurationCap = 20

// ImpliedExpectation 是"当前价格隐含的市场预期"及其兑现评估。
type ImpliedExpectation struct {
	// Available 表示能可靠反解（需要正的 TTM 自由现金流与完整现金流科目）。
	Available bool `json:"available"`
	// Reason 在不可用时说明原因（不硬算）。
	Reason string `json:"reason,omitempty"`

	// ImpliedGrowth 是反解出的**市场要求增速**（小数，0.25 = 25%）。
	//
	// ⚠ ImpliedGrowth **只在 `Unreachable` 为 false 时才可信**：
	// 为 true 时它被夹在搜索区间端点，真实要求只在那个方向之外。
	ImpliedGrowth float64 `json:"impliedGrowth"`
	// Unreachable 表示"当前市值超出搜索区间的可解释范围"。
	//
	// 只有上端会这样（价格高于 80% 增速所能支撑的价值）——
	// 下端（价格过低）直接判不可用并说明，因为那时 DCF 框架本身不适用。
	Unreachable bool `json:"unreachable"`
	// ImpliedYears 是现值口径下，市场要求的成长需要持续的**年数**。
	//
	// 含义：在 ImpliedGrowth 这条路径上，累计折现现金流覆盖当前市值所需年数。
	// 0 表示"当前现金流的现值就已经覆盖市值"（不需要成长）。
	ImpliedYears float64 `json:"impliedYears"`
	// DurationCapped 表示 ImpliedYears 撞到了 ImpliedDurationCap（真实需求更久）。
	DurationCapped bool `json:"durationCapped"`

	// AchievableGrowth 是"公司自身能做到的增速"（历史增速代理，小数）。
	AchievableGrowth float64 `json:"achievableGrowth"`
	// AchievableBasis 说明 AchievableGrowth 取自哪个口径。
	AchievableBasis string `json:"achievableBasis,omitempty"`
	// GrowthGap 是 ImpliedGrowth − AchievableGrowth（正数=市场要求高于自身能力）。
	GrowthGap float64 `json:"growthGap"`

	// Fulfillment 是兑现概率档位（high/medium/low/unknown）。
	Fulfillment string `json:"fulfillment"`
	// FulfillmentNote 说明这个档位是依据哪几个信号判的。
	FulfillmentNote string `json:"fulfillmentNote,omitempty"`

	// ValueAtAchievable 是"成长只按自身实际增速实现"时的每股合理价值。
	ValueAtAchievable float64 `json:"valueAtAchievable"`
	// DownsideIfMiss 是不及预期时相对现价的跌幅（负数表示下跌）。
	DownsideIfMiss float64 `json:"downsideIfMiss"`

	// Verdict 是结论档位：over/under/fair/unknown。
	Verdict string `json:"verdict"`
	// Note 是给人看的结论与口径说明。
	Note string `json:"note,omitempty"`
}

// 反解的搜索区间。
//
// ⚠ 下限取 −50% 而不是 −30%：现金流折现是**严格单调**的，如果目标市值
// （= 股价 × 股本）比"最低增速下的模型价值"还低，说明**在这个模型的假设下
// 无解** —— 而不是"市场要求 −30% 增长"。早期版本把结果夹到下限返回，
// 于是"代回正向模型"对不上当前市值（实测相差 575%），自洽性被破坏。
//
// 现在的做法是：区间内无解就如实标记 `Unreachable`（价格低于模型下限，
// 通常意味着市场在定价"现金流会消失"，DCF 框架不适用），不再返回假数字。
const (
	impliedGrowthFloor = -0.50
	impliedGrowthCeil  = 0.80
	impliedBisectSteps = 60
)

// EvaluateImpliedExpectation 反解当前价格隐含的市场预期。
//
// 入参是**已经在用的一组量**，刻意不接收 `FairValueInput`：
// 这样它是纯函数、可单测，也不会把 fairvalue.go 的耦合面扩大。
//
//   - fairValuePerShare：正向模型算出的**基准合理价值**（每股），用于交叉校验与文案；
//   - price：当前价（不复权，与市值同口径）；
//   - shares：总股本；
//   - fcf：TTM 自由现金流（元）；
//   - cashQuarters：现金流科目完整度（最少的那个季度数），<4 视为不完整；
//   - achievable：公司自身可实现增速（历史增速代理）；
//   - achievableBasis：该增速来自哪个口径（"近 3 年营收 CAGR"等），用于文案；
//   - qualityScore / growthPersist：兑现概率要用的质量分与成长持续性。
func EvaluateImpliedExpectation(
	fairValuePerShare, price, shares, fcf float64,
	cashQuarters int,
	achievable float64, achievableBasis string,
	qualityScore, growthPersist float64,
	params Params,
) ImpliedExpectation {
	out := ImpliedExpectation{
		AchievableGrowth: achievable,
		AchievableBasis:  achievableBasis,
		Fulfillment:      "unknown",
		Verdict:          "unknown",
	}

	if price <= 0 || shares <= 0 {
		out.Reason = "缺少价格或股本，无法反解市场隐含预期"
		return out
	}
	// 与正向 DCF 同门槛：没有正的经营现金流就做不了现金流折现反解。
	// 这时**不退回 fairPE 反解**（那正是本文件要替换掉的循环论证），
	// 而是如实说明不可用，由调用方降置信度。
	if fcf <= 0 {
		out.Reason = fmt.Sprintf("TTM 自由现金流 %.2f 亿 ≤ 0，无法用现金流折现反解市场隐含预期"+
			"（亏损或重资产扩张期的公司不适用该口径）", fcf/1e8)
		return out
	}
	if cashQuarters < 4 {
		out.Reason = fmt.Sprintf("现金流科目不完整（只有 %d 个季度、需 4 个），"+
			"反解隐含预期不可靠", cashQuarters)
		return out
	}

	wacc := params.WACC
	terminal := params.TerminalGrowth
	if wacc-terminal < 0.02 {
		out.Reason = "WACC 与稳态增长的差不足 2 个百分点，永续增长模型的终值会发散，反解不可靠"
		return out
	}

	targetValue := price * shares

	// ① 二分反解 g*：dcfValue 对 growth 单调递增，所以二分是稳的。
	low, high := impliedGrowthFloor, impliedGrowthCeil
	valueLow := dcfValue(fcf, low, wacc, terminal)
	valueHigh := dcfValue(fcf, high, wacc, terminal)
	switch {
	case targetValue <= valueLow:
		// 比"最低增速下的模型价值"还低 → 在这个模型假设下无解。
		// 常见于极低价（市场在定价"现金流会消失"）或 FCF 相对市值过大。
		// 如实说明，不返回夹住的假数字。
		out.Reason = fmt.Sprintf(
			"当前市值 %.2f 亿低于「最低增速（%.0f%%）下的现金流折现价值 %.2f 亿」——"+
				"市场定价隐含「未来现金流会大幅萎缩」，DCF 框架在此不适用，"+
				"无法用增速反解表达该预期",
			targetValue/1e8, low*100, valueLow/1e8)
		return out
	case targetValue >= valueHigh:
		// 比"最高增速下的模型价值"还高 → 在 80% 增速上限内无解。
		// 记上限并标记：真实要求只会更高。
		out.ImpliedGrowth = high
		out.Unreachable = true
	default:
		for i := 0; i < impliedBisectSteps; i++ {
			mid := (low + high) / 2
			if dcfValue(fcf, mid, wacc, terminal) < targetValue {
				low = mid
			} else {
				high = mid
			}
		}
		out.ImpliedGrowth = (low + high) / 2
	}

	// ② 隐含持续时间：**增长溢价需要多少年才能靠自身现金流填平**。
	//
	// ⚠ 这里换过一次定义，值得记下来。第一版算的是"累计折现现金流何时覆盖市值"，
	// 实测**没有区分力**：在 9% 的折现率下，任何正的永续现金流序列的现值都会收敛到
	// 大约"FCF 的 3~4 倍"，而高估值股票的市值本来就是 FCF 的几十倍 ——
	// 于是不管增速要求是 0% 还是 28%，答案都是"≥20 年"（撞上限）。
	// 一个所有输入都给同一个输出的指标是**没有用的指标**。
	//
	// 现在的定义是两边作差：
	//
	//	增长溢价 = 当前市值 − 按"公司自身可实现增速"折现的价值
	//	隐含持续时间 = 增长溢价 ÷ 当前 1 年自由现金流
	//
	// 含义清楚得多："当前价格里，有相当于 N 年自由现金流的钱，
	// 是在为**超出自身能力的增长**付账"。0% 增速要求时它接近 0，
	// 要求 30% 而自身只能做到 10% 时它会很大 —— 有区分力。
	//
	// 它同时呼应了"PEG 忽略持续时间"这个批评：这个量就是"要求的成长要持续几年"
	// 的**价格口径**表达。
	baseValue := dcfValue(fcf, achievable, wacc, terminal)
	premium := targetValue - baseValue
	impliedYears := premium / fcf
	if impliedYears < 0 {
		// 市场给的价低于"按自身能力"的价值 → 不需要任何超出能力的成长
		impliedYears = 0
	}
	capped := false
	if impliedYears >= ImpliedDurationCap {
		impliedYears = ImpliedDurationCap
		capped = true
	}
	out.ImpliedYears = impliedYears
	out.DurationCapped = capped

	out.GrowthGap = out.ImpliedGrowth - achievable

	// ③ 兑现概率档位：三个信号 —— 隐含要求相对自身能力的差距、
	// 成长持续性、盈利质量。任一明显不利就降档。
	out.Fulfillment, out.FulfillmentNote = judgeFulfillment(out.ImpliedGrowth, achievable, growthPersist, qualityScore)

	// ④ 若成长只按自身实际增速实现，合理价值是多少、相对现价跌多少。
	out.ValueAtAchievable = dcfValue(fcf, achievable, wacc, terminal) / shares
	if out.ValueAtAchievable > 0 {
		out.DownsideIfMiss = out.ValueAtAchievable/price - 1
	}

	out.Available = true
	out.Verdict, out.Note = impliedVerdict(out, fairValuePerShare, price)
	return out
}

// judgeFulfillment 判断"市场要求的增速能否兑现"。
//
// 三个信号，任一明显不利就降档 —— 刻意用**不利即降档**而不是加权平均：
// 兑现概率这种事，短板比长板重要（一项做不成就全做不成）。
func judgeFulfillment(implied, achievable, growthPersist, qualityScore float64) (string, string) {
	gap := implied - achievable
	reasons := []string{}

	switch {
	case gap <= 0:
		reasons = append(reasons, fmt.Sprintf(
			"市场要求的 %.1f%% 不高于公司自身近年的 %.1f%%，兑现压力小", implied*100, achievable*100))
	case gap <= 0.05:
		reasons = append(reasons, fmt.Sprintf(
			"市场要求比自身增速高 %.1f 个百分点，差距不大", gap*100))
	case gap <= 0.15:
		reasons = append(reasons, fmt.Sprintf(
			"市场要求比自身增速高 %.1f 个百分点，需要成长加速", gap*100))
	default:
		reasons = append(reasons, fmt.Sprintf(
			"市场要求比自身增速高 %.1f 个百分点，需要显著加速", gap*100))
	}

	if growthPersist > 0 {
		switch {
		case growthPersist >= 0.7:
			reasons = append(reasons, fmt.Sprintf("成长持续性 %.0f%%（较好）", growthPersist*100))
		case growthPersist >= 0.4:
			reasons = append(reasons, fmt.Sprintf("成长持续性 %.0f%%（一般）", growthPersist*100))
		default:
			reasons = append(reasons, fmt.Sprintf("成长持续性仅 %.0f%%（偏弱）", growthPersist*100))
		}
	} else {
		reasons = append(reasons, "成长持续性数据不足")
	}

	if qualityScore > 0 {
		switch {
		case qualityScore >= 70:
			reasons = append(reasons, fmt.Sprintf("盈利质量 %.0f（较高）", qualityScore))
		case qualityScore >= 50:
			reasons = append(reasons, fmt.Sprintf("盈利质量 %.0f（中等）", qualityScore))
		default:
			reasons = append(reasons, fmt.Sprintf("盈利质量 %.0f（偏低）", qualityScore))
		}
	}

	// 判定：以"差距"为主，持续性与质量作为降档条件
	level := "medium"
	switch {
	case gap <= 0.02 && (growthPersist == 0 || growthPersist >= 0.5) && (qualityScore == 0 || qualityScore >= 50):
		level = "high"
	case gap > 0.20:
		level = "low"
	case gap > 0.10 && (growthPersist < 0.4 || (qualityScore > 0 && qualityScore < 45)):
		level = "low"
	case gap > 0.10:
		level = "medium"
	}

	return level, joinStrings(reasons, "；")
}

// impliedVerdict 给出结论档位与文案。
//
// 阈值与旧的 `impliedGrowth` 保持一致（±10 个百分点），
// 这样替换实现不会让"偏高/偏低"的判定口径悄悄变化。
func impliedVerdict(out ImpliedExpectation, fairValuePerShare, price float64) (string, string) {
	durationText := fmt.Sprintf("%.1f 年", out.ImpliedYears)
	if out.DurationCapped || out.Unreachable {
		durationText = fmt.Sprintf("≥%d 年", ImpliedDurationCap)
	}
	growthText := fmt.Sprintf("%.1f%%", out.ImpliedGrowth*100)
	if out.Unreachable {
		growthText = fmt.Sprintf("≥%.1f%%", out.ImpliedGrowth*100)
	}
	base := fmt.Sprintf("按当前价反解，市场要求的长期增速约 %s、"+
		"且需要持续约 %s 才能支撑当前市值；公司自身近年增速 %.1f%%（%s）",
		growthText, durationText, out.AchievableGrowth*100, out.AchievableBasis)

	// ⚠ "要求高于自身能力"必须**同时**要求"要求的增速本身是正的"。
	//
	// 反例（实测踩到过）：盐湖股份自身增速 −20.4%（在萎缩）、市场隐含 −9.1%，
	// 差额 +11.3% 满足 `GrowthGap > 0.10`，于是被判为 over（市场要求过高）——
	// 但"市场要求它萎缩得少一点"根本不是高估，那是**衰退股被定价为衰退放缓**。
	// 单看差额会在"自身增速为负"时系统性地误判。
	//
	// 所以阈值改成"差额够大 **且** 隐含要求是实质性正增长"。
	// 衰退股的合理标签是 fair/under（或由其它风险码覆盖），不是 over。
	const overGrowthFloor = 0.05

	switch {
	case out.GrowthGap > 0.10 && out.ImpliedGrowth > overGrowthFloor:
		return "over", base + fmt.Sprintf("。市场要求明显高于自身能力（高出 %.1f 个百分点），"+
			"成长一旦不及预期，估值下修风险显著；若只按自身增速实现，"+
			"合理价值约 %.2f 元、相对现价 %.1f%%",
			out.GrowthGap*100, out.ValueAtAchievable, out.DownsideIfMiss*100)
	case out.GrowthGap < -0.10:
		return "under", base + fmt.Sprintf("。市场要求低于自身能力（低 %.1f 个百分点），"+
			"若成长能维持，存在估值修复空间",
			-out.GrowthGap*100)
	default:
		return "fair", base + "。市场要求与自身能力基本匹配，估值对成长的依赖处于合理区间"
	}
}
