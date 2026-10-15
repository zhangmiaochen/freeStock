package valuation

import (
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// 第四层与第五层：成长调整后的合理价值 + 多模型交叉验证
//
// 方案 §5.4 的核心问题：**当前估值是否已经反映了公司未来可能实现的成长？
// 市场给予的估值溢价是否有足够的基本面支撑？**
//
// 三条明确禁止（方案 §5.4 末段）：
//   - 不能简单把高增长率直接转换成高估值 → 增速先经过衰减与质量折价，见 fairPE；
//   - 不能将行业平均估值不加调整地套用于所有公司 → 行业估值只作为"环境"，
//     不直接当目标倍数（见 fairvalue 里的环境修正）；
//   - 相对估值用于判断市场定价位置、绝对估值用于检验内在价值，两者冲突时
//     **解释原因并降低置信度，不机械取平均**（见 reconcile）。
// ---------------------------------------------------------------------------

// MethodResult 是一种估值方法给出的结论。
type MethodResult struct {
	Method Method `json:"method"`
	Label  string `json:"label"`
	// Applicable 表示该方法的适用条件是否满足。
	Applicable bool `json:"applicable"`
	// Reason 说明适用/不适用的依据（带数值）。
	Reason string `json:"reason"`

	// FairValue 是该方法的每股合理价值（元）。
	FairValue float64 `json:"fairValue"`
	// Low / High 是该方法的价值区间（悲观/乐观）。
	Low  float64 `json:"low"`
	High float64 `json:"high"`

	// TargetMultiple 是采用的目标倍数（PE/PB/PS 等方法给出）。
	TargetMultiple float64 `json:"targetMultiple"`
	// CurrentMultiple 是当前倍数。
	CurrentMultiple float64 `json:"currentMultiple"`
	// Basis 是价值锚的口径说明，如 "预测 TTM 每股收益 3.21 元 × 目标 PE 15.0 倍"。
	Basis string `json:"basis"`
}

// FairValue 是合理价值与安全边际的汇总。
type FairValue struct {
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`

	// Price 是当前价格（不复权收盘价，元）。
	Price float64 `json:"price"`
	// Shares 是当前总股本。
	Shares float64 `json:"shares"`

	// 三情景每股合理价值。
	Pessimistic float64 `json:"pessimistic"`
	Base        float64 `json:"base"`
	Optimistic  float64 `json:"optimistic"`

	// SafetyMargin 是安全边际 =（基准合理价值 − 当前价格）÷ 基准合理价值。
	SafetyMargin float64 `json:"safetyMargin"`
	// Upside 是潜在收益 =（目标价值 − 当前价格）÷ 当前价格。
	//
	// ⚠ 与安全边际**口径不同、不能混用**（方案 §5.6 明确要求），
	// 所以这两个字段在结构体里与界面上都分开展示。
	Upside float64 `json:"upside"`

	// Methods 是各方法的结论（含不适用的，便于界面解释"为什么没用 DCF"）。
	Methods []MethodResult `json:"methods"`

	// MethodsUsed 是真正参与汇总的方法。
	MethodsUsed []Method `json:"methodsUsed"`
	// Conflict 表示多方法结果明显冲突（区间不重叠）。
	Conflict bool `json:"conflict"`
	// ConflictNote 说明冲突的具体表现。
	ConflictNote string `json:"conflictNote,omitempty"`

	// ImpliedGrowth 是当前价格隐含的成长预期（反向 DCF 的结果）。
	ImpliedGrowth float64 `json:"impliedGrowth"`
	// ModelGrowth 是模型采用的成长假设。
	ModelGrowth float64 `json:"modelGrowth"`
	// GrowthExpectation 是"市场预期偏高 / 合理 / 偏低"的结论。
	GrowthExpectation string `json:"growthExpectation"`
	GrowthNote        string `json:"growthNote"`

	// Confidence 是估值置信度。
	Confidence Confidence `json:"confidence"`

	// Warnings 是估值方法选择与假设上的提示（界面必须显示）。
	Warnings []string `json:"warnings"`

	// Assumptions 是关键假设清单（可追溯）。
	Assumptions []Evidence `json:"assumptions"`
}

// safetyMarginCapPercent 返回该期限允许的最大价值空间（基准合理价值 ÷ 现价 − 1）。
//
// 0 表示不限。数值依据见调用处的说明：短期限内价格走不到那么远，
// 而模型给出"便宜 3 倍"通常意味着用错了盈利基数（周期高点）。
func safetyMarginCapPercent(horizonKey string) float64 {
	switch horizonKey {
	case "1m":
		return 0.60
	case "3m":
		return 1.00
	case "6m":
		return 1.50
	case "12m":
		return 2.00
	case "36m":
		return 0 // 长期不做上限：3 年时间足够让低估真正兑现
	}
	return 1.00
}

// growthDampening 是"历史增速 → 预测增速"的衰减系数。
//
// 为什么必须衰减：连续 3 年 30% 增长的公司，直接外推 3 年 30% 会得到一个
// 高得离谱的价值。实证上增速会向行业与 GDP 中枢回归，所以：
//
//	预测增速 = 历史增速 × 衰减系数，并夹在 [0, 上限] 内
//
// 衰减系数按**成长质量**分档（高质量公司的增速更可持续，这与方案
// "成长质量单独评价"的要求是一致的：质量在这里影响可持续性，不影响当前得分）。
func growthDampening(qualityScore float64, horizonKey string) float64 {
	base := 0.55
	switch {
	case qualityScore >= 75:
		base = 0.80
	case qualityScore >= 60:
		base = 0.68
	case qualityScore >= 45:
		base = 0.55
	default:
		base = 0.42
	}
	// 期限越长，衰减越强（越远的预测越不可靠）
	switch horizonKey {
	case "1m", "3m":
		return math.Min(base*1.35, 0.95)
	case "6m":
		return base
	case "12m":
		return base * 0.90
	case "36m":
		return base * 0.72
	}
	return base
}

// fairPE 是"成长调整后的合理 PE"。
//
// 这是方案 §5.4 的核心公式。设计意图：
//
//	基础 8 倍        —— 零增长、低质量公司也不该低于这个数（壳价值 + 分红能力）
//	+ 最多 22 倍     —— 由成长能力分与成长质量分的加权位置决定
//	上限 40 倍       —— 再高的 PE 需要极强的持续成长支撑，本模型不给
//
// ⚠ 它**不是** PEG：PEG 会把 50% 增速直接换算成 50 倍 PE，本模型通过
// "质量加权 + 上限"避免这一点。质量差的公司在同样增速下拿到的目标 PE 明显更低。
func fairPE(growthScore, qualityScore float64) float64 {
	// 成长与质量的加权位置：两者都满分时得 1，都为 0 时得 0
	position := (growthScore*0.55 + qualityScore*0.45) / 100
	return 8 + 22*clamp01(position)
}

// fairPB 是"PB-ROE 框架下的合理 PB"。
//
// 理论依据：在稳定状态下，PB ≈ (ROE − g) / (r − g)。
// 这里用它的简化形式，r（要求回报率）取 WACC，g 取稳态增长。
//
//	合理 PB = (ROE − g) / (r − g)
//
// ⚠ ROE 取的是**当期 TTM**，而周期股/高杠杆金融股的当期 ROE 常常是峰值。
// 这会让公式给出远超现实的合理 PB（实测新华保险：ROE 30% 会推出 12 倍 PB
// 的上限，而它的当期 PB 只有 1.6 倍 —— 于是"安全边际 84%"这种荒谬结论）。
// 所以这里做了两道限制：
//
//  1. **上限从 12 倍收到 6 倍**：A 股里 PB 长期高于 6 倍的只有极少数高 ROE
//     消费/科技龙头，把 6 倍当上限仍很宽裕；
//  2. ROE 越高时**给一个递减的可信度**：ROE 超过 25% 的部分按一半计入，
//     因为长期维持 25% 以上 ROE 的公司极少（均值回归是硬约束）。
func fairPB(roe, wacc, terminalGrowth float64) (float64, string) {
	if roe <= 0 {
		return 0, "ROE ≤ 0，PB-ROE 框架不适用"
	}
	// ROE 的可维持化：超过 25% 的部分按一半计入
	adjusted := roe
	note := ""
	if roe > 0.25 {
		adjusted = 0.25 + (roe-0.25)*0.5
		note = fmt.Sprintf("当期 ROE 高达 %.1f%%，已按「超过 25%% 的部分只计入一半」"+
			"调整为可维持 ROE %.1f%%（长期维持 25%% 以上 ROE 的公司极少）",
			roe*100, adjusted*100)
	}
	denominator := wacc - terminalGrowth
	if denominator <= 0.01 {
		denominator = 0.01
	}
	value := (adjusted - terminalGrowth) / denominator
	if value <= 0 {
		value = 0.5
		if note != "" {
			note += "；"
		}
		note += fmt.Sprintf("调整后 ROE %.1f%% 低于稳态增长 %.1f%%，模型算出的合理 PB 为负；"+
			"回落到 0.5 倍净资产作为下限（净资产的重置/清算价值）", adjusted*100, terminalGrowth*100)
	} else if value > 6 {
		value = 6
		if note != "" {
			note += "；"
		}
		note += "模型算出的合理 PB 超过 6 倍，已按上限截断"
	}
	return value, note
}

// industryEnvironmentFactor 把行业估值环境折成一个温和的乘数。
//
// 方案要求"行业估值不能直接当目标倍数"，所以这里**只做 ±8% 的修正**：
// 行业贵就让合理价值略低，行业便宜就略高。真正的价值判断仍来自公司自身。
func industryEnvironmentFactor(industry IndustryValuation) (float64, string) {
	if industry.PEPercentile <= 0 {
		return 1.0, "行业历史分位不可用，未对合理价值做行业环境修正（按中性处理）"
	}
	// 分位 50 → 1.00；分位 0 → 1.08；分位 100 → 0.92
	factor := 1.0 + (50-industry.PEPercentile)/50*0.08
	return factor, fmt.Sprintf("行业 PE 处于自身历史 %.0f%% 分位，合理价值乘数 %.3f（仅 ±8%% 的温和修正）",
		industry.PEPercentile, factor)
}

// FairValueInput 是合理价值测算的输入。
type FairValueInput struct {
	Company    CompanyValuation
	AsOf       AsOf
	Price      float64 // 当前不复权收盘价
	Shares     float64 // 当前总股本
	PrevShares float64 // 上一年总股本（用于识别股本摊薄）
	Growth     Growth
	Quality    Quality
	Industry   IndustryValuation
	// IndustryAvailable 表示行业聚合是否可用。
	IndustryAvailable bool
	Horizon           Horizon
	Params            Params
	// PriceSeries 用于算波动率（置信度用）。
	PriceSeries PriceSeries
}

// EvaluateFairValue 测算某档期限的合理价值与安全边际。
func EvaluateFairValue(input FairValueInput) FairValue {
	out := FairValue{
		Price:  input.Price,
		Shares: input.Shares,
	}
	if input.Shares <= 0 || input.Price <= 0 {
		out.Note = "缺少当前价格或总股本，无法测算每股合理价值"
		out.Confidence = Confidence{Score: 20, Level: "low", Reasons: []string{out.Note}}
		return out
	}
	asOf := input.AsOf

	// ---- 成长假设 ----
	modelGrowth, growthNote := projectGrowth(input)
	out.ModelGrowth = modelGrowth
	out.GrowthNote = growthNote

	// ---- 股本摊薄 ----
	dilution := 0.0
	if input.PrevShares > 0 && input.Shares > 0 {
		dilution = input.Shares/input.PrevShares - 1
		if dilution > 0.02 {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"总股本比去年同期增加 %.1f%%；每股价值的增长会被摊薄抵消一部分（已按摊薄后的每股口径计算）",
				dilution*100))
		}
	}

	// ---- 环境修正 ----
	envFactor, envNote := industryEnvironmentFactor(input.Industry)
	if !input.IndustryAvailable {
		envFactor, envNote = 1.0, "行业聚合不可用，未做行业环境修正"
	}

	// ---- 三情景假设 ----
	// 悲观：增速取负一档（历史增速的一半作反向，或 −8% 取更低的那个）
	// 乐观：增速取模型增速的 1.35 倍
	baseGrowth := modelGrowth
	pessimisticGrowth := math.Min(baseGrowth*0.5-0.10, -0.05)
	optimisticGrowth := baseGrowth * 1.35

	// ---- 逐年预测的 TTM 每股指标 ----
	years := float64(input.Horizon.TradingDays) / 252.0
	project := func(growth float64) (eps, bps, sps, fcfPerShare float64) {
		factor := math.Pow(1+growth, years)
		// 稀释：按历史摊薄速度线性外推（最多外推 3 年）
		dilute := 1.0
		if dilution > 0 {
			span := math.Min(years, 3)
			dilute = math.Pow(1+dilution, span)
		}
		if asOf.TTM.NetProfit > 0 {
			eps = asOf.TTM.NetProfit * factor / input.Shares / dilute
		}
		if equity := asOf.Items[ItemTotalEquity]; equity > 0 {
			// 净资产增长：留存 = 净利润 × (1 − 分红率)；没有分红数据时按 70% 留存
			bps = equity / input.Shares
			// 用净利润累积近似净资产增长（简化：不做分红拆分，直接按利润留存 70%）
			bps += asOf.TTM.NetProfit * factor * 0.70 * years / input.Shares / dilute
		}
		if asOf.TTM.Revenue > 0 {
			sps = asOf.TTM.Revenue * factor / input.Shares / dilute
		}
		if asOf.TTM.FreeCashFlow != 0 {
			fcfPerShare = asOf.TTM.FreeCashFlow * factor / input.Shares / dilute
		}
		return
	}

	// ---- 方法 1：PE ----
	methods := make([]MethodResult, 0, 6)
	peResult := evaluatePEMethod(input, out.ModelGrowth, baseGrowth, pessimisticGrowth, optimisticGrowth, project, envFactor)
	methods = append(methods, peResult)

	// ---- 方法 2：PB（PB-ROE） ----
	methods = append(methods, evaluatePBMethod(input, project, envFactor))

	// ---- 方法 3：PS ----
	methods = append(methods, evaluatePSMethod(input, project, envFactor))

	// ---- 方法 4：EV/EBITDA（近似） ----
	methods = append(methods, evaluateEVEBITDAMethod(input, project, envFactor))

	// ---- 方法 5：DCF（仅 36 个月） ----
	methods = append(methods, evaluateDCFMethod(input, baseGrowth, pessimisticGrowth, optimisticGrowth))

	// ---- 方法 6：周期调整估值（行业判为周期时） ----
	methods = append(methods, evaluateCyclicalMethod(input, project, envFactor))

	out.Methods = methods

	// ---- 汇总三情景 ----
	out.Pessimistic, out.Base, out.Optimistic, out.MethodsUsed, out.Warnings =
		combineMethods(methods, out.Warnings)

	// 统一乘上行业环境修正
	if envFactor != 1.0 {
		out.Pessimistic *= envFactor
		out.Base *= envFactor
		out.Optimistic *= envFactor
	}
	out.Warnings = append(out.Warnings, envNote)

	if out.Base <= 0 {
		out.Note = "没有适用的估值方法（缺少盈利、净资产与收入数据），无法测算合理价值"
		out.Confidence = Confidence{Score: 15, Level: "low", Reasons: []string{out.Note}}
		return out
	}

	// 保证区间有序（各方法区间可能交叉）
	out.Pessimistic = math.Min(out.Pessimistic, out.Base)
	out.Optimistic = math.Max(out.Optimistic, out.Base)

	// ---- 安全边际/潜在收益的上限（防止价值模型产出荒谬结论）----
	//
	// 为什么需要这一道：稳定增长型的估值方法（PB-ROE、DCF）作用在**周期高点
	// 盈利**上会给出远超现实的合理价值。实测量级：新华保险当期 PE 3.8 倍，
	// PB-ROE 用当期 ROE 推出 6 倍 PB（上限）→ 基准合理价值是现价的 6 倍以上，
	// "安全边际 84%"。方案 §6.2 明确要求"避免模型因过度乐观而给出虚高的
	// 性价比评分"，所以这里按期限**夹住**价值空间：
	//
	//	1 个月 60% / 3 个月 100% / 6 个月 150% / 12 个月 200% / 36 个月不限
	//
	// 依据：向合理价值收敛的比例本身就随期限递增（12%/25%/45%/75%/100%），
	// 所以短期限内"已经比合理价值便宜 3 倍"没有可操作性 —— 那不是估值信号，
	// 而是模型在用错盈利基数。夹住的是**极端值**（>200%），正常区间不受影响。
	if cap := safetyMarginCapPercent(input.Horizon.Key); cap > 0 && out.Base > 0 {
		ceiling := out.Price * (1 + cap)
		if out.Base > ceiling {
			ratio := ceiling / out.Base
			out.Base = ceiling
			out.Optimistic = math.Min(out.Optimistic, out.Price*(1+cap*1.5))
			out.Pessimistic *= ratio
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s 期限的基准合理价值被夹到现价的 %.0f%% 上限（模型原值 %.2f 元、现价 %.2f 元，"+
					"价值空间超过 %.0f%%）：稳定增长型方法作用在周期高点的当期盈利上会系统性高估，"+
					"本上限就是为此设置的", input.Horizon.Label, (1+cap)*100, out.Base/ratio, out.Price, cap*100))
		}
	}

	// ---- 安全边际与潜在收益（口径不同，分别算） ----
	out.SafetyMargin = (out.Base - out.Price) / out.Base
	out.Upside = (out.Optimistic - out.Price) / out.Price

	// ---- 反向 DCF：当前价格隐含的成长预期 ----
	out.ImpliedGrowth, out.GrowthExpectation, out.GrowthNote = impliedGrowth(input, out)

	// ---- 多方法冲突 ----
	out.Conflict, out.ConflictNote = detectMethodConflict(methods)

	out.Available = true
	out.Confidence = estimateValuationConfidence(input, out)
	out.Assumptions = buildAssumptions(input, out)
	return out
}

// projectGrowth 给出模型采用的成长假设。
//
// 优先用营收 3 年 CAGR（比单季同比稳），缺失时回落到单季同比；
// 再乘上按成长质量分档的衰减系数，并夹在 [−15%, +45%] 内。
func projectGrowth(input FairValueInput) (float64, string) {
	growth := input.Growth
	base := growth.RevenueCAGR3
	source := "近 3 年营收 CAGR"
	if base == 0 {
		base = growth.RevenueYoY
		source = "最近单季营收同比"
	}
	if base == 0 {
		base = growth.ProfitYoY
		source = "最近单季净利润同比"
	}
	if base == 0 {
		return input.Params.TerminalGrowth, "成长数据不足，按稳态增长假设（营收/利润的增长指标都缺失）"
	}
	dampening := growthDampening(input.Quality.Score, input.Horizon.Key)
	projected := base * dampening
	// 夹持上限 30%（不是 45%）：理由是三条独立的约束同时指向这个量级 ——
	//   ① 名义 GDP + 通胀的长期名义增速在 8~10%，能长期跑赢 25% 的公司极少；
	//   ② 周期股/金融股的"3 年 CAGR 45%"（实测新华保险）往往是基数效应或
	//      景气高峰，把它外推 3 年会得出荒谬的合理价值；
	//   ③ 目标 PE 上限 30 倍已经是 A 股里极贵的水平，再叠加 45% 的盈利
	//      外推会得到 5 倍以上的价值空间 —— 那不是估值，是想象。
	if projected > 0.30 {
		projected = 0.30
	}
	if projected < -0.15 {
		projected = -0.15
	}
	// 期限越长越向稳态增长回归
	if input.Horizon.Key == "36m" {
		projected = projected*0.75 + input.Params.TerminalGrowth*0.25
	}
	return projected, fmt.Sprintf(
		"历史增速 %.1f%%（%s）× 衰减系数 %.2f（按成长质量分 %.0f 分档、期限 %s 调整）= 预测增速 %.1f%%；"+
			"夹持区间 [−15%%, +45%%]，36 个月期限再向稳态增长 %.1f%% 回归",
		base*100, source, dampening, input.Quality.Score, input.Horizon.Label, projected*100,
		input.Params.TerminalGrowth*100)
}

// evaluatePEMethod 用 PE 法算合理价值。
func evaluatePEMethod(input FairValueInput, modelGrowth, baseGrowth, pessimisticGrowth, optimisticGrowth float64,
	project func(float64) (float64, float64, float64, float64), envFactor float64) MethodResult {
	out := MethodResult{
		Method: MethodPE,
		Label:  MethodLabel(MethodPE),
	}
	asOf := input.AsOf
	if asOf.TTM.NetProfit <= 0 {
		out.Reason = fmt.Sprintf("TTM 归母净利润 %.2f 亿 ≤ 0（亏损或数据缺失），PE 法不适用；已切换到 PS/PB 法",
			asOf.TTM.NetProfit/1e8)
		return out
	}
	target := fairPE(input.Growth.Score, input.Quality.Score)
	current := 0.0
	if asOf.TTM.NetProfit > 0 && input.Shares > 0 && input.Price > 0 {
		epsTTM := asOf.TTM.NetProfit / input.Shares
		if epsTTM > 0 {
			current = input.Price / epsTTM
		}
	}
	epsBase, _, _, _ := project(baseGrowth)
	epsPess, _, _, _ := project(pessimisticGrowth)
	epsOpt, _, _, _ := project(optimisticGrowth)
	if epsBase <= 0 {
		out.Reason = "预测每股收益 ≤ 0，PE 法不适用"
		return out
	}
	out.Applicable = true
	out.TargetMultiple = target
	out.CurrentMultiple = current
	out.FairValue = epsBase * target
	out.Low = math.Max(epsPess*target*0.85, 0)
	out.High = epsOpt * target * 1.10
	out.Basis = fmt.Sprintf("预测 TTM 每股收益 %.2f 元 × 目标 PE %.1f 倍"+
		"（成长分 %.0f × 55%% + 质量分 %.0f × 45%% 映射，8~30 倍区间）",
		epsBase, target, input.Growth.Score, input.Quality.Score)
	if current > 0 && target > 0 {
		out.Reason = fmt.Sprintf("当前 PE %.1f 倍 → 目标 PE %.1f 倍（%+.0f%%）；"+
			"盈利预测增速 %.1f%%（%s）", current, target, (target/current-1)*100, baseGrowth*100, input.Horizon.Label)
	}
	_ = modelGrowth
	_ = envFactor
	return out
}

// evaluatePBMethod 用 PB-ROE 算合理价值。
func evaluatePBMethod(input FairValueInput, project func(float64) (float64, float64, float64, float64), envFactor float64) MethodResult {
	out := MethodResult{Method: MethodPB, Label: MethodLabel(MethodPB)}
	asOf := input.AsOf
	equity := asOf.Items[ItemTotalEquity]
	if equity <= 0 || input.Shares <= 0 {
		out.Reason = "缺少归母净资产或股本，PB 法不适用"
		return out
	}
	roe := returnOnEquity(asOf, AsOf{})
	if roe <= 0 {
		out.Reason = fmt.Sprintf("TTM ROE 为 %.1f%%（≤0，公司未创造正回报），PB-ROE 框架不适用", roe*100)
		return out
	}
	target, note := fairPB(roe, input.Params.WACC, input.Params.TerminalGrowth)
	if target <= 0 {
		out.Reason = note
		return out
	}
	bps := equity / input.Shares
	current := 0.0
	if bps > 0 {
		current = input.Price / bps
	}
	out.Applicable = true
	out.TargetMultiple = target
	out.CurrentMultiple = current
	out.FairValue = bps * target
	out.Low = bps * target * 0.80
	out.High = bps * target * 1.20
	out.Basis = fmt.Sprintf("每股净资产 %.2f 元 × 合理 PB %.2f 倍"+
		"（PB-ROE：(ROE %.1f%% − g %.1f%%) ÷ (WACC %.1f%% − g %.1f%%)）",
		bps, target, roe*100, input.Params.TerminalGrowth*100,
		input.Params.WACC*100, input.Params.TerminalGrowth*100)
	if note != "" {
		out.Basis += "；" + note
	}
	if current > 0 {
		out.Reason = fmt.Sprintf("当前 PB %.2f 倍 → 合理 PB %.2f 倍（%+.0f%%）", current, target, (target/current-1)*100)
	}
	_ = project
	_ = envFactor
	return out
}

// evaluatePSMethod 用 PS 法算合理价值（利润不稳定时的主要方法）。
func evaluatePSMethod(input FairValueInput, project func(float64) (float64, float64, float64, float64), envFactor float64) MethodResult {
	out := MethodResult{Method: MethodPS, Label: MethodLabel(MethodPS)}
	asOf := input.AsOf
	revenue := asOf.TTM.Revenue
	if revenue <= 0 || input.Shares <= 0 {
		out.Reason = "缺少营业收入或股本，PS 法不适用"
		return out
	}
	// PS 目标：用"净利率 × 合理 PE"推导。
	// 净利率缺失（亏损）时按行业常见的 5% 中性假设，并明确标注。
	netMargin := 0.05
	marginNote := "净利率不可用（净利润 ≤ 0），按 5% 中性净利率假设"
	if revenue > 0 && asOf.TTM.NetProfit > 0 {
		netMargin = asOf.TTM.NetProfit / revenue
		marginNote = fmt.Sprintf("按当前 TTM 净利率 %.1f%%", netMargin*100)
	}
	pe := fairPE(input.Growth.Score, input.Quality.Score)
	target := netMargin * pe
	if target <= 0 {
		out.Reason = "推导出的合理 PS 为负，PS 法不适用"
		return out
	}
	sps := revenue / input.Shares
	current := 0.0
	if sps > 0 {
		current = input.Price / sps
	}
	out.Applicable = true
	out.TargetMultiple = target
	out.CurrentMultiple = current
	out.FairValue = sps * target
	out.Low = sps * target * 0.75
	out.High = sps * target * 1.25
	out.Basis = fmt.Sprintf("每股营业收入 %.2f 元 × 合理 PS %.2f 倍"+
		"（= %s × 合理 PE %.1f 倍）", sps, target, marginNote, pe)
	if current > 0 {
		out.Reason = fmt.Sprintf("当前 PS %.2f 倍 → 合理 PS %.2f 倍（%+.0f%%）", current, target, (target/current-1)*100)
	}
	_ = project
	_ = envFactor
	return out
}

// evaluateEVEBITDAMethod 用 EV/EBITDA 算合理价值。
//
// ⚠ 本机**没有"折旧摊销"科目**，所以 EBITDA 用营业利润近似（方案 §16 已如实降级）。
// 这会让重资产行业的 EBITDA 被系统性低估，所以只在"营业利润为正且公司负债率
// 明显高于行业中位"时才采用 —— 那时 EV/EBITDA 相对 PE 更能反映资本结构差异。
func evaluateEVEBITDAMethod(input FairValueInput, project func(float64) (float64, float64, float64, float64), envFactor float64) MethodResult {
	out := MethodResult{Method: MethodEVEBITDA, Label: MethodLabel(MethodEVEBITDA)}
	asOf := input.AsOf
	ebitda := asOf.TTM.OperatingProfit
	if ebitda <= 0 {
		out.Reason = "营业利润 ≤ 0，EV/EBITDA 法不适用"
		return out
	}
	if input.Company.DebtRatio < 0.40 {
		out.Reason = fmt.Sprintf("资产负债率仅 %.1f%%，资本结构差异不显著；"+
			"EV/EBITDA 相比 PE 没有额外信息量，本次不采用（避免重复计分）", input.Company.DebtRatio*100)
		return out
	}
	// 目标 EV/EBITDA：以 10 倍为基础，按成长质量位置在 6~16 倍之间
	position := (input.Growth.Score*0.55 + input.Quality.Score*0.45) / 100
	targetMultiple := 6 + 10*clamp01(position)

	// EV = 市值 + 净负债；净负债用总负债 − 总资产×0.3 近似（无有息负债科目）
	equity := asOf.Items[ItemTotalEquity]
	totalAssets := asOf.Items[ItemTotalAssets]
	totalLiabilities := asOf.Items[ItemTotalLiabilities]
	if equity <= 0 || totalAssets <= 0 {
		out.Reason = "缺少净资产或总资产，无法估算企业价值（EV）"
		return out
	}
	netDebt := totalLiabilities - totalLiabilities*0.55 // 近似：45% 视为经营性负债
	targetEV := ebitda * targetMultiple
	targetEquity := targetEV - netDebt
	if targetEquity <= 0 {
		out.Reason = fmt.Sprintf("按目标 EV/EBITDA %.1f 倍算出的股权价值为负（净负债 %.2f 亿），该方法不适用",
			targetMultiple, netDebt/1e8)
		return out
	}
	out.Applicable = true
	out.TargetMultiple = targetMultiple
	out.FairValue = targetEquity / input.Shares
	// 当前 EV/EBITDA
	currentMarketCap := input.Price * input.Shares
	if ebitda > 0 {
		out.CurrentMultiple = (currentMarketCap + netDebt) / ebitda
	}
	out.Low = out.FairValue * 0.75
	out.High = out.FairValue * 1.25
	out.Basis = fmt.Sprintf("EBITDA 近似值 %.2f 亿（= 营业利润，**未减折旧摊销**，本机无该科目）× 目标 EV/EBITDA %.1f 倍，"+
		"扣除净负债 %.2f 亿后 ÷ 股本", ebitda/1e8, targetMultiple, netDebt/1e8)
	out.Reason = "公司资产负债率较高，资本结构差异显著，EV/EBITDA 相比 PE 更能反映真实估值水平"
	_ = project
	_ = envFactor
	return out
}

// evaluateDCFMethod 用简化两阶段 DCF 算内在价值（只在 36 个月期限使用）。
//
// 口径（全部显式写出，可追溯）：
//
//	自由现金流 = 经营现金流 − 资本开支（TTM）
//	第 1~5 年：FCF 按模型增速增长，并逐年衰减到稳态
//	终值：第 5 年 FCF × (1+g) / (WACC − g)（永续增长模型）
//	股权价值 = 企业价值（这里直接用 FCF 折现，因为净负债无法可靠拆分）
func evaluateDCFMethod(input FairValueInput, baseGrowth, pessimisticGrowth, optimisticGrowth float64) MethodResult {
	out := MethodResult{Method: MethodDCF, Label: MethodLabel(MethodDCF)}
	if input.Horizon.Key != "36m" {
		out.Reason = fmt.Sprintf("%s 期限不做 DCF：现金流折现依赖长期终值假设，"+
			"用在 %s 上会把长期结论硬套到短期（方案 §6.1 明确禁止）",
			input.Horizon.Label, input.Horizon.Label)
		return out
	}
	asOf := input.AsOf
	fcf := asOf.TTM.FreeCashFlow
	if fcf <= 0 {
		out.Reason = fmt.Sprintf("TTM 自由现金流 %.2f 亿 ≤ 0，DCF 不适用", fcf/1e8)
		return out
	}
	if asOf.TTM.CashOperateQuarters < 4 || asOf.TTM.CapexQuarters < 4 {
		out.Reason = fmt.Sprintf("现金流科目不完整（经营现金流 %d 个季度、资本开支 %d 个季度，需 4 个），DCF 不适用",
			asOf.TTM.CashOperateQuarters, asOf.TTM.CapexQuarters)
		return out
	}
	wacc := input.Params.WACC
	g := input.Params.TerminalGrowth
	if wacc-g < 0.02 {
		out.Reason = "WACC 与稳态增长的差不足 2 个百分点，永续增长模型的终值会发散，DCF 不可靠"
		return out
	}
	if vol, ok := input.PriceSeries.AnnualizedVolatility(252); ok && vol > 0.60 {
		// 波动率超过 60% 的公司，其现金流预测的可靠性极低
		out.Reason = fmt.Sprintf("股价年化波动率 %.0f%%（>60%%），现金流可预测性过低，DCF 结论不可靠，本次不采用",
			vol*100)
		return out
	}

	value := dcfValue(fcf, baseGrowth, wacc, g)
	low := dcfValue(fcf, pessimisticGrowth, wacc, g)
	high := dcfValue(fcf, optimisticGrowth, wacc, g)

	out.Applicable = true
	out.FairValue = value / input.Shares
	out.Low = low / input.Shares
	out.High = high / input.Shares
	out.Basis = fmt.Sprintf("TTM 自由现金流 %.2f 亿（经营现金流 %.2f 亿 − 资本开支 %.2f 亿），"+
		"5 年显式预测 + 永续增长终值（WACC %.1f%%、稳态增长 %.1f%%）",
		fcf/1e8, asOf.TTM.CashOperate/1e8, asOf.TTM.Capex/1e8, wacc*100, g*100)
	out.Reason = "经营现金流为正、现金流科目完整、波动率可控，满足 DCF 的适用前提"
	return out
}

// dcfValue 算企业价值（两阶段：5 年显式 + 永续终值）。
func dcfValue(fcf, growth, wacc, terminalGrowth float64) float64 {
	value := 0.0
	current := fcf
	for year := 1; year <= 5; year++ {
		// 增速从 growth 线性衰减到 terminalGrowth
		weight := float64(year-1) / 4
		rate := growth*(1-weight) + terminalGrowth*weight
		current *= 1 + rate
		value += current / math.Pow(1+wacc, float64(year))
	}
	// 永续终值（第 5 年末）
	terminal := current * (1 + terminalGrowth) / (wacc - terminalGrowth)
	value += terminal / math.Pow(1+wacc, 5)
	return value
}

// evaluateCyclicalMethod 是周期调整估值（行业判为周期波动时启用）。
//
// 思路：周期股的当期利润可能处在峰值或谷底，直接用 TTM PE 会得出"看起来极便宜"
// 的错误结论。这里用**当前倍数与自身历史中位数的几何平均**做锚，
// 相当于对 TTM 与历史平均双口径取折中。
func evaluateCyclicalMethod(input FairValueInput, project func(float64) (float64, float64, float64, float64), envFactor float64) MethodResult {
	out := MethodResult{Method: MethodCyclicalAdj, Label: MethodLabel(MethodCyclicalAdj)}
	if input.Industry.Stage != "cyclical" {
		out.Reason = fmt.Sprintf("行业阶段判定为「%s」而非周期波动，不采用周期调整估值",
			stageLabel(input.Industry.Stage))
		return out
	}
	asOf := input.AsOf
	if asOf.TTM.NetProfit <= 0 || input.Shares <= 0 {
		out.Reason = "亏损或缺少股本数据，周期调整估值不适用"
		return out
	}
	epsTTM := asOf.TTM.NetProfit / input.Shares
	if epsTTM <= 0 {
		out.Reason = "TTM 每股收益 ≤ 0，周期调整估值不适用"
		return out
	}
	currentPE := input.Price / epsTTM
	// 用自身历史 PE 中位数当"周期中枢倍数"
	historicalMedian := 0.0
	if input.Company.SelfAvailable {
		// SelfPercentile 不给中位数本身，所以这里用 fairPE 作为中枢的代理，
		// 并对周期股按 0.85 折价（周期中枢的合理倍数应低于成长股）
		historicalMedian = fairPE(input.Growth.Score, input.Quality.Score) * 0.85
	} else {
		historicalMedian = currentPE * 0.9
	}
	if historicalMedian <= 0 {
		out.Reason = "无法确定周期中枢倍数，不采用周期调整估值"
		return out
	}
	// 几何平均：对"当期倍数"与"中枢倍数"取折中
	anchor := math.Sqrt(currentPE * historicalMedian)
	out.Applicable = true
	out.TargetMultiple = anchor
	out.CurrentMultiple = currentPE
	out.FairValue = epsTTM * anchor
	out.Low = epsTTM * anchor * 0.70
	out.High = epsTTM * anchor * 1.30
	out.Basis = fmt.Sprintf("TTM 每股收益 %.2f 元 × 周期中枢 PE %.1f 倍"+
		"（当前 PE %.1f 倍与中枢倍数 %.1f 倍的几何平均）", epsTTM, anchor, currentPE, historicalMedian)
	out.Reason = "所属行业判定为周期波动，当期利润可能是周期峰值/谷底，" +
		"用双口径（TTM 与周期中枢）折中，避免低 PE 陷阱"
	if input.Industry.CyclicalWarning {
		out.Reason += "；且已触发「PE 低 + ROE 高位」的周期顶部预警"
	}
	_ = project
	_ = envFactor
	return out
}

// stageLabel 把行业阶段翻译成中文。
func stageLabel(stage string) string {
	switch stage {
	case "growth":
		return "成长"
	case "mature":
		return "成熟"
	case "decline":
		return "衰退"
	case "cyclical":
		return "周期波动"
	}
	return "未知"
}

// combineMethods 汇总各方法的结论。
//
// ⚠ 方案 §5.5 明确要求"两者应相互校验；若结果明显冲突，需要解释原因并降低
// 结论置信度，而不是机械取平均值"。所以这里的做法是：
//
//  1. **中位数**而不是算术平均（对单一方法的异常值更稳健）；
//  2. 检测冲突（区间不重叠）并交给置信度模块；
//  3. 只把"适用"的方法计入，不适用的方法连同原因一起保留供界面解释。
func combineMethods(methods []MethodResult, warnings []string) (pessimistic, base, optimistic float64, used []Method, outWarnings []string) {
	outWarnings = warnings
	applicable := make([]MethodResult, 0, len(methods))
	for _, method := range methods {
		if method.Applicable && method.FairValue > 0 {
			applicable = append(applicable, method)
		}
	}
	if len(applicable) == 0 {
		return 0, 0, 0, nil, outWarnings
	}
	values := make([]float64, 0, len(applicable))
	lows := make([]float64, 0, len(applicable))
	highs := make([]float64, 0, len(applicable))
	weights := make([]float64, 0, len(applicable))
	for _, method := range applicable {
		values = append(values, method.FairValue)
		lows = append(lows, method.Low)
		highs = append(highs, method.High)
		weights = append(weights, methodWeight(method.Method))
		used = append(used, method.Method)
	}

	base = weightedMedian(values, weights)
	// 悲观取"各方法下限的 25 分位"，乐观取"各方法上限的 75 分位" ——
	// 这样区间反映的是方法之间的分歧，而不只是单个方法内部的不确定性
	pessimistic = Quantile(lows, 0.25)
	optimistic = Quantile(highs, 0.75)
	if len(applicable) == 1 {
		pessimistic = applicable[0].Low
		optimistic = applicable[0].High
	}
	if pessimistic <= 0 {
		pessimistic = base * 0.60
	}
	if optimistic <= base {
		optimistic = base * 1.30
	}
	return pessimistic, base, optimistic, used, outWarnings
}

// methodWeight 是各方法在汇总中的权重。
//
// 权重依据方法对"本机数据条件"的适配度，不是主观偏好：
//   - PE 最高：盈利稳定时信息量最大，且数据最全；
//   - PB：净资产口径最可靠（资产负债表是时点值，不受累计口径影响）；
//   - PS：收入口径抗利润操纵，但对利润率不同的公司可比性差；
//   - DCF：模型最"干净"但对假设最敏感，所以权重反而低于 PE；
//   - 周期调整与 EV/EBITDA 都是修正性方法，权重最低。
func methodWeight(method Method) float64 {
	switch method {
	case MethodPE:
		return 1.0
	case MethodPB:
		return 0.8
	case MethodPS:
		return 0.6
	case MethodDCF:
		return 0.7
	case MethodCyclicalAdj:
		return 0.5
	case MethodEVEBITDA:
		return 0.5
	}
	return 0.5
}

// weightedMedian 是加权中位数。
func weightedMedian(values, weights []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	type pair struct {
		value  float64
		weight float64
	}
	pairs := make([]pair, len(values))
	for i := range values {
		pairs[i] = pair{values[i], weights[i]}
	}
	sortSlice(pairs, func(a, b pair) bool { return a.value < b.value })
	total := 0.0
	for _, item := range pairs {
		total += item.weight
	}
	if total <= 0 {
		return Median(values)
	}
	accumulated := 0.0
	for _, item := range pairs {
		accumulated += item.weight
		if accumulated >= total/2 {
			return item.value
		}
	}
	return pairs[len(pairs)-1].value
}

// detectMethodConflict 判断多方法结果是否明显冲突。
//
// 判据：任意两个适用方法的价值区间**不重叠**，且差距超过较窄区间的 30%。
// 中间取折中的区间重叠情况（例如 PE 说 20 元、PB 说 15 元但区间都很宽）
// 不算冲突 —— 那只是不确定性大，置信度模块会另行处理。
func detectMethodConflict(methods []MethodResult) (bool, string) {
	applicable := make([]MethodResult, 0, len(methods))
	for _, method := range methods {
		if method.Applicable && method.FairValue > 0 {
			applicable = append(applicable, method)
		}
	}
	if len(applicable) < 2 {
		return false, ""
	}
	for i := 0; i < len(applicable); i++ {
		for j := i + 1; j < len(applicable); j++ {
			left, right := applicable[i], applicable[j]
			overlap := math.Min(left.High, right.High) - math.Max(left.Low, right.Low)
			narrower := math.Min(left.High-left.Low, right.High-right.Low)
			if overlap <= 0 && narrower > 0 {
				gap := math.Max(left.Low, right.Low) - math.Min(left.High, right.High)
				if gap/narrower > 0.30 {
					return true, fmt.Sprintf(
						"%s 给出 %.2f 元（区间 %.2f~%.2f），%s 给出 %.2f 元（区间 %.2f~%.2f），"+
							"两者区间完全不重叠且相差 %.2f 元（超过较窄区间的 30%%）。"+
							"按方案要求**不取平均**：结论以置信度下调的方式反映这个分歧，"+
							"并请重点核对盈利口径（是否含一次性损益）与资产口径（是否有大额商誉/受限资产）",
						left.Label, left.FairValue, left.Low, left.High,
						right.Label, right.FairValue, right.Low, right.High, gap)
				}
			}
		}
	}
	return false, ""
}

// impliedGrowth 用反向 DCF 反解当前价格隐含的成长预期。
//
// 做法：把 fairPE 的目标 PE 公式视为"当前 PE 隐含的成长分"，反解出成长分，
// 再映射回增速区间。这是**检查市场预期是否过高**的关键闸门 ——
// 如果市场隐含的增速远高于公司历史与行业水平，那么"估值便宜"就是错觉。
func impliedGrowth(input FairValueInput, fair FairValue) (float64, string, string) {
	asOf := input.AsOf
	if asOf.TTM.NetProfit <= 0 || input.Shares <= 0 || input.Price <= 0 {
		return 0, "", "公司亏损或缺少股本，无法用反向 DCF 反解市场隐含预期"
	}
	epsTTM := asOf.TTM.NetProfit / input.Shares
	if epsTTM <= 0 {
		return 0, "", "TTM 每股收益 ≤ 0，无法反解隐含预期"
	}
	currentPE := input.Price / epsTTM
	quality := input.Quality.Score

	// 当前 PE 对应的"成长分"：由 fairPE 反解
	// fairPE = 8 + 22 × (g×0.55 + q×0.45)/100  ⇒  g = ((PE−8)/22 × 100 − 0.45q) / 0.55
	impliedScore := ((currentPE-8)/22*100 - 0.45*quality) / 0.55
	impliedScore = clampScore(impliedScore)

	// 把"成长分"映射回增速：用与正推相同的映射反解
	// 正推时 growthScore 由 growthCurve 组合而成，这里用最直接的近似：
	// 成长分 50 视为对应 0% 增长，100 视为 30% 增长，0 视为 −30%
	implied := (impliedScore - 50) / 50 * 0.30

	model := input.Growth.RevenueCAGR3
	if model == 0 {
		model = input.Growth.RevenueYoY
	}
	verdict := ""
	note := ""
	switch {
	case model == 0:
		verdict = "unknown"
		note = fmt.Sprintf("当前 PE %.1f 倍隐含的长期增速约 %.1f%%；"+
			"公司自身成长数据不足，无法判断该预期是否偏高", currentPE, implied*100)
	case implied > model+0.10:
		verdict = "high"
		note = fmt.Sprintf("当前 PE %.1f 倍隐含的市场预期增速约 %.1f%%，"+
			"明显高于公司近 3 年实际增速 %.1f%%（高出 %.1f 个百分点）——**市场预期偏高**，"+
			"成长一旦不及预期，估值下修风险显著", currentPE, implied*100, model*100, (implied-model)*100)
	case implied < model-0.10:
		verdict = "low"
		note = fmt.Sprintf("当前 PE %.1f 倍隐含的市场预期增速约 %.1f%%，"+
			"低于公司近 3 年实际增速 %.1f%%（低 %.1f 个百分点）——**市场预期偏低**，"+
			"若成长能维持，存在估值修复空间", currentPE, implied*100, model*100, (model-implied)*100)
	default:
		verdict = "fair"
		note = fmt.Sprintf("当前 PE %.1f 倍隐含的市场预期增速约 %.1f%%，与公司近 3 年实际增速 %.1f%% 基本匹配",
			currentPE, implied*100, model*100)
	}
	_ = fair
	return implied, verdict, note
}

// estimateValuationConfidence 估计估值置信度。
//
// 四个扣分维度（每个都带具体数值，不能只说"数据不足"）：
//
//  1. **区间宽度**：(乐观−悲观)/基准，越宽越不确定；
//  2. **假设敏感度**：增速 ±1 个百分点带来的价值变化；
//  3. **数据覆盖**：行情长度、财务科目完整度、行业样本；
//  4. **方法冲突**：区间不重叠直接重扣。
func estimateValuationConfidence(input FairValueInput, fair FairValue) Confidence {
	score := 100.0
	reasons := make([]string, 0, 5)

	// ① 区间宽度
	if fair.Base > 0 {
		width := (fair.Optimistic - fair.Pessimistic) / fair.Base
		if width > 0.30 {
			penalty := math.Min((width-0.30)/0.70*30, 30)
			score -= penalty
			reasons = append(reasons, fmt.Sprintf(
				"合理价值区间较宽：悲观 %.2f 元 ~ 乐观 %.2f 元，宽度为基准值的 %.0f%%（阈值 30%%），扣 %.0f 分",
				fair.Pessimistic, fair.Optimistic, width*100, penalty))
		}
	}

	// ② 假设敏感度：用各方法上下限的相对离散度近似
	if len(fair.MethodsUsed) >= 2 {
		values := make([]float64, 0, len(fair.Methods))
		for _, method := range fair.Methods {
			if method.Applicable {
				values = append(values, method.FairValue)
			}
		}
		if len(values) >= 2 && fair.Base > 0 {
			spread := stddev(values) / fair.Base
			if spread > 0.20 {
				penalty := math.Min((spread-0.20)/0.80*25, 25)
				score -= penalty
				reasons = append(reasons, fmt.Sprintf(
					"不同估值方法给出的价值差异较大：%d 种方法的价值标准差相当于基准值的 %.0f%%（阈值 20%%），扣 %.0f 分",
					len(values), spread*100, penalty))
			}
		}
	}

	// ③ 数据覆盖
	if input.AsOf.Quarters < 8 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf(
			"财务报告期只有 %d 期（不足 8 期），无法可靠判断成长趋势与季节性问题，扣 10 分", input.AsOf.Quarters))
	}
	if len(input.AsOf.Missing) > 0 {
		penalty := math.Min(float64(len(input.AsOf.Missing))*2.0, 15)
		score -= penalty
		reasons = append(reasons, fmt.Sprintf(
			"有 %d 个财务科目缺失（%s），扣 %.0f 分",
			len(input.AsOf.Missing), joinItems(input.AsOf.Missing, 4), penalty))
	}
	if input.AsOf.TTM.CashOperateQuarters < 4 {
		score -= 8
		reasons = append(reasons, fmt.Sprintf(
			"经营现金流只有 %d 个季度参与 TTM，自由现金流口径不完整，扣 8 分", input.AsOf.TTM.CashOperateQuarters))
	}
	if input.PriceSeries.Len() < 504 {
		penalty := math.Min(float64(504-input.PriceSeries.Len())/504*15, 15)
		score -= penalty
		reasons = append(reasons, fmt.Sprintf(
			"行情历史只有 %d 个交易日（不足 2 年 504 日），波动率与回撤的估计不稳，扣 %.0f 分",
			input.PriceSeries.Len(), penalty))
	}
	if !input.PriceSeries.Adjusted {
		score -= 12
		reasons = append(reasons, "价格序列**未应用复权因子**：区间收益与回撤在含除权的窗口里不可信，扣 12 分")
	}
	if !input.IndustryAvailable {
		score -= 10
		reasons = append(reasons, "行业聚合不可用（成分不足或行业归属缺失），缺少行业估值参照，扣 10 分")
	}

	// ④ 方法冲突
	if fair.Conflict {
		score -= 20
		reasons = append(reasons, "不同估值方法的结论区间完全不重叠（方法冲突），扣 20 分")
	}

	score = clampScore(score)
	if len(reasons) == 0 {
		reasons = append(reasons, "区间宽度、方法一致性、数据覆盖三项均正常，未扣分")
	}
	return Confidence{Score: score, Level: ConfidenceLevelFor(score), Reasons: reasons}
}

// buildAssumptions 汇总关键假设，供依据追溯。
func buildAssumptions(input FairValueInput, fair FairValue) []Evidence {
	out := make([]Evidence, 0, 8)
	out = append(out, Evidence{
		Dimension: string(DimMargin),
		Metric:    "预测增速假设",
		Value:     fmt.Sprintf("%.1f%%", fair.ModelGrowth*100),
		Raw:       fair.ModelGrowth,
		AsOf:      input.AsOf.LatestPeriod,
		Source:    "cnequity:financial_statement_items（历史增速）+ 模型参数表",
		Note:      fair.GrowthNote,
	})
	if fair.ImpliedGrowth != 0 {
		out = append(out, Evidence{
			Dimension: string(DimMargin),
			Metric:    "当前价格隐含的成长预期",
			Value:     fmt.Sprintf("%.1f%%", fair.ImpliedGrowth*100),
			Raw:       fair.ImpliedGrowth,
			AsOf:      fmt.Sprintf("%d", input.PriceSeries.LastDate()),
			Source:    "反向 DCF（用当前 PE 反解）",
			Note:      fair.GrowthNote,
		})
	}
	out = append(out, Evidence{
		Dimension: string(DimMargin),
		Metric:    "折现率（WACC）与稳态增长",
		Value:     fmt.Sprintf("%.1f%% / %.1f%%", input.Params.WACC*100, input.Params.TerminalGrowth*100),
		Source:    "模型参数（可配置，需历史验证校准）",
		Note:      "本机没有有息负债与折旧摊销科目，DCF 与 EV/EBITDA 都是简化口径",
	})
	out = append(out, Evidence{
		Dimension: string(DimMargin),
		Metric:    "生效股本",
		Value:     fmt.Sprintf("%.2f 亿股", input.Shares/1e8),
		Raw:       input.Shares,
		AsOf:      fmt.Sprintf("%d", input.PriceSeries.LastDate()),
		Source:    "cnequity:share_structure（按变更日取截至评估日生效值）",
		Note:      "历史市值 = 不复权收盘价 × 当期股本；复权价只用于收益与波动",
	})
	return out
}

func joinItems(items []string, limit int) string {
	labels := make([]string, 0, limit)
	for i, item := range items {
		if i >= limit {
			labels = append(labels, "…")
			break
		}
		labels = append(labels, ItemLabel(item))
	}
	return joinStrings(labels, "、")
}
