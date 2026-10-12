package service

import (
	"context"
	"sort"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/score"
)

// **分块批量读必须与"一次读完"逐行一致。**
//
// 背景：`bulkMaxDays = 700` 原本是"区间超过就不许批量读"，导致长区间
// （2022~2026 回填窗口约 1500 个交易日）整段退化成逐只慢读 ——
// 实测每只 2.8 秒 × 7600 只 ≈ 数小时，这正是"全量重算区间耗时太久"的根因。
//
// 修法是"照样批量读，但一次不超过 `bulkChunkDays` 天"，然后把各块 append 起来。
// 分块引入了新的出错可能：块间重复、块间乱序、边界漏读。
// 所以这里用**跨块**的区间去比对"按代码直读"的结果。
func TestBulkChunkedFillMatchesRangeRead(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < bulkChunkDays+10 {
		t.Skipf("数据湖分区不足（%d 个）：%v", len(dates), err)
	}
	// 故意取跨 2 个分块的区间
	span := bulkChunkDays + 10
	window := dates[len(dates)-span:]
	start, end := int(window[0]), int(window[len(window)-1])
	t.Logf("区间 %d~%d（%d 个交易日，分块大小 %d → 至少 2 块）", start, end, len(window), bulkChunkDays)

	if _, err := provider.barsFor("000001", start, end); err != nil {
		t.Fatalf("barsFor 失败：%v", err)
	}
	provider.bulkMu.Lock()
	filled := provider.bulkBars != nil
	coveredFrom, coveredTo := provider.bulkFrom, provider.bulkTo
	codes := make([]string, 0, len(provider.bulkBars))
	for code := range provider.bulkBars {
		codes = append(codes, code)
	}
	provider.bulkMu.Unlock()
	if !filled {
		t.Fatalf("区间应当触发批量读，却没有填上")
	}
	if len(codes) == 0 {
		t.Fatal("批量缓存里没有任何股票")
	}
	t.Logf("批量缓存：%d 只，覆盖 %d~%d", len(codes), coveredFrom, coveredTo)

	sort.Strings(codes)
	if len(codes) > 8 {
		codes = codes[:8]
	}
	for _, code := range codes {
		oracle, err := provider.lake.DailyBarsInRange([]string{code}, int64(start), int64(end))
		if err != nil {
			t.Fatalf("%s 直读失败：%v", code, err)
		}
		provider.bulkMu.Lock()
		cached := provider.bulkBars[code]
		provider.bulkMu.Unlock()

		// 直读会带出区间外的行，按区间过滤后再比
		want := make([]cnlake.BarRow, 0, len(oracle))
		for _, row := range oracle {
			if date := row.Date(); date >= int64(start) && date <= int64(end) {
				want = append(want, row)
			}
		}
		if len(cached) != len(want) {
			t.Errorf("%s：分块批量读 %d 行，直读 %d 行", code, len(cached), len(want))
			continue
		}
		// 必须严格按日期升序 —— `quoteRange` 靠这个顺序推导前收盘，
		// 乱序会静默算错，所以这里顺带断言
		for i := range cached {
			if i > 0 && cached[i-1].TradeDate > cached[i].TradeDate {
				t.Fatalf("%s：分块后的行没有按日期升序（第 %d 行 %d > 第 %d 行 %d）",
					code, i-1, cached[i-1].TradeDate, i, cached[i].TradeDate)
			}
			if cached[i] != want[i] {
				t.Errorf("%s 第 %d 行不一致：\n  分块 %+v\n  直读 %+v", code, i, cached[i], want[i])
				break
			}
		}
	}
}

