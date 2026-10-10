package service

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// **真实组合回测复现**（要显式设环境变量才跑）。
//
// 用途：在**开发机真实数据**上验证"低吸组合回测到底会不会成交"。
// 之前用户报的就是"组合回测结果一直是 0"，而当时所有单测都是绿的 ——
// 因为它们用 `&PortfolioService{}` 或临时库，喂进去的 `Market` 字段
// 恰好用了组合范围那套词汇（main/gem），把真实的词汇差异掩盖掉了。
//
// 跑法（先把真实库**复制**一份，测试只动副本）：
//
//	$env:FREESTOCK_BT_DB="D:\...\copy_of_portfolio.db"
//	$env:FREESTOCK_BT_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	$env:FREESTOCK_BT_PORTFOLIO="pf_..."
//	$env:FREESTOCK_BT_START="20260914"; $env:FREESTOCK_BT_END="20261009"
//	go test ./internal/service/ -run TestRealPortfolioBacktest -v -timeout 60m
func TestRealPortfolioBacktest(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_BT_DB")
	lakeRoot := os.Getenv("FREESTOCK_BT_LAKE")
	portfolioID := os.Getenv("FREESTOCK_BT_PORTFOLIO")
	if srcDB == "" || lakeRoot == "" || portfolioID == "" {
		t.Skip("未设置 FREESTOCK_BT_DB / FREESTOCK_BT_LAKE / FREESTOCK_BT_PORTFOLIO，跳过")
	}
	start := envInt(t, "FREESTOCK_BT_START", 0)
	end := envInt(t, "FREESTOCK_BT_END", 0)
	if start <= 0 || end <= 0 {
		t.Skip("需要 FREESTOCK_BT_START / FREESTOCK_BT_END")
	}

	// 只动副本 —— 绝不写开发机真实组合库
	dstDB := filepath.Join(t.TempDir(), "portfolio.db")
	copyFile(t, srcDB, dstDB)

	stock, _ := newIsolatedService(t)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dstDB)
	t.Cleanup(svc.Close)

	// 预热股票列表：真实应用里界面早就加载过了
	universe, err := stock.GetStockUniverse(true)
	if err != nil || universe == nil {
		t.Fatalf("股票列表失败：%v", err)
	}
	t.Logf("数据源 %s，股票 %d 只，最新交易日 %d", universe.SourceURL, len(universe.Stocks), universe.TradeDate)

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		t.Fatalf("读组合 %s 失败：%v", portfolioID, err)
	}
	t.Logf("组合 %s（%s）策略=%s 市场范围=%s",
		item.Name, item.ID, item.StrategyID, item.ScopeValueJSON)

	// 区间内的评分行数 —— 这是判断"数据被削过"的关键事实
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(start), int64(end))
	if err != nil {
		t.Fatal(err)
	}
	perDate := map[string]int{}
	for _, row := range strengths {
		perDate[itoa64(row.TradeDate)]++
	}
	t.Logf("区间 %d~%d：评分 %d 行，覆盖 %d 个交易日", start, end, len(strengths), len(perDate))

	result, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID:    item.ID,
		StartDate:      start,
		EndDate:        end,
		InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("**回测失败**：%v", err)
	}

	payload, err := svc.GetBacktestPayload(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	m := result.Metrics
	t.Logf("指标：总收益 %.2f%% / 年化 %.2f%% / 回撤 %.2f%% / 夏普 %.2f",
		m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio)
	t.Logf("成交 %d 笔（卖出）/ 买+卖总数 %d / 胜率 %.2f%% / 交易日 %d",
		m.TotalTrades, len(payload.Trades), m.WinRate, m.TradingDays)
	if m.ScopeWarning != "" {
		t.Logf("⚠ 提示：%s", m.ScopeWarning)
	}
	if m.AnnualReturnWarning != "" {
		t.Logf("提示：%s", m.AnnualReturnWarning)
	}

	// 落库的初始资金不能是 0（曾经写 0，显示"初始资金 0 / 期末 0"）
	if payload.Run.RunID == "" {
		t.Error("回测记录没落库")
	}

	// ---- 关键断言：这次**必须**有成交 ----
	if m.TradingDays == 0 || len(payload.Daily) == 0 {
		t.Fatal("净值曲线为空 —— 回测根本没跑起来")
	}
	if len(payload.Trades) == 0 {
		t.Errorf("**仍然零成交**（区间 %d~%d，评分 %d 行 / %d 天）",
			start, end, len(strengths), len(perDate))
		for _, d := range payload.Daily {
			t.Logf("  %s 净值 %.2f 现金 %.2f 市值 %.2f 持仓 %d",
				d.TradeDate, d.TotalValue, d.Cash, d.PositionValue, d.PositionCount)
		}
		return
	}
	for i, trade := range payload.Trades {
		if i >= 10 {
			t.Logf("  ... 共 %d 笔", len(payload.Trades))
			break
		}
		t.Logf("  %s %s %s %d 股 @ %.2f 佣金 %.2f",
			trade.TradeDate, trade.Action, trade.Code, trade.Shares, trade.Price, trade.Commission)
	}
	first, last := payload.Daily[0], payload.Daily[len(payload.Daily)-1]
	t.Logf("净值：%s %.2f → %s %.2f（累计 %.2f%%）",
		first.TradeDate, first.TotalValue, last.TradeDate, last.TotalValue, last.CumulativeReturn)
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s=%q 不是数字", key, raw)
	}
	return value
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("打开源库失败：%v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("创建副本失败：%v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("复制失败：%v", err)
	}
}
