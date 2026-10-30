// Package qgrowth 实现"质量成长 · 趋势回调"策略的纯计算部分。
//
// 设计口径见 reports/fund_strategy/开发说明_v3.md：
//   - 基本面过滤：ROE(年化)≥8%、营收/净利同比≥0、毛利≥15%、负债≤70%、现金流/净利≥0.3
//   - 趋势：收盘 > MA200
//   - 回调：20 日回撤在 [-20%, -2%]
//   - 打分：0.4*质量 + 0.4*成长 + 0.2*安全（截面百分位）
//   - 权重：raw = 0.05 + rank²，归一化后单票上限 15%（迭代配平）
package qgrowth

import (
	"math"
	"sort"
)

// Config 是策略参数（冻结值，见开发说明第 2 节）。
type Config struct {
	N             int     `json:"n"`
	TrendMA       int     `json:"trendMA"`
	PullbackHi    float64 `json:"pullbackHi"`    // -2
	PullbackLo    float64 `json:"pullbackLo"`    // -20
	MinROE        float64 `json:"minROE"`        // 8
	MinRevYoY     float64 `json:"minRevYoY"`     // 0
	MinNPYoY      float64 `json:"minNPYoY"`      // 0
	MinGross      float64 `json:"minGross"`      // 15
	MaxDebt       float64 `json:"maxDebt"`       // 70
	MinOCF        float64 `json:"minOCF"`        // 0.3
	MinAmount20   float64 `json:"minAmount20"`   // 3e7
	MinListedDays int     `json:"minListedDays"` // 250
	MaxWeight     float64 `json:"maxWeight"`     // 0.15
	BearExposure  float64 `json:"bearExposure"`  // 0.5
	Band          float64 `json:"band"`          // 0.01
}

// DefaultConfig 返回冻结参数。
func DefaultConfig() Config {
	return Config{
		N: 8, TrendMA: 200, PullbackHi: -2, PullbackLo: -20,
		MinROE: 8, MinRevYoY: 0, MinNPYoY: 0, MinGross: 15, MaxDebt: 70, MinOCF: 0.3,
		MinAmount20: 3e7, MinListedDays: 250, MaxWeight: 0.15, BearExposure: 0.5, Band: 0.01,
	}
}

// Fundamental 是某只股票在调仓日可见的最新一期财报指标（PIT 由调用方保证）。
type Fundamental struct {
	ROE         float64 // 年化 %
	RevYoY      float64 // %
	NPYoY       float64 // %
	GrossMargin float64 // %
	DebtRatio   float64 // %
	OCFRatio    float64 // 经营现金流 / 净利
	Period      string  // 报告期，如 2025Q1（用于排查）
	Valid       bool
}

// Candidate 是某只股票在某交易日的行情 + 基本面快照。
type Candidate struct {
	Code       string
	Name       string
	Close      float64
	MA200      float64
	DD20       float64 // 相对 20 日最高价的回撤，负值（%）
	Amount20   float64
	ListedDays int
	Fund       Fundamental
	Score      float64 // 由 Score 填写
}

// Eligible 判断是否通过全部过滤（不含回调，回调由 PullbackOK 单独判断）。
func (c Config) Eligible(x Candidate) bool {
	if x.Close <= 0 || x.ListedDays < c.MinListedDays || x.Amount20 < c.MinAmount20 {
		return false
	}
	if c.TrendMA > 0 && (x.MA200 <= 0 || x.Close <= x.MA200) {
		return false
	}
	f := x.Fund
	if !f.Valid {
		return false
	}
	if f.ROE < c.MinROE || f.RevYoY < c.MinRevYoY || f.NPYoY < c.MinNPYoY {
		return false
	}
	if f.GrossMargin < c.MinGross || f.DebtRatio > c.MaxDebt {
		return false
	}
	if f.OCFRatio < c.MinOCF {
		return false
	}
	return true
}

// PullbackOK 判断是否落在回调买点区间。
func (c Config) PullbackOK(x Candidate) bool {
	return x.DD20 <= c.PullbackHi && x.DD20 >= c.PullbackLo
}

// rankPct 返回每个元素的百分位（最小=1/n，最大=1）。
func rankPct(vals []float64) []float64 {
	n := len(vals)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] < vals[idx[b]] })
	out := make([]float64, n)
	for rank, i := range idx {
		out[i] = float64(rank+1) / float64(n)
	}
	return out
}

// Score 给候选打截面分（只在"已通过过滤"的集合内计算）。
func Score(cands []Candidate) {
	n := len(cands)
	if n == 0 {
		return
	}
	roe := make([]float64, n)
	gm := make([]float64, n)
	rev := make([]float64, n)
	np := make([]float64, n)
	negDebt := make([]float64, n)
	ocf := make([]float64, n)
	for i, c := range cands {
		roe[i] = c.Fund.ROE
		gm[i] = c.Fund.GrossMargin
		rev[i] = c.Fund.RevYoY
		np[i] = c.Fund.NPYoY
		negDebt[i] = -c.Fund.DebtRatio
		ocf[i] = c.Fund.OCFRatio
	}
	rROE, rGM := rankPct(roe), rankPct(gm)
	rRev, rNP := rankPct(rev), rankPct(np)
	rDebt, rOCF := rankPct(negDebt), rankPct(ocf)
	for i := range cands {
		quality := 0.6*rROE[i] + 0.4*rGM[i]
		growth := 0.5*rRev[i] + 0.5*rNP[i]
		safety := 0.5*rDebt[i] + 0.5*rOCF[i]
		cands[i].Score = 0.4*quality + 0.4*growth + 0.2*safety
	}
}

// Weights 按"score² + 单票上限"分配权重。
//
// scores 必须按分数降序（第 0 个是第一名）。返回权重和 == 1（上限允许时），
// 若 k*cap < 1（例如 3 只、cap 15%），剩余部分留现金，和 < 1。
func Weights(scores []float64, cap float64) []float64 {
	k := len(scores)
	if k == 0 {
		return nil
	}
	raw := make([]float64, k)
	sum := 0.0
	for i := range scores {
		r := float64(k-i) / float64(k) // 1.0, (k-1)/k, ..., 1/k
		raw[i] = 0.05 + r*r
		sum += raw[i]
	}
	w := make([]float64, k)
	for i := range raw {
		w[i] = raw[i] / sum
	}
	if cap <= 0 || cap >= 1 {
		return w
	}
	// 迭代配平：超限的**永久**压到 cap，超额按比例分给尚未封顶的；
	// 如果全部封顶（k*cap < 1），剩余部分留现金（权重和 < 1）。
	frozen := make([]bool, k)
	for iter := 0; iter < 40; iter++ {
		over := false
		for i := range w {
			if !frozen[i] && w[i] > cap+1e-12 {
				over = true
				break
			}
		}
		if !over {
			break
		}
		excess := 0.0
		for i := range w {
			if !frozen[i] && w[i] > cap+1e-12 {
				excess += w[i] - cap
				w[i] = cap
				frozen[i] = true
			}
		}
		restSum := 0.0
		for i := range w {
			if !frozen[i] {
				restSum += w[i]
			}
		}
		if restSum <= 0 {
			break
		}
		for i := range w {
			if !frozen[i] {
				w[i] += excess * w[i] / restSum
			}
		}
	}
	return w
}

// WeightsFor 是 Weights 的便利封装：四舍五入到 1e-6。
func WeightsFor(scores []float64, cap float64) []float64 {
	w := Weights(scores, cap)
	for i := range w {
		w[i] = math.Round(w[i]*1e6) / 1e6
	}
	return w
}
