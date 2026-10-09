// Package backtest 是组合回测引擎。
//
// 1:1 移植自 `portfolio_backtest_engine.py`（621 行）。
//
// 核心时间语义：**信号在 T 日收盘后生成，在 T+1 日开盘执行**
// （原实现的 `pending_plan`：每个交易日的动作来自**上一个**交易日的计划）。
// 执行价用 `open`，当日盯市用 `close`。
package backtest

import (
	"math"
	"sort"

	"freestock/internal/score"
)

// Config 是一次回测的配置（对应原实现 `PortfolioBacktestConfig`，:19-37）。
type Config struct {
	PortfolioID         string
	PortfolioName       string
	StrategyID          string
	StrategyLabel       string
	BenchmarkCode       string
	StartDate           string
	EndDate             string
	TargetPositionCount int
	MaxStockCount       int
	TotalLayers         int
	MaxLayersPerStock   int
	InitialCapital      float64
	// 手续费与印花税（原实现默认值）
	CommissionRate float64 // 0.00025（万 2.5）
	MinCommission  float64 // 5.0 元
	TaxRate        float64 // 0.001（千 1，仅卖出收）
	SlippagePct    float64 // 0.1
	// SlippageType 是 "pct"（按百分比）或 "fixed"（按绝对值）
	SlippageType string
	MarketScope  []string
}

// withDefaults 补上原实现的默认值。
func (c Config) withDefaults() Config {
	if c.TargetPositionCount < 1 {
		c.TargetPositionCount = 1
	}
	if c.MaxStockCount < c.TargetPositionCount {
		c.MaxStockCount = c.TargetPositionCount
	}
	if c.TotalLayers < 1 {
		c.TotalLayers = 10
	}
	if c.MaxLayersPerStock < 1 {
		c.MaxLayersPerStock = 3
	}
	if c.InitialCapital < 1 {
		c.InitialCapital = 100000
	}
	if c.CommissionRate == 0 {
		c.CommissionRate = 0.00025
	}
	if c.MinCommission == 0 {
		c.MinCommission = 5
	}
	if c.TaxRate == 0 {
		c.TaxRate = 0.001
	}
	if c.SlippagePct == 0 {
		c.SlippagePct = 0.1
	}
	if c.SlippageType == "" {
		c.SlippageType = "pct"
	}
	return c
}

// Position 是一笔持仓（原实现 `Position`）。
type Position struct {
	Code         string
	Name         string
	BuyDate      string
	CostPrice    float64
	Shares       int
	Layers       int
	CurrentPrice float64
	HighestPrice float64
}

// PriceBar 是某只股票某日的价格。
type PriceBar struct {
	Date  string
	Code  string
	Open  float64
	Close float64
	Name  string
}

// Trade 是一笔成交。
type Trade struct {
	Code           string
	Name           string
	Action         string // buy / sell
	TradeDate      string
	SignalDate     string
	Price          float64
	Shares         int
	Amount         float64
	Commission     float64
	Tax            float64
	TotalCost      float64
	PositionLayers int
	ProfitLoss     float64
	ProfitLossPct  float64
	HoldingDays    int
	BuyDate        string
	CostPrice      float64
	Reason         string
	Score          float64
}

// Snapshot 是某一天的净值快照。
type Snapshot struct {
	Date             string
	TotalValue       float64
	Cash             float64
	PositionValue    float64
	DailyReturn      float64
	CumulativeReturn float64
	PositionCount    int
	TradeCount       int
	BenchmarkValue   float64
	BenchmarkReturn  float64
}

// StrengthDay 是某个交易日的评分截面。
type StrengthDay struct {
	Date        string
	MarketState string
	Rows        []score.Row
}

// Result 是一次回测的输出。
type Result struct {
	Snapshots []Snapshot
	Trades    []Trade
}

// Engine 是回测引擎。
type Engine struct {
	cfg Config

	strengthByDate map[string]StrengthDay
	scoreByDate    map[string]map[string]score.Row
	priceMap       map[string]map[string]PriceBar
	benchmark      map[string]float64

	trades    []Trade
	snapshots []Snapshot
}

