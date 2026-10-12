package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// ---------------------------------------------------------------------------
// 规范化与纯函数
// ---------------------------------------------------------------------------

// 市场温度的周期里 **1 是合法值**（当日）：它本身就是温度最直接的输入，
// 这与板块页的"区间周期从 2 起"不同。
func TestNormalizeTemperaturePeriod(t *testing.T) {
	cases := map[int]int{0: 1, -5: 1, 1: 1, 5: 5, 60: 60, 250: 250, 300: maxBoardPeriod}
	for in, want := range cases {
		if got := normalizeTemperaturePeriod(in); got != want {
			t.Errorf("normalizeTemperaturePeriod(%d) = %d，期望 %d", in, got, want)
		}
	}
	if got := periodLabelOf(1); got != "当日" {
		t.Errorf("1 的标签应为「当日」，实际 %q", got)
	}
	if got := periodLabelOf(30); got != "30日" {
		t.Errorf("30 的标签应为「30日」，实际 %q", got)
	}
}

// 分位是市场温度的核心口径，必须钉住：短样本取中性、极值取两端、并列取中点。
func TestPercentileRank(t *testing.T) {
	// 样本不足 → 50（中性），否则刚建库时会把一切判成极端
	short := make([]float64, tempMinSamples-1)
	if got := percentileRank(short, 1); got != 50 {
		t.Errorf("样本不足时应返回 50，实际 %v", got)
	}

	sample := make([]float64, 0, 200)
	for i := 0; i < 200; i++ {
		sample = append(sample, float64(i))
	}
	cases := []struct {
		value float64
		want  float64
	}{
		{sample[0], 0.25}, // 最小值 → 接近 0 分位
		{sample[199], 99.75},
		{sample[100], 50}, // 中位
		{sample[180], 90}, // 高位
	}
	for _, tc := range cases {
		if got := percentileRank(sample, tc.value); math.Abs(got-tc.want) > 0.51 {
			t.Errorf("percentileRank(%v) = %v，期望约 %v", tc.value, got, tc.want)
		}
	}

	// 并列值取中点，而不是被判成"最低"或"最高"
	tie := []float64{}
	for i := 0; i < 120; i++ {
		tie = append(tie, 1)
	}
	if got := percentileRank(tie, 1); math.Abs(got-50) > 0.1 {
		t.Errorf("全部并列时应为 50 分位，实际 %v", got)
	}
}

func TestMovingAverageSeries(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5}
	got := movingAverageSeries(closes, 3)
	want := []float64{1, 1.5, 2, 3, 4}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("第 %d 个均线 = %v，期望 %v", i, got[i], want[i])
		}
	}
}

// 新高/新低要落在 20/60/250 三档里，并且**只比之前**的点（今天自己不算）。
func TestNewExtremeDays(t *testing.T) {
	rising := make([]float64, 300)
	for i := range rising {
		rising[i] = float64(i)
	}
	if got := newExtremeDays(rising, true); got != 250 {
		t.Errorf("持续新高应报 250 日新高，实际 %d", got)
	}
	if got := newExtremeDays(rising, false); got != 0 {
		t.Errorf("持续新高时不该有新低，实际 %d", got)
	}

	flat := make([]float64, 100)
	for i := range flat {
		flat[i] = 10
	}
	if got := newExtremeDays(flat, true); got != 0 {
		t.Errorf("持平不该算新高，实际 %d", got)
	}

	// 前 70 天更低、最近 30 天一路往上：今天比之前 60 天都高，所以是 60 日新高
	mixed := make([]float64, 100)
	for i := range mixed {
		if i < 70 {
			mixed[i] = 10 - float64(i)/10
		} else {
			mixed[i] = float64(i)
		}
	}
	if got := newExtremeDays(mixed, true); got != 60 {
		t.Errorf("应报 60 日新高，实际 %d", got)
	}
}

func TestTemperatureLevelThresholds(t *testing.T) {
	cases := []struct {
		temperature float64
		want        string
	}{
		{95, "过热"}, {70, "偏热"}, {50, "中性"}, {30, "偏冷"}, {10, "冰点"},
	}
	for _, tc := range cases {
		got, text := temperatureLevel(tc.temperature, 50)
		if got != tc.want {
			t.Errorf("温度 %.0f 的等级 = %q，期望 %q", tc.temperature, got, tc.want)
		}
		if text == "" {
			t.Errorf("温度 %.0f 缺少结论文案", tc.temperature)
		}
	}
}

