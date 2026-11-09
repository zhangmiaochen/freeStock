package service

import "testing"

// 质量成长必须尊重组合的板块范围：主板+创业板的组合不能选出科创板股票。
func TestQGrowthMarketAllowed(t *testing.T) {
	cases := []struct {
		code    string
		markets []string
		want    bool
	}{
		{"688192", []string{"main", "gem"}, false}, // 迪哲医药：科创板，应被排除
		{"688192", []string{"star"}, true},
		{"688192", nil, true}, // 未配置范围 = 不限
		{"600519", []string{"main", "gem"}, true},
		{"000001", []string{"main", "gem"}, true},
		{"300750", []string{"main", "gem"}, true},
		{"300750", []string{"main"}, false},
		{"689009", []string{"main", "gem"}, false},
	}
	for _, tc := range cases {
		if got := qGrowthMarketAllowed(tc.code, tc.markets); got != tc.want {
			t.Errorf("%s markets=%v: got %v want %v", tc.code, tc.markets, got, tc.want)
		}
	}
}
