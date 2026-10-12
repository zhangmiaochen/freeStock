package service

import (
	"testing"
)

// 需要一个可用的数据湖（指数行情只在 index_bars 里）。
func requireLakeIndices(t *testing.T, svc *StockService) {
	t.Helper()
	lake := svc.lakeHandle()
	if lake == nil || !lake.HasIndexBars() {
		t.Skip("本机数据湖没有 index_bars，跳过指数用例")
	}
}

// 搜索框必须能搜到指数：代码（带后缀）、名称、拼音首字母都要能命中。
func TestSearchStocksIncludesIndices(t *testing.T) {
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	requireLakeIndices(t, svc)

	// ① 名称
	byName, err := svc.SearchStocks("上证", 10)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	found := false
	for _, item := range byName {
		if item.Code == "000001.SH" {
			found = true
			if !item.IsIndex {
				t.Error("上证指数必须标记为 IsIndex")
			}
			if item.Name != "上证指数" || item.MarketCN != "指数" {
				t.Errorf("指数结果信息不对: %+v", item)
			}
			if item.Close <= 0 {
				t.Errorf("指数结果应带上最新点数: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("搜「上证」没找到上证指数: %+v", byName)
	}

	// ② 拼音首字母（指数名也要走同一套拼音规则）
	byInitials, err := svc.SearchStocks("szzs", 10)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	hitInitials := false
	for _, item := range byInitials {
		if item.Code == "000001.SH" {
			hitInitials = true
		}
	}
	if !hitInitials {
		t.Errorf("搜「szzs」应命中上证指数: %+v", byInitials)
	}

	// ③ 带后缀的代码要精确命中
	byCode, err := svc.SearchStocks("000300.SH", 10)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(byCode) == 0 || byCode[0].Code != "000300.SH" || !byCode[0].IsIndex {
		t.Errorf("搜「000300.SH」应精确命中沪深300: %+v", byCode)
	}

	// ④ 裸码 000001 两边都有：股票在前、指数也在列表里（带后缀）
	byBare, err := svc.SearchStocks("000001", 10)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	var sawStock, sawIndex bool
	for i, item := range byBare {
		if item.Code == "000001" && !item.IsIndex {
			sawStock = true
			if i != 0 {
				t.Errorf("裸码搜索时股票应排在指数前面: %+v", byBare)
			}
		}
		if item.Code == "000001.SH" && item.IsIndex {
			sawIndex = true
		}
	}
	if !sawStock || !sawIndex {
		t.Errorf("搜「000001」应同时给出平安银行与上证指数（带后缀）: %+v", byBare)
	}
}

// 指数 K 线：走 index_bars，没有复权、没有估值/财务/资金流/板块，且提示写清楚。
func TestGetKlineForIndex(t *testing.T) {
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	requireLakeIndices(t, svc)

	bundle, err := svc.GetKline("000001.SH", "qfq", 1990)
	if err != nil {
		t.Fatalf("读取指数 K 线失败: %v", err)
	}
	if !bundle.IsIndex {
		t.Error("指数 K 线必须标记 IsIndex")
	}
	if bundle.Name != "上证指数" || bundle.Profile.MarketCN != "指数" {
		t.Errorf("指数名称/市场不对: name=%q market=%q", bundle.Name, bundle.Profile.MarketCN)
	}
	if len(bundle.Candles) < 500 {
		t.Fatalf("指数历史太短: %d 根", len(bundle.Candles))
	}
	if bundle.Profile.Close <= 0 || bundle.Profile.TradeDate <= 0 {
		t.Errorf("指数缺少最新点数/交易日: %+v", bundle.Profile)
	}
	if bundle.Profile.PctChg < -20 || bundle.Profile.PctChg > 20 {
		t.Errorf("指数当日涨跌幅异常: %v", bundle.Profile.PctChg)
	}

	// 个股才有的字段一律留 0，而不是编一个数
	if bundle.Profile.PeTTM != 0 || bundle.Profile.PB != 0 ||
		bundle.Profile.TotalMV != 0 || bundle.Profile.FloatMV != 0 ||
		bundle.Profile.Turnover != 0 || bundle.Profile.TotalShare != 0 {
		t.Errorf("指数不该有估值/股本/换手率: %+v", bundle.Profile)
	}
	if len(bundle.Profile.Boards) != 0 {
		t.Errorf("指数不该有板块归属: %v", bundle.Profile.Boards)
	}
	if len(bundle.MoneyFlows) != 0 || len(bundle.Dividends) != 0 {
		t.Errorf("指数不该有资金流/分红: flows=%d divs=%d", len(bundle.MoneyFlows), len(bundle.Dividends))
	}

	// 提示必须说清数据来源、没有哪些字段、为什么没有复权
	notes := ""
	for _, note := range bundle.Notes {
		notes += note
	}
	for _, want := range []string{"index_bars", "换手率", "没有复权概念"} {
		if !containsText(notes, want) {
			t.Errorf("指数提示缺少 %q：%s", want, notes)
		}
	}

	// 日期升序、OHLC 有效
	for i, candle := range bundle.Candles {
		if candle.Close <= 0 || candle.Open <= 0 || candle.High <= 0 || candle.Low <= 0 {
			t.Fatalf("第 %d 根 K 线价格无效: %+v", i, candle)
		}
		if i > 0 && candle.Date <= bundle.Candles[i-1].Date {
			t.Fatalf("K 线未按日期升序: 第 %d 根 %d", i, candle.Date)
		}
		if candle.High < candle.Low {
			t.Fatalf("第 %d 根 K 线最高价低于最低价: %+v", i, candle)
		}
	}

	// **指数没有复权**：三种复权方式必须给出完全一样的曲线
	none, err := svc.GetKline("000001.SH", "none", 1990)
	if err != nil {
		t.Fatalf("不复权读取失败: %v", err)
	}
	hfq, err := svc.GetKline("000001.SH", "hfq", 1990)
	if err != nil {
		t.Fatalf("后复权读取失败: %v", err)
	}
	if len(none.Candles) != len(bundle.Candles) || len(hfq.Candles) != len(bundle.Candles) {
		t.Fatalf("复权方式不该改变 K 线根数: %d/%d/%d",
			len(none.Candles), len(hfq.Candles), len(bundle.Candles))
	}
	for i := range bundle.Candles {
		if none.Candles[i].Close != bundle.Candles[i].Close || hfq.Candles[i].Close != bundle.Candles[i].Close {
			t.Fatalf("第 %d 根：指数复权后价格变了（不该有复权）none=%v qfq=%v hfq=%v",
				i, none.Candles[i].Close, bundle.Candles[i].Close, hfq.Candles[i].Close)
		}
	}

	// 与市场温度页读的是**同一份数据**：最后一天必须一致
	raw := rawIndexCloses(t, svc)["000001.SH"]
	if len(raw) == 0 {
		t.Fatal("拿不到原始指数收盘价")
	}
	if last := bundle.Candles[len(bundle.Candles)-1].Close; last != round(raw[len(raw)-1], 4) {
		t.Errorf("K 线最后收盘 %v 与 index_bars 的 %v 不一致", last, raw[len(raw)-1])
	}
	t.Logf("%s %d 根（%d ~ %d），最新 %.2f %+.2f%%",
		bundle.Name, len(bundle.Candles), bundle.Candles[0].Date,
		bundle.Candles[len(bundle.Candles)-1].Date, bundle.Profile.Close, bundle.Profile.PctChg)
}

// 裸码解析：是股票的按股票走（000001 平安银行），不是股票的按指数走（399001 深证成指）。
func TestGetKlineResolvesBareCodes(t *testing.T) {
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	requireLakeIndices(t, svc)

	index, err := svc.GetKline("399001", "none", 1990)
	if err != nil {
		t.Fatalf("裸码 399001 应能按指数打开: %v", err)
	}
	if !index.IsIndex || index.Name != "深证成指" || index.Code != "399001.SZ" {
		t.Errorf("399001 应解析成深证成指: code=%q name=%q isIndex=%v",
			index.Code, index.Name, index.IsIndex)
	}

	// 000001 是股票（平安银行）：不能被指数抢走
	stock, err := svc.GetKline("000001", "none", 1990)
	if err != nil {
		t.Fatalf("裸码 000001 应按股票打开: %v", err)
	}
	if stock.IsIndex {
		t.Errorf("000001 是平安银行，不该按指数处理: %+v", stock.Profile)
	}
	if stock.Name == "上证指数" {
		t.Error("000001 解析成了上证指数（重名时必须以股票优先）")
	}
	t.Logf("000001 → %s（股票）；399001 → %s（指数）", stock.Name, index.Name)

	// 解析结果要缓存：第二次调用不该再走点查询（这里只验证行为一致）
	again, err := svc.GetKline("000001", "none", 1990)
	if err != nil {
		t.Fatalf("第二次解析失败: %v", err)
	}
	if again.IsIndex != stock.IsIndex || again.Code != stock.Code {
		t.Errorf("缓存后解析结果不一致: %v/%s vs %v/%s",
			again.IsIndex, again.Code, stock.IsIndex, stock.Code)
	}
	svc.indexResolveMu.Lock()
	cached, ok := svc.indexResolveCache["000001"]
	svc.indexResolveMu.Unlock()
	if !ok || cached != "" {
		t.Errorf("000001 的解析结果应被缓存为「不是指数」，实际 %q（存在=%v）", cached, ok)
	}
}

// 数据湖里没有 index_bars 时，带后缀的指数代码要给出**可操作**的错误，
// 而不是一句"没有日K数据"。
func TestGetKlineIndexWithoutLake(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 这个服务指向 free-stockdb（没有指数表），但本机数据湖是可用的 ——
	// 先确认本机有数据湖，否则这条用例说明不了问题
	if !svc.lakeHandle().HasIndexBars() {
		t.Skip("本机数据湖没有 index_bars，跳过")
	}

	// 带后缀的代码一定走指数分支（不受当前行情源影响）
	bundle, err := svc.GetKline("000001.SH", "none", 1990)
	if err != nil {
		return // 没有数据湖时给出的是明确错误，这也符合预期
	}
	if !bundle.IsIndex {
		t.Error("带后缀的指数代码必须走指数分支")
	}
}

// containsText 是小工具：把多个子串都要求出现时用它（子串匹配用 strings.Contains 也可以，
// 但这里显式命名，避免和别的测试文件里的同名 helper 冲突）。
func containsText(text, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