func TestIndexTrendLabel(t *testing.T) {
	cases := map[float64]string{90: "强势上行", 65: "偏强", 50: "震荡", 30: "偏弱", 10: "弱势下行"}
	for score, want := range cases {
		if got := indexTrendLabel(score); got != want {
			t.Errorf("动向分 %.0f 的标签 = %q，期望 %q", score, got, want)
		}
	}
}

// 合成的指数序列：验证派生指标（区间涨跌 / 量能 / 均线偏离）算对了。
func TestIndexMetricsOnSyntheticSeries(t *testing.T) {
	n := 400
	item := &indexSeries{meta: MajorIndex{Code: "TEST", Name: "测试指数"}}
	for i := 0; i < n; i++ {
		item.dates = append(item.dates, int64(20240101+i))
		item.close = append(item.close, 100+float64(i)) // 每天 +1 点
		item.amount = append(item.amount, 1e8)          // 成交额恒定
	}

	mom, vol, level, maS, _, maL := item.metrics(5)

	last := item.dates[n-1]
	// 5 日涨跌幅：(104/99-1)*100
	want := (item.close[n-1]/item.close[n-6] - 1) * 100
	if got := mom[last]; math.Abs(got-want) > 0.01 {
		t.Errorf("区间涨跌 = %v，期望 %v", got, want)
	}
	// 成交额恒定 → 量能倍数 1
	if got := vol[last]; math.Abs(got-1) > 1e-6 {
		t.Errorf("量能倍数 = %v，期望 1", got)
	}
	// 均线偏离 = close/MA250-1，且必为正（一路上涨）
	if got := level[last]; got <= 0 {
		t.Errorf("一路上涨时均线偏离应为正，实际 %v", got)
	}
	if maS[last] <= 0 || maL[last] <= 0 {
		t.Error("均线应有值")
	}
	// 前 249 天算不出 250 日均线偏离
	if _, ok := level[item.dates[100]]; ok {
		t.Error("样本不足 250 天时不该给出均线偏离")
	}
}

// ---------------------------------------------------------------------------
// 真实数据（本地数据湖 index_bars）
// ---------------------------------------------------------------------------

