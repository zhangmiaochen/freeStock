package service

import (
	"log/slog"
	"sort"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 一次刷新**所有活跃策略**的评分。
//
// 为什么需要它：原实现的 `refresh_latest` / `refresh_backfill` 都是**全局**操作 ——
//
//	strategy_ids = self._active_strategy_ids()      # portfolio_service.py:663
//	# 取所有**非 archived** 组合用到的策略去重；为空时回落到默认策略
//
// 而本项目的「刷新评分」按钮是**按当前选中的组合**取策略的。
// 如果用户有多个组合用不同策略，点一次只会刷新其中一个 ——
// **其余组合的评分会悄悄过期**，接着点「生成建议」就会基于旧数据。
//
// 这个包装把"刷新所有在用策略"补上，语义与原版一致；
// 同时保留单策略的 `RefreshStrength`（回测页需要按策略刷）。

// ActiveStrategyIDs 返回当前**非归档**组合用到的策略（去重、排序）。
//
// 对应原实现 `_active_strategy_ids`（portfolio_service.py:2675-2684）。
// 没有任何组合时回落到默认策略。
func (s *PortfolioService) ActiveStrategyIDs() ([]string, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	return activeStrategyIDs(store)
}

func activeStrategyIDs(store *portfolio.Store) ([]string, error) {
	items, err := store.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		// 原实现：`active = portfolios[status != "archived"]`
		if item.Status == portfolio.StatusArchived {
			continue
		}
		// 归一化后再去重（原实现走 normalize_strategy_ids）
		id := portfolio.NormalizeStrategy(item.StrategyID, item.Type)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []string{score.StrategyTrendGrowth}, nil
	}
	sort.Strings(ids)
	return ids, nil
}

// StrategyRefresh 是单个策略的刷新结果。
type StrategyRefresh struct {
	StrategyID    string `json:"strategyId"`
	StrategyLabel string `json:"strategyLabel"`
	Saved         int    `json:"saved"`
	Reused        int    `json:"reused"`
	Total         int    `json:"total"`
	Skipped       int    `json:"skipped"`
	FromCache     bool   `json:"fromCache"`
	Error         string `json:"error,omitempty"`
}

// RefreshAllResult 是"刷新所有活跃策略"的结果。
type RefreshAllResult struct {
	TradeDate  int               `json:"tradeDate"`
	Limit      int               `json:"limit"`
	Force      bool              `json:"force"`
	Elapsed    string            `json:"elapsed"`
	Strategies []StrategyRefresh `json:"strategies"`
	// SkippedStrategies 是不参与 stock_strength 评分的策略（质量成长）
	SkippedStrategies []string `json:"skippedStrategies,omitempty"`
	// Saved / Reused / Total 是各策略的合计，方便界面一句话说明
	Saved  int `json:"saved"`
	Reused int `json:"reused"`
	Total  int `json:"total"`
}

// RefreshBackfillAll 刷新**所有活跃策略**在 [startDate, endDate] 区间的评分。
//
// 与 `RefreshStrengthAll` 同理：原实现的 `refresh_backfill(start, end)` 也是
// **全局**操作（portfolio_service.py:685，取 `_active_strategy_ids()` 一次刷完），
// 而且**连 limit 参数都没有**。
//
// ⚠ 前端「指定区间」按钮此前只刷当前组合的策略 —— 其它组合的评分会悄悄过期，
// 区间数据更容易被忽略（用户以为整段历史都算好了）。
func (s *PortfolioService) RefreshBackfillAll(startDate, endDate, limit int, force bool) (*BackfillAllResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	ids, err := activeStrategyIDs(store)
	if err != nil {
		return nil, err
	}

	out := &BackfillAllResult{
		StartDate: startDate, EndDate: endDate, Limit: limit, Force: force,
		Strategies: make([]BackfillStrategy, 0, len(ids)),
	}
	began := time.Now()
	for _, id := range ids {
		// 质量成长不写 stock_strength，跳过（否则会按别的策略口径白算一遍）
		if !portfolio.StrategyUsesStrength(id) {
			out.SkippedStrategies = append(out.SkippedStrategies, score.StrategyLabelOf(id))
			continue
		}
		entry := BackfillStrategy{StrategyID: id, StrategyLabel: score.StrategyLabelOf(id)}
		result, err := s.RefreshBackfill(id, startDate, endDate, limit, force)
		if err != nil {
			entry.Error = err.Error()
			out.Strategies = append(out.Strategies, entry)
			slog.Warn("区间回填失败", "strategy", id, "err", err)
			continue
		}
		entry.Days = result.Days
		entry.Scored = result.Scored
		entry.Reused = result.Reused
		entry.Total = result.Total
		if len(result.Dates) > 0 {
			entry.StartDate, entry.EndDate = result.Dates[0], result.Dates[len(result.Dates)-1]
		}
		if result.Days > out.Days {
			out.Days = result.Days
		}
		out.Scored += result.Scored
		out.Reused += result.Reused
		out.Total += result.Total
		out.Dates = result.Dates // 交易日历与策略无关，取最后一个即可
		out.Strategies = append(out.Strategies, entry)
	}
	out.Elapsed = time.Since(began).Round(time.Millisecond).String()
	slog.Info("区间回填所有活跃策略完成",
		"strategies", len(out.Strategies), "days", out.Days,
		"scored", out.Scored, "reused", out.Reused, "elapsed", out.Elapsed)
	return out, nil
}

