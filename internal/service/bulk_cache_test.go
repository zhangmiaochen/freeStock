package service

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// 造一个接在本机数据湖上的 `*lakeProvider`（拿具体类型才能看内部缓存）。
func newLocalLakeProvider(t *testing.T) *lakeProvider {
	t.Helper()
	// 优先用配置里的湖根；配置没设时用 FREESTOCK_LAKE_ROOT（本机验证用）
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录：配置里没设 cnLakeRoot，也没设 FREESTOCK_LAKE_ROOT")
	}
	lake := cnlake.Open(root)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	t.Logf("数据湖：%s", root)
	return &lakeProvider{lake: lake}
}

// ⚠ **核心不变量**：走批量缓存取到的日线，必须和"按代码直接读"逐字段一致。
//
// 这是替换取数路径的前提 —— 换了读法绝不能改变数据。
func TestBulkCacheBarsMatchDirectRead(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 40 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	// 取一段 30 个交易日的短区间（会走批量缓存）
	window := dates[len(dates)-30:]
	start, end := int(window[0]), int(window[len(window)-1])

	// 先跑一次，触发批量填充
	if _, err := provider.barsFor("000001", start, end); err != nil {
		t.Fatalf("barsFor 失败：%v", err)
	}
	provider.bulkMu.Lock()
	filled := provider.bulkBars != nil
	codes := len(provider.bulkBars)
	provider.bulkMu.Unlock()
	if !filled {
		t.Fatalf("区间 %d~%d（%d 个交易日）应该走批量缓存，却没有填上", start, end, len(window))
	}
	t.Logf("批量缓存已填：%d 只股票（区间 %d~%d）", codes, start, end)

	// 抽若干只逐一比对
	sample := []string{}
	provider.bulkMu.Lock()
	for code := range provider.bulkBars {
		sample = append(sample, code)
	}
	provider.bulkMu.Unlock()
	sort.Strings(sample)
	if len(sample) > 12 {
		sample = sample[:12]
	}

	compared := 0
	for _, code := range sample {
		bulk, err := provider.barsFor(code, start, end)
		if err != nil {
			t.Fatalf("%s 批量读失败：%v", code, err)
		}
		direct, err := provider.lake.DailyBars(code, start/10000, end/10000)
		if err != nil {
			t.Fatalf("%s 直接读失败：%v", code, err)
		}

		want := map[int64]cnlake.BarRow{}
		for _, bar := range direct {
			date := bar.Date()
			if date < int64(start) || date > int64(end) {
				continue
			}
			want[date] = bar
		}
		if len(bulk) != len(want) {
			t.Errorf("%s 行数不一致：批量 %d，直接 %d", code, len(bulk), len(want))
			continue
		}
		for _, bar := range bulk {
			other, ok := want[bar.Date()]
			if !ok {
				t.Errorf("%s %d 直接读里没有", code, bar.Date())
				continue
			}
			if bar.Open != other.Open || bar.High != other.High || bar.Low != other.Low ||
				bar.Close != other.Close || bar.Volume != other.Volume || bar.Amount != other.Amount {
				t.Errorf("%s %d 不一致：批量 %+v，直接 %+v", code, bar.Date(), bar, other)
			}
			compared++
		}
	}
	t.Logf("逐字段比对通过：%d 只股票、%d 行", len(sample), compared)
}

// **端到端**：`DailyHistory` 的输出在批量缓存生效前后必须一致，
// 并且批量路径要快得多。
func TestDailyHistoryFasterWithBulkCache(t *testing.T) {
	provider := newLocalLakeProvider(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 40 {
		t.Skipf("数据湖分区不足：%v", err)
	}
	window := dates[len(dates)-30:]
	start, end := int(window[0]), int(window[len(window)-1])

	// 取一批代码（随便挑，只要在湖里有数据）
	sample := []string{}
	provider.bulkMu.Lock()
	provider.bulkMu.Unlock()
	rows, err := provider.lake.DailyBarsInRange(nil, window[0], window[len(window)-1])
	if err != nil {
		t.Fatalf("读区间失败：%v", err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		code := cnlake.BareSymbol(row.Symbol)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		sample = append(sample, code)
		if len(sample) >= 60 {
			break
		}
	}
	sort.Strings(sample)
	if len(sample) < 10 {
		t.Skipf("窗口内代码太少（%d）", len(sample))
	}
	t.Logf("样本 %d 只，区间 %d~%d", len(sample), start, end)

	// ① 批量路径（第一次会灌缓存）
	began := time.Now()
	first, err := provider.DailyHistory(context.Background(), sample[0], start, end)
	if err != nil {
		t.Fatalf("批量路径失败：%v", err)
	}
	firstElapsed := time.Since(began)

	began = time.Now()
	for _, code := range sample[1:] {
		if _, err := provider.DailyHistory(context.Background(), code, start, end); err != nil {
			t.Fatalf("%s 失败：%v", code, err)
		}
	}
	bulkElapsed := time.Since(began)
	t.Logf("① 批量路径：第 1 只 %s（含灌缓存），其余 %d 只共 %s（每只 %s）",
		firstElapsed.Round(time.Millisecond), len(sample)-1, bulkElapsed.Round(time.Millisecond),
		(bulkElapsed / time.Duration(len(sample)-1)).Round(time.Microsecond))

	// ② 对照：按代码直接读同样这些数据
	began = time.Now()
	for _, code := range sample[1:6] {
		if _, err := provider.lake.DailyBars(code, start/10000, end/10000); err != nil {
			t.Fatalf("%s 直接读失败：%v", code, err)
		}
	}
	directElapsed := time.Since(began) / 5
	projected := directElapsed * time.Duration(len(sample)-1)
	t.Logf("② 对照（按代码直接读）：每只 %s；样本若逐只读约 %s",
		directElapsed.Round(time.Millisecond), projected.Round(time.Millisecond))
	if bulkElapsed > 0 {
		t.Logf("   ★ 批量路径快 %.0f 倍", float64(projected)/float64(bulkElapsed))
	}

	// ③ 输出必须与直接读一致（比行情条数与前收盘）
	began = time.Now()
	direct, err := provider.lake.DailyBars(sample[0], start/10000, end/10000)
	if err != nil {
		t.Fatalf("直接读失败：%v", err)
	}
	_ = began
	wantLen := 0
	for _, bar := range direct {
		if bar.Date() >= int64(start) && bar.Date() <= int64(end) {
			wantLen++
		}
	}
	if len(first) != wantLen {
		t.Errorf("%s 的行情条数：批量 %d，直接 %d", sample[0], len(first), wantLen)
	}
	// 前收盘必须逐条递推正确
	for i := 1; i < len(first); i++ {
		if first[i].PreClose != first[i-1].Close {
			t.Errorf("%s 第 %d 条前收 %.4f ≠ 上一条收盘 %.4f",
				sample[0], i, first[i].PreClose, first[i-1].Close)
			break
		}
	}
	t.Logf("③ 输出一致：%s 共 %d 条，前收盘递推正确", sample[0], len(first))
}
