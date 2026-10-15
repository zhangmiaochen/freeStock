package valuation

import (
	"math"
	"testing"
)

// 本文件钉住两件事，它们都是"踩过才知道"的口径：
//
//  1. 数据湖的报告期数值是**年内累计**，且 **Q4 是全年值**（不是第四季度单季）。
//     实测 600519：2025Q4 = 823.20 亿 = 2025 全年；2025Q4 单季 = 823.20 − 646.27 = 176.93。
//  2. TTM = 上年全年 + 本年至今 − 去年同期。用"把 Q4 当单季再累加"会得到 2~3 倍偏离。

// report 造一个报告期（只带指定科目）。
func report(period string, announce int64, values map[string]float64) Report {
	year, quarter, _ := ParsePeriod(period)
	return Report{Period: period, Year: year, Quarter: quarter, AnnounceDate: announce, Values: values}
}

// maotaiReports 是 600519 的真实归母净利润序列（单位：亿元，实测自数据湖）。
// 报告期值为年内累计，Q4 为全年值。
func maotaiReports() []Report {
	revenue := map[string]float64{
		"2022Q1": 331.87, "2022Q2": 594.44, "2022Q3": 897.86, "2022Q4": 1275.54,
		"2023Q1": 393.79, "2023Q2": 709.84, "2023Q3": 1053.03, "2023Q4": 1505.60,
		"2024Q1": 464.85, "2024Q2": 834.51, "2024Q3": 1231.23, "2024Q4": 1741.44,
		"2025Q1": 514.43, "2025Q2": 910.94, "2025Q3": 1309.04, "2025Q4": 1720.54,
		"2026Q1": 547.03, "2026Q2": 922.78,
	}
	profit := map[string]float64{
		"2022Q1": 172.45, "2022Q2": 297.94, "2022Q3": 444.00, "2022Q4": 627.17,
		"2023Q1": 207.95, "2023Q2": 359.80, "2023Q3": 528.76, "2023Q4": 747.34,
		"2024Q1": 240.65, "2024Q2": 416.96, "2024Q3": 608.28, "2024Q4": 862.28,
		"2025Q1": 268.47, "2025Q2": 454.03, "2025Q3": 646.27, "2025Q4": 823.20,
		"2026Q1": 272.43, "2026Q2": 445.17,
	}
	// 公告日取实测值（年报在次年 3~4 月，一季报在 4 月下旬，半年报 8 月，三季报 10 月）。
	announce := map[string]int64{
		"2022Q1": 20220426, "2022Q2": 20220803, "2022Q3": 20221017, "2022Q4": 20230331,
		"2023Q1": 20230426, "2023Q2": 20230803, "2023Q3": 20231021, "2023Q4": 20240403,
		"2024Q1": 20240427, "2024Q2": 20240809, "2024Q3": 20241026, "2024Q4": 20250403,
		"2025Q1": 20250430, "2025Q2": 20250813, "2025Q3": 20251030, "2025Q4": 20260417,
		"2026Q1": 20260425, "2026Q2": 20260815,
	}

	periods := []string{
		"2022Q1", "2022Q2", "2022Q3", "2022Q4",
		"2023Q1", "2023Q2", "2023Q3", "2023Q4",
		"2024Q1", "2024Q2", "2024Q3", "2024Q4",
		"2025Q1", "2025Q2", "2025Q3", "2025Q4",
		"2026Q1", "2026Q2",
	}
	out := make([]Report, 0, len(periods))
	for _, period := range periods {
		out = append(out, report(period, announce[period], map[string]float64{
			ItemNetProfit: profit[period],
			ItemRevenue:   revenue[period],
		}))
	}
	return out
}