func TestGetMarketTemperatureFromLake(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（数据湖全量读）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}

	result, err := svc.GetMarketTemperature(1)
	if err != nil {
		t.Fatalf("GetMarketTemperature 失败: %v", err)
	}
	if !result.Available {
		t.Skipf("数据湖里没有 index_bars：%s", result.Message)
	}

	// ---- 温度与分项 ----
	if result.Temperature < 0 || result.Temperature > 100 {
		t.Errorf("温度应在 0~100，实际 %v", result.Temperature)
	}
	if result.Composite < 0 || result.Composite > 100 {
		t.Errorf("综合分应在 0~100，实际 %v", result.Composite)
	}
	if result.Level == "" || result.LevelText == "" {
		t.Error("温度缺少等级或结论文案")
	}
	if result.PeriodDays != 1 || result.PeriodLabel != "当日" {
		t.Errorf("默认周期应为当日，实际 %d/%s", result.PeriodDays, result.PeriodLabel)
	}
	if len(result.Factors) != 4 {
		t.Fatalf("应有 4 个分项，实际 %d", len(result.Factors))
	}
	weightSum := 0.0
	for _, factor := range result.Factors {
		weightSum += factor.Weight
		if factor.Score < 0 || factor.Score > 100 {
			t.Errorf("分项 %s 的得分应在 0~100，实际 %v", factor.Name, factor.Score)
		}
		if factor.ValueText == "" || factor.Detail == "" {
			t.Errorf("分项 %s 缺少数值文案或说明", factor.Name)
		}
	}
	if math.Abs(weightSum-1) > 1e-9 {
		t.Errorf("分项权重之和应为 1，实际 %v", weightSum)
	}

	// ---- 指数列表 ----
	if result.IndexCount != len(result.Indices) || result.IndexCount == 0 {
		t.Fatalf("指数数量对不上：%d vs %d", result.IndexCount, len(result.Indices))
	}
	if result.IndexCount != len(majorIndices) {
		t.Logf("提示：数据湖里有 %d 个指数，注册表里有 %d 个", result.IndexCount, len(majorIndices))
	}
	if result.Window < 20 || len(result.Dates) != result.Window {
		t.Errorf("走势窗口异常：window=%d dates=%d", result.Window, len(result.Dates))
	}
	for _, item := range result.Indices {
		if item.Close <= 0 || item.Date <= 0 {
			t.Errorf("%s 缺少最新点数: %+v", item.Name, item)
		}
		if item.LevelScore < 0 || item.LevelScore > 100 {
			t.Errorf("%s 的位置分位应在 0~100，实际 %v", item.Name, item.LevelScore)
		}
		if item.TrendLabel == "" || item.TrendNote == "" {
			t.Errorf("%s 缺少趋势说明", item.Name)
		}
		if len(item.Recent) != result.Window || len(item.Norm) != result.Window {
			t.Errorf("%s 的走势点数 %d/%d 与窗口 %d 不一致",
				item.Name, len(item.Recent), len(item.Norm), result.Window)
		}
		if len(item.Norm) > 0 && math.Abs(item.Norm[0]-100) > 0.01 {
			t.Errorf("%s 的归一化走势首点应为 100，实际 %v", item.Name, item.Norm[0])
		}
		if item.HistoryDays <= 0 || item.HistoryStart <= 0 {
			t.Errorf("%s 缺少历史区间信息", item.Name)
		}
	}

	// ---- 区间涨跌幅用独立的原始数据核对 ----
	lake := svc.lakeHandle()
	rows, err := lake.AllIndexBars()
	if err != nil {
		t.Fatalf("读取 index_bars 失败: %v", err)
	}
	closes := map[string][]float64{}
	dates := map[string][]int64{}
	for _, row := range rows {
		code := row.Symbol
		closes[code] = append(closes[code], row.Close)
		dates[code] = append(dates[code], row.Date())
	}
	// index_bars 未保证按日期升序（服务里会排），这里自己也排一遍
	for code, list := range dates {
		for i := 1; i < len(list); i++ {
			for j := i; j > 0 && dates[code][j] < dates[code][j-1]; j-- {
				dates[code][j], dates[code][j-1] = dates[code][j-1], dates[code][j]
				closes[code][j], closes[code][j-1] = closes[code][j-1], closes[code][j]
			}
		}
	}
	checked := 0
	for _, item := range result.Indices {
		list := closes[item.Code]
		if len(list) < 2 {
			continue
		}
		want := round((list[len(list)-1]/list[len(list)-2]-1)*100, 4)
		if math.Abs(item.PctChg-want) > 0.02 {
			t.Errorf("%s 当日涨跌 = %v，独立计算 = %v", item.Name, item.PctChg, want)
		}
		checked++
	}
	if checked == 0 {
		t.Error("一个指数都没能对照上")
	}

	t.Logf("温度 %.1f（%s，综合分 %.1f），样本 %s ~ %s 共 %d 个交易日，%d 个指数",
		result.Temperature, result.Level, result.Composite,
		formatDateString(result.HistoryStart), formatDateString(result.HistoryEnd),
		result.HistoryDays, result.IndexCount)
	for _, factor := range result.Factors {
		t.Logf("  %s %5.1f 分（权重 %.0f%%）现值 %s", factor.Name, factor.Score, factor.Weight*100, factor.ValueText)
	}
	for _, item := range result.Indices[:min(3, len(result.Indices))] {
		t.Logf("  %s %s %.2f 当日 %+.2f%% 位置 %.0f 分位 [%s]",
			item.Code, item.Name, item.Close, item.PctChg, item.LevelScore, item.TrendLabel)
		t.Logf("      %s", item.TrendNote)
	}
}

