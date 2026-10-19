// Package chanlun 实现缠论（缠中说禅）结构分析引擎。
//
// 设计原则（对应 docs/缠论功能开发方案.md）：
//
//  1. **独立可复用**：本包不依赖 service / config / 网络，只吃 K 线、吐结构。
//     个股 K 线图与条件选股都只消费它的输出，不允许在页面或选股条件里写死缠论逻辑。
//  2. **级别独立**：每个级别都用**自己的** K 线序列独立算分型 → 笔 → 线段 → 中枢 →
//     走势类型 → 背驰 → 买卖点；大级别只用于定方向与印证，**不能**反推小级别的画线。
//  3. **无未来数据**：所有结构在"确认"之后才进入结构序列（分型等第三根、笔等分型确认、
//     线段等反向破坏、中枢等第三段、背驰等离开段走完），信号记录形成/确认时间，
//     历史信号不会被后续走势改写（见 replay.go 的审计）。
//  4. **先判状态再出信号**：走势类型与当前状态先确定，再按方案 §7.3 的映射表出买卖点。
//  5. **无中枢不背驰**：没有中枢就不输出趋势背驰，也不输出三类买卖点。
//
// 本包的输出全部带 JSON 标签，直接经 Wails 绑定给前端；金额/价格保留 4 位小数。
package chanlun

// Level 是分析级别。
//
// 注意：`day-stroke`（日线笔）不是 K 线周期，而是**只有日 K 时允许的最小级别** ——
// 它的"次级别走势"就是日线笔本身。方案 §3.2 明确要求允许配置最小可用级别。
type Level string

const (
	// LevelDayStroke 日线笔：只有日 K 数据时的最小级别。
	LevelDayStroke Level = "day-stroke"
	// LevelDay 日线。
	LevelDay Level = "day"
	// LevelWeek 周线。
	LevelWeek Level = "week"
	// LevelMonth 月线。
	LevelMonth Level = "month"
	// LevelYear 年线。
	LevelYear Level = "year"
)

// LevelOrder 是从小到大的级别顺序（下标越大级别越高）。
//
// ⚠ 级别顺序不是"周期"顺序：`day-stroke` 比 `day` 更小，因为它是日线内部的次级别。
var LevelOrder = []Level{LevelDayStroke, LevelDay, LevelWeek, LevelMonth, LevelYear}

// LevelRank 返回级别的高低排序（越大越高）。
func LevelRank(level Level) int {
	for i, item := range LevelOrder {
		if item == level {
			return i
		}
	}
	return 0
}

// ValidLevel 判断级别是否合法。
func ValidLevel(level Level) bool {
	for _, item := range LevelOrder {
		if item == level {
			return true
		}
	}
	return false
}

// LevelCN 返回级别中文名。
func LevelCN(level Level) string {
	switch level {
	case LevelDayStroke:
		return "日线笔"
	case LevelDay:
		return "日线"
	case LevelWeek:
		return "周线"
	case LevelMonth:
		return "月线"
	case LevelYear:
		return "年线"
	}
	return string(level)
}

// ParentLevel 返回上一级（更大）级别；已到最大时返回空。
func ParentLevel(level Level) Level {
	switch level {
	case LevelDayStroke:
		return LevelDay
	case LevelDay:
		return LevelWeek
	case LevelWeek:
		return LevelMonth
	case LevelMonth:
		return LevelYear
	}
	return ""
}

// Disclaimer 是所有输出必须携带的风险提示（方案 §4.4 / §10.4 / §12.3）。
const Disclaimer = "仅作参考，不构成投资建议"

// ---------------------------------------------------------------------------
// 输入
// ---------------------------------------------------------------------------

// Bar 是一根 K 线。Date 用 YYYYMMDD 整数，与项目其余部分一致。
type Bar struct {
	Date   int     `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	Amount float64 `json:"amount"`
}

// MergedBarInfo 是包含处理后的合并 K 线（可追溯，方案 §5.1）。
type MergedBarInfo struct {
	Index      int     `json:"index"`
	StartIndex int     `json:"startIndex"` // 原始下标区间（含）
	EndIndex   int     `json:"endIndex"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	HighIndex  int     `json:"highIndex"` // 最高价所在原始下标
	LowIndex   int     `json:"lowIndex"`  // 最低价所在原始下标
	HighDate   int     `json:"highDate"`
	LowDate    int     `json:"lowDate"`
	Date       int     `json:"date"`      // 合并段最后一根原始 K 线的日期
	Direction  int     `json:"direction"` // +1 向上 / -1 向下 / 0 未定
	Members    int     `json:"members"`   // 合并了多少根原始 K 线
}

