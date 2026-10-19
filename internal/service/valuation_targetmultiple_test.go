package service

import (
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
)

// 本文件钉住"目标倍数必须锚定市场实际水平"这条约束。
//
// ## 背景（起因是一次实测诊断）
//
// 用户点名四只高成长股评分偏低，查下来发现是**系统性偏差**而不是个股问题：
// 338 只样本里 68% 命中"方法冲突"，而冲突时最低的方法**永远是 PB**、
// PE **从不**是最低方 —— PB 相对 PE 中位折价 −45%，说明模型偏了。
//
// 进一步定位到根因：`fairPE` 给的目标倍数来自"公司相对同行的成长/质量分位"，
// 那是个**相对**分数，却被当成**绝对**倍数用。后果是低估值行业被严重高估：
//
//	平安银行  ROE 8.2%  市场 PE 5.1   → 模型目标 PE 20.7 倍（说要涨 4 倍）
//	招商银行  ROE 11.6% 市场 PE 7.1   → 模型目标 PE 23.6 倍
//	贵州茅台  ROE 31.9% 市场 PE 20.2  → 模型目标 PE 24.4 倍
//
// 银行与茅台的目标 PE 只差 3 倍多，而市场给它们的估值差 4 倍以上 ——
// 这就是"目标倍数脱离市场"的典型症状。
//
// 修法：以**行业实际 PE 中位数**为锚，成长/质量只决定"相对行业该溢价还是折价"，
// 并给溢价/折价设上限，避免再出现"任何好公司都给 20 倍"。

// bankAndLiquorCases 是一组"市场倍数差异极大"的对照样本。
//
// 用它们是因为银行与白酒的合理估值区间在市场上有共识：
// 银行长期 5~8 倍 PE，白酒龙头 20~30 倍。模型若给银行 20 倍目标 PE，
// 就说明它没在看市场。
var bankAndLiquorCases = []struct {
	code string
	name string
	// maxTargetPE 是"合理目标 PE 的上限"，超过即说明模型脱离市场
	maxTargetPE float64
}{
	{"000001", "平安银行", 12},
	{"600036", "招商银行", 12},
	{"601398", "工商银行", 12},
}

// TestBankTargetPEDoesNotDetachFromMarket 钉住"低估值行业不会被给出荒谬的目标 PE"。
//
// 这是本轮诊断最核心的一条断言：目标倍数必须与市场实际水平同量级。
func TestBankTargetPEDoesNotDetachFromMarket(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	checked := 0
	for _, item := range bankAndLiquorCases {
		detail, err := svc.GetStockValuation(item.code)
		if err != nil || detail == nil || !detail.Available {
			continue
		}
		stock := detail.Stock
		fair := stock.FairByHorizon["12m"]
		pe := findMethod(fair.Methods, "pe")
		if pe == nil || !pe.Applicable {
			t.Logf("%s %s：PE 法不适用，跳过", item.code, item.name)
			continue
		}
		checked++
		t.Logf("%s %s | 市场 PE %.1f ROE %.1f%% → 模型目标 PE %.2f | 合理价值 %.2f（现价 %.2f）| 锚 %s",
			item.code, item.name, stock.Self.CurrentPE, stock.Growth.ROE*100,
			pe.TargetMultiple, pe.FairValue, stock.Price, pe.Basis)

		if pe.TargetMultiple > item.maxTargetPE {
			t.Errorf("%s %s 的模型目标 PE 是 %.1f 倍（市场只有 %.1f 倍，上限应为 %.0f 倍）—— "+
				"目标倍数脱离了市场实际水平，会把低估值行业判成「严重低估」",
				item.code, item.name, pe.TargetMultiple, stock.Self.CurrentPE, item.maxTargetPE)
		}
		// 合理价值不应给出"要涨一倍以上"的荒谬结论
		if upside := pe.FairValue/stock.Price - 1; upside > 1.0 {
			t.Errorf("%s %s 的 PE 法合理价值 %.2f 元相对现价 %.2f 元高出 %.0f%% —— 不可能是可信结论",
				item.code, item.name, pe.FairValue, stock.Price, upside*100)
		}
	}
	if checked == 0 {
		t.Skip("抽样里的银行都不适用 PE 法，跳过")
	}
}

// TestTargetMultipleOrderingMatchesMarket 钉住"模型给的目标倍数排序，
// 必须与市场实际倍数排序一致"。
//
// 茅台的市场 PE 比银行高 3 倍以上。如果模型给它们的目标 PE 几乎一样
// （修前：24.4 vs 20.7），那就说明模型没有区分能力。
func TestTargetMultipleOrderingMatchesMarket(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	targetPE := func(code string) (market, target float64, ok bool) {
		detail, err := svc.GetStockValuation(code)
		if err != nil || detail == nil || !detail.Available {
			return 0, 0, false
		}
		pe := findMethod(detail.Stock.FairByHorizon["12m"].Methods, "pe")
		if pe == nil || !pe.Applicable {
			return 0, 0, false
		}
		return detail.Stock.Self.CurrentPE, pe.TargetMultiple, true
	}

	bankMarket, bankTarget, bankOK := targetPE("000001")
	liquorMarket, liquorTarget, liquorOK := targetPE("600519")
	if !bankOK || !liquorOK {
		t.Skip("对照样本的 PE 法不可用，跳过")
	}
	t.Logf("平安银行：市场 PE %.1f → 目标 PE %.2f", bankMarket, bankTarget)
	t.Logf("贵州茅台：市场 PE %.1f → 目标 PE %.2f", liquorMarket, liquorTarget)

	// 市场的倍数差是 3 倍以上；模型的目标倍数差也应显著（不能几乎相等）
	if liquorTarget <= bankTarget {
		t.Errorf("茅台的市场 PE（%.1f）远高于平安银行（%.1f），但模型给的目标 PE "+
			"（%.2f vs %.2f）没有体现出这个差异",
			liquorMarket, bankMarket, liquorTarget, bankTarget)
	}
	if liquorTarget-bankTarget < 5 {
		t.Errorf("两个行业的目标 PE 只差 %.2f 倍（%.2f vs %.2f）—— "+
			"市场的实际差是 %.1f 倍，模型几乎没有区分能力",
			liquorTarget-bankTarget, liquorTarget, bankTarget, liquorMarket/bankMarket)
	}
}

// findMethod 在方法列表里按标识查找。
func findMethod(methods []valuation.MethodResult, method string) *valuation.MethodResult {
	for i := range methods {
		if string(methods[i].Method) == method {
			return &methods[i]
		}
	}
	return nil
}
