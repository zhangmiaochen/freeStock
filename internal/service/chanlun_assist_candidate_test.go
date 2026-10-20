package service

import (
	"sort"
	"strconv"
	"sync"

	"freestock/internal/chanlun"
	"freestock/internal/chanlunassist"
)

// candidateProvider 是**测试专用**的信号源：把缠论状态机里「形成中 / 触发 / 确认」
// 三种状态都暴露出来，用来验证「让缠论更早」的买卖时序。
//
// 生产代码的 `chanlunassist.Analyzer` 只认 confirmed（显式允许时才加 triggered），
// 这里为了做前瞻实验，把 candidate 也算进来 —— 但只存在于 `_test.go`。
type candidateProvider struct {
	bars  map[string][]chanlun.Bar
	cfg   chanlun.Config
	fresh int

	mu    sync.Mutex
	cache map[string]chanlunassist.Snapshot
}

func newCandidateProvider(bars map[string][]chanlun.Bar, fresh int) *candidateProvider {
	if fresh < 10 {
		fresh = 10
	}
	return &candidateProvider{
		bars: bars, cfg: chanlun.DefaultConfig(), fresh: fresh,
		cache: map[string]chanlunassist.Snapshot{},
	}
}

func (p *candidateProvider) Signals(code string, asOf int) chanlunassist.Snapshot {
	series := p.bars[code]
	if len(series) == 0 || asOf <= 0 {
		return chanlunassist.Snapshot{}
	}
	last := sort.Search(len(series), func(i int) bool { return series[i].Date > asOf }) - 1
	if last < 0 {
		return chanlunassist.Snapshot{}
	}
	key := code + "|" + strconv.Itoa(series[last].Date)
	p.mu.Lock()
	if cached, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return cached
	}
	p.mu.Unlock()

	analysis := chanlun.Analyze(series[:last+1], chanlun.LevelDay, p.cfg, chanlun.AdaptInfo{})
	out := chanlunassist.Snapshot{}
	for i := range analysis.Signals {
		sig := analysis.Signals[i]
		if sig.Conflict {
			continue
		}
		idx, date := candidateAnchor(sig)
		if idx < 0 || idx > last || date <= 0 {
			continue
		}
		age := last - idx
		fresh := p.fresh
		if sig.Direction == "sell" {
			fresh = 10
		}
		if age > fresh {
			continue
		}
		item := &chanlunassist.Signal{
			Type: sig.Type, TypeCN: chanlun.SignalTypeCN(sig.Type), State: sig.State,
			Score: sig.Score, ConfirmDate: date, TriggerDate: sig.TriggerDate,
			ConfirmIndex: sig.ConfirmIndex, Price: sig.Price, AgeBars: age,
		}
		switch sig.Type {
		case chanlun.SignalBuy2:
			out.Buy2 = betterCandidate(out.Buy2, item)
		case chanlun.SignalBuy3:
			out.Buy3 = betterCandidate(out.Buy3, item)
		case chanlun.SignalSell1:
			out.Sell1 = betterCandidate(out.Sell1, item)
		case chanlun.SignalSell2:
			out.Sell2 = betterCandidate(out.Sell2, item)
		case chanlun.SignalSell3:
			out.Sell3 = betterCandidate(out.Sell3, item)
		}
	}
	p.mu.Lock()
	p.cache[key] = out
	p.mu.Unlock()
	return out
}

// candidateAnchor 返回信号用于「新鲜度」的下标与日期：
// 确认→确认点；触发→触发点；候选→形成点（三者依次更早）。
func candidateAnchor(sig chanlun.Signal) (int, int) {
	switch sig.State {
	case chanlun.StateConfirmed:
		return sig.ConfirmIndex, sig.ConfirmDate
	case chanlun.StateTriggered:
		return sig.TriggerIndex, sig.TriggerDate
	case chanlun.StateCandidate:
		return sig.FormIndex, sig.FormDate
	}
	return -1, 0
}

func betterCandidate(current, candidate *chanlunassist.Signal) *chanlunassist.Signal {
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