// ---------------------------------------------------------------------------
// 分型 / 笔 / 线段
// ---------------------------------------------------------------------------

// FractalType 分型类型。
type FractalType string

const (
	FractalTop    FractalType = "top"
	FractalBottom FractalType = "bottom"
)

// Fractal 是顶/底分型。
//
// `Date` 是**形成时间**（极值那根 K 线），`ConfirmDate` 是**确认时间**
// （第三根 K 线，方案 §5.2 要求必须等后续 K 线确认）。
type Fractal struct {
	Date         int         `json:"date"`
	Type         FractalType `json:"type"`
	Price        float64     `json:"price"`
	Index        int         `json:"index"`       // 极值所在的原始 K 线下标
	MergedIndex  int         `json:"mergedIndex"` // 所在合并 K 线下标
	ConfirmDate  int         `json:"confirmDate"`
	ConfirmIndex int         `json:"confirmIndex"`
	Level        Level       `json:"level"`
}

// Stroke 是一笔（方案 §5.3）。
//
// 包含处理后两个相邻反向分型之间至少 5 根合并 K 线；`Confirmed=false` 表示
// 最后一笔还在延伸（尚未出现反向分型确认），画图时要连到最新价。
type Stroke struct {
	Index       int     `json:"index"`
	Direction   int     `json:"direction"` // +1 向上 / -1 向下
	StartDate   int     `json:"startDate"`
	EndDate     int     `json:"endDate"`
	StartPrice  float64 `json:"startPrice"`
	EndPrice    float64 `json:"endPrice"`
	StartIndex  int     `json:"startIndex"`
	EndIndex    int     `json:"endIndex"`
	StartMerged int     `json:"startMerged"`
	EndMerged   int     `json:"endMerged"`
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	ConfirmDate int     `json:"confirmDate"`
	Confirmed   bool    `json:"confirmed"`
	Level       Level   `json:"level"`
	Note        string  `json:"note"`
}

// Segment 是一条线段（方案 §5.4）。
//
// 由笔构成；必须等待反向线段破坏确认，所以最后一根常为 `Confirmed=false`。
type Segment struct {
	Index       int     `json:"index"`
	Direction   int     `json:"direction"`
	StartDate   int     `json:"startDate"`
	EndDate     int     `json:"endDate"`
	StartPrice  float64 `json:"startPrice"`
	EndPrice    float64 `json:"endPrice"`
	StartIndex  int     `json:"startIndex"`
	EndIndex    int     `json:"endIndex"`
	StartStroke int     `json:"startStroke"`
	EndStroke   int     `json:"endStroke"`
	BreakDate   int     `json:"breakDate"` // 反向破坏的确认时间
	Confirmed   bool    `json:"confirmed"`
	Level       Level   `json:"level"`
}

// ---------------------------------------------------------------------------
// 中枢 / 走势类型
// ---------------------------------------------------------------------------

// PivotStatus 中枢状态（方案 §5.5）。
type PivotStatus string

const (
	PivotForming   PivotStatus = "forming"   // 刚形成（3 段重叠）
	PivotExtension PivotStatus = "extension" // 延伸
	PivotExpanded  PivotStatus = "expanded"  // 扩展（≥9 段仍重叠）
	PivotNew       PivotStatus = "new"       // 新生（离开前中枢后重新形成）
)

// Pivot 是一个中枢。
//
// ZG = 三段重叠区上沿，ZD = 下沿（方案 §5.5）。`LeaveStroke` 为 -1 表示
// 价格还在中枢里、尚未离开。
type Pivot struct {
	Index       int         `json:"index"`
	Level       Level       `json:"level"`
	ZG          float64     `json:"zg"`
	ZD          float64     `json:"zd"`
	StartDate   int         `json:"startDate"`
	EndDate     int         `json:"endDate"`
	StartIndex  int         `json:"startIndex"`
	EndIndex    int         `json:"endIndex"`
	StartStroke int         `json:"startStroke"`
	EndStroke   int         `json:"endStroke"`
	StrokeCount int         `json:"strokeCount"`
	Status      PivotStatus `json:"status"`
	EnterStroke int         `json:"enterStroke"` // 进入中枢的那一段（-1 表示数据起点不足）
	LeaveStroke int         `json:"leaveStroke"` // 离开中枢的那一段（-1 表示还在中枢内）
	ConfirmDate int         `json:"confirmDate"`
	Confirmed   bool        `json:"confirmed"`
	Note        string      `json:"note"`
}

