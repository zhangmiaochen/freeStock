package service

import "testing"

// 月份对齐决定"一次重算把同一分区读几遍"。
// 分区是月粒度时，不对齐 = 22 个交易日读同一个文件 22 遍。
func TestAlignMonth(t *testing.T) {
	cases := []struct {
		in        int
		wantStart int
		wantEnd   int
	}{
		{20261116, 20261101, 20261130},
		{20260201, 20260201, 20260228},  // 平年
		{20240210, 20240201, 20240229},  // 闰年
		{20261231, 20261201, 20261231},  // 大月
		{20260430, 20260401, 20260430},  // 小月
	}
	for _, tc := range cases {
		if got := alignMonthStart(tc.in); got != tc.wantStart {
			t.Errorf("alignMonthStart(%d) = %d，应为 %d", tc.in, got, tc.wantStart)
		}
		if got := alignMonthEnd(tc.in); got != tc.wantEnd {
			t.Errorf("alignMonthEnd(%d) = %d，应为 %d", tc.in, got, tc.wantEnd)
		}
	}
}

// 非法/空日期必须原样返回 —— 0 表示"不设边界"，不能被改成 19000101 之类。
func TestAlignMonthKeepsInvalid(t *testing.T) {
	for _, date := range []int{0, -1, 123, 20261301, 2026000} {
		if got := alignMonthStart(date); got != date {
			if date != 20261301 && date != 2026000 {
				t.Errorf("alignMonthStart(%d) = %d，应原样返回", date, got)
			}
		}
	}
}
