package service

import (
	"fmt"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// TestValuationReportRealData 用本机数据湖跑一次真实的全市场评估。
//
// ⚠ 数据湖不可用时 skip（与本仓库其它"真实数据依赖型"用例一致，
// 避免在 CI / 别人机器上误报失败）。
func TestValuationReportRealData(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	started := time.Now()
	report, err := svc.GetValuationReport(150, true)
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if !report.Available {
		t.Fatalf("评估不可用: %s", report.Message)
	}
	t.Logf("评估耗时 %s（服务层统计 %d ms）", time.Since(started), report.ElapsedMS)
	for _, phase := range report.PhaseTimings {
		t.Logf("  阶段 %-16s %6d ms（累计 %6d ms）", phase.Name, phase.MS, phase.Total)
	}
	t.Logf("溯源: %s", report.Snapshot.String())
	t.Logf("评估日 %s，数据截止：%s", formatDate(report.Snapshot.EvalDate), report.Snapshot.DataCutoff)

	if len(report.Rankings) != 5 {
		t.Fatalf("应有 5 档期限，实际 %d", len(report.Rankings))
	}
	for _, ranking := range report.Rankings {
		dist := ranking.Distribution
		t.Logf("--- %s：评分 %d 只 / 排除 %d 只 | 均分 %.1f 中位 %.1f P10 %.1f P90 %.1f",
			ranking.Horizon.Label, ranking.Scored, ranking.ExcludedCount,
			dist.Mean, dist.Median, dist.P10, dist.P90)
		// 五档的均分不允许完全相同（否则就是复制了同一个评分）
		for index := 0; index < 5 && index < len(ranking.Rows); index++ {
			row := ranking.Rows[index]
			t.Logf("    #%d %s %s | 分 %.1f (%s, 约束前 %.1f, 置信 %.0f) | PE %.1f 分位 %.0f%% | 成长 %.0f 质量 %.0f | 预期 %+.1f%% 悲观 %.1f%% | 安全边际 %+.1f%%",
				row.Rank, row.Code, row.Name, row.Score, valuation.RatingLabel(row.Rating),
				row.RawScore, row.Confidence.Score, row.CurrentPE, row.SelfPercentile,
				row.GrowthScore, row.QualityScore,
				row.ExpectedReturn*100, row.PessimisticLoss*100, row.SafetyMargin*100)
		}
		if len(ranking.Rows) > 0 {
			t.Logf("    关键优势: %v", ranking.Rows[0].KeyStrengths)
			t.Logf("    主要风险: %v", ranking.Rows[0].MainRisks)
		}
	}

	// 五档必须真的不同
	means := make([]float64, 0, 5)
	for _, ranking := range report.Rankings {
		means = append(means, ranking.Distribution.Mean)
	}
	allSame := true
	for i := 1; i < len(means); i++ {
		if means[i] != means[0] {
			allSame = false
		}
	}
	if allSame {
		t.Error("五档期限的均分完全相同 —— 说明没有独立计算")
	}

	// 行业与指数
	t.Logf("行业聚合：%d 个", len(report.Industries))
	for index, industry := range report.Industries {
		if index >= 5 {
			break
		}
		t.Logf("    %s(%s) 成分 %d | 加权 PE %.1f 中位 PE %.1f 分位 %.0f | 亏损占比 %.0f%% 前3权重 %.0f%% | 阶段 %s | 分 %.1f",
			industry.Name, industry.Code, industry.Members, industry.WeightedPE, industry.MedianPE,
			industry.PEPercentile, industry.LossRatio*100, industry.Top3Weight*100,
			industry.Stage, industry.Score)
		if index < 2 {
			t.Logf("        说明: %s", industry.Note)
		}
	}
	t.Logf("指数评估：%d 个", len(report.Indices))
	for index, item := range report.Indices {
		if index >= 8 {
			break
		}
		t.Logf("    %s(%s) 可用=%v 成分 %d | 加权PE %.1f 中位PE %.1f | 前10权重 %.0f%% 最大行业 %.0f%% | 便宜权重 %.0f%% 贵权重 %.0f%% 安全边际权重 %.0f%%",
			item.Name, item.Symbol, item.Available, len(item.IndustryWeights),
			item.WeightedPE, item.MedianPE, item.Top10Weight*100, item.TopIndustryWeight*100,
			item.CheapWeight*100, item.ExpensiveWeight*100, item.MarginWeight*100)
		if len(item.Scores) > 0 {
			t.Logf("        12 个月评分 %.1f (%s)", item.Scores[3].FinalScore, valuation.RatingLabel(item.Scores[3].Rating))
		}
	}

	// 分布直方图之和必须等于标的数
	for _, ranking := range report.Rankings {
		sum := 0
		for _, count := range ranking.Distribution.Histogram {
			sum += count
		}
		if sum != ranking.Scored {
			t.Errorf("%s 的直方图计数之和 %d != 参与排名的标的数 %d",
				ranking.Horizon.Key, sum, ranking.Scored)
		}
	}
}

// TestStockValuationRealData 单只标的的详情评估。
func TestStockValuationRealData(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	for _, code := range []string{"600519", "000001", "300750"} {
		result, err := svc.GetStockValuation(code)
		if err != nil {
			t.Fatalf("%s 评估失败: %v", code, err)
		}
		if !result.Available {
			t.Logf("%s 不可评估: %s", code, result.Message)
			continue
		}
		stock := result.Stock
		t.Logf("=== %s %s ===", stock.Code, stock.Name)
		t.Logf("  价格 %.2f 市值 %.1f 亿 | 报告期 %s", stock.Price, stock.MarketCap/1e8, stock.LatestPeriod)
		t.Logf("  自身: PE %.1f PB %.1f PS %.1f | PE 分位 3年 %.0f%% 5年 %.0f%% | 样本 %d 天",
			stock.Self.CurrentPE, stock.Self.CurrentPB, stock.Self.CurrentPS,
			stock.Self.PEPercentile3Y, stock.Self.PEPercentile5Y, stock.Self.SampleDays5Y)
		if stock.Self.StructuralShift {
			t.Logf("  结构性变化: %v", stock.Self.StructuralReasons)
		}
		t.Logf("  行业: %s 成分 %d | 加权 PE %.1f 中位 PE %.1f | 亏损占比 %.0f%%",
			stock.Industry.Name, stock.Industry.Members, stock.Industry.WeightedPE,
			stock.Industry.MedianPE, stock.Industry.LossRatio*100)
		t.Logf("  相对: 对加权 %+.1f%% 对中位 %+.1f%% | 行业内 PE 分位 %.0f%% | 四象限 %s",
			stock.Relative.PremiumVsWeighted*100, stock.Relative.PremiumVsMedian*100,
			stock.Relative.PEPercentileInIndustry, stock.Relative.QuadrantLabel)
		t.Logf("  成长: 分 %.0f | 营收同比 %+.1f%% 3年CAGR %.1f%% 持续性 %.0f%% | ROE %.1f%% 增量ROIC %.1f%%",
			stock.Growth.Score, stock.Growth.RevenueYoY*100, stock.Growth.RevenueCAGR3*100,
			stock.Growth.Consistency*100, stock.Growth.ROE*100, stock.Growth.IncrementalROIC*100)
		if len(stock.Growth.Flags) > 0 {
			for _, flag := range stock.Growth.Flags {
				t.Logf("      形态[%s] %s：%s", flag.Code, flag.Title, flag.Detail)
			}
		}
		t.Logf("  质量: 分 %.0f | 扣非占比 %.2f 现金含量 %.2f 负债率 %.1f%% 毛利率 %.1f%%",
			stock.Quality.Score, stock.Quality.DeductedRatio, stock.Quality.CashContent,
			stock.Quality.DebtRatio*100, stock.Quality.GrossMargin*100)
		for _, issue := range stock.Quality.Issues {
			t.Logf("      问题[%s] %s：%s", issue.Code, issue.Title, issue.Detail)
		}
		for _, horizonScore := range stock.Scores {
			fair := stock.FairByHorizon[horizonScore.Horizon.Key]
			t.Logf("  [%s] 综合 %.1f (%s) 原始 %.1f 乘子 %.2f 封顶=%v",
				horizonScore.Horizon.Label, horizonScore.FinalScore,
				valuation.RatingLabel(horizonScore.Rating), horizonScore.RawScore,
				horizonScore.DataMultiplier, horizonScore.RatingCapped)
			t.Logf("        合理价值 悲观 %.2f / 基准 %.2f / 乐观 %.2f | 安全边际 %+.1f%% 潜在收益 %+.1f%% | 置信 %.0f (%s)",
				fair.Pessimistic, fair.Base, fair.Optimistic,
				fair.SafetyMargin*100, fair.Upside*100, fair.Confidence.Score, fair.Confidence.Level)
			if len(fair.Methods) > 0 {
				for _, method := range fair.Methods {
					status := "适用"
					if !method.Applicable {
						status = "跳过"
					}
					detail := method.Basis
					if !method.Applicable {
						detail = method.Reason
					}
					t.Logf("        %s[%s] %.2f 元 目标倍数 %.2f 当前 %.2f — %s",
						method.Label, status, method.FairValue, method.TargetMultiple,
						method.CurrentMultiple, detail)
				}
			}
			if fair.Conflict {
				t.Logf("        方法冲突: %s", fair.ConflictNote)
			}
			t.Logf("        隐含成长 %.1f%% vs 模型 %.1f%% → %s",
				fair.ImpliedGrowth*100, fair.ModelGrowth*100, fair.GrowthExpectation)
			t.Logf("        情景: 悲观 %+.1f%% (p=%.2f) 基准 %+.1f%% (p=%.2f) 乐观 %+.1f%% (p=%.2f) | 预期 %+.1f%% 悲观损失 %.1f%% 风险收益比 %.2f 乐观依赖 %.2f",
				horizonScore.Outlook.Scenarios[0].Return*100, horizonScore.Outlook.Scenarios[0].Probability,
				horizonScore.Outlook.Scenarios[1].Return*100, horizonScore.Outlook.Scenarios[1].Probability,
				horizonScore.Outlook.Scenarios[2].Return*100, horizonScore.Outlook.Scenarios[2].Probability,
				horizonScore.Outlook.ExpectedReturn*100, horizonScore.Outlook.PessimisticLoss*100,
				horizonScore.Outlook.ReturnRiskRatio, horizonScore.Outlook.OptimismDependence)
			for _, flag := range horizonScore.RiskFlags {
				t.Logf("        风险[%s/%s] %s：%s", flag.Code, flag.Severity, flag.Title, flag.Reason)
			}
		}
		if len(stock.Scores) > 0 {
			t.Logf("  维度分（12 个月）:")
			for _, dimension := range valuation.ScoreDimensions {
				t.Logf("      %-14s %.1f (权重 %.1f → 贡献 %.2f)",
					valuation.DimensionLabel(dimension),
					stock.Scores[3].Dimensions[dimension],
					stock.Scores[3].Weights[dimension],
					stock.Scores[3].Contributions[dimension])
			}
		}
	}
}

// TestValuationUnavailableStates 钉住"数据湖不可用 / 缺数据集"时的空状态。
func TestValuationUnavailableStates(t *testing.T) {
	svc, _ := newIsolatedService(t)
	// 隔离配置下没有数据湖（除非环境变量指定）：不设断言条件，只验证不 panic
	// 且返回可解释的结果。
	report, err := svc.GetValuationReport(20, true)
	if err != nil {
		t.Fatalf("不应返回错误（不可用要用字段表达）: %v", err)
	}
	if report == nil {
		t.Fatal("应返回非空结果")
	}
	if !report.Available {
		t.Logf("按预期返回不可用状态：%s", report.Message)
		if report.Message == "" {
			t.Error("不可用时必须说明原因")
		}
		if len(report.Notes) == 0 {
			t.Error("不可用时仍应给出数据来源说明")
		}
	}
	stock, err := svc.GetStockValuation("600519")
	if err != nil {
		t.Fatalf("单只评估不应返回错误: %v", err)
	}
	if stock == nil {
		t.Fatal("应返回非空结果")
	}
	_ = fmt.Sprint(stock.Available)
}