// **裁剪不能裁掉还会被读到的数据。**
//
// 回填是按交易日往前推的：第 i 天读 [D_i-320, D_i]。只要 `TrimBulk(before)`
// 里的 `before` 不晚于下一次请求的 `start`，裁剪就是安全的。
// 这里模拟"推进若干天 + 定期裁剪"，把每天的取值与直读逐一比对。
func TestBulkTrimKeepsForwardWindowReadable(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 120 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	window := 60 // 模拟"回填窗口"长度
	tail := dates[len(dates)-window:]
	start, end := int(tail[0]), int(tail[len(tail)-1])
	if _, err := provider.barsFor("000001", start, end); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	provider.bulkMu.Lock()
	rowsBefore := 0
	for _, bars := range provider.bulkBars {
		rowsBefore += len(bars)
	}
	provider.bulkMu.Unlock()

	// 把裁剪线推到倒数第 10 个交易日：之前的都不该再被需要
	cut := int(dates[len(dates)-10])
	provider.TrimBulk(cut)

	provider.bulkMu.Lock()
	rowsAfter := 0
	for _, bars := range provider.bulkBars {
		for _, bar := range bars {
			if int(bar.Date()) < cut {
				t.Errorf("裁剪后仍残留 %d 的行（裁剪线 %d）", bar.Date(), cut)
				break
			}
		}
		rowsAfter += len(bars)
	}
	from := provider.bulkFrom
	provider.bulkMu.Unlock()

	if rowsAfter >= rowsBefore {
		t.Errorf("裁剪后行数没有减少：%d → %d", rowsBefore, rowsAfter)
	}
	if from != cut {
		t.Errorf("bulkFrom 应推进到 %d，实际 %d", cut, from)
	}
	t.Logf("裁剪：%d 行 → %d 行，bulkFrom=%d", rowsBefore, rowsAfter, from)

	// 裁剪线之后的每一天，**公开取数接口**的结果都必须与直读一致。
	//
	// ⚠ 必须用 `DailyBars`（公开方法）：内部的 `barsFor` 返回的是该股票
	// 缓存里的**整条切片**、并不按 [start,end] 过滤 —— 过滤在
	// `DailyBars` 组装 `score.Bar` 时做。我第一次拿 `barsFor` 去比，
	// 得到"每天都是 10 行"，误以为裁剪坏了，其实是比错了接口。
	sample := "000001"
	for index := len(dates) - 10; index < len(dates); index++ {
		day := int(dates[index])
		got, err := provider.DailyBars(context.Background(), sample, cut, day)
		if err != nil {
			t.Fatalf("%d 取数失败：%v", day, err)
		}
		oracle, err := provider.lake.DailyBarsInRange([]string{sample}, int64(cut), int64(day))
		if err != nil {
			t.Fatalf("%d 直读失败：%v", day, err)
		}
		want := 0
		for _, row := range oracle {
			if date := row.Date(); date >= int64(cut) && date <= int64(day) && row.Close > 0 {
				want++
			}
		}
		if len(got) != want {
			t.Errorf("%d：裁剪后取到 %d 行，直读 %d 行", day, len(got), want)
		}
	}
}

// **长区间冷填充：这正是"重算区间耗时太久"那条路。**
//
// 修之前 `bulkMaxDays = 700` 会让超过 700 个交易日的区间**放弃批量读**，
// 退化成逐只 `lake.DailyBars(code, 年, 年)` —— 实测 7 年窗口 2.8 秒/只，
// 全市场 7600 只就是**约 6 小时**。
//
// 这个用例量的是"改完之后，一次把整个长区间读进内存要多久"，
// 并对照"逐只读"的实测单价，把量级差写进日志。
func TestBulkLongWindowColdFill(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 1100 {
		t.Skipf("数据湖分区不足（%d 个，需要 1100+ 才能构造长区间）：%v", len(dates), err)
	}
	span := 1100 // ≈ 4.5 年，> 700，正是会把旧逻辑打回逐只读的长度
	window := dates[len(dates)-span:]
	start, end := int(window[0]), int(window[len(window)-1])
	if len(window) <= bulkMaxDaysForTest {
		t.Skipf("构造出的区间只有 %d 个交易日，没有超过旧的阈值", len(window))
	}
	t.Logf("长区间 %d~%d：%d 个交易日（分成 %d 块）",
		start, end, len(window), (len(window)+bulkChunkDays-1)/bulkChunkDays)

	began := time.Now()
	if err := provider.fillBulk(start, end); err != nil {
		t.Fatalf("长区间批量填充失败：%v", err)
	}
	elapsed := time.Since(began)

	provider.bulkMu.Lock()
	rows := 0
	for _, bars := range provider.bulkBars {
		rows += len(bars)
	}
	codes := len(provider.bulkBars)
	provider.bulkMu.Unlock()

	t.Logf("批量读：%d 只 / %d 行，耗时 %s", codes, rows, elapsed.Round(time.Millisecond))
	t.Logf("对照：逐只读按 2.8 秒/只 × %d 只 ≈ %s",
		codes, (time.Duration(codes) * 2800 * time.Millisecond).Round(time.Minute))

	// 顺带断言：填充后的覆盖区间必须真的覆盖到请求区间
	provider.bulkMu.Lock()
	from, to := provider.bulkFrom, provider.bulkTo
	provider.bulkMu.Unlock()
	if from > start || to < end {
		t.Errorf("覆盖区间不完整：请求 %d~%d，实际 %d~%d", start, end, from, to)
	}
}

