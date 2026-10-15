package service

import (
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// TestProfileMatchesSnapshot 钉住"轻量画像"与"完整快照"在**行业聚合用到的字段**上完全一致。
//
// ## 为什么必须钉这一条
//
// 全市场行业基准对几千只标的走 `profileCompany`（零价格序列），
// 只有被评分的那批走 `snapshotCompany`（1400 个交易日的价格序列）。
// 两条路算出来的市值/PE/PB/PS 一旦有差异，行业加权 PE 与中位数就会失真 ——
// 而且是那种"只差一点点"的失真，界面上看不出来，只能靠断言挡。
//
// 覆盖的字段就是 `AggregateIndustry` 真正读的那些：
// MarketCap / FloatMarketCap / PE / PB / PS / NetProfit / Revenue / Equity / DebtRatio。
func TestProfileMatchesSnapshot(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)
	params := valuation.DefaultParams()

	data, err := svc.loadValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	// 抽一批有代表性的标的：大市值、小市值、不同行业
	sample := []string{"600519", "000001", "300750", "601899", "002415", "600036", "000858", "601318"}
	checked := 0
	for _, code := range sample {
		if _, ok := data.bars[code]; !ok {
			continue
		}
		profile, okProfile := svc.profileCompany(data, code, params)
		snapshot, okSnapshot := svc.snapshotCompany(data, code, params, true)
		if okProfile != okSnapshot {
			t.Errorf("%s 可用性不一致：profile=%v snapshot=%v", code, okProfile, okSnapshot)
			continue
		}
		if !okProfile {
			continue
		}

		a, b := profile.company, snapshot.company
		compare := func(field string, left, right float64) {
			// 相对容差 1e-9：两条路都是"不复权价 × 股本 ÷ 同期业绩"，
			// 差异只可能来自浮点结合律，不该有实质差别
			scale := 1.0
			if v := absFloat(left); v > scale {
				scale = v
			}
			if v := absFloat(right); v > scale {
				scale = v
			}
			if absFloat(left-right) > 1e-9*scale {
				t.Errorf("%s 的 %s 不一致：profile=%.6f snapshot=%.6f", code, field, left, right)
			}
		}
		compare("MarketCap", a.MarketCap, b.MarketCap)
		compare("FloatMarketCap", a.FloatMarketCap, b.FloatMarketCap)
		compare("PE", a.PE, b.PE)
		compare("PB", a.PB, b.PB)
		compare("PS", a.PS, b.PS)
		compare("NetProfit", a.NetProfit, b.NetProfit)
		compare("Revenue", a.Revenue, b.Revenue)
		compare("Equity", a.Equity, b.Equity)
		compare("DebtRatio", a.DebtRatio, b.DebtRatio)

		if a.IndustryCode != b.IndustryCode {
			t.Errorf("%s 的行业码不一致：%q vs %q", code, a.IndustryCode, b.IndustryCode)
		}
		if a.Name != b.Name {
			t.Errorf("%s 的名称不一致：%q vs %q", code, a.Name, b.Name)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有抽验到任何标的 —— 抽样列表与数据湖不符")
	}
	t.Logf("抽验 %d 只：市值 / 流通市值 / PE / PB / PS / 净利润 / 营收 / 净资产 / 负债率 / 行业码 / 名称 全部一致", checked)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
