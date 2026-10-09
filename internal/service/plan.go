package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 计划编排：把物化的评分喂进 internal/score 的轮动引擎，产出真实调仓建议。
//
// 对应参考实现的 PortfolioService._build_portfolio_rotation_rows +
// refresh_latest 的组合。
//
// 为什么分两步（先 RefreshStrength 再 BuildPlan）：
// 全市场评分要数分钟，而看一次建议是秒级的交互。参考实现同样是两步
// （refresh_latest 物化 → 出建议时读表）。

// RefreshResult 是一次评分刷新的结果。
type RefreshResult struct {
	TradeDate   int               `json:"tradeDate"`
	Saved       int               `json:"saved"`
	Skipped     int               `json:"skipped"`
	Limit       int               `json:"limit"`
	MarketState score.MarketState `json:"marketState"`
	Unavailable []string          `json:"unavailable"`
	Elapsed     string            `json:"elapsed"`
	// Reused 是本次**复用已有评分、没有重算**的只数
	Reused int `json:"reused"`
	// Total 是该 (交易日, 策略) 现在落库的总只数（新增 + 复用）
	Total int `json:"total"`
	// SkippedFromCache 为 true 表示"全部已算过，本次一次行情都没拉"
	SkippedFromCache bool `json:"skippedFromCache"`
}

// RefreshStrength 给指定策略评分并把结果物化。
//
// limit > 0 时只评前 limit 只（保护数据源；全市场 7000+ 次请求要几分钟）。
// 建议日常用 800~1500，够选候选池了。
// limit > 0 时只评股票列表的前 limit 只（保护数据源；全市场 7000+ 次请求要几分钟）。
// 建议日常用 800~1500，够选候选池了。
//
// **所有组合的持仓代码一定会被评上**，哪怕不在前 limit 只里 ——
// 否则持仓没有评分，轮动建议就永远是空的。
// RefreshStrength 刷新评分。
//
// **增量刷新**：已经算过的代码不再重算 —— 评分结果只取决于 (代码, 交易日, 策略)，
// 重算出来的值完全相同，但每只都会省掉一次 320 个交易日的行情拉取
// （7546 只就是 7546 次 HTTP 往返，这是刷新的主要成本）。
//
// force 为 true 时按全量重算（数据修订后需要覆盖时用）。
func (s *PortfolioService) RefreshStrength(strategyID string, limit int, force bool) (*RefreshResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	if strategyID == "" {
		strategyID = score.StrategyTrendGrowth
	}

	codes, heldCount, err := s.scoringUniverse(store, limit)
	if err != nil {
		return nil, err
	}

	// 先解析评分日 —— 增量判断需要知道"算的是哪一天"
	tradeDate := 0
	if provider, _ := s.stock.snapshot(); provider != nil {
		if resolved, err := provider.LatestTradeDate(context.Background()); err == nil {
			tradeDate = resolved
		}
	}

	reused := 0
	if !force && tradeDate > 0 {
		existing, err := store.StrengthCodes(int64(tradeDate), strategyID)
		if err != nil {
			// 读不到已有代码不该让刷新失败 —— 退化成全量重算即可
			slog.Warn("读取已有评分失败，退化为全量重算", "err", err)
		} else if len(existing) > 0 {
			missing := make([]string, 0, len(codes))
			for _, code := range codes {
				if existing[portfolio.BareCode(code)] {
					reused++
					continue
				}
				missing = append(missing, code)
			}
			codes = missing
		}
	}

	// 全部已算过：直接回已有结果，**一次行情都不拉**
	if len(codes) == 0 {
		state, _ := store.LoadMarketState(int64(tradeDate))
		total, _ := store.StrengthCount(int64(tradeDate), strategyID)
		slog.Info("评分已是最新，跳过重算",
			"tradeDate", tradeDate, "strategy", strategyID, "reused", reused)
		return &RefreshResult{
			TradeDate: tradeDate, Saved: 0, Reused: reused, Total: total,
			Limit: limit,
			// 从已落库的市场状态回填，保证界面拿到的结构完整
			MarketState: score.MarketState{
				State: state.State, Label: state.Label,
				BullScore: state.BullScore, BearScore: state.BearScore,
				ProxyRet20: state.ProxyRet20, Days: state.Days,
			},
			SkippedFromCache: true,
		}, nil
	}

	scoring := NewScoringService(s.stock)
	result, err := scoring.ScoreStocks(codes, 0, 0, strategyID)
	if err != nil {
		return nil, err
	}

	// 行业与市场从板块归属补，然后落库（回填复用同一段）
	if err := s.saveScoringResult(store, result, strategyID); err != nil {
		return nil, err
	}
	total, _ := store.StrengthCount(int64(result.TradeDate), strategyID)
	slog.Info("评分完成",
		"tradeDate", result.TradeDate, "新增", len(result.Rows),
		"复用已有", reused, "含持仓", heldCount)
	return &RefreshResult{
		TradeDate: result.TradeDate, Saved: len(result.Rows), Skipped: result.Skipped,
		Reused: reused, Total: total,
		Limit: limit, MarketState: result.MarketState,
		Unavailable: result.Unavailable,
	}, nil
}

