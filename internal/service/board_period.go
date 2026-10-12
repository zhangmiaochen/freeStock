package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 板块区间（多周期）行情
//
// 板块的**当日**行情只需要成分股的当日行情 —— 那份数据在后端内存里就有（见 board.go）。
// 而「最近 N 个交易日的板块涨幅」必须回看历史：数据源里既没有板块指数，
// 也没有板块历史行情，只有**当前**的成分名单，所以只能由成分股的区间涨跌幅聚合。
//
// 口径（与当日口径保持一致）：
//   - 区间涨跌幅 = 成分股区间涨跌幅的**流通市值加权**，同时给等权值；
//   - 区间涨跌家数按成分股区间涨跌幅的正负号统计；
//   - 区间涨跌幅 = 最新收盘价 ÷ 基准日收盘价 - 1，基准日是区间起点的**前一交易日**。
//
// 三处**有意的近似**，界面必须把话说清楚，否则数字会被误读：
//
//  1. 用**不复权收盘价**算区间涨跌幅。除权除息日价格自然回落，会被算成下跌。
//     要做复权得逐只取复权因子（全市场 5000+ 次请求），代价远大于收益 ——
//     个股除权当天的影响通常只有百分之几，在板块层面更小。
//  2. 权重用**最新**流通市值，不是区间起点的市值：数据源不提供历史市值
//     （数据湖的 valuation_metrics 只有最新一天，见 provider.go 的说明）。
//  3. 成分名单只有**当前**这一份。区间内被剔除出板块的股票仍按当前名单计算，
//     这与条件选股的板块口径是同一个已知限制。
//
// 另外**没有**区间成交额与区间换手率：那需要把区间内每一天的成交额都取回来，
// 而 free-stockdb 的区间查询会把每条记录的 21 个字段全返回（实测 106 个交易日
// = 34KB/只、全市场 190MB、16 秒），代价是"只取几个基准日收盘价"的 4 倍以上。
// 成交额与换手率仍然按**当日**给出（见 board.go），热度看当日就够了。
// ---------------------------------------------------------------------------

// DefaultBoardPeriods 是界面默认展示的周期（单位：交易日）。
var DefaultBoardPeriods = []int{5, 10, 15, 30, 60}

const (
	// minBoardPeriod 是最短的区间周期。
	//
	// 1 个交易日就是「当日」，走日行情那条路（成分股行情已在内存里），
	// 不需要也不应该回看历史 —— 所以周期从 2 起，1 会被 NormalizeBoardPeriods 丢掉。
	minBoardPeriod = 2
	// maxBoardPeriod 是最长的区间周期（约一年交易日）。
	//
	// 再长下去取数成本与内存都线性增长，而"一年以上的区间涨跌"对板块轮动的
	// 参考价值已经很低。
	maxBoardPeriod = 250

	// boardPeriodWindowFactor / boardPeriodWindowExtra 决定取数窗口的自然日长度：
	//
	//	窗口 = 周期 × 2 + 40（自然日）
	//
	// 周期按**交易日**算，取数窗口按**自然日**算，中间隔着周末与长假：
	// 60 个交易日实测需要约 95 个自然日（本机 160 天窗口里恰好 106 个交易日）。
	// 留 2 倍加 40 天既保证一定够，又不会白拿一大段用不上的交易日历。
	boardPeriodWindowFactor = 2
	boardPeriodWindowExtra  = 40

	// boardPeriodTTL 是基准日收盘价缓存的存活时间。
	//
	// ⚠ 比 `universeTTL`（10 分钟）长得多是**有意的**：缓存里只有**历史**收盘价，
	// 它们不会再变（当日收盘价每次都从股票列表快照里取，那份有自己的 TTL）。
	// 取一次全市场五个基准日要好几秒，10 分钟就过期会让每次进板块页都要重新等；
	// 而交易日一变（`tradeDate` 字段）缓存立刻作废，不会有跨日串味。
	boardPeriodTTL = 6 * time.Hour
)