// TrendType 走势类型（方案 §5.6：只有盘整、上涨趋势、下跌趋势）。
type TrendType string

const (
	// TrendNone 无中枢 —— 视为走势未完成，不给三类买卖点。
	TrendNone TrendType = "none"
	// TrendConsolidation 盘整（只有一个中枢，或多个中枢互相重叠）。
	TrendConsolidation TrendType = "consolidation"
	// TrendUp 上涨趋势（至少两个同向、不重叠且依次抬高的中枢）。
	TrendUp TrendType = "up"
	// TrendDown 下跌趋势。
	TrendDown TrendType = "down"
)

// TrendStatus 是走势的**生命周期**状态。
//
// "走势终完美" 是缠论的公理：任何走势类型最终都会走完自己的结构，然后转化为
// 另外两种类型之一。系统不能只给一个静态的"当前类型"，还要说清楚它走到哪一步了：
//
//	形成中 —— 结构还不完整（例如中枢尚未确认），谈不上"完成"；
//	延伸中 —— 结构已成立但**还没出现完成条件**（最后一个中枢仍在、未被反向破坏）；
//	已完成 —— 反向走势破坏了该走势的最后一个中枢，这一段走势结束了。
//
// 注意：**"完成"不等于"反转"**。趋势完成后至少转化为盘整，是否走成反向趋势要另看，
// 这由 Trend.Outlook（完全分类）给出三种可能。
type TrendStatus string

const (
	TrendForming   TrendStatus = "forming"   // 形成中
	TrendExtending TrendStatus = "extending" // 延伸中（结构成立、尚未完成）
	TrendCompleted TrendStatus = "completed" // 已完成（最后一个中枢被反向破坏）
)

// ScenarioStatus 是"完全分类"里某一种后续可能的状态。
type ScenarioStatus string

const (
	ScenarioActive      ScenarioStatus = "active"      // 正在发生
	ScenarioPaused      ScenarioStatus = "paused"      // 暂停（价格回到中枢，趋势未破但也没在延续）
	ScenarioPending     ScenarioStatus = "pending"     // 尚未发生（条件未满足）
	ScenarioConfirmed   ScenarioStatus = "confirmed"   // 已经发生
	ScenarioInvalidated ScenarioStatus = "invalidated" // 已被走势排除
)

// Scenario 是"接下来只有这几种可能"的一种（不预测，只应对）。
//
// 系统不判断"未来一定涨/跌"，而是把走势**完全分类**成有限几种可能，
// 每一种都给出"成立/失效的结构条件"和"现在走到哪一步"，由使用者决定怎么应对。
type Scenario struct {
	Key       string         `json:"key"` // continuation / consolidation / reversal / leave-up / leave-down
	Label     string         `json:"label"`
	Condition string         `json:"condition"`
	Status    ScenarioStatus `json:"status"`
	StatusCN  string         `json:"statusCN"`
	Date      int            `json:"date"` // 状态变成"已确认/已发生"的时间
	Note      string         `json:"note"`
}

// Trend 是当前走势类型。
type Trend struct {
	Type       TrendType `json:"type"`
	TypeCN     string    `json:"typeCN"`
	Level      Level     `json:"level"`
	StartDate  int       `json:"startDate"`
	EndDate    int       `json:"endDate"`
	PivotIndex []int     `json:"pivotIndex"` // 参与判定的中枢下标

	// 生命周期（走势终完美）
	Status           TrendStatus `json:"status"`
	StatusCN         string      `json:"statusCN"`
	CompletedDate    int         `json:"completedDate"`
	CompletionReason string      `json:"completionReason"`
	// CompletedBy 是完成方式：breakdown / breakup / leave-up / leave-down
	CompletedBy string `json:"completedBy"`

	// Outlook 是当前走势的完全分类（三种可能 + 各自状态）
	Outlook []Scenario `json:"outlook"`

	Note string `json:"note"`
}

// ScenarioStatusCN 返回后续可能状态的中文名。
func ScenarioStatusCN(status ScenarioStatus) string {
	switch status {
	case ScenarioActive:
		return "进行中"
	case ScenarioPaused:
		return "暂停"
	case ScenarioPending:
		return "待发生"
	case ScenarioConfirmed:
		return "已确认"
	case ScenarioInvalidated:
		return "已排除"
	}
	return string(status)
}