// New 构造引擎。
func New(cfg Config, days []StrengthDay, bars []PriceBar, benchmark map[string]float64) *Engine {
	engine := &Engine{
		cfg:            cfg.withDefaults(),
		strengthByDate: map[string]StrengthDay{},
		scoreByDate:    map[string]map[string]score.Row{},
		priceMap:       map[string]map[string]PriceBar{},
		benchmark:      benchmark,
	}
	for _, day := range days {
		// 同日按 total_score 降序、ts_code 升序（原实现 :78-81）
		rows := make([]score.Row, len(day.Rows))
		copy(rows, day.Rows)
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].TotalScore != rows[j].TotalScore {
				return rows[i].TotalScore > rows[j].TotalScore
			}
			return rows[i].Code < rows[j].Code
		})
		day.Rows = rows
		engine.strengthByDate[day.Date] = day
		byCode := make(map[string]score.Row, len(rows))
		for _, row := range rows {
			byCode[row.Code] = row
		}
		engine.scoreByDate[day.Date] = byCode
	}
	for _, bar := range bars {
		if bar.Close <= 0 {
			continue
		}
		if engine.priceMap[bar.Date] == nil {
			engine.priceMap[bar.Date] = map[string]PriceBar{}
		}
		engine.priceMap[bar.Date][bar.Code] = bar
	}
	return engine
}

// applySlippage 对应原实现 :59-70。
//
//	买入：price + slippage；卖出：price - slippage
//	pct 模式 slippage = price * pct / 100；fixed 模式 slippage = pct
//	结果 round(2) 且不为负
func (e *Engine) applySlippage(price float64, isBuy bool) float64 {
	if price <= 0 {
		return 0
	}
	pct := math.Max(0, e.cfg.SlippagePct)
	var slippage float64
	if e.cfg.SlippageType == "fixed" {
		slippage = pct
	} else {
		slippage = price * pct / 100
	}
	adjusted := price - slippage
	if isBuy {
		adjusted = price + slippage
	}
	return round2(math.Max(0, adjusted))
}

// layerBudget 对应原实现 :170-172：一层的预算是总资产的 1/total_layers。
func (e *Engine) layerBudget(totalValue float64, layerCount int) float64 {
	return math.Max(0, totalValue) * float64(max0(layerCount)) / float64(e.cfg.TotalLayers)
}

// affordableBuyShares 对应原实现 :174-190：算上手续费后买得起的股数。
func (e *Engine) affordableBuyShares(available, price float64) int {
	if available <= 0 || price <= 0 {
		return 0
	}
	shares := int(available / price)
	for shares > 0 {
		amount := float64(shares) * price
		commission := math.Max(amount*e.cfg.CommissionRate, e.cfg.MinCommission)
		total := amount + commission
		if total <= available+1e-9 {
			return shares
		}
		excess := total - available
		shares -= max0(int(excess / math.Max(price, 1e-9)))
		if shares < 0 {
			return 0
		}
	}
	return 0
}

func (e *Engine) recordTrade(t Trade) {
	e.trades = append(e.trades, t)
}

// sellPosition 对应原实现 :233-304。
func (e *Engine) sellPosition(
	positions map[string]*Position, code, tradeDate string, cash float64,
	shares, layerDelta int, reason, signalDate, priceField string, row score.Row,
) float64 {
	pos := positions[code]
	bar, ok := e.priceMap[tradeDate][code]
	if pos == nil || !ok {
		return cash
	}
	rawPrice := bar.Open
	if priceField == "close" {
		rawPrice = bar.Close
	}
	dealPrice := e.applySlippage(rawPrice, false)
	if dealPrice <= 0 {
		return cash
	}

	sellShares := shares
	currentLayers := max1(pos.Layers)
	layerUnits := max0(layerDelta)
	if sellShares <= 0 && layerUnits > 0 && currentLayers > 0 {
		if layerUnits >= currentLayers {
			sellShares = pos.Shares
		} else {
			sellShares = max1(int(math.Round(float64(pos.Shares) * float64(layerUnits) / float64(currentLayers))))
		}
	}
	if sellShares <= 0 || sellShares >= pos.Shares {
		sellShares = pos.Shares
	}
	if sellShares <= 0 {
		return cash
	}

	amount := float64(sellShares) * dealPrice
	commission := math.Max(amount*e.cfg.CommissionRate, e.cfg.MinCommission)
	tax := amount * e.cfg.TaxRate
	totalIncome := amount - commission - tax
	costBasis := pos.CostPrice * float64(sellShares)
	realized := totalIncome - costBasis
	realizedPct := 0.0
	if costBasis > 0 {
		realizedPct = realized / costBasis * 100
	}
	cash += totalIncome

	layersForTrade := layerUnits
	if layersForTrade <= 0 {
		layersForTrade = pos.Layers
	}
	e.recordTrade(Trade{
		Code: code, Name: pos.Name, Action: "sell",
		TradeDate: tradeDate, SignalDate: firstNonEmpty(signalDate, tradeDate),
		Price: dealPrice, Shares: sellShares, Amount: amount,
		Commission: commission, Tax: tax, TotalCost: -totalIncome,
		PositionLayers: max1(layersForTrade),
		ProfitLoss:     realized, ProfitLossPct: realizedPct,
		HoldingDays: daysBetween(pos.BuyDate, tradeDate),
		BuyDate:     pos.BuyDate, CostPrice: pos.CostPrice,
		Reason: reason, Score: row.TotalScore,
	})

	remaining := pos.Shares - sellShares
	if remaining <= 0 {
		delete(positions, code)
	} else {
		soldLayers := layerUnits
		if soldLayers <= 0 {
			soldLayers = max1(int(math.Round(float64(currentLayers) * float64(sellShares) / float64(max1(pos.Shares)))))
		}
		pos.Shares = remaining
		pos.CurrentPrice = rawPrice
		pos.Layers = max1(currentLayers - min(currentLayers-1, soldLayers))
	}
	return cash
}