// NormalizeBoardPeriods 把界面传来的周期列表规范化：丢弃当日（<2）、升序、去重、限幅。
//
// 空列表（或全被过滤掉）时回落到 DefaultBoardPeriods ——
// 界面漏传参数不该变成"没有区间行情"。
func NormalizeBoardPeriods(periods []int) []int {
	seen := make(map[int]struct{}, len(periods))
	out := make([]int, 0, len(periods))
	for _, days := range periods {
		if days < minBoardPeriod {
			continue
		}
		if days > maxBoardPeriod {
			days = maxBoardPeriod
		}
		if _, exists := seen[days]; exists {
			continue
		}
		seen[days] = struct{}{}
		out = append(out, days)
	}
	if len(out) == 0 {
		out = append(out, DefaultBoardPeriods...)
	}
	sort.Ints(out)
	return out
}

// BoardPeriodWindow 是一个周期的日历窗口。它与具体板块无关（只由交易日历决定），
// 所以放在结果这一层，全部分类共用。
//
// 为什么要单独一层：某个板块可能一个成分都算不出区间涨跌幅（全是次新股/停牌），
// 界面仍然需要知道这个周期的起止日期来给出表头与提示。
type BoardPeriodWindow struct {
	Days  int    `json:"days"`
	Label string `json:"label"`

	// BaseDate 是基准日：区间起点的**前一个交易日**，区间涨跌幅以它的收盘价为基准。
	BaseDate  int64 `json:"baseDate"`
	StartDate int64 `json:"startDate"`
	EndDate   int64 `json:"endDate"`

	// Available 为 false 表示历史交易日不够这个周期（例如数据源只同步了最近 20 天）。
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
}

// BoardPeriodStat 是一个板块在某个周期内的行情。
type BoardPeriodStat struct {
	Days int `json:"days"`

	// PctChg 是流通市值加权区间涨跌幅 %，PctChgEqual 是等权区间涨跌幅 %。
	PctChg      float64 `json:"pctChg"`
	PctChgEqual float64 `json:"pctChgEqual"`

	UpCount   int `json:"upCount"`
	DownCount int `json:"downCount"`
	FlatCount int `json:"flatCount"`

	// QuotedCount 是**能算出这个区间涨跌幅**的成分数：它需要成分股在区间起点
	// 之前就有行情。次新股 / 区间内一直停牌 / 未同步的成分会被排除在外，
	// 所以它与 MemberCount 不等是正常的，界面要能看出差在哪。
	QuotedCount int `json:"quotedCount"`
	MemberCount int `json:"memberCount"`

	// Leader / Laggard 是区间涨跌幅最高 / 最低的成分股（PctChg 是**区间**涨跌幅）。
	Leader  *BoardMemberQuote `json:"leader"`
	Laggard *BoardMemberQuote `json:"laggard"`
}

// periodReturn 是一只股票在**一个**周期上的区间涨跌结果。
type periodReturn struct {
	pctChg float64 // 区间涨跌幅 %
	base   float64 // 基准日收盘价
	close  float64 // 最新收盘价
}

// periodReturnFromCloses 用基准日与最新收盘价算区间涨跌幅。
//
// ok=false 表示**算不出来**（缺少基准价：次新股 / 区间内一直停牌且回看窗口内
// 没有数据 / 未同步），而不是"区间涨跌幅是 0"。
// 这个区别很重要：把次新股按 0 摊进等权平均会系统性压低板块涨幅。
func periodReturnFromCloses(base, latest float64) (periodReturn, bool) {
	if base <= 0 || latest <= 0 {
		return periodReturn{}, false
	}
	return periodReturn{
		pctChg: round((latest-base)/base*100, 4),
		base:   base,
		close:  latest,
	}, true
}

