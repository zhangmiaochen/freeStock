package chanlun

import "sort"

// detectFractals 识别顶分型与底分型（方案 §5.2）。
//
// 判定在**合并 K 线**序列上做：中间那根的高点高于左右两根 → 顶分型；
// 低点低于左右两根 → 底分型。因为依赖右边那一根，分型天然是"等待后续 K 线确认"的：
// 只有 i+1 存在时才可能有分型，`ConfirmDate` 取第三根（i+1）的日期。
//
// 形成时间取极值所在**原始 K 线**的日期（不是合并段的结束日）—— 否则图上标注会
// 偏到后面几根，用户看到的顶不在最高价上。
func detectFractals(merged []MergedBarInfo, level Level) []Fractal {
	out := make([]Fractal, 0, len(merged)/2)

	for i := 1; i+1 < len(merged); i++ {
		prev, cur, next := merged[i-1], merged[i], merged[i+1]

		// 顶分型要求严格高于左右；相等时报错性极低，按"不是分型"处理更稳
		if cur.High > prev.High && cur.High > next.High {
			out = append(out, Fractal{
				Date:         cur.HighDate,
				Type:         FractalTop,
				Price:        cur.High,
				Index:        cur.HighIndex,
				MergedIndex:  i,
				ConfirmDate:  next.Date,
				ConfirmIndex: next.EndIndex,
				Level:        level,
			})
		}
		if cur.Low < prev.Low && cur.Low < next.Low {
			out = append(out, Fractal{
				Date:         cur.LowDate,
				Type:         FractalBottom,
				Price:        cur.Low,
				Index:        cur.LowIndex,
				MergedIndex:  i,
				ConfirmDate:  next.Date,
				ConfirmIndex: next.EndIndex,
				Level:        level,
			})
		}
	}

	// 同一根合并 K 线极少同时给出顶和底（合并后不会包含，基本不可能），
	// 若真出现，按时间先后稳定排序，保证下游交替判定不受影响。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MergedIndex != out[j].MergedIndex {
			return out[i].MergedIndex < out[j].MergedIndex
		}
		return out[i].Index < out[j].Index
	})
	return out
}
