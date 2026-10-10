package service

import (
	"context"
	"sort"
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
		// ⚠ **入缓存前必须确认按日期升序**。
		//
		// `sliceBars` 用二分定位 [start,end] 并返回连续子切片 —— 前提是
		// 已排序。若顺序不满足，二分给出的区间是错的，会**静默取到错误数据**。
		// 两个底层实现（lakeProvider / stockdbProvider）都排过序，
		// 但这是个不能靠"记得"来保证的前提，所以在这里断言一次
		//（每只股票只在首次取数时走一遍，开销可忽略）。
		if !sort.SliceIsSorted(full, func(i, j int) bool { return full[i].Date < full[j].Date }) {
			sort.Slice(full, func(i, j int) bool { return full[i].Date < full[j].Date })
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

// sliceBars 从整窗口**零拷贝**切出 [start, end]。
//
// ⚠ 这是"合并计算量"的落点。同一只股票在整段回填里只需要**物化一次**
// （整窗口的 []score.Bar 缓存在 barCache 里），之后每一天只是按日期取出
// 连续的一段。原来每天都重新遍历整段并重建切片 ——
// 5 天 x 5821 只 x 281 根 = 820 万个结构体（约 460MB 分配），
// 这正是 CPU profile 里 GC 占 14% 的来源。
//
// all 已按日期升序（底层保证），所以 [start, end] 必然落在**连续区间**上，
// 用二分定出边界后直接返回子切片即可，**不分配、不复制**。
//
// ⚠ 调用方**不得修改**返回的切片（它指向缓存内部）。
// 评分路径只读（ComputeIndicators / PrevHigh60 / BuildRepairFeatures），
// 满足这个前提；若将来有人要就地排序，必须先自己复制。
func sliceBars(all []score.Bar, start, end int) []score.Bar {
	lo := sort.Search(len(all), func(i int) bool { return all[i].Date >= int64(start) })
	hi := sort.Search(len(all), func(i int) bool { return all[i].Date > int64(end) })
	if lo >= hi {
		return nil
	}
	return all[lo:hi]
}
