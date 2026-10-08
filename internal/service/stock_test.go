package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/market"
	"freestock/internal/stockdb"
)

// newLocalService 返回一个指向本机 free-stockdb 的服务实例。
func newLocalService() *StockService {
	svc := NewStockService(config.NewStore())
	svc.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	return svc
}

// newLakeService 返回一个把行情源指向数据湖的服务实例；没有数据湖时返回 nil。
func newLakeService(t *testing.T) *StockService {
	t.Helper()
	lake := cnlake.Open("")
	if !lake.Available() {
		return nil
	}
	svc := NewStockService(config.NewStore())
	svc.applySource(config.KindCnLake, lake.Root())
	return svc
}

// requireLocalServer 在本机数据库不可用时跳过用例，避免 CI 误报。
func requireLocalServer(t *testing.T, svc *StockService) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := stockdb.NewClient(stockdb.DefaultLocalURL).Ping(ctx); err != nil {
		t.Skipf("本机 free-stockdb (%s) 不可用，跳过集成用例: %v", stockdb.DefaultLocalURL, err)
	}
}

// newIsolatedService 返回配置文件被隔离在临时目录的服务实例，
// 供需要读写配置的用例使用 —— 不能污染开发机上真实的配置文件。
func newIsolatedService(t *testing.T) (*StockService, *config.Store) {
	t.Helper()
	store := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	svc := NewStockService(store)
	svc.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	return svc, store
}

func TestStockUniverseFromLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	universe, err := svc.GetStockUniverse(false)
	if err != nil {
		t.Fatalf("GetStockUniverse 失败: %v", err)
	}

	t.Logf("数据源=%s 交易日=%d 总数=%d 有行情=%d 耗时=%s",
		universe.SourceURL, universe.TradeDate, universe.Total, universe.WithQuote, universe.Elapsed)

	if universe.Total < 5000 {
		t.Errorf("股票总数偏少: %d", universe.Total)
	}
	if universe.TradeDate < 20000101 {
		t.Errorf("交易日异常: %d", universe.TradeDate)
	}
	if len(universe.Stocks) != universe.Total {
		t.Errorf("Stocks 数量 %d 与 Total %d 不一致", len(universe.Stocks), universe.Total)
	}
	if universe.WithQuote == 0 {
		t.Error("没有任何股票取到行情")
	}

	// 市场计数必须与市场列表自洽
	sum := 0
	for _, m := range universe.Markets {
		if m.Name == "" || m.Count <= 0 {
			t.Errorf("市场信息异常: %+v", m)
		}
		sum += m.Count
	}
	if sum != universe.Total {
		t.Errorf("市场计数合计 %d 与总数 %d 不一致", sum, universe.Total)
	}
	if len(universe.Markets) < 5 {
		t.Errorf("市场分类数量偏少: %d", len(universe.Markets))
	}

	// 每一只股票都应该能判定市场，且代码为 6 位
	for _, s := range universe.Stocks {
		if len(s.Code) != 6 {
			t.Fatalf("代码格式异常: %q", s.Code)
		}
		if s.MarketCN == "" || s.MarketCN == market.KeyUnknown {
			t.Fatalf("代码 %s 无法判定市场 (market=%s)", s.Code, s.Market)
		}
		// 取名成功率是衡量抓取质量的关键指标
		if s.HasQuote && s.Name == "" {
			t.Fatalf("代码 %s 有行情但没有名称", s.Code)
		}
	}

	ratio := float64(universe.WithQuote) / float64(universe.Total)
	t.Logf("行情覆盖率 = %.2f%%", ratio*100)
	if ratio < 0.85 {
		t.Errorf("行情覆盖率过低: %.2f%%", ratio*100)
	}
}

func TestBoardsFromLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	categories, err := svc.GetBoardCategories()
	if err != nil {
		t.Fatalf("GetBoardCategories 失败: %v", err)
	}
	byKey := map[string]BoardCategoryInfo{}
	for _, c := range categories {
		byKey[c.Key] = c
	}
	t.Logf("行业分类: %+v", categories)

	for _, want := range []string{"申万一级", "申万二级", "申万三级", "概念"} {
		if _, ok := byKey[want]; !ok {
			t.Errorf("缺少行业分类 %q", want)
		}
	}
	if c, ok := byKey["申万一级"]; ok && c.Count < 20 {
		t.Errorf("申万一级板块数量偏少: %d", c.Count)
	}

	boards, err := svc.GetBoards("申万一级")
	if err != nil {
		t.Fatalf("GetBoards 失败: %v", err)
	}
	if len(boards) != byKey["申万一级"].Count {
		t.Errorf("GetBoards 返回 %d 个板块，与分类统计 %d 不一致", len(boards), byKey["申万一级"].Count)
	}

	withSymbols := 0
	for _, b := range boards {
		if b.Name == "" || b.Code == "" {
			t.Errorf("板块信息不完整: %+v", b)
		}
		if len(b.Symbols) > 0 {
			withSymbols++
		}
	}
	if withSymbols == 0 {
		t.Error("没有任何板块包含成分股")
	}
	t.Logf("申万一级板块 %d 个，其中 %d 个含成分股，样例: %s(%d 只)",
		len(boards), withSymbols, boards[0].Name, len(boards[0].Symbols))
}

func TestMarketClassification(t *testing.T) {
	cases := map[string]string{
		"600000":    market.KeySHMain,
		"601398":    market.KeySHMain,
		"603633":    market.KeySHMain,
		"605001":    market.KeySHMain,
		"688981":    market.KeySTAR,
		"000001":    market.KeySZMain,
		"002594":    market.KeySZMain,
		"300750":    market.KeyChiNext,
		"301029":    market.KeyChiNext,
		"920001":    market.KeyBSE,
		"159915":    market.KeySZFund,
		"510300":    market.KeySHFund,
		"600633.SH": market.KeySHMain,
	}
	for code, want := range cases {
		if got := market.KeyOf(code); got != want {
			t.Errorf("KeyOf(%q) = %q, 期望 %q", code, got, want)
		}
	}
}

func TestShiftDate(t *testing.T) {
	if got := shiftDate(20260930, -45); got != 20260816 {
		t.Errorf("shiftDate(20260930,-45) = %d, 期望 20260816", got)
	}
	if got := shiftDate(20260101, -1); got != 20251231 {
		t.Errorf("shiftDate(20260101,-1) = %d, 期望 20251231", got)
	}
}
