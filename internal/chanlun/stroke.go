package chanlun

// minStrokeMergedGap 是两个相邻反向分型中心之间最少隔开的**合并 K 线**根数。
//
// 缠论对笔的要求是"分型之间至少有一根独立 K 线"，等价于两个分型的中心下标差 ≥ 4：
// 顶分型占 [i-1, i+1]，底分型占 [j-1, j+1]，j-i ≥ 4 时 [i+2, j-2] 之间还有独立 K 线。
const minStrokeMergedGap = 4

// buildStrokes 由分型构造笔（方案 §5.3）。
//
// 三条规则：
//  1. 顶底必须交替；同类型分型更极端时**延伸**（替换终点，不新起一笔）；
//  2. 两个分型的合并下标差必须 ≥ 4（至少 5 根合并 K 线），否则视为噪声跳过；
//  3. 顶必须高于底（否则是数据异常，跳过）。
//
// 返回的笔里，除最后一笔（若有延伸）外都已确认；最后一笔若价格已越过端点，
// 会补一条 `Confirmed=false` 的延伸笔，方便图连到最新价。
func buildStrokes(fractals []Fractal, bars []Bar, level Level) []Stroke {
	if len(fractals) == 0 || len(bars) == 0 {
		return nil
	}

	// ---- 1. 过滤出交替的端点序列 ----
	endpoints := make([]Fractal, 0, len(fractals)/2+1)
	for _, f := range fractals {
		if len(endpoints) == 0 {
			endpoints = append(endpoints, f)
			continue
		}
		last := &endpoints[len(endpoints)-1]

		if f.Type == last.Type {
			// 同类型：更极端就延伸（替换终点），否则丢弃
			moreExtreme := (f.Type == FractalTop && f.Price > last.Price) ||
				(f.Type == FractalBottom && f.Price < last.Price)
			if moreExtreme {
				*last = f
			}
			continue
		}

		// 反向分型：间距与价格都要成立
		if f.MergedIndex-last.MergedIndex < minStrokeMergedGap {
			continue
		}
		if f.Type == FractalTop && f.Price <= last.Price {
			continue
		}
		if f.Type == FractalBottom && f.Price >= last.Price {
			continue
		}
		endpoints = append(endpoints, f)
	}

	// ---- 2. 端点 → 笔 ----
	strokes := make([]Stroke, 0, len(endpoints))
	for i := 0; i+1 < len(endpoints); i++ {
		start, end := endpoints[i], endpoints[i+1]
		direction := 1
		if start.Type == FractalTop {
			direction = -1
		}
		stroke := Stroke{
			Index:       i,
			Direction:   direction,
			StartDate:   start.Date,
			EndDate:     end.Date,
			StartPrice:  start.Price,
			EndPrice:    end.Price,
			StartIndex:  start.Index,
			EndIndex:    end.Index,
			StartMerged: start.MergedIndex,
			EndMerged:   end.MergedIndex,
			ConfirmDate: end.ConfirmDate,
			Confirmed:   true,
			Level:       level,
		}
		stroke.High, stroke.Low = strokeRange(stroke)
		strokes = append(strokes, stroke)
	}

	// ---- 3. 最后一笔的延伸（未确认） ----
	lastBar := bars[len(bars)-1]
	if len(endpoints) > 0 {
		last := endpoints[len(endpoints)-1]
		if stroke := pendingStroke(last, bars, lastBar, len(strokes), level); stroke != nil {
			strokes = append(strokes, *stroke)
		}
	} else if len(bars) >= 2 {
		// 连一个分型都没有：给一条覆盖全段的未确认笔，至少图上有东西可画
		start, end := bars[0], lastBar
		direction := 1
		if end.Close < start.Close {
			direction = -1
		}
		stroke := Stroke{
			Index:       0,
			Direction:   direction,
			StartDate:   start.Date,
			EndDate:     end.Date,
			StartPrice:  start.Close,
			EndPrice:    end.Close,
			StartIndex:  0,
			EndIndex:    len(bars) - 1,
			ConfirmDate: end.Date,
			Confirmed:   false,
			Level:       level,
			Note:        "尚未形成分型，仅示意区间",
		}
		if direction > 0 {
			stroke.StartPrice = start.Low
			stroke.EndPrice = end.High
		} else {
			stroke.StartPrice = start.High
			stroke.EndPrice = end.Low
		}
		stroke.High, stroke.Low = strokeRange(stroke)
		strokes = append(strokes, stroke)
	}

	return strokes
}

// pendingStroke 从最后一个分型往最新一根 K 线补一条未确认笔。
//
// 只有价格确实越过了分型端点时才补 —— 否则最后一段就是"从端点回到原处"，
// 补一笔会画出一条没有意义的零长度线。
func pendingStroke(last Fractal, bars []Bar, lastBar Bar, index int, level Level) *Stroke {
	direction := 1
	if last.Type == FractalTop {
		direction = -1
	}

	bestIndex := -1
	bestPrice := 0.0
	for i := last.Index + 1; i < len(bars); i++ {
		bar := bars[i]
		if bar.High <= 0 || bar.Low <= 0 {
			continue
		}
		var price float64
		if direction > 0 {
			price = bar.High
			if bestIndex < 0 || price >= bestPrice {
				bestIndex, bestPrice = i, price
			}
		} else {
			price = bar.Low
			if bestIndex < 0 || price <= bestPrice {
				bestIndex, bestPrice = i, price
			}
		}
	}
	if bestIndex < 0 {
		return nil
	}
	// 没有真正越过端点：不补笔
	if direction > 0 && bestPrice <= last.Price {
		return nil
	}
	if direction < 0 && bestPrice >= last.Price {
		return nil
	}

	stroke := Stroke{
		Index:       index,
		Direction:   direction,
		StartDate:   last.Date,
		EndDate:     bars[bestIndex].Date,
		StartPrice:  last.Price,
		EndPrice:    bestPrice,
		StartIndex:  last.Index,
		EndIndex:    bestIndex,
		StartMerged: last.MergedIndex,
		EndMerged:   last.MergedIndex,
		ConfirmDate: lastBar.Date,
		Confirmed:   false,
		Level:       level,
		Note:        "延伸中（尚未出现反向分型确认）",
	}
	stroke.High, stroke.Low = strokeRange(stroke)
	return &stroke
}

// strokeRange 返回一笔的价格区间（起止价的较大/较小值）。
func strokeRange(stroke Stroke) (float64, float64) {
	high := stroke.StartPrice
	low := stroke.StartPrice
	if stroke.EndPrice > high {
		high = stroke.EndPrice
	}
	if stroke.EndPrice < low {
		low = stroke.EndPrice
	}
	return high, low
}