// **先填晚的一段、再填早的一段 —— 顺序必须仍然升序。**
//
// 这是实测踩到的坑：回填时 `marketState`/基准会先访问最近的一段
// （`20260825~20261009`），随后主回填才去填 `20231026~20260824`。
// 如果分块 append 就完事，每条切片会变成"2026 在前、2023 在后"，
// 而依赖升序的两处都是**静默出错**：
//
//	quoteRange 推导前收盘 → 取错
//	TrimBulk 的二分找裁剪点 → 找不到，裁剪失效（实测只丢 2017 行）
func TestBulkFillOutOfOrderKeepsAscending(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 120 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	// 先填靠后的一段
	late := dates[len(dates)-40:]
	lateStart, lateEnd := int(late[0]), int(late[len(late)-1])
	if err := provider.fillBulk(lateStart, lateEnd); err != nil {
		t.Fatalf("先填后段失败：%v", err)
	}
	// 再填**紧邻其前**的一段（模拟"补左边"）。
	// ⚠ 两段必须相邻：留空档的话下面"与直读比行数"会因空档而失败
	//（我第一版切成 len-120:len-50，中间空 10 天）。
	early := dates[len(dates)-120 : len(dates)-40]
	earlyStart, earlyEnd := int(early[0]), int(early[len(early)-1])
	if err := provider.fillBulk(earlyStart, earlyEnd); err != nil {
		t.Fatalf("再填前段失败：%v", err)
	}

	provider.bulkMu.Lock()
	codes := make([]string, 0, len(provider.bulkBars))
	for code := range provider.bulkBars {
		codes = append(codes, code)
	}
	provider.bulkMu.Unlock()
	sort.Strings(codes)
	if len(codes) > 6 {
		codes = codes[:6]
	}

	for _, code := range codes {
		provider.bulkMu.Lock()
		bars := append([]cnlake.BarRow(nil), provider.bulkBars[code]...)
		provider.bulkMu.Unlock()
		for i := 1; i < len(bars); i++ {
			if bars[i-1].TradeDate > bars[i].TradeDate {
				t.Fatalf("%s：切片没有按日期升序（第 %d 行 %d > 第 %d 行 %d）—— "+
					"前收盘推导与 TrimBulk 都会静默出错",
					code, i-1, bars[i-1].TradeDate, i, bars[i].TradeDate)
			}
		}
		// 两段的数据都要在，且不重不漏
		oracle, err := provider.lake.DailyBarsInRange([]string{code}, int64(earlyStart), int64(lateEnd))
		if err != nil {
			t.Fatalf("%s 直读失败：%v", code, err)
		}
		want := 0
		for _, row := range oracle {
			if date := row.Date(); date >= int64(earlyStart) && date <= int64(lateEnd) {
				want++
			}
		}
		if len(bars) != want {
			t.Errorf("%s：乱序填充后 %d 行，直读区间内 %d 行", code, len(bars), want)
		}
	}

	// 顺带确认：乱序填充之后，裁剪仍然有效
	provider.bulkMu.Lock()
	rowsBefore := 0
	for _, bars := range provider.bulkBars {
		rowsBefore += len(bars)
	}
	provider.bulkMu.Unlock()
	provider.TrimBulk(int(dates[len(dates)-10]))
	provider.bulkMu.Lock()
	rowsAfter := 0
	for _, bars := range provider.bulkBars {
		rowsAfter += len(bars)
	}
	provider.bulkMu.Unlock()
	if rowsAfter >= rowsBefore {
		t.Errorf("乱序填充后裁剪失效：%d → %d", rowsBefore, rowsAfter)
	}
	t.Logf("乱序填充后裁剪：%d 行 → %d 行", rowsBefore, rowsAfter)
}

