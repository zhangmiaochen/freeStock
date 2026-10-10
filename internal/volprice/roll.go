// Package volprice 是原项目「量价行为三模型」的 1:1 移植。
//
// 对应原文件：
//
//	volume_price_features.py    → features.go（+ 本文件的 pandas 兼容原语）
//	volume_price_market_state.py→ market_state.go
//	volume_price_models.py      → models.go
//	volume_price_backtest.py    → backtest.go
//
// ⚠ **为什么先自己实现一套 pandas 原语**：原实现的特征层几乎全是
// `.rolling(window, min_periods=m)` + `.shift()` + `.where()` + `.fillna()`
// 的向量化写法，语义细节很多（min_periods 的截断窗口、NaN 不参与聚合、
// `where` 的单参/双参两种语义）。用 Go 的近似写法（例如"窗口不足就取已有值"）
// 会静默产出不同的特征值，进而让信号与原版不一致 —— 而这些信号最终会
// 决定组合买什么。
//
// 所以这里把用到的原语逐一对齐 pandas，并用
// `testdata/gen_pandas_golden.py`（**直接 import 原模块**跑 pandas）生成黄金值，
// 由 `pandas_golden_test.go` 逐列比对。
package volprice

import (
	"math"
	"sort"
)

// nan 是 pandas 的 NaN（Go 的 math.NaN）。
func nan() float64 { return math.NaN() }

func isNaN(v float64) bool { return math.IsNaN(v) }

// Series 是 float64 序列，NaN 表示缺失（与 pandas 的 float64 Series 一致）。
type Series []float64

// BoolSeries 是布尔序列（pandas 的 bool Series，不含缺失）。
type BoolSeries []bool

// StrSeries 是字符串序列（pandas 的 object Series）。
type StrSeries []string

func newSeries(n int) Series {
	out := make(Series, n)
	for i := range out {
		out[i] = nan()
	}
	return out
}

// Shift 对应 `s.shift(n)`：前 n 个位置补 NaN。
//
// n <= 0 时原样返回（原实现只用正值）。
func (s Series) Shift(n int) Series {
	out := newSeries(len(s))
	if n <= 0 {
		copy(out, s)
		return out
	}
	for i := n; i < len(s); i++ {
		out[i] = s[i-n]
	}
	return out
}

// Diff 对应 `s.diff()`：`s[i] - s[i-1]`，首个为 NaN。
func (s Series) Diff() Series {
	out := newSeries(len(s))
	for i := 1; i < len(s); i++ {
		out[i] = s[i] - s[i-1]
	}
	return out
}

// WhereOther 对应 `s.where(cond, other)`（**双参**形式）：
// cond 为真取 s，否则取 other。
func (s Series) WhereOther(cond BoolSeries, other Series) Series {
	out := make(Series, len(s))
	for i := range s {
		if cond[i] {
			out[i] = s[i]
		} else {
			out[i] = other[i]
		}
	}
	return out
}

// Where 对应 `s.where(cond)`（**单参**形式）：cond 为假置 NaN。
func (s Series) Where(cond BoolSeries) Series {
	out := make(Series, len(s))
	for i := range s {
		if cond[i] {
			out[i] = s[i]
		} else {
			out[i] = nan()
		}
	}
	return out
}

// Mask 对应 `s.mask(cond)`（单参）：cond 为真置 NaN。
func (s Series) Mask(cond BoolSeries) Series {
	out := make(Series, len(s))
	for i := range s {
		if cond[i] {
			out[i] = nan()
		} else {
			out[i] = s[i]
		}
	}
	return out
}

// MaskValue 对应 `s.mask(cond, value)`（双参）：cond 为真取 value。
func (s Series) MaskValue(cond BoolSeries, value float64) Series {
	out := make(Series, len(s))
	for i := range s {
		if cond[i] {
			out[i] = value
		} else {
			out[i] = s[i]
		}
	}
	return out
}

// FillNA 对应 `s.fillna(v)`。
func (s Series) FillNA(v float64) Series {
	out := make(Series, len(s))
	for i := range s {
		if isNaN(s[i]) {
			out[i] = v
		} else {
			out[i] = s[i]
		}
	}
	return out
}

// Clip 对应 `s.clip(lower, upper)`：NaN 原样保留（pandas 如此）。
//
// upper 传 NaN 表示不设上界，lower 传 NaN 表示不设下界 —— 与原实现里
// 只写 `lower=` 或只写 `upper=` 的用法对应。
func (s Series) Clip(lower, upper float64) Series {
	out := make(Series, len(s))
	for i, v := range s {
		if isNaN(v) {
			out[i] = v
			continue
		}
		if !isNaN(lower) && v < lower {
			v = lower
		}
		if !isNaN(upper) && v > upper {
			v = upper
		}
		out[i] = v
	}
	return out
}

// IsNA 对应 `s.isna()`。
func (s Series) IsNA() BoolSeries {
	out := make(BoolSeries, len(s))
	for i, v := range s {
		out[i] = isNaN(v)
	}
	return out
}

