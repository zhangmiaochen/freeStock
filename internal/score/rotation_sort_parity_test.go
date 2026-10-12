package score

import (
	"strings"
	"testing"
)

// 与原版 `_candidate_sort_frame`（portfolio_rotation_logic.py:1582-1601）比对**排序结果**。
//
// 那是一个八键排序，`ascending=[False,True,False,True,False,False,False,True]`。
// 抄反任何一位都不报错，只会静默选出不同的票 —— 所以这里比的是**顺序**，
// 而且黄金值里每组都让 `total_score` 相同，逼着后七个键真正起作用。
//
// ⚠ 这个函数在 Go 里曾经是**死代码**（只有测试引用、生产从不调用）：
// 原实现把它嵌在 `select_candidate_pool` 最里层，对每种策略都跑
// （`prioritize_trend_growth_entry_points(_candidate_sort_frame(work))`，:1728）。
// 现已接到 `buildCandidatePoolFrom` 里、位于策略重排之前（`score.SortCandidates`）。
func TestCandidateSortParityWithOriginalPython(t *testing.T) {
	for _, c := range goldenSortCases {
		rows := make([]Row, 0, len(c.Rows))
		for _, r := range c.Rows {
			rows = append(rows, Row{
				Code:                r.Code,
				TotalScore:          r.TotalScore,
				SignalType:          strings.ToLower(r.SignalType),
				SignalScore:         r.SignalScore,
				SignalAge:           r.SignalAge,
				RelativeStrength20D: r.RelativeStrength20D,
				RepairScore:         r.RepairScore,
				ResearchScore:       r.ResearchScore,
			})
		}
		got := SortCandidates(rows)
		gotOrder := make([]string, 0, len(got))
		for _, r := range got {
			gotOrder = append(gotOrder, r.Code)
		}
		if len(gotOrder) != len(c.Order) {
			t.Errorf("[%s] 行数变了：原版 %d 行，Go %d 行", c.Desc, len(c.Order), len(gotOrder))
			continue
		}
		for i := range c.Order {
			if gotOrder[i] != c.Order[i] {
				t.Errorf("[%s] 顺序不一致：\n  原版 %v\n  Go   %v", c.Desc, c.Order, gotOrder)
				break
			}
		}
	}
}
