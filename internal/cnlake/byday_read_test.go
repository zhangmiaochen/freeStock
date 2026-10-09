package cnlake

import (
	"testing"
	"time"
)

// **按交易日批量读**的实测 —— 这是不改 cne 项目、只在客户端提升查询速度的关键。
//
// 数据湖 `daily_bars` 按交易日分区，每天约 40KB（一天的全市场）。
// 而现有路径 `DailyBars(code, year, year)` 会把**整年**的日文件全打开
// 再按代码过滤 —— 为了一只股票的一段区间扫 250 个文件。
//
// 评分/回填/回测要的本来就是"一批代码 × 一段区间"，
// 按天读只打开区间内的分区，一次拿到全部代码。
func TestReadByDayPartitionIsFast(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	dates, err := lake.BarPartitionDates()
	if err != nil || len(dates) == 0 {
		t.Skipf("列分区失败：%v", err)
	}
	t.Logf("可用交易日 %d 个：%d ~ %d", len(dates), dates[0], dates[len(dates)-1])

	// 取最近 320 个交易日（评分窗口）
	window := dates
	if len(window) > 320 {
		window = window[len(window)-320:]
	}
	start, end := window[0], window[len(window)-1]
	t.Logf("窗口 %d ~ %d（%d 个交易日）", start, end, len(window))

	// ① 新路径：按交易日区间读**全市场**
	began := time.Now()
	all, err := lake.DailyBarsInRange(nil, start, end)
	if err != nil {
		t.Fatalf("按天读失败：%v", err)
	}
	byDayElapsed := time.Since(began)
	t.Logf("① 按交易日读全市场：%d 行，耗时 %s（%d 个日文件，每个 %s）",
		len(all), byDayElapsed.Round(time.Millisecond), len(window),
		(byDayElapsed / time.Duration(len(window))).Round(time.Microsecond))

	// ② 旧路径：按代码读一只（同为 320 个交易日，实际会读整年）
	codes := map[string]bool{}
	for _, bar := range all {
		codes[BareSymbol(bar.Symbol)] = true
	}
	t.Logf("   窗口内共 %d 只股票", len(codes))

	sample := ""
	for _, bar := range all {
		if code := BareSymbol(bar.Symbol); code != "" {
			sample = code
			break
		}
	}
	if sample == "" {
		t.Skip("窗口内没有数据")
	}
	year := int(start / 10000)
	began = time.Now()
	perCode, err := lake.DailyBars(sample, year, year)
	if err != nil {
		t.Fatalf("按代码读失败：%v", err)
	}
	perCodeElapsed := time.Since(began)
	t.Logf("② 按代码读 1 只（%s，整年）：%d 行，耗时 %s",
		sample, len(perCode), perCodeElapsed.Round(time.Millisecond))

	// ③ 外推：同样这批数据若逐只读要多久
	if perCodeElapsed > 0 {
		projected := perCodeElapsed * time.Duration(len(codes))
		t.Logf("③ 同样数据若逐只读：约 %s（%d 只 × %s）",
			projected.Round(time.Second), len(codes), perCodeElapsed.Round(time.Millisecond))
		if byDayElapsed > 0 {
			t.Logf("   ★ 按天读快 %.0f 倍", float64(projected)/float64(byDayElapsed))
		}
	}
}

// 换读法**不能改变数据** —— 按天读出来的每一行必须和按代码读的逐字段一致。
func TestReadByDayMatchesReadByCode(t *testing.T) {
	lake := openLocalLake(t)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	dates, err := lake.BarPartitionDates()
	if err != nil || len(dates) < 5 {
		t.Skip("分区不足")
	}
	window := dates[len(dates)-5:]
	start, end := window[0], window[len(window)-1]

	all, err := lake.DailyBarsInRange(nil, start, end)
	if err != nil {
		t.Fatalf("按天读失败：%v", err)
	}
	if len(all) == 0 {
		t.Skip("窗口内没有数据")
	}
	sample := BareSymbol(all[0].Symbol)
	year := int(start / 10000)

	byCode, err := lake.DailyBars(sample, year, year)
	if err != nil {
		t.Fatalf("按代码读失败：%v", err)
	}

	// 按天读里属于这只股票、且落在窗口内的行
	byDay := map[int64]BarRow{}
	for _, bar := range all {
		if BareSymbol(bar.Symbol) == sample {
			byDay[bar.Date()] = bar
		}
	}

	matched := 0
	for _, bar := range byCode {
		date := bar.Date()
		if date < start || date > end {
			continue // 按代码读的是整年，只比窗口内的
		}
		other, ok := byDay[date]
		if !ok {
			t.Errorf("%s %d 按天读没读到", sample, date)
			continue
		}
		if bar.Open != other.Open || bar.High != other.High || bar.Low != other.Low ||
			bar.Close != other.Close || bar.Volume != other.Volume || bar.Amount != other.Amount {
			t.Errorf("%s %d 数据不一致：按代码 %+v，按天 %+v", sample, date, bar, other)
		}
		matched++
	}
	if matched == 0 {
		t.Fatalf("%s 在窗口 %d~%d 内没有可比对的行", sample, start, end)
	}
	t.Logf("两种读法逐字段一致：%s，%d 行（窗口 %d~%d）", sample, matched, start, end)
}