// boardPeriodCache 是"若干基准日 × 全市场"的收盘价缓存。
//
// 为什么要缓存：界面上的周期可以随时切换（5 日 → 30 日 → 自定义），
// 而每个周期都需要一个自己的基准日收盘价。缓存之后，切周期只补**缺的那个基准日**，
// 切分类则完全不取数。
type boardPeriodCache struct {
	// gen 是数据源代次：切换数据源后作废（旧源的收盘价不能拿来算新源的区间涨跌）
	gen       int64
	tradeDate int
	builtAt   time.Time

	// calendarFrom/calendarTo 是 calendar 覆盖的自然日区间
	calendarFrom, calendarTo int
	calendar                 []int

	// closes 是 基准日 → 代码 → 收盘价，按需增量填充
	closes map[int]map[string]float64
}

// boardPeriodIndex 是全市场在若干周期上的区间涨跌（按 周期 → 代码 索引）。
type boardPeriodIndex struct {
	periods []int
	returns map[int]map[string]periodReturn
	windows []BoardPeriodWindow
}

// boardPeriodData 返回全市场在 periods 各周期上的区间涨跌。
//
// latest 是"当前交易日各标的的收盘价"（就是股票列表里那份行情，含停牌回溯）。
// 由调用方传进来而不是在这里再查一次：同一份快照里取数才能保证当日列与区间列
// 说的是同一天，也少一次全市场查询。
func (s *StockService) boardPeriodData(periods []int, tradeDate int, latest map[string]float64) (*boardPeriodIndex, error) {
	windows, returns, err := s.periodReturns(periods, tradeDate, latest)
	if err != nil {
		return nil, err
	}
	return &boardPeriodIndex{periods: periods, returns: returns, windows: windows}, nil
}

// periodReturns 取出各周期的窗口与全市场区间涨跌（带缓存）。
func (s *StockService) periodReturns(periods []int, tradeDate int, latest map[string]float64) ([]BoardPeriodWindow, map[int]map[string]periodReturn, error) {
	if tradeDate <= 0 {
		return nil, nil, fmt.Errorf("还没有可用的交易日，无法计算区间行情")
	}

	// 全程持锁：并发请求时只取一次数，其它 goroutine 等它完成即可。
	//（与数据湖的 fillBulk 同一个理由：重复取一遍是几千次请求的浪费。）
	s.periodMu.Lock()
	defer s.periodMu.Unlock()

	codeCache, err := s.periodClosesLocked(periods, tradeDate)
	if err != nil {
		return nil, nil, err
	}

	windows, baseDates := boardPeriodWindows(codeCache.calendar, periods)

	returns := make(map[int]map[string]periodReturn, len(periods))
	for _, days := range periods {
		returns[days] = make(map[string]periodReturn, len(codeCache.closes[baseDates[days]]))
	}
	for _, days := range periods {
		baseDate := baseDates[days]
		if baseDate <= 0 {
			continue
		}
		for code, base := range codeCache.closes[baseDate] {
			close, ok := latest[code]
			if !ok {
				continue
			}
			if ret, ok := periodReturnFromCloses(base, close); ok {
				returns[days][code] = ret
			}
		}
	}
	return windows, returns, nil
}

