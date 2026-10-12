package service

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 板块行情
//
// free-stockdb / 数据湖的板块数据只有**成分名单**，没有行情。
// 所以板块行情要由成分股行情聚合出来 —— 这也是这个文件存在的全部理由。
//
// 口径说明（有意的取舍）：
//   - 涨跌幅用**流通市值加权**，接近申万行业指数的口径；
//     等权平均对板块同样有参考价值（能看出是普涨还是权重股拉动），所以两个都给。
//   - 换手率 = Σ成交额 ÷ Σ流通市值 × 100，板块层面不能简单平均个股换手率。
//   - 涨跌家数按个股涨跌幅的正负号统计，0 计为平盘。
// ---------------------------------------------------------------------------

// BoardMemberQuote 是板块成分股的一行行情。
type BoardMemberQuote struct {
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	Close   float64 `json:"close"`
	PctChg  float64 `json:"pctChg"`
	Amount  float64 `json:"amount"`
	FloatMV float64 `json:"floatMV"`
}

// BoardQuote 是一个板块的行情聚合结果。
type BoardQuote struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Type     string `json:"type"`

	// MemberCount 是成分股总数；QuotedCount 是其中取到行情的数量。
	// 两者不等说明有停牌/退市/未同步的成分，界面需要能看出这一点。
	MemberCount int `json:"memberCount"`
	QuotedCount int `json:"quotedCount"`

	PctChg      float64 `json:"pctChg"`      // 流通市值加权涨跌幅 %
	PctChgEqual float64 `json:"pctChgEqual"` // 等权涨跌幅 %
	Amount      float64 `json:"amount"`      // 成分股成交额合计
	Turnover    float64 `json:"turnover"`    // Σ成交额 ÷ Σ流通市值 × 100
	FloatMV     float64 `json:"floatMV"`     // Σ流通市值

	UpCount   int `json:"upCount"`
	DownCount int `json:"downCount"`
	FlatCount int `json:"flatCount"`

	Leader  *BoardMemberQuote `json:"leader"`  // 领涨股
	Laggard *BoardMemberQuote `json:"laggard"` // 领跌股

	// Periods 是这个板块在各**区间周期**上的行情，顺序与请求的周期一致（升序）。
	//
	// 为什么放在板块上而不是结果这一层：每个板块的区间行情都不一样，
	// 而周期列表是全分类共用的（见 BoardQuoteResult.Periods）。
	// 算不出的周期**不会**出现在这里（例如 5 日有、60 日因为上市太短没有），
	// 界面按 Days 去找，找不到就显示「—」，而不是显示 0%。
	Periods []BoardPeriodStat `json:"periods"`
}

// BoardQuoteResult 是某个分类下全部板块的行情快照。
type BoardQuoteResult struct {
	Category string `json:"category"`
	// TradeDate 是聚合所用的交易日
	TradeDate int64 `json:"tradeDate"`
	// BoardCount 是板块总数（Boards 可能被 limit 截断）
	BoardCount int          `json:"boardCount"`
	Boards     []BoardQuote `json:"boards"`

	// Periods 是本次请求实际算出来的区间周期（交易日数，升序）。
	// 界面直接用它渲染"多周期对比"的表头，不必自己记请求里传了什么。
	Periods []int `json:"periods"`
	// Windows 与 Periods 一一对应，给出每个周期的基准日与起止日（全分类共用）。
	Windows []BoardPeriodWindow `json:"windows"`
	// PeriodNotice 非空表示**区间行情整体没算出来**（日行情仍然有效），
	// 例如历史日线没同步。界面据此提示，而不是显示一屏 0%。
	PeriodNotice string `json:"periodNotice,omitempty"`

	UpBoards    int     `json:"upBoards"`
	DownBoards  int     `json:"downBoards"`
	FlatBoards  int     `json:"flatBoards"`
	TotalAmount float64 `json:"totalAmount"`

	// Overlapping 表示这个分类下的板块会互相重叠。
	//
	// 申万一级/二级/三级是层级分类，每只股票每级只属于一个板块，可看成一次完整切分；
	// 概念板块是多对多（一只股票同时属于多个概念），成分总数是标的数的十几倍。
	// 这时 TotalAmount 只是「各板块成交额之和」，**不是市场总成交额**，
	// 界面必须据此改措辞，否则会给出一个放大十倍的数字。
	Overlapping  bool    `json:"overlapping"`
	OverlapRatio float64 `json:"overlapRatio"`

	Elapsed string `json:"elapsed"`
}

