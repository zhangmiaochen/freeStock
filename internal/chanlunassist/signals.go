package chanlunassist

import (
	"sort"
	"strconv"
	"sync"

	"freestock/internal/chanlun"
)

// Signal 是一个「当下可用」的缠论买卖点快照。
//
// 只保留辅助层真正需要的字段：类型、状态、确认日、新鲜度、综合分。
type Signal struct {
	Type         chanlun.SignalType  `json:"type"`
	TypeCN       string              `json:"typeCN"`
	State        chanlun.SignalState `json:"state"`
	Score        float64             `json:"score"`
	ConfirmDate  int                 `json:"confirmDate"`
	TriggerDate  int                 `json:"triggerDate"`
	ConfirmIndex int                 `json:"confirmIndex"`
	Price        float64             `json:"price"`
	// AgeBars 是确认日（或触发日）距计划日的交易日数，0 表示当天。
	AgeBars int `json:"ageBars"`
}

// Snapshot 是某只股票在某一天的可用买卖点。
type Snapshot struct {
	Buy2  *Signal `json:"buy2,omitempty"`
	Buy3  *Signal `json:"buy3,omitempty"`
	Sell1 *Signal `json:"sell1,omitempty"`
	Sell2 *Signal `json:"sell2,omitempty"`
	Sell3 *Signal `json:"sell3,omitempty"`
}

// Empty 表示当天没有任何可用信号。
func (s Snapshot) Empty() bool {
	return s.Buy2 == nil && s.Buy3 == nil && s.Sell1 == nil && s.Sell2 == nil && s.Sell3 == nil
}

// BestBuy 返回最强的买入信号：三买优先于二买。
func (s Snapshot) BestBuy() *Signal {
	if s.Buy3 != nil {
		return s.Buy3
	}
	return s.Buy2
}

// BestSell 返回最强的卖出信号：三卖 > 二卖 > 一卖。
func (s Snapshot) BestSell() *Signal {
	switch {
	case s.Sell3 != nil:
		return s.Sell3
	case s.Sell2 != nil:
		return s.Sell2
	default:
		return s.Sell1
	}
}

// Provider 是计划改写时查询信号的抽象，便于单测注入假信号。
type Provider interface {
	Signals(code string, asOf int) Snapshot
}

// Analyzer 用「截断到计划日」的日线现算缠论信号，天然没有未来函数。
//
// 之所以不预先把整个区间的信号算好再按日期回填：缠论信号的状态会随走势演化
// （确认后还可能失效/过期），全区间分析得到的是「最终态」，回填会把
// 「今天确认、下周失效」的信号当成一直有效，这正是回测里最典型的未来函数。
type Analyzer struct {
	cfg    chanlun.Config
	assist Config

	bars  map[string][]chanlun.Bar
	mu    sync.Mutex
	cache map[string]Snapshot
}

// NewAnalyzer 构造分析器。barsByCode 会被按日期升序整理（不改调用方的切片顺序）。
func NewAnalyzer(barsByCode map[string][]chanlun.Bar, cfg chanlun.Config, assist Config) *Analyzer {
	assist = assist.Sanitized()
	clean := make(map[string][]chanlun.Bar, len(barsByCode))
	for code, bars := range barsByCode {
		if len(bars) == 0 {
			continue
		}
		copied := make([]chanlun.Bar, 0, len(bars))
		for _, bar := range bars {
			if bar.Date <= 0 || bar.High <= 0 || bar.Low <= 0 || bar.Close <= 0 {
				continue
			}
			copied = append(copied, bar)
		}
		if len(copied) == 0 {
			continue
		}
		sort.SliceStable(copied, func(i, j int) bool { return copied[i].Date < copied[j].Date })
		// 去重：同一天只留最后一根（数据源重复行时不让结构算两次）
		unique := copied[:1]
		for i := 1; i < len(copied); i++ {
			if copied[i].Date == unique[len(unique)-1].Date {
				unique[len(unique)-1] = copied[i]
				continue
			}
			unique = append(unique, copied[i])
		}
		clean[code] = unique
	}
	return &Analyzer{
		cfg:    cfg.Sanitize(),
		assist: assist,
		bars:   clean,
		cache:  make(map[string]Snapshot),
	}
}