// TrendStatusCN 返回走势生命周期状态的中文名。
func TrendStatusCN(status TrendStatus) string {
	switch status {
	case TrendForming:
		return "形成中"
	case TrendExtending:
		return "延伸中"
	case TrendCompleted:
		return "已完成"
	}
	return string(status)
}

// PositionPhase 当前在中枢结构中的位置（方案 §9.3）。
type PositionPhase string

const (
	PhaseUnknown   PositionPhase = "unknown"
	PhaseBefore    PositionPhase = "pivot-before" // 中枢前（尚未形成中枢）
	PhaseInside    PositionPhase = "pivot-inside" // 中枢中
	PhaseAfter     PositionPhase = "pivot-after"  // 中枢后
	PhaseDeparture PositionPhase = "departure"    // 离开段
	PhasePullback  PositionPhase = "pullback"     // 回抽段
)

// Position 描述"当前在更大级别/本级别结构中的位置"。
type Position struct {
	Phase         PositionPhase `json:"phase"`
	PhaseCN       string        `json:"phaseCN"`
	PivotIndex    int           `json:"pivotIndex"`
	BarsFromPivot int           `json:"barsFromPivot"`
	Note          string        `json:"note"`
}

// ---------------------------------------------------------------------------
// 背驰
// ---------------------------------------------------------------------------

// DimScore 是背驰力度的一个维度（A/B 两段的原始比值与权重）。
type DimScore struct {
	Name   string  `json:"name"`
	A      float64 `json:"a"`
	B      float64 `json:"b"`
	Ratio  float64 `json:"ratio"` // B / A
	Weight float64 `json:"weight"`
	Note   string  `json:"note"`
}

// Divergence 是一次背驰判定（方案 §6）。
type Divergence struct {
	Index      int    `json:"index"`
	Date       int    `json:"date"`
	Kind       string `json:"kind"` // trend（趋势背驰）/ consolidation（盘整背驰）
	KindCN     string `json:"kindCN"`
	Direction  string `json:"direction"` // top（顶背驰）/ bottom（底背驰）
	Level      Level  `json:"level"`
	PivotIndex int    `json:"pivotIndex"`
	LegAStart  int    `json:"legAStart"`
	LegAEnd    int    `json:"legAEnd"`
	LegBStart  int    `json:"legBStart"`
	LegBEnd    int    `json:"legBEnd"`
	// 两段的起止日期：前端按**日期**映射下标，不依赖引擎内部的 K 线下标
	LegAStartDate int        `json:"legAStartDate"`
	LegAEndDate   int        `json:"legAEndDate"`
	LegBStartDate int        `json:"legBStartDate"`
	LegBEndDate   int        `json:"legBEndDate"`
	StrengthA     float64    `json:"strengthA"`
	StrengthB     float64    `json:"strengthB"`
	Ratio         float64    `json:"ratio"`
	Dims          []DimScore `json:"dims"`
	NewExtreme    bool       `json:"newExtreme"`
	Confirmed     bool       `json:"confirmed"`

	// Outcomes 是背驰后的**三种可能**及各自状态。
	//
	// 背驰只代表"原趋势力度衰竭"，它**不保证反转**，也不保证一定上涨/下跌；
	// 缠论给的保证只有一句：至少回到最后一个中枢附近。之后有三种走向：
	// 反转 / 盘整 / 背驰失败（继续原趋势）。这里把三种可能都列出来并标注
	// "现在走到了哪一步"，而不是把背驰直接当成买卖信号。
	Outcomes  []DivergenceOutcome `json:"outcomes"`
	Outcome   string              `json:"outcome"` // 已确认的分支 key；未定为 pending
	OutcomeCN string              `json:"outcomeCN"`

	Note string `json:"note"`
}

// DivergenceOutcome 是背驰三种可能之一。
type DivergenceOutcome struct {
	Key       string         `json:"key"` // reversal / consolidation / failure
	Label     string         `json:"label"`
	Condition string         `json:"condition"`
	Status    ScenarioStatus `json:"status"`
	StatusCN  string         `json:"statusCN"`
	Date      int            `json:"date"`
}

// ---------------------------------------------------------------------------
// 买卖点信号
// ---------------------------------------------------------------------------

// SignalType 买卖点类型。
type SignalType string

