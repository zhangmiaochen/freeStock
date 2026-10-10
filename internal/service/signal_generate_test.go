package service

import (
	"testing"

	"freestock/internal/score"
	"freestock/internal/volprice"
)

// 信号生成里的纯函数。
//
// 重点是 `tsCodeOf`：信号表要存**原项目的 tushare 格式**（`600519.SH`），
// 而评分侧用裸 6 位码。这一层转换错了，写进表的代码会被
// `portfolio.SignalCodeAllowed` 全部挡掉 —— 表看着写成功了，attach 却一条都读不到。
func TestTsCodeOf(t *testing.T) {
	cases := map[string]string{
		"600519":    "600519.SH",
		"601318":    "601318.SH",
		"688981":    "688981.SH",
		"900901":    "900901.SH", // 9 开头算沪市（与原实现一致）
		"000001":    "000001.SZ",
		"002594":    "002594.SZ",
		"300750":    "300750.SZ",
		"600519.SH": "600519.SH", // 已带后缀则原样返回
		"000001.SZ": "000001.SZ",
		"430047.BJ": "430047.BJ",
		"12345":     "12345", // 长度不对 → 原样返回，交给白名单去挡
		"":          "",
	}
	for input, want := range cases {
		if got := tsCodeOf(input); got != want {
			t.Errorf("tsCodeOf(%q)：go %q，期望 %q", input, got, want)
		}
	}
}

// 每个模型要的回溯天数不同（对应 `_load_stock_bars` 的 420 / 220）。
func TestSignalLookbackForModel(t *testing.T) {
	if got := signalLookbackForModel("trend_pullback"); got != 420 {
		t.Errorf("trend_pullback 回溯应为 420，实际 %d", got)
	}
	for _, model := range []string{"box_range", "oversold_rebound"} {
		if got := signalLookbackForModel(model); got != 220 {
			t.Errorf("%s 回溯应为 220，实际 %d", model, got)
		}
	}
	// 别名也要归一化后再判断
	if got := signalLookbackForModel("trend"); got != 420 {
		t.Errorf("别名 trend 应归一化为 trend_pullback（420），实际 %d", got)
	}
}

// K 线转换：日期从 int64 变成 8 位字符串，OHLCV 原样。
func TestToVolPriceBars(t *testing.T) {
	bars := []score.Bar{
		{Date: 20261009, Open: 10.1, High: 10.5, Low: 10.0, Close: 10.4, Volume: 1234},
		{Date: 20261012, Open: 10.4, High: 10.9, Low: 10.3, Close: 10.8, Volume: 2345},
	}
	got := toVolPriceBars(bars)
	if len(got) != 2 {
		t.Fatalf("条数：%d", len(got))
	}
	if got[0].Date != "20261009" || got[1].Date != "20261012" {
		t.Errorf("日期转换错误：%q / %q", got[0].Date, got[1].Date)
	}
	if got[1].Close != 10.8 || got[1].Volume != 2345 {
		t.Errorf("OHLCV 未透传：%+v", got[1])
	}
}

// 出表的代码必须是 tushare 格式，价格的可空语义要保留。
func TestToPortfolioSignalRows(t *testing.T) {
	entry, stop, target := 10.4, 9.8, 12.0
	rows := []volprice.SignalHistoryRow{{
		TradeDate: 20261009, Name: "贵州茅台",
		ModelID: "trend_pullback", ModelName: "趋势缩量回踩",
		MarketState: "bull", MarketStateLabel: "牛市",
		SignalType: "buy", SignalLevel: "confirmed", SignalLevelLabel: "确认",
		EntryPrice: &entry, StopPrice: &stop, TargetPrice: &target,
		Score: 88.0, RiskLevel: "low",
		BehaviorQualityJSON: "{}", ReasonJSON: "{}", CreatedAt: "t", UpdatedAt: "t",
	}}
	got := toPortfolioSignalRows("600519", rows)
	if len(got) != 1 {
		t.Fatalf("条数：%d", len(got))
	}
	if got[0].Code != "600519.SH" {
		t.Errorf("代码应为 tushare 格式 600519.SH，实际 %q", got[0].Code)
	}
	if got[0].EntryPrice == nil || *got[0].EntryPrice != 10.4 {
		t.Errorf("entry_price 未透传：%v", got[0].EntryPrice)
	}
	if got[0].TradeDate != 20261009 || got[0].SignalType != "buy" || got[0].Score != 88 {
		t.Errorf("关键字段未透传：%+v", got[0])
	}

	// 可空价格：nil 要保持 nil（对应 NULL），不能变成 0
	empty := toPortfolioSignalRows("600519", []volprice.SignalHistoryRow{{TradeDate: 20261009}})
	if empty[0].EntryPrice != nil || empty[0].StopPrice != nil || empty[0].TargetPrice != nil {
		t.Errorf("缺失价格应为 nil（NULL），实际 %v/%v/%v",
			empty[0].EntryPrice, empty[0].StopPrice, empty[0].TargetPrice)
	}
}

func TestAtoiDate(t *testing.T) {
	if got := atoiDate("20261009"); got != 20261009 {
		t.Errorf("atoiDate 正常路径：%d", got)
	}
	for _, bad := range []string{"", "2026-10-09", "2026abcd", "2026100a"} {
		if got := atoiDate(bad); got != 0 {
			t.Errorf("atoiDate(%q) 应返回 0，实际 %d", bad, got)
		}
	}
}