// saveScoringResult 把一次评分结果落库（评分表 + 市场状态）。
//
// 抽出来是因为**历史回填**要复用同一段 —— 之前它内联在 RefreshStrength 里。
func (s *PortfolioService) saveScoringResult(
	store *portfolio.Store, result *ScoringResult, strategyID string,
) error {
	rows := make([]portfolio.StrengthRow, 0, len(result.Rows))
	industryByCode := map[string]string{}
	scoredCodes := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		scoredCodes = append(scoredCodes, row.Code)
	}
	if membership, err := s.stock.boardsForCodes(scoredCodes); err == nil {
		for code, boards := range membership {
			if len(boards) > 0 {
				industryByCode[code] = boards[0]
			}
		}
	}
	marketByCode := map[string]string{}
	if universe, err := s.stock.GetStockUniverse(false); err == nil {
		for _, item := range universe.Stocks {
			marketByCode[item.Code] = item.Market
		}
	}

	for _, row := range result.Rows {
		rows = append(rows, portfolio.StrengthRow{
			Code: row.Code, Name: row.Name,
			Industry: industryByCode[row.Code],
			Market:   marketByCode[row.Code],

			TotalScore: row.TotalScore, TrendScore: row.TrendScore,
			StructureScore: row.StructureScore, RSScore: row.RSScore,
			CapitalScore: row.CapitalScore, RepairScore: row.RepairScore,

			Close: row.Close, High: row.High, Low: row.Low, Open: row.Open, PctChg: row.PctChg,
			Amount: row.Amount,
			Ma5:    row.Ma5, Ma20: row.Ma20, Ma30: row.Ma30,
			Ma60: row.Ma60, Ma120: row.Ma120, Ma250: row.Ma250,

			RelativeStrength20D: row.RelativeStrength20D,
			DrawdownFromHigh20:  row.DrawdownFromHigh20,
			DrawdownFromHigh120: row.DrawdownFromHigh120,
			ReboundFromLow60:    row.ReboundFromLow60,
			StockReturn3D:       row.StockReturn3D,
			StockReturn5D:       row.StockReturn5D,
			StockReturn10D:      row.StockReturn10D,
			StockReturn20D:      row.StockReturn20D,
			VolumeRatio20:       row.VolumeRatio20,
			VolumeRatio520:      row.VolumeRatio520,
			Ma5Prev:             row.Ma5Prev,
			Ma250Prev:           row.Ma250Prev,
			IndustryReturn20D:   row.IndustryReturn20D,
			BenchmarkReturn20D:  row.BenchmarkReturn20D,
		})
	}

	if err := store.SaveStrength(int64(result.TradeDate), strategyID, rows); err != nil {
		return err
	}
	// 市场状态跟着评分一起存
	if err := store.SaveMarketState(portfolio.MarketStateRow{
		TradeDate:  int64(result.TradeDate),
		State:      result.MarketState.State,
		Label:      result.MarketState.Label,
		BullScore:  result.MarketState.BullScore,
		BearScore:  result.MarketState.BearScore,
		ProxyRet20: result.MarketState.ProxyRet20,
		Days:       result.MarketState.Days,
	}); err != nil {
		return err
	}

	slog.Info("评分已物化",
		"strategy", strategyID, "tradeDate", result.TradeDate,
		"saved", len(rows), "marketState", result.MarketState.State)
	return nil
}

// scoringUniverse 决定这次要评哪些股票：股票列表前 limit 只 + 所有持仓代码。
//
// 持仓必须无条件纳入 —— 候选池是从评分表里选的，如果持仓不在表里，
// 参考实现"左连接落空 → 现价记 0"就会让所有持仓评分变 0，
// 轮动建议直接失去意义。
func (s *PortfolioService) scoringUniverse(store *portfolio.Store, limit int) ([]string, int, error) {
	seen := map[string]bool{}
	codes := make([]string, 0, 1024)
	add := func(code string) {
		code = portfolio.BareCode(code)
		if code == "" || seen[code] {
			return
		}
		seen[code] = true
		codes = append(codes, code)
	}

	// 1) 股票列表前 limit 只
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil {
		return nil, 0, err
	}
	for _, item := range universe.Stocks {
		if !item.HasQuote {
			continue
		}
		if limit > 0 && len(codes) >= limit {
			break
		}
		add(item.Code)
	}
	universeCount := len(codes)

	// 2) 所有组合的持仓（不受 limit 限制）
	list, err := store.List()
	if err != nil {
		return nil, 0, err
	}
	for _, item := range list {
		positions, err := store.ListPositions(item.ID)
		if err != nil {
			return nil, 0, err
		}
		for _, position := range positions {
			add(position.Code)
		}
	}

	heldCount := len(codes) - universeCount
	slog.Info("本次评分范围",
		"universeTop", universeCount, "heldAdded", heldCount, "total", len(codes))
	return codes, heldCount, nil
}

// PlanResult 是一次调仓建议。
type PlanResult struct {
	PortfolioID   string               `json:"portfolioId"`
	PortfolioName string               `json:"portfolioName"`
	StrategyID    string               `json:"strategyId"`
	StrategyLabel string               `json:"strategyLabel"`
	Config        score.StrategyConfig `json:"config"`
	MarketState   score.MarketState    `json:"marketState"`
	StrengthDate  int                  `json:"strengthDate"`
	// PortfolioScore 是组合评分（持仓总分均值；无持仓时取候选前 N 名均值）
	PortfolioScore float64 `json:"portfolioScore"`
	// HealthLabel 是健康度标签：强势(>=85)/稳健(>=72)/观察(>=58)/风险(<58)
	HealthLabel string `json:"healthLabel"`
	// MaxDrawdownPct 是持仓 120 日回撤里最负那个的绝对值
	MaxDrawdownPct float64            `json:"maxDrawdownPct"`
	Plan           score.RotationPlan `json:"plan"`
	Holdings       []score.Row        `json:"holdings"`
	Candidates     []score.Row        `json:"candidates"`
	Notes          []string           `json:"notes"`
}