// 换周期只影响计算，不改变"用全部历史算分位"这件事；不同周期的读数应当都自洽。
func TestGetMarketTemperaturePeriods(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（数据湖全量读）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}

	first, err := svc.GetMarketTemperature(1)
	if err != nil || !first.Available {
		t.Skipf("数据湖 index_bars 不可用: %v / %s", err, first.Message)
	}

	// 原始收盘价（独立于服务实现地自己排一遍），用来核对区间涨跌幅
	rawCloses := rawIndexCloses(t, svc)

	for _, period := range []int{5, 60, 300} {
		result, err := svc.GetMarketTemperature(period)
		if err != nil {
			t.Fatalf("周期 %d 失败: %v", period, err)
		}
		wantPeriod := period
		if wantPeriod > maxBoardPeriod {
			wantPeriod = maxBoardPeriod
		}
		if result.PeriodDays != wantPeriod {
			t.Errorf("周期 %d 被规范化成 %d", period, result.PeriodDays)
		}
		if result.Temperature < 0 || result.Temperature > 100 {
			t.Errorf("周期 %d 的温度越界: %v", period, result.Temperature)
		}
		// 窗口 = max(周期, 20)
		wantWindow := wantPeriod
		if wantWindow < 20 {
			wantWindow = 20
		}
		if result.Window != wantWindow && result.Window != result.HistoryDays {
			t.Errorf("周期 %d 的走势窗口 = %d，期望 %d", period, result.Window, wantWindow)
		}

		// 区间涨跌幅必须等于"最新收盘 ÷ 倒数第 period+1 个收盘 − 1"（独立核对）
		if period <= maxBoardPeriod {
			for _, item := range result.Indices {
				list := rawCloses[item.Code]
				if len(list) < period+1 {
					continue
				}
				base := list[len(list)-1-period]
				if base <= 0 {
					continue
				}
				want := (list[len(list)-1]/base - 1) * 100
				if math.Abs(item.PeriodPctChg-want) > 0.02 {
					t.Errorf("%s %d日区间涨跌 = %v，独立计算 = %v（基准 %v → %v）",
						item.Name, period, item.PeriodPctChg, want, base, list[len(list)-1])
				}
			}
		}

		parts := make([]string, 0, len(result.Factors))
		for _, factor := range result.Factors {
			parts = append(parts, fmt.Sprintf("%s %.0f 分(%s)", factor.Name, factor.Score, factor.ValueText))
		}
		t.Logf("周期 %-4s 温度 %5.1f（%s）综合分 %5.1f：%s",
			result.PeriodLabel, result.Temperature, result.Level, result.Composite, strings.Join(parts, " | "))
	}

	// 温度的历史样本口径：只统计四个分项都有值的交易日。
	// 本机指数历史 728 天、前 249 天还没有 250 日均线，所以样本必然小于总天数。
	if first.HistoryDays <= 0 || first.HistoryDays > 728 {
		t.Errorf("分位样本天数异常: %d", first.HistoryDays)
	}
	if first.HistoryStart <= 20231009 {
		t.Errorf("分位样本起点不该早于第一根指数 K 线: %d", first.HistoryStart)
	}
	t.Logf("分位样本 %d 个交易日（指数历史更长，前一段缺 250 日均线）", first.HistoryDays)

	// 同一周期重复请求必须完全一致（缓存不能引入漂移）
	again, err := svc.GetMarketTemperature(1)
	if err != nil {
		t.Fatalf("重复请求失败: %v", err)
	}
	if again.Temperature != first.Temperature || again.Composite != first.Composite {
		t.Errorf("重复请求结果不一致：温度 %v vs %v", again.Temperature, first.Temperature)
	}
}

// rawIndexCloses 直接从数据湖读 index_bars，整理成 代码 → 按日期升序的收盘价。
func rawIndexCloses(t *testing.T, svc *StockService) map[string][]float64 {
	t.Helper()
	rows, err := svc.lakeHandle().AllIndexBars()
	if err != nil {
		t.Fatalf("读取 index_bars 失败: %v", err)
	}
	type point struct {
		date  int64
		close float64
	}
	grouped := map[string][]point{}
	for _, row := range rows {
		if row.Close <= 0 {
			continue
		}
		grouped[row.Symbol] = append(grouped[row.Symbol], point{row.Date(), row.Close})
	}
	out := make(map[string][]float64, len(grouped))
	for code, list := range grouped {
		sort.Slice(list, func(i, j int) bool { return list[i].date < list[j].date })
		closes := make([]float64, 0, len(list))
		seen := map[int64]struct{}{}
		for _, p := range list {
			if _, exists := seen[p.date]; exists {
				continue
			}
			seen[p.date] = struct{}{}
			closes = append(closes, p.close)
		}
		out[code] = closes
	}
	return out
}