// TestTTMUsesQ4AsFullYearNotSingleQuarter 钉住"Q4 是全年值"这个口径。
//
// 这就是本项目在数据上踩到的坑：如果把 2025Q4 的 823.20 当成第四季度单季，
// TTM(2026Q2) 会算成 176.93+185.59+268.47+185.59 ≈ 816（看着很接近），
// 但 TTM(2026Q1) 会算成 176.93+185.59+268.47 = 631 亿（真值 814 亿，差 22%）。
// 更糟的是在"季报刚出、年报还没出"的窗口里，第四季度根本不存在，
// 却会被算成一个巨大的单季 —— 那才是 2~3 倍偏离的来源。
func TestTTMUsesQ4AsFullYearNotSingleQuarter(t *testing.T) {
	reports := maotaiReports()

	// 2026Q2 已公布 → TTM = 2025 全年 + 2026 上半年 − 2025 上半年
	asOf := BuildAsOf(reports, 20260815)
	if asOf.LatestPeriod != "2026Q2" {
		t.Fatalf("最新报告期应为 2026Q2，实际 %s", asOf.LatestPeriod)
	}
	// 823.20 + 445.17 − 454.03 = 814.34
	want := 823.20 + 445.17 - 454.03
	if math.Abs(asOf.TTM.NetProfit-want) > 0.01 {
		t.Errorf("TTM 归母净利润 = %.2f 亿，应为 %.2f 亿", asOf.TTM.NetProfit, want)
	}
	// 营收同口径：1720.54 + 922.78 − 910.94 = 1732.38
	wantRevenue := 1720.54 + 922.78 - 910.94
	if math.Abs(asOf.TTM.Revenue-wantRevenue) > 0.01 {
		t.Errorf("TTM 营收 = %.2f 亿，应为 %.2f 亿", asOf.TTM.Revenue, wantRevenue)
	}
}

// TestTTMAtQ1BeforeAnnualReport 覆盖"一季报已出、上年年报也出了"的正常窗口。
func TestTTMAtQ1BeforeAnnualReport(t *testing.T) {
	reports := maotaiReports()
	// 2026-04-25 一季报公告，同日 2026Q1 生效；2025 年报（20260417）已在之前生效
	asOf := BuildAsOf(reports, 20260425)
	if asOf.LatestPeriod != "2026Q1" {
		t.Fatalf("最新报告期应为 2026Q1，实际 %s", asOf.LatestPeriod)
	}
	// 823.20 + 272.43 − 268.47 = 827.16
	want := 823.20 + 272.43 - 268.47
	if math.Abs(asOf.TTM.NetProfit-want) > 0.01 {
		t.Errorf("TTM = %.2f，应为 %.2f", asOf.TTM.NetProfit, want)
	}
}

// TestPointInTimeGatingIgnoresFutureReport 钉住时点门控：
// 在 2026Q2 公告之前评估，绝不能用到 2026Q2 的数据。
func TestPointInTimeGatingIgnoresFutureReport(t *testing.T) {
	reports := maotaiReports()

	before := BuildAsOf(reports, 20260814)
	if before.LatestPeriod != "2026Q1" {
		t.Fatalf("2026-08-14 评估时最新报告期应为 2026Q1（Q2 次日才公告），实际 %s", before.LatestPeriod)
	}
	if before.TTM.NetProfit != before.Items[ItemNetProfit] {
		t.Error("TTM 与 Items 里的归母净利润应一致")
	}

	after := BuildAsOf(reports, 20260815)
	if after.LatestPeriod != "2026Q2" {
		t.Fatalf("2026-08-15 评估时最新报告期应为 2026Q2，实际 %s", after.LatestPeriod)
	}
	if after.TTM.NetProfit == before.TTM.NetProfit {
		t.Error("跨过公告日后 TTM 应当变化 —— 没变说明时点门控没生效")
	}
}

// TestBuildAsOfDeduplicatesRevisions 钉住"同一报告期多次修订"的处理。
//
// 数据湖里同一 (symbol, report_period) 可能有多行。**绝不能相加** ——
// 相加会把利润算成两倍，进而把 PE 算成一半（看起来"极其便宜"）。
func TestBuildAsOfDeduplicatesRevisions(t *testing.T) {
	reports := []Report{
		report("2025Q4", 20260401, map[string]float64{ItemNetProfit: 800}),
		report("2025Q4", 20260417, map[string]float64{ItemNetProfit: 823.20}), // 修订版（更晚）
		report("2026Q1", 20260425, map[string]float64{ItemNetProfit: 272.43}),
		report("2026Q1", 20260420, map[string]float64{ItemNetProfit: 260.00}), // 更早的版本，应被丢弃
		report("2025Q1", 20250430, map[string]float64{ItemNetProfit: 268.47}),
		report("2025Q2", 20250813, map[string]float64{ItemNetProfit: 454.03}),
		report("2025Q3", 20251030, map[string]float64{ItemNetProfit: 646.27}),
		report("2026Q2", 20260815, map[string]float64{ItemNetProfit: 445.17}),
	}
	asOf := BuildAsOf(reports, 20260815)

	// 应为 823.20 + 445.17 − 454.03 = 814.34，而不是把修订版本相加得到的更大值
	want := 823.20 + 445.17 - 454.03
	if math.Abs(asOf.TTM.NetProfit-want) > 0.01 {
		t.Errorf("TTM = %.2f，应为 %.2f（说明重复修订被相加了）", asOf.TTM.NetProfit, want)
	}
	if asOf.Quarters != 6 {
		t.Errorf("去重后报告期数应为 6，实际 %d", asOf.Quarters)
	}
}