// BuildPlan 为组合生成真实的调仓建议。
//
// 需要该策略的评分已经物化过（先调 RefreshStrength）。
func (s *PortfolioService) BuildPlan(portfolioID string) (*PlanResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		return nil, err
	}

	strategyID := item.StrategyID
	strengthDate, err := store.LatestStrengthDate(strategyID)
	if err != nil {
		return nil, err
	}
	if strengthDate == 0 {
		return nil, fmt.Errorf(
			"「%s」还没有评分数据。请先刷新评分（策略：%s），评分是全市场计算的，需要几分钟",
			item.Name, item.StrategyLabel)
	}

	stateRow, err := store.LoadMarketState(strengthDate)
	if err != nil {
		return nil, err
	}
	marketState := score.MarketState{
		State: stateRow.State, Label: stateRow.Label,
		BullScore: stateRow.BullScore, BearScore: stateRow.BearScore,
		ProxyRet20: stateRow.ProxyRet20, Days: stateRow.Days,
	}

	cfg := score.BuildStrategyConfig(
		strategyID, marketState.State,
		item.TargetPositionCount, item.TotalLayers, 3,
		scopeKeyOf(item, store),
	)

	positions, err := store.ListPositions(portfolioID)
	if err != nil {
		return nil, err
	}

	// ---- 持仓行 ----
	holdingCodes := make([]string, 0, len(positions))
	for _, position := range positions {
		holdingCodes = append(holdingCodes, position.Code)
	}
	strengthByCode, err := store.LoadStrengthForCodes(strengthDate, strategyID, holdingCodes)
	if err != nil {
		return nil, err
	}

	var notes []string
	holdingRows := make([]score.Row, 0, len(positions))
	missing := 0
	// 行业/概念范围匹配走数据湖成员表（多值），读不到时内部退回单值
	matcher := s.scopeMatcher()
	for _, position := range positions {
		strength, ok := strengthByCode[position.Code]
		// 持仓也要过组合范围（原实现 :2469-2470）——
		// 范围外的持仓按"左连接落空"处理（评分/现价记 0）
		if ok && !inPortfolioScope(strength, item, matcher) {
			ok = false
		}
		if !ok {
			// 左连接落空 → 现价/评分记 0（不是 NULL、不报错）。
			//
			// ⚠ 但 **highest_price / holding_trade_days 仍要算** ——
			// 原实现（portfolio_service.py:3217-3268）只要 ts_code/entry_date 有效
			// 就去查 bars_daily，**与有没有评分行无关**。
			// 记 0 会让浮盈保护（要求 HighestPrice>0）与持有交易日类规则整体失效。
			missing++
			highlight, days := s.holdingExtremes(
				position.Code, position.EntryDate, int(strengthDate),
				position.EntryPrice, 0)
			holdingRows = append(holdingRows, score.Row{
				Code: position.Code, Name: position.Name,
				PositionLayers:   position.PositionLayers,
				EntryPrice:       position.EntryPrice,
				HighestPrice:     highlight,
				HoldingTradeDays: days,
			})
			continue
		}
		row := strengthToRow(strength)
		row.PositionLayers = position.PositionLayers
		row.EntryPrice = position.EntryPrice
		row.CurrentPrice = strength.Close
		if position.EntryPrice > 0 && strength.Close > 0 {
			row.ProfitLossPct = round4((strength.Close/position.EntryPrice - 1) * 100)
		}
		// 持仓期最高价与交易日数：需要历史，逐只查（持仓通常只有几只）
		row.HighestPrice, row.HoldingTradeDays = s.holdingExtremes(
			position.Code, position.EntryDate, int(strengthDate), position.EntryPrice, strength.Close)
		holdingRows = append(holdingRows, row)
	}
	if missing > 0 {
		notes = append(notes, fmt.Sprintf(
			"有 %d 只持仓在评分表里没有记录（停牌/数据不足），它们的评分按 0 计", missing))
	}

	// ---- 候选池 ----
	candidates, universeScores, err := s.buildCandidatePool(store, item, cfg, marketState, strengthDate, holdingCodes)
	if err != nil {
		return nil, err
	}

	// ---- 跑轮动 ----
	// 低吸广度就绪数必须**真算出来**：传 0 会让低吸组合的每只持仓都被判
	// "广度不足 < 900" 而强制清仓（原实现从评分帧里取，这里从物化表数）
	breadthReady, err := store.LowBuyBreadthReadyCount(strengthDate, strategyID)
	if err != nil {
		return nil, err
	}
	// 低吸候选的 dd20 档位依赖它（>=1200 用紧档 -18~-1.5）
	cfg.LowBuyBreadthReadyCount = breadthReady
	// 指数闸门与广度并列，共同决定低吸能否开新仓
	lowBuyIndex := s.loadLowBuyIndexGate(int(strengthDate))
	plan := score.BuildRotationPlan(score.RotationInput{
		Holdings:    holdingRows,
		Candidates:  candidates,
		Config:      cfg,
		MarketState: marketState.State,
		// max_stock_count 要抬到 target 以上（原实现 :2492
		// `max_stock_count = max(target_count, portfolio.max_stock_count)`）——
		// 否则 `planned_names < max_stock_limit` 的判定会偏严、少换股
		MaxStockLimit: maxInt(item.MaxStockCount, item.TargetPositionCount),
		// 低吸的候选条件按广度分档（>=1200 用紧档 -18~-1.5）
		LowBuyBreadthReadyCount: breadthReady,
		// 广度闸门用全量评分，不是候选池
		UniverseScores: universeScores,
		// 低吸的指数闸门（创业板指站上 20/60 日线 + 非弱周期假热度）
		LowBuyIndex: lowBuyIndex,
	})
	notes = append(notes, plan.Notes...)

	// 执行语义要按**组合类型**覆盖（score 包拿不到组合类型）
	applyPortfolioBehavior(plan.Rows, item)

	// ---- 组合评分 / 健康度 / 最大回撤 ----
	//
	// 原实现 portfolio_service.py:2500-2528：
	//
	//	portfolio_score = mean(held.total_score)                    # 无持仓时取候选前 N 名均值
	//	health_label    = _score_label(portfolio_score)             # :226-234
	//	max_drawdown    = abs(min(held.drawdown_from_high_120))     # :2512-2513
	portfolioScore := 0.0
	if len(holdingRows) > 0 {
		sum := 0.0
		for _, row := range holdingRows {
			sum += row.TotalScore
		}
		portfolioScore = sum / float64(len(holdingRows))
	} else if len(candidates) > 0 {
		n := cfg.TargetCount
		if n > len(candidates) {
			n = len(candidates)
		}
		if n > 0 {
			sum := 0.0
			for i := 0; i < n; i++ {
				sum += candidates[i].TotalScore
			}
			portfolioScore = sum / float64(n)
		}
	}

	// **是 abs(min) 不是 max(abs)** —— 全为正时两者结果不同
	maxDrawdown := 0.0
	if len(holdingRows) > 0 {
		minDrawdown := holdingRows[0].DrawdownFromHigh120
		for _, row := range holdingRows {
			if row.DrawdownFromHigh120 < minDrawdown {
				minDrawdown = row.DrawdownFromHigh120
			}
		}
		maxDrawdown = math.Abs(minDrawdown)
	}

	// ---- 落档：把"当时看到的东西"存一份 ----
	//
	// 原实现 `_rebuild_portfolio_view`（portfolio_service.py:2437+）会覆盖写四张表。
	// 不落档的话，事后无法回答"系统在某天建议过什么"，
	// 也没法对账（按建议操作了，结果如何）。
	result := &PlanResult{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: strategyID, StrategyLabel: item.StrategyLabel,
		Config: cfg, MarketState: marketState,
		StrengthDate:   int(strengthDate),
		PortfolioScore: round4(portfolioScore),
		HealthLabel:    HealthLabel(portfolioScore),
		MaxDrawdownPct: round4(maxDrawdown),
		Plan:           plan,
		Holdings:       holdingRows,
		Candidates:     candidates,
		Notes:          notes,
	}
	// 落档失败**不影响建议本身** —— 记日志即可，
	// 不要因为存档问题让用户看不到建议。
	if err := s.savePlanSnapshots(store, item, result); err != nil {
		slog.Warn("写入调仓建议留档失败", "portfolio", item.ID, "err", err)
	}
	return result, nil
}

