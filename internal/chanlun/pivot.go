package chanlun

import "math"

// buildPivots 用笔构造中枢（方案 §5.5）。
//
// 中枢 = **至少三个连续次级别走势的重叠区间**。本实现里"次级别走势"就是笔
// （只有日 K 时，日线笔即最小级别走势，见方案 §3.2），所以：
//
//	ZG = min(三笔的最高价)，ZD = max(三笔的最低价)，ZG > ZD 才成立。
//
// 后续笔只要与 [ZD, ZG] 仍有交集就**延伸**；笔数 ≥ 9 标记为**扩展**；
// 离开中枢后重新成立的中枢标记为**新生**。中枢一旦被离开，离开那一段就是
// 背驰与三类买卖点要比较的"离开段"。
func buildPivots(strokes []Stroke, bars []Bar, level Level) []Pivot {
	if len(strokes) < 3 {
		return nil
	}

	out := make([]Pivot, 0, len(strokes)/3+1)
	i := 0
	for i+2 < len(strokes) {
		first, second, third := strokes[i], strokes[i+1], strokes[i+2]

		zg := minFloat(first.High, minFloat(second.High, third.High))
		zd := maxFloat(first.Low, maxFloat(second.Low, third.Low))
		if zg <= zd {
			// 三笔不重叠，构不成中枢：起点后移一笔再试
			i++
			continue
		}

		count := 3
		end := i + 2
		// 延伸：后续笔仍与重叠区相交
		for k := i + 3; k < len(strokes); k++ {
			if !strokeIntersects(strokes[k], zd, zg) {
				break
			}
			count++
			end = k
		}

		leaveStroke := -1
		if end+1 < len(strokes) {
			leaveStroke = end + 1
		}

		status := PivotForming
		switch {
		case count >= 9:
			status = PivotExpanded
		case count > 3:
			status = PivotExtension
		case len(out) > 0:
			status = PivotNew
		}

		pivot := Pivot{
			Index:       len(out),
			Level:       level,
			ZG:          round4(zg),
			ZD:          round4(zd),
			StartDate:   strokes[i].StartDate,
			EndDate:     strokes[end].EndDate,
			StartIndex:  strokes[i].StartIndex,
			EndIndex:    strokes[end].EndIndex,
			StartStroke: i,
			EndStroke:   end,
			StrokeCount: count,
			Status:      status,
			EnterStroke: -1,
			LeaveStroke: leaveStroke,
			ConfirmDate: strokes[i+2].ConfirmDate,
			Confirmed:   strokes[i+2].Confirmed,
		}
		if i > 0 {
			pivot.EnterStroke = i - 1
		}
		switch status {
		case PivotExtension:
			pivot.Note = "中枢延伸（后续笔仍在重叠区内）"
		case PivotExpanded:
			pivot.Note = "中枢扩展（≥9 笔仍重叠，级别有升级可能）"
		case PivotNew:
			pivot.Note = "离开前中枢后新形成的中枢"
		}
		out = append(out, pivot)

		if leaveStroke >= 0 {
			i = leaveStroke
			continue
		}
		// 没有离开段（数据在重叠区内结束）：结束
		break
	}

	return out
}

// strokeIntersects 判断一笔的价格区间与中枢重叠区 [zd, zg] 是否有交集。
func strokeIntersects(stroke Stroke, zd, zg float64) bool {
	return stroke.Low <= zg && stroke.High >= zd
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func round4(value float64) float64 {
	return math.Round(value*10000) / 10000
}
