package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"freestock/internal/cnlake"
	"freestock/internal/cnlake/cnlaketest"
)

// dayOf 把 "2026-01-05" 折成 parquet DATE（距 1970-01-01 的天数）。
func dayOf(t *testing.T, ymd string) (int32, int64) {
	t.Helper()
	d, err := time.Parse("2006-01-02", ymd)
	if err != nil {
		t.Fatalf("日期写法不对：%q（要 YYYY-MM-DD）", ymd)
	}
	return int32(d.Unix() / 86400), mustDateInt(t, d)
}

func mustDateInt(t *testing.T, d time.Time) int64 {
	t.Helper()
	return int64(d.Year()*10000 + int(d.Month())*100 + d.Day())
}

// buildCloseLake 造一个"只有日线"的小湖。
//
// 不用 cnlaketest.Build：那个按年分区、每年只有 3 月 20 个交易日，
// 而这里要的是连续日期，才能验证"基准日停牌往前找"这类语义。
func buildCloseLake(t *testing.T, closes map[string]map[string]float64) *cnlake.Lake {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "curated", "daily_bars")

	byDate := map[string][]cnlake.BarRow{}
	for symbol, series := range closes {
		for ymd, close := range series {
			days, _ := dayOf(t, ymd)
			byDate[ymd] = append(byDate[ymd], cnlake.BarRow{
				Symbol:    symbol,
				TradeDate: days,
				Open:      close,
				High:      close,
				Low:       close,
				Close:     close,
				Volume:    1000,
				Amount:    close * 1000,
			})
		}
	}
	for ymd, rows := range byDate {
		partition := filepath.Join(dir, "trade_date="+ymd)
		if err := os.MkdirAll(partition, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := parquet.WriteFile(filepath.Join(partition, "part-000.parquet"), rows); err != nil {
			t.Fatalf("写日线分区失败：%v", err)
		}
	}
	// 标的清单留空目录即可：这些用例只读日线
	if err := os.MkdirAll(filepath.Join(root, "curated", "instruments"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cnlake.Open(root)
}

// 基准日当天有成交：取当天收盘价
func TestLakeClosesBeforeUsesSameDay(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {"2026-01-05": 10, "2026-01-06": 11, "2026-01-07": 12},
	})
	p := &lakeProvider{lake: lake}

	got, err := p.ClosesBefore(context.Background(), []string{"600519.SH"}, []int{20260106})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if v := got[20260106]["600519"]; v != 11 {
		t.Errorf("应取基准日当天收盘 11，实际 %v（全部 %+v）", v, got)
	}
}

// 基准日停牌（当天没有这根）：往前找最近一根 —— 这是 closesLookbackDays 的语义
func TestLakeClosesBeforeFallsBackToPreviousBar(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		// 只在 1/5 有数据，1/6、1/7 停牌
		"600519.SH": {"2026-01-05": 10},
		"000001.SZ": {"2026-01-05": 20, "2026-01-06": 21, "2026-01-07": 22},
	})
	p := &lakeProvider{lake: lake}

	got, err := p.ClosesBefore(context.Background(),
		[]string{"600519.SH", "000001.SZ"},
		[]int{20260106, 20260107})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if v := got[20260106]["600519"]; v != 10 {
		t.Errorf("停牌股应回落到 1/5 的 10，实际 %v", v)
	}
	if v := got[20260107]["600519"]; v != 10 {
		t.Errorf("停牌股应回落到 1/5 的 10，实际 %v", v)
	}
	if v := got[20260106]["000001"]; v != 21 {
		t.Errorf("1/6 应取 21，实际 %v", v)
	}
	if v := got[20260107]["000001"]; v != 22 {
		t.Errorf("1/7 应取 22，实际 %v", v)
	}
}

// 基准日之前完全没有数据：不能给值（界面显示「—」而不是 0%）
func TestLakeClosesBeforeNoEarlierBar(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"300750.SZ": {"2026-01-07": 30}, // 1/7 才有数据
	})
	p := &lakeProvider{lake: lake}

	got, err := p.ClosesBefore(context.Background(), []string{"300750.SZ"}, []int{20260105})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if _, ok := got[20260105]["300750"]; ok {
		t.Errorf("基准日之前没有数据时不该给值，实际 %+v", got[20260105])
	}
}

// 多个基准日各取各自"不晚于它的最后一根"——流式实现最容易写错的地方
func TestLakeClosesBeforeMultipleBaseDates(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {
			"2026-01-01": 1, "2026-01-02": 2, "2026-01-03": 3,
			"2026-01-04": 4, "2026-01-05": 5,
		},
	})
	p := &lakeProvider{lake: lake}

	got, err := p.ClosesBefore(context.Background(), []string{"600519.SH"},
		[]int{20260103, 20260105})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if v := got[20260103]["600519"]; v != 3 {
		t.Errorf("1/3 应为 3，实际 %v", v)
	}
	if v := got[20260105]["600519"]; v != 5 {
		t.Errorf("1/5 应为 5，实际 %v", v)
	}
}

// 收盘价无效（<=0）的行必须跳过，否则会拿 0 当基准价算出离谱涨幅
func TestLakeClosesBeforeSkipsZeroClose(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {"2026-01-05": 10, "2026-01-06": 0},
	})
	p := &lakeProvider{lake: lake}

	got, err := p.ClosesBefore(context.Background(), []string{"600519.SH"}, []int{20260106})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if v := got[20260106]["600519"]; v != 10 {
		t.Errorf("0 收盘应被跳过，回落到 1/5 的 10，实际 %v", v)
	}
}

// 空输入不报错、不 panic
func TestLakeClosesBeforeEmptyInput(t *testing.T) {
	lake := buildCloseLake(t, map[string]map[string]float64{
		"600519.SH": {"2026-01-05": 10},
	})
	p := &lakeProvider{lake: lake}

	if got, err := p.ClosesBefore(context.Background(), nil, []int{20260105}); err != nil || len(got) != 0 {
		t.Errorf("空 codes 应返回空且不报错，实际 %v / %v", got, err)
	}
	if got, err := p.ClosesBefore(context.Background(), []string{"600519.SH"}, nil); err != nil || len(got) != 0 {
		t.Errorf("空 dates 应返回空且不报错，实际 %v / %v", got, err)
	}
}

// 用 cnlaketest 造的真实 schema 湖再跑一遍（按年分区、每年 3 月 20 个交易日）
func TestLakeClosesBeforeAgainstSyntheticLake(t *testing.T) {
	root := t.TempDir()
	if err := cnlaketest.Build(root); err != nil {
		t.Fatalf("造湖失败：%v", err)
	}
	p := NewLakeProvider(cnlake.Open(root))

	// cnlaketest 的日线是每年 3 月 1~20 日
	got, err := p.ClosesBefore(context.Background(), []string{"600519.SH"}, []int{20260310})
	if err != nil {
		t.Fatalf("ClosesBefore 失败：%v", err)
	}
	if _, ok := got[20260310]["600519"]; !ok {
		t.Errorf("合成湖里 3/10 应有收盘价，实际 %+v", got[20260310])
	}
}
