package cnlake

import "testing"

// TestCorporateActionShareMultiplier 覆盖送转/拆股的股数乘数。
func TestCorporateActionShareMultiplier(t *testing.T) {
	cases := []struct {
		name string
		a    CorporateAction
		want float64
	}{
		{"纯现金分红", CorporateAction{ActionType: "cash_dividend", CashDividend: 0.733}, 1},
		{"10送3", CorporateAction{ActionType: "bonus", BonusRatio: 0.3}, 1.3},
		{"10转2", CorporateAction{ActionType: "transfer", TransferRatio: 0.2}, 1.2},
		{"送3转2", CorporateAction{ActionType: "bonus", BonusRatio: 0.3, TransferRatio: 0.2}, 1.5},
		{"1拆3", CorporateAction{ActionType: "unit_split", SplitFactor: 3}, 3},
		{"缺失字段按 1", CorporateAction{ActionType: "bonus"}, 1},
	}
	for _, c := range cases {
		if got := c.a.ShareMultiplier(); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: ShareMultiplier = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestApplyCorporateActions 覆盖"先分红（按除权前股数）、再送转"的顺序。
//
// 例：1000 股，先 10 送 3（→1300 股）并每股派 0.5 元（按 1000 股计 500 元）。
func TestApplyCorporateActions(t *testing.T) {
	actions := []CorporateAction{
		{ActionType: "cash_dividend", CashDividend: 0.5},
		{ActionType: "bonus", BonusRatio: 0.3},
	}
	shares, cash := ApplyCorporateActions(1000, actions)
	if shares != 1300 {
		t.Errorf("股数 = %v，期望 1300", shares)
	}
	if cash != 500 {
		t.Errorf("分红现金 = %v，期望 500", cash)
	}

	// 无事件：原样返回
	if s, c := ApplyCorporateActions(800, nil); s != 800 || c != 0 {
		t.Errorf("无事件时 = (%v,%v)，期望 (800,0)", s, c)
	}
}

// TestCorporateActionExDate 日期折算：DATE 列用 int32 承载天数。
func TestCorporateActionExDate(t *testing.T) {
	a := CorporateAction{ExDateRaw: dateToDays(20250729)}
	if got := a.ExDate(); got != 20250729 {
		t.Errorf("ExDate = %d，期望 20250729", got)
	}
}
