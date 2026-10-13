package cnlake

import "testing"

// TestParseAdjMode 默认口径必须是**前复权**（空值与未知值都回落）。
func TestParseAdjMode(t *testing.T) {
	cases := map[string]AdjMode{
		"":        AdjHFQ,
		"qfq":     AdjQFQ,
		"QFQ":     AdjQFQ,
		" hfq ":   AdjHFQ,
		"none":    AdjNone,
		"raw":     AdjNone,
		"unknown": AdjHFQ,
	}
	for in, want := range cases {
		if got := ParseAdjMode(in); got != want {
			t.Errorf("ParseAdjMode(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestAdjScaleAt 覆盖阶梯查找与两种口径。
//
// points 取自真实数据（002594 的 1:3 送转）：2025-07-28 因子 1.037805，
// 2025-07-29 跳到 3.150567 —— 原始价由 337.00 变成 111.42。
func TestAdjScaleAt(t *testing.T) {
	points := []AdjFactorPoint{
		{Date: 19000101, Factor: 1.0},
		{Date: 20240729, Factor: 1.037805},
		{Date: 20250729, Factor: 3.150567},
	}
	anchor := points[len(points)-1].Factor // 3.150567

	// 后复权：乘当期因子
	if got := adjScaleAt(points, 20250728, AdjHFQ, anchor); got < 1.037 || got > 1.038 {
		t.Errorf("HFQ 20250728 = %v，期望 ≈1.037805", got)
	}
	// 前复权：除权日之后 = 1（锚定最新），之前 ≈ 1.037805/3.150567 ≈ 0.3294
	if got := adjScaleAt(points, 20250729, AdjQFQ, anchor); got < 0.9999 || got > 1.0001 {
		t.Errorf("QFQ 20250729 = %v，期望 1.0", got)
	}
	if got := adjScaleAt(points, 20250728, AdjQFQ, anchor); got < 0.329 || got > 0.330 {
		t.Errorf("QFQ 20250728 = %v，期望 ≈0.3294", got)
	}
	// 首点（因子 1.0）的前复权乘数 = 1.0/anchor ≈ 0.3174
	if got := adjScaleAt(points, 19000101, AdjQFQ, anchor); got < 0.317 || got > 0.318 {
		t.Errorf("首点 QFQ = %v，期望 ≈0.3174", got)
	}
	// none
	if got := adjScaleAt(points, 20250728, AdjNone, anchor); got != 1 {
		t.Errorf("None = %v，期望 1", got)
	}
	// 空序列
	if got := adjScaleAt(nil, 20250728, AdjQFQ, anchor); got != 1 {
		t.Errorf("空序列 = %v，期望 1", got)
	}
}

// TestAdjustBarsScalesPricesOnly 验证：
//  1. OHLC 被复权，Volume/Amount 不变（真实成交口径）；
//  2. **不修改入参**（批量缓存与按股票缓存共享底层数组，原地改写会污染缓存）；
//  3. 复权日之后前复权乘数为 1，除权跳空被抹掉（337.00 → 111.42 这条缺口消失）。
func TestAdjustBarsScalesPricesOnly(t *testing.T) {
	lake := &Lake{root: t.TempDir()}
	lake.adj = &adjFactorCache{
		loaded: true,
		index:  map[string]string{},
		byCode: map[string][]AdjFactorPoint{
			"002594": {
				{Date: 19000101, Factor: 1.0},
				{Date: 20250729, Factor: 3.0},
			},
		},
	}

	in := []BarRow{
		{
			Symbol: "002594.SZ", TradeDate: dateToDays(20250728),
			Open: 337.0, High: 340.0, Low: 336.0, Close: 337.0,
			Volume: 1000, Amount: 337000,
		},
	}
	before := in[0]

	got := lake.AdjustBars("002594", in, AdjQFQ)
	if len(got) != 1 {
		t.Fatalf("行数 = %d，期望 1", len(got))
	}
	// 前复权：2025-07-28 的乘数 = 1/3 → 337/3 ≈ 112.33
	if got[0].Close < 112.3 || got[0].Close > 112.4 {
		t.Errorf("QFQ close = %v，期望 ≈112.33", got[0].Close)
	}
	if got[0].Volume != 1000 || got[0].Amount != 337000 {
		t.Errorf("成交口径被改动：volume=%v amount=%v", got[0].Volume, got[0].Amount)
	}
	if in[0] != before {
		t.Errorf("入参被原地修改：%+v", in[0])
	}

	// 后复权：乘 1.0（该日因子为 1.0）
	hfq := lake.AdjustBars("002594", in, AdjHFQ)
	if hfq[0].Close != 337.0 {
		t.Errorf("HFQ close = %v，期望 337（该日因子 1.0）", hfq[0].Close)
	}

	// 没有因子的股票：原样返回（同一个切片也可以）
	none := lake.AdjustBars("600000", in, AdjQFQ)
	if len(none) != 1 || none[0].Close != 337.0 {
		t.Errorf("无因子股票被误改：%+v", none[0])
	}

	// none 模式：直接返回入参
	if raw := lake.AdjustBars("002594", in, AdjNone); len(raw) != 1 || raw[0].Close != 337.0 {
		t.Errorf("AdjNone 被误改：%+v", raw[0])
	}
}

// TestAdjFactorDir 目录约定：`<root>/meta/adj_factors_cache`。
func TestAdjFactorDir(t *testing.T) {
	lake := &Lake{root: `D:\lake`}
	want := `D:\lake\meta\adj_factors_cache`
	if got := lake.AdjFactorDir(); got != want {
		t.Errorf("AdjFactorDir = %q，期望 %q", got, want)
	}
}
