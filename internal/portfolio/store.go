// Package portfolio 实现组合管理（组合 + 持仓 + 流水）。
//
// 逻辑移植自 Python 项目 tushare-stock-tonyz 的
// portfolio_service.py / portfolio_store.py / portfolio_manager.py。
//
// 移植时做了**两处有意的偏离**，都是为了去掉原实现里的耦合，
// 但保证所有算出来的数值与原来一致：
//
//  1. **total_layers 提升为组合级字段**。原实现把它放在一个全局的
//     data/portfolio_config.json 里（默认 10），每次算权重都去读那个文件 ——
//     于是"每个组合的仓位分母"实际上是一个隐式的全局配置。这里显式建模成
//     组合字段，语义不变、数值不变，但不再有隐式耦合。
//
//  2. **股票代码用 6 位裸码**（600519），与原项目的 600519.SH 不同。
//     本项目其余部分（行情、K 线、板块）统一用裸码，混用两套写法只会制造
//     bug。基准代码保留后缀（000300.SH），因为它是指数。
//
// 还有几处**如实照搬**的反直觉之处，不要"顺手改好"：
//
//   - 数量口径是**层数**（position_layers），不是股数。1 层 = 总资金的
//     1/total_layers。股数只存在于回测引擎里。
//   - 手动新增持仓时层数被 **clamp 到 [1,2]**，尽管界面允许输入到 999。
//     （原实现如此，见 add_position）
//   - position_weight 是**百分数**（0–100），算组合收益时才 /100。
//   - 组合收益率 = Σ(逐仓收益率 × 权重)，**不含现金**，不是真金白银收益率。
//   - 实盘持仓**没有金额字段**，也没有止盈止损价/目标价/备注。
//   - 实盘成本**不含手续费**（回测的成本才含费）。
package portfolio

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，不需要 CGO
)

// ---------------------------------------------------------------------------
// 取值常量（与原项目一致）
// ---------------------------------------------------------------------------

// 组合类型。别名归一化见 NormalizeType。
const (
	TypeManual   = "manual"    // 手动组合：仅分析提醒
	TypeSemiAuto = "semi_auto" // 半自动：人工确认后执行
	TypeAuto     = "auto"      // 自动：可直接应用
)

// 组合类型对应的执行模式（原项目 portfolio_service 的三元组）。
var executionModes = map[string]struct {
	Mode                      string
	ModeLabel                 string
	AllowApplyRotation        bool
	RequireManualConfirmation bool
	ActionPrefix              string
	TypeLabel                 string
}{
	TypeManual:   {"manual_review", "仅分析提醒", false, true, "人工复核", "手动组合"},
	TypeSemiAuto: {"confirm_required", "人工确认后执行", true, true, "待确认", "半自动组合"},
	TypeAuto:     {"auto_ready", "可直接应用", true, false, "", "自动组合"},
}

// 策略 ID。参数不落库，运行时现算（见 StrategyConfig）。
const (
	StrategyTrendGrowth = "trend_growth"
	StrategyTrendLowBuy = "trend_pullback_low_buy"
	// StrategyQualityGrowth 是"质量成长 · 趋势回调"（基本面选股 + 趋势/回调择时）。
	// 见 reports/fund_strategy/开发说明_v3.md。
	StrategyQualityGrowth = "quality_growth_pullback"
	StrategyOversold      = "oversold_rebound"
	StrategyExpert        = "expert_simulation"
)

// strategyLabels 是策略 ID 的中文名。
var strategyLabels = map[string]string{
	StrategyTrendGrowth:   "趋势成长",
	StrategyTrendLowBuy:   "趋势低吸",
	StrategyQualityGrowth: "质量成长·趋势回调",
	StrategyOversold:      "超跌反弹",
	StrategyExpert:        "高手模拟",
}

// 选股范围类型。
const (
	ScopeAll         = "all"
	ScopeManualCodes = "manual_codes"
	ScopeIndustry    = "industry"
	ScopeConcept     = "concept"
)

