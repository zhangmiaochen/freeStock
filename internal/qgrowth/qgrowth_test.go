package qgrowth

import (
	"math"
	"testing"
)

func TestWeightsCapAndSum(t *testing.T) {
	scores := []float64{0.9, 0.85, 0.8, 0.75, 0.7, 0.65, 0.6, 0.55}
	w := Weights(scores, 0.15)
	if len(w) != 8 {
		t.Fatalf("len=%d", len(w))
	}
	sum := 0.0
	for _, v := range w {
		if v > 0.15+1e-9 {
			t.Errorf("单票 %.4f 超过上限 15%%", v)
		}
		if v <= 0 {
			t.Errorf("权重必须为正: %.4f", v)
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("权重和 %.6f != 1", sum)
	}
	// 第一名权重应 >= 最后一名
	if w[0] < w[7] {
		t.Errorf("权重未按分数降序: %v", w)
	}
}

func TestWeightsThreeNamesLeavesCash(t *testing.T) {
	w := Weights([]float64{0.9, 0.8, 0.7}, 0.15)
	sum := 0.0
	for _, v := range w {
		if v > 0.15+1e-9 {
			t.Errorf("超上限: %v", v)
		}
		sum += v
	}
	if math.Abs(sum-0.45) > 1e-6 {
		t.Errorf("3 只 × 15%% 应只占 45%%，实际 %.4f", sum)
	}
}

func TestScoreRanksAndFilters(t *testing.T) {
	cfg := DefaultConfig()
	cands := []Candidate{
		{Code: "A", Close: 10, MA200: 8, DD20: -5, Amount20: 1e8, ListedDays: 500,
			Fund: Fundamental{ROE: 20, RevYoY: 30, NPYoY: 40, GrossMargin: 50, DebtRatio: 30, OCFRatio: 1, Valid: true}},
		{Code: "B", Close: 10, MA200: 8, DD20: -5, Amount20: 1e8, ListedDays: 500,
			Fund: Fundamental{ROE: 10, RevYoY: 5, NPYoY: 5, GrossMargin: 20, DebtRatio: 60, OCFRatio: 0.5, Valid: true}},
		{Code: "C", Close: 10, MA200: 8, DD20: -5, Amount20: 1e8, ListedDays: 500,
			Fund: Fundamental{ROE: 15, RevYoY: 15, NPYoY: 20, GrossMargin: 35, DebtRatio: 45, OCFRatio: 0.8, Valid: true}},
	}
	for _, c := range cands {
		if !cfg.Eligible(c) {
			t.Fatalf("%s 应通过过滤", c.Code)
		}
		if !cfg.PullbackOK(c) {
			t.Fatalf("%s 应满足回调", c.Code)
		}
	}
	Score(cands)
	if !(cands[0].Score > cands[2].Score && cands[2].Score > cands[1].Score) {
		t.Errorf("分数排序应为 A>C>B，实际 %v %v %v", cands[0].Score, cands[1].Score, cands[2].Score)
	}
}

func TestFilters(t *testing.T) {
	cfg := DefaultConfig()
	base := Candidate{Close: 10, MA200: 8, DD20: -5, Amount20: 1e8, ListedDays: 500,
		Fund: Fundamental{ROE: 10, RevYoY: 5, NPYoY: 5, GrossMargin: 20, DebtRatio: 50, OCFRatio: 0.5, Valid: true}}

	cases := []struct {
		name string
		mut  func(*Candidate)
		ok   bool
	}{
		{"基准", func(*Candidate) {}, true},
		{"跌破MA200", func(c *Candidate) { c.Close = 7 }, false},
		{"回撤不足", func(c *Candidate) { c.DD20 = -1 }, false},
		{"回撤过深", func(c *Candidate) { c.DD20 = -25 }, false},
		{"ROE不足", func(c *Candidate) { c.Fund.ROE = 5 }, false},
		{"营收负增长", func(c *Candidate) { c.Fund.RevYoY = -1 }, false},
		{"毛利率低", func(c *Candidate) { c.Fund.GrossMargin = 10 }, false},
		{"负债过高", func(c *Candidate) { c.Fund.DebtRatio = 80 }, false},
		{"现金流差", func(c *Candidate) { c.Fund.OCFRatio = 0.1 }, false},
		{"上市不足", func(c *Candidate) { c.ListedDays = 100 }, false},
		{"成交额不足", func(c *Candidate) { c.Amount20 = 1e7 }, false},
	}
	for _, tc := range cases {
		c := base
		tc.mut(&c)
		got := cfg.Eligible(c) && cfg.PullbackOK(c)
		if got != tc.ok {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.ok)
		}
	}
}
