// Package chanlunassist 把缠论买卖点作为「辅助层」叠加到组合轮动计划上。
//
// 它**不选股、不改评分、不改撮合**，只在既有调仓计划（score.RotationPlan）上做两件事：
//
//	买入：计划本来就要买（add / add_layer / replace_in）时，若当日有**确认的二买/三买**，
//	      按配置加 1 / 2 层；
//	卖出：持仓出现**确认的一卖/二卖/三卖**时，按配置分批分层减仓（1/1/2 层）。
//
// 设计约束：
//
//  1. **无未来函数**：信号由 Analyzer 用「截断到计划日」的日线现算（见 signals.go），
//     而不是先对全区间分析再按日期回填。
//  2. **单一职责**：只改计划行，层数变化仍然交给回测引擎的 `layerBudget` 与现金约束。
//  3. **可归因**：每次加/减层都往 Reason / DecisionState / Stats.Events 里留痕。
package chanlunassist

// Config 是缠论辅助层的开关与权重。
//
// 层数与总层数的关系：回测引擎把「1 层」定义成总资产的 1/TotalLayers，所以
// Buy3ExtraLayers=2 表示「这次建仓/加仓比原计划多买 2 层」。
// 六个层数字段**零值表示该信号不参与**；全部为零时 Sanitized() 会填入默认权重，
// 便于界面只传 `{"enable": true}` 也能按默认方案运行。
type Config struct {
	Enable bool `json:"enable"`

	// ---- 买入 ----
	// Buy2ExtraLayers / Buy3ExtraLayers 是确认二买 / 三买时额外加的层数。
	Buy2ExtraLayers int `json:"buy2ExtraLayers"`
	Buy3ExtraLayers int `json:"buy3ExtraLayers"`
	// BuyFreshBars 是买入信号的新鲜度（交易日）：确认日距计划日不超过它才算「当下有效」。
	BuyFreshBars int `json:"buyFreshBars"`

	// ---- 卖出 ----
	// Sell1Layers / Sell2Layers / Sell3Layers 是确认一卖 / 二卖 / 三卖时减掉的层数。
	Sell1Layers int `json:"sell1Layers"`
	Sell2Layers int `json:"sell2Layers"`
	Sell3Layers int `json:"sell3Layers"`
	// SellFreshBars 是卖出信号的新鲜度（交易日）。
	SellFreshBars int `json:"sellFreshBars"`

	// ---- 约束 ----
	// MaxLayersPerStock 是叠加后的单股层数上限。
	//
	// 默认 7 = 趋势低吸的常见基准建仓 5 层（10 层 / 动态目标 2 只）+ 三买最多 2 层。
	// 设得太小（比如 4）会让「基准 5 层」的票永远加不进去，实验会静默变成 no-op。
	MaxLayersPerStock int `json:"maxLayersPerStock"`
	// MinSignalScore 是最低信号综合分（0~100）；<=0 表示不限。
	MinSignalScore float64 `json:"minSignalScore"`
	// AllowTriggered 为 true 时，「触发」状态也算（默认只认「确认」）。
	AllowTriggered bool `json:"allowTriggered"`
	// AllowConflict 为 true 时，结构矛盾（同日反向信号）仍参与；默认剔除。
	AllowConflict bool `json:"allowConflict"`
	// BoostHoldings 为 true 时，**已持仓**的股票在出现新鲜确认二买/三买时也加层
	// （把 Keep 行改成 add_layer）。
	//
	// 默认 false，即只对「计划本来就要买」的行加层 —— 这是对用户原话
	// 「买入时出现 2 买 / 3 买则加层」的最小解释。打开后，持仓期间反复出现的
	// 三买会持续加仓（同一确认日只加一次，受单股上限与现金约束）。
	BoostHoldings bool `json:"boostHoldings"`
}

// DefaultConfig 返回默认方案：二买 +1、三买 +2、一卖 -1、二卖 -1、三卖 -2，新鲜度 10 个交易日。
func DefaultConfig() Config {
	return Config{
		Enable:            true,
		Buy2ExtraLayers:   1,
		Buy3ExtraLayers:   2,
		BuyFreshBars:      10,
		Sell1Layers:       1,
		Sell2Layers:       1,
		Sell3Layers:       2,
		SellFreshBars:     10,
		MaxLayersPerStock: 7,
	}
}

// allLayersZero 判断六个层数字段是否全为零。
func (c Config) allLayersZero() bool {
	return c.Buy2ExtraLayers == 0 && c.Buy3ExtraLayers == 0 &&
		c.Sell1Layers == 0 && c.Sell2Layers == 0 && c.Sell3Layers == 0
}

// Sanitized 收敛到合法值，并补上默认值；**永不报错**。
//
// 注意「零 = 关闭」的语义：只要还有任意一个层数字段非零，就保留显式的零值，
// 这样才能表达「只加层不提前卖」「只分层卖不加层」这类对照方案。
func (c Config) Sanitized() Config {
	if c.allLayersZero() {
		def := DefaultConfig()
		def.Enable = c.Enable
		c = def
	}
	if c.Buy2ExtraLayers < 0 {
		c.Buy2ExtraLayers = 0
	}
	if c.Buy3ExtraLayers < 0 {
		c.Buy3ExtraLayers = 0
	}
	if c.Sell1Layers < 0 {
		c.Sell1Layers = 0
	}
	if c.Sell2Layers < 0 {
		c.Sell2Layers = 0
	}
	if c.Sell3Layers < 0 {
		c.Sell3Layers = 0
	}
	if c.BuyFreshBars <= 0 {
		c.BuyFreshBars = 10
	}
	if c.SellFreshBars <= 0 {
		c.SellFreshBars = 10
	}
	if c.MaxLayersPerStock <= 0 {
		c.MaxLayersPerStock = 7
	}
	if c.MinSignalScore < 0 {
		c.MinSignalScore = 0
	}
	return c
}

// HasBuyRule / HasSellRule 便于调用方判断某一侧是否启用。
func (c Config) HasBuyRule() bool {
	return c.Enable && (c.Buy2ExtraLayers > 0 || c.Buy3ExtraLayers > 0)
}

func (c Config) HasSellRule() bool {
	return c.Enable && (c.Sell1Layers > 0 || c.Sell2Layers > 0 || c.Sell3Layers > 0)
}