// 组合状态。
const (
	StatusActive   = "active"
	StatusPaused   = "paused"
	StatusArchived = "archived"
)

// 持仓来源。
const (
	SourceManual = "manual"
	SourceAuto   = "auto_compose"
	SourceImport = "import_current"
	SourceSystem = "system"
)

// 仓位性质。
const (
	HoldingSwing = "swing" // 波段仓
	HoldingBase  = "base"  // 底仓
)

// RotationMode 是调仓频率的常量。原项目里这个字段是**死字段** ——
// 无论界面传什么，写入时一律是 "signal"（条件触发）。
const RotationMode = "signal"

// DefaultTotalLayers 是层数上限的默认值（原项目 DEFAULT_TOTAL_LAYERS）。
const DefaultTotalLayers = 10

// DefaultBenchmark 是基准代码的默认值（原项目同样是 000300.SH）。
const DefaultBenchmark = "000300.SH"

// defaultBenchmark 是包内使用的别名。
const defaultBenchmark = DefaultBenchmark

// NormalizeBenchmarkCode 归一化基准代码。
//
// 对应原实现 portfolio_service.py:201-207 的 `_normalize_ts_code`：
// 大写、去空格；6 位纯数字按首位补交易所后缀。
// 不补的话填 `000300` 会存成裸码，之后去行情/指数里取基准就取不到。
// NormalizeBenchmarkCode 把 6 位裸码补上交易所后缀。
//
// ⚠ **与原实现有一处刻意不同**（原实现 `_normalize_ts_code`，
// portfolio_service.py:57-62）：
//
//	if re.fullmatch(r"\d{6}", code):
//	    return f"{code}.SH" if code.startswith("6") else f"{code}.SZ"
//
// 原版是"只有 6 开头给 .SH，**其余一律 .SZ**"。这里改成：
//
//	6 / 9 → .SH      （9 开头是沪市 B 股，原版判成 .SZ 是错的）
//	0 / 3 → .SZ
//	其余  → 保持裸码  （不认识的前缀不硬猜）
//
// **对实际用途没有影响**：基准用的都是指数代码，
// `000300` / `000905` / `399006` 两边结果完全相同
// （0/3 开头 → .SZ，6 开头 → .SH）。差异只出现在
// `9xxxxx`（沪 B）与 `4xxxxx`/`8xxxxx`（北交所）这类**不会当基准**的代码上。
//
// 之所以不照抄：把沪 B 判成深市是明确的数据错误，
// 而"硬猜 .SZ"会让北交所代码指向一个不存在的深市标的。
func NormalizeBenchmarkCode(value string) string {
	code := strings.ToUpper(strings.TrimSpace(value))
	if code == "" || strings.Contains(code, ".") || len(code) != 6 {
		return code
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return code
		}
	}
	switch code[0] {
	case '6', '9':
		return code + ".SH"
	case '0', '3':
		return code + ".SZ"
	}
	return code
}

// maxManualLayers 是**手动新增持仓**时层数的上限。
//
// 原项目把它硬编码成 2（`min(2, max(1, ...))`），而界面上允许输入到 999、
// apply_rotation 的上限又是 3、低吸策略的上限是 total_layers —— 三处不一致。
// 这里照搬 add_position 的 [1,2]，因为阶段一只做手动新增。
const maxManualLayers = 2

// ---------------------------------------------------------------------------
// 模型
// ---------------------------------------------------------------------------