// 数据湖里没有 index_bars 时必须是**可解释的空状态**，而不是一屏 0。
func TestGetMarketTemperatureWithoutLakeIndices(t *testing.T) {
	svc, store := newIsolatedService(t)
	store.SetCnLakeRoot(t.TempDir()) // 空目录：没有 curated
	svc.applySource(config.KindCnLake, store.CnLakeRoot())

	result, err := svc.GetMarketTemperature(1)
	if err != nil {
		t.Fatalf("缺少 index_bars 时不该报错，应返回可解释的空状态: %v", err)
	}
	if result.Available {
		t.Fatal("没有 index_bars 时不该报告 Available")
	}
	if result.Message == "" {
		t.Error("空状态必须说明原因")
	}
	if len(result.Indices) != 0 {
		t.Errorf("空状态不该有指数数据，实际 %d 个", len(result.Indices))
	}
}

// 「位置」必须用去趋势后的相对水平，**不能**用点数在历史区间里的位置 ——
// 指数长期向上，后者会把任何时点都判成高位。这条用例把两者的差异钉住。
func TestPositionScoreIgnoresRawPointLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（数据湖全量读）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}

	result, err := svc.GetMarketTemperature(1)
	if err != nil || !result.Available {
		t.Skipf("数据湖 index_bars 不可用: %v / %s", err, result.Message)
	}

	lake := svc.lakeHandle()
	rows, err := lake.AllIndexBars()
	if err != nil {
		t.Fatalf("读取 index_bars 失败: %v", err)
	}
	history := map[string][]float64{}
	for _, row := range rows {
		history[row.Symbol] = append(history[row.Symbol], row.Close)
	}

	differs := 0
	for _, item := range result.Indices {
		list := history[item.Code]
		if len(list) < tempMinSamples {
			continue
		}
		rawPercentile := percentileRank(sortedFloats(list), item.Close)
		if math.Abs(rawPercentile-item.LevelScore) > 5 {
			differs++
		}
		t.Logf("%s：点数分位 %.0f vs 去趋势后的位置分位 %.0f",
			item.Name, rawPercentile, item.LevelScore)
	}
	if differs == 0 {
		t.Error("位置分位与「点数在历史区间里的分位」完全一致 —— " +
			"说明位置又变回了比点数高低，指数长期向上的趋势会让它永远显示高位")
	}
}

// sortedFloats 返回升序副本（给分位函数当样本）。
func sortedFloats(values []float64) []float64 {
	out := append([]float64(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// 说明文字要能自证：既提到分位口径，也给出可核对的事实（均线、区间、量能、高低点）。
func TestIndexTrendNoteMentionsEvidence(t *testing.T) {
	base := MarketIndexStat{
		Name: "测试指数", Close: 110, Ma20: 100, Ma60: 95, Ma250: 90,
		PeriodPctChg: 5.2, MomentumScore: 88, VolumeRatio: 1.35, VolumeScore: 70,
		MaDeviation: 22.2, LevelScore: 91, TrendScore: 85,
		HighDate: 20260101, DrawdownFromHigh: -1.5,
	}

	// ① 创了新高：要写"创 N 日新高"
	stat := base
	stat.NewHighDays = 60
	note := indexTrendNote(stat, 5, 20231009)
	for _, want := range []string{"站上 20 / 60 / 250 日均线", "5日", "历史分位", "量能", "250 日均线", "创 60 日新高"} {
		if !strings.Contains(note, want) {
			t.Errorf("趋势说明缺少 %q：%s", want, note)
		}
	}

	// ② 没创新高：要写距高点的回撤（不能让"当前状态"没有位置信息）
	stat = base
	stat.NewHighDays = 0
	note = indexTrendNote(stat, 5, 20231009)
	if !strings.Contains(note, "回撤") || !strings.Contains(note, "2026-01-01") {
		t.Errorf("没有新高时应写明回撤与高点日期：%s", note)
	}

	// ③ 跌破均线时的措辞
	stat = base
	stat.Close = 80
	stat.MaDeviation = -11.1
	stat.LevelScore = 8
	note = indexTrendNote(stat, 5, 20231009)
	if !strings.Contains(note, "跌破 20 / 60 / 250 日均线") {
		t.Errorf("跌破均线时的措辞不对：%s", note)
	}

	if text := indexLevelText(base, 20231009); !strings.Contains(text, "分位") ||
		!strings.Contains(text, "250 日均线") || !strings.Contains(text, "2023-10-09") {
		t.Errorf("位置说明缺少口径或历史起点：%s", text)
	}
}

// 编译期确认：数据湖接口出现在 provider 的生命周期里（避免以后误删）。
var _ = cnlake.IndexBarRow{}
