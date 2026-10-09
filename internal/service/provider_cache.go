package service

import (
	"context"
	"sync"

	"freestock/internal/stockdb"
)

// rangeCachingProvider 让同一只股票的日线**只取一次**。
//
// 背景：历史回填对区间内每个交易日都调一次 `ScoreStocks`，而每天的评分窗口
// 起点都不同（`scoreWindowStart(该日, 320)`），于是
//
//	回填 N 天 = N × 股票数 × 一次完整窗口请求
//
// —— 同一只股票的历史被**重复拉了 N 次**。实测 7 天 × 20 只就要 57 秒。
//
// 这里把每只股票在整个回填窗口 `[from, to]` 的日线取一次并缓存，
// 后续对更窄区间的请求直接从缓存**按日期切片**返回。
//
// ⚠ **为什么结果不会变**：真实 `DailyHistory(code, start, end)` 返回的就是
// 「该股票在 [start, end] 内的日线」，所以"取全窗口再按区间切片"与
// "直接请求该区间"应当得到**同一批行**（前提是复权口径与窗口无关 ——
// 逐日回填本来就是这个假设，否则既有的回填结果早就自相矛盾了）。
// 这一点由 `TestRangeCacheMatchesDirectFetch` 用真实数据逐条比对。
type rangeCachingProvider struct {
	Provider // 其余方法直接透传，只覆盖 DailyHistory

	from, to int

	mu    sync.Mutex
	cache map[string][]*stockdb.Quote
}

// newRangeCachingProvider 包装一个数据源，预取窗口为 [from, to]。
func newRangeCachingProvider(inner Provider, from, to int) *rangeCachingProvider {
	return &rangeCachingProvider{
		Provider: inner,
		from:     from,
		to:       to,
		cache:    map[string][]*stockdb.Quote{},
	}
}

// cachedCodes 返回已缓存的股票数（供日志与测试观察）。
func (p *rangeCachingProvider) cachedCodes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cache)
}

// DailyHistory 命中缓存时按区间切片返回，未命中则整窗口取一次再切片。
func (p *rangeCachingProvider) DailyHistory(
	ctx context.Context, code string, start, end int,
) ([]*stockdb.Quote, error) {
	// 请求区间必须落在预取窗口内才敢用缓存；
	// 否则（比如别处要更早的数据）老实走真实请求，避免少给数据。
	if start < p.from || end > p.to {
		return p.Provider.DailyHistory(ctx, code, start, end)
	}

	p.mu.Lock()
	cached, ok := p.cache[code]
	p.mu.Unlock()

	if !ok {
		full, err := p.Provider.DailyHistory(ctx, code, p.from, p.to)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		// 并发下可能有别的 worker 先写进去了，保留先到的即可（内容相同）
		if _, exists := p.cache[code]; !exists {
			p.cache[code] = full
		}
		cached = p.cache[code]
		p.mu.Unlock()
	}

	return quotesInRange(cached, start, end), nil
}

// quotesInRange 从一整段日线里切出 [start, end] 的行。
//
// 必须与真实请求返回**同一批行**：包含边界、保持原顺序。
func quotesInRange(all []*stockdb.Quote, start, end int) []*stockdb.Quote {
	lo, hi := int64(start), int64(end)
	out := make([]*stockdb.Quote, 0, len(all))
	for _, q := range all {
		if q == nil || q.Date < lo || q.Date > hi {
			continue
		}
		out = append(out, q)
	}
	return out
}