// buyPosition 对应原实现 :306-394。
func (e *Engine) buyPosition(
	positions map[string]*Position, row score.Row, tradeDate string,
	cash, budget float64, reason string, targetLayers int, signalDate, priceField string,
) float64 {
	code := row.Code
	if code == "" {
		return cash
	}
	bar, ok := e.priceMap[tradeDate][code]
	if !ok {
		return cash
	}
	rawPrice := bar.Open
	if priceField == "close" {
		rawPrice = bar.Close
	}
	dealPrice := e.applySlippage(rawPrice, true)
	if dealPrice <= 0 {
		return cash
	}

	existing := positions[code]
	currentLayers := 0
	if existing != nil {
		currentLayers = max0(existing.Layers)
	}
	target := max1(targetLayers)
	if existing != nil {
		target = min(max(currentLayers, target), max(currentLayers, e.cfg.MaxLayersPerStock))
	} else {
		target = min(target, max1(e.cfg.MaxLayersPerStock))
	}
	layerDelta := max0(target - currentLayers)
	if layerDelta <= 0 {
		return cash
	}

	available := math.Min(cash, math.Max(0, budget))
	shares := e.affordableBuyShares(available, dealPrice)
	if shares <= 0 {
		return cash
	}
	amount := float64(shares) * dealPrice
	commission := math.Max(amount*e.cfg.CommissionRate, e.cfg.MinCommission)
	totalCost := amount + commission
	if totalCost > available || totalCost > cash {
		shares = e.affordableBuyShares(math.Min(cash, available), dealPrice)
		if shares <= 0 {
			return cash
		}
		amount = float64(shares) * dealPrice
		commission = math.Max(amount*e.cfg.CommissionRate, e.cfg.MinCommission)
		totalCost = amount + commission
		if totalCost > available || totalCost > cash {
			return cash
		}
	}
	cash -= totalCost

	if existing == nil {
		name := firstNonEmpty(row.Name, bar.Name, code)
		costPrice := dealPrice
		if shares > 0 {
			costPrice = totalCost / float64(shares)
		}
		positions[code] = &Position{
			Code: code, Name: name, BuyDate: tradeDate,
			CostPrice: costPrice, Shares: shares, Layers: layerDelta,
			CurrentPrice: dealPrice, HighestPrice: dealPrice,
		}
	} else {
		oldShares := existing.Shares
		newShares := oldShares + shares
		existing.CostPrice = (existing.CostPrice*float64(oldShares) + totalCost) / float64(max1(newShares))
		existing.Shares = newShares
		existing.Layers = min(max1(e.cfg.MaxLayersPerStock), currentLayers+layerDelta)
		if existing.BuyDate == "" {
			existing.BuyDate = tradeDate
		}
		existing.CurrentPrice = dealPrice
		if dealPrice > existing.HighestPrice {
			existing.HighestPrice = dealPrice
		}
	}

	e.recordTrade(Trade{
		Code: code, Name: positions[code].Name, Action: "buy",
		TradeDate: tradeDate, SignalDate: firstNonEmpty(signalDate, tradeDate),
		Price: dealPrice, Shares: shares, Amount: amount,
		Commission: commission, Tax: 0, TotalCost: totalCost,
		PositionLayers: layerDelta, Reason: reason, Score: row.TotalScore,
	})
	return cash
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
