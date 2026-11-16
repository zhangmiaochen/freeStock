package service

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"freestock/internal/cnlake"
)

// 这组用例回答一个问题：**"只留最近几根"和"流式快照"会不会改变结果？**
//
// 做法是把**旧算法**在测试里原样实现一遍（把区间内所有行读成切片、排序、
// 二分/取末尾），再和新实现逐字段对比。随机造数据、随机取窗口，
// 覆盖停牌（某天没有这根）、上市晚（前面没有数据）、整段缺失等情况。
//
// 只要这组对比是绿的，就说明两处内存优化是**纯实现变化**，口径没动。

// randCloseLake 造一个确定性的随机小湖：codes 只 × dates 天，带缺口。
func randCloseLake(t *testing.T, codes, days int) (map[string]map[string]float64, []string) {
	t.Helper()
	rng := rand.New(rand.NewSource(20260116))
	out := map[string]map[string]float64{}
	symbols := make([]string, 0, codes)
	for i := 0; i < codes; i++ {
		symbol := fmt.Sprintf("%06d.SZ", i+1)
		symbols = append(symbols, symbol)
		series := map[string]float64{}
		// 每只票随机"晚上市"：前 skip 天没有数据
		skip := rng.Intn(days / 3)
		price := 5 + rng.Float64()*50
		for d := skip; d < days; d++ {
			// 5% 概率停牌（当天没有这根）
			if rng.Intn(100) < 5 {
				continue
			}
			price *= 1 + (rng.Float64()-0.5)*0.06
			series[dayString(t, d)] = round(price, 2)
		}
		if len(series) > 0 {
			out[symbol] = series
		}
	}
	return out, symbols
}

// dayString 从基准日 2026-01-01 起第 d 天。
func dayString(t *testing.T, d int) string {
	t.Helper()
	base := parseEquivDate(t, "2026-01-01")
	return base.AddDate(0, 0, d).Format("2006-01-02")
}

// ---------------- 旧算法（参考实现） ----------------

// oldClosesBefore 完全照抄改动前的 lakeProvider.ClosesBefore：
// 把区间内全部行读成切片 → 按代码分组 → 每个基准日二分找"不晚于它的最后一根"。
func oldClosesBefore(t *testing.T, lake *cnlake.Lake, codes []string, dates []int) map[int]map[string]float64 {
	t.Helper()
	sorted := append([]int(nil), dates...)
	sort.Ints(sorted)
	from := shiftBack(sorted[0], closesLookbackDays)

	rows, err := lake.DailyBarsInRange(codes, int64(from), int64(sorted[len(sorted)-1]))
	if err != nil {
		t.Fatalf("参考实现读取失败：%v", err)
	}

	out := map[int]map[string]float64{}
	for _, date := range dates {
		out[date] = map[string]float64{}
	}
	grouped := map[string][]cnlake.BarRow{}
	for _, row := range rows {
		if row.Close <= 0 {
			continue
		}
		date := row.Date()
		if date < int64(from) || date > int64(sorted[len(sorted)-1]) {
			continue
		}
		bare := cnlake.BareSymbol(row.Symbol)
		if bare == "" {
			continue
		}
		grouped[bare] = append(grouped[bare], row)
	}
	for code, list := range grouped {
		sort.Slice(list, func(i, j int) bool { return list[i].TradeDate < list[j].TradeDate })
		for _, date := range sorted {
			idx := sort.Search(len(list), func(i int) bool { return list[i].Date() > int64(date) })
			if idx == 0 {
				continue
			}
			out[date][code] = list[idx-1].Close
		}
	}
	return out
}

// oldBulkQuotes 照抄改动前的 bulkQuotes 的"取哪几根"部分（估值/股本/组装不变）。
func oldBulkQuotes(t *testing.T, p *lakeProvider, codes []string, start, end int) map[string]*stockdbQuoteLite {
	t.Helper()
	if start <= 0 {
		start = 19900101
	}
	rows, err := p.lake.DailyBarsInRange(codes, int64(start), int64(end))
	if err != nil {
		t.Fatalf("参考实现读取失败：%v", err)
	}
	grouped := map[string][]cnlake.BarRow{}
	for _, bar := range rows {
		if date := bar.Date(); date < int64(start) || date > int64(end) {
			continue
		}
		code := cnlake.BareSymbol(bar.Symbol)
		grouped[code] = append(grouped[code], bar)
	}
	out := map[string]*stockdbQuoteLite{}
	for _, code := range codes {
		bare := cnlake.BareSymbol(code)
		list := grouped[bare]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].TradeDate < list[j].TradeDate })
		last := list[len(list)-1]
		var prev float64
		if len(list) >= 2 {
			prev = list[len(list)-2].Close
		}
		q := p.buildQuote(bare, p.name(bare), last, prev, nil, p.shareHistory(bare), priorVolumes(list))
		out[bare] = &stockdbQuoteLite{
			Date: q.Date, Close: q.Close, PreClose: q.PreClose,
			Volume: q.Volume, Amount: q.Amount, PctChg: q.PctChg, VolRatio: q.VolRatio,
		}
	}
	return out
}

