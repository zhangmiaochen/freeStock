package valuation

import (
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// 多期限风险收益评估（方案 §6）
//
// ## 为什么五档必须独立计算
//
// 方案反复强调"评估期限设置为 1/3/6/12/36 个月，分别独立计算，不允许简单复制
// 同一个评分"。本文件用**三重机制**保证这一点，而不是靠"代码里写了五遍"：
//
//  1. **向合理价值收敛的速度不同**（convergenceFor）：1 个月只看 12% 的收敛，
//     36 个月看 100%。所以同一家公司、同一个合理价值，五档的"基准目标价"必然不同。
//  2. **成长假设不同**（fairvalue.go 的 projectGrowth）：期限越长，增速越向
//     稳态回归、衰减越强。
//  3. **维度权重与情景概率不同**（config.go 的 HorizonWeights / ScenarioProb）。
//
// 这三者叠加的结果是五档评分几乎不可能相同，`TestHorizonsAreNotCopies` 会钉住它。
//
// ## 三种价格的层次（容易混淆，必须分清）
//
//	当前价格        今天真实成交价
//	合理价值区间    成长调整后算出的"值多少钱"（fairvalue.go）
//	目标价格        该期限到期时**预期成交价** —— 由当前价向合理价值部分收敛得到
//
// 目标价格 ≠ 合理价值：1 个月到期时价格通常还没走到合理价值。把合理价值
// 直接当短期目标价，会得出"所有便宜股票一个月都能涨到位"的荒谬结论
// （方案 §6.1 明确禁止）。
// ---------------------------------------------------------------------------

// Scenario 是一档期限下的一个情景。
type Scenario struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Probability 是概率（0~1）。
	Probability float64 `json:"probability"`
	// TargetPrice 是该情景下的目标价格。
	TargetPrice float64 `json:"targetPrice"`
	// Return 是该情景的收益率（含分红）。
	Return float64 `json:"return"`
	// Assumption 是该情景的关键假设说明。
	Assumption string `json:"assumption"`
}

