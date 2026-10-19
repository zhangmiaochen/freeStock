package service

import (
	"slices"
	"strings"
	"testing"

	"freestock/internal/stockdb"
)

func contains(results []StockSearchItem, code string) bool {
	for _, item := range results {
		if item.Code == code {
			return true
		}
	}
	return false
}

func TestPinyinVariants(t *testing.T) {
	cases := []struct {
		name         string
		wantFulls    []string
		wantInitials []string
	}{
		{"贵州茅台", []string{"guizhoumaotai"}, []string{"gzmt"}},
		// 「行」是多音字：xíng / háng，两种读法都要能搜到
		{"平安银行", []string{"pinganyinxing", "pinganyinhang"}, []string{"payx", "payh"}},
		{"宁德时代", []string{"ningdeshidai"}, []string{"ndsd"}},
		{"中芯国际", []string{"zhongxinguoji"}, []string{"zxgj"}},
	}
	for _, c := range cases {
		full, initials := pinyinVariants(c.name)
		fullSet := strings.Fields(full)
		initialSet := strings.Fields(initials)
		for _, want := range c.wantFulls {
			if !slices.Contains(fullSet, want) {
				t.Errorf("pinyinVariants(%q) 全拼 = %q，应包含 %q", c.name, full, want)
			}
		}
		for _, want := range c.wantInitials {
			if !slices.Contains(initialSet, want) {
				t.Errorf("pinyinVariants(%q) 首字母 = %q，应包含 %q", c.name, initials, want)
			}
		}
	}
	// 展示用取第一个读音
	if got := firstVariant("pinganyinxing pinganyinhang"); got != "pinganyinxing" {
		t.Errorf("firstVariant = %q", got)
	}
}

func TestMatchScore(t *testing.T) {
	entry := searchEntry{
		code:     "600519",
		name:     "贵州茅台",
		pinyin:   "guizhoumaotai",
		initials: "gzmt",
	}

	cases := []struct {
		keyword  string
		wantOn   string
		minScore int
	}{
		{"600519", "code", 100},
		{"6005", "code", 90},
		{"gzmt", "initials", 85},
		{"gz", "initials", 80},
		{"贵州茅台", "name", 75},
		{"茅台", "name", 70},
		{"guizhou", "pinyin", 60},
		{"zhou", "pinyin", 50},
	}
	for _, c := range cases {
		score, on := matchScore(entry, c.keyword)
		if score < c.minScore {
			t.Errorf("matchScore(%q) = %d (%s)，期望至少 %d", c.keyword, score, on, c.minScore)
		}
		if on != c.wantOn {
			t.Errorf("matchScore(%q) 命中方式 = %q，期望 %q", c.keyword, on, c.wantOn)
		}
	}
	if score, _ := matchScore(entry, "zzzz"); score != 0 {
		t.Errorf("无关关键字不应命中，得到 %d", score)
	}

	// 多读法：payh 与 payx 都要能命中平安银行
	bank := searchEntry{code: "000001", name: "平安银行", pinyin: "pinganyinxing pinganyinhang", initials: "payx payh"}
	for _, kw := range []string{"payx", "payh"} {
		if score, on := matchScore(bank, kw); score == 0 || on != "initials" {
			t.Errorf("matchScore(%q) 应命中首字母，得到 %d/%s", kw, score, on)
		}
	}
}

func TestSearchStocksAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	cases := []struct {
		keyword string
		want    string
		matchOn string
	}{
		{"600519", "600519", "code"},
		{"茅台", "600519", "name"},
		{"gzmt", "600519", "initials"},
		{"maotai", "600519", "pinyin"},
		{"000001", "000001", "code"},
		{"payh", "000001", "initials"},
	}
	for _, c := range cases {
		results, err := svc.SearchStocks(c.keyword, 10)
		if err != nil {
			t.Fatalf("SearchStocks(%q) 失败: %v", c.keyword, err)
		}
		if !contains(results, c.want) {
			t.Errorf("SearchStocks(%q) 没找到 %s，返回 %d 条", c.keyword, c.want, len(results))
			continue
		}
		t.Logf("SearchStocks(%-8q) -> %d 条，首条 %s %s (命中 %s)",
			c.keyword, len(results), results[0].Code, results[0].Name, results[0].MatchOn)
		if results[0].Code != c.want || results[0].MatchOn != c.matchOn {
			t.Errorf("SearchStocks(%q) 首条 = %s (命中 %s)，期望 %s (命中 %s)",
				c.keyword, results[0].Code, results[0].MatchOn, c.want, c.matchOn)
		}
	}
}

func TestGetKlineAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	bundle, err := svc.GetKline("600519", "qfq", 2020)
	if err != nil {
		t.Fatalf("GetKline 失败: %v", err)
	}

	t.Logf("代码=%s 名称=%s 周期数据=%d 根 除权记录=%d 条",
		bundle.Code, bundle.Name, len(bundle.Candles), len(bundle.Dividends))
	t.Logf("快照: 收盘=%.2f 涨跌=%.2f%% 市盈=%.2f 市净=%.2f 总市值=%.0f 股息率=%.2f%% 近12月每股分红=%.4f",
		bundle.Profile.Close, bundle.Profile.PctChg, bundle.Profile.PeTTM, bundle.Profile.PB,
		bundle.Profile.TotalMV, bundle.Profile.DividendYield, bundle.Profile.DividendTTM)

	if bundle.Name == "" {
		t.Error("没有取到股票名称")
	}
	if len(bundle.Candles) < 500 {
		t.Errorf("K 线数量偏少: %d", len(bundle.Candles))
	}
	// 必须按日期升序
	for i := 1; i < len(bundle.Candles); i++ {
		if bundle.Candles[i].Date <= bundle.Candles[i-1].Date {
			t.Fatalf("K 线未按日期升序: 第 %d 根 %d <= %d", i, bundle.Candles[i].Date, bundle.Candles[i-1].Date)
		}
	}
	if bundle.Profile.PeTTM <= 0 {
		t.Error("市盈率异常")
	}
	if bundle.Profile.TotalMV <= 0 {
		t.Error("总市值异常")
	}
	if len(bundle.Dividends) == 0 {
		t.Error("没有取到分红记录")
	}
	if bundle.Profile.DividendYield <= 0 {
		t.Errorf("股息率未计算出来: %.4f", bundle.Profile.DividendYield)
	}
	if bundle.Profile.LatestDivDate == 0 {
		t.Error("最近除权日缺失")
	}
}