// GT / GE / LT / LE / EQ 对应 pandas 的比较。
//
// ⚠ NaN 与任何值比较**一律为 false**（Go 的浮点比较天然如此，与 pandas 一致），
// 这正是原实现大量依赖的性质（`x.gt(0)` 会自然把 NaN 滤成 False）。
func (s Series) GT(v float64) BoolSeries { return s.cmp(v, func(a, b float64) bool { return a > b }) }
func (s Series) GE(v float64) BoolSeries { return s.cmp(v, func(a, b float64) bool { return a >= b }) }
func (s Series) LT(v float64) BoolSeries { return s.cmp(v, func(a, b float64) bool { return a < b }) }
func (s Series) LE(v float64) BoolSeries { return s.cmp(v, func(a, b float64) bool { return a <= b }) }
func (s Series) EQ(v float64) BoolSeries { return s.cmp(v, func(a, b float64) bool { return a == b }) }
func (s Series) cmp(v float64, f func(a, b float64) bool) BoolSeries {
	out := make(BoolSeries, len(s))
	for i := range s {
		out[i] = f(s[i], v)
	}
	return out
}

// cmp2 是**两个序列之间**的逐元素比较（pandas 的 `a.gt(b)`）。
//
// ⚠ 任一侧是 NaN 一律为 false —— 与原实现大量依赖的行为一致
// （例如 `close_s.gt(prev_close_base)` 在 prev_close 缺失时自然为 False）。
func (s Series) cmp2(o Series, f func(a, b float64) bool) BoolSeries {
	out := make(BoolSeries, len(s))
	for i := range s {
		out[i] = f(s[i], o[i])
	}
	return out
}

func (s Series) GT2(o Series) BoolSeries { return s.cmp2(o, func(a, b float64) bool { return a > b }) }
func (s Series) GE2(o Series) BoolSeries { return s.cmp2(o, func(a, b float64) bool { return a >= b }) }
func (s Series) LT2(o Series) BoolSeries { return s.cmp2(o, func(a, b float64) bool { return a < b }) }
func (s Series) LE2(o Series) BoolSeries { return s.cmp2(o, func(a, b float64) bool { return a <= b }) }

// arithmetic 是逐元素四则运算（NaN 传染，与 pandas 一致）。
func (s Series) Add(o Series) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] + o[i]
	}
	return out
}

func (s Series) Sub(o Series) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] - o[i]
	}
	return out
}

func (s Series) Div(o Series) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] / o[i]
	}
	return out
}

func (s Series) MulScalar(v float64) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] * v
	}
	return out
}

func (s Series) DivScalar(v float64) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] / v
	}
	return out
}

// FillNARef 对应 `s.fillna(other)`（**用另一条序列**逐元素补缺）。
func (s Series) FillNARef(other Series) Series {
	out := make(Series, len(s))
	for i := range s {
		if isNaN(s[i]) {
			out[i] = other[i]
		} else {
			out[i] = s[i]
		}
	}
	return out
}

func (s Series) AddScalar(v float64) Series {
	out := make(Series, len(s))
	for i := range s {
		out[i] = s[i] + v
	}
	return out
}

func (s Series) SubScalar(v float64) Series { return s.AddScalar(-v) }

// rollingValues 取出第 i 个位置的滚动窗口（pandas 语义）。
//
//	pandas: window=w, min_periods=m
//	  可用值个数 = min(w, i+1)（窗口在序列开头会被截断）
//	  其中**非 NaN 的个数**必须 >= m，否则结果是 NaN
//	  聚合只对窗口内的非 NaN 值做
func rollingValues(s Series, i, window int) ([]float64, bool) {
	if i < 0 || i >= len(s) {
		return nil, false
	}
	start := i + 1 - window
	if start < 0 {
		start = 0
	}
	values := make([]float64, 0, i-start+1)
	for j := start; j <= i; j++ {
		if !isNaN(s[j]) {
			values = append(values, s[j])
		}
	}
	return values, true
}

// rollingAgg 是滚动聚合的公共骨架（min_periods 语义见 rollingValues）。
func rollingAgg(s Series, window, minPeriods int, agg func([]float64) float64) Series {
	out := newSeries(len(s))
	if window < 1 {
		return out
	}
	if minPeriods < 1 {
		minPeriods = 1
	}
	for i := range s {
		values, ok := rollingValues(s, i, window)
		if !ok || len(values) < minPeriods {
			continue
		}
		out[i] = agg(values)
	}
	return out
}

// RollingMean 对应 `s.rolling(window, min_periods=minPeriods).mean()`。
func (s Series) RollingMean(window, minPeriods int) Series {
	return rollingAgg(s, window, minPeriods, func(v []float64) float64 {
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		return sum / float64(len(v))
	})
}