// BackfillStrategy 是单个策略的区间回填结果。
type BackfillStrategy struct {
	StrategyID    string `json:"strategyId"`
	StrategyLabel string `json:"strategyLabel"`
	StartDate     int    `json:"startDate"`
	EndDate       int    `json:"endDate"`
	Days          int    `json:"days"`
	Scored        int    `json:"scored"`
	Reused        int    `json:"reused"`
	Total         int    `json:"total"`
	Error         string `json:"error,omitempty"`
}

// BackfillAllResult 是"区间回填所有活跃策略"的结果。
type BackfillAllResult struct {
	StartDate  int                `json:"startDate"`
	EndDate    int                `json:"endDate"`
	Limit      int                `json:"limit"`
	Force      bool               `json:"force"`
	Elapsed    string             `json:"elapsed"`
	Days       int                `json:"days"`
	Scored     int                `json:"scored"`
	Reused     int                `json:"reused"`
	Total      int                `json:"total"`
	Dates      []int              `json:"dates"`
	Strategies []BackfillStrategy `json:"strategies"`
	// SkippedStrategies 是不参与 stock_strength 评分的策略（质量成长），
	// 只做提示，不计入 Strategies。
	SkippedStrategies []string `json:"skippedStrategies,omitempty"`
}

// RefreshStrengthAll 刷新所有活跃策略的评分（原版 `refresh_latest` 的语义）。
//
// 单个策略失败**不影响其它策略** —— 错误记在该策略的 `Error` 上，
// 整体不返回错误。理由：一个策略的数据问题不该让用户连别的策略都刷不了。
func (s *PortfolioService) RefreshStrengthAll(limit int, force bool) (*RefreshAllResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	ids, err := activeStrategyIDs(store)
	if err != nil {
		return nil, err
	}

	out := &RefreshAllResult{Limit: limit, Force: force, Strategies: make([]StrategyRefresh, 0, len(ids))}
	began := time.Now()
	for _, id := range ids {
		// 质量成长不写 stock_strength，跳过
		if !portfolio.StrategyUsesStrength(id) {
			out.SkippedStrategies = append(out.SkippedStrategies, score.StrategyLabelOf(id))
			continue
		}
		entry := StrategyRefresh{StrategyID: id, StrategyLabel: score.StrategyLabelOf(id)}
		result, err := s.RefreshStrength(id, limit, force)
		if err != nil {
			entry.Error = err.Error()
			out.Strategies = append(out.Strategies, entry)
			slog.Warn("刷新策略评分失败", "strategy", id, "err", err)
			continue
		}
		entry.Saved = result.Saved
		entry.Reused = result.Reused
		entry.Total = result.Total
		entry.Skipped = result.Skipped
		entry.FromCache = result.SkippedFromCache
		if result.TradeDate > out.TradeDate {
			out.TradeDate = result.TradeDate
		}
		out.Saved += result.Saved
		out.Reused += result.Reused
		out.Total += result.Total
		out.Strategies = append(out.Strategies, entry)
	}
	out.Elapsed = time.Now().Sub(began).Round(1e6).String()
	slog.Info("刷新所有活跃策略完成",
		"strategies", len(out.Strategies), "saved", out.Saved,
		"reused", out.Reused, "elapsed", out.Elapsed)
	return out, nil
}
