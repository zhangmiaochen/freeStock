package service

import (
	"strings"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// TestProfileStockDetail 量"点一只股票"这条路径的时间去哪了。
//
// 用户反馈：排名表点进个股详情要等很久。这条路径与全市场评估是**两条独立入口**
// （`GetStockValuation` 不走报告缓存），所以 §4.5 那些优化未必覆盖到它。
func TestProfileStockDetail(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)
	params := valuation.DefaultParams()

	// 先让数据集缓存就位（模拟"用户已经打开过性价比页"）
	if _, err := svc.loadValuationDataset(lake, nil); err != nil {
		t.Fatalf("预加载失败: %v", err)
	}

	for _, code := range []string{"600519", "000001"} {
		started := time.Now()
		data, err := svc.loadValuationDataset(lake, []string{code})
		if err != nil {
			t.Fatalf("loadValuationDataset(%s) 失败: %v", code, err)
		}
		loadCost := time.Since(started)

		started = time.Now()
		eval, ok := svc.evaluateOneStock(data, code, params, true)
		evalCost := time.Since(started)
		if !ok {
			t.Logf("%s 评估不可用，跳过", code)
			continue
		}

		started = time.Now()
		industry, members := svc.aggregateIndustryOf(data, eval, params)
		industryCost := time.Since(started)

		started = time.Now()
		peers, peerNote := valuation.SelectComparables(eval.company, members, params)
		relative := valuation.EvaluateRelativeValuation(eval.company, industry, members, peers, params)
		relative.ComparableNote = peerNote
		eval.fair = svc.applyIndustry(eval, industry, params)
		sharePoint, _ := eval.shares.Latest()
		_, scoreErr := valuation.EvaluateScore(valuation.ScoreInput{
			Code: eval.code, Name: eval.name, Industry: industry, Self: eval.self,
			Relative: relative, Growth: eval.growth, Quality: eval.quality, AsOf: eval.asOf,
			Company: eval.company, Price: eval.series.LastClose(), Shares: sharePoint.TotalShares,
			Volatility: eval.volatility, PaidLast12M: data.dividendPaid[code],
			PriceLength: eval.series.Len(), PriceAdjusted: eval.series.Adjusted,
			Confidence: eval.confidence, FairByHorizon: eval.fair, Params: params,
		})
		scoreCost := time.Since(started)
		if scoreErr != nil {
			t.Fatalf("评分失败: %v", scoreErr)
		}

		total := loadCost + evalCost + industryCost + scoreCost
		t.Logf("=== %s（行业 %s，成分 %d）===", code, eval.company.IndustryName, len(members))
		t.Logf("   ① 加载数据集        %8s  ← data 大小：行情 %d 只 / 财报 %d 只",
			loadCost.Round(time.Millisecond), len(data.bars), len(data.reports))
		t.Logf("   ② 评估这一只        %8s", evalCost.Round(time.Millisecond))
		t.Logf("   ③ aggregateIndustryOf %6s  ← 遍历同行业 %d 只，每只建完整快照",
			industryCost.Round(time.Millisecond), len(members))
		t.Logf("   ④ 评分与行业重算    %8s", scoreCost.Round(time.Millisecond))
		t.Logf("   合计                %8s", total.Round(time.Millisecond))
	}
}

