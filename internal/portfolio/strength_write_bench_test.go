package portfolio

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// 评分落库的写入成本 —— 优化完指标之后，这是回填单日里最大的一块。
//
// 实测（全市场 7600 只 × 12 天，热缓存）：ScoreStocks 69ms/天，
// 而 `saveScoringResult`（DELETE + 7600 行 INSERT，39 列）127~215ms/天。
//
// 两种场景都要量：
//   - 新交易日（回填首跑）：DELETE 删不到东西
//   - 覆盖已有交易日（重跑）：DELETE 要删掉 7600 行
func BenchmarkSaveStrength(b *testing.B) {
	for _, size := range []int{1000, 7600} {
		for _, overwrite := range []bool{false, true} {
			name := fmt.Sprintf("rows=%d/overwrite=%v", size, overwrite)
			b.Run(name, func(b *testing.B) {
				store, err := Open(filepath.Join(b.TempDir(), "bench.db"))
				if err != nil {
					b.Fatal(err)
				}
				defer store.Close()

				rows := benchStrengthRows(size)
				const tradeDate = int64(20261009)
				if overwrite {
					if err := store.SaveStrength(tradeDate, StrategyTrendGrowth, rows); err != nil {
						b.Fatal(err)
					}
				}
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					date := tradeDate
					if !overwrite {
						// 每次换一个交易日：模拟"回填新的一天"
						date = tradeDate + int64(i)
					}
					if err := store.SaveStrength(date, StrategyTrendGrowth, rows); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkLoadStrengthRange 顺带量一下读（回测的主要读路径）。
func BenchmarkLoadStrengthRange(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	rows := benchStrengthRows(7600)
	for day := 0; day < 20; day++ {
		if err := store.SaveStrength(int64(20260101+day*7), StrategyTrendGrowth, rows); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := store.LoadStrengthRange(StrategyTrendGrowth, 20260101, 20261231)
		if err != nil {
			b.Fatal(err)
		}
		if len(got) == 0 {
			b.Fatal("读不到数据")
		}
	}
	_ = context.Background()
}

func benchStrengthRows(n int) []StrengthRow {
	rows := make([]StrengthRow, n)
	for i := range rows {
		rows[i] = StrengthRow{
			Code:   fmt.Sprintf("%06d", 600000+i),
			Name:   "基准股票",
			Market: "main",
			// 分数带小数，避免"纯整数压缩"这类不真实的优化
			TotalScore: 70 + float64(i%3000)/100,
			Close:      10 + float64(i%900)/7, Amount: 1e7 + float64(i)*1e3,
			Ma5: 9.9, Ma20: 9.8, Ma30: 9.7, Ma60: 9.6, Ma120: 9.5, Ma250: 9.4,
			RelativeStrength20D: 3.3, DrawdownFromHigh120: -12.5,
			IndustryReturn20D: 1.25, BenchmarkReturn20D: 0.75,
		}
	}
	return rows
}
