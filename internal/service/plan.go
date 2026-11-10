package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
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
	// incremental 表示 `codes` 被裁成了"这次没算过的那部分"。
	// 它决定落库用**按主键覆盖**还是**整分区替换** —— 见 saveScoringResult。
	incremental := false
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
			incremental = true
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
	if err := s.saveScoringResult(store, result, strategyID, incremental); err != nil {
		return nil, err
	}
	total, _ := store.StrengthCount(int64(result.TradeDate), strategyID)
	slog.Info("评分完成",
		"tradeDate", result.TradeDate, "新增", len(result.Rows),
		"复用已有", reused, "含持仓", heldCount, "增量落库", incremental)
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
//
// incremental 标记"这次落库的 rows 只是该日该策略的**一部分代码**"：
//
//	true  → `UpsertStrength`（按主键覆盖这一批，当天其余行保留）
//	false → `SaveStrength`（整分区替换，rows 就是全集）
//
// ⚠ 这个参数**必须传对**。增量刷新只拿到"这次新算的代码"，
// 交给整分区替换会把当天已经算好的其余行**整片删掉** —— 那不是"少算几只"，
// 而是**数据丢失**：本机库就是这么坏掉的（多数交易日只剩 ~990 行，
// 正常 4400~7600 行），随后低吸广度就绪数被系统性低估、
// 回测/建议都判"广度不足"。
func (s *PortfolioService) saveScoringResult(
	store *portfolio.Store, result *ScoringResult, strategyID string, incremental bool,
) error {
	rows := make([]portfolio.StrengthRow, 0, len(result.Rows))

	// **行业归属优先用评分时已经算好的那一份**（`ScoringResult.IndustryByCode`）。
	//
	// 原来这里无条件再调一次 `boardsForCodes` —— 同一批代码、同一份板块数据，
	// 一天扫两遍全部板块成分（本机 1337 个板块）。回填是逐日调用，
	// 这一遍就是白花的（实测量级 20ms/天 × 天数）。
	// 只有在调用方没带（比如别处直接调这个函数）时才回退到现场查。
	industryByCode := result.IndustryByCode
	if industryByCode == nil {
		scoredCodes := make([]string, 0, len(result.Rows))
		for _, row := range result.Rows {
			scoredCodes = append(scoredCodes, row.Code)
		}
		industryByCode = map[string]string{}
		if membership, err := s.stock.boardsForCodes(scoredCodes); err == nil {
			for code, boards := range membership {
				if len(boards) > 0 {
					industryByCode[code] = boards[0]
				}
			}
		}
	}

	// 市场：评分行里已经带了（`ScoreStocks` 从同一个股票列表填的 Market），
	// 只有空值才回退去查股票列表 —— 同样是"别每天重建一张 7600 项的 map"。
	needsMarketFallback := false
	for _, row := range result.Rows {
		if row.Market == "" {
			needsMarketFallback = true
			break
		}
	}
	marketByCode := map[string]string{}
	if needsMarketFallback {
		if universe, err := s.stock.GetStockUniverse(false); err == nil {
			for _, item := range universe.Stocks {
				marketByCode[item.Code] = item.Market
			}
		}
	}
	marketOf := func(row score.Row) string {
		if row.Market != "" {
			return row.Market
		}
		return marketByCode[row.Code]
	}

	for _, row := range result.Rows {
		rows = append(rows, portfolio.StrengthRow{
			Code: row.Code, Name: row.Name,
			Industry: industryByCode[row.Code],
			Market:   marketOf(row),

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

	if incremental {
		if err := store.UpsertStrength(int64(result.TradeDate), strategyID, rows); err != nil {
			return err
		}
	} else if err := store.SaveStrength(int64(result.TradeDate), strategyID, rows); err != nil {
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

// scoringUniverse 决定这次要评哪些股票：股票列表里抽样的 limit 只 + 所有持仓代码。
//
// 持仓必须无条件纳入 —— 候选池是从评分表里选的，如果持仓不在表里，
// 参考实现"左连接落空 → 现价记 0"就会让所有持仓评分变 0，
// 轮动建议直接失去意义。
//
// ⚠ **limit 是"等距抽样"，不是"取前 limit 只"。**
//
// `GetStockUniverse` 返回的列表是**按代码升序**的（stock.go 里
// `sort.Slice(targets, code <)`）。直接取前 limit 只等于**只评代码最小的那一段**：
// limit=1000 时命中的全是 000/001/002（深市主板），
// **创业板（300/301）、科创板（688/689）、沪市主板（600/603）、北交所（920）一只都不评**。
//
// 实测本机库就是这个后果：用 1000 回填出来的交易日只有 1000 行，
// 代码前缀分布 `000:412 / 001:115 / 002:473`；而全市场日有 4517 行，
// 覆盖 000/001/002/003/300/301/302/600/601/603/605/688/689/920。
//
// 危害不是"少评几千只"，而是**范围整体偏了**：
//   - 组合设了「主板+创业板」时，创业板候选恒为空
//   - 低吸广度就绪数（close>MA60 且 MA20>MA60 的只数）被系统性低估 →
//     恒低于 900 的清仓线 → 清仓 + 禁止开新仓 → **回测恒为 0 成交**
//   - 非低吸的广度闸门同样失真
//
// 生产路径应当传 **0（全市场）**；传非 0 时也要是**跨板块的代表性样本**，
// 所以这里改成等距抽样。
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

	// 1) 从"有行情"的股票里抽样 limit 只（limit<=0 = 全市场）
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil {
		return nil, 0, err
	}
	eligible := make([]string, 0, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if item.HasQuote {
			eligible = append(eligible, item.Code)
		}
	}
	for _, code := range sampleUniverse(eligible, limit) {
		add(code)
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

// sampleUniverse 从**按代码升序**的全市场列表里取出 limit 只。
//
//	limit <= 0 或 >= len(eligible) → 全量返回
//	否则 → **等距抽样**：把有序列表均分 limit 段，每段取起点
//
// 为什么不能"取前 limit 只"：列表是按代码升序的，前 1000 只全是
// 000/001/002（深市主板）—— 创业板 300/301、科创板 688、沪市主板 600/603、
// 北交所 920 **一只都不在里面**。于是：
//   - 组合设「主板+创业板」时候选恒为空
//   - 低吸广度（全市场 close>MA60 且 MA20>MA60 的只数）被系统性低估
//     → 恒判「广度不足 < 900」→ 清仓 + 不许开新仓 → **回测恒为 0 成交**
//
// 等距抽样保持"评 N 只"的语义，同时让板块分布与全市场一致。
func sampleUniverse(eligible []string, limit int) []string {
	if limit <= 0 || limit >= len(eligible) {
		return eligible
	}
	out := make([]string, 0, limit)
	stride := float64(len(eligible)) / float64(limit)
	for i := 0; i < limit; i++ {
		idx := int(float64(i) * stride)
		if idx >= len(eligible) {
			break
		}
		out = append(out, eligible[idx])
	}
	return out
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
	// 资金口径（质量成长）：当前权益、现金
	MoneyEquity float64 `json:"moneyEquity,omitempty"`
	MoneyCash   float64 `json:"moneyCash,omitempty"`
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

	// 质量成长走独立的数据与计划口径（PIT 财报 + 权重制），不复用低吸的
	// stock_strength 轮动逻辑。
	if item.StrategyID == portfolio.StrategyQualityGrowth {
		return s.buildQualityGrowthPlan(item, store)
	}

	strategyID := item.StrategyID
	// ⚠ **全表**最新评分日，不是该策略自己的 —— 原实现
	// `get_rotation_payload`（portfolio_service.py:839）与 `apply_rotation`（:862）
	// 都是先 `_get_latest_strength_trade_date()`（`MAX(trade_date)` 不带 strategy_id），
	// 再按策略去取那一帧（`_latest_strength_frame(latest_trade_date, strategy_id)`）。
	strengthDate, err := store.LatestStrengthDateAny()
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
		if ok && !inPortfolioScopeStrength(strength, item, matcher) {
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
		// 现价用**不复权**价：评分表里的 Close 是复权价，和行情软件对不上
		cur := strength.Close
		if v, ok := s.rawCloseFor(position.Code, int(strengthDate)); ok {
			cur = v
		}
		row.CurrentPrice = cur
		if position.EntryPrice > 0 && cur > 0 {
			row.ProfitLossPct = round4((cur/position.EntryPrice - 1) * 100)
		}
		// 持仓期最高价与交易日数：需要历史，逐只查（持仓通常只有几只）
		row.HighestPrice, row.HoldingTradeDays = s.holdingExtremes(
			position.Code, position.EntryDate, int(strengthDate), position.EntryPrice, cur)
		holdingRows = append(holdingRows, row)
	}
	if missing > 0 {
		notes = append(notes, fmt.Sprintf(
			"有 %d 只持仓在评分表里没有记录（停牌/数据不足），它们的评分按 0 计", missing))
	}

	// ---- 低吸广度就绪数 ----
	//
	// ⚠ **必须在建候选池之前算出来并写进 cfg**。
	//
	// 它同时驱动两件事，而且两处都发生在候选池里：
	//   - 候选池的低吸质量过滤 `isLowBuyCandidate` 用 dd20 档位（>=1200 紧档 -18~-1.5）
	//   - 计划里的动态目标只数 / 每只层数 / 强制清仓阈值（< 900）
	//
	// 原实现把这些值挂在**每一行**上（`strength_df["low_buy_breadth_ready_count"]`，
	// portfolio_rotation_logic.py:2044），所以候选池过滤时行里已经有正确值。
	// 此前 Go 把它写在 `buildCandidatePool` **之后** —— 候选池那一遍
	// 是按"广度 0"的宽档 dd20 过滤的，与原实现不是同一个集合。
	breadthReady, err := store.LowBuyBreadthReadyCount(strengthDate, strategyID)
	if err != nil {
		return nil, err
	}
	cfg.LowBuyBreadthReadyCount = breadthReady

	// ---- 候选池 ----
	// 第二个返回值是**截断后**的池（喂给调仓计划），第三个是**截断前**的完整池
	//（留档与报数用）—— 两者都是原实现里的东西，见 `buildCandidatePoolFrom` 尾部注释。
	candidates, universeScores, fullPool, err := s.buildCandidatePool(store, item, cfg, marketState, strengthDate, holdingCodes)
	if err != nil {
		return nil, err
	}

	// ---- 跑轮动 ----
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
		// `candidate_pool` 是**截断前**的完整池（原实现 :2080 把
		// `candidate_pool` 与 `candidate_ranked` 分开）：留档表与
		// `candidate_count` 都用它，而 `plan` 内部建仓/配对用截断后的那个。
		Candidates: fullPool,
		Notes:      notes,
	}
	// 落档失败**不影响建议本身** —— 记日志即可，
	// 不要因为存档问题让用户看不到建议。
	if err := s.savePlanSnapshots(store, item, result); err != nil {
		slog.Warn("写入调仓建议留档失败", "portfolio", item.ID, "err", err)
	}
	// 所有策略都按实际本金折算目标股数/金额（失败不影响建议本身）
	_ = s.ApplyMoneyToPlan(item.ID, result)
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
) ([]score.Row, []float64, []score.Row, error) {
	all, err := store.LoadStrength(strengthDate, item.StrategyID, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	// 物化评分 → 轮动行，**只在这里转一次**：转换会带上 Industry/TradeDate，
	// 之后整条链路都在 `score.Row` 上走，不再有丢字段的往返。
	rowsIn := make([]score.Row, 0, len(all))
	for _, strength := range all {
		rowsIn = append(rowsIn, strengthToRow(strength))
	}
	rows, scores, fullPool := s.buildCandidatePoolFrom(store, rowsIn, item, cfg, holdingCodes)
	// 展示用现价换成**不复权**价（评分表里是复权价）；筛选/打分已在上一步完成，
	// 这里只改展示字段，不影响选股结果。
	for i := range rows {
		if v, ok := s.rawCloseFor(rows[i].Code, int(strengthDate)); ok {
			rows[i].Close = v
		}
	}
	for i := range fullPool {
		if v, ok := s.rawCloseFor(fullPool[i].Code, int(strengthDate)); ok {
			fullPool[i].Close = v
		}
	}
	return rows, scores, fullPool, nil
}

// buildCandidatePoolFrom 用**已加载好的评分行**建候选池。
//
// 抽出来是因为**回测要逐日复用同一段逻辑** —— 它不能每天去查一次库
// （回测要把整个区间的评分一次性读出来）。
// `stored` 用来读 `signal_history`：原实现在范围过滤之后、广度统计之前做了
// `_attach_signal_candidates`（portfolio_service.py:2469），所以这里也必须
// **先贴信号、再统计 universeScores** —— attach 可能丢弃不在交易日历里的行，
// 顺序颠倒会让广度闸门用的集合与 Python 不同。
// 传 nil 表示不再额外贴信号（**行上已经带了** —— 回测会按区间先贴一次）。
//
// ⚠ 参数类型是 `[]score.Row` 而**不是** `[]portfolio.StrengthRow`：
// 后者会迫使调用方在 `score.Row → StrengthRow → score.Row` 之间往返，
// 而那条往返会**静默丢字段**（`Industry` 与整套 `active_signal_*`）。
// 丢 `Industry` → 行业范围从"单值 OR 成员表并集"退化成"只靠成员表"；
// 丢信号 → 熊市闸门把每一只候选都判成"无新鲜信号"而拒。
func (s *PortfolioService) buildCandidatePoolFrom(
	stored *portfolio.Store,
	all []score.Row,
	item portfolio.Portfolio,
	cfg score.StrategyConfig,
	holdingCodes []string,
) ([]score.Row, []float64, []score.Row) {

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
	scoped := make([]score.Row, 0, len(all))
	for _, row := range all {
		// 范围过滤（行业/概念走数据湖成员表，与持仓侧同一口径）
		switch item.ScopeType {
		case portfolio.ScopeManualCodes:
			if !scopeCodes[row.Code] {
				continue
			}
		case portfolio.ScopeIndustry:
			if !scopeHit(scope.Items, row.Code, row.Industry, matcher, true) {
				continue
			}
		case portfolio.ScopeConcept:
			if !scopeHit(scope.Items, row.Code, row.Industry, matcher, false) {
				continue
			}
		}
		// 市场过滤
		//
		// ⚠ **必须走 `score.MarketInScope`**：`row.Market` 存的是本项目
		// `market.KeyOf` 的 key（sh-main / sz-main / chinext / star / bse / ...），
		// 而组合范围的 `markets` 是原实现词汇（main / gem / star）。
		// 直接 `!markets[row.Market]` 会把**每一行**都判成范围外
		// → 候选池恒为空 → 回测零成交、自动组合报"没有通过质量过滤的候选"。
		if !score.MarketInScope(row.Market, scope.Markets) {
			continue
		}
		scoped = append(scoped, row)
	}

	// ---- ①.5 信号 attach（原实现 :2469，在广度统计之前）----
	//
	// `scoped` 本身就是 `[]score.Row`，不再有"转回 StrengthRow 再转回来"的往返。
	scopedRows := scoped
	scopedDates := make([]int64, 0, len(scopedRows))
	for _, row := range scopedRows {
		scopedDates = append(scopedDates, row.TradeDate)
	}
	scopedRows = s.attachSignalsToRows(stored, scopedRows, scopedDates)

	// 广度闸门用的是**贴完信号之后**的帧（见函数注释）
	universeScores := make([]float64, 0, len(scopedRows))
	for _, row := range scopedRows {
		universeScores = append(universeScores, row.TotalScore)
	}

	// ---- ② 候选池过滤（剔 ST、剔已持仓、分数下限）----
	out := make([]score.Row, 0, len(scopedRows))
	for _, row := range scopedRows {
		if held[row.Code] {
			continue
		}
		// 剔除 ST（参考实现按名称含 "ST" 判断）
		if strings.Contains(strings.ToUpper(row.Name), "ST") {
			continue
		}
		// 分数下限
		if row.TotalScore < cfg.CandidateScoreFloor {
			continue
		}
		out = append(out, row)
	}

	// **先无条件排序、再做策略重排。**
	//
	// 原实现（portfolio_rotation_logic.py:1728）是
	// `prioritize_trend_growth_entry_points(_candidate_sort_frame(work))` ——
	// `_candidate_sort_frame` 在最里层，**对每一种策略都跑**。
	//
	// Go 此前整个漏掉了这一步：`score.sortCandidates` 早就实现了那八个排序键，
	// 但**生产代码从没调用过**（只有测试引用）。后果分两档：
	//   - 超跌/高手：`PrioritizeEntryPoints` 对它们是原样返回，于是候选池
	//     实际按 `LoadStrength` 的 `ORDER BY total_score DESC, ts_code` 排 ——
	//     总分相同（总分是 round(2)，同分很常见）时，谁进 `head(80)`、
	//     配对配到哪只，都跟原版不同；
	//   - 趋势成长/低吸：后续重排是策略相关的，但**重排本身是稳定排序**，
	//     平局时保留的就是这一步的顺序 —— 少了它，平局的名次与原版不同。
	//
	// 位置必须在 `PrioritizeEntryPoints` **之前**（原版就是嵌套在最里层）。
	out = score.SortCandidates(out)

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

	// ---- ④ head 截断 ----
	//
	// ⚠ **截断只影响"喂给调仓计划的候选"，不影响对外的候选数。**
	//
	// 原实现里这是两个东西（portfolio_rotation_logic.py:2072-2080）：
	//
	//	candidate_pool   = 过滤后的完整池        ← 返回给调用方、`candidate_count` 用它
	//	candidate_ranked = candidate_pool.head() ← 内部建仓/配对只用它
	//
	// Go 此前只留了截断后的那个，于是 `candidateCount` 报的是 80（或 target*12），
	// 而原版报的是完整池（全市场范围能到几千）。界面"候选池 N"因此差一个量级。
	fullPool := out
	head := 80
	if size := item.TargetPositionCount * 12; size > head {
		head = size
	}
	if len(out) > head {
		out = out[:head]
	}
	return out, universeScores, fullPool
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
func inPortfolioScope(row score.Row, item portfolio.Portfolio, matcher *ScopeMatcher) bool {
	return inScopeFields(row.Code, row.Industry, row.Market, item, matcher)
}

// inPortfolioScopeStrength 同上，但吃**物化评分行**。
//
// 给"过滤阶段"用（`buildStrengthDays` 逐日过滤持仓、自动组合的范围过滤）：
// 那里只有 `portfolio.StrengthRow`，而转成 `score.Row` 要整行拷贝
// （回填是每天数千行 × 上千天）。这里只需要 Code/Industry/Market 三个字段，
// 所以两边共用 `inScopeFields` —— **判定逻辑只有一份**，不会漂移。
func inPortfolioScopeStrength(
	strength portfolio.StrengthRow, item portfolio.Portfolio, matcher *ScopeMatcher,
) bool {
	return inScopeFields(strength.Code, strength.Industry, strength.Market, item, matcher)
}

// inScopeFields 是范围判定的薄包装（自解析范围值）。
//
// ⚠ **逐行调用时不要用它**，用 `inScopeFieldsParsed` 把解析提到循环外。
//
// 注意别夸大这个优化：`ParseScopeValue` 是 JSON 反序列化，逐行调用确实是浪费
// （回测 1 年全市场 = 125 万次），但它**不是**"回测慢"的根因 ——
// 实测把它提出来之后"组装评分日"耗时几乎没变（那一项的大头是
// `s.scopeMatcher()` 首次加载数据集的一次性开销）。定位性能问题要看分阶段计时。
func inScopeFields(
	code, industry, market string, item portfolio.Portfolio, matcher *ScopeMatcher,
) bool {
	return inScopeFieldsParsed(code, industry, market, item.ScopeType,
		portfolio.ParseScopeValue(item.ScopeValueJSON), matcher)
}

// inScopeFieldsParsed 是范围判定的唯一实现：范围值**由调用方预先解析**。
func inScopeFieldsParsed(
	code, industry, market string, scopeType string, scope portfolio.ScopeValue,
	matcher *ScopeMatcher,
) bool {
	switch scopeType {
	case portfolio.ScopeManualCodes:
		for _, want := range scope.Codes {
			if want == code {
				return score.MarketInScope(market, scope.Markets)
			}
		}
		return false
	case portfolio.ScopeIndustry:
		if !scopeHit(scope.Items, code, industry, matcher, true) {
			return false
		}
	case portfolio.ScopeConcept:
		if !scopeHit(scope.Items, code, industry, matcher, false) {
			return false
		}
	}

	// 市场范围：空 = 全部市场
	//
	// ⚠ 同样必须经 `score.MarketInScope` 映射（理由见 `buildCandidatePoolFrom`）。
	// 这里判错的话，范围**内**的持仓会被当成"左连接落空"→ 评分/现价记 0
	// → 实盘建议会把所有持仓判成弱持仓清仓/换出。
	return score.MarketInScope(market, scope.Markets)
}

// scopeHit 判定股票是否命中范围里的**任意一个**行业/概念名。
//
// ⚠ **行业与概念的口径不一样**，必须分开实现 —— 这是原实现
// `_scope_filter`（portfolio_service.py:3284-3324）里两段不同的代码：
//
//	# 行业：单值行业名 OR 成员表命中 —— 是**并集**
//	filtered = out[out["industry"].isin(board_names)
//	               | out["ts_code"].str.upper().isin(board_codes_by_stock)]
//
//	# 概念：**只看成员表**；成员表为空 → 空帧
//	if board_df.empty:
//	    filtered = out.iloc[0:0].copy()
//	else:
//	    filtered = out[out["ts_code"].str.upper().isin(allowed)]
//
// 此前 Go 把两者混成一个函数，且 `useMembers` 为真时**丢掉了单值那一支**
// （`continue` 跳过了 `strength.Industry == want`）→ 行业范围少选一批票；
// 概念在成员表不可用时又**退回按行业名单值匹配** → 概念范围多选一批票。
//
// items 为空 → 返回 false（匹配不到任何票），与原实现的空集一致。
//
// 已知未实现：原实现还允许用**板块代码**（`board_code`）匹配 items，
// 而本项目的 `ScopeMatcher` 只按名称建索引。界面的范围选择器传的是板块名，
// 所以这条路径实际不可达；若以后要支持，需要给成员表补一份 code 索引。
func scopeHit(items []string, code, industry string, matcher *ScopeMatcher, industryScope bool) bool {
	if len(items) == 0 {
		return false
	}
	if industryScope {
		// 并集：单值行业名命中即可，不必依赖成员表
		useMembers := matcher.HasIndustryData()
		for _, want := range items {
			if industry == want {
				return true
			}
			if useMembers && matcher.HasIndustry(code, want) {
				return true
			}
		}
		return false
	}
	// 概念：成员表不可用 → **匹配不到任何票**（原实现返回空帧）
	if !matcher.HasSectorData() {
		return false
	}
	for _, want := range items {
		if matcher.HasSector(code, want) {
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
		Industry:   strength.Industry,
		TradeDate:  strength.TradeDate,
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
// ⚠ **`entry_date` 无效时是 0，不是 1。** 原实现（portfolio_service.py:3208）是
//
//	merged["holding_trade_days"] = np.where(
//	    merged["entry_date"].astype(str).str.strip().ne(""), 1, 0)
//
// 而且前面还有一道 `_normalize_trade_date`（:3206）—— 非空但解析不出来的字符串
// 会被**归一化成空串**，于是也走 0 那一支。Go 原来在"空日期"和"解析失败"两条
// 早退路径上返回 1，两条都错。
//
// 后果不是差一个数：`holding_trade_days` 会喂给
// `should_quick_profit_take`（要求 1<=days<=2）、
// `isBullTrendContinuationAddSignal`（要求 >=2/4）、
// `canAddSecondLayer` 的 `current_layers>=2 && holding_days<3` —— 脏数据持仓
// 在 Go 里会因为 days=1 而**真的产出 reduce/clear 动作**，原版不会。
//
// 逐只查历史：持仓通常只有几只，成本可接受。
func (s *PortfolioService) holdingExtremes(code, entryDate string, endDate int, entryPrice, currentPrice float64) (float64, int) {
	highest := entryPrice
	if currentPrice > highest {
		highest = currentPrice
	}
	// 日期无效 → 0（原实现：空串或归一化后为空 → 0）。详见函数注释。
	if strings.TrimSpace(entryDate) == "" {
		return highest, 0
	}
	if s.stock == nil {
		return highest, 1
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return highest, 1
	}
	start := 0
	fmt.Sscanf(entryDate, "%d", &start)
	if start <= 0 || start > endDate {
		// 非空但解析不出来 —— 原版的 `_normalize_trade_date` 也会把它变成空串 → 0
		return highest, 0
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
	return lowBuyIndexGateFrom(indexCloses, benchCloses, tradeDate)
}

// lowBuyIndexGateFrom 用**已经取好的**两条收盘序列算某一天的低吸指数闸门。
//
// 从 `lowBuyIndexGateFor` 里抽出来，是为了**回测能一次取数、逐日算闸门** ——
// 否则 240 个交易日要打 480 次数据源（每年两次 2 年窗口的指数日线）。
// 计算本身与单日版**逐字相同**，不会有口径差异。
func lowBuyIndexGateFrom(
	indexCloses, benchCloses map[int64]float64, tradeDate int,
) score.LowBuyIndexGate {
	gate := score.LowBuyIndexGate{Code: score.LowBuyIndexCode}
	if tradeDate <= 0 || len(indexCloses) == 0 || len(benchCloses) == 0 {
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

// lowBuyIndexGatesForRange 批量算多日的低吸指数闸门（回测用）。
//
// 取一次指数日线（窗口 = 最早一天往前推两年 起，到最晚一天），
// 再对每一天调 `lowBuyIndexGateFrom`。取不到数据时返回空 map ——
// 调用方据此拿到"未就绪"的零值闸门，也就是**关闸**（与原实现
// "指数数据取不到 → index_entry_allowed=false" 一致）。
//
// 键是 YYYYMMDD 字符串（与回测的交易日表示一致）。
func (s *StockService) lowBuyIndexGatesForRange(dates []string) map[string]score.LowBuyIndexGate {
	out := map[string]score.LowBuyIndexGate{}
	if s == nil || len(dates) == 0 {
		return out
	}
	parsed := make([]int, 0, len(dates))
	for _, date := range dates {
		if value, err := strconv.Atoi(date); err == nil && value > 0 {
			parsed = append(parsed, value)
		}
	}
	if len(parsed) == 0 {
		return out
	}
	sort.Ints(parsed)
	first, last := parsed[0], parsed[len(parsed)-1]
	start := (first/10000-2)*10000 + 101

	indexCloses, err := s.loadBenchmark(score.LowBuyIndexCode, start, last)
	if err != nil || len(indexCloses) == 0 {
		return out
	}
	benchCloses, err := s.loadBenchmark(score.LowBuyIndexBenchmarkCode, start, last)
	if err != nil || len(benchCloses) == 0 {
		return out
	}
	for _, date := range parsed {
		out[strconv.Itoa(date)] = lowBuyIndexGateFrom(indexCloses, benchCloses, date)
	}
	return out
}

// lowBuyBreadthReadyCountOf 从**当日评分行**数低吸广度就绪数。
//
// 与 `Store.LowBuyBreadthReadyCount` 的 SQL 口径**逐字一致**
// （close>0 且 ma60>0 且 close>ma60 且 ma20>ma60）。抽成内存版是因为
// 回测已经把整个区间的评分读进来了 —— 再按天回查一次库既慢又没必要。
//
// ⚠ 必须传**当日评分行**（`day.Rows`），不能混入"持仓但当天没评分"的行 ——
// 那些行的价格/均线是 0，会被 SQL 口径直接排除，混进来只是白算。
func lowBuyBreadthReadyCountOf(rows []score.Row) int {
	count := 0
	for _, row := range rows {
		if row.Close > 0 && row.Ma60 > 0 && row.Close > row.Ma60 && row.Ma20 > row.Ma60 {
			count++
		}
	}
	return count
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
	return s.applyRotationWithPlan(portfolioID, nil)
}

// ApplyRotationAt 用**指定日期**的建议应用调仓（补做机制用）。
//
// 与 ApplyRotation 的唯一区别：计划不是按最新交易日算，而是按 date 那天的
// 数据算 —— 节假日/数据延迟导致月末没调仓时，用它补上那一次。
func (s *PortfolioService) ApplyRotationAt(portfolioID string, date int) (*ApplyResult, error) {
	return s.applyRotationWithPlan(portfolioID, func(item portfolio.Portfolio, store *portfolio.Store) (*PlanResult, error) {
		return s.buildQualityGrowthPlanAt(item, store, date)
	})
}

func (s *PortfolioService) applyRotationWithPlan(
	portfolioID string,
	planFn func(item portfolio.Portfolio, store *portfolio.Store) (*PlanResult, error),
) (*ApplyResult, error) {
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

	var plan *PlanResult
	if planFn != nil {
		plan, err = planFn(item, store)
	} else {
		plan, err = s.BuildPlan(portfolioID)
	}
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
	// 应用后按新层数换算整手股数、重算现金（资金随盈亏变化）
	_ = s.syncMoneyFor(portfolioID, item.StrategyID)

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