// TestBuildAsOfRejectsRowsWithoutAnnounceDate 钉住"没有公告日的行一律丢弃"。
//
// 没有公告日就无法做时点门控 —— 宁可少算一个季度，也不能把不确定是否
// 已经公开的数据当成已知。
func TestBuildAsOfRejectsRowsWithoutAnnounceDate(t *testing.T) {
	reports := []Report{
		report("2025Q2", 0, map[string]float64{ItemNetProfit: 454.03}),
		report("2026Q2", 20260815, map[string]float64{ItemNetProfit: 445.17}),
	}
	asOf := BuildAsOf(reports, 20260815)
	if asOf.Quarters != 1 {
		t.Errorf("无公告日的行应被丢弃，剩余报告期数应为 1，实际 %d", asOf.Quarters)
	}
}

// TestSingleQuartersSplitsQ4Correctly 钉住"Q4 单季 = 全年 − Q3 累计"。
func TestSingleQuartersSplitsQ4Correctly(t *testing.T) {
	reports := maotaiReports()
	asOf := BuildAsOf(reports, 20260815)
	singles := SingleQuarters(asOf.Reports, []string{ItemNetProfit})

	byPeriod := map[string]float64{}
	for _, single := range singles {
		byPeriod[single.Period] = single.Items[ItemNetProfit]
	}

	// Q1 直接是单季
	if got := byPeriod["2025Q1"]; math.Abs(got-268.47) > 0.01 {
		t.Errorf("2025Q1 单季 = %.2f，应为 268.47", got)
	}
	// Q2 单季 = 454.03 − 268.47
	if got := byPeriod["2025Q2"]; math.Abs(got-(454.03-268.47)) > 0.01 {
		t.Errorf("2025Q2 单季 = %.2f，应为 %.2f", got, 454.03-268.47)
	}
	// Q4 单季 = 全年 823.20 − Q3 累计 646.27 = 176.93
	if got := byPeriod["2025Q4"]; math.Abs(got-(823.20-646.27)) > 0.01 {
		t.Errorf("2025Q4 单季 = %.2f，应为 %.2f（说明把全年值当成了单季）", got, 823.20-646.27)
	}
	// 四个单季之和必须等于全年
	sum := byPeriod["2025Q1"] + byPeriod["2025Q2"] + byPeriod["2025Q3"] + byPeriod["2025Q4"]
	if math.Abs(sum-823.20) > 0.01 {
		t.Errorf("2025 四个单季之和 = %.2f，应等于全年 823.20", sum)
	}
}

// TestQuarterYoYUsesSingleQuarters 钉住同比用**单季**而不是累计。
//
// 2026Q2 单季 = 445.17 − 272.43 = 172.74；2025Q2 单季 = 454.03 − 268.47 = 185.56。
// 单季同比 = 172.74/185.56 − 1 = −6.9%（下滑）。
// 而若误用累计同比：445.17/454.03 − 1 = −1.95% —— 结论方向一致但幅度差三倍。
func TestQuarterYoYUsesSingleQuarters(t *testing.T) {
	reports := maotaiReports()
	asOf := BuildAsOf(reports, 20260815)
	series := QuarterYoYSeries(SingleQuarters(asOf.Reports, []string{ItemNetProfit}), ItemNetProfit, 4)
	if len(series) == 0 {
		t.Fatal("应能算出至少一个单季同比")
	}
	latest := series[len(series)-1]
	wantSingle := (445.17-272.43)/(454.03-268.47) - 1
	if math.Abs(latest-wantSingle) > 0.001 {
		t.Errorf("最新单季同比 = %.4f，应为 %.4f", latest, wantSingle)
	}
	// 显式断言"不是累计同比"，否则这个测试对错误的实现也会通过
	cumulative := 445.17/454.03 - 1
	if math.Abs(latest-cumulative) < 0.005 {
		t.Errorf("算出来的同比（%.4f）太接近累计同比（%.4f），口径可能写错了", latest, cumulative)
	}
}