// GetBoardQuotes 按分类聚合板块行情。category 为空时聚合全部板块。
//
// periodDays 是要额外计算的**区间周期**（交易日数，如 5/10/15/30/60 或自定义），
// 由 NormalizeBoardPeriods 去重、升序、限幅（2 ~ 250）；传空则用 DefaultBoardPeriods。
// 注意 1 日不是区间周期 —— 它就是「当日」，走成分股当日行情那条路
//（BoardQuote 上的 PctChg / Amount / Turnover / UpCount…），不重复计算。
//
// limit > 0 时只返回涨跌幅绝对值最大的前 limit 个（概念板块有近千个，
// 全量返回对界面没意义），但统计数字仍按全量计算。
func (s *StockService) GetBoardQuotes(category string, periodDays []int, limit int) (*BoardQuoteResult, error) {
	started := time.Now()
	periods := NormalizeBoardPeriods(periodDays)

	boards, err := s.loadBoards()
	if err != nil {
		return nil, err
	}

	universe, err := s.GetStockUniverse(false)
	if err != nil {
		return nil, err
	}

	// 成分股行情索引：板块聚合要按代码反复查，必须先建索引
	quotes := make(map[string]StockItem, len(universe.Stocks))
	latest := make(map[string]float64, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if item.HasQuote {
			quotes[item.Code] = item
		}
		if item.Close > 0 {
			latest[item.Code] = item.Close
		}
	}

	result := &BoardQuoteResult{
		Category:  category,
		TradeDate: universe.TradeDate,
		Periods:   periods,
	}

	// 区间行情需要回看历史日线。取不到时**不整页失败**：
	// 当日行情照样给（那是内存里就有的数据），只是没有区间列。
	index, periodErr := s.boardPeriodData(periods, int(universe.TradeDate), latest)
	if periodErr != nil {
		slog.Warn("板块区间行情不可用，仅返回当日行情", "err", periodErr)
		result.PeriodNotice = periodErr.Error()
	} else {
		result.Windows = index.windows
	}

	all := make([]BoardQuote, 0, len(boards))
	memberTotal := 0
	for _, board := range boards {
		if category != "" && board.Category != category {
			continue
		}
		memberTotal += len(board.Symbols)
		quote := aggregateBoard(board, quotes)
		if index != nil {
			quote.Periods = make([]BoardPeriodStat, 0, len(periods))
			for _, days := range periods {
				quote.Periods = append(quote.Periods, aggregateBoardPeriod(board, quotes, index, days))
			}
		}
		all = append(all, quote)
	}

	result.BoardCount = len(all)
	for _, board := range all {
		result.TotalAmount += board.Amount
		switch {
		case board.PctChg > 0:
			result.UpBoards++
		case board.PctChg < 0:
			result.DownBoards++
		default:
			result.FlatBoards++
		}
	}

	// 成分总数 ÷ 标的数：明显大于 1 说明板块互相重叠，成交额不能当市场总量看
	if len(quotes) > 0 {
		result.OverlapRatio = round(float64(memberTotal)/float64(len(quotes)), 2)
		result.Overlapping = result.OverlapRatio > 1.2
	}

	// 涨跌幅从高到低，界面默认就是这个顺序
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].PctChg != all[j].PctChg {
			return all[i].PctChg > all[j].PctChg
		}
		return all[i].Amount > all[j].Amount
	})

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	result.Boards = all
	result.Elapsed = time.Since(started).Round(time.Millisecond).String()

	slog.Info("板块行情就绪",
		"category", category, "boards", result.BoardCount,
		"returned", len(all), "up", result.UpBoards, "down", result.DownBoards,
		"elapsed", result.Elapsed)
	return result, nil
}

