package service

import (
	"context"
	"sync"

	"freestock/internal/score"
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

	// barCache 是 DailyBars 的缓存。
	//
	// **为什么不直接透传给底层 DailyBars**：换方法会让 stockdb 那边丢掉
	// 「每只只取一次」的区间缓存，退化成按天重复请求。
	// 走底层 DailyBars 才能让数据湖那条路（真正省掉 Quote 组装）拿到全部好处。
	barMu    sync.Mutex
	barCache map[string][]score.Bar
	// hits/misses/fetches 用来观察缓存到底有没有生效 ——
	// "以为命中了其实没有"是这类缓存最容易出的问题，必须能看见。
	hits, misses, fetches int
}

// stats 返回 (命中, 未命中, 真实取数) 次数。
func (p *rangeCachingProvider) stats() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits, p.misses, p.fetches
}

// newRangeCachingProvider 包装一个数据源，预取窗口为 [from, to]。
func newRangeCachingProvider(inner Provider, from, to int) *rangeCachingProvider {
	return &rangeCachingProvider{
		Provider: inner,
		from:     from,
		to:       to,
		cache:    map[string][]*stockdb.Quote{},
		barCache: map[string][]score.Bar{},
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
			p.fetches++
		}
		cached = p.cache[code]
		p.mu.Unlock()
		p.mu.Lock()
		p.misses++
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		p.hits++
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

// DailyBars 与 DailyHistory 用同一套缓存策略：
// 请求区间落在预取窗口内就用缓存切片，否则整窗口取一次。
//
// 底层用的是 Provider.DailyBars —— 数据湖那条路因此完全不组装 Quote，
// 这是回填从 30 秒级降下来的关键。
func (p *rangeCachingProvider) DailyBars(
	ctx context.Context, code string, start, end int,
) ([]score.Bar, error) {
	// ⚠ **数据湖这条路直接透传，不再套一层缓存**。
	//
	// 底层 lakeProvider 已经有"按交易日批量读"的缓存：每次取数只是从内存
	// 里按区间切片，已经是 O(结果)。再在上面套一层区间缓存，收益为零，
	// 却要为**每天每只股票**多拷贝一份整窗口的 bar（5821 只 × 281 根），
	// 白白推高 GC 压力（profile 里 GC 占 14%）。
	//
	// stockdb 那条路没有这层内存缓存，仍然需要它（否则会按天重复发请求）。
	if p.Provider.Kind() == SourceKindCnLake {
		return p.Provider.DailyBars(ctx, code, start, end)
	}

	// 与 DailyHistory 同样的保护：区间超出预取窗口就不敢用缓存
	if start < p.from || end > p.to {
		return p.Provider.DailyBars(ctx, code, start, end)
	}

	p.barMu.Lock()
	cached, ok := p.barCache[code]
	p.barMu.Unlock()

	if !ok {
		full, err := p.Provider.DailyBars(ctx, code, p.from, p.to)
		if err != nil {
			return nil, err
		}
		p.barMu.Lock()
		// 并发下可能已有别的 worker 写进去了，保留先到的（内容相同）
		if _, exists := p.barCache[code]; !exists {
			p.barCache[code] = full
		}
		cached = p.barCache[code]
		p.barMu.Unlock()
	}

	return sliceBars(cached, start, end), nil
}

// sliceBars 从整窗口切片出 [start, end]，并**复制一份**。
//
// 复制是必须的：调用方（评分）会遍历/持有这个切片，
// 直接返回缓存内部切片会让外部改动污染缓存。
func sliceBars(all []score.Bar, start, end int) []score.Bar {
	out := make([]score.Bar, 0, len(all))
	for _, bar := range all {
		if bar.Date < int64(start) || bar.Date > int64(end) {
			continue
		}
		out = append(out, bar)
	}
	return out
}
