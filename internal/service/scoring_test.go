package service

import (
	"testing"

	"freestock/internal/score"
	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 纯函数
// ---------------------------------------------------------------------------

func TestScoreWindowStart(t *testing.T) {
	// 320 个交易日 ≈ 480 个日历日
	got := scoreWindowStart(20261008, 320)
	if got >= 20261008 {
		t.Errorf("起始日应早于评分日: %d", got)
	}
	if got > 20260601 {
		t.Errorf("320 个交易日应回看到 6 月之前，实际 %d", got)
	}
	// 结果应能解析回日期
	if got < 20250101 {
		t.Errorf("回看过头了: %d", got)
	}
	// 非法日期时原样返回，不 panic
	if got := scoreWindowStart(0, 10); got != 0 {
		t.Errorf("非法日期应原样返回: %d", got)
	}
}

func TestBarsFromQuotes(t *testing.T) {
	quotes := []*stockdb.Quote{
		{Date: 20260103, Close: 12, High: 12.5, Low: 11.5, Volume: 100, Amount: 1e6},
		{Date: 20260101, Close: 10, High: 10.5, Low: 9.5, Volume: 90, Amount: 9e5},
		{Date: 20260102, Close: 11, High: 11.5, Low: 10.5, Volume: 95, Amount: 9.5e5},
	}
	bars := barsFromQuotes(quotes)
	if len(bars) != 3 {
		t.Fatalf("应转换 3 根，实际 %d", len(bars))
	}
	// **必须按日期升序**（评分依赖滚动窗口的顺序）
	for i := 1; i < len(bars); i++ {
		if bars[i].Date <= bars[i-1].Date {
			t.Errorf("应升序排列: %v", bars)
			break
		}
	}
	// 收盘价 <= 0 的脏数据被丢掉
	bad := barsFromQuotes([]*stockdb.Quote{
		{Date: 20260101, Close: 0},
		{Date: 20260102, Close: 10},
		nil,
	})
	if len(bad) != 1 {
		t.Errorf("收盘价无效的记录应被丢弃，实际 %d", len(bad))
	}
}

func TestTrendGrowthAdjustment(t *testing.T) {
	// 行业分位 1.0 → 6 分；rs20 = 30 → 3 分；合计 9
	ind := score.Indicators{IndustryStrengthPct: 1, RelativeStrength20D: 30}
	if got := trendGrowthAdjustment(ind, score.RepairFeatures{}, 0); got != 9 {
		t.Errorf("满分修正应为 9，实际 %v", got)
	}
	// 行业弱（分位 0.2 → 20 分）且 RS 低（10 < 12）→ 惩罚 -4。
	// 基础项 = 0.2*6 + ((10-5)/25)*3 = 1.2 + 0.6 = 1.8，再 -4 → -2.2
	cold := score.Indicators{IndustryStrengthPct: 0.2, RelativeStrength20D: 10}
	if got := trendGrowthAdjustment(cold, score.RepairFeatures{}, 0); got != -2.2 {
		t.Errorf("行业弱且 RS 低应为 -2.2，实际 %v", got)
	}
	// 惩罚生效的验证：同样的基础项，去掉惩罚条件（rs20 >= 12）就不该扣分
	noPenalty := score.Indicators{IndustryStrengthPct: 0.2, RelativeStrength20D: 13}
	if got := trendGrowthAdjustment(noPenalty, score.RepairFeatures{}, 0); got <= trendGrowthAdjustment(cold, score.RepairFeatures{}, 0) {
		t.Errorf("无惩罚的组合应比有惩罚的高: %v vs %v", got, trendGrowthAdjustment(cold, score.RepairFeatures{}, 0))
	}
	// 行业强、RS 中等：6*0.8 + 3*((20-5)/25) = 4.8 + 1.8 = 6.6，不触发惩罚
	strong := score.Indicators{IndustryStrengthPct: 0.8, RelativeStrength20D: 20}
	if got := trendGrowthAdjustment(strong, score.RepairFeatures{}, 0); got != 6.6 {
		t.Errorf("行业强且 RS 中等应为 6.6，实际 %v", got)
	}
	// 另一个惩罚：dd120 > -2 且 rs20 > 30 → -5
	hot := score.Indicators{IndustryStrengthPct: 1, RelativeStrength20D: 35, DrawdownFromHigh120: -1}
	plain := score.Indicators{IndustryStrengthPct: 1, RelativeStrength20D: 35, DrawdownFromHigh120: -10}
	if got := trendGrowthAdjustment(hot, score.RepairFeatures{}, 0); got != trendGrowthAdjustment(plain, score.RepairFeatures{}, 0)-5 {
		t.Errorf("高位过热应多扣 5 分: %v vs %v", got, trendGrowthAdjustment(plain, score.RepairFeatures{}, 0))
	}
}

func TestScoreWindow01(t *testing.T) {
	cases := []struct{ value, low, high, want float64 }{
		{5, 5, 30, 0},
		{30, 5, 30, 1},
		{17.5, 5, 30, 0.5},
		{-10, 5, 30, 0},
		{100, 5, 30, 1},
	}
	for _, c := range cases {
		if got := scoreWindow01(c.value, c.low, c.high); got != c.want {
			t.Errorf("scoreWindow01(%v,%v,%v) = %v，期望 %v", c.value, c.low, c.high, got, c.want)
		}
	}
	// 区间非法时返回 0，不除零
	if got := scoreWindow01(5, 10, 10); got != 0 {
		t.Errorf("区间非法应返回 0，实际 %v", got)
	}
}

func TestRound4(t *testing.T) {
	if got := round4(1.234567); got != 1.2346 {
		t.Errorf("round4 错误: %v", got)
	}
	if got := round4(0); got != 0 {
		t.Errorf("round4(0) 错误: %v", got)
	}
}

// ---------------------------------------------------------------------------
// 真实数据端到端
// ---------------------------------------------------------------------------

// 用本地 free-stockdb 对几只真实股票评分。
func TestScoreStocksRealData(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	result, err := scoring.ScoreStocks([]string{"600519", "000001", "300750", "601318"}, 0, 0)
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if result.TradeDate <= 0 {
		t.Fatal("应解析出评分日")
	}
	if result.Scored == 0 {
		t.Fatal("应至少评出一只股票")
	}

	for _, row := range result.Rows {
		// 分数必须在合法区间
		if row.TotalScore < 0 || row.TotalScore > 100 {
			t.Errorf("%s 总分越界: %v", row.Code, row.TotalScore)
		}
		if row.TrendScore < 0 || row.TrendScore > 30 {
			t.Errorf("%s 趋势分越界: %v", row.Code, row.TrendScore)
		}
		if row.StructureScore < 0 || row.StructureScore > 20 {
			t.Errorf("%s 结构分越界: %v", row.Code, row.StructureScore)
		}
		if row.RSScore < 0 || row.RSScore > 20 {
			t.Errorf("%s RS 分越界: %v", row.Code, row.RSScore)
		}
		if row.CapitalScore < 0 || row.CapitalScore > 15 {
			t.Errorf("%s 资金分越界: %v", row.Code, row.CapitalScore)
		}
		// 价格与均线
		if row.Close <= 0 {
			t.Errorf("%s 收盘价应大于 0: %v", row.Code, row.Close)
		}
		if row.Ma5 <= 0 || row.Ma20 <= 0 || row.Ma60 <= 0 || row.Ma120 <= 0 || row.Ma250 <= 0 {
			t.Errorf("%s 长周期均线应算得出来: ma5=%v ma20=%v ma60=%v ma120=%v ma250=%v",
				row.Code, row.Ma5, row.Ma20, row.Ma60, row.Ma120, row.Ma250)
		}
		// 横截面分位必须在 0..1
		if row.RelativeStrength20D == 0 && row.StockReturn5D == 0 {
			t.Errorf("%s 收益指标不该全为 0", row.Code)
		}
		if row.Name == "" {
			t.Errorf("%s 名称不该为空", row.Code)
		}
		t.Logf("%s %s 总分 %.2f（趋势 %.1f 结构 %.1f RS %.1f 资金 %.1f 修复 %.1f）现价 %.2f MA250 %.2f RS20 %.2f",
			row.Code, row.Name, row.TotalScore, row.TrendScore, row.StructureScore,
			row.RSScore, row.CapitalScore, row.RepairScore, row.Close, row.Ma250, row.RelativeStrength20D)
	}

	// 未覆盖的字段要如实声明
	if len(result.Unavailable) == 0 {
		t.Error("应声明哪些字段没能计算（研究分/板块研究分/模型信号）")
	}
	t.Logf("市场状态: %s（%s）bull=%.1f bear=%.1f",
		result.MarketState.State, result.MarketState.Label,
		result.MarketState.BullScore, result.MarketState.BearScore)
	t.Logf("未覆盖: %v", result.Unavailable)
}

// 市场状态必须是三种之一。
func TestMarketStateRealData(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	universe, err := svc.GetStockUniverse(false)
	if err != nil {
		t.Fatalf("取股票列表失败: %v", err)
	}
	provider, _ := svc.snapshot()
	state := scoring.marketState(provider, int(universe.TradeDate))
	if state.State != score.StateBull && state.State != score.StateRange && state.State != score.StateBear {
		t.Errorf("市场状态非法: %s", state.State)
	}
	if state.Label == "" {
		t.Error("市场状态应有中文标签")
	}
	// 窗口应该取到了足够的天数
	if state.Days < 20 {
		t.Errorf("应取到 >=20 个交易日，实际 %d", state.Days)
	}
	t.Logf("市场状态 %s（%s）天数 %d bull=%.1f bear=%.1f 代理 20 日收益 %.2f%%",
		state.State, state.Label, state.Days, state.BullScore, state.BearScore, state.ProxyRet20)
}

// 数据不足的代码应被跳过而不是报错。
func TestScoreStocksSkipsUnknownCodes(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	result, err := scoring.ScoreStocks([]string{"600519", "999999", "000000"}, 0, 0)
	if err != nil {
		t.Fatalf("不该因为有不存在的代码就报错: %v", err)
	}
	if result.Scored == 0 {
		t.Error("600519 应该能评出来")
	}
	if result.Skipped == 0 {
		t.Error("不存在的代码应被计入 skipped")
	}
	t.Logf("评出 %d 只，跳过 %d 只", result.Scored, result.Skipped)
}

// 指定历史日期时不应报错（数据可能不足，但要走通）。
func TestScoreStocksHistoricalDate(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	result, err := scoring.ScoreStocks([]string{"600519", "000001"}, 20260630, 0)
	if err != nil {
		t.Fatalf("历史日期评分不该报错: %v", err)
	}
	if result.TradeDate != 20260630 {
		t.Errorf("评分日应保留用户指定的值，实际 %d", result.TradeDate)
	}
	t.Logf("历史日期 %d 评出 %d 只", result.TradeDate, result.Scored)
}

// 空输入
func TestScoreStocksEmpty(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	result, err := scoring.ScoreStocks([]string{}, 0, 30)
	if err != nil {
		t.Fatalf("空输入不该报错: %v", err)
	}
	// 传空切片时语义是"全市场"，limit=30 只取前 30 只 ——
	if result.Unavailable == nil {
		t.Error("应始终带上未覆盖字段说明")
	}
	t.Logf("空输入按全市场处理（limit=30）：评出 %d 只，跳过 %d 只", result.Scored, result.Skipped)
	if result.Scored > 30 {
		t.Errorf("limit=30 时不该评出超过 30 只，实际 %d", result.Scored)
	}
}