// Portfolio 是一个组合。
type Portfolio struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"` // manual / semi_auto / auto
	TypeLabel     string `json:"typeLabel"`
	StrategyID    string `json:"strategyId"` // trend_growth / ...
	StrategyLabel string `json:"strategyLabel"`

	BenchmarkCode string `json:"benchmarkCode"`
	Status        string `json:"status"` // active / paused / archived
	StatusLabel   string `json:"statusLabel"`

	TargetPositionCount int `json:"targetPositionCount"`
	MaxStockCount       int `json:"maxStockCount"`
	// TotalLayers 是**仓位权重的分母**。原项目放在全局 JSON 里，这里提升为组合字段。
	TotalLayers int `json:"totalLayers"`

	// RebalanceFrequency 恒为 "signal"（条件触发）
	RebalanceFrequency string `json:"rebalanceFrequency"`

	ScopeType      string `json:"scopeType"`      // all / manual_codes / industry / concept
	ScopeValueJSON string `json:"scopeValueJson"` // 见 ScopeValue
	ScopeLabel     string `json:"scopeLabel"`

	Notes string `json:"notes"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`

	// 执行模式三元组（由 Type 推导，便于界面直接用）
	ExecutionMode             string `json:"executionMode"`
	ExecutionModeLabel        string `json:"executionModeLabel"`
	AllowApplyRotation        bool   `json:"allowApplyRotation"`
	RequireManualConfirmation bool   `json:"requireManualConfirmation"`

	// 运行时汇总（查询时填充，不落库）
	PositionCount   int     `json:"positionCount"`
	UsedLayers      int     `json:"usedLayers"`
	AvailableLayers int     `json:"availableLayers"`
	PositionRatio   float64 `json:"positionRatio"` // 已用层数占比 %
}

// ScopeValue 是 scope_value_json 的结构。键随 ScopeType 变化。
type ScopeValue struct {
	Codes   []string `json:"codes,omitempty"`   // manual_codes
	Items   []string `json:"items,omitempty"`   // industry / concept
	Markets []string `json:"markets,omitempty"` // main / gem / star
}

// Position 是一条持仓。
//
// 注意：**没有股数、没有市值、没有手续费、没有止盈止损价**。
// 数量口径是层数，1 层 = 总资金的 1/totalLayers。
type Position struct {
	PortfolioID string `json:"portfolioId"`
	Code        string `json:"code"` // 6 位裸码
	Name        string `json:"name"`

	// EntryPrice 是建仓均价，**不含手续费**（与原项目的实盘口径一致）
	EntryPrice float64 `json:"entryPrice"`
	EntryDate  string  `json:"entryDate"` // YYYYMMDD

	PositionLayers int     `json:"positionLayers"`
	PositionWeight float64 `json:"positionWeight"` // 百分数 0–100

	SourceType  string `json:"sourceType"`  // manual / auto_compose / import_current / system
	HoldingType string `json:"holdingType"` // swing / base / ""
	StrategyTag string `json:"strategyTag"`
	LastAction  string `json:"lastAction"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`

	// ---- 以下为查询时按行情填充，不落库 ----
	CurrentPrice  float64 `json:"currentPrice"`  // 最新交易日收盘价；缺失为 0
	ProfitLossPct float64 `json:"profitLossPct"` // (现价/成本-1)*100；成本为 0 记 0
	PriceDate     string  `json:"priceDate"`     // 现价对应的交易日
}

// Transaction 是一条持仓流水。只有 INSERT，不修改 —— 唯一的审计账。
type Transaction struct {
	ID             string  `json:"id"`
	PortfolioID    string  `json:"portfolioId"`
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	TradeDate      string  `json:"tradeDate"`
	Action         string  `json:"action"` // buy / clear / add / keep / reduce / ...
	Price          float64 `json:"price"`
	LayersDelta    int     `json:"layersDelta"`
	WeightDelta    float64 `json:"weightDelta"`
	Reason         string  `json:"reason"`
	SourceSignalID string  `json:"sourceSignalId"`
	CreatedAt      string  `json:"createdAt"`
}

// Summary 是组合层面的汇总。
//
// 与 Python 一致：**现金不参与盈亏/收益率/权重**，收益率是持仓收益的加权和。
type Summary struct {
	PortfolioID     string  `json:"portfolioId"`
	PositionCount   int     `json:"positionCount"`
	UsedLayers      int     `json:"usedLayers"`
	AvailableLayers int     `json:"availableLayers"`
	PositionRatio   float64 `json:"positionRatio"` // 已用层数 / 总层数 * 100

	// TotalReturnPct = Σ(逐仓收益率 × 权重)，权重是百分数 /100
	TotalReturnPct float64 `json:"totalReturnPct"`
	// MaxDrawdownPct 在阶段一无数据来源（原项目取持仓的 120 日回撤），
	// 阶段三接入评分表后填充。
	MaxDrawdownPct float64 `json:"maxDrawdownPct"`
	PriceDate      string  `json:"priceDate"`
	MissingPrices  int     `json:"missingPrices"`
}