const (
	SignalBuy1  SignalType = "buy1"
	SignalBuy2  SignalType = "buy2"
	SignalBuy3  SignalType = "buy3"
	SignalSell1 SignalType = "sell1"
	SignalSell2 SignalType = "sell2"
	SignalSell3 SignalType = "sell3"
)

// SignalState 状态机状态（方案 §7.2）。
type SignalState string

const (
	StateCandidate SignalState = "candidate" // 候选
	StateTriggered SignalState = "triggered" // 触发
	StateConfirmed SignalState = "confirmed" // 确认
	StateInvalid   SignalState = "invalid"   // 失效
	StateExpired   SignalState = "expired"   // 过期
)

// SignalTypeCN 返回类型中文名。
func SignalTypeCN(t SignalType) string {
	switch t {
	case SignalBuy1:
		return "一买"
	case SignalBuy2:
		return "二买"
	case SignalBuy3:
		return "三买"
	case SignalSell1:
		return "一卖"
	case SignalSell2:
		return "二卖"
	case SignalSell3:
		return "三卖"
	}
	return string(t)
}

// SignalDirection 返回方向（buy / sell）。
func SignalDirection(t SignalType) string {
	switch t {
	case SignalBuy1, SignalBuy2, SignalBuy3:
		return "buy"
	}
	return "sell"
}

// StateCN 返回状态中文名。
func StateCN(s SignalState) string {
	switch s {
	case StateCandidate:
		return "候选"
	case StateTriggered:
		return "触发"
	case StateConfirmed:
		return "确认"
	case StateInvalid:
		return "失效"
	case StateExpired:
		return "过期"
	}
	return string(s)
}

// ScoreFactor 是综合分的一个因子（方案 §8.2）。
type ScoreFactor struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Note  string  `json:"note"`
}

// Signal 是一个买卖点信号，带完整状态机与依据。
type Signal struct {
	Type         SignalType    `json:"type"`
	TypeCN       string        `json:"typeCN"`
	Direction    string        `json:"direction"` // buy / sell
	Level        Level         `json:"level"`
	State        SignalState   `json:"state"`
	StateCN      string        `json:"stateCN"`
	Price        float64       `json:"price"` // 参考价（触发点价格）
	Score        float64       `json:"score"` // 综合分 0~100
	ScoreFactors []ScoreFactor `json:"scoreFactors"`

	FormDate    int `json:"formDate"`
	TriggerDate int `json:"triggerDate"`
	ConfirmDate int `json:"confirmDate"`
	InvalidDate int `json:"invalidDate"`
	ExpireDate  int `json:"expireDate"`

	FormIndex    int `json:"formIndex"`
	TriggerIndex int `json:"triggerIndex"`
	ConfirmIndex int `json:"confirmIndex"`
	InvalidIndex int `json:"invalidIndex"`
	ExpireIndex  int `json:"expireIndex"`

	PivotIndex      int `json:"pivotIndex"`
	DivergenceIndex int `json:"divergenceIndex"`

	Reasons      []string `json:"reasons"`
	Conflict     bool     `json:"conflict"`
	ConflictNote string   `json:"conflictNote"`
	Note         string   `json:"note"`
}

// ---------------------------------------------------------------------------
// 状态（先判状态再出信号，方案 §7.3）
// ---------------------------------------------------------------------------

// StateName 是"当前状态"的枚举。
type StateName string

const (
	StateInsufficient  StateName = "insufficient"            // 数据不足
	StateNoPivot       StateName = "no-pivot"                // 无中枢（走势未完成）
	StateConsolidation StateName = "consolidation"           // 盘整
	StateUpTrend       StateName = "up-trend"                // 上涨趋势
	StateDownTrend     StateName = "down-trend"              // 下跌趋势
	StateReversalSell  StateName = "reversal-candidate-sell" // 上涨趋势背驰，反转候选卖
	StateReversalBuy   StateName = "reversal-candidate-buy"  // 下跌趋势背驰，反转候选买
	StateReverseUp     StateName = "reversal-confirmed-up"   // 反转确认向上
	StateReverseDown   StateName = "reversal-confirmed-down" // 反转确认向下
	StateInvalidated   StateName = "invalid"                 // 信号失效
)

// StateInfo 是"先判状态"的结果。
type StateInfo struct {
	Name      StateName `json:"name"`
	NameCN    string    `json:"nameCN"`
	Note      string    `json:"note"`
	SinceDate int       `json:"sinceDate"`
	// Signals 是当前状态下可关注的信号类型（方案 §7.3 的映射表）
	WatchSignals []string `json:"watchSignals"`
}

