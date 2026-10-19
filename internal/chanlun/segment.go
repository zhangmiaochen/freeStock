package chanlun

// buildSegments 由笔构造线段（方案 §5.4）。
//
// 严格定义要用「特征序列 + 分型」。这里采用与之等价的**笔破坏 + 反向不创新高/新低**
// 判定，理由写在这里而不是藏在代码里：
//
//   - 向上线段：从某个底开始。线段内的向上笔不断创出新高 → 延伸；
//     当某个向下笔**跌破它前一个向上笔的起点**（即跌破上一个回抽低点）时记为"破坏候选"，
//     随后若第一根向上笔**未能创出线段新高**，则线段在该最高点结束；
//   - 向下线段对称。
//
// 这样得到的线段与笔方向交替、端点不重叠、满足结合律；与特征序列分型在少数边界
// 形态上可能给出不同分段，属于方案 §5.7 允许的"多义性"，界面与文档都会说明。
//
// 至少 3 笔才构成线段；数据末尾尚未确认破坏的线段以 `Confirmed=false` 输出，便于画图。
func buildSegments(strokes []Stroke, level Level) []Segment {
	if len(strokes) < 2 {
		return nil
	}

	out := make([]Segment, 0, len(strokes)/3+1)
	i := 0
	for i < len(strokes) {
		dir := strokes[i].Direction
		start := strokes[i]
		bestEnd := strokes[i].EndPrice
		bestEndIdx := i
		pendingBreak := -1
		confirmed := false
		breakDate := 0

		j := i + 1
		for ; j < len(strokes); j++ {
			stroke := strokes[j]

			if stroke.Direction == dir {
				better := (dir > 0 && stroke.EndPrice > bestEnd) ||
					(dir < 0 && stroke.EndPrice < bestEnd)
				if better {
					bestEnd = stroke.EndPrice
					bestEndIdx = j
					pendingBreak = -1
					continue
				}
				// 同向笔没能创新高/新低，并且此前已有破坏候选 → 线段确认结束
				if pendingBreak >= 0 {
					confirmed = true
					breakDate = strokes[pendingBreak].ConfirmDate
					if breakDate == 0 {
						breakDate = stroke.ConfirmDate
					}
					break
				}
				continue
			}

			// 反向笔：判断是否跌破/涨过前一根同向笔的起点
			if pendingBreak < 0 {
				prev := strokes[j-1]
				broke := (dir > 0 && stroke.EndPrice < prev.StartPrice) ||
					(dir < 0 && stroke.EndPrice > prev.StartPrice)
				if broke {
					pendingBreak = j
				}
			}
		}

		// 至少 3 笔才成线段
		if bestEndIdx-i+1 >= 3 {
			end := strokes[bestEndIdx]
			out = append(out, Segment{
				Index:       len(out),
				Direction:   dir,
				StartDate:   start.StartDate,
				EndDate:     end.EndDate,
				StartPrice:  start.StartPrice,
				EndPrice:    end.EndPrice,
				StartIndex:  start.StartIndex,
				EndIndex:    end.EndIndex,
				StartStroke: i,
				EndStroke:   bestEndIdx,
				BreakDate:   breakDate,
				Confirmed:   confirmed,
				Level:       level,
			})
		}

		next := bestEndIdx + 1
		if next <= i {
			next = i + 1
		}
		i = next
	}

	return out
}
