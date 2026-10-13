package service

import (
	"math"
	"os"
	"testing"
	"time"
)

// **决策记录用探针**：把「不限起点补抓」（全量列表里最贵的一步）去掉，
// 板块行情的数字会差多少？
//
// 结论（本机数据湖实测）：**1265 个板块一个都不差** —— 补抓回来的 237 个标的
// 都是早已退市、不在任何板块成分里的老代码；唯一会变的是"重叠倍数"这个
// **全市场**口径（概念 45.57 → 47.51：分母里不再计入这 237 个没有当期行情的标的）。
//
// 所以"要不要为了快 4 秒而改掉那个重叠倍数"是产品决定，不是技术限制：
//
//	FREESTOCK_BOARDS_PERF=1 go test ./internal/service/ -run TestRetryImpactOnBoardNumbers -v -count=1
func TestRetryImpactOnBoardNumbers(t *testing.T) {
	if os.Getenv("FREESTOCK_BOARDS_PERF") != "1" {
		t.Skip("决策探针：设 FREESTOCK_BOARDS_PERF=1 才跑（要读数据湖）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("没有数据湖")
	}

	universe, err := svc.GetStockUniverse(true)
	if err != nil {
		t.Fatal(err)
	}
	boards, err := svc.loadBoards()
	if err != nil {
		t.Fatal(err)
	}
	tradeDate := int(universe.TradeDate)
	cut := shiftDate(tradeDate, -closesLookbackDays)

	build := func(skipStale bool) map[string]StockItem {
		quotes := make(map[string]StockItem, len(universe.Stocks))
		for _, item := range universe.Stocks {
			if !item.HasQuote {
				continue
			}
			// 「不限起点补抓」补回来的票：行情日期早于回看窗口
			if skipStale && item.Date > 0 && int(item.Date) < cut {
				continue
			}
			quotes[item.Code] = item
		}
		return quotes
	}

	full := build(false)
	without := build(true)
	t.Logf("有行情标的：现状 %d，去掉历史补抓后 %d（少 %d）",
		len(full), len(without), len(full)-len(without))

	for _, category := range []string{"概念", "eastmoney"} {
		count := 0
		for _, board := range boards {
			if board.Category == category {
				count++
			}
		}
		if count == 0 {
			continue
		}

		type agg struct {
			pctChg         float64
			pctChgEqual    float64
			amount         float64
			up, down, flat int
			quoted         int
			leaderDiff     float64
		}
		run := func(quotes map[string]StockItem) (map[string]agg, int, float64) {
			out := make(map[string]agg, count)
			memberTotal := 0
			for _, board := range boards {
				if board.Category != category {
					continue
				}
				memberTotal += len(board.Symbols)
				got := aggregateBoard(board, quotes)
				item := agg{
					pctChg: got.PctChg, pctChgEqual: got.PctChgEqual, amount: got.Amount,
					up: got.UpCount, down: got.DownCount, flat: got.FlatCount, quoted: got.QuotedCount,
				}
				if got.Leader != nil {
					item.leaderDiff = got.Leader.PctChg
				}
				out[board.Code] = item
			}
			ratio := 0.0
			if len(quotes) > 0 {
				ratio = round(float64(memberTotal)/float64(len(quotes)), 2)
			}
			return out, len(quotes), ratio
		}

		began := time.Now()
		before, _, ratioBefore := run(full)
		after, _, ratioAfter := run(without)
		t.Logf("== %s：%d 个板块（聚合两次 %s）", category, count, time.Since(began).Round(time.Millisecond))
		t.Logf("   重叠倍数：%.2f → %.2f（Overlapping %v → %v）",
			ratioBefore, ratioAfter, ratioBefore > 1.2, ratioAfter > 1.2)

		boardsDiff, maxPct, maxAmount, maxCount, leaderDiff := 0, 0.0, 0.0, 0, 0
		upBefore, upAfter := 0, 0
		for code, a := range before {
			b := after[code]
			changed := math.Abs(a.pctChg-b.pctChg) > 0.0001 ||
				math.Abs(a.amount-b.amount) > 1 ||
				a.up != b.up || a.down != b.down || a.flat != b.flat ||
				a.quoted != b.quoted
			if changed {
				boardsDiff++
			}
			maxPct = math.Max(maxPct, math.Abs(a.pctChg-b.pctChg))
			maxAmount = math.Max(maxAmount, math.Abs(a.amount-b.amount))
			for _, pair := range [][2]int{{a.up, b.up}, {a.down, b.down}, {a.flat, b.flat}, {a.quoted, b.quoted}} {
				if diff := absInt(pair[0] - pair[1]); diff > maxCount {
					maxCount = diff
				}
			}
			if math.Abs(a.leaderDiff-b.leaderDiff) > 0.0001 {
				leaderDiff++
			}
			if a.pctChg > 0 {
				upBefore++
			}
			if b.pctChg > 0 {
				upAfter++
			}
		}
		t.Logf("   有差异的板块 %d/%d（最大加权涨跌差 %.4f 个百分点，最大成交额差 %.0f，涨跌家数最大差 %d，领涨股变化 %d 个）",
			boardsDiff, count, maxPct, maxAmount, maxCount, leaderDiff)
		t.Logf("   上涨板块数：%d → %d", upBefore, upAfter)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