// ---------------------------------------------------------------------------
// 归一化（严格对齐原项目的 _normalize_* 规则）
// ---------------------------------------------------------------------------

// StatusLabels 是组合状态的中文标签。
//
// 原实现 portfolio_service.py:241-247 **只定义了 `active = "启用中"`**，
// `paused` / `archived` 没有中文标签。这里补上是为了界面完整，
// 属有意补充（不参与任何判定逻辑）。
var StatusLabels = map[string]string{
	StatusActive:   "启用中",
	StatusPaused:   "已暂停",
	StatusArchived: "已归档",
}

// StatusLabelOf 返回状态的中文标签。
func StatusLabelOf(status string) string {
	if label, ok := StatusLabels[status]; ok {
		return label
	}
	return status
}

// NormalizeType 归一化组合类型。无法识别 → manual。
func NormalizeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TypeManual, "手动", "手动组合", "manual_portfolio":
		return TypeManual
	case TypeSemiAuto, "半自动", "半自动组合", "semi", "semi-auto":
		return TypeSemiAuto
	case TypeAuto, "自动", "自动组合", "ai", "ai管理":
		return TypeAuto
	}
	return TypeManual
}

// strategyAliases 是策略 ID 的别名表。
//
// 1:1 对照 portfolio_strategy.py:48-76 的 `_STRATEGY_ALIASES`。
// 原实现是 `key = str(value or "").strip().lower()` 后 `_STRATEGY_ALIASES.get(key, TREND_GROWTH)`。
//
// 少这张表的话，传 `low_buy` / `超跌反弹` / `mainline` 都会被静默当成趋势成长 ——
// 候选池、ready_score、低吸专属逻辑全变，而且**没有任何提示**。
var strategyAliases = map[string]string{
	"":             StrategyTrendGrowth,
	"manual":       StrategyTrendGrowth,
	"semi_auto":    StrategyTrendGrowth,
	"auto":         StrategyTrendGrowth,
	"trend":        StrategyTrendGrowth,
	"trend_growth": StrategyTrendGrowth,
	"growth":       StrategyTrendGrowth,
	"趋势成长":         StrategyTrendGrowth,
	"趋势强势":         StrategyTrendGrowth,

	"trend_pullback_low_buy": StrategyTrendLowBuy,
	"trend_low_buy":          StrategyTrendLowBuy,
	"pullback_low_buy":       StrategyTrendLowBuy,
	"low_buy":                StrategyTrendLowBuy,
	"趋势低吸":                   StrategyTrendLowBuy,
	"低吸趋势":                   StrategyTrendLowBuy,
	"去rs低吸":                  StrategyTrendLowBuy,
	"去 rs 低吸":                StrategyTrendLowBuy,

	"quality_growth_pullback": StrategyQualityGrowth,
	"quality_growth":          StrategyQualityGrowth,
	"qgrowth":                 StrategyQualityGrowth,
	"质量成长":                   StrategyQualityGrowth,
	"趋势回调":                   StrategyQualityGrowth,
	"质量成长趋势回调":               StrategyQualityGrowth,

	"oversold":         StrategyOversold,
	"rebound":          StrategyOversold,
	"oversold_rebound": StrategyOversold,
	"超跌反弹":             StrategyOversold,
	"超跌反抽":             StrategyOversold,

	"expert":            StrategyExpert,
	"expert_simulation": StrategyExpert,
	"mainline":          StrategyExpert,
	"高手模拟":              StrategyExpert,
	"主线强度":              StrategyExpert,
}

