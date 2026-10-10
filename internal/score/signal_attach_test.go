package score

import (
	"testing"
)

// **merge_asof 回填与原版 pandas 的比对。**
//
// 基准来自 `testdata/gen_signal_attach_golden.py`：它把
// `_attach_signal_candidates` 那一段**逐句复刻**，用 pandas 的 `merge_asof`
// 跑出结果。Go 侧只需比对"贴完之后的 9 个 active_signal_* 列"。
//
// 覆盖的边界（都在生成器里刻意构造）：
//   - 信号正好 29 个交易日前 → 命中；30 个交易日前 → 不命中（tolerance=29 不是 30）
//   - 同一 `(trade_date, ts_code)` 同时有 buy 与 candidate_buy → buy 胜出
//   - 非 A 股代码被正则剔除
//   - 左表里不在交易日历内的日期 → 该行被丢掉（不是填默认值！）
//   - 完全没有信号的票 → type=""、age=999、count=0
func TestSignalAttachParityWithPandas(t *testing.T) {
	if len(goldenSignalAttachRows) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_signal_attach_golden.py")
	}
	rows := make([]Row, 0, len(goldenSignalAttachRowCodes))
	for i, code := range goldenSignalAttachRowCodes {
		rows = append(rows, Row{Code: code})
		_ = i
	}
	got, diagnostics := AttachSignalCandidates(
		rows, goldenSignalAttachRowDates, goldenSignalAttachDeduped,
		goldenSignalAttachCalendar, SignalAttachOptions{})

	if diagnostics.InputRows != len(goldenSignalAttachRowCodes) {
		t.Errorf("input_rows：go %d，python %d",
			diagnostics.InputRows, len(goldenSignalAttachRowCodes))
	}
	if len(got) != len(goldenSignalAttachRows) {
		t.Fatalf("输出行数：go %d，python %d（丢弃 %d 行）",
			len(got), len(goldenSignalAttachRows), diagnostics.DroppedRows)
	}
	if diagnostics.DroppedRows != 2 {
		t.Errorf("丢弃行数：go %d，期望 2（构造了两个不在日历里的日期）", diagnostics.DroppedRows)
	}

	matched := 0
	for i, want := range goldenSignalAttachRows {
		mine := got[i]
		prefix := "row[" + itoaScore(i) + "]"
		checkAttachStr(t, prefix+".ts_code", mine.Code, want.Code)
		if mine.SignalType != want.Type {
			t.Errorf("%s.active_signal_type：go %q，python %q（%s %d）",
				prefix, mine.SignalType, want.Type, want.Code, want.TradeDate)
		}
		if mine.SignalAge != want.Age {
			t.Errorf("%s.signal_age：go %d，python %d（%s %d）",
				prefix, mine.SignalAge, want.Age, want.Code, want.TradeDate)
		}
		compareAttachFloat(t, prefix+".active_signal_score", mine.SignalScore, want.Score)
		checkAttachStr(t, prefix+".active_signal_model_id", mine.SignalModelID, want.ModelID)
		checkAttachStr(t, prefix+".active_signal_model_name", mine.SignalModelName, want.ModelName)
		checkAttachStr(t, prefix+".active_signal_level", mine.SignalLevel, want.Level)
		checkAttachStr(t, prefix+".active_signal_level_label", mine.SignalLevelLabel, want.LevelLabel)
		if mine.SignalTradeDate != want.SignalTradeDate {
			t.Errorf("%s.signal_trade_date：go %d，python %d",
				prefix, mine.SignalTradeDate, want.SignalTradeDate)
		}
		if mine.SignalCount != want.Count {
			t.Errorf("%s.signal_count：go %d，python %d", prefix, mine.SignalCount, want.Count)
		}
		if want.Age != 999 {
			matched++
		}
	}
	t.Logf("比对 %d 行，其中 %d 行命中信号", len(got), matched)
	if matched == 0 {
		t.Fatal("没有任何行命中信号 —— 黄金值没有覆盖到匹配路径")
	}
}

// tolerance 的边界必须精确：29 命中、30 不命中。
func TestSignalAttachLookbackBoundary(t *testing.T) {
	calendar := make([]int64, 0, 40)
	for i := 0; i < 40; i++ {
		calendar = append(calendar, 20260100+int64(i))
	}
	candidates := []SignalCandidateRow{{
		TradeDate: calendar[0], Code: "600000.SH", ModelID: "trend_pullback",
		SignalType: "buy", Score: 88.0, SignalCount: 1,
	}}
	for _, tc := range []struct {
		name string
		idx  int
		want bool
	}{
		{"同日（age=0）", 0, true},
		{"29 个交易日后（age=29，边界内）", 29, true},
		{"30 个交易日后（age=30，边界外）", 30, false},
		{"更远（age=35）", 35, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			date := calendar[tc.idx]
			got, _ := AttachSignalCandidates(
				[]Row{{Code: "600000.SH"}}, []int64{date}, candidates, calendar, SignalAttachOptions{})
			if len(got) != 1 {
				t.Fatalf("行数异常：%d", len(got))
			}
			hit := got[0].SignalType == "buy"
			if hit != tc.want {
				t.Errorf("命中：go %v，期望 %v（signal_age=%d）", hit, tc.want, got[0].SignalAge)
			}
			if !tc.want && got[0].SignalAge != 999 {
				t.Errorf("未命中时 signal_age 应为 999，实际 %d", got[0].SignalAge)
			}
			if tc.want && got[0].SignalAge != tc.idx {
				t.Errorf("命中时 signal_age 应为 %d，实际 %d", tc.idx, got[0].SignalAge)
			}
		})
	}
}

// 同一天同票多条信号时按 (priority, score desc, model_id) 取第一条 ——
// 这一条由 `_load_signal_candidate_frame` 的去重决定，这里只验证 Go 的排序比较器
// 在"喂进来已去重的候选"前提下不会再挑错。
func TestSignalAttachPicksNearestNotHighest(t *testing.T) {
	calendar := []int64{20260101, 20260102, 20260103, 20260104, 20260105}
	candidates := []SignalCandidateRow{
		{TradeDate: 20260101, Code: "600000.SH", ModelID: "trend_pullback",
			SignalType: "buy", Score: 99.0, SignalCount: 1},
		{TradeDate: 20260104, Code: "600000.SH", ModelID: "box_range",
			SignalType: "candidate_buy", Score: 60.0, SignalCount: 1},
	}
	got, _ := AttachSignalCandidates(
		[]Row{{Code: "600000.SH"}}, []int64{20260105}, candidates, calendar, SignalAttachOptions{})
	if len(got) != 1 {
		t.Fatalf("行数异常：%d", len(got))
	}
	// backward 取"最近的" —— 1/4 的 60 分胜出，而不是 1/1 的 99 分。
	if got[0].SignalTradeDate != 20260104 || got[0].SignalScore != 60.0 {
		t.Errorf("应取最近的信号（20260104/60.0），实际 %d/%.2f",
			got[0].SignalTradeDate, got[0].SignalScore)
	}
	if got[0].SignalAge != 1 {
		t.Errorf("signal_age 应为 1，实际 %d", got[0].SignalAge)
	}
}

func checkAttachStr(t *testing.T, label, mine, want string) {
	t.Helper()
	if mine != want {
		t.Errorf("%s：\n  go     %q\n  python %q", label, mine, want)
	}
}

func compareAttachFloat(t *testing.T, label string, mine, want float64) {
	t.Helper()
	if mine != want {
		t.Errorf("%s：go %v，python %v", label, mine, want)
	}
}

func itoaScore(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
