package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/cnlake/cnlaketest"
	"freestock/internal/stockdb"
	"github.com/parquet-go/parquet-go"
)

// **按月分区**的测试湖（cne 当前布局 trade_date=2026-09）。
//
// 返回（湖根，按日期升序的交易日列表）。
func buildMonthlyLake(t *testing.T, code string, months [][2]int) (string, []int) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "curated", "daily_bars")

	byMonth := map[string][]cnlake.BarRow{}
	dates := make([]int, 0, 64)
	price := 10.0
	for _, ym := range months {
		y, m := ym[0], ym[1]
		for day := 1; day <= 28; day++ {
			if time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC).Weekday() == time.Saturday ||
				time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC).Weekday() == time.Sunday {
				continue
			}
			price += 0.1
			key := fmt.Sprintf("trade_date=%04d-%02d", y, m)
			byMonth[key] = append(byMonth[key], cnlake.BarRow{
				Symbol:    code,
				TradeDate: cnlaketest.Day(y, m, day),
				Open:      price,
				High:      price,
				Low:       price,
				Close:     price,
				Volume:    1000,
				Amount:    price * 1000,
			})
			dates = append(dates, y*10000+m*100+day)
		}
	}
	for key, rows := range byMonth {
		partition := filepath.Join(dir, key)
		if err := os.MkdirAll(partition, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := parquet.WriteFile(filepath.Join(partition, "part-000.parquet"), rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "curated", "instruments"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, dates
}

// 「按月分区」上逐日推进（模拟回填）得到的结果，必须与"每次新建一个
// provider 直接查同一区间"逐字段一致。
//
// 这条守的是对齐改动最容易引入的错误：bulkTo 被推到月末后，
// 缓存里会多出**请求范围之外**的行；若下游忘了裁剪，K 线/回填就会
// 悄悄多出几天数据 —— 结果变了，但不报错。
func TestMonthlyPartitionBulkAlignmentDoesNotChangeQuotes(t *testing.T) {
	const code = "000001.SZ"
	root, dates := buildMonthlyLake(t, code, [][2]int{{2026, 9}, {2026, 10}})
	if len(dates) < 20 {
		t.Fatalf("测试数据太少：%d 个交易日", len(dates))
	}

	ctx := context.Background()

	// ① 同一个 provider 逐日推进：批量缓存会不断向右扩展（并因对齐而跨月）
	advancing := NewLakeProvider(cnlake.Open(root))
	for _, date := range dates {
		got, err := advancing.DailyHistory(ctx, code, date, date)
		if err != nil {
			t.Fatalf("%d 取数失败：%v", date, err)
		}
		if len(got) != 1 {
			t.Fatalf("%d 应返回 1 行，实际 %d 行（说明区间外的行泄漏出来了）", date, len(got))
		}
		if int(got[0].Date) != date {
			t.Fatalf("%d 返回了错误日期 %d", date, got[0].Date)
		}

		// ② 对照组：每次全新 provider，等价于"直接读"
		fresh := NewLakeProvider(cnlake.Open(root))
		want, err := fresh.DailyHistory(ctx, code, date, date)
		if err != nil {
			t.Fatalf("%d 对照取数失败：%v", date, err)
		}
		compareQuotes(t, fmt.Sprintf("日期 %d", date), got, want)
	}
}

// 区间查询同样不能被"对齐到月末"污染。
func TestMonthlyPartitionBulkAlignmentRange(t *testing.T) {
	const code = "000001.SZ"
	root, dates := buildMonthlyLake(t, code, [][2]int{{2026, 9}, {2026, 10}})
	ctx := context.Background()

	// 先让缓存向右扩到 10 月末（模拟回填已推进到那里）
	warm := NewLakeProvider(cnlake.Open(root))
	if _, err := warm.DailyHistory(ctx, code, dates[len(dates)-1], dates[len(dates)-1]); err != nil {
		t.Fatal(err)
	}

	// 再问一个很窄的区间：必须只返回区间内的行
	start, end := dates[2], dates[4]
	got, err := warm.DailyHistory(ctx, code, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("窄区间应返回 3 行，实际 %d 行", len(got))
	}
	for _, q := range got {
		if int(q.Date) < start || int(q.Date) > end {
			t.Fatalf("返回了区间外的行 %d（请求 %d~%d）", q.Date, start, end)
		}
	}

	// 与直接读逐字段一致
	fresh := NewLakeProvider(cnlake.Open(root))
	want, err := fresh.DailyHistory(ctx, code, start, end)
	if err != nil {
		t.Fatal(err)
	}
	compareQuotes(t, "窄区间", got, want)
}

// DailyBars（评分路径）同样不能被污染：它直接给 score 用，
// 多一行就会让指标（均线、量比）算错。
func TestMonthlyPartitionBulkAlignmentDailyBars(t *testing.T) {
	const code = "000001.SZ"
	root, dates := buildMonthlyLake(t, code, [][2]int{{2026, 9}, {2026, 10}})
	ctx := context.Background()

	advancing := NewLakeProvider(cnlake.Open(root))
	for _, date := range dates {
		got, err := advancing.DailyBars(ctx, code, date, date)
		if err != nil {
			t.Fatalf("%d DailyBars 失败：%v", date, err)
		}
		if len(got) != 1 {
			t.Fatalf("%d DailyBars 应返回 1 行，实际 %d 行", date, len(got))
		}
	}
}

func compareQuotes(t *testing.T, label string, got, want []*stockdb.Quote) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：行数 %d != %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i].Date != want[i].Date || got[i].Close != want[i].Close ||
			got[i].Volume != want[i].Volume || got[i].Amount != want[i].Amount {
			t.Fatalf("%s：第 %d 行不一致 got=%+v want=%+v", label, i, got[i], want[i])
		}
	}
}
