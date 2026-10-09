package score

import "testing"

func lowBuyRow(code string, amount, dd20 float64, market string) Row {
	return Row{
		Code: code, Name: code, Market: market,
		TotalScore: 80, TrendScore: 20, StructureScore: 12, RSScore: 10, RepairScore: 8,
		Amount: amount, DrawdownFromHigh20: dd20,
		Close: 100, Ma5: 99, Ma20: 102, Ma30: 100, Ma60: 95,
		Ma120: 90, Ma250: 85, Ma5Prev: 98, Ma250Prev: 84,
		Open: 99, StockReturn20D: 5, StockReturn5D: 1,
	}
}

// U5：低吸候选排序 —— 三种模式（原实现 :1650-1683）。
//
//	amount_entry（默认）→ 成交额 DESC
//	deep_amount         → 20 日回撤 ASC，再成交额 DESC
//	board_norm_amount   → **市场内**成交额百分位 DESC
func TestLowBuySortModes(t *testing.T) {
	rows := []Row{
		lowBuyRow("A", 100, -5, "main"),
		lowBuyRow("B", 300, -20, "main"),
		lowBuyRow("C", 200, -10, "main"),
	}

	// ① amount_entry：成交额降序 → B, C, A
	cfg := baseConfig(StrategyTrendLowBuy)
	cfg.LowBuySortMode = "amount_entry"
	got := PrioritizeEntryPoints(rows, cfg)
	if got[0].Code != "B" || got[1].Code != "C" || got[2].Code != "A" {
		t.Errorf("amount_entry 应按成交额降序，实际 %s %s %s",
			got[0].Code, got[1].Code, got[2].Code)
	}

	// ② deep_amount：`ascending=True` → **回撤最深**的在前
	//（-20 < -10 < -5，负数越小=跌得越多）→ B, C, A
	cfg.LowBuySortMode = "deep_amount"
	got = PrioritizeEntryPoints(rows, cfg)
	if got[0].Code != "B" || got[1].Code != "C" || got[2].Code != "A" {
		t.Errorf("deep_amount 应按回撤升序（最深在前），实际 %s %s %s",
			got[0].Code, got[1].Code, got[2].Code)
	}

	// ③ board_norm_amount：市场内百分位。主板里 B 最高 → 仍 B 在前
	cfg.LowBuySortMode = "board_norm_amount"
	got = PrioritizeEntryPoints(rows, cfg)
	if got[0].Code != "B" {
		t.Errorf("board_norm_amount 主板内 B 的成交额最高，应排第一，实际 %s", got[0].Code)
	}
}

// 市场内排名：不同市场**各排各的**，跨市场不比较绝对值。
func TestLowBuyBoardNormAmountIsPerMarket(t *testing.T) {
	// 主板的 100 与创业板的 100 在各自市场里排名不同：
	// 主板只有它一只 → 百分位 1.0；创业板有更大的 → 百分位低
	rows := []Row{
		lowBuyRow("MAIN1", 100, -10, "main"),
		lowBuyRow("GEM1", 100, -10, "gem"),
		lowBuyRow("GEM2", 900, -10, "gem"),
	}
	cfg := baseConfig(StrategyTrendLowBuy)
	cfg.LowBuySortMode = "board_norm_amount"
	got := PrioritizeEntryPoints(rows, cfg)

	// 主板那只在自己市场内是第 1（百分位 1.0），应当排最前
	if got[0].Code != "MAIN1" {
		t.Errorf("市场内排名应让'主板唯一'排最前（成交额绝对值最小），实际首位 %s", got[0].Code)
	}
	t.Logf("board_norm_amount 结果：%s → %s → %s", got[0].Code, got[1].Code, got[2].Code)
}

// 非低吸/非趋势成长策略不做重排。
func TestPrioritizeNoopForOtherStrategies(t *testing.T) {
	rows := []Row{lowBuyRow("B", 100, -5, "main"), lowBuyRow("A", 300, -5, "main")}
	for _, strategy := range []string{StrategyOversold, StrategyExpert} {
		got := PrioritizeEntryPoints(rows, baseConfig(strategy))
		if got[0].Code != "B" {
			t.Errorf("%s 不该重排，实际首位 %s", strategy, got[0].Code)
		}
	}
}