// ---------------------------------------------------------------------------
// 适配性 / 数据信息
// ---------------------------------------------------------------------------

// AdaptInfo 是外部（服务层）能提供、引擎自己算不出来的输入。
type AdaptInfo struct {
	// FloatMV 流通市值（元）；0 表示未知
	FloatMV float64 `json:"floatMV"`
	// Amount 最近一个交易日的成交额（元）；0 表示未知
	Amount float64 `json:"amount"`
	// Levels 本次实际算出的级别数（含自身）
	Levels int `json:"levels"`
	// LevelTotal 数据源理论上能提供的级别数
	LevelTotal int `json:"levelTotal"`
	// HasIntraday 是否有分钟数据（决定能否做真正的区间套）
	HasIntraday bool `json:"hasIntraday"`
	// IsIndex 大盘指数：不存在"流通市值太小被单一资金操纵"的问题，
	// 适配性评分不该按个股口径去扣分
	IsIndex bool `json:"isIndex"`
}

// Adaptability 是股票适配性评分（方案 §10.5）。
type Adaptability struct {
	Score        float64  `json:"score"`
	Grade        string   `json:"grade"` // A / B / C / D
	Liquidity    float64  `json:"liquidity"`
	Amount       float64  `json:"amount"`
	History      float64  `json:"history"`
	Completeness float64  `json:"completeness"`
	Stability    float64  `json:"stability"`
	Penalty      float64  `json:"penalty"`
	Reasons      []string `json:"reasons"`
}

// DataInfo 是数据完整性/精度信息（方案 §3.2、§4.4）。
type DataInfo struct {
	Bars          int    `json:"bars"`
	StartDate     int    `json:"startDate"`
	EndDate       int    `json:"endDate"`
	MergedBars    int    `json:"mergedBars"`
	HasIntraday   bool   `json:"hasIntraday"`
	PrecisionNote string `json:"precisionNote"`
}

// ---------------------------------------------------------------------------
// 分析结果
// ---------------------------------------------------------------------------

// Analysis 是单个级别的完整分析结果，也是给前端的标注数据包。
type Analysis struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Level     Level  `json:"level"`
	LevelCN   string `json:"levelCN"`
	Adjust    string `json:"adjust"`
	TradeDate int    `json:"tradeDate"`

	Data        DataInfo        `json:"data"`
	Merged      []MergedBarInfo `json:"mergedBars"`
	Fractals    []Fractal       `json:"fractals"`
	Strokes     []Stroke        `json:"strokes"`
	Segments    []Segment       `json:"segments"`
	Pivots      []Pivot         `json:"pivots"`
	Trend       Trend           `json:"trend"`
	Position    Position        `json:"position"`
	Divergences []Divergence    `json:"divergences"`
	Signals     []Signal        `json:"signals"`
	State       StateInfo       `json:"state"`

	Adaptability Adaptability `json:"adaptability"`

	Notes      []string `json:"notes"`
	Disclaimer string   `json:"disclaimer"`
}

// LevelSummary 是多级别印证里单个级别的摘要（不重复整份标注，避免载荷过大）。
type LevelSummary struct {
	Level       Level     `json:"level"`
	LevelCN     string    `json:"levelCN"`
	Trend       TrendType `json:"trend"`
	TrendCN     string    `json:"trendCN"`
	StateName   StateName `json:"stateName"`
	StateCN     string    `json:"stateCN"`
	Pivots      int       `json:"pivots"`
	Signals     []Signal  `json:"signals"`
	HasConflict bool      `json:"hasConflict"`
	Note        string    `json:"note"`
}

// MultiLevel 是多级别印证与区间套结果（方案 §3.4、§6.5）。
type MultiLevel struct {
	Levels    []LevelSummary `json:"levels"`
	Resonance bool           `json:"resonance"`
	Nested    bool           `json:"nested"`
	Note      string         `json:"note"`
	Notes     []string       `json:"notes"`
}

// Bundle 是服务层一次性返回给前端的数据包。
type Bundle struct {
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Adjust    string      `json:"adjust"`
	TradeDate int         `json:"tradeDate"`
	Analysis  *Analysis   `json:"analysis"`
	Multi     *MultiLevel `json:"multiLevel"`

	Notes      []string `json:"notes"`
	Disclaimer string   `json:"disclaimer"`
}