// StrategyUsesStrength 判断策略是否依赖 stock_strength 评分表。
//
// 质量成长·趋势回调直接从数据湖取 PIT 财报 + 日线现算，不写 stock_strength，
// 因此"刷新评分/全量重算"应跳过它（否则会按低吸/成长的口径白算一遍）。
func StrategyUsesStrength(strategyID string) bool {
	return NormalizeStrategy(strategyID, "") != StrategyQualityGrowth
}

// NormalizeStrategy 归一化策略 ID。
//
// 走别名表（含中文与常见简写）；**未知值一律回退 trend_growth**
// —— 原实现是 `.get(key, STRATEGY_TREND_GROWTH)`，并不看组合类型。
// 第二个参数保留是为了兼容既有调用点。
func NormalizeStrategy(strategyID, portfolioType string) string {
	_ = portfolioType
	key := strings.ToLower(strings.TrimSpace(strategyID))
	if mapped, ok := strategyAliases[key]; ok {
		return mapped
	}
	return StrategyTrendGrowth
}

// StrategyLabel 返回策略的中文名。
func StrategyLabel(strategyID string) string {
	if label, ok := strategyLabels[strategyID]; ok {
		return label
	}
	return strategyLabels[StrategyTrendGrowth]
}

// NormalizeStatus 归一化状态。白名单外 → active。
func NormalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case StatusActive, StatusPaused, StatusArchived:
		return strings.ToLower(strings.TrimSpace(value))
	}
	return StatusActive
}

// NormalizeScopeType 归一化范围类型。无法识别 → all。
//
// 别名集对齐 portfolio_service.py:3644-3655，**包含复数形式**
// （`industries` / `concepts`）—— 少了它们会静默降级为"全市场"，
// 候选池与调仓建议全变而且没有任何提示。
func NormalizeScopeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ScopeAll, "all_market", "market":
		return ScopeAll
	case ScopeManualCodes, "codes", "manual":
		return ScopeManualCodes
	case ScopeIndustry, "industries":
		return ScopeIndustry
	case ScopeConcept, "concepts":
		return ScopeConcept
	}
	return ScopeAll
}