// TestTTMFallsBackWhenAnnualMissing 覆盖"上年年报缺失"的回落路径。
func TestTTMFallsBackWhenAnnualMissing(t *testing.T) {
	reports := []Report{
		report("2025Q1", 20250430, map[string]float64{ItemNetProfit: 100}),
		report("2025Q2", 20250813, map[string]float64{ItemNetProfit: 210}),
		report("2025Q3", 20251030, map[string]float64{ItemNetProfit: 330}),
		report("2026Q1", 20260425, map[string]float64{ItemNetProfit: 110}),
		report("2026Q2", 20260815, map[string]float64{ItemNetProfit: 230}),
	}
	asOf := BuildAsOf(reports, 20260815)
	// 回落路径：上年 Q1~Q3 之和(100+210+330=640) + 本年至今(230) = 870。
	//
	// ⚠ 这里**不能**再减"去年同期"：2025Q1~Q3 之和里本来就没有第四季度，
	// 减掉同期会把第四季度算成负数（640 + 230 − 210 = 660，那是错的）。
	// 代价是缺掉一个 Q4，所以季度数要如实记成 5，让调用方降置信度。
	//
	// 注：本机的年报覆盖率其实很高（2001~2025 每年都有 1189~5582 只），
	// 所以这条回落路径只在次新股、或数据湖刚开始回填时才会走到。
	if math.Abs(asOf.TTM.NetProfit-870) > 0.01 {
		t.Errorf("缺年报时的 TTM = %.2f，应为 870", asOf.TTM.NetProfit)
	}
	if _, quarters, ok := ttmQuarters(asOf.Reports, ItemNetProfit); !ok || quarters != 5 {
		t.Errorf("参与 TTM 的报告期数应为 5，实际 %d (ok=%v)", quarters, ok)
	}
}

// TestTTMReportsMissingItems 钉住"缺科目要如实列出来"。
func TestTTMReportsMissingItems(t *testing.T) {
	reports := []Report{
		report("2025Q4", 20260417, map[string]float64{ItemNetProfit: 823.20}),
		report("2026Q2", 20260815, map[string]float64{ItemNetProfit: 445.17, ItemRevenue: 922.78}),
		report("2025Q2", 20250813, map[string]float64{ItemNetProfit: 454.03, ItemRevenue: 910.94}),
	}
	asOf := BuildAsOf(reports, 20260815)
	found := false
	for _, item := range asOf.Missing {
		if item == ItemTotalAssets {
			found = true
		}
	}
	if !found {
		t.Error("缺失的科目（总资产）应出现在 Missing 里，供界面降置信度")
	}
}

// TestCAGRNegativeBaseRejected 钉住"基期为负不给 CAGR"。
//
// 从亏损到盈利算"增长率"会让符号反转（−1 亿 → +10 亿会算出 −1100%），
// 所以这种情况必须返回 false，由成长模块改用"扭亏"这一独立判据。
func TestCAGRNegativeBaseRejected(t *testing.T) {
	if _, ok := CAGR(-1, 10, 3); ok {
		t.Error("基期为负时不应给出 CAGR")
	}
	if _, ok := CAGR(0, 10, 3); ok {
		t.Error("基期为零时不应给出 CAGR")
	}
	value, ok := CAGR(100, 133.1, 3)
	if !ok || math.Abs(value-0.10) > 0.001 {
		t.Errorf("100→133.1 三年 CAGR 应为 10%%，实际 %.4f (ok=%v)", value, ok)
	}
}

// TestAnnualValuesUsesQ4Rows 钉住全年值取自 Q4 行。
func TestAnnualValuesUsesQ4Rows(t *testing.T) {
	reports := maotaiReports()
	values := AnnualValues(reports, ItemNetProfit, 3)
	if len(values) != 3 {
		t.Fatalf("应取到 3 个年度值，实际 %d", len(values))
	}
	want := []float64{747.34, 862.28, 823.20}
	for i := range want {
		if math.Abs(values[i]-want[i]) > 0.01 {
			t.Errorf("第 %d 个年度值 = %.2f，应为 %.2f", i, values[i], want[i])
		}
	}
}