// RollingSum 对应 `s.rolling(...).sum()`。
func (s Series) RollingSum(window, minPeriods int) Series {
	return rollingAgg(s, window, minPeriods, func(v []float64) float64 {
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		return sum
	})
}

// RollingMin / RollingMax 对应 `.min()` / `.max()`。
func (s Series) RollingMin(window, minPeriods int) Series {
	return rollingAgg(s, window, minPeriods, func(v []float64) float64 {
		m := v[0]
		for _, x := range v[1:] {
			if x < m {
				m = x
			}
		}
		return m
	})
}

func (s Series) RollingMax(window, minPeriods int) Series {
	return rollingAgg(s, window, minPeriods, func(v []float64) float64 {
		m := v[0]
		for _, x := range v[1:] {
			if x > m {
				m = x
			}
		}
		return m
	})
}

// RollingMedian 对应 `.median()`（偶数个取中间两个的均值）。
func (s Series) RollingMedian(window, minPeriods int) Series {
	return rollingAgg(s, window, minPeriods, func(v []float64) float64 {
		sorted := append([]float64(nil), v...)
		sort.Float64s(sorted)
		n := len(sorted)
		if n%2 == 1 {
			return sorted[n/2]
		}
		return (sorted[n/2-1] + sorted[n/2]) / 2
	})
}

// RankAvgPct 对应 `s.rank(method="average", pct=True)`：
// NaN 保持 NaN，非 NaN 用**平均名次**（1 基）除以非 NaN 个数。
//
// ⚠ 不做四舍五入 —— 原实现这里没有 round，四舍五入会改变候选排序。
func (s Series) RankAvgPct() Series {
	out := newSeries(len(s))
	valid := make([]float64, 0, len(s))
	for _, v := range s {
		if !isNaN(v) {
			valid = append(valid, v)
		}
	}
	if len(valid) == 0 {
		return out
	}
	sort.Float64s(valid)
	n := float64(len(valid))
	for i, v := range s {
		if isNaN(v) {
			continue
		}
		less := sort.SearchFloat64s(valid, v)
		upper := sort.Search(len(valid), func(k int) bool { return valid[k] > v })
		avgRank := float64(less) + (float64(upper-less)+1)/2
		out[i] = avgRank / n
	}
	return out
}

// ---------------------------------------------------------------------------
// BoolSeries 的运算
// ---------------------------------------------------------------------------

func boolAnd(a, b BoolSeries) BoolSeries {
	out := make(BoolSeries, len(a))
	for i := range a {
		out[i] = a[i] && b[i]
	}
	return out
}

func boolOr(a, b BoolSeries) BoolSeries {
	out := make(BoolSeries, len(a))
	for i := range a {
		out[i] = a[i] || b[i]
	}
	return out
}

func boolNot(a BoolSeries) BoolSeries {
	out := make(BoolSeries, len(a))
	for i := range a {
		out[i] = !a[i]
	}
	return out
}

// boolAndAll / boolOrAll 便于按原实现的 `a & b & c & ...` 链式书写。
func boolAndAll(list ...BoolSeries) BoolSeries {
	if len(list) == 0 {
		return nil
	}
	out := list[0]
	for _, item := range list[1:] {
		out = boolAnd(out, item)
	}
	return out
}

func boolOrAll(list ...BoolSeries) BoolSeries {
	if len(list) == 0 {
		return nil
	}
	out := list[0]
	for _, item := range list[1:] {
		out = boolOr(out, item)
	}
	return out
}

// Float 把布尔序列转成 0.0/1.0（对应 `.astype("float64")`）。
func (b BoolSeries) Float() Series {
	out := make(Series, len(b))
	for i, v := range b {
		if v {
			out[i] = 1.0
		} else {
			out[i] = 0.0
		}
	}
	return out
}

// selectStr 对应 `np.select(condlist, choicelist, default=...)`：
// 取**第一个**为真的条件对应的取值，都不满足则取 default。
func selectStr(conds []BoolSeries, choices []string, def string) StrSeries {
	if len(conds) != len(choices) {
		panic("volprice: selectStr 的条件与取值数量不一致")
	}
	length := 0
	if len(conds) > 0 {
		length = len(conds[0])
	}
	out := make(StrSeries, length)
	for i := 0; i < length; i++ {
		out[i] = def
		for k := range conds {
			if conds[k][i] {
				out[i] = choices[k]
				break
			}
		}
	}
	return out
}

// selectFloat 对应 `np.select(condlist, choicelist, default=...)` 的浮点版本。
func selectFloat(conds []BoolSeries, choices []float64, def float64) Series {
	if len(conds) != len(choices) {
		panic("volprice: selectFloat 的条件与取值数量不一致")
	}
	length := 0
	if len(conds) > 0 {
		length = len(conds[0])
	}
	out := make(Series, length)
	for i := 0; i < length; i++ {
		out[i] = def
		for k := range conds {
			if conds[k][i] {
				out[i] = choices[k]
				break
			}
		}
	}
	return out
}
