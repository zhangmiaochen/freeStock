package cne

import "testing"

// DecideCatchUp 的策略（方案 §5.2）：缺失 1~3 天自动补；更多只提示。
//
// 这条策略的意义是**不让人一开机就跑几个小时** —— 缺口大时必须由用户确认。
func TestDecideCatchUpPolicy(t *testing.T) {
	cases := []struct {
		behind []int64
		should bool
		auto   bool
		why    string
	}{
		{nil, false, false, "没有缺口，什么都不做"},
		{[]int64{20261009}, true, true, "缺 1 天，自动补"},
		{[]int64{20261007, 20261008, 20261009}, true, true, "缺 3 天（边界），自动补"},
		{[]int64{20261006, 20261007, 20261008, 20261009}, false, false, "缺 4 天，超出自动阈值，等确认"},
		{[]int64{20260101, 20260102, 20260105, 20260106, 20260107}, false, false, "缺口大，等确认"},
	}
	for _, c := range cases {
		got := DecideCatchUp(&FreshnessReport{BehindDays: c.behind})
		if got.ShouldRun != c.should || got.Auto != c.auto {
			t.Errorf("落后 %v：ShouldRun=%v Auto=%v，期望 %v/%v（%s）",
				c.behind, got.ShouldRun, got.Auto, c.should, c.auto, c.why)
		}
		if got.Reason == "" {
			t.Errorf("落后 %v：必须给出人类可读的理由", c.behind)
		}
	}
	// nil 报告也不能 panic
	if got := DecideCatchUp(nil); got.ShouldRun {
		t.Error("nil 报告不该触发补跑")
	}
}

// summary 的措辞必须点明"只有 --trade-date 能补行情缺口" ——
// 这是方案 §2.2/§2.3 里最容易踩的一条，用户看到提示才知道该点哪个按钮。
func TestSummaryMentionsTradeDateForSmallGap(t *testing.T) {
	r := &FreshnessReport{LastTrading: 20261009, BehindDays: []int64{20261009}}
	got := summarize(r)
	if !contains(got, "trade-date") {
		t.Errorf("小缺口时提示应说明要用 --trade-date 补，实际：%s", got)
	}
	if !contains(got, "20261009") {
		t.Errorf("提示应带上具体日期，实际：%s", got)
	}
}

// 没有缺口时说"已是最新"，并带上最后交易日。
func TestSummaryWhenUpToDate(t *testing.T) {
	got := summarize(&FreshnessReport{LastTrading: 20261009})
	if !contains(got, "最新") || !contains(got, "20261009") {
		t.Errorf("已最新时的措辞不对：%s", got)
	}
	if contains(got, "trade-date") {
		t.Errorf("没有缺口时不该提补跑：%s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