// Signals 返回 code 在 asOf（YYYYMMDD）收盘时的可用买卖点。
//
// 只使用 Date <= asOf 的 K 线；找不到数据时返回空快照。
func (a *Analyzer) Signals(code string, asOf int) Snapshot {
	series := a.bars[code]
	if len(series) == 0 || asOf <= 0 {
		return Snapshot{}
	}
	// 二分找最后一根 Date <= asOf 的 K 线
	last := sort.Search(len(series), func(i int) bool { return series[i].Date > asOf }) - 1
	if last < 0 {
		return Snapshot{}
	}
	key := code + "|" + strconv.Itoa(series[last].Date)
	a.mu.Lock()
	cached, ok := a.cache[key]
	a.mu.Unlock()
	if ok {
		return cached
	}

	prefix := series[:last+1]
	analysis := chanlun.Analyze(prefix, chanlun.LevelDay, a.cfg, chanlun.AdaptInfo{})
	out := signalsFromAnalysis(analysis.Signals, last, a.assist)

	a.mu.Lock()
	a.cache[key] = out
	a.mu.Unlock()
	return out
}

// signalsFromAnalysis 从一次分析结果里挑出「当下可用」的信号。
//
// 抽成纯函数是为了单测能直接构造 chanlun.Signal 验证筛选规则，
// 不必先造出一段能长出二买/三卖的行情。
func signalsFromAnalysis(signals []chanlun.Signal, lastIdx int, cfg Config) Snapshot {
	out := Snapshot{}
	for i := range signals {
		sig := signals[i]
		if sig.Direction != "buy" && sig.Direction != "sell" {
			continue
		}
		anchorIdx, anchorDate, ok := signalAnchor(sig, cfg)
		if !ok || anchorIdx < 0 || anchorIdx > lastIdx || anchorDate <= 0 {
			continue
		}
		age := lastIdx - anchorIdx
		fresh := cfg.BuyFreshBars
		if sig.Direction == "sell" {
			fresh = cfg.SellFreshBars
		}
		if age > fresh {
			continue
		}
		if sig.Conflict && !cfg.AllowConflict {
			continue
		}
		if cfg.MinSignalScore > 0 && sig.Score < cfg.MinSignalScore {
			continue
		}
		candidate := &Signal{
			Type:         sig.Type,
			TypeCN:       chanlun.SignalTypeCN(sig.Type),
			State:        sig.State,
			Score:        sig.Score,
			ConfirmDate:  anchorDate,
			TriggerDate:  sig.TriggerDate,
			ConfirmIndex: sig.ConfirmIndex,
			Price:        sig.Price,
			AgeBars:      age,
		}
		switch sig.Type {
		case chanlun.SignalBuy2:
			out.Buy2 = betterSignal(out.Buy2, candidate)
		case chanlun.SignalBuy3:
			out.Buy3 = betterSignal(out.Buy3, candidate)
		case chanlun.SignalSell1:
			out.Sell1 = betterSignal(out.Sell1, candidate)
		case chanlun.SignalSell2:
			out.Sell2 = betterSignal(out.Sell2, candidate)
		case chanlun.SignalSell3:
			out.Sell3 = betterSignal(out.Sell3, candidate)
		}
	}
	return out
}

// signalAnchor 返回信号用于「新鲜度」计时的锚点。
//
// 确认信号用确认日；只有显式允许触发信号时才回落到触发日。
func signalAnchor(sig chanlun.Signal, cfg Config) (int, int, bool) {
	switch sig.State {
	case chanlun.StateConfirmed:
		if sig.ConfirmIndex < 0 || sig.ConfirmDate <= 0 {
			return 0, 0, false
		}
		return sig.ConfirmIndex, sig.ConfirmDate, true
	case chanlun.StateTriggered:
		if !cfg.AllowTriggered {
			return 0, 0, false
		}
		if sig.TriggerIndex < 0 || sig.TriggerDate <= 0 {
			return 0, 0, false
		}
		return sig.TriggerIndex, sig.TriggerDate, true
	}
	return 0, 0, false
}

// betterSignal 同类型多条信号时取综合分更高者；同分取确认更晚者（更贴近当下）。
func betterSignal(current, candidate *Signal) *Signal {
	if current == nil {
		return candidate
	}
	if candidate.Score != current.Score {
		if candidate.Score > current.Score {
			return candidate
		}
		return current
	}
	if candidate.ConfirmDate > current.ConfirmDate {
		return candidate
	}
	return current
}

// CacheSize 返回已缓存的 (股票, 交易日) 快照数（测试/诊断用）。
func (a *Analyzer) CacheSize() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.cache)
}

// CodeCount 返回分析器里有日线的股票数。
func (a *Analyzer) CodeCount() int { return len(a.bars) }
