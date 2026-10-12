package service

import (
	"testing"
	"time"

	"freestock/internal/stockdb"
)

// ---------------------------------------------------------------------------
// 周期规范化
// ---------------------------------------------------------------------------

func TestNormalizeBoardPeriods(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want []int
	}{
		{name: "空列表回落到默认周期", in: nil, want: []int{5, 10, 15, 30, 60}},
		{name: "排序去重", in: []int{30, 5, 5, 10}, want: []int{5, 10, 30}},
		// 1 个交易日就是「当日」，走日行情那条路，不该混进区间周期
		{name: "丢弃当日", in: []int{1, 5}, want: []int{5}},
		{name: "全是当日时回落到默认", in: []int{0, 1, -3}, want: []int{5, 10, 15, 30, 60}},
		{name: "上限截断", in: []int{400}, want: []int{maxBoardPeriod}},
		{name: "自定义周期保留", in: []int{7, 5}, want: []int{5, 7}},
	}

	for _, tc := range cases {
		got := NormalizeBoardPeriods(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: 得到 %v，期望 %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: 得到 %v，期望 %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 区间涨跌幅与周期窗口
// ---------------------------------------------------------------------------

// **算不出来和"涨跌幅是 0"是两回事**：缺少基准价（次新股 / 长期停牌）时必须
// 报告"算不出来"，让它从区间口径里排除；按 0 摊进等权平均会系统性压低板块涨幅。
func TestPeriodReturnFromCloses(t *testing.T) {
	ret, ok := periodReturnFromCloses(10, 11)
	if !ok {
		t.Fatal("有效收盘价应当能算出区间涨跌幅")
	}
	if ret.pctChg != 10 {
		t.Errorf("区间涨跌幅 = %v，期望 10", ret.pctChg)
	}
	if ret.base != 10 || ret.close != 11 {
		t.Errorf("基准/最新收盘价 = %v/%v，期望 10/11", ret.base, ret.close)
	}

	if _, ok := periodReturnFromCloses(0, 11); ok {
		t.Error("缺少基准价时应当报「算不出来」")
	}
	if _, ok := periodReturnFromCloses(10, 0); ok {
		t.Error("缺少最新价时应当报「算不出来」")
	}
}

// tradingDays 造一串连续交易日（只需要递增，函数只比较大小）。
func tradingDays(count int) []int {
	out := make([]int, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, 20260101+i)
	}
	return out
}

// 基准日必须是"区间起点的**前一个**交易日"：否则「最近 5 个交易日」会多算第一天。
func TestBoardPeriodWindows(t *testing.T) {
	calendar := tradingDays(20)

	windows, baseDates := boardPeriodWindows(calendar, []int{5, 20})

	if len(windows) != 2 {
		t.Fatalf("窗口数 = %d，期望 2", len(windows))
	}
	five := windows[0]
	if !five.Available {
		t.Fatalf("5 日窗口应当可用: %+v", five)
	}
	// 倒数第 6 个交易日 = 20260115，区间是 20260116 ~ 20260120
	if five.BaseDate != 20260115 || five.StartDate != 20260116 || five.EndDate != 20260120 {
		t.Errorf("5 日窗口 = %+v，期望基准 20260115 / 起 20260116 / 止 20260120", five)
	}
	if baseDates[5] != 20260115 {
		t.Errorf("5 日基准日 = %d，期望 20260115", baseDates[5])
	}
	if five.Label != "5日" {
		t.Errorf("标签 = %q，期望 \"5日\"", five.Label)
	}

	// 20 个交易日算不出 20 日周期（要 21 个），必须明确标为不可用并给出原因
	twenty := windows[1]
	if twenty.Available {
		t.Errorf("交易日不足时不该标为可用: %+v", twenty)
	}
	if twenty.Message == "" {
		t.Error("不可用时必须给出原因")
	}
	if _, ok := baseDates[20]; ok {
		t.Error("算不出的周期不该有基准日")
	}
}

func TestTrimDates(t *testing.T) {
	got := trimDates([]int{20260103, 20260101, 20260102, 20260102, 20251231, 20260104}, 20260101, 20260103)
	want := []int{20260101, 20260102, 20260103}
	if len(got) != len(want) {
		t.Fatalf("裁剪结果 = %v，期望 %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("裁剪结果 = %v，期望 %v（应升序去重）", got, want)
		}
	}
}

// 成分并集要跨板块去重：基准日收盘价是按这个并集取的，重一份就是白查一次。
func TestBoardSymbolsDeduplicates(t *testing.T) {
	boards := []stockdb.Board{
		{Code: "BK1", Symbols: []string{"600000", "000001"}},
		{Code: "BK2", Symbols: []string{"000001", "600519"}},
		{Code: "BK3", Symbols: []string{""}},
	}

	got := boardSymbols(boards)
	want := []string{"000001", "600000", "600519"}
	if len(got) != len(want) {
		t.Fatalf("成分并集 = %v，期望 %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("成分并集 = %v，期望 %v（应当升序）", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 板块区间聚合
// ---------------------------------------------------------------------------

// periodIndex 造一个只含指定周期结果的索引。
func periodIndex(periods []int, byCode map[int]map[string]periodReturn) *boardPeriodIndex {
	return &boardPeriodIndex{periods: periods, returns: byCode}
}

func TestAggregateBoardPeriodWeightedAndEqual(t *testing.T) {
	// 小盘股区间涨 20%（市值 1 亿），大盘股跌 4%（市值 99 亿）
	quotes := map[string]StockItem{
		"A": stock("A", "小盘", 1, 1e6, 1e8),
		"B": stock("B", "大盘", 1, 1e8, 99e8),
	}
	index := periodIndex([]int{5}, map[int]map[string]periodReturn{
		5: {
			"A": {pctChg: 20, base: 10, close: 12},
			"B": {pctChg: -4, base: 10, close: 9.6},
		},
	})

	got := aggregateBoardPeriod(board("BK1", "测试", "A", "B"), quotes, index, 5)

	// 加权：(20×1 + (-4)×99) / 100 = -3.76%
	if got.PctChg != -3.76 {
		t.Errorf("加权区间涨跌幅 = %v，期望 -3.76", got.PctChg)
	}
	// 等权：(20 + (-4)) / 2 = 8%
	if got.PctChgEqual != 8 {
		t.Errorf("等权区间涨跌幅 = %v，期望 8", got.PctChgEqual)
	}
	if got.UpCount != 1 || got.DownCount != 1 || got.FlatCount != 0 {
		t.Errorf("区间涨跌家数错误: 涨=%d 跌=%d 平=%d", got.UpCount, got.DownCount, got.FlatCount)
	}
	if got.QuotedCount != 2 || got.MemberCount != 2 {
		t.Errorf("成分数错误: quoted=%d member=%d", got.QuotedCount, got.MemberCount)
	}
	if got.Leader == nil || got.Leader.Code != "A" || got.Leader.PctChg != 20 {
		t.Errorf("区间领涨股错误: %+v", got.Leader)
	}
	if got.Laggard == nil || got.Laggard.Code != "B" || got.Laggard.PctChg != -4 {
		t.Errorf("区间领跌股错误: %+v", got.Laggard)
	}
}

// 缺少基准价的成分（次新股）要从区间口径里排除，并体现在覆盖率上。
func TestAggregateBoardPeriodExcludesMissingBase(t *testing.T) {
	quotes := map[string]StockItem{
		"A": stock("A", "老股", 1, 1e8, 10e8),
		"B": stock("B", "次新", 1, 1e8, 10e8),
		"C": stock("C", "停牌", 0, 0, 10e8),
	}
	index := periodIndex([]int{5}, map[int]map[string]periodReturn{
		5: {"A": {pctChg: 6, base: 10, close: 10.6}},
	})

	got := aggregateBoardPeriod(board("BK1", "测试", "A", "B", "C"), quotes, index, 5)

	if got.MemberCount != 3 {
		t.Errorf("成分总数应为 3，实际 %d", got.MemberCount)
	}
	// B 是次新（缺少基准价）、C 没有当日行情 → 只有 A 参与区间统计
	if got.QuotedCount != 1 {
		t.Errorf("能算区间涨跌幅的成分应为 1，实际 %d", got.QuotedCount)
	}
	if got.PctChg != 6 || got.PctChgEqual != 6 {
		t.Errorf("只该按 A 统计: 加权=%v 等权=%v", got.PctChg, got.PctChgEqual)
	}
	if got.UpCount+got.DownCount+got.FlatCount != got.QuotedCount {
		t.Errorf("涨跌家数之和应等于有区间行情的成分数: %d != %d",
			got.UpCount+got.DownCount+got.FlatCount, got.QuotedCount)
	}
}

func TestAggregateBoardPeriodEmpty(t *testing.T) {
	quotes := map[string]StockItem{"A": stock("A", "甲", 1, 1e8, 10e8)}
	// 周期结果整体为空（历史不够）：不能出 NaN，也不能假装有数据
	got := aggregateBoardPeriod(board("BK1", "测试", "A"), quotes,
		periodIndex([]int{5}, map[int]map[string]periodReturn{5: {}}), 5)

	if got.QuotedCount != 0 || got.PctChg != 0 || got.PctChgEqual != 0 {
		t.Errorf("没有区间数据时各项应为 0: %+v", got)
	}
	if got.Leader != nil || got.Laggard != nil {
		t.Error("没有区间数据时不该有领涨/领跌股")
	}
}

// 加权/等权都是平均值，必须落在区间领涨与领跌之间 —— 这条不变式能抓住
// 基准日取错、权重用错、区间涨跌幅正负号搞反等一整类错误。
func TestAggregateBoardPeriodAverageWithinRange(t *testing.T) {
	quotes := map[string]StockItem{}
	returns := map[string]periodReturn{}
	for i, pct := range []float64{12, -3, 5.5, 0, -8.2, 30} {
		code := string(rune('A' + i))
		quotes[code] = stock(code, code, 1, 1e8, float64(10+i)*1e8)
		returns[code] = periodReturn{pctChg: pct, base: 10, close: 10 * (1 + pct/100)}
	}

	got := aggregateBoardPeriod(board("BK1", "测试", "A", "B", "C", "D", "E", "F"), quotes,
		periodIndex([]int{5}, map[int]map[string]periodReturn{5: returns}), 5)

	if got.PctChg > got.Leader.PctChg || got.PctChg < got.Laggard.PctChg {
		t.Errorf("加权区间涨跌幅 %v 不在 [%v, %v] 之间", got.PctChg, got.Laggard.PctChg, got.Leader.PctChg)
	}
	if got.PctChgEqual > got.Leader.PctChg || got.PctChgEqual < got.Laggard.PctChg {
		t.Errorf("等权区间涨跌幅 %v 不在 [%v, %v] 之间", got.PctChgEqual, got.Laggard.PctChg, got.Leader.PctChg)
	}
}

// ---------------------------------------------------------------------------
// 真实数据
// ---------------------------------------------------------------------------

// 默认周期（5/10/15/30/60）要真的算出来，且各周期窗口是自洽的。
func TestGetBoardQuotesPeriodsAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	began := time.Now()
	result, err := svc.GetBoardQuotes("申万一级", []int{60, 5, 10, 5, 15, 30}, 0)
	if err != nil {
		t.Fatalf("GetBoardQuotes 失败: %v", err)
	}
	t.Logf("区间行情耗时 %s（含首次取数）", time.Since(began).Round(time.Millisecond))

	if result.PeriodNotice != "" {
		t.Fatalf("区间行情不该不可用: %s", result.PeriodNotice)
	}
	want := []int{5, 10, 15, 30, 60}
	if len(result.Periods) != len(want) {
		t.Fatalf("周期列表 = %v，期望 %v（应去重并升序）", result.Periods, want)
	}
	for i, days := range want {
		if result.Periods[i] != days {
			t.Fatalf("周期列表 = %v，期望 %v", result.Periods, want)
		}
	}
	if len(result.Windows) != len(want) {
		t.Fatalf("窗口数 = %d，期望 %d", len(result.Windows), len(want))
	}

	for i, window := range result.Windows {
		if !window.Available {
			t.Errorf("%s 窗口应当可用: %+v", window.Label, window)
			continue
		}
		if window.BaseDate >= window.StartDate || window.StartDate > window.EndDate {
			t.Errorf("%s 窗口日期不合理: %+v", window.Label, window)
		}
		if window.EndDate != result.TradeDate {
			t.Errorf("%s 窗口的结束日应当是当前交易日 %d，实际 %d",
				window.Label, result.TradeDate, window.EndDate)
		}
		// 周期越长，基准日越早
		if i > 0 && window.BaseDate >= result.Windows[i-1].BaseDate {
			t.Errorf("%s 的基准日 %d 不早于更短周期的 %d",
				window.Label, window.BaseDate, result.Windows[i-1].BaseDate)
		}
	}

	covered := 0
	for _, item := range result.Boards {
		if len(item.Periods) != len(want) {
			t.Fatalf("%s 的区间周期数 = %d，期望 %d", item.Name, len(item.Periods), len(want))
		}
		for _, stat := range item.Periods {
			if stat.QuotedCount > stat.MemberCount {
				t.Errorf("%s %d日: 有区间行情的成分 %d 超过成分总数 %d",
					item.Name, stat.Days, stat.QuotedCount, stat.MemberCount)
			}
			if stat.UpCount+stat.DownCount+stat.FlatCount != stat.QuotedCount {
				t.Errorf("%s %d日: 涨跌家数之和对不上 %d+%d+%d != %d",
					item.Name, stat.Days, stat.UpCount, stat.DownCount, stat.FlatCount, stat.QuotedCount)
			}
			if stat.QuotedCount == 0 {
				continue
			}
			// 平均值必须落在领涨/领跌之间（加权与等权都是平均）
			if stat.PctChg > stat.Leader.PctChg+0.0001 || stat.PctChg < stat.Laggard.PctChg-0.0001 {
				t.Errorf("%s %d日: 加权 %v 不在 [%v, %v] 之间",
					item.Name, stat.Days, stat.PctChg, stat.Laggard.PctChg, stat.Leader.PctChg)
			}
			// 板块区间涨跌幅不该出现离谱值（60 个交易日翻倍/腰斩都算异常）
			if stat.PctChg < -90 || stat.PctChg > 200 {
				t.Errorf("%s %d日: 区间涨跌幅异常 %v", item.Name, stat.Days, stat.PctChg)
			}
		}
		if item.Periods[len(item.Periods)-1].QuotedCount > 0 {
			covered++
		}
	}

	if covered != len(result.Boards) {
		t.Errorf("有 %d/%d 个板块连最长周期都算不出区间行情", len(result.Boards)-covered, len(result.Boards))
	}

	top := result.Boards[0]
	longest := top.Periods[len(top.Periods)-1]
	t.Logf("%d 个板块；领涨板块 %s 当日 %+.2f%%，%s %+.2f%%（覆盖 %d/%d）",
		result.BoardCount, top.Name, top.PctChg, result.Windows[len(result.Windows)-1].Label,
		longest.PctChg, longest.QuotedCount, longest.MemberCount)
}

// 换周期只补**缺的那个基准日**：已经取过的基准日不重取，切分类完全不取数。
func TestBoardPeriodCacheOnlyFetchesMissingBaseDates(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	if _, err := svc.GetBoardQuotes("申万一级", []int{5}, 0); err != nil {
		t.Fatalf("首次请求失败: %v", err)
	}

	svc.periodMu.Lock()
	first := svc.periodCache
	svc.periodMu.Unlock()
	if first == nil {
		t.Fatal("首次请求之后应当有基准价缓存")
	}
	if len(first.closes) != 1 {
		t.Fatalf("首次请求应当只取 1 个基准日，实际 %d", len(first.closes))
	}

	// ① 同样的周期集合：不取数，缓存对象都不该换
	began := time.Now()
	if _, err := svc.GetBoardQuotes("申万一级", []int{5}, 0); err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}
	cachedHit := time.Since(began)

	svc.periodMu.Lock()
	second := svc.periodCache
	svc.periodMu.Unlock()
	if second != first || len(second.closes) != 1 {
		t.Errorf("重复请求不该重新取数：closes=%d（期望 1）", len(second.closes))
	}

	// ② 换一个没算过的周期：只补 1 个新的基准日
	if _, err := svc.GetBoardQuotes("申万一级", []int{20}, 0); err != nil {
		t.Fatalf("换周期失败: %v", err)
	}
	svc.periodMu.Lock()
	third := svc.periodCache
	svc.periodMu.Unlock()
	if third != first {
		t.Error("换周期不该丢缓存")
	}
	if len(third.closes) != 2 {
		t.Errorf("换周期应当只补 1 个基准日，实际有 %d 个", len(third.closes))
	}
	t.Logf("命中缓存的区间行情耗时 %s", cachedHit.Round(time.Millisecond))
}

// 数据湖那条路也要算出同样的东西 —— 它走的是 `TradingDates`（列日分区目录）
// 与 `ClosesBefore`（一次扫一段分区），而不是逐只点查询。
func TestGetBoardQuotesPeriodsFromLake(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（数据湖全量列表很慢）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}

	// 分类名由数据源决定（数据湖的行业体系名与 free-stockdb 不一定相同），
	// 所以挑一个板块最多的分类来验证，并把分类名列出来便于排查。
	categories, err := svc.GetBoardCategories()
	if err != nil {
		t.Fatalf("读取板块分类失败: %v", err)
	}
	if len(categories) == 0 {
		t.Skip("数据湖里没有板块分类")
	}
	for _, category := range categories {
		t.Logf("分类 %s：%d 个板块", category.Name, category.Count)
	}
	target := categories[0]
	for _, category := range categories {
		if category.Name == "申万一级" || category.Count > target.Count {
			target = category
		}
	}

	began := time.Now()
	result, err := svc.GetBoardQuotes(target.Key, []int{5, 20}, 0)
	if err != nil {
		t.Fatalf("数据湖上 GetBoardQuotes 失败: %v", err)
	}
	t.Logf("数据湖区间行情耗时 %s（分类 %s）", time.Since(began).Round(time.Millisecond), target.Key)

	if result.PeriodNotice != "" {
		t.Fatalf("数据湖上区间行情不该不可用: %s", result.PeriodNotice)
	}
	if len(result.Periods) != 2 || result.Periods[0] != 5 || result.Periods[1] != 20 {
		t.Fatalf("周期列表 = %v，期望 [5 20]", result.Periods)
	}
	if len(result.Windows) != 2 || !result.Windows[1].Available {
		t.Fatalf("窗口 = %+v，期望两个都可算", result.Windows)
	}

	covered := 0
	for _, item := range result.Boards {
		if len(item.Periods) != 2 {
			t.Fatalf("%s 的区间周期数 = %d，期望 2", item.Name, len(item.Periods))
		}
		longest := item.Periods[1]
		if longest.QuotedCount > 0 {
			covered++
		}
		// 20 日区间涨跌幅必须落在成分股的区间涨跌幅之间
		if longest.QuotedCount > 0 &&
			(longest.PctChg > longest.Leader.PctChg+0.0001 || longest.PctChg < longest.Laggard.PctChg-0.0001) {
			t.Errorf("%s 20日: 加权 %v 不在 [%v, %v] 之间",
				item.Name, longest.PctChg, longest.Laggard.PctChg, longest.Leader.PctChg)
		}
	}
	if covered == 0 {
		t.Error("数据湖上一个板块都算不出 20 日区间涨跌幅")
	}
	t.Logf("数据湖：%d 个板块，其中 %d 个算出了 20 日区间行情", result.BoardCount, covered)
}
