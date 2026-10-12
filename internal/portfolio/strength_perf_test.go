package portfolio

import (
	"path/filepath"
	"testing"
	"time"
)

// **落库单行成本 vs 批量大小的微基准。**
//
// 回填的落库是"每天一个事务 + 若干条批量 INSERT"。从实测两点
// （297 行/17ms、7151 行/159ms）拟合出来是
//
//	每天 ≈ 11ms 固定开销 + ≈ 21µs/行
//
// 也就是**行数成本占绝对多数**（970 天：固定约 10s、行数约 143s）。
// 所以先确认"加大批量"能不能把每行成本压下来 —— 能压就压，
// 压不动就说明已经贴近纯 Go 驱动的底，不该再折腾事务结构。
func TestStrengthSaveRowCost(t *testing.T) {
	if testing.Short() {
		t.Skip("落库微基准（写几万行）：-short 跳过")
	}
	store, err := Open(filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	defer store.Close()

	const rowCount = 7000
	rows := make([]StrengthRow, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		rows = append(rows, StrengthRow{
			Code: codeN(i), Name: "测试", TradeDate: 20261009,
			TotalScore: 80, TrendScore: 25, StructureScore: 15, RSScore: 13,
			CapitalScore: 10, RepairScore: 10,
			Close: 10.5, High: 11, Low: 10, Open: 10.2, PctChg: 1.5, Amount: 1.23e8,
			Ma5: 10.4, Ma20: 10.3, Ma30: 10.2, Ma60: 10, Ma120: 9.8, Ma250: 9.5,
			RelativeStrength20D: 1.2, DrawdownFromHigh20: -3, DrawdownFromHigh120: -8,
			ReboundFromLow60: 5, StockReturn3D: 0.5, StockReturn5D: 1, StockReturn10D: 2,
			StockReturn20D: 3, VolumeRatio20: 1.1, VolumeRatio520: 1.0,
			Ma5Prev: 10.3, Ma250Prev: 9.4, IndustryReturn20D: 0.8, BenchmarkReturn20D: 0.3,
		})
	}

	// 三种写法的单日成本：整分区替换（回填 force=true 走这条）、按键覆盖、
	// 以及"同一天连写两次"（第二次必然全是覆盖路径）。
	measure := func(name string, fn func() error) {
		began := time.Now()
		if err := fn(); err != nil {
			t.Fatalf("%s 失败：%v", name, err)
		}
		elapsed := time.Since(began)
		t.Logf("%-28s %8s（%.1fµs/行）", name, elapsed.Round(time.Millisecond),
			float64(elapsed.Microseconds())/float64(rowCount))
	}

	measure("SaveStrength（空表）", func() error {
		return store.SaveStrength(20261009, "s1", rows)
	})
	measure("SaveStrength（覆盖已有）", func() error {
		return store.SaveStrength(20261009, "s1", rows)
	})
	measure("UpsertStrength（覆盖已有）", func() error {
		return store.UpsertStrength(20261009, "s1", rows)
	})
}

func codeN(n int) string {
	const digits = "0123456789"
	buf := []byte("000000")
	for i := 5; i >= 0 && n > 0; i-- {
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf)
}
