package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 评分指纹：把**真实数据**上算出的评分逐字段摘成一个 sha256。
//
// 用途：改动"指标 / 分位 / 修复能力"这类实现时，用它做**改动前后 A/B** ——
// 只要有一位不同就说明数据结果变了，改动不能要。
//
// 跑法：
//
//	FREESTOCK_DIGEST=1 go test ./internal/service/ -run TestScoreDigestRealData -v
//	# 记录指纹
//	FREESTOCK_DIGEST=1 FREESTOCK_DIGEST_EXPECT=<指纹> go test ...   # 复核
//
// 规模：FREESTOCK_DIGEST_CODES（默认 400）、FREESTOCK_DIGEST_DAYS（默认 5）。
//
// ⚠ 指纹**依赖于数据源当天的最新交易日** —— 换了日子（新 K 线入库）指纹就会变，
// 这是预期的。所以它只适合"同一台机器、同一天、改动前后"的对比。
func TestScoreDigestRealData(t *testing.T) {
	if os.Getenv("FREESTOCK_DIGEST") != "1" {
		t.Skip("评分指纹（要打数据源）：设 FREESTOCK_DIGEST=1 才跑")
	}
	codeCount := envIntOr("FREESTOCK_DIGEST_CODES", 400)
	dayCount := envIntOr("FREESTOCK_DIGEST_DAYS", 5)

	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	store := mustStore(t, svc)
	codes, _, err := svc.scoringUniverse(store, codeCount)
	if err != nil || len(codes) == 0 {
		t.Skipf("拿不到评分范围：%v", err)
	}
	latest, err := provider.LatestTradeDate(t.Context())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	dates, err := svc.tradingDates(shiftDays(t, latest, -dayCount*3-10), latest)
	if err != nil || len(dates) == 0 {
		t.Skipf("交易日不足：%v", err)
	}
	if len(dates) > dayCount {
		dates = dates[len(dates)-dayCount:]
	}

	// 与生产路径同一套缓存（区间缓存不改变结果，这里只是快）
	cache := newRangeCachingProvider(provider,
		scoreWindowStart(dates[0], scoreLookbackTradingDays), dates[len(dates)-1])
	scoring := NewScoringService(svc.stock)
	scoring.override = cache

	hash := sha256.New()
	rowCount := 0
	for _, strategyID := range []string{
		score.StrategyTrendGrowth, score.StrategyOversold,
		score.StrategyExpert, score.StrategyTrendLowBuy,
	} {
		for _, date := range dates {
			result, err := scoring.ScoreStocks(codes, date, 0, strategyID)
			if err != nil {
				t.Fatalf("%s %d 评分失败：%v", strategyID, date, err)
			}
			rows := result.Rows
			// 行顺序本身不参与指纹（并发 append 后已排序，但这里再排一次更稳）
			sort.Slice(rows, func(i, j int) bool { return rows[i].Code < rows[j].Code })
			for _, row := range rows {
				fmt.Fprintf(hash, "%s|%d|%s\n", strategyID, date, digestRow(row))
				rowCount++
			}
			fmt.Fprintf(hash, "%s|%d|%s\n", strategyID, date, digestMarketState(result))

			// **落库再读回来**：这样指纹覆盖的就不只是计算结果，还有
			// 行业/市场归属（saveScoringResult）与写入本身（SaveStrength 分批）。
			if err := svc.saveScoringResult(store, result, strategyID); err != nil {
				t.Fatalf("%s %d 落库失败：%v", strategyID, date, err)
			}
			stored, err := store.LoadStrength(int64(date), strategyID, 0)
			if err != nil {
				t.Fatalf("%s %d 读回失败：%v", strategyID, date, err)
			}
			sort.Slice(stored, func(i, j int) bool {
				if stored[i].TotalScore != stored[j].TotalScore {
					return stored[i].TotalScore > stored[j].TotalScore
				}
				return stored[i].Code < stored[j].Code
			})
			for _, row := range stored {
				fmt.Fprintf(hash, "%s|%d|stored|%s\n", strategyID, date, digestStoredRow(row))
			}
			state, err := store.LoadMarketState(int64(date))
			if err != nil {
				t.Fatalf("%s %d 读市场状态失败：%v", strategyID, date, err)
			}
			fmt.Fprintf(hash, "%s|%d|state|%s|%s|%.17g|%.17g|%.17g|%d\n",
				strategyID, date, state.State, state.Label,
				state.BullScore, state.BearScore, state.ProxyRet20, state.Days)
		}
	}

	got := hex.EncodeToString(hash.Sum(nil))
	t.Logf("规模 %d 只 × %d 天 × 4 策略，%d 行（含落库回读）；指纹 %s",
		len(codes), len(dates), rowCount, got)

	if want := os.Getenv("FREESTOCK_DIGEST_EXPECT"); want != "" && !strings.EqualFold(want, got) {
		t.Fatalf("评分指纹变了！\n  改动前 %s\n  改动后 %s", want, got)
	}
}

// digestStoredRow 把落库回读的一行全部字段展开（含行业/市场这两列，
// 它们由 saveScoringResult 填，不在 ScoreStocks 的结果里）。
func digestStoredRow(row portfolio.StrengthRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%d|%s",
		row.Code, row.Name, row.Industry, row.Market, row.TradeDate, row.StrategyID)
	for _, v := range []float64{
		row.TotalScore, row.TrendScore, row.StructureScore, row.RSScore,
		row.CapitalScore, row.RepairScore,
		row.Close, row.High, row.Low, row.Open, row.PctChg, row.Amount,
		row.Ma5, row.Ma20, row.Ma30, row.Ma60, row.Ma120, row.Ma250,
		row.RelativeStrength20D, row.DrawdownFromHigh20, row.DrawdownFromHigh120,
		row.ReboundFromLow60, row.StockReturn3D, row.StockReturn5D,
		row.StockReturn10D, row.StockReturn20D,
		row.VolumeRatio20, row.VolumeRatio520, row.Ma5Prev, row.Ma250Prev,
		row.IndustryReturn20D, row.BenchmarkReturn20D,
	} {
		fmt.Fprintf(&b, "|%.17g", v)
	}
	return b.String()
}

// digestRow 把一行的全部字段按高精度展开（%.17g 保证浮点逐位可比）。
func digestRow(row score.Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", row.Code, row.Name, row.Market)
	for _, v := range []float64{
		row.TotalScore, row.TrendScore, row.StructureScore, row.RSScore,
		row.CapitalScore, row.RepairScore,
		row.Close, row.High, row.Low, row.Open, row.Amount, row.PctChg,
		row.Ma5, row.Ma20, row.Ma30, row.Ma60, row.Ma120, row.Ma250,
		row.RelativeStrength20D, row.DrawdownFromHigh20, row.DrawdownFromHigh120,
		row.ReboundFromLow60, row.StockReturn3D, row.StockReturn5D,
		row.StockReturn10D, row.StockReturn20D,
		row.VolumeRatio20, row.VolumeRatio520, row.Ma5Prev, row.Ma250Prev,
		row.IndustryReturn20D, row.BenchmarkReturn20D,
	} {
		fmt.Fprintf(&b, "|%.17g", v)
	}
	return b.String()
}

func digestMarketState(result *ScoringResult) string {
	state := result.MarketState
	return fmt.Sprintf("%s|%s|%.17g|%.17g|%.17g|%d",
		state.State, state.Label, state.BullScore, state.BearScore, state.ProxyRet20, state.Days)
}
