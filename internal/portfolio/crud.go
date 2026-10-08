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
// 原项目是从"最新评分日"的 stock_strength_history.close_price 取现价；
// 本项目阶段一没有评分表，改为取最新交易日的日 K 收盘价 ——
// 语义一致（都是"最近一个有数据的交易日的收盘价"），
// 且原实现里"取不到就记 0"的行为也照搬。
type PriceSource interface {
	LatestClose(codes []string) (map[string]PricePoint, error)
}

// PortfolioInput 是创建/更新组合的入参（对应原项目的 payload）。
//
// 必须带 json tag：Wails 按 tag 生成 TS 绑定，没有 tag 时字段名会原样变成
// PascalCase（Name / ScopeValue），与前端的 camelCase 约定不一致。
type PortfolioInput struct {
	Name                string     `json:"name"`
	Type                string     `json:"type"`
	StrategyID          string     `json:"strategyId"`
	BenchmarkCode       string     `json:"benchmarkCode"`
	Status              string     `json:"status"`
	TargetPositionCount int        `json:"targetPositionCount"`
	MaxStockCount       int        `json:"maxStockCount"`
	TotalLayers         int        `json:"totalLayers"`
	ScopeType           string     `json:"scopeType"`
	ScopeValue          ScopeValue `json:"scopeValue"`
	Notes               string     `json:"notes"`
}

// PositionInput 是新增持仓的入参。
type PositionInput struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	EntryPrice  float64 `json:"entryPrice"`
	EntryDate   string  `json:"entryDate"`
	Layers      int     `json:"layers"`
	Weight      float64 `json:"weight"` // 百分数；0 表示由层数换算
	HoldingType string  `json:"holdingType"`
	StrategyTag string  `json:"strategyTag"`
}

// ---------------------------------------------------------------------------
// 组合 CRUD
// ---------------------------------------------------------------------------

// normalizeInput 把入参归一化成可落库的组合。
func normalizeInput(in PortfolioInput) (Portfolio, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Portfolio{}, errors.New("组合名称不能为空")
	}

	portfolioType := NormalizeType(in.Type)
	strategyID := NormalizeStrategy(in.StrategyID, portfolioType)

	benchmark := strings.TrimSpace(in.BenchmarkCode)
	if benchmark == "" {
		benchmark = defaultBenchmark
	}

	target := in.TargetPositionCount
	if target < 1 {
		target = 5
	}
	maxCount := in.MaxStockCount
	if maxCount < target {
		maxCount = target
	}
	layers := in.TotalLayers
	if layers < 1 {
		layers = DefaultTotalLayers
	}

	scopeType := NormalizeScopeType(in.ScopeType)

	mode := executionModes[portfolioType]
	return Portfolio{
		Name:                      name,
		Type:                      portfolioType,
		TypeLabel:                 mode.TypeLabel,
		StrategyID:                strategyID,
		StrategyLabel:             StrategyLabel(strategyID),
		BenchmarkCode:             benchmark,
		Status:                    NormalizeStatus(in.Status),
		TargetPositionCount:       target,
		MaxStockCount:             maxCount,
		TotalLayers:               layers,
		RebalanceFrequency:        RotationMode, // 恒为 signal（原项目的死字段）
		ScopeType:                 scopeType,
		ScopeValueJSON:            NormalizeScopeValue(scopeType, in.ScopeValue),
		Notes:                     strings.TrimSpace(in.Notes),
		ExecutionMode:             mode.Mode,
		ExecutionModeLabel:        mode.ModeLabel,
		AllowApplyRotation:        mode.AllowApplyRotation,
		RequireManualConfirmation: mode.RequireManualConfirmation,
	}, nil
}

// Create 新建组合。
func (s *Store) Create(in PortfolioInput) (Portfolio, error) {
	item, err := normalizeInput(in)
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
	item, err := normalizeInput(in)
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

// List 列出全部组合，按创建时间倒序（新的在前）。
func (s *Store) List() ([]Portfolio, error) {
	rows, err := s.db.Query(portfolioSelect + ` ORDER BY p.created_at DESC`)
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
	if in.EntryPrice < 0 {
		return Position{}, errors.New("建仓价格不能为负")
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

	// 层数 clamp：原项目 add_position 是 min(2, max(1, ...))
	layers := in.Layers
	if layers < 1 {
		// 未指定时按"总层数 / 目标持仓数"取整，再 clamp
		layers = int(math.Round(float64(item.TotalLayers) / float64(item.TargetPositionCount)))
	}
	layers = clamp(layers, 1, maxManualLayers)

	// 权重：界面没填就按层数换算
	weight := in.Weight
	if weight <= 0 {
		weight = weightFromLayers(layers, item.TotalLayers)
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = code
	}
	holdingType := strings.TrimSpace(in.HoldingType)
	if holdingType != HoldingSwing && holdingType != HoldingBase {
		holdingType = ""
	}

	now := nowText()
	entryDate := NormalizeDate(in.EntryDate)
	if entryDate == "" {
		entryDate = time.Now().Format("20060102")
	}

	position := Position{
		PortfolioID:    portfolioID,
		Code:           code,
		Name:           name,
		EntryPrice:     round(in.EntryPrice, 4),
		EntryDate:      entryDate,
		PositionLayers: layers,
		PositionWeight: round(weight, 4),
		SourceType:     SourceManual,
		HoldingType:    holdingType,
		StrategyTag:    strings.TrimSpace(in.StrategyTag),
		LastAction:     "manual_add",
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

	return s.insertTxn(Transaction{
		PortfolioID:    portfolioID,
		Code:           bare,
		Name:           existing.Name,
		TradeDate:      time.Now().Format("20060102"),
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

// ListPositions 列出组合的持仓，按权重从大到小。
func (s *Store) ListPositions(portfolioID string) ([]Position, error) {
	rows, err := s.db.Query(
		positionSelect+` WHERE portfolio_id=? ORDER BY position_weight DESC, ts_code`,
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
		if loaded, err := prices.LatestClose(codes); err == nil {
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
