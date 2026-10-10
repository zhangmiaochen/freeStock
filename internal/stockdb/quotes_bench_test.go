package stockdb

import (
	"context"
	"os"
	"testing"
)

// 快速解码 vs 通用解码的基准（用**真实日k负载**）。
//
// 这条路径是冷取数的主要成本：全市场回填要解约 250 万条记录。
// 设 FREESTOCK_QUOTES_BENCH=1 才会去打数据源（先把负载取回来，再离线跑基准）。
func BenchmarkDecodeQuotes(b *testing.B) {
	if os.Getenv("FREESTOCK_QUOTES_BENCH") != "1" {
		b.Skip("要打 stockdb 取真实负载：设 FREESTOCK_QUOTES_BENCH=1 才跑")
	}
	client := NewClient(DefaultLocalURL)
	ctx := context.Background()

	buckets, err := client.StockCodes(ctx)
	if err != nil || len(buckets) == 0 {
		b.Skipf("stockdb 不可用：%v", err)
	}
	all := make([]string, 0, 8192)
	for _, list := range buckets {
		all = append(all, list...)
	}
	if len(all) == 0 {
		b.Skip("代码表为空")
	}
	latest, err := client.LatestTradeDate(ctx)
	if err != nil || latest <= 0 {
		b.Skipf("拿不到最新交易日：%v", err)
	}

	// 取一整年（约 250 个交易日）的真实负载，够接近回填要解的记录数
	start := latest - 10000 // 约一年前（年份差 1，日期仍是合法格式）
	if start < 19900101 {
		start = 19900101
	}
	payloads := make([][]byte, 0, 20)
	rows := 0
	for i := 0; i < len(all) && len(payloads) < 20; i++ {
		body, err := client.callRaw(ctx, "vals", TableDailyK, "key:"+all[i], rangeExpr(start, latest))
		if err != nil || len(body) == 0 {
			continue
		}
		quotes, err := DecodeQuotes(body)
		if err != nil || len(quotes) == 0 {
			continue
		}
		payloads = append(payloads, body)
		rows += len(quotes)
	}
	if len(payloads) == 0 {
		b.Skip("没取到可用负载")
	}
	b.Logf("负载：%d 份、%d 行（约 %d 字节/份）", len(payloads), rows, len(payloads[0]))

	b.Run("快速路径", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, payload := range payloads {
				if _, err := DecodeQuotes(payload); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	b.Run("通用路径", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, payload := range payloads {
				if _, err := decodeQuotesGeneric(payload); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
