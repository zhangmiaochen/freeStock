// 本文件是 `internal/service` 与 `internal/cnlake` 之间的**适配层**。
//
// 存在的理由：`internal/valuation` 是纯计算包，刻意不认识 parquet、也不认识
// cnlake 的行类型（这样它的口径能被脱离文件系统单测钉住）。服务层需要一个
// 薄薄的翻译层把 cnlake 已有能力暴露给上层，就是这里。
//
// 唯一需要留意的翻译是**日期**：cnlake 的 DATE 列读出来是 int32（1970 起的天数），
// 而 valuation 全程用 YYYYMMDD 整数。所有转换都必须走 daysToDate，
// 不允许在别处自己写一遍（写错一次就会让整段历史对错年份）。
package cnlake

// SharePoint 是一次股本变更（按变更日生效），供上层使用。
//
// ⚠ 与 Lake 内部的 ShareStructureRow 不同：那个是 parquet 的投影（change_date
// 是天数），这个已经折算成 YYYYMMDD，可以直接与行情日期比较。
type SharePoint struct {
	Date        int64
	TotalShares float64
	FloatShares float64
}

// ShareTimeline 是一只股票的股本变更时间线（按日期升序）。
type ShareTimeline []SharePoint

// NewShareTimeline 把数据湖的股本行转成按日期升序的时间线。
func NewShareTimeline(rows []SharePoint) ShareTimeline {
	out := make(ShareTimeline, 0, len(rows))
	for _, row := range rows {
		if row.Date <= 0 || row.TotalShares <= 0 {
			continue
		}
		out = append(out, row)
	}
	// 插入排序：每只股票的股本变更通常只有几条到几十条，
	// 而且它天然稳定 —— 同一天的重复记录保持原始顺序。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Date < out[j-1].Date; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// At 返回"截至 date 生效"的股本；早于第一条记录时返回 false。
func (t ShareTimeline) At(date int64) (SharePoint, bool) {
	index := -1
	for i := range t {
		if t[i].Date <= date {
			index = i
			continue
		}
		break
	}
	if index < 0 {
		return SharePoint{}, false
	}
	return t[index], true
}

// Latest 返回最后一条股本记录。
func (t ShareTimeline) Latest() (SharePoint, bool) {
	if len(t) == 0 {
		return SharePoint{}, false
	}
	return t[len(t)-1], true
}

// AdjFactorPoints 导出复权因子阶梯（内部带缓存）。
//
// 返回的点按日期升序；没有因子文件时返回 nil。上层用 valuation.AdjScaleAt
// 在阶梯上求某个交易日的乘数 —— 那是个纯函数，可以脱离文件系统单测。
func (l *Lake) AdjFactorPoints(code string) []AdjFactorPoint {
	return l.adjFactorPoints(code)
}

// StatementDate 把股本结构的 change_date 折成 YYYYMMDD。
//
// 数据湖里这一列是 BIGINT，实测形态是**年份**（如 2026）而不是 DATE；
// 少数情况下也可能是 1970 起的天数。三种形态都在这里归一，
// 否则历史市值会整体用错股本。
func StatementDate(value int64) int64 {
	switch {
	case value <= 0:
		return 0
	case value >= 19000101:
		return value // 已经是 YYYYMMDD
	case value >= 1900 && value <= 2200:
		// 只有年份：当作该年年初生效（保守取早，避免用上未来才生效的股本）
		return value*10000 + 101
	default:
		return daysToDate(int32(value))
	}
}

// BarDateValue 把 parquet DATE（1970 起的天数）折成 YYYYMMDD。
//
// index_bars 的 trade_date 在数据湖里是 BIGINT 形态的天数（与 daily_bars 的
// DATE 列不同），所以不能直接用 daysToDate 之外的方式解读。
func BarDateValue(days int32) int64 { return daysToDate(days) }
