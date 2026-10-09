package service

import (
	"testing"

	"freestock/internal/score"
)

// 真实数据下四个策略必须给出**不同**的评分 —— 这是"四套公式无法统一抽象"的端到端验证。
//
// 如果某两个策略结果一模一样，说明 service 层没把 strategyID 传下去，
// 四个策略会全部退化成趋势成长（单元测试抓不到这种"接线"问题）。
func TestStrategiesProduceDifferentScoresOnRealData(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	scoring := NewScoringService(svc)
	codes := []string{"600519", "000001", "601318", "300750"}

	results := map[string]map[string]float64{}
	for _, strategy := range []string{
		score.StrategyTrendGrowth, score.StrategyOversold,
		score.StrategyExpert, score.StrategyTrendLowBuy,
	} {
		result, err := scoring.ScoreStocks(codes, 0, 0, strategy)
		if err != nil {
			t.Fatalf("%s 评分失败: %v", strategy, err)
		}
		byCode := map[string]float64{}
		for _, row := range result.Rows {
			byCode[row.Code] = row.TotalScore
			if row.TotalScore < 0 || row.TotalScore > 100 {
				t.Errorf("%s 的 %s 总分越界: %v", strategy, row.Code, row.TotalScore)
			}
		}
		if len(byCode) == 0 {
			t.Fatalf("%s 没评出任何股票", strategy)
		}
		results[strategy] = byCode
		t.Logf("%-22s %v", strategy, byCode)
	}

	// 任取一只股票，四个策略的总分不该全部相同
	for _, code := range codes {
		values := map[float64]int{}
		for _, strategy := range []string{
			score.StrategyTrendGrowth, score.StrategyOversold,
			score.StrategyExpert, score.StrategyTrendLowBuy,
		} {
			if value, ok := results[strategy][code]; ok {
				values[value]++
			}
		}
		if len(values) < 3 {
			t.Errorf("%s 在四个策略下只有 %d 种不同分数，说明公式没真正分叉: %v",
				code, len(values), values)
		}
	}
}