// stockdbQuoteLite 只比对"由日线决定"的字段（估值/股本两条路完全一样，不在这里比）。
type stockdbQuoteLite struct {
	Date     int64
	Close    float64
	PreClose float64
	Volume   float64
	Amount   float64
	PctChg   float64
	VolRatio float64
}

// ---------------- 对拍 ----------------

// 板块区间行情的基准价：新旧算法在随机数据上必须逐值相同。
func TestClosesBeforeMatchesOldAlgorithm(t *testing.T) {
	closes, symbols := randCloseLake(t, 20, 120)
	lake := buildCloseLake(t, closes)
	p := &lakeProvider{lake: lake}

	// 用 5/10/15/30/60 的典型基准日（落在随机数据覆盖的区间里）
	base := parseEquivDate(t, "2026-01-01")
	pick := func(offset int) int {
		d := base.AddDate(0, 0, offset)
		return int(d.Year())*10000 + int(d.Month())*100 + d.Day()
	}
	dates := []int{pick(119), pick(110), pick(100), pick(80), pick(50)}

	codes := symbols
	got, err := p.ClosesBefore(context.Background(), codes, dates)
	if err != nil {
		t.Fatalf("新实现失败：%v", err)
	}
	want := oldClosesBefore(t, lake, codes, dates)

	for _, date := range dates {
		if len(got[date]) != len(want[date]) {
			t.Errorf("基准日 %d 代码数不一致：新 %d / 旧 %d", date, len(got[date]), len(want[date]))
		}
		for code, w := range want[date] {
			if g := got[date][code]; g != w {
				t.Errorf("基准日 %d 代码 %s 收盘价不一致：新 %v / 旧 %v", date, code, g, w)
			}
		}
		for code := range got[date] {
			if _, ok := want[date][code]; !ok {
				t.Errorf("基准日 %d 新实现多出了代码 %s（%v）", date, code, got[date][code])
			}
		}
	}
}

// 股票列表的"最新几根"：新旧算法在随机数据上必须逐字段相同。
func TestBulkQuotesMatchesOldAlgorithm(t *testing.T) {
	closes, symbols := randCloseLake(t, 15, 90)
	lake := buildCloseLake(t, closes)
	p := &lakeProvider{lake: lake}

	base := parseEquivDate(t, "2026-01-01")
	dateOf := func(offset int) int {
		d := base.AddDate(0, 0, offset)
		return int(d.Year())*10000 + int(d.Month())*100 + d.Day()
	}

	cases := []struct {
		name  string
		start int
		end   int
	}{
		{"近期窗口", dateOf(89 - quoteLookbackDays), dateOf(89)},
		{"不限起点（退市/停牌常走这条）", 0, dateOf(89)},
		{"中间一段", dateOf(60), dateOf(80)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := p.bulkQuotes(context.Background(), symbols, tc.start, tc.end)
			if err != nil {
				t.Fatalf("新实现失败：%v", err)
			}
			want := oldBulkQuotes(t, p, symbols, tc.start, tc.end)

			if len(got) != len(want) {
				t.Errorf("代码数不一致：新 %d / 旧 %d", len(got), len(want))
			}
			for code, w := range want {
				g := got[code]
				if g == nil {
					t.Errorf("%s 新实现缺失", code)
					continue
				}
				if g.Date != w.Date || g.Close != w.Close || g.PreClose != w.PreClose ||
					g.Volume != w.Volume || g.Amount != w.Amount ||
					g.PctChg != w.PctChg || g.VolRatio != w.VolRatio {
					t.Errorf("%s 不一致：\n 新 %+v\n 旧 %+v", code, *g, *w)
				}
			}
			for code := range got {
				if _, ok := want[code]; !ok {
					t.Errorf("新实现多出了 %s", code)
				}
			}
		})
	}
}

// parseEquivDate 解析测试用日期。
func parseEquivDate(t *testing.T, ymd string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", ymd)
	if err != nil {
		t.Fatalf("日期写法不对：%q", ymd)
	}
	return d
}
