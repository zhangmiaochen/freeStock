package portfolio

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PricePoint 是某只股票的最新收盘价。
type PricePoint struct {
	Close float64
	Date  string // YYYYMMDD
}

// PriceSource 提供持仓现价。
//
// 定义成接口是为了让 portfolio 包**不反过来依赖 service 包**（会成环）。
//
// ⚠ **现价必须来自"最新评分日"**（原实现 portfolio_service.py:3199
// `merged["current_price"] = close_price`，取自 stock_strength_history）。
// 取"最新交易日收盘价"会导致同一程序内**两个页面打架**：
// 评分没刷到最新时，详情页显示今日浮盈、而建议页与清仓判定用评分日旧价 ——
// 用户看到 +8%，系统按 +3% 判定。原实现两处同源，永远自洽。
type PriceSource interface {
	// LatestClose 返回这些股票在**该策略最新评分日**的收盘价。
	// 评分表里没有的股票不返回（调用方按 0 处理）。
	LatestClose(codes []string, strategyID string) (map[string]PricePoint, error)
}

// PortfolioInput 是创建/更新组合的入参（对应原项目的 payload）。
//
// 必须带 json tag：Wails 按 tag 生成 TS 绑定，没有 tag 时字段名会原样变成
// PascalCase（Name / ScopeValue），与前端的 camelCase 约定不一致。
type PortfolioInput struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	StrategyID    string `json:"strategyId"`
	BenchmarkCode string `json:"benchmarkCode"`
	Status        string `json:"status"`
	// TargetPositionCount / MaxStockCount 用指针区分"未提供"与"显式传 0"。
	//
	// 原实现 portfolio_service.py:3607-3608：
	//
	//	target_count = max(1, _safe_int(payload["target_position_count"],
	//	                                _safe_int(base["target_position_count"], 5)))
	//	max_count    = max(target_count, _safe_int(payload["max_stock_count"],
	//	                                _safe_int(base["max_stock_count"], target_count)))
	//
	// `_safe_int(0, 默认)` 返回 **0**（0 是合法整数），再被 `max(1, ...)` 抬到 1。
	// 所以"显式传 0"和"字段缺失"结果不同：
	//
	//	target：未提供 → 旧值或 5；显式 0 → 1
	//	max   ：未提供 → 旧值或 target；显式 0 → target
	TargetPositionCount *int       `json:"targetPositionCount"`
	MaxStockCount       *int       `json:"maxStockCount"`
	TotalLayers         int        `json:"totalLayers"`
	ScopeType           string     `json:"scopeType"`
	ScopeValue          ScopeValue `json:"scopeValue"`
	Notes               string     `json:"notes"`
}

// PositionInput 是新增持仓的入参。
//
// Layers / Weight 用指针是为了区分"**未提供**"与"**显式传 0**" ——
// 原实现（portfolio_service.py:534-535）这两者的语义不同：
//
//	position_layers: min(2, max(1, _safe_int(payload.get("position_layers"), default_layers)))
//	  → 未提供 = default_layers；显式 0 → clamp 到 1
//	position_weight: max(0.0, _safe_float(payload.get("position_weight"), _weight_from_layers(...)))
//	  → 未提供 = 按层数换算；显式 0 → 存 0
type PositionInput struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	EntryPrice  float64  `json:"entryPrice"`
	EntryDate   string   `json:"entryDate"`
	Layers      *int     `json:"layers"`
	Weight      *float64 `json:"weight"`
	HoldingType string   `json:"holdingType"`
	StrategyTag string   `json:"strategyTag"`
	// SourceType / LastAction 可入参指定（原实现支持覆盖），
	// 留空时分别是 manual / manual_add
	SourceType string `json:"sourceType"`
	LastAction string `json:"lastAction"`
}

// ---------------------------------------------------------------------------
// 组合 CRUD
// ---------------------------------------------------------------------------

