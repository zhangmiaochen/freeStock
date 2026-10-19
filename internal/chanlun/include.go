package chanlun

// mergeBars 做 K 线包含处理（方案 §5.1）。
//
// 规则（标准缠论）：
//
//   - 相邻两根存在包含关系（一根的高低区间完全盖住另一根）时合并；
//   - 合并方向由**前一根合并 K 线**决定：
//     向上（前一根比它前一根高）→ 新高取两者较大、新低取两者较大；
//     向下 → 新高取两者较小、新低取两者较小；
//   - 合并结果保留原始下标区间与极值所在下标，保证**可追溯**。
//
// 为什么方向必须由前一根决定（而不是当前两根）：包含关系本身没有方向，
// 方向来自它处在的走势里。用当前两根的高低去判断会得到"自己证明自己"的结果。
func mergeBars(bars []Bar) []MergedBarInfo {
	out := make([]MergedBarInfo, 0, len(bars))

	for i, bar := range bars {
		if bar.High <= 0 || bar.Low <= 0 || bar.High < bar.Low {
			// 脏数据（停牌/未同步）不参与结构，但不能让整段断裂
			continue
		}
		current := MergedBarInfo{
			StartIndex: i,
			EndIndex:   i,
			High:       bar.High,
			Low:        bar.Low,
			HighIndex:  i,
			LowIndex:   i,
			HighDate:   bar.Date,
			LowDate:    bar.Date,
			Date:       bar.Date,
			Members:    1,
		}

		if len(out) == 0 {
			current.Index = 0
			out = append(out, current)
			continue
		}

		last := &out[len(out)-1]
		contained := (bar.High <= last.High && bar.Low >= last.Low) ||
			(bar.High >= last.High && bar.Low <= last.Low)
		if !contained {
			current.Index = len(out)
			current.Direction = directionOf(*last, current)
			if current.Direction == 0 {
				current.Direction = 1
			}
			out = append(out, current)
			continue
		}

		// 合并进最后一根
		dir := last.Direction
		if dir == 0 {
			// 第一根合并 K 线还没有方向：按当前这根相对它的位置推断
			if bar.High > last.High {
				dir = 1
			} else {
				dir = -1
			}
			last.Direction = dir
		}

		last.Members++
		last.EndIndex = i
		last.Date = bar.Date

		if dir > 0 {
			if bar.High > last.High {
				last.High = bar.High
				last.HighIndex = i
				last.HighDate = bar.Date
			}
			if bar.Low > last.Low {
				last.Low = bar.Low
				last.LowIndex = i
				last.LowDate = bar.Date
			}
		} else {
			if bar.High < last.High {
				last.High = bar.High
				last.HighIndex = i
				last.HighDate = bar.Date
			}
			if bar.Low < last.Low {
				last.Low = bar.Low
				last.LowIndex = i
				last.LowDate = bar.Date
			}
		}
	}

	return out
}

// directionOf 判断新合并 K 线相对上一根的方向（+1 上 / -1 下 / 0 无法判断）。
//
// 只比较高点：高点抬高算向上，降低算向下；高点相同时用低点判断；
// 两根完全相同时返回 0，由调用方沿用上一根的方向（数据里极少见，不能因此断裂）。
func directionOf(prev, current MergedBarInfo) int {
	if current.High > prev.High {
		return 1
	}
	if current.High < prev.High {
		return -1
	}
	if current.Low > prev.Low {
		return 1
	}
	if current.Low < prev.Low {
		return -1
	}
	return prev.Direction
}