// 复权应该让老价格向最新价格靠拢：前复权后，越早的价格被压得越低。
func TestAdjustCandles(t *testing.T) {
	quotes := []*stockdb.Quote{
		{Date: 20240102, Open: 10, High: 11, Low: 9, Close: 10},
		{Date: 20240603, Open: 20, High: 21, Low: 19, Close: 20},
		{Date: 20250106, Open: 30, High: 31, Low: 29, Close: 30},
	}
	factors := []stockdb.Factor{
		{Date: 20240102, Div: 0, Cum: 1.0},
		{Date: 20240603, Div: 1, Cum: 2.0},
	}

	raw := adjustCandles(quotes, factors, "none")
	if raw[0].Close != 10 || raw[2].Close != 30 {
		t.Errorf("不复权不应改动价格: %+v", raw)
	}

	qfq := adjustCandles(quotes, factors, "qfq")
	// f最新 = 2.0
	// 20240102: ratio = 2/1 = 2   -> 10/2  = 5
	// 20240603: ratio = 2/2 = 1   -> 20/1  = 20
	// 20250106: ratio = 2/2 = 1   -> 30/1  = 30
	if qfq[0].Close != 5 || qfq[1].Close != 20 || qfq[2].Close != 30 {
		t.Errorf("前复权结果不对: %v %v %v", qfq[0].Close, qfq[1].Close, qfq[2].Close)
	}

	hfq := adjustCandles(quotes, factors, "hfq")
	// 20240102: ratio = 1/1 = 1   -> 10
	// 20240603: ratio = 1/2 = 0.5 -> 40
	// 20250106: ratio = 1/2 = 0.5 -> 60
	if hfq[0].Close != 10 || hfq[1].Close != 40 || hfq[2].Close != 60 {
		t.Errorf("后复权结果不对: %v %v %v", hfq[0].Close, hfq[1].Close, hfq[2].Close)
	}
}

// 回归：复权只能在出口做一次。
//
// 之前的 bug：数据湖的 KlineHistory 已按全局口径（默认 hfq）复权，
// GetKline 又折算一次，于是"前复权"显示的是后复权价 ——
// 000969 实际 14.81 显示成 104.99，600519 实际 1258 显示成 11804。
func TestAdjustQuotesQfqAnchorsLatest(t *testing.T) {
	quotes := []*stockdb.Quote{
		{Date: 20240102, Open: 40, High: 41, Low: 39, Close: 40, PreClose: 40},
		{Date: 20250102, Open: 20, High: 21, Low: 19, Close: 20, PreClose: 20},
		{Date: 20260102, Open: 10, High: 11, Low: 9, Close: 10, PreClose: 10},
	}
	factors := []stockdb.Factor{
		{Date: 20240102, Cum: 1},
		{Date: 20250102, Cum: 2},
		{Date: 20260102, Cum: 4},
	}

	none := adjustQuotes(quotes, factors, "none")
	if none[0].Close != 40 || none[2].Close != 10 {
		t.Fatalf("不复权不应改动价格：%v %v", none[0].Close, none[2].Close)
	}

	// 前复权：最新一根必须等于原始价（这条就是本次 bug 的判据）
	qfq := adjustQuotes(quotes, factors, "qfq")
	for i, want := range []float64{10, 10, 10} {
		if qfq[i].Close != want {
			t.Fatalf("前复权第 %d 根应为 %.2f，得到 %.4f", i, want, qfq[i].Close)
		}
	}
	if qfq[2].Close != quotes[2].Close {
		t.Fatalf("前复权最新一根必须等于原始价：原始 %.4f，得到 %.4f", quotes[2].Close, qfq[2].Close)
	}
	if qfq[0].PreClose != 10 {
		t.Fatalf("昨收也要跟着折算：得到 %.4f", qfq[0].PreClose)
	}

	// 后复权：历史连续（保持同一水平），且高于原始价
	hfq := adjustQuotes(quotes, factors, "hfq")
	for i, want := range []float64{40, 40, 40} {
		if hfq[i].Close != want {
			t.Fatalf("后复权第 %d 根应为 %.2f，得到 %.4f", i, want, hfq[i].Close)
		}
	}
	if hfq[2].Close <= quotes[2].Close {
		t.Fatalf("后复权应高于原始价：原始 %.4f，得到 %.4f", quotes[2].Close, hfq[2].Close)
	}

	// 纯函数：不能改写入参（调用方可能复用同一份行情）
	if quotes[0].Close != 40 {
		t.Fatalf("adjustQuotes 修改了入参：%.4f", quotes[0].Close)
	}
}

func TestAdjustCandlesWithoutFactors(t *testing.T) {
	quotes := []*stockdb.Quote{{Date: 20240102, Open: 10, High: 11, Low: 9, Close: 10}}
	out := adjustCandles(quotes, nil, "qfq")
	if len(out) != 1 || out[0].Close != 10 {
		t.Errorf("没有复权因子时应保持原值: %+v", out)
	}
}