// periodClosesLocked 保证缓存里有这批周期所需的基准日收盘价（调用方持 periodMu）。
func (s *StockService) periodClosesLocked(periods []int, tradeDate int) (*boardPeriodCache, error) {
	maxPeriod := maxOfInts(periods)
	needFrom := shiftDate(tradeDate, -(maxPeriod*boardPeriodWindowFactor + boardPeriodWindowExtra))

	gen := s.universeGen.Load()
	cached := s.periodCache
	if cached != nil && (cached.gen != gen || cached.tradeDate != tradeDate ||
		time.Since(cached.builtAt) >= boardPeriodTTL) {
		cached = nil
	}
	if cached == nil {
		cached = &boardPeriodCache{
			gen:       gen,
			tradeDate: tradeDate,
			closes:    map[int]map[string]float64{},
		}
	}

	provider, source := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// ---- 1. 交易日历 ----
	// 窗口不够（用户把周期调长了）就重取一次：这一步很便宜
	//（free-stockdb 是两只股票各一次区间查询，数据湖是列一下日分区目录）。
	if len(cached.calendar) == 0 || cached.calendarFrom > needFrom || cached.calendarTo < tradeDate {
		started := time.Now()
		dates, err := provider.TradingDates(ctx, needFrom, tradeDate)
		if err != nil {
			return nil, err
		}
		dates = trimDates(dates, needFrom, tradeDate)
		if len(dates) == 0 {
			return nil, fmt.Errorf("在 %d~%d 内没有找到任何交易日，请确认数据源 %s 已同步日线",
				needFrom, tradeDate, source.URL)
		}
		cached.calendar = dates
		cached.calendarFrom, cached.calendarTo = needFrom, tradeDate
		slog.Info("板块区间交易日历就绪",
			"dates", len(dates), "window", fmt.Sprintf("%d~%d", needFrom, tradeDate),
			"elapsed", time.Since(started).Round(time.Millisecond).String())
	}

	// ---- 2. 各周期的基准日收盘价（只补缺的那几个）----
	_, baseDates := boardPeriodWindows(cached.calendar, periods)
	missing := make([]int, 0, len(baseDates))
	for _, days := range periods {
		date := baseDates[days]
		if date <= 0 {
			continue
		}
		if _, ok := cached.closes[date]; !ok {
			missing = append(missing, date)
		}
	}

	if len(missing) > 0 {
		boards, err := s.loadBoards()
		if err != nil {
			return nil, err
		}
		codes := boardSymbols(boards)
		if len(codes) == 0 {
			return nil, fmt.Errorf("板块成分名单为空，无法计算区间行情")
		}
		sort.Ints(missing)

		started := time.Now()
		// ⚠ 依赖 ClosesBefore 的"批量"语义：数据湖上这是"扫一遍日分区"，
		// free-stockdb 上是"每个基准日各一轮点查询"（7500 只约 1 秒）。
		closes, err := provider.ClosesBefore(ctx, codes, missing)
		if err != nil && len(closes) == 0 {
			return nil, fmt.Errorf("读取基准日收盘价失败：%w", err)
		}
		if err != nil {
			// 部分失败不该让整页失败：少几只成分只影响覆盖率（界面上会体现出来）
			slog.Warn("基准日收盘价存在失败", "dates", missing, "err", err)
		}
		for date, byCode := range closes {
			cached.closes[date] = byCode
		}
		slog.Info("板块区间基准价就绪",
			"dates", missing, "codes", len(codes),
			"elapsed", time.Since(started).Round(time.Millisecond).String())
	}

	cached.builtAt = time.Now()
	s.periodCache = cached
	return cached, nil
}

// boardPeriodWindows 把周期列表折算成日历窗口，并给出每个周期的基准日。
//
// 基准日 = 倒数第 days+1 个交易日：区间涨跌幅要以**区间起点之前的**收盘价为基准，
// 否则「最近 5 个交易日」会变成"从 5 天前的收盘算到今天"（多算了第一个交易日）。
func boardPeriodWindows(calendar []int, periods []int) ([]BoardPeriodWindow, map[int]int) {
	baseDates := make(map[int]int, len(periods))
	windows := make([]BoardPeriodWindow, 0, len(periods))

	end := int64(0)
	if len(calendar) > 0 {
		end = int64(calendar[len(calendar)-1])
	}

	for _, days := range periods {
		window := BoardPeriodWindow{Days: days, Label: periodLabel(days), EndDate: end}
		baseIdx := len(calendar) - 1 - days
		if baseIdx < 0 {
			window.Message = fmt.Sprintf("交易日不足 %d 个（当前只有 %d 个），无法计算 %s 区间",
				days+1, len(calendar), periodLabel(days))
			windows = append(windows, window)
			continue
		}
		window.Available = true
		window.BaseDate = int64(calendar[baseIdx])
		window.StartDate = int64(calendar[baseIdx+1])
		baseDates[days] = calendar[baseIdx]
		windows = append(windows, window)
	}
	return windows, baseDates
}

