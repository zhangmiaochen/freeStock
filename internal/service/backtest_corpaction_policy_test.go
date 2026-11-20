package service

import (
	"testing"

	"freestock/internal/cnlake"
)

// P0：公司行为只能注入到"原始价"口径的回测。
//
// 复权模式的价格序列已经连续（除权日不跳空），再注入送转/分红会把同一笔收益
// 算两遍 —— 引擎实验实测：1 送 1 让总资产从 99,876 变成 198,876；10 元股票
// 每股分红 1 元凭空多出 10% 收益。
func TestShouldInjectCorporateActionsOnlyForRawPrices(t *testing.T) {
	cases := []struct {
		name string
		mode cnlake.AdjMode
		has  bool
		want bool
	}{
		{"原始价 + 有公司行为", cnlake.AdjNone, true, true},
		{"原始价 + 无公司行为", cnlake.AdjNone, false, false},
		{"后复权 + 有公司行为", cnlake.AdjHFQ, true, false},
		{"前复权 + 有公司行为", cnlake.AdjQFQ, true, false},
	}
	for _, c := range cases {
		if got := shouldInjectCorporateActions(c.mode, c.has); got != c.want {
			t.Errorf("%s：得到 %v，应为 %v", c.name, got, c.want)
		}
	}
}
