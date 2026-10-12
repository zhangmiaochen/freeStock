package service

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
)

// **测量"没有回填缓存时"的取价成本** —— 这是用户在界面上点「回测」的真实路径。
//
// 背景：`TestBacktestPhaseTiming` 是**先回填再回测**，于是回测复用了回填
// 留下的内存日线缓存（日志里的"回测复用回填时的日线缓存"），取价只要 22 秒。
// 但用户直接点回测时**没有**那份缓存，`loadBacktestBars` 会对每只股票
// 单独发一次 HTTP，让 stockdb 服务端为该股组装**整段区间**的行情。
//
// 这个测试就是要量出那个差价：全市场、1 年、无缓存。
//
//	FREESTOCK_NOCACHE=1 [FREESTOCK_NOCACHE_DAYS=365] [FREESTOCK_BACKFILL_LIMIT=5000] \
//	    go test ./internal/service/ -run TestBacktestBarsWithoutCache -v -timeout 90m
func TestBacktestBarsWithoutCache(t *testing.T) {
	if os.Getenv("FREESTOCK_NOCACHE") != "1" {
		t.Skip("测量用：设 FREESTOCK_NOCACHE=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// ⚠ 数据源必须能切换。
	//
	// 我第一版按配置文件里的 `cnLakeRoot` 判断，得出了"用户用 stockdb"的结论 ——
	// **错了**（用户实际用数据湖）。而这两条取数路径的成本差一个量级：
	//   stockdb：`DailyHistory` 是服务端直接返回行情，很薄；
	//   数据湖：`DailyHistory` → `quoteRange` 要为**每根 K 线组装完整的
	//           stockdb.Quote**（估值/市值/股本/PE/PB/换手率…），实测 26.2µs/根。
	// 所以"回测慢"必须在**数据湖**上测，不能在 stockdb 上测。
	if strings.EqualFold(os.Getenv("FREESTOCK_NOCACHE_SOURCE"), "cnlake") {
		root := cnlake.Detect()
		if root == "" {
			t.Skip("没有数据湖：设 FREESTOCK_CNLAKE 或确认默认路径存在")
		}
		svc.stock.applySource(config.KindCnLake, root)
		t.Logf("数据源已切到数据湖：%s", root)
	}

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用：%v", err)
	}
	latest := int(universe.TradeDate)
	span := 365
	if v := os.Getenv("FREESTOCK_NOCACHE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			span = n
		}
	}
	start := shiftDays(t, latest, -span)

	limit := 5000
	if v := os.Getenv("FREESTOCK_BACKFILL_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	codes := make([]string, 0, limit)
	for _, item := range universe.Stocks {
		if len(codes) >= limit {
			break
		}
		codes = append(codes, item.Code)
	}
	if len(codes) == 0 {
		t.Skip("股票列表为空")
	}

	// 确认**没有**回填缓存可用（这正是用户点回测时的状态）
	provider, _ := svc.stock.snapshot()
	if cache := svc.backfillCacheFor(provider, start, latest); cache != nil {
		t.Logf("⚠ 存在回填缓存，这测不出用户的路径")
	} else {
		t.Logf("✓ 无回填缓存（与用户直接点回测一致）")
	}

	t.Logf("开始取价：%d 只 × %d 天（%d~%d）", len(codes), span, start, latest)
	began := time.Now()
	bars, err := svc.loadBacktestBars(codes, start, latest)
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("取价失败：%v", err)
	}
	t.Logf("取价完成：%d 根，耗时 %s（%.2f ms/只）",
		len(bars), elapsed.Round(time.Millisecond),
		float64(elapsed.Milliseconds())/float64(len(codes)))
}

var _ = portfolio.StrategyTrendGrowth
