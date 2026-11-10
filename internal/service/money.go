package service

import (
	"fmt"

	"freestock/internal/portfolio"
)

// 组合资金：本金 / 现金 / 每只持仓股数。
//
// 口径：
//   - 当前权益 equity = 现金 + Σ 股数 × 现价（现价用不复权收盘价）
//   - 目标股数 = floor(权重 × equity / 现价 / 100) × 100（A 股整手）
//   - 现金 = equity - Σ 目标股数 × 现价（先按整手重算，再写回）
//
// 每次调仓/增删持仓后调用 SyncPortfolioMoney，让"本金"随盈亏自然变化。

// MoneyView 是给界面用的资金视图。
type MoneyView struct {
	InitialCapital float64 `json:"initialCapital"`
	Cash           float64 `json:"cash"`
	MarketValue    float64 `json:"marketValue"`
	Equity         float64 `json:"equity"`  // 当前本金
	ReturnPct      float64 `json:"returnPct"` // 相对初始本金
}

// syncMoneyFor 调仓后同步资金（所有策略通用）。
//
// 层数体系不同，但"层数 → 目标金额 → 整手股数 → 现金"的口径一致。
func (s *PortfolioService) syncMoneyFor(portfolioID, strategyID string) error {
	_ = strategyID
	_, err := s.SyncPortfolioMoney(portfolioID)
	return err
}

// GetPortfolioMoney 读资金视图。
func (s *PortfolioService) GetPortfolioMoney(portfolioID string) (*MoneyView, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	if _, err := store.Get(portfolioID); err != nil {
		return nil, err
	}
	money, err := store.GetMoney(portfolioID)
	if err != nil {
		return nil, err
	}
	shares, err := store.SharesMap(portfolioID)
	if err != nil {
		return nil, err
	}
	positions, err := store.ListPositions(portfolioID)
	if err != nil {
		return nil, err
	}
	mv := 0.0
	for _, p := range positions {
		if n := shares[p.Code]; n > 0 {
			px := s.rawPriceOr(p.Code, p.EntryPrice)
			mv += float64(n) * px
		}
	}
	equity := money.Cash + mv
	out := &MoneyView{
		InitialCapital: money.InitialCapital,
		Cash:           money.Cash,
		MarketValue:    mv,
		Equity:         equity,
	}
	if money.InitialCapital > 0 {
		out.ReturnPct = (equity/money.InitialCapital - 1) * 100
	}
	return out, nil
}

// SetPortfolioCapital 设置本金，并按新本金重置现金。
func (s *PortfolioService) SetPortfolioCapital(portfolioID string, capital float64) (*MoneyView, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	if _, err := store.SetInitialCapital(portfolioID, capital); err != nil {
		return nil, err
	}
	if _, err := s.SyncPortfolioMoney(portfolioID); err != nil {
		return nil, err
	}
	return s.GetPortfolioMoney(portfolioID)
}

// SyncPortfolioMoney 按当前权益把持仓层数换算成整手股数，并重算现金。
func (s *PortfolioService) SyncPortfolioMoney(portfolioID string) (*portfolio.Money, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		return nil, err
	}
	money, err := store.GetMoney(portfolioID)
	if err != nil {
		return nil, err
	}
	shares, err := store.SharesMap(portfolioID)
	if err != nil {
		return nil, err
	}
	positions, err := store.ListPositions(portfolioID)
	if err != nil {
		return nil, err
	}
	// 先算权益：现金 + 现有股数市值
	equity := money.Cash
	prices := make(map[string]float64, len(positions))
	for _, p := range positions {
		px := s.rawPriceOr(p.Code, p.EntryPrice)
		prices[p.Code] = px
		if n := shares[p.Code]; n > 0 && px > 0 {
			equity += float64(n) * px
		}
	}
	total := item.TotalLayers
	if total < 1 {
		total = 10
	}
	newShares := make(map[string]int, len(positions))
	invested := 0.0
	for _, p := range positions {
		px := prices[p.Code]
		if px <= 0 {
			continue
		}
		amount := equity * float64(p.PositionLayers) / float64(total)
		n := int(amount/px/100) * 100
		if n < 0 {
			n = 0
		}
		if n > 0 {
			newShares[p.Code] = n
			invested += float64(n) * px
		}
	}
	cash := equity - invested
	if cash < 0 {
		cash = 0
	}
	if err := store.SetShares(portfolioID, newShares); err != nil {
		return nil, err
	}
	out := portfolio.Money{InitialCapital: money.InitialCapital, Cash: cash}
	if err := store.SetMoney(portfolioID, out); err != nil {
		return nil, err
	}
	return &out, nil
}

// rawPriceOr 取最新不复权收盘价；取不到用兜底价。
func (s *PortfolioService) rawPriceOr(code string, fallback float64) float64 {
	if s.stock != nil {
		if u, err := s.stock.GetStockUniverse(false); err == nil && u.TradeDate > 0 {
			if px, ok := s.rawCloseFor(code, int(u.TradeDate)); ok && px > 0 {
				return px
			}
		}
	}
	return fallback
}

// PortfolioMoneySummary 是给计划页用的"本金 + 每只目标股数/金额"。
type RowMoney struct {
	Code   string  `json:"code"`
	Shares int     `json:"shares"`
	Amount float64 `json:"amount"`
}

// ApplyMoneyToPlan 给计划行补上"目标股数/金额/买不起"标记。
//
// 所有策略通用：目标金额 = 权益 × 目标层数 / 总层数，再按 100 股整手向下取整。
func (s *PortfolioService) ApplyMoneyToPlan(portfolioID string, plan *PlanResult) error {
	if plan == nil {
		return nil
	}
	store, err := s.db()
	if err != nil {
		return err
	}
	money, err := store.GetMoney(portfolioID)
	if err != nil {
		return err
	}
	shares, _ := store.SharesMap(portfolioID)
	positions, _ := store.ListPositions(portfolioID)
	equity := money.Cash
	for _, p := range positions {
		if n := shares[p.Code]; n > 0 {
			equity += float64(n) * s.rawPriceOr(p.Code, p.EntryPrice)
		}
	}
	if equity <= 0 {
		return nil
	}
	total := plan.Config.TotalLayers
	if total < 1 {
		total = 10
	}
	priceByCode := make(map[string]float64, len(plan.Holdings)+len(plan.Candidates))
	for _, r := range plan.Holdings {
		if r.CurrentPrice > 0 {
			priceByCode[r.Code] = r.CurrentPrice
		}
	}
	for _, r := range plan.Candidates {
		if r.Close > 0 {
			priceByCode[r.Code] = r.Close
		}
	}
	for i := range plan.Plan.Rows {
		row := &plan.Plan.Rows[i]
		if row.TargetLayers <= 0 {
			continue
		}
		px := priceByCode[row.Code]
		if px <= 0 {
			continue
		}
		amount := equity * float64(row.TargetLayers) / float64(total)
		n := int(amount/px/100) * 100
		row.TargetShares = n
		row.TargetAmount = float64(n) * px
		row.Affordable = n > 0
	}
	plan.MoneyEquity = equity
	plan.MoneyCash = money.Cash
	return nil
}

var _ = fmt.Sprintf
