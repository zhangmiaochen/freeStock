package score

import "testing"

// F14：`market.KeyOf` 返回的是 sh-main / sz-main / chinext / star / bse，
// 而原实现用 主板/创业板/科创板。两套词汇表**直接比较会永远不命中**
// （这个坑踩过一次 —— 分档门槛静默失效）。
func TestStockMarketOfMapping(t *testing.T) {
	cases := map[string]string{
		// 本项目的市场 key（market.KeyOf 的真实取值）
		"sh-main": "main",
		"sz-main": "main",
		"chinext": "gem",
		"star":    "star",
		// bse / 基金类没有对应档位
		"bse":     "",
		"sh-fund": "",
		"":        "",
		// 也接受原实现词汇（幂等）
		"main": "main",
		"gem":  "gem",
	}
	for input, want := range cases {
		if got := StockMarketOf(input); got != want {
			t.Errorf("StockMarketOf(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// F14：只有趋势成长与超跌反弹有门槛表，其余策略**必须返回 0**。
func TestMarketScopeFloorStrategies(t *testing.T) {
	// 趋势成长 + main_gem：主板 80 / 创业板 82
	if got := marketScopeFloor(StrategyTrendGrowth, "main_gem", "sh-main"); got != 80 {
		t.Errorf("趋势成长/主板 应为 80，实际 %v", got)
	}
	if got := marketScopeFloor(StrategyTrendGrowth, "main_gem", "chinext"); got != 82 {
		t.Errorf("趋势成长/创业板 应为 82，实际 %v", got)
	}
	// 超跌反弹：创业板 74 / 科创板 90
	if got := marketScopeFloor(StrategyOversold, "all", "chinext"); got != 74 {
		t.Errorf("超跌/创业板 应为 74，实际 %v", got)
	}
	if got := marketScopeFloor(StrategyOversold, "all", "star"); got != 90 {
		t.Errorf("超跌/科创板 应为 90，实际 %v", got)
	}
	// **高手模拟与低吸没有这张表**（原实现里两个 floor 函数对非本策略返回 0）
	if got := marketScopeFloor(StrategyExpert, "main_gem", "chinext"); got != 0 {
		t.Errorf("高手模拟不该有门槛表，实际 %v", got)
	}
	if got := marketScopeFloor(StrategyTrendLowBuy, "main_gem", "sh-main"); got != 0 {
		t.Errorf("低吸不该有门槛表，实际 %v", got)
	}
	// 表里没写到的组合 → 0
	if got := marketScopeFloor(StrategyTrendGrowth, "main", "star"); got != 0 {
		t.Errorf("趋势成长/主板范围配科创板 → 无门槛，实际 %v", got)
	}
	// bse 映射不出来 → 0
	if got := marketScopeFloor(StrategyTrendGrowth, "all", "bse"); got != 0 {
		t.Errorf("北交所无档位，实际 %v", got)
	}
}