// TestParsePeriod 边界。
func TestParsePeriod(t *testing.T) {
	cases := []struct {
		in      string
		year    int
		quarter int
		ok      bool
	}{
		{"2025Q4", 2025, 4, true},
		{"2001Q1", 2001, 1, true},
		{"2025q2", 2025, 2, true},
		{"2025Q5", 0, 0, false},
		{"garbage", 0, 0, false},
		{"", 0, 0, false},
		{"2025", 0, 0, false},
	}
	for _, item := range cases {
		year, quarter, ok := ParsePeriod(item.in)
		if ok != item.ok || year != item.year || quarter != item.quarter {
			t.Errorf("ParsePeriod(%q) = (%d,%d,%v)，应为 (%d,%d,%v)",
				item.in, year, quarter, ok, item.year, item.quarter, item.ok)
		}
	}
}

// TestTTMIndexedMatchesQuadratic 钉住"索引版 TTM"与"线性扫描版 TTM"逐值一致。
//
// 为什么必须钉：这两份实现是同一条业务公式（TTM = 上年全年 + 本年至今 − 去年同期）
// 的两个版本 —— 索引版是为了消掉全市场评估里的近亿次比较才加的。
// 一旦有人只改其中一份，就会出现"引擎单测走线性版、真实评估走索引版"，
// 结果是**单测全绿但线上算错**，属于最难查的一类问题。
//
// 用例覆盖三条分支：Q4（直接用全年）、正常相减、上年年报缺失的回退。
func TestTTMIndexedMatchesQuadratic(t *testing.T) {
	build := func(periods ...string) ([]Report, map[int64]int) {
		reports := make([]Report, 0, len(periods))
		announce := int64(20200101)
		for _, period := range periods {
			year, quarter, ok := ParsePeriod(period)
			if !ok {
				t.Fatalf("测试数据里的报告期格式不对: %q", period)
			}
			reports = append(reports, Report{
				Period: period, Year: year, Quarter: quarter, AnnounceDate: announce,
				Values: map[string]float64{
					ItemRevenue:   float64(year-2000)*100 + float64(quarter)*10,
					ItemNetProfit: float64(year-2000)*10 + float64(quarter),
				},
			})
			announce += 10000
		}
		index := make(map[int64]int, len(reports))
		for i := range reports {
			index[yearQuarterKey(reports[i].Year, reports[i].Quarter)] = i
		}
		return reports, index
	}

	cases := []struct {
		name    string
		periods []string
	}{
		{"单季 Q1", []string{"2026Q1"}},
		{"Q2 正常相减（上年年报与同期都在）", []string{"2025Q1", "2025Q2", "2025Q3", "2025Q4", "2026Q1", "2026Q2"}},
		{"Q3 正常相减", []string{"2025Q4", "2026Q1", "2026Q2", "2026Q3"}},
		{"Q4 直接用全年", []string{"2025Q4", "2026Q4"}},
		{"上年年报缺失 → 回退前三季和", []string{"2025Q1", "2025Q2", "2025Q3", "2026Q1", "2026Q2"}},
		{"只有本年 Q1，无任何上年数据", []string{"2026Q1"}},
	}
	for _, item := range []string{ItemRevenue, ItemNetProfit} {
		for _, testCase := range cases {
			reports, index := build(testCase.periods...)

			wantValue, wantQuarters, wantOK := ttmQuarters(reports, item)
			gotValue, gotQuarters, gotOK := ttmQuartersIndexed(reports, index, item)

			if wantOK != gotOK {
				t.Errorf("%s / %s：可用性不一致 want=%v got=%v", testCase.name, item, wantOK, gotOK)
				continue
			}
			if !wantOK {
				continue
			}
			if wantValue != gotValue {
				t.Errorf("%s / %s：TTM 值不一致 want=%.4f got=%.4f", testCase.name, item, wantValue, gotValue)
			}
			if wantQuarters != gotQuarters {
				t.Errorf("%s / %s：参与季度数不一致 want=%d got=%d", testCase.name, item, wantQuarters, gotQuarters)
			}
		}
	}
}