// buildCandidatePool 组装候选池。
//
// 链路（对齐参考实现第 4 部分）：
//  1. 从评分表取当日该策略全部行
//  2. 范围过滤（全市场 / 指定股票 / 行业 / 概念 + 市场）
//  3. 剔除 ST、剔除已持仓
//  4. 评分下限（低吸是 0，靠自己的结构条件）
//  5. 排序，取 head(max(80, target*12)) —— 参考实现的硬编码
//
// buildCandidatePool 从库里读当日评分再建候选池（实盘建议用）。
func (s *PortfolioService) buildCandidatePool(
	store *portfolio.Store,
	item portfolio.Portfolio,
	cfg score.StrategyConfig,
	marketState score.MarketState,
	strengthDate int64,
	holdingCodes []string,
) ([]score.Row, []float64, error) {
	all, err := store.LoadStrength(strengthDate, item.StrategyID, 0)
	if err != nil {
		return nil, nil, err
	}
	rows, scores := s.buildCandidatePoolFrom(all, item, cfg, holdingCodes)
	return rows, scores, nil
}

// buildCandidatePoolFrom 用**已加载好的评分行**建候选池。
//
// 抽出来是因为**回测要逐日复用同一段逻辑** —— 它不能每天去查一次库
// （回测要把整个区间的评分一次性读出来）。
func (s *PortfolioService) buildCandidatePoolFrom(
	all []portfolio.StrengthRow,
	item portfolio.Portfolio,
	cfg score.StrategyConfig,
	holdingCodes []string,
) ([]score.Row, []float64) {

	held := map[string]bool{}
	for _, code := range holdingCodes {
		held[code] = true
	}
	// 行业/概念范围匹配走数据湖成员表（多值）
	matcher := s.scopeMatcher()

	scope := portfolio.ParseScopeValue(item.ScopeValueJSON)
	scopeCodes := map[string]bool{}
	for _, code := range scope.Codes {
		scopeCodes[code] = true
	}
	markets := map[string]bool{}
	for _, market := range scope.Markets {
		markets[market] = true
	}

	// ---- ① 范围 + 市场过滤 ----
	//
	// ⚠ 这一步的集合就是**广度闸门用的帧**（原实现 portfolio_service.py:2469
	// `score_frame = self._attach_signal_candidates(self._scope_filter(...))`
	// 再作为 `daily_strength` 传给 `build_rotation_plan`），
	// `market_breadth_status` 是在**范围过滤后**的帧上数 universe/ready 的。
	//
	// 此前 Go 用**全量**评分算 universeScores（未过范围）——
	// 窄范围组合（自定义 20 只、行业范围）在原版里 `universe < 100` → 闸门**不启用**，
	// 在 Go 里却按全市场数千只启用，行为正好相反。
	scoped := make([]portfolio.StrengthRow, 0, len(all))
	universeScores := make([]float64, 0, len(all))
	for _, strength := range all {
		// 范围过滤（行业/概念走数据湖成员表，与持仓侧同一口径）
		switch item.ScopeType {
		case portfolio.ScopeManualCodes:
			if !scopeCodes[strength.Code] {
				continue
			}
		case portfolio.ScopeIndustry:
			if !matchScopeItem(scope.Items, strength, matcher, true) {
				continue
			}
		case portfolio.ScopeConcept:
			if !matchScopeItem(scope.Items, strength, matcher, false) {
				continue
			}
		}
		// 市场过滤
		if len(markets) > 0 && !markets[strength.Market] {
			continue
		}
		scoped = append(scoped, strength)
		universeScores = append(universeScores, strength.TotalScore)
	}

	// ---- ② 候选池过滤（剔 ST、剔已持仓、分数下限）----
	out := make([]score.Row, 0, len(scoped))
	for _, strength := range scoped {
		if held[strength.Code] {
			continue
		}
		// 剔除 ST（参考实现按名称含 "ST" 判断）
		if strings.Contains(strings.ToUpper(strength.Name), "ST") {
			continue
		}
		// 分数下限
		if strength.TotalScore < cfg.CandidateScoreFloor {
			continue
		}
		out = append(out, strengthToRow(strength))
	}

	// 入场优先级重排（原实现 :1646-1698）——
	// 趋势成长按六项加权百分位复合分重排，**不是**简单按总分。
	// 必须在 head 截断**之前**做，否则取到的是不同的前 80 名。
	out = score.PrioritizeEntryPoints(out, cfg)

	// ---- ③ 低吸：先过质量门槛，**再**截断 ----
	//
	// 原实现 :2072-2079 对低吸是特例：
	//
	//	candidate_pool = candidate_pool.loc[quality_mask]   # 先逐行候选质量过滤
	//	candidate_ranked = candidate_pool.head(...)         # 再截断
	//
	// 其余策略是"先截断、后在使用处过滤"（:2080 + :2465）。
	// 低吸**不能**照抄后者 —— 否则 head-80 取的是"全池前 80 名再过滤"，
	// 与"前 80 个**合格**候选"是不同的集合，可能几乎无合格候选 → 长期不建仓。
	if cfg.StrategyID == portfolio.StrategyTrendLowBuy {
		kept := out[:0]
		for _, row := range out {
			if ok, _ := score.CandidateQualityOK(row, cfg, cfg.MarketState, false); ok {
				kept = append(kept, row)
			}
		}
		out = kept
	}

	head := 80
	if size := item.TargetPositionCount * 12; size > head {
		head = size
	}
	if len(out) > head {
		out = out[:head]
	}
	return out, universeScores
}

