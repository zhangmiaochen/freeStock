package service

import (
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **回归**：低吸广度就绪数必须在**范围过滤之前**统计（原版口径）。
//
// 原版在 `market_df` 上数 —— 那是 `apply_strategy_scores` **之前**的全市场行情帧，
// **与组合范围无关**（portfolio_service.py:1841-1853），算好后挂在每一行上落库。
//
// Go 回测曾经在 `day.Rows` 上数，而那是**已经被组合范围过滤过**的行。后果不是小偏差：
// 就绪数决定低吸的**动态目标只数档位**（1000/1500/2000/2600），档位又决定建仓层数：
//
//	就绪 2019（全市场） → desired=3 → ⌊10/3⌋=3 层 → 3 只 × 30%
//	就绪 1716（主板+创业板）→ desired=2 → ⌊10/2⌋=5 层 → 2 只 × 50%
//
// 实测（用户库 2025-10-27）就绪数 2019 vs 1716，成交额 49,971 元 = 50% 能精确对上
// 错误那一路。241 个交易日里 87 天档位不同、26 天"是否强制清仓"判定相反。
//
// 这个用例把不变量钉死：**范围过滤只影响 `days[].Rows`，不影响就绪数**。
func TestBacktestReadyCountCountsBeforeScopeFilter(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatalf("取库失败：%v", err)
	}

	// 五行都满足就绪掩码（close>0 && ma60>0 && close>ma60 && ma20>ma60），
	// 这样"就绪数"与"范围过滤"两个变量能分开断言。
	rows := []portfolio.StrengthRow{
		scopeRow("600519", "sh-main", "白酒", 80),
		scopeRow("601318", "sh-main", "保险", 70),
		scopeRow("688111", "star", "软件", 90),
		// ⚠ 下面两行**必须被白名单排除**（原版 `_load_market_frame` 的代码前缀
		// 正则不含它们，portfolio_service.py:1256-1258）：
		//   920571 北交所（market=bse）—— 实测库里 62,379 行
		//   302xxx 新版创业板代码段 —— market 是 chinext，
		//          **只排除 bse 会漏掉它**（实测库里 243 行）
		scopeRow("920571", "bse", "", 80),
		scopeRow("302001", "chinext", "", 80),
	}
	for i := range rows {
		rows[i].TradeDate = 20251027
		rows[i].StrategyID = score.StrategyTrendLowBuy
	}

	// 范围故意放开到全部市场 —— 这样"就绪数"只受**白名单**影响，
	// 不受范围过滤影响（范围过滤已由上一条断言单独覆盖）。
	item := portfolio.Portfolio{
		ID: "pf_ready", StrategyID: score.StrategyTrendLowBuy,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType:      portfolio.ScopeAll,
		ScopeValueJSON: `{"markets":["main","gem","star","bse"]}`,
	}

	days, _, _, readyByDate := svc.buildStrengthDays(store, rows, item)

	// ① 范围放开后剩 4 行 —— `bse` 在范围词汇里**没有对应档位**
	//（`MarketInScope` 只认 main/gem/star），所以 920571 是被范围过滤掉的；
	// 302001 的 market 是 chinext，**在范围内**，所以它活着。
	if len(days) != 1 {
		t.Fatalf("应只有 1 个交易日，实际 %d", len(days))
	}
	if got := len(days[0].Rows); got != 4 {
		t.Errorf("范围放开后应剩 4 行（bse 无档位被范围剔除），实际 %d", got)
	}

	// ② 就绪数必须把 302 也排掉 → 只剩 3
	//
	// ⚠ 这一条才是白名单的关键：920 靠"范围里没有 bse 档位"也能被剔掉，
	// 但 **302 是 chinext、在范围内** —— 只写 `market != "bse"` 会把它算进来。
	got, ok := readyByDate["20251027"]
	if !ok {
		t.Fatalf("就绪数表里没有 20251027：%v", readyByDate)
	}
	if got != 3 {
		t.Errorf("低吸广度就绪数应为 3（600519/601318/688111）；"+
			"302001 必须被 A 股代码前缀白名单排除"+
			"（原版 `_load_market_frame` 的正则不含 302），实际 %d", got)
	}
}