// trimDates 把交易日历裁到 [start, end]（去重、升序）。
//
// ⚠ 不能假设 provider 一定按请求区间返回：数据湖的日分区目录可能比请求多，
// free-stockdb 的两只探针股票也可能带出边界之外的一天。
func trimDates(dates []int, start, end int) []int {
	seen := make(map[int]struct{}, len(dates))
	out := make([]int, 0, len(dates))
	for _, date := range dates {
		if date < start || date > end {
			continue
		}
		if _, exists := seen[date]; exists {
			continue
		}
		seen[date] = struct{}{}
		out = append(out, date)
	}
	sort.Ints(out)
	return out
}

// boardSymbols 返回全部板块成分的**去重并集**。
//
// 用并集而不是"当前分类的成分"：基准价缓存与分类无关，
// 取回全市场之后切分类完全不取数。周期是**交易日**，与分类无关，
// 所以基准日也只需要取一次。
func boardSymbols(boards []stockdb.Board) []string {
	seen := make(map[string]struct{}, 4096)
	out := make([]string, 0, 4096)
	for _, board := range boards {
		for _, code := range board.Symbols {
			if code == "" {
				continue
			}
			if _, exists := seen[code]; exists {
				continue
			}
			seen[code] = struct{}{}
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

// aggregateBoardPeriod 把一个板块的成分股区间涨跌聚合成板块区间行情。
func aggregateBoardPeriod(board stockdb.Board, quotes map[string]StockItem, index *boardPeriodIndex, days int) BoardPeriodStat {
	out := BoardPeriodStat{Days: days, MemberCount: len(board.Symbols)}

	returns := index.returns[days]
	if len(returns) == 0 {
		return out
	}

	var (
		weightedSum float64 // Σ(区间涨跌幅 × 流通市值)
		weight      float64 // Σ流通市值
		equalSum    float64
	)
	members := make([]BoardMemberQuote, 0, len(board.Symbols))

	for _, code := range board.Symbols {
		item, ok := quotes[code]
		if !ok {
			// 没有当日行情的成分：当天那条口径已经算不到它，区间口径同样算不到
			continue
		}
		ret, ok := returns[code]
		if !ok {
			continue // 缺少基准价（次新股 / 长期停牌 / 未同步）
		}

		cap := item.FloatMV
		if cap <= 0 {
			cap = item.TotalMV
		}

		weightedSum += ret.pctChg * cap
		weight += cap
		equalSum += ret.pctChg

		switch {
		case ret.pctChg > 0:
			out.UpCount++
		case ret.pctChg < 0:
			out.DownCount++
		default:
			out.FlatCount++
		}

		members = append(members, BoardMemberQuote{
			Code:    item.Code,
			Name:    item.Name,
			Close:   ret.close,
			PctChg:  ret.pctChg,
			Amount:  item.Amount,
			FloatMV: item.FloatMV,
		})
	}

	out.QuotedCount = len(members)
	if out.QuotedCount == 0 {
		return out
	}

	if weight > 0 {
		out.PctChg = round(weightedSum/weight, 4)
	}
	out.PctChgEqual = round(equalSum/float64(out.QuotedCount), 4)

	// 区间领涨/领跌：与当日口径一致，按区间涨跌幅取极值
	sort.SliceStable(members, func(i, j int) bool { return members[i].PctChg > members[j].PctChg })
	leader := members[0]
	laggard := members[len(members)-1]
	out.Leader = &leader
	out.Laggard = &laggard

	return out
}

// periodLabel 返回周期在界面上的标签。
func periodLabel(days int) string {
	return fmt.Sprintf("%d日", days)
}

// maxOfInts 返回切片最大值（空切片返回 0）。
func maxOfInts(values []int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}