// inPortfolioScope 判定一只股票是否在组合的选股范围内。
//
// 原实现把**持仓也过一遍** `_scope_filter`（portfolio_service.py:2469-2470）：
//
//	score_frame = self._attach_signal_candidates(
//	    self._scope_filter(self._strategy_strength_slice(latest_scores, portfolio), portfolio), ...)
//	held_scores = self._merge_position_scores(positions, score_frame)
//
// 范围外的持仓于是"左连接落空" → 评分/现价记 0 → 会被判成弱持仓清仓或换出。
//
// **行业/概念匹配走数据湖成员表**（`ScopeMatcher`），不再用
// `strength.Industry` 那个"板块优先级第一名"的单值 —— 单值会让
// "还属于其它行业/概念"的股票匹配不到。成员表读不到时退回单值，
// 保证不会因为数据缺失把所有范围都匹配空。
func inPortfolioScope(strength portfolio.StrengthRow, item portfolio.Portfolio, matcher *ScopeMatcher) bool {
	scope := portfolio.ParseScopeValue(item.ScopeValueJSON)

	switch item.ScopeType {
	case portfolio.ScopeManualCodes:
		found := false
		for _, code := range scope.Codes {
			if code == strength.Code {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	case portfolio.ScopeIndustry:
		if !matchScopeItem(scope.Items, strength, matcher, true) {
			return false
		}
	case portfolio.ScopeConcept:
		if !matchScopeItem(scope.Items, strength, matcher, false) {
			return false
		}
	}

	// 市场范围：空 = 全部市场
	if len(scope.Markets) > 0 {
		found := false
		for _, market := range scope.Markets {
			if market == strength.Market {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// matchScopeItem 判定股票是否命中范围里的**任意一个**行业/概念名。
//
// 成员表可用时用多值匹配；不可用时退回单值 `strength.Industry`。
func matchScopeItem(items []string, strength portfolio.StrengthRow, matcher *ScopeMatcher, industry bool) bool {
	if len(items) == 0 {
		return true
	}
	useMembers := matcher.Ready()
	for _, want := range items {
		if useMembers {
			if industry {
				if matcher.HasIndustry(strength.Code, want) {
					return true
				}
				continue
			}
			if matcher.HasSector(strength.Code, want) {
				return true
			}
			continue
		}
		// 退回单值：与旧行为一致
		if strength.Industry == want {
			return true
		}
	}
	return false
}

// applyPortfolioBehavior 用组合类型覆盖执行语义。
//
// score 包按"半自动"给默认值，这里按组合真实类型改：
//
//	manual    → manual_review / "仅分析提醒" / 不可执行 / 需确认
//	semi_auto → confirm_required / "人工确认后执行" / 可执行 / 需确认
//	auto      → auto_ready / "可直接应用" / 可执行 / 免确认
func applyPortfolioBehavior(rows []score.RotationRow, item portfolio.Portfolio) {
	for i := range rows {
		row := &rows[i]
		row.ExecutionMode = item.ExecutionMode
		row.ExecutionModeLabel = item.ExecutionModeLabel
		row.IsExecutable = item.AllowApplyRotation && isExecutableAction(row.Action)
		row.RequiresConfirmation = item.RequireManualConfirmation && row.IsExecutable

		switch {
		case row.Action == score.ActionKeep || row.Action == score.ActionObserve:
			row.ActionPrefix = ""
		case item.Type == portfolio.TypeManual:
			row.ActionPrefix = "人工复核"
		case item.Type == portfolio.TypeSemiAuto:
			row.ActionPrefix = "待确认"
		default:
			row.ActionPrefix = ""
		}
	}
}

func isExecutableAction(action string) bool {
	switch action {
	case score.ActionClear, score.ActionReplaceOut, score.ActionReduce,
		score.ActionAdd, score.ActionAddLayer, score.ActionReplaceIn:
		return true
	}
	return false
}

// scopeKeyOf 把组合的市场范围折算成低吸策略的 market_scope key。
func scopeKeyOf(item portfolio.Portfolio, store *portfolio.Store) string {
	scope := portfolio.ParseScopeValue(item.ScopeValueJSON)
	set := map[string]bool{}
	for _, market := range scope.Markets {
		set[market] = true
	}
	// 空 = 全部市场
	if len(set) == 0 || (set["main"] && set["gem"] && set["star"]) {
		return "all"
	}
	order := []string{"main", "gem", "star"}
	var present []string
	for _, key := range order {
		if set[key] {
			present = append(present, key)
		}
	}
	return strings.Join(present, "_")
}

// strengthToRow 把物化评分转成轮动引擎的行。
func strengthToRow(strength portfolio.StrengthRow) score.Row {
	return score.Row{
		Code: strength.Code, Name: strength.Name,
		Market:     strength.Market,
		TotalScore: strength.TotalScore, TrendScore: strength.TrendScore,
		StructureScore: strength.StructureScore, RSScore: strength.RSScore,
		CapitalScore: strength.CapitalScore, RepairScore: strength.RepairScore,

		Close: strength.Close, High: strength.High, Low: strength.Low, Open: strength.Open, PctChg: strength.PctChg,
		Amount: strength.Amount,
		Ma5:    strength.Ma5, Ma20: strength.Ma20, Ma30: strength.Ma30,
		Ma60: strength.Ma60, Ma120: strength.Ma120, Ma250: strength.Ma250,

		RelativeStrength20D: strength.RelativeStrength20D,
		DrawdownFromHigh20:  strength.DrawdownFromHigh20,
		DrawdownFromHigh120: strength.DrawdownFromHigh120,
		ReboundFromLow60:    strength.ReboundFromLow60,
		StockReturn3D:       strength.StockReturn3D,
		StockReturn5D:       strength.StockReturn5D,
		StockReturn10D:      strength.StockReturn10D,
		StockReturn20D:      strength.StockReturn20D,
		VolumeRatio20:       strength.VolumeRatio20,
		VolumeRatio520:      strength.VolumeRatio520,
		Ma5Prev:             strength.Ma5Prev,
		Ma250Prev:           strength.Ma250Prev,
		IndustryReturn20D:   strength.IndustryReturn20D,
		BenchmarkReturn20D:  strength.BenchmarkReturn20D,
	}
}

// holdingExtremes 计算持仓期的最高价与持有交易日数。
//
// 对应参考实现 _merge_position_scores 的后半段：
//   - highest_price = max(entry_price, current_price, 建仓日至今日的收盘价最大值)
//   - holding_trade_days = 建仓日至今日之间**不同交易日**的数量（入场当天算 1）
//
// 逐只查历史：持仓通常只有几只，成本可接受。
func (s *PortfolioService) holdingExtremes(code, entryDate string, endDate int, entryPrice, currentPrice float64) (float64, int) {
	highest := entryPrice
	if currentPrice > highest {
		highest = currentPrice
	}
	if s.stock == nil || entryDate == "" {
		return highest, 1
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return highest, 1
	}
	start := 0
	fmt.Sscanf(entryDate, "%d", &start)
	if start <= 0 || start > endDate {
		return highest, 1
	}
	quotes, err := provider.DailyHistory(context.Background(), code, start, endDate)
	if err != nil || len(quotes) == 0 {
		return highest, 1
	}
	dates := map[int64]bool{}
	for _, quote := range quotes {
		if quote == nil || quote.Date < int64(start) || quote.Date > int64(endDate) {
			continue
		}
		dates[quote.Date] = true
		if quote.Close > highest {
			highest = quote.Close
		}
	}
	days := len(dates)
	if days < 1 {
		days = 1
	}
	return round4(highest), days
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// loadLowBuyIndexGate 从数据湖读创业板指与沪深300，算低吸的指数闸门。
//
// 数据来源与选股器算 RS 用的是同一条路径（`index_bars` / loadBenchmark）。
// 取不到时返回 `Ready=false` —— 让闸门明确判定为"不可用"，
// 而不是把 0 当成"指数在均线下方"把低吸建仓永久关死。
func (s *PortfolioService) loadLowBuyIndexGate(tradeDate int) score.LowBuyIndexGate {
	if s.stock == nil {
		return score.LowBuyIndexGate{Code: score.LowBuyIndexCode}
	}
	return s.stock.lowBuyIndexGateFor(tradeDate)
}

// lowBuyIndexGateFor 是闸门的实际实现（拆出来便于用真实数据湖做测试）。
func (s *StockService) lowBuyIndexGateFor(tradeDate int) score.LowBuyIndexGate {
	gate := score.LowBuyIndexGate{Code: score.LowBuyIndexCode}
	if tradeDate <= 0 {
		return gate
	}
	// 250 日均线要多取两年数据
	start := (tradeDate/10000-2)*10000 + 101

	indexCloses, err := s.loadBenchmark(score.LowBuyIndexCode, start, tradeDate)
	if err != nil || len(indexCloses) == 0 {
		return gate
	}
	benchCloses, err := s.loadBenchmark(score.LowBuyIndexBenchmarkCode, start, tradeDate)
	if err != nil || len(benchCloses) == 0 {
		return gate
	}

	indexSeries := sortedCloses(indexCloses, tradeDate)
	benchSeries := sortedCloses(benchCloses, tradeDate)
	if len(indexSeries) == 0 {
		return gate
	}

	gate.Close = indexSeries[len(indexSeries)-1]
	gate.Ma20 = trailingMean(indexSeries, 20)
	gate.Ma60 = trailingMean(indexSeries, 60)
	gate.Ma250 = trailingMean(indexSeries, 250)
	if len(benchSeries) > 0 {
		gate.BenchmarkClose = benchSeries[len(benchSeries)-1]
		gate.BenchmarkMa250 = trailingMean(benchSeries, 250)
	}
	// 收盘价与 20/60 日线都必须真的取到，否则不算 Ready
	gate.Ready = gate.Close > 0 && gate.Ma20 > 0 && gate.Ma60 > 0
	return gate
}

// sortedCloses 把 date→close 拍成"到 tradeDate 为止、按日期升序"的收盘序列。
func sortedCloses(byDate map[int64]float64, until int) []float64 {
	dates := make([]int64, 0, len(byDate))
	for date := range byDate {
		if int(date) <= until {
			dates = append(dates, date)
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })
	out := make([]float64, len(dates))
	for i, date := range dates {
		out[i] = byDate[date]
	}
	return out
}

// trailingMean 取序列末尾 period 个值的均值；不足 period 个返回 0。
func trailingMean(series []float64, period int) float64 {
	if period <= 0 || len(series) < period {
		return 0
	}
	sum := 0.0
	for _, value := range series[len(series)-period:] {
		sum += value
	}
	return sum / float64(period)
}

// HealthLabel 按组合评分给出健康度标签。
//
// 原实现 portfolio_service.py:226-234：强势 >=85 / 稳健 >=72 / 观察 >=58 / 风险 <58。
func HealthLabel(score float64) string {
	switch {
	case score >= 85:
		return "强势"
	case score >= 72:
		return "稳健"
	case score >= 58:
		return "观察"
	default:
		return "风险"
	}
}

// ApplyResult 是一次"一键应用建议"的结果。
type ApplyResult struct {
	PortfolioID   string                `json:"portfolioId"`
	PortfolioName string                `json:"portfolioName"`
	TradeDate     int                   `json:"tradeDate"`
	Applied       portfolio.ApplyResult `json:"applied"`
	// Changed 是应用前后持仓的对比（便于界面提示改了什么）
	Changed []AppliedChange `json:"changed"`
}

// AppliedChange 是一条实际改动。
type AppliedChange struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Action      string  `json:"action"`
	ActionLabel string  `json:"actionLabel"`
	FromLayers  int     `json:"fromLayers"`
	ToLayers    int     `json:"toLayers"`
	FromWeight  float64 `json:"fromWeight"`
	ToWeight    float64 `json:"toWeight"`
	Price       float64 `json:"price"`
	Reason      string  `json:"reason"`
}

// ApplyRotation 生成建议并**一键应用**到持仓。
//
// 流程（对齐参考实现 portfolio_service.apply_rotation）：
//  1. 组合类型不允许应用时直接拒绝（手动组合）
//  2. 重建当日建议
//  3. 过滤出可执行的动作
//  4. 逐股算目标层数（**硬编码 clamp 到 3**）
//  5. 写流水 + 整表重写持仓
//
// 与参考实现的差异：参考实现在应用后还会**再重建一次建议并覆盖四张快照表**。
// 本项目目前不落快照表（建议是实时算的），所以省掉这一步；等需要历史建议
// 追溯时再补。
func (s *PortfolioService) ApplyRotation(portfolioID string) (*ApplyResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		return nil, err
	}
	if !item.AllowApplyRotation {
		return nil, fmt.Errorf(
			"「%s」是%s，仅生成分析提醒，不支持一键应用建议",
			item.Name, item.TypeLabel)
	}

	plan, err := s.BuildPlan(portfolioID)
	if err != nil {
		return nil, err
	}

	// 价格表：持仓与候选都能查到现价
	priceByCode := map[string]float64{}
	// 持仓成本表：取不到现价时用它兜底（原实现 :906 回退成本价）
	entryByCode := map[string]float64{}
	for _, row := range plan.Holdings {
		if row.CurrentPrice > 0 {
			priceByCode[row.Code] = row.CurrentPrice
		}
		entryByCode[row.Code] = row.EntryPrice
	}
	for _, row := range plan.Candidates {
		if row.Close > 0 {
			priceByCode[row.Code] = row.Close
		}
	}

	// 应用前的层数，用于生成 Changed
	before := map[string]portfolio.Position{}
	if positions, err := store.ListPositions(portfolioID); err == nil {
		for _, position := range positions {
			before[position.Code] = position
		}
	}

	actions := make([]portfolio.ApplyAction, 0, len(plan.Plan.Rows))
	for _, row := range plan.Plan.Rows {
		// ⚠ **持仓行必须全部传入**，不能只传可执行的。
		//
		// Store.ApplyRotation 是"整表 DELETE 再 INSERT"（参考实现如此），
		// 只传可执行动作会把 keep / observe 的健康持仓**静默删除**。
		// 参考实现也是把 rotation_signal.action 原样透传（含 keep/observe）。
		//
		// 候选行（add / replace_in）必须可执行才传入 —— 它们不是已有持仓。
		isHolding := row.SourceType == "holding" || row.CurrentLayers > 0
		if !isHolding && !row.IsExecutable {
			continue
		}

		price := priceByCode[row.Code]
		if price <= 0 {
			// 拿不到现价：持仓行仍要保留（否则会被整表重写删掉），
			// 但**保留的方式是照原动作执行、用成本价兜底**，
			// 而不是把 clear/reduce 偷偷改成"保持不动"。
			//
			// 原实现 :896-899/:906/:912 —— `target` 只看 action；
			// `latest_price` 取不到时回退**成本价**，动作照执行、流水照写。
			//
			// 之前的写法是把 TargetLayers 覆盖成 CurrentLayers，但 **Action 仍是 clear**
			// → 落库时走"层数没变"分支：票没被清掉、界面却提示成功、
			// last_action 写成 'clear'、且本次零流水。
			if isHolding {
				fallback := row.TargetLayers
				if fallback < 1 && row.Action != score.ActionClear {
					fallback = row.CurrentLayers
				}
				actions = append(actions, portfolio.ApplyAction{
					Code: row.Code, Name: row.Name, Action: row.Action,
					TargetLayers: fallback,
					Price:        entryByCode[row.Code], // 成本价兜底（可能是 0）
					Reason:       row.Reason,
				})
			}
			continue
		}
		target := row.TargetLayers
		// 换入是新建仓：参考实现给 1 层
		if row.Action == score.ActionReplaceIn && target < 1 {
			target = 1
		}
		actions = append(actions, portfolio.ApplyAction{
			Code: row.Code, Name: row.Name, Action: row.Action,
			TargetLayers: target, Price: price, Reason: row.Reason,
		})
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("当前没有可应用的持仓")
	}
	// 至少要有一个真正改变状态的动作，否则"应用"没有意义
	hasChange := false
	for _, action := range actions {
		if portfolio.IsApplyExecutable(action.Action) && action.TargetLayers >= 0 {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return nil, fmt.Errorf("当前没有可执行的动作（建议都是继续持有或观察）")
	}

	tradeDate := fmt.Sprintf("%d", plan.StrengthDate)
	applied, err := store.ApplyRotation(portfolioID, tradeDate, actions)
	if err != nil {
		return nil, err
	}

	// 应用后的层数
	after := map[string]portfolio.Position{}
	if positions, err := store.ListPositions(portfolioID); err == nil {
		for _, position := range positions {
			after[position.Code] = position
		}
	}

	labelByAction := map[string]string{}
	for _, row := range plan.Plan.Rows {
		labelByAction[row.Action] = row.ActionLabel
	}

	changed := make([]AppliedChange, 0, len(actions))
	for _, action := range actions {
		code := portfolio.BareCode(action.Code)
		from, to := before[code], after[code]
		changed = append(changed, AppliedChange{
			Code: code, Name: firstNonEmptyStr(action.Name, from.Name, to.Name, code),
			Action: action.Action, ActionLabel: labelByAction[action.Action],
			FromLayers: from.PositionLayers, ToLayers: to.PositionLayers,
			FromWeight: from.PositionWeight, ToWeight: to.PositionWeight,
			Price: action.Price, Reason: action.Reason,
		})
	}

	slog.Info("建议已应用",
		"portfolio", item.Name, "tradeDate", tradeDate,
		"applied", applied.PositionCount, "cleared", applied.Cleared, "added", applied.Added)

	return &ApplyResult{
		PortfolioID: item.ID, PortfolioName: item.Name,
		TradeDate: plan.StrengthDate,
		Applied:   *applied,
		Changed:   changed,
	}, nil
}

func firstNonEmptyStr(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// StrengthSummary 是评分物化状态的摘要（界面显示用）。
type StrengthSummary struct {
	StrategyID string  `json:"strategyId"`
	LatestDate int64   `json:"latestDate"`
	Count      int     `json:"count"`
	Dates      []int64 `json:"dates"`
}

// GetStrengthSummary 返回评分物化状态。
func (s *PortfolioService) GetStrengthSummary(strategyID string) (*StrengthSummary, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	if strategyID == "" {
		strategyID = score.StrategyTrendGrowth
	}
	dates, err := store.StrengthDates(strategyID)
	if err != nil {
		return nil, err
	}
	out := &StrengthSummary{StrategyID: strategyID, Dates: dates}
	if len(dates) > 0 {
		out.LatestDate = dates[len(dates)-1]
		out.Count, err = store.StrengthCount(out.LatestDate, strategyID)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] > dates[j] })
	return out, nil
}