// **增量组装的 `score.Bar` 缓存必须与"逐行组装"逐字段一致。**
//
// 这是本轮优化里风险最高的一处：数据湖原本每天把窗口内每根行重新组装一遍
// （算换手率、查股本索引、建结构体），现在改成**按日分区增量组装 + 二分子切片**。
// 一旦切片边界或同步裁剪出错，同一只票"走缓存"与"不走缓存"会算出不同的评分 ——
// 而这种错是静默的（不报错、只是数值不同）。
//
// 断言方式：先用长窗口把缓存灌满，再对**同一只票的多个子窗口**取值，
// 结果必须等于"长窗口结果按区间投影"。
func TestLakeDailyBarsScoredCacheMatchesProjection(t *testing.T) {
	provider := newLocalLakeProvider(t)
	ctx := context.Background()

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 80 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	window := dates[len(dates)-60:]
	start, end := int(window[0]), int(window[len(window)-1])

	// 灌满缓存（顺带把 bulkScored 一起建起来）
	if _, err := provider.DailyBars(ctx, "000001", start, end); err != nil {
		t.Fatalf("预热失败：%v", err)
	}
	provider.bulkMu.Lock()
	scoredCodes := len(provider.bulkScored)
	provider.bulkMu.Unlock()
	if scoredCodes == 0 {
		t.Fatal("增量组装缓存没有被填上 —— DailyBars 仍走了逐行组装")
	}
	t.Logf("已组装缓存：%d 只", scoredCodes)

	full, err := provider.DailyBars(ctx, "000001", start, end)
	if err != nil {
		t.Fatalf("长窗口取值失败：%v", err)
	}
	if len(full) == 0 {
		t.Fatal("长窗口没有数据")
	}

	// 多个子窗口：含首日、末日、单日、以及内部一段
	subCases := [][2]int{
		{int(window[0]), int(window[0])},
		{int(window[len(window)-1]), int(window[len(window)-1])},
		{int(window[0]), int(window[10])},
		{int(window[len(window)-11]), int(window[len(window)-1])},
		{int(window[20]), int(window[40])},
	}
	for _, sub := range subCases {
		got, err := provider.DailyBars(ctx, "000001", sub[0], sub[1])
		if err != nil {
			t.Fatalf("%d~%d 取值失败：%v", sub[0], sub[1], err)
		}
		want := make([]score.Bar, 0, len(got))
		dates := map[int64]bool{}
		for _, bar := range got {
			dates[bar.Date] = true
		}
		for _, bar := range full {
			if bar.Date >= int64(sub[0]) && bar.Date <= int64(sub[1]) {
				want = append(want, bar)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%d~%d：取到 %d 根，长窗口投影 %d 根", sub[0], sub[1], len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%d~%d 第 %d 根不一致：\n  子窗口 %+v\n  投影   %+v",
					sub[0], sub[1], i, got[i], want[i])
				break
			}
		}
	}

	// 缓存里的每一只都必须是升序（切片依赖它）
	provider.bulkMu.Lock()
	checked := 0
	for code, bars := range provider.bulkScored {
		for i := 1; i < len(bars); i++ {
			if bars[i-1].Date > bars[i].Date {
				provider.bulkMu.Unlock()
				t.Fatalf("%s：已组装缓存没有按日期升序（%d > %d）", code, bars[i-1].Date, bars[i].Date)
			}
		}
		checked++
		if checked >= 200 {
			break
		}
	}
	provider.bulkMu.Unlock()
}

// bulkMaxDaysForTest 是旧的阈值（700）。留在这里是为了让"这个区间确实
// 超过了旧阈值"成为可断言的**测试事实**，而不是注释里的一句话。
const bulkMaxDaysForTest = 700

// **服务层的区间缓存必须给数据湖让路。**
//
// `rangeCachingProvider` 的策略是"每只股票首次访问就把整个预取窗口取一遍"。
// 对没有批量能力的源这是对的（把 N 天 × M 只压成 M 次），但对数据湖是反向优化：
// 数据湖天然按日分区，它自己的批量缓存已经做到"每个日文件只读一次"，
// 而那层会让每只股票各自把整窗重读一遍。
//
// 断言方式：让区间缓存去取一笔，底层 lakeProvider 的批量缓存应被填上、
// 而区间缓存自己的 `barFetches` 应保持 0（走了透传）。
func TestRangeCacheDelegatesBarsToLake(t *testing.T) {
	inner := newLocalLakeProvider(t)
	dates, err := inner.lake.BarPartitionDates()
	if err != nil || len(dates) < 40 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	window := dates[len(dates)-40:]
	start, end := int(window[0]), int(window[len(window)-1])

	cache := newRangeCachingProvider(inner, start, end)
	ctx := context.Background()
	if _, err := cache.DailyBars(ctx, "000001", start, end); err != nil {
		t.Fatalf("区间缓存取数失败：%v", err)
	}

	cache.barMu.Lock()
	fetches := cache.barFetches
	cache.barMu.Unlock()
	if fetches != 0 {
		t.Errorf("数据湖模式下区间缓存不应自己取数（走了 %d 次），应当透传", fetches)
	}

	inner.bulkMu.Lock()
	filled := inner.bulkBars != nil
	inner.bulkMu.Unlock()
	if !filled {
		t.Error("透传后底层 lakeProvider 的批量缓存应当被填上")
	}
}