// aggregateBoard 把成分股行情聚合成板块行情。
func aggregateBoard(board stockdb.Board, quotes map[string]StockItem) BoardQuote {
	out := BoardQuote{
		Code:        board.Code,
		Name:        board.Name,
		Category:    board.Category,
		Type:        board.Type,
		MemberCount: len(board.Symbols),
	}

	var (
		weightedSum float64 // Σ(涨跌幅 × 流通市值)
		weight      float64 // Σ流通市值
		equalSum    float64
	)
	members := make([]BoardMemberQuote, 0, len(board.Symbols))

	for _, code := range board.Symbols {
		item, ok := quotes[code]
		if !ok {
			continue
		}

		// 有些数据源不提供流通市值，退化为总市值，否则权重会全变成 0
		cap := item.FloatMV
		if cap <= 0 {
			cap = item.TotalMV
		}

		weightedSum += item.PctChg * cap
		weight += cap
		equalSum += item.PctChg

		out.Amount += item.Amount
		out.FloatMV += cap
		switch {
		case item.PctChg > 0:
			out.UpCount++
		case item.PctChg < 0:
			out.DownCount++
		default:
			out.FlatCount++
		}

		members = append(members, BoardMemberQuote{
			Code:    item.Code,
			Name:    item.Name,
			Close:   item.Close,
			PctChg:  item.PctChg,
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
	if out.FloatMV > 0 {
		out.Turnover = round(out.Amount/out.FloatMV*100, 4)
	}

	// 领涨/领跌：成交额太小的成分容易因为一笔小单出现极端涨跌幅，参考价值低。
	// 这里不做硬性过滤，但把成交额一起给出去，界面可以自行判断。
	sort.SliceStable(members, func(i, j int) bool { return members[i].PctChg > members[j].PctChg })
	if len(members) > 0 {
		leader := members[0]
		laggard := members[len(members)-1]
		out.Leader = &leader
		out.Laggard = &laggard
	}
	return out
}

// GetBoardMembers 返回某个板块的成分股行情，按涨跌幅从高到低。
// 板块代码为空或找不到时返回空列表而不是错误 —— 界面上的展开是渐进增强。
func (s *StockService) GetBoardMembers(code string) ([]BoardMemberQuote, error) {
	boards, err := s.loadBoards()
	if err != nil {
		return nil, err
	}

	var target *stockdb.Board
	for i := range boards {
		if boards[i].Code == code {
			target = &boards[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("没有找到板块 %s", code)
	}

	universe, err := s.GetStockUniverse(false)
	if err != nil {
		return nil, err
	}
	quotes := make(map[string]StockItem, len(universe.Stocks))
	for _, item := range universe.Stocks {
		quotes[item.Code] = item
	}

	members := make([]BoardMemberQuote, 0, len(target.Symbols))
	for _, symbol := range target.Symbols {
		item, ok := quotes[symbol]
		if !ok {
			// 没有行情的成分也要列出来，否则界面上的数量对不上
			members = append(members, BoardMemberQuote{Code: symbol})
			continue
		}
		members = append(members, BoardMemberQuote{
			Code:    item.Code,
			Name:    item.Name,
			Close:   item.Close,
			PctChg:  item.PctChg,
			Amount:  item.Amount,
			FloatMV: item.FloatMV,
		})
	}

	sort.SliceStable(members, func(i, j int) bool { return members[i].PctChg > members[j].PctChg })
	return members, nil
}