// NormalizeScopeValue 把范围内容归一化成 JSON。
//
// 与原实现对齐的两点（portfolio_service.py:3667/3679/3684-3686）：
//  1. 三个列表都**排序**后落库（原实现是 `sorted({...})`）
//  2. **三个市场全选时也照写**，不丢弃 —— 此前我会把它当成"全部市场"抹掉
func NormalizeScopeValue(scopeType string, value ScopeValue) string {
	out := ScopeValue{}
	switch scopeType {
	case ScopeManualCodes:
		out.Codes = cleanCodes(value.Codes)
	case ScopeIndustry, ScopeConcept:
		out.Items = cleanStrings(value.Items)
	}
	out.Markets = normalizeMarkets(value.Markets)
	// sorted()：代码/名称按字典序，市场按 main/gem/star 的固定序（normalizeMarkets 已保证）
	sort.Strings(out.Codes)
	sort.Strings(out.Items)
	raw, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// ParseScopeValue 解析 scope_value_json；坏数据返回空值而不是报错。
func ParseScopeValue(raw string) ScopeValue {
	var out ScopeValue
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// normalizeMarkets 归一化市场取值。别名含中文与拼音缩写。
func normalizeMarkets(values []string) []string {
	out := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, item := range values {
		var key string
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "main", "主板", "mb", "sh-main", "sz-main":
			key = "main"
		case "gem", "创业板", "cyb", "chinext":
			key = "gem"
		case "star", "科创板", "kcb":
			key = "star"
		default:
			continue
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, item := range values {
		value := strings.TrimSpace(item)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// cleanCodes 清洗股票代码：统一成 6 位裸码。
func cleanCodes(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, item := range values {
		code := BareCode(item)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

// BareCode 去掉交易所后缀：600519.SH → 600519。
func BareCode(value string) string {
	text := strings.ToUpper(strings.TrimSpace(value))
	if idx := strings.Index(text, "."); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimSpace(text)
}

// ---------------------------------------------------------------------------
// 存储
// ---------------------------------------------------------------------------

// Store 是组合库（SQLite）。
type Store struct {
	db   *sql.DB
	path string
}

// DefaultPath 返回组合库的默认路径：**与 config.json 同目录**。
//
// 组合数据量比配置大得多（流水会持续增长），所以单独一个文件，
// 不塞进 config.json。
func DefaultPath(configPath string) string {
	if configPath == "" {
		return "portfolio.db"
	}
	return filepath.Join(filepath.Dir(configPath), "portfolio.db")
}

// Open 打开（必要时创建）组合库并建表。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("组合库路径不能为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建组合库目录失败：%w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开组合库失败：%w", err)
	}
	// SQLite 单文件，并发写会互相阻塞；限制成单连接最省心
	db.SetMaxOpenConns(1)

	// ⚠ **`sql.Open` 是惰性的** —— 它不碰文件，所以永远不会失败。
	// 真正的 `SQLITE_CANTOPEN` 是**之后第一次真正用这个连接**时才冒出来的。
	// 这里主动做一次探测，让失败**在开库时就暴露**、并带上诊断信息；
	// 否则错误会飘到很远的地方（表现为"回填好好的、跑回测突然报错"）。
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接组合库失败（%s）：%w", path, err)
	}

	// **限制 journal 大小**。
	//
	// SQLite 默认用 rollback journal，大小与"本次事务改动过的页数"相当。
	// 回填对每个交易日都做 `DELETE + INSERT`（一次删/写约 7500 行），
	// 在 400MB 级的库上 journal 会涨到几百 MB —— 这正是
	// `unable to open database file (14)` 最可能的来源。
	// 设上限后，提交时会把 journal 截断到这个尺寸。
	if _, err := db.Exec("PRAGMA journal_size_limit = 67108864"); err != nil {
		slog.Warn("设置 journal_size_limit 失败", "err", err)
	}

	// **临时表放内存**。
	//
	// `temp_store` 默认是 0（=FILE）：SQLite 遇到无法用索引完成的排序/分组时，
	// 会建临时 B 树并**溢出到磁盘临时文件**。而"临时文件创建不出来"时报的
	// **正是 `unable to open database file (14)`** —— 与用户的现象吻合
	//（回测区间一年、要读十几万行评分；诊断显示 openHandles=673 完全正常，
	//  所以不是句柄耗尽，指向的只能是临时文件这一条）。
	//
	// 设成 MEMORY 后临时表只在内存里，不再碰临时文件。
	if _, err := db.Exec("PRAGMA temp_store = MEMORY"); err != nil {
		slog.Warn("设置 temp_store 失败", "err", err)
	}
	// 顺带把页缓存调大些，减少回测读十几万行时的反复换入换出
	if _, err := db.Exec("PRAGMA cache_size = -16384"); err != nil {
		slog.Warn("设置 cache_size 失败", "err", err)
	}

	store := &Store{db: db, path: path}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// 把库切到 INCREMENTAL 自动回收（一次性；已是该模式时直接跳过）。
	// 失败**不阻断开库** —— 回收是维护动作，不该让整个应用起不来。
	if err := store.ensureAutoVacuum(); err != nil {
		slog.Warn("切换组合库自动回收失败（不影响使用）", "err", err)
	}
	return store, nil
}

// IncrementalVacuum 把空闲页归还给文件系统。
//
// 只在 auto_vacuum = INCREMENTAL（=2）下有意义：那时 SQLite 会把释放的页
// 挂到 freelist 并登记在指针映射里，调用本方法才能真正缩小文件。
// 其它模式下它是空操作（返回 nil，但什么都不回收）。
//
// 回填对每个交易日做 DELETE + INSERT，是空闲页的主要来源，
// 所以在回填结束后调一次最合适。
func (s *Store) IncrementalVacuum() error {
	if s == nil || s.db == nil {
		return nil
	}
	var mode int64
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		return nil // 不是 INCREMENTAL，做了也没用
	}
	if _, err := s.db.Exec("PRAGMA incremental_vacuum"); err != nil {
		return fmt.Errorf("回收空闲页失败：%w", err)
	}
	return nil
}

// FreePages 返回 (空闲页数, 总页数)，供日志/界面判断是否需要回收。
func (s *Store) FreePages() (free int64, total int64, err error) {
	if s == nil || s.db == nil {
		return 0, 0, nil
	}
	if err = s.db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		return
	}
	err = s.db.QueryRow("PRAGMA page_count").Scan(&total)
	return
}

// ensureAutoVacuum 把库切到 INCREMENTAL 自动回收模式（**一次性**）。
//
// 为什么需要：auto_vacuum 默认是 0（NONE）—— 删除大量行后腾出的页
// **留在文件里永不归还**。实测用户的库曾到 470MB，其中 338MB 是空洞；
// 回填每跑一次就再制造一批。
//
// 切换模式**必须跟随一次 VACUUM** 才生效（SQLite 的规定），所以这里是一次性
// 的重活：只在"还不是 INCREMENTAL"时做一次，之后每次开库直接跳过。
//
// 做成"开库时自愈"而不是"让用户手动跑命令"，是因为手动步骤迟早会被忘掉，
// 而这个问题会随回填反复出现。
func (s *Store) ensureAutoVacuum() error {
	var mode int64
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode == 2 {
		return nil // 已是 INCREMENTAL
	}

	free, total, err := s.FreePages()
	if err != nil {
		return err
	}
	if total < 8192 { // < 32MB，新库没有空洞，不值得为它跑 VACUUM
		return nil
	}

	began := time.Now()
	slog.Info("组合库切换到 INCREMENTAL 自动回收（一次性）",
		"autoVacuum", mode, "freePages", free, "totalPages", total)

	if _, err := s.db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return fmt.Errorf("设置 auto_vacuum 失败：%w", err)
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("VACUUM 失败：%w", err)
	}

	after, totalAfter, _ := s.FreePages()
	slog.Info("组合库自动回收已开启",
		"freePagesBefore", free, "freePagesAfter", after,
		"totalPagesBefore", total, "totalPagesAfter", totalAfter,
		"elapsed", time.Since(began).Round(time.Millisecond))
	return nil
}

// DB 暴露底层连接，**仅供诊断与维护**（行数统计、quick_check、VACUUM、
// 复现问题时手动读写）。业务代码请用 Store 上的具体方法，
// 不要绕过它们直接写 SQL。
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close 关闭组合库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回组合库文件路径。
func (s *Store) Path() string { return s.path }

// schema 建表语句。
//
// 与原项目（DuckDB）的差异说明：
//   - 原表所有列都可空且无默认值、无外键。这里给关键列加了 NOT NULL 与默认值 ——
//     逻辑不变，但坏数据写不进去。
//   - 原表 portfolio_id 是裸 VARCHAR 没有外键。这里加了 ON DELETE CASCADE，
//     因为原项目删组合时要手动级联清理 7 张表，漏一张就留垃圾。
//   - 日期仍是 YYYYMMDD 字符串（与原项目一致），不是 DATE 类型。
const schema = `
CREATE TABLE IF NOT EXISTS portfolio (
  portfolio_id         TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  portfolio_type       TEXT NOT NULL DEFAULT 'manual',
  strategy_id          TEXT NOT NULL DEFAULT 'trend_growth',
  benchmark_code       TEXT NOT NULL DEFAULT '000300.SH',
  status               TEXT NOT NULL DEFAULT 'active',
  max_stock_count      INTEGER NOT NULL DEFAULT 5,
  target_position_count INTEGER NOT NULL DEFAULT 5,
  total_layers         INTEGER NOT NULL DEFAULT 10,
  rebalance_frequency  TEXT NOT NULL DEFAULT 'signal',
  scope_type           TEXT NOT NULL DEFAULT 'all',
  scope_value_json     TEXT NOT NULL DEFAULT '{}',
  notes                TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS portfolio_position (
  portfolio_id    TEXT NOT NULL,
  ts_code         TEXT NOT NULL,
  name            TEXT NOT NULL DEFAULT '',
  entry_price     REAL NOT NULL DEFAULT 0,
  entry_date      TEXT NOT NULL DEFAULT '',
  position_layers INTEGER NOT NULL DEFAULT 1,
  position_weight REAL NOT NULL DEFAULT 0,
  source_type     TEXT NOT NULL DEFAULT 'manual',
  holding_type    TEXT NOT NULL DEFAULT '',
  strategy_tag    TEXT NOT NULL DEFAULT '',
  last_action     TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  PRIMARY KEY (portfolio_id, ts_code),
  FOREIGN KEY (portfolio_id) REFERENCES portfolio(portfolio_id) ON DELETE CASCADE
);

-- 持仓流水：只有 INSERT，不做 UPDATE/DELETE（删组合时级联清理）。
-- 原项目这张表没有已实现盈亏 —— 已实现盈亏只存在于回测成交表。
CREATE TABLE IF NOT EXISTS portfolio_position_txn (
  txn_id            TEXT PRIMARY KEY,
  portfolio_id      TEXT NOT NULL,
  ts_code           TEXT NOT NULL,
  name              TEXT NOT NULL DEFAULT '',
  trade_date        TEXT NOT NULL DEFAULT '',
  action            TEXT NOT NULL,
  price             REAL NOT NULL DEFAULT 0,
  layers_delta      INTEGER NOT NULL DEFAULT 0,
  weight_delta      REAL NOT NULL DEFAULT 0,
  reason            TEXT NOT NULL DEFAULT '',
  source_signal_id  TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  FOREIGN KEY (portfolio_id) REFERENCES portfolio(portfolio_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_position_portfolio ON portfolio_position(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_txn_portfolio      ON portfolio_position_txn(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_txn_code           ON portfolio_position_txn(portfolio_id, ts_code);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("开启外键约束失败：%w", err)
	}
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("建表失败：%w", err)
	}
	// 评分物化表（见 strength.go）
	if err := s.migrateStrength(); err != nil {
		return err
	}
	// 回测三张表（见 backtest.go）
	if _, err := s.db.Exec(backtestSchema); err != nil {
		return fmt.Errorf("建回测表失败：%w", err)
	}
	// 四张留档表（见 snapshot.go）
	if _, err := s.db.Exec(snapshotSchema); err != nil {
		return fmt.Errorf("建留档表失败：%w", err)
	}
	// 量价信号表（见 signal.go）
	if err := s.migrateSignal(); err != nil {
		return err
	}
	return nil
}

// nowText 返回当前时间戳文本（原项目用 TIMESTAMP，这里用 RFC3339）。
func nowText() string { return time.Now().Format(time.RFC3339) }

// idSeq 保证同一纳秒内生成的 ID 也不重复。
//
// Windows 的时钟粒度较粗，紧凑循环里连续两次 time.Now() 可能返回**完全相同**
// 的纳秒值 —— 只用时间戳做 ID 会撞主键（批量应用调仓时就会撞上）。
var idSeq atomic.Uint64

// newID 生成 `{prefix}_{时间戳}_{序号}` 形式的唯一 ID。
func newID(prefix string) string {
	now := time.Now()
	return fmt.Sprintf("%s_%d_%04x", prefix, now.UnixNano(), idSeq.Add(1)&0xFFFF)
}

// newPortfolioID 生成组合 ID（格式与原项目一致：pf_{时间戳}_{后缀}）。
func newPortfolioID() string {
	return newID("pf")
}

// newTxnID 生成流水 ID。
func newTxnID() string {
	return newID("txn")
}

// NewBacktestRunID 生成回测 ID（原项目前缀是 `btr`，见 portfolio_service.py:1030）。
func NewBacktestRunID() string {
	return newID("btr")
}

// newBacktestTradeID 生成回测成交 ID。
func newBacktestTradeID() string {
	return newID("btt")
}