// Outlook 是一档期限的完整评估结果。
type Outlook struct {
	// 四段独立结果，不是同一个分数的切片。
	Horizon        Horizon `json:"horizon"`
	ExpectedReturn float64 `json:"expectedReturn"`
	// PessimisticLoss 是悲观情景的损失（正数表示亏损幅度）。
	PessimisticLoss float64 `json:"pessimisticLoss"`
	// ReturnRiskRatio 是预期收益 ÷ 悲观损失。
	ReturnRiskRatio float64 `json:"returnRiskRatio"`
	// DividendYield 是期限内预期分红收益。
	DividendYield float64 `json:"dividendYield"`

	// Scenarios 是悲观/基准/乐观三情景。
	Scenarios []Scenario `json:"scenarios"`

	// Convergence 是这档期限向合理价值收敛的比例（机制说明，可追溯）。
	Convergence float64 `json:"convergence"`
	// TargetPrice 是基准情景的目标价格。
	TargetPrice float64 `json:"targetPrice"`
	// ExpectedVolatility 是该期限内的预期波动幅度（年化波动率 × √期限）。
	ExpectedVolatility float64 `json:"expectedVolatility"`

	// 该项为该期限特有的说明（界面展示，可追溯）。
	FocusNote string `json:"focusNote"`

	// OptimismDependence 是"乐观依赖度"：若不发生乐观情景，得分会损失多少比例。
	// 用于方案 §6.2 的"避免模型因过度乐观而给出虚高性价比评分"。
	OptimismDependence float64 `json:"optimismDependence"`

	// Available 表示该期限是否算出结果。
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// horizonFocus 是每档期限的侧重点（方案 §6.1 原文的落地）。
//
// 这不只是文案：下面的收敛速度、预测跨度、分红计入方式都按这些侧重点设计。
var horizonFocus = map[string]string{
	"1m": "关注当前估值位置、近期已知风险、盈利预期变化与不确定性；" +
		"**不以短期涨跌幅作为性价比依据**（本档的收益估计只反映估值位置与波动，不含动量）",
	"3m": "关注近期基本面变化、行业估值环境、盈利预期变化与潜在估值调整风险；" +
		"包含一次季报披露窗口",
	"6m": "关注盈利兑现、成长预期变化、行业相对估值及估值修复空间；" +
		"包含两次季报，成长假设开始有兑现验证",
	"12m": "重点考察未来一年盈利增长、合理估值变化、分红与潜在风险；" +
		"一个完整财报年度，成长与估值的双重作用",
	"36m": "重点考察长期成长空间、竞争优势、资本回报、现金流、合理终值与长期风险；" +
		"本轮开始引入 DCF 与稳态估值，短期市场估值不再外推",
}

// convergenceFor 返回该期限"向合理价值收敛"的比例。
//
// 这就是"五档独立"的第一重机制。数值依据（不是随手填的）：
//
//   - 1 个月（21 交易日）：估值修复需要催化剂（业绩、政策、资金），一个月内
//     通常只走完一小段，取 12%；
//   - 3 个月：一个季报窗口，取 25%；
//   - 6 个月：两个季报窗口，取 45%；
//   - 12 个月：一个完整财报年度，估值与企业盈利完成一次重定价，取 75%；
//   - 36 个月：取 100%（长期视角下价格终将反映价值，也是"长期合理价值"
//     这个概念的应有之义）。
//
// ⚠ 反向（合理价值低于现价时）用**更小的收敛比例**：估值向下修正往往比
// 向上修复更快（利空兑现快、利多需要验证），所以下行用 1.25 倍收敛。
func convergenceFor(key string) float64 {
	switch key {
	case "1m":
		return 0.12
	case "3m":
		return 0.25
	case "6m":
		return 0.45
	case "12m":
		return 0.75
	case "36m":
		return 1.00
	}
	return 0.50
}

// EvaluateHorizon 算出一档期限的风险收益。
//
// fair 是成长调整后的合理价值（fairvalue.go 的输出）；
// price 是当前价格；vol 是年化波动率；dividendYield 是期限内预期分红收益率。
func EvaluateHorizon(key string, fair FairValue, price, volatility, dividendYield float64, params Params) Outlook {
	horizon, ok := HorizonByKey(key)
	out := Outlook{Horizon: horizon, Available: false}
	if !ok {
		out.Note = fmt.Sprintf("未知期限 %s", key)
		return out
	}
	out.FocusNote = horizonFocus[key]
	out.Convergence = convergenceFor(key)
	if price <= 0 || !fair.Available {
		out.Note = "缺少当前价格或合理价值，无法做该期限的收益情景分析"
		return out
	}
	out.Available = true

	// ---- 向合理价值收敛，得到基准目标价 ----
	convergence := out.Convergence
	if fair.Base < price {
		// 下行收敛更快（见 convergenceFor 的说明），但上限不超过 1
		convergence = math.Min(convergence*1.25, 1.0)
	}
	out.TargetPrice = price + (fair.Base-price)*convergence

	// ---- 该期限内的波动幅度 ----
	// sqrt(T)：波动率随时间按平方根律扩散。期限长度用 TradingDays/242 年化。
	years := float64(horizon.TradingDays) / tradingDaysPerYear
	out.ExpectedVolatility = volatility * math.Sqrt(years)

	// ---- 三情景 ----
	prob := params.ScenarioProbFor(key)
	// 悲观/乐观的偏移幅度：
	//   悲观 = 目标价 × (1 − 1.0×σ_T)，但不低于合理价值悲观情景
	//   乐观 = 目标价 × (1 + 1.0×σ_T)，但覆盖合理价值乐观情景
	//
	// 为什么同时用"合理价值情景"与"波动率情景"两个来源取更极端的那个：
	// 只靠波动率会漏掉基本面悲观（盈利下滑导致价值下移），
	// 只靠价值情景会漏掉市场情绪波动（好公司也可能先跌 30%）。
	pessimistic := math.Min(out.TargetPrice*(1-out.ExpectedVolatility), fair.Pessimistic)
	optimistic := math.Max(out.TargetPrice*(1+out.ExpectedVolatility), fair.Optimistic)
	// 悲观价不能为负
	if pessimistic < 0 {
		pessimistic = 0
	}

	base := Scenario{
		Key: "base", Label: "基准", Probability: prob[1],
		TargetPrice: out.TargetPrice,
		Return:      out.TargetPrice/price - 1 + dividendYield,
		Assumption: fmt.Sprintf("价格向合理价值收敛 %.0f%%（当前 %.2f 元 → 基准合理价值 %.2f 元）；"+
			"盈利按模型预测增速推进，估值维持当前水平不变",
			convergence*100, price, fair.Base),
	}
	pessimisticScenario := Scenario{
		Key: "pessimistic", Label: "悲观", Probability: prob[0],
		TargetPrice: pessimistic,
		Return:      pessimistic/price - 1 + dividendYield,
		Assumption: fmt.Sprintf("盈利不及预期叠加估值下修：价格下跌 %.0f%%（该期限年化波动 %.0f%% × √期限），"+
			"并下探到合理价值悲观情景 %.2f 元", out.ExpectedVolatility*100, volatility*100, fair.Pessimistic),
	}
	optimisticScenario := Scenario{
		Key: "optimistic", Label: "乐观", Probability: prob[2],
		TargetPrice: optimistic,
		Return:      optimistic/price - 1 + dividendYield,
		Assumption: fmt.Sprintf("盈利超预期叠加估值上修：价格上涨 %.0f%%，"+
			"并触及合理价值乐观情景 %.2f 元", out.ExpectedVolatility*100, fair.Optimistic),
	}
	out.Scenarios = []Scenario{pessimisticScenario, base, optimisticScenario}

	// ---- 概率加权预期收益 ----
	out.ExpectedReturn = 0
	for _, scenario := range out.Scenarios {
		out.ExpectedReturn += scenario.Probability * scenario.Return
	}
	out.DividendYield = dividendYield
	if pessimisticScenario.Return < 0 {
		out.PessimisticLoss = -pessimisticScenario.Return
	}
	// 悲观损失的下限 5%：任何期限下"最坏情况零亏损"都不成立 ——
	// 即使价格远低于合理价值，季报不及预期、行业政策、大盘系统性下跌都会
	// 造成实际亏损。实测第一版出现过"悲观损失 0.0%"（价格已经在模型认为的
	// 乐观价值之下），那会让风险收益比变成无穷大、风险约束永远触发不了。
	// 方案 §6 要求"保留悲观情景损失"并据此判断风险收益匹配，所以必须有下限。
	const minPessimisticLoss = 0.05
	if out.PessimisticLoss < minPessimisticLoss {
		out.PessimisticLoss = minPessimisticLoss
	}
	if out.PessimisticLoss > 0 {
		out.ReturnRiskRatio = out.ExpectedReturn / out.PessimisticLoss
	}

	// ---- 乐观依赖度 ----
	// 定义：把乐观情景概率置零并按剩余概率重新归一后，预期收益相对原值的下降幅度。
	// 这个指标回答"这个期限的好结论有多少是靠乐观情景撑起来的"。
	if optimisticScenario.Probability > 0 && out.ExpectedReturn != 0 {
		rest := 1 - optimisticScenario.Probability
		if rest > 0 {
			withoutOptimism := (pessimisticScenario.Probability*pessimisticScenario.Return +
				base.Probability*base.Return) / rest
			out.OptimismDependence = math.Abs(out.ExpectedReturn-withoutOptimism) / math.Abs(out.ExpectedReturn)
		}
	}
	return out
}

// dividendYieldInWindow 计算期限内预期分红收益率。
//
// actions 是评估日之后、期限之内的除权记录（由服务层从 corporate_actions 取）。
// 口径：Σ(每股派息) ÷ 当前价格 —— **不假设分红再投入**，因为再投入的收益
// 已经隐含在除权后的价格路径里，重复计入会高估收益。
func dividendYieldInWindow(dividendsPerShare []float64, price float64) float64 {
	if price <= 0 {
		return 0
	}
	total := 0.0
	for _, dividend := range dividendsPerShare {
		if dividend > 0 {
			total += dividend
		}
	}
	return total / price
}

// FutureDividends 估算评估日之后该期限内的每股分红。
//
// 数据来源限制（必须如实说明）：本机 corporate_actions 只覆盖 2023~2026 年，
// 且**只有已实施的除权记录**，没有未来分红计划。所以这里的做法是
// "用过去 12 个月的实际每股派息做年化，再乘以期限年数" —— 这是一个**估算**，
// 不是已知的现金流，界面上必须写明。
func FutureDividends(paidLast12M float64, horizonDays int) float64 {
	if paidLast12M <= 0 {
		return 0
	}
	years := float64(horizonDays) / tradingDaysPerYear
	return paidLast12M * years
}