// IsSupportedAShareCode 校验是否是受支持的 A 股代码。
//
// 对应 Python 的 `is_supported_a_share_ts_code`（market_data.py:182-191）：
// 6 位数字 + **前缀白名单**。
//
// ⚠ **不能复用 `market.KeyOf`** —— 它带"首字符兜底"
// （`'4'/'8'/'9'→BSE`、`'6'→SHMain`、`'0'→SZMain`、`'3'→ChiNext`），
// 会放行原版明确拒绝的代码：
//
//	900901  沪 B 股
//	430047 / 920001  北交所
//	302xxx / 004xxx  不存在的号段
//	任意 6xxxxx
//
// 原实现（market_data.py:43-58）只认这几段：
//
//	沪市  600 / 601 / 603 / 605 / 688 / 689
//	深市  000 / 001 / 002 / 003 / 300 / 301
//
// 放行脏代码的后果：写入后所有行情查询**静默失败**，用户以为建仓成功了。
func IsSupportedAShareCode(code string) bool {
	bare := BareCode(code)
	if len(bare) != 6 {
		return false
	}
	for _, ch := range bare {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	switch bare[:3] {
	case "600", "601", "603", "605", "688", "689", // 沪市（含科创板）
		"000", "001", "002", "003", "300", "301": // 深市（含创业板）
		return true
	}
	return false
}

// NormalizePortfolioInput 是 `normalizeInput` 的导出包装（`existing` 为 nil 即"新建"）。
//
// 自动建组合（`create_auto_portfolio`）要用同一套归一化 —— 否则它落库的
// 默认值/别名解析会与手动创建不一致。
func NormalizePortfolioInput(in PortfolioInput) (Portfolio, error) {
	return normalizeInput(in, nil)
}

// normalizeInput 把入参归一化成可落库的组合。
//
// existing 非 nil 时按 Python 的 `payload.get(x) or base.get(x) or 默认` 语义
// **逐字段回落** —— 省略某个字段（传零值）会保留原值，而不是被打回默认。
//
// ⚠ 注意 Python 的 `or` 语义：
//   - 传 0 / 空串 → falsy → 回落到旧值 → 再回落到默认
//   - 传**负数** → truthy → 不回落 → 由 `max(1, ...)` 兜到 1
//
// 少了这个回落，只改个名字就会把 strategy_id 打回趋势成长、scope 清空。
func normalizeInput(in PortfolioInput, existing *Portfolio) (Portfolio, error) {
	// 按字段取"入参非零值 → 旧值 → 默认"
	pick := func(value, old string) string {
		if value != "" {
			return value
		}
		return old
	}

	var old Portfolio
	if existing != nil {
		old = *existing
	}

	name := strings.TrimSpace(pick(in.Name, old.Name))
	if name == "" {
		return Portfolio{}, errors.New("组合名称不能为空")
	}

	portfolioType := NormalizeType(pick(in.Type, old.Type))
	strategyID := NormalizeStrategy(pick(in.StrategyID, old.StrategyID), portfolioType)

	benchmark := strings.TrimSpace(pick(in.BenchmarkCode, old.BenchmarkCode))
	// 基准代码要**归一化**（原实现 :3621 走 `_normalize_ts_code`）：
	// 大写 + 6 位纯数字补 .SH/.SZ。不补的话填 `000300` 会存成裸码，
	// 之后去行情/指数里取基准就取不到。
	benchmark = NormalizeBenchmarkCode(benchmark)
	if benchmark == "" {
		benchmark = defaultBenchmark
	}

	// 目标只数（原实现 :3607）：
	//
	//	payload 有值 → max(1, 该值)          # **显式 0 → 1**
	//	payload 缺省 → max(1, 旧值 or 5)
	target := 0
	if in.TargetPositionCount != nil {
		target = *in.TargetPositionCount
	} else {
		target = old.TargetPositionCount
		if target == 0 {
			target = 5
		}
	}
	if target < 1 {
		target = 1
	}

	// 最大只数（原实现 :3608）：**下限是 target**
	//
	//	payload 有值 → max(target, 该值)     # **显式 0 → target**
	//	payload 缺省 → max(target, 旧值 or target)
	maxCount := 0
	if in.MaxStockCount != nil {
		maxCount = *in.MaxStockCount
	} else {
		maxCount = old.MaxStockCount
		if maxCount == 0 {
			maxCount = target
		}
	}
	if maxCount < target {
		maxCount = target
	}

	layers := in.TotalLayers
	if layers == 0 {
		layers = old.TotalLayers
	}
	if layers == 0 {
		layers = DefaultTotalLayers
	}
	if layers < 1 {
		layers = 1
	}

	scopeType := NormalizeScopeType(pick(in.ScopeType, old.ScopeType))

	// 范围内容：入参为空（两个数组都空且 scopeType 没变）时沿用旧 JSON
	scopeJSON := NormalizeScopeValue(scopeType, in.ScopeValue)
	if existing != nil && isEmptyScopeValue(in.ScopeValue) && scopeType == NormalizeScopeType(old.ScopeType) {
		scopeJSON = old.ScopeValueJSON
	}

	status := NormalizeStatus(pick(in.Status, old.Status))

	notes := strings.TrimSpace(in.Notes)
	if notes == "" {
		notes = old.Notes
	}

	mode := executionModes[portfolioType]
	return Portfolio{
		Name:                      name,
		Type:                      portfolioType,
		TypeLabel:                 mode.TypeLabel,
		StrategyID:                strategyID,
		StrategyLabel:             StrategyLabel(strategyID),
		BenchmarkCode:             benchmark,
		Status:                    status,
		TargetPositionCount:       target,
		MaxStockCount:             maxCount,
		TotalLayers:               layers,
		RebalanceFrequency:        RotationMode, // 恒为 signal（原项目的死字段）
		ScopeType:                 scopeType,
		ScopeValueJSON:            scopeJSON,
		Notes:                     notes,
		ExecutionMode:             mode.Mode,
		ExecutionModeLabel:        mode.ModeLabel,
		AllowApplyRotation:        mode.AllowApplyRotation,
		RequireManualConfirmation: mode.RequireManualConfirmation,
	}, nil
}

// isEmptyScopeValue 判断范围内容是否为空（用于"省略即保留原值"）。
func isEmptyScopeValue(value ScopeValue) bool {
	return len(value.Codes) == 0 && len(value.Items) == 0 && len(value.Markets) == 0
}

// Create 新建组合。
func (s *Store) Create(in PortfolioInput) (Portfolio, error) {
	item, err := normalizeInput(in, nil)
	if err != nil {
		return Portfolio{}, err
	}
	item.ID = newPortfolioID()
	now := nowText()
	item.CreatedAt = now
	item.UpdatedAt = now

	_, err = s.db.Exec(`
		INSERT INTO portfolio (
			portfolio_id, name, portfolio_type, strategy_id, benchmark_code, status,
			max_stock_count, target_position_count, total_layers, rebalance_frequency,
			scope_type, scope_value_json, notes, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.Name, item.Type, item.StrategyID, item.BenchmarkCode, item.Status,
		item.MaxStockCount, item.TargetPositionCount, item.TotalLayers, item.RebalanceFrequency,
		item.ScopeType, item.ScopeValueJSON, item.Notes, item.CreatedAt, item.UpdatedAt,
	)
	if err != nil {
		return Portfolio{}, fmt.Errorf("创建组合失败：%w", err)
	}
	return item, nil
}

// Update 更新组合。类型/策略等字段会重新归一化。
func (s *Store) Update(id string, in PortfolioInput) (Portfolio, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Portfolio{}, err
	}
	item, err := normalizeInput(in, &existing)
	if err != nil {
		return Portfolio{}, err
	}
	item.ID = existing.ID
	item.CreatedAt = existing.CreatedAt
	item.UpdatedAt = nowText()

	_, err = s.db.Exec(`
		UPDATE portfolio SET
			name=?, portfolio_type=?, strategy_id=?, benchmark_code=?, status=?,
			max_stock_count=?, target_position_count=?, total_layers=?, rebalance_frequency=?,
			scope_type=?, scope_value_json=?, notes=?, updated_at=?
		WHERE portfolio_id=?`,
		item.Name, item.Type, item.StrategyID, item.BenchmarkCode, item.Status,
		item.MaxStockCount, item.TargetPositionCount, item.TotalLayers, item.RebalanceFrequency,
		item.ScopeType, item.ScopeValueJSON, item.Notes, item.UpdatedAt, item.ID,
	)
	if err != nil {
		return Portfolio{}, fmt.Errorf("更新组合失败：%w", err)
	}
	return item, nil
}

// Delete 删除组合。
//
// 持仓与流水靠外键 ON DELETE CASCADE 一起清掉 —— 原项目要手动级联 7 张表，
// 漏一张就留垃圾。阶段一只有这两张子表。
func (s *Store) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM portfolio WHERE portfolio_id=?`, id)
	if err != nil {
		return fmt.Errorf("删除组合失败：%w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("组合 %s 不存在", id)
	}
	return nil
}

// Get 读取单个组合（含运行时汇总）。
func (s *Store) Get(id string) (Portfolio, error) {
	row := s.db.QueryRow(portfolioSelect+` WHERE p.portfolio_id=?`, id)
	item, err := scanPortfolio(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Portfolio{}, fmt.Errorf("组合 %s 不存在", id)
		}
		return Portfolio{}, err
	}
	return item, nil
}

// List 列出全部组合。
//
// 排序（原实现 portfolio_service.py:3035）：
// `CASE status WHEN 'active' THEN 0 ELSE 1 END` → **只有 active 在最前**，
// paused 与 archived 一起排在后面（不是"只有 archived 靠后"）。
// 此前写成 `status='archived' THEN 1 ELSE 0` → paused 会与 active 混排。
func (s *Store) List() ([]Portfolio, error) {
	rows, err := s.db.Query(portfolioSelect + `
		ORDER BY CASE WHEN p.status = 'active' THEN 0 ELSE 1 END ASC,
		         p.updated_at DESC, p.portfolio_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("读取组合列表失败：%w", err)
	}
	defer rows.Close()

	out := make([]Portfolio, 0, 8)
	for rows.Next() {
		item, err := scanPortfolio(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// portfolioSelect 把持仓统计**用子查询一次带出来**。
//
// 不能"先查组合、再逐条查统计"：连接池配了 SetMaxOpenConns(1)，
// 遍历 rows 期间那唯一的连接被占着，再发一次查询会直接死锁
// （写成这样之前测试就卡死在这里）。
const portfolioSelect = `
	SELECT p.portfolio_id, p.name, p.portfolio_type, p.strategy_id, p.benchmark_code, p.status,
	       p.max_stock_count, p.target_position_count, p.total_layers, p.rebalance_frequency,
	       p.scope_type, p.scope_value_json, p.notes, p.created_at, p.updated_at,
	       COALESCE(pos.cnt, 0), COALESCE(pos.used_layers, 0)
	FROM portfolio p
	LEFT JOIN (
		SELECT portfolio_id, COUNT(*) AS cnt, SUM(position_layers) AS used_layers
		FROM portfolio_position GROUP BY portfolio_id
	) pos ON pos.portfolio_id = p.portfolio_id`

// scanner 抽象 QueryRow 与 Rows 的公共方法。
type scanner interface {
	Scan(dest ...any) error
}

func scanPortfolio(row scanner) (Portfolio, error) {
	var item Portfolio
	err := row.Scan(
		&item.ID, &item.Name, &item.Type, &item.StrategyID, &item.BenchmarkCode, &item.Status,
		&item.MaxStockCount, &item.TargetPositionCount, &item.TotalLayers, &item.RebalanceFrequency,
		&item.ScopeType, &item.ScopeValueJSON, &item.Notes, &item.CreatedAt, &item.UpdatedAt,
		&item.PositionCount, &item.UsedLayers,
	)
	if err != nil {
		return Portfolio{}, err
	}
	applyDerived(&item)
	return item, nil
}

// applyDerived 填充由其他字段推导出来的展示字段。
func applyDerived(item *Portfolio) {
	mode := executionModes[NormalizeType(item.Type)]
	item.Type = NormalizeType(item.Type)
	item.TypeLabel = mode.TypeLabel
	item.ExecutionMode = mode.Mode
	item.ExecutionModeLabel = mode.ModeLabel
	item.AllowApplyRotation = mode.AllowApplyRotation
	item.RequireManualConfirmation = mode.RequireManualConfirmation
	item.StrategyLabel = StrategyLabel(item.StrategyID)
	if item.TotalLayers < 1 {
		item.TotalLayers = DefaultTotalLayers
	}
	item.StatusLabel = StatusLabelOf(item.Status)
	item.ScopeLabel = scopeLabel(item.ScopeType, ParseScopeValue(item.ScopeValueJSON))
	item.AvailableLayers = item.TotalLayers - item.UsedLayers
	item.PositionRatio = round(weightFromLayers(item.UsedLayers, item.TotalLayers), 4)
}

// scopeLabel 生成范围的中文描述。
func scopeLabel(scopeType string, value ScopeValue) string {
	var parts []string
	switch scopeType {
	case ScopeManualCodes:
		parts = append(parts, fmt.Sprintf("指定 %d 只股票", len(value.Codes)))
	case ScopeIndustry:
		parts = append(parts, "行业："+strings.Join(value.Items, "、"))
	case ScopeConcept:
		parts = append(parts, "概念："+strings.Join(value.Items, "、"))
	default:
		parts = append(parts, "全市场")
	}
	if len(value.Markets) > 0 {
		names := map[string]string{"main": "主板", "gem": "创业板", "star": "科创板"}
		labels := make([]string, 0, len(value.Markets))
		for _, market := range value.Markets {
			labels = append(labels, names[market])
		}
		parts = append(parts, strings.Join(labels, "/"))
	} else {
		parts = append(parts, "全部市场")
	}
	return strings.Join(parts, "｜")
}

// fillCounts 已废弃：持仓统计改由 portfolioSelect 的子查询一次带出。
// 保留这个注释是为了说明为什么不能按"先查组合再逐条统计"的写法做。

// weightFromLayers 由层数算权重百分数。
//
// 这是原项目 _weight_from_layers 的 1:1 复刻：
// 分母是 total_layers（层数上限），不是市值也不是成本。
// WeightFromLayers 由层数换算权重（百分比）。
//
// 导出给 service 层的自动建组合用 —— 它落库的 `position_weight` 必须与
// 手动建仓用同一个公式。
func WeightFromLayers(layers, totalLayers int) float64 {
	return weightFromLayers(layers, totalLayers)
}

func weightFromLayers(layers, totalLayers int) float64 {
	if totalLayers < 1 {
		totalLayers = 1
	}
	return float64(layers) / float64(totalLayers) * 100
}

// ---------------------------------------------------------------------------
// 持仓
// ---------------------------------------------------------------------------

// AddPosition 新增持仓。
//
// 与原项目一致：**已存在则报错**（一个组合一只股票只能有一条），
// 且层数被 clamp 到 [1, maxManualLayers]。
func (s *Store) AddPosition(portfolioID string, in PositionInput) (Position, error) {
	item, err := s.Get(portfolioID)
	if err != nil {
		return Position{}, err
	}

	code := BareCode(in.Code)
	if code == "" {
		return Position{}, errors.New("股票代码不能为空")
	}
	// 代码合法性（Python 是 is_supported_a_share_ts_code，不合法直接报错）
	if !IsSupportedAShareCode(code) {
		return Position{}, fmt.Errorf(
			"股票代码格式错误，应为 6 位 A 股代码（如 600519 / 000001）：%s", in.Code)
	}
	if in.EntryPrice < 0 {
		return Position{}, errors.New("建仓价格不能为负")
	}
	// 建仓价为 0 时应当由调用方用当日收盘价补齐（service 层负责）。
	// 这里兜住：0 成本会让 profit_loss_pct 恒为 0、浮盈保护永不触发，
	// 用户却以为建仓成功了 —— 必须报错而不是静默写入。
	if in.EntryPrice == 0 {
		return Position{}, errors.New("缺少有效的建仓价格（传 0 且未取到收盘价）")
	}

	var exists int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM portfolio_position WHERE portfolio_id=? AND ts_code=?`,
		portfolioID, code,
	).Scan(&exists); err != nil {
		return Position{}, fmt.Errorf("检查持仓失败：%w", err)
	}
	if exists > 0 {
		return Position{}, fmt.Errorf("%s 已在该组合中，不能重复添加", code)
	}

	// 层数：未提供 → default_layers（总层数/目标持仓数，再 clamp）；
	// **显式传 0 → clamp 到 1**（原实现 :534）
	defaultLayers := int(math.Round(float64(item.TotalLayers) / float64(item.TargetPositionCount)))
	defaultLayers = clamp(defaultLayers, 1, maxManualLayers)
	layers := defaultLayers
	if in.Layers != nil {
		layers = clamp(*in.Layers, 1, maxManualLayers)
	}

	// 权重：未提供 → 按层数换算；**显式传 0 → 存 0**（原实现 :535 的 max(0.0, ...)）
	weight := weightFromLayers(layers, item.TotalLayers)
	if in.Weight != nil {
		weight = math.Max(0, *in.Weight)
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = code
	}
	// holding_type **不设白名单**（原实现 :537 是 `_safe_str(...)` 原样存）
	holdingType := strings.TrimSpace(in.HoldingType)
	sourceType := strings.TrimSpace(in.SourceType)
	if sourceType == "" {
		sourceType = SourceManual
	}
	lastAction := strings.TrimSpace(in.LastAction)
	if lastAction == "" {
		lastAction = "manual_add"
	}

	now := nowText()
	entryDate := NormalizeDate(in.EntryDate)
	if entryDate == "" {
		// 缺省用**最新评分日**，不是"今天"（原实现 :513/524
		// `latest_trade_date = self._get_latest_strength_trade_date() or ... or
		//  pro.get_latest_daily_trade_date()`，再 `entry_date = ... or latest_trade_date`）。
		//
		// 用今天的话，周末/节假日/盘前建仓会得一个非交易日 ——
		// 持有交易日数与最高价窗口的起点都会偏。
		if date, err := s.LatestStrengthDate(item.StrategyID); err == nil && date > 0 {
			entryDate = fmt.Sprintf("%d", date)
		} else {
			// 该策略还没评分过：退到"今天"是最后的兜底
			//（调用方 service 层会用股票列表的交易日覆盖，正常路径走不到这里）
			entryDate = time.Now().Format("20060102")
		}
	}

	position := Position{
		PortfolioID:    portfolioID,
		Code:           code,
		Name:           name,
		EntryPrice:     in.EntryPrice, // 不 round（原实现原样落库）
		EntryDate:      entryDate,
		PositionLayers: layers,
		PositionWeight: round(weight, 4),
		SourceType:     sourceType,
		HoldingType:    holdingType,
		StrategyTag:    strings.TrimSpace(in.StrategyTag),
		LastAction:     lastAction,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	_, err = s.db.Exec(`
		INSERT INTO portfolio_position (
			portfolio_id, ts_code, name, entry_price, entry_date, position_layers, position_weight,
			source_type, holding_type, strategy_tag, last_action, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		position.PortfolioID, position.Code, position.Name, position.EntryPrice, position.EntryDate,
		position.PositionLayers, position.PositionWeight, position.SourceType, position.HoldingType,
		position.StrategyTag, position.LastAction, position.CreatedAt, position.UpdatedAt,
	)
	if err != nil {
		return Position{}, fmt.Errorf("新增持仓失败：%w", err)
	}

	// 写流水（原项目 add_position 写 action='buy'）
	if err := s.insertTxn(Transaction{
		PortfolioID:    portfolioID,
		Code:           code,
		Name:           name,
		TradeDate:      entryDate,
		Action:         "buy",
		Price:          position.EntryPrice,
		LayersDelta:    layers,
		WeightDelta:    position.PositionWeight,
		Reason:         "手动新增持仓",
		SourceSignalID: "manual",
	}); err != nil {
		return Position{}, err
	}
	return position, nil
}

// DeletePosition 删除持仓，并补一条 clear 流水。
//
// 与原项目一致：**硬删 + 补流水**。
func (s *Store) DeletePosition(portfolioID, code string) error {
	bare := BareCode(code)
	existing, err := s.getPosition(portfolioID, bare)
	if err != nil {
		return err
	}

	if _, err := s.db.Exec(
		`DELETE FROM portfolio_position WHERE portfolio_id=? AND ts_code=?`,
		portfolioID, bare,
	); err != nil {
		return fmt.Errorf("删除持仓失败：%w", err)
	}

	// 流水日期：**最新评分日 → 建仓日 → 今天**（原实现 portfolio_service.py:576
	// `latest_strength_date or entry_date or now`）。
	// 直接写"今天"的话，周末/盘前清仓会落一个非交易日的流水。
	tradeDate := ""
	if item, err := s.Get(portfolioID); err == nil {
		if date, err := s.LatestStrengthDate(item.StrategyID); err == nil && date > 0 {
			tradeDate = fmt.Sprintf("%d", date)
		}
	}
	if tradeDate == "" {
		tradeDate = NormalizeDate(existing.EntryDate)
	}
	if tradeDate == "" {
		tradeDate = time.Now().Format("20060102")
	}

	return s.insertTxn(Transaction{
		PortfolioID:    portfolioID,
		Code:           bare,
		Name:           existing.Name,
		TradeDate:      tradeDate,
		Action:         "clear",
		Price:          existing.EntryPrice,
		LayersDelta:    -existing.PositionLayers,
		WeightDelta:    -existing.PositionWeight,
		Reason:         "手动删除持仓",
		SourceSignalID: "manual",
	})
}

func (s *Store) getPosition(portfolioID, code string) (Position, error) {
	row := s.db.QueryRow(positionSelect+` WHERE portfolio_id=? AND ts_code=?`, portfolioID, code)
	item, err := scanPosition(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Position{}, fmt.Errorf("持仓 %s 不存在", code)
		}
		return Position{}, err
	}
	return item, nil
}

// ListPositions 列出组合的持仓。
//
// 排序（原实现 portfolio_service.py:3051）：
// **权重降序 → 建仓日升序 → 代码升序**。
// 只按权重+代码排的话，同权重持仓的顺序与原实现不同。
func (s *Store) ListPositions(portfolioID string) ([]Position, error) {
	rows, err := s.db.Query(
		positionSelect+` WHERE portfolio_id=?
		ORDER BY position_weight DESC, entry_date ASC, ts_code ASC`,
		portfolioID,
	)
	if err != nil {
		return nil, fmt.Errorf("读取持仓失败：%w", err)
	}
	defer rows.Close()

	out := make([]Position, 0, 16)
	for rows.Next() {
		item, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const positionSelect = `
	SELECT portfolio_id, ts_code, name, entry_price, entry_date, position_layers, position_weight,
	       source_type, holding_type, strategy_tag, last_action, created_at, updated_at
	FROM portfolio_position`

func scanPosition(row scanner) (Position, error) {
	var item Position
	err := row.Scan(
		&item.PortfolioID, &item.Code, &item.Name, &item.EntryPrice, &item.EntryDate,
		&item.PositionLayers, &item.PositionWeight, &item.SourceType, &item.HoldingType,
		&item.StrategyTag, &item.LastAction, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

// ListTransactions 列出组合的流水，最新在前。
func (s *Store) ListTransactions(portfolioID string) ([]Transaction, error) {
	rows, err := s.db.Query(`
		SELECT txn_id, portfolio_id, ts_code, name, trade_date, action, price,
		       layers_delta, weight_delta, reason, source_signal_id, created_at
		FROM portfolio_position_txn WHERE portfolio_id=?
		ORDER BY created_at DESC, txn_id DESC`, portfolioID)
	if err != nil {
		return nil, fmt.Errorf("读取流水失败：%w", err)
	}
	defer rows.Close()

	out := make([]Transaction, 0, 32)
	for rows.Next() {
		var item Transaction
		if err := rows.Scan(
			&item.ID, &item.PortfolioID, &item.Code, &item.Name, &item.TradeDate, &item.Action,
			&item.Price, &item.LayersDelta, &item.WeightDelta, &item.Reason,
			&item.SourceSignalID, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) insertTxn(txn Transaction) error {
	txn.ID = newTxnID()
	txn.CreatedAt = nowText()
	_, err := s.db.Exec(`
		INSERT INTO portfolio_position_txn (
			txn_id, portfolio_id, ts_code, name, trade_date, action, price,
			layers_delta, weight_delta, reason, source_signal_id, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		txn.ID, txn.PortfolioID, txn.Code, txn.Name, txn.TradeDate, txn.Action, txn.Price,
		txn.LayersDelta, txn.WeightDelta, txn.Reason, txn.SourceSignalID, txn.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("写持仓流水失败：%w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

// Detail 是组合详情（组合 + 持仓 + 汇总），一次查询返回。
type Detail struct {
	Portfolio    Portfolio     `json:"portfolio"`
	Positions    []Position    `json:"positions"`
	Transactions []Transaction `json:"transactions"`
	Summary      Summary       `json:"summary"`
}

// Detail 读取组合详情，并用 prices 填充现价与盈亏。
//
// prices 为 nil 时也能用（现价记 0，盈亏记 0）—— 这和原实现
// "left join 落空 → current_price = 0" 的行为一致。
func (s *Store) Detail(portfolioID string, prices PriceSource) (Detail, error) {
	item, err := s.Get(portfolioID)
	if err != nil {
		return Detail{}, err
	}
	positions, err := s.ListPositions(portfolioID)
	if err != nil {
		return Detail{}, err
	}
	txns, err := s.ListTransactions(portfolioID)
	if err != nil {
		return Detail{}, err
	}

	summary := Summarize(item, positions, prices, &positions)

	return Detail{Portfolio: item, Positions: positions, Transactions: txns, Summary: summary}, nil
}

// Summarize 计算组合汇总，并把现价回填到 positions。
//
// 计算口径逐条对齐原项目：
//
//	position_weight = layers / total_layers * 100
//	profit_loss_pct = (current_price / entry_price - 1) * 100     # 成本为 0 记 0
//	total_return_pct = Σ(profit_loss_pct_i × weight_i / 100)      # 不含现金
func Summarize(item Portfolio, positions []Position, prices PriceSource, fill *[]Position) Summary {
	summary := Summary{
		PortfolioID:     item.ID,
		PositionCount:   len(positions),
		AvailableLayers: item.TotalLayers,
	}

	// 取现价
	priceMap := map[string]PricePoint{}
	if prices != nil && len(positions) > 0 {
		codes := make([]string, 0, len(positions))
		for _, position := range positions {
			codes = append(codes, position.Code)
		}
		if loaded, err := prices.LatestClose(codes, item.StrategyID); err == nil {
			priceMap = loaded
		}
	}

	weighted := 0.0
	for i := range positions {
		position := &positions[i]
		summary.UsedLayers += position.PositionLayers

		if point, ok := priceMap[position.Code]; ok && point.Close > 0 {
			position.CurrentPrice = point.Close
			position.PriceDate = point.Date
			if summary.PriceDate == "" || point.Date > summary.PriceDate {
				summary.PriceDate = point.Date
			}
		} else {
			// 取不到现价 → 记 0（与原实现的 left join 一致），不是缺失
			position.CurrentPrice = 0
			summary.MissingPrices++
		}

		// 盈亏比例：成本为 0 记 0
		if position.EntryPrice > 0 && position.CurrentPrice > 0 {
			position.ProfitLossPct = round((position.CurrentPrice/position.EntryPrice-1)*100, 4)
		} else {
			position.ProfitLossPct = 0
		}

		// 组合收益率 = Σ(逐仓收益率 × 权重)，权重是百分数要 /100
		weighted += position.ProfitLossPct * position.PositionWeight / 100
	}

	summary.AvailableLayers = item.TotalLayers - summary.UsedLayers
	summary.PositionRatio = round(weightFromLayers(summary.UsedLayers, item.TotalLayers), 4)
	summary.TotalReturnPct = round(weighted, 4)

	if fill != nil {
		*fill = positions
	}
	return summary
}

// Overview 是组合总览（原项目 get_overview_payload 的口径）。
type Overview struct {
	PortfolioCount int `json:"portfolioCount"`
	ActiveCount    int `json:"activeCount"`
	PositionCount  int `json:"positionCount"`
	UsedLayers     int `json:"usedLayers"`
	TotalLayers    int `json:"totalLayers"`
}

// Overview 汇总所有组合。
func (s *Store) Overview() (Overview, error) {
	list, err := s.List()
	if err != nil {
		return Overview{}, err
	}
	out := Overview{PortfolioCount: len(list)}
	for _, item := range list {
		if item.Status != StatusArchived {
			out.ActiveCount++
		}
		// position_count 取**真实持仓行数**（原项目总览也是这么算的）
		out.PositionCount += item.PositionCount
		out.UsedLayers += item.UsedLayers
		out.TotalLayers += item.TotalLayers
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// NormalizeDate 把各种日期写法归一成 YYYYMMDD。
//
// 原项目 _normalize_trade_date 的做法：抽取数字再截前 8 位。
// 这样 2026-09-30 / 20260930 / 2026/09/30 都能吃。
func NormalizeDate(value string) string {
	digits := make([]rune, 0, 8)
	for _, ch := range strings.TrimSpace(value) {
		if ch >= '0' && ch <= '9' {
			digits = append(digits, ch)
			if len(digits) == 8 {
				break
			}
		}
	}
	if len(digits) != 8 {
		return ""
	}
	return string(digits)
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// round 保留 n 位小数（原项目统一 round(2)/round(4) 的口径）。
func round(value float64, digits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}

// SortPositionsByWeight 按权重降序（界面默认顺序）。
func SortPositionsByWeight(positions []Position) {
	sort.SliceStable(positions, func(i, j int) bool {
		if positions[i].PositionWeight != positions[j].PositionWeight {
			return positions[i].PositionWeight > positions[j].PositionWeight
		}
		return positions[i].Code < positions[j].Code
	})
}