// TestStockDetailIsFast 钉住"点一只股票不该重读全市场数据"。
//
// ## 这条用例的来历
//
// 用户反馈"点股票时个股详情加载也很慢"。量出来是 **19.2 秒**，而拆开看：
//
//	① 加载数据集        19.234s   ← 99.5% 的时间在这里
//	② 评估这一只            71ms
//	③ 行业聚合（20 只）     26ms
//	④ 评分与行业重算         1ms
//
// 根因是 `loadValuationDataset` 里一句"focus 非空 → 不走缓存"，理由是
// "详情页是一次性查询，缓存整份全市场数据不划算"。但 `focus` **只影响统计口径**，
// 数据本身永远是全市场（行业基准必须用全市场成分算）—— 于是每点一只股票
// 就把 703 万行行情 + 652 万行财报重读一遍。
//
// 修法是让 focus 不参与缓存判定。这条断言就是它的哨兵：
// **数据湖已经在内存里时，点个股必须 ≤ 1 秒**（实测 13~62ms）。
func TestStockDetailIsFast(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	// 模拟"用户先打开了性价比页"（数据集已就位）
	started := time.Now()
	if _, err := svc.loadValuationDataset(lake, nil); err != nil {
		t.Fatalf("预加载失败: %v", err)
	}
	t.Logf("预热数据集 %s", time.Since(started).Round(time.Millisecond))

	for _, code := range []string{"600519", "000001", "300750"} {
		started = time.Now()
		report, err := svc.GetStockValuation(code)
		if err != nil {
			t.Fatalf("GetStockValuation(%s) 失败: %v", code, err)
		}
		cost := time.Since(started)
		t.Logf("点开 %s：%s（可用 %v，五档 %d 个）",
			code, cost.Round(time.Millisecond), report.Available, len(report.Stock.Scores))
		if cost > time.Second {
			t.Errorf("点开 %s 花了 %s —— 数据集已缓存，不该超过 1 秒（是不是又重读数据湖了？）",
				code, cost)
		}
		// 第二次必须命中按代码的缓存
		started = time.Now()
		if _, err := svc.GetStockValuation(code); err != nil {
			t.Fatalf("第二次 GetStockValuation(%s) 失败: %v", code, err)
		}
		if again := time.Since(started); again > 100*time.Millisecond {
			t.Errorf("第二次点开 %s 花了 %s —— 按代码的报告缓存没生效", code, again)
		}
	}
}

// TestCoverageNoteIsPerRequest 钉住"覆盖说明按**本次**报告，不是累计"。
//
// ## 这条用例的来历（两次踩坑）
//
// ① 原来报告里的说明读的是 `data.skipped` 的**累计值**，而 `data.skipped`
//
//	挂在**跨请求复用**的数据集上 —— 同一份数据集评估第二次时数字会翻倍。
//
// ② 修成"取增量"之后才发现：`selectValuationCodes` 早就把"行情不足 / 缺财报 /
//
//	缺股本"的标的挡在外面了，所以 `data.skipped` 在全市场评估里**恒为 0**，
//	那条说明根本不显示 —— 白写。
//
// 现在报的是**前置筛选排除的数量**（`selectValuationCodesWithStats`），
// 这才是真正决定"哪些标的没进榜"的地方。用例断言它两次一致。
func TestCoverageNoteIsPerRequest(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	first, err := svc.GetValuationReport(0, true)
	if err != nil {
		t.Fatalf("第一次失败: %v", err)
	}
	second, err := svc.GetValuationReport(0, true)
	if err != nil {
		t.Fatalf("第二次失败: %v", err)
	}

	firstNote := coverageNoteOf(first)
	if firstNote == "" {
		t.Fatal("全市场评估居然没有覆盖说明 —— 这条用例失去了意义")
	}
	if secondNote := coverageNoteOf(second); firstNote != secondNote {
		t.Errorf("两次评估的覆盖说明不一致：\n  第一次 %q\n  第二次 %q\n"+
			"（跨请求复用的累加值不能直接进报告）", firstNote, secondNote)
	}
	t.Logf("覆盖说明：%q", firstNote)
}

// TestStockDetailSkippedCountIsPerRequest 已被 TestCoverageNoteIsPerRequest 取代。
//
// 保留名字作为指引：这条路径的"跳过"统计已经移到前置筛选里，
// 见 TestCoverageNoteIsPerRequest 的说明。
func TestStockDetailSkippedCountIsPerRequest(t *testing.T) {
	t.Skip("由 TestCoverageNoteIsPerRequest 覆盖（统计口径已移到前置筛选）")
}

// coverageNoteOf 取出报告里"X 只没有进入评估"那条说明。
func coverageNoteOf(report *ValuationReport) string {
	for _, note := range report.Notes {
		if strings.Contains(note, "没有进入评估") {
			return note
		}
	}
	return ""
}

// skippedNoteOf 取出旧的"因数据不足被跳过"说明（保留给历史用例，现已不再产生）。
func skippedNoteOf(report *ValuationReport) string {
	for _, note := range report.Notes {
		if strings.Contains(note, "因数据不足被跳过") {
			return note
		}
	}
	return ""
}
