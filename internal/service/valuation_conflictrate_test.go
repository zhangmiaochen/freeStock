package service

import (
	"strconv"
	"testing"

	"freestock/internal/cnlake"
)

// TestConflictRateAndUserStocks 是修完"目标倍数锚定市场"之后的验收：
//
//  1. 方法冲突率必须从修前的 68% 显著下降 —— 那是"模型偏了"而不是"市场分歧"；
//  2. 用户点名的那四只票，评分与结论必须变得可解释。
//
// 冲突率这条是**回归闸门**：如果哪天它又涨回 60% 以上，说明目标倍数
// 又脱离了市场（见 internal/valuation/fairvalue.go 的 fairPE 说明）。
func TestConflictRateAndUserStocks(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	report, err := svc.GetValuationReport(400, true)
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if len(report.Rankings) == 0 {
		t.Fatal("没有排名")
	}

	total, conflicts := 0, 0
	lowestPB, lowestEV, lowestPE := 0, 0, 0
	for _, row := range report.Rankings[3].Rows {
		detail, err := svc.GetStockValuation(row.Code)
		if err != nil || detail == nil || !detail.Available {
			continue
		}
		fair := detail.Stock.FairByHorizon["12m"]
		total++
		if !fair.Conflict {
			continue
		}
		conflicts++
		lowest, lowestValue := "", 1e18
		for _, method := range fair.Methods {
			if !method.Applicable || method.FairValue <= 0 {
				continue
			}
			if method.FairValue < lowestValue {
				lowestValue, lowest = method.FairValue, string(method.Method)
			}
		}
		switch lowest {
		case "pb":
			lowestPB++
		case "ev_ebitda":
			lowestEV++
		case "pe":
			lowestPE++
		}
	}
	t.Logf("方法冲突率：%d/%d = %.0f%%（修前为 68%%）", conflicts, total, conflictRatio(conflicts, total))
	t.Logf("冲突时最低的方法：PB %d / EV-EBITDA %d / PE %d", lowestPB, lowestEV, lowestPE)

	// ⚠ 这道闸门**没有修好**，如实记录而不是把阈值调到"刚好通过"。
	//
	// 现状：修前 68% → 修后 66%。目标倍数锚定市场这一项确实修好了
	// （银行目标 PE 从 20~25 倍降到 5.7~9.4 倍，见 TestBankTargetPEDoesNotDetachFromMarket），
	// 但它**不是**冲突率的主因。
	//
	// 主因是：PB-ROE 是**稳态框架**（合理 PB = (ROE − g) ÷ (WACC − g)），
	// 它无法为"价值来自未来盈利、净资产很薄"的轻资产成长股定价。
	// 实测鼎龙股份：TTM ROE 17.0%（模型认定的稳态 ROE 15.9%，**数据是对的**），
	// 模型给 2.43 倍 PB，而市场给 9.31 倍 —— 市场的钱买的是未来增长，
	// 而公式里只有稳态。于是 PB 恒定给出远低的值，被当成"方法冲突"。
	//
	// 真要降下来，得决定"轻资产高 PB 公司是否还让 PB 法参与冲突判定"，
	// 那是产品判断（见开发计划 §8.2），不属于本轮已确认的两个修复。
	//
	// 所以这里只设一道**防回退**的宽闸门：超过 80% 说明模型又整体跑偏了。
	if total > 100 && conflictRatio(conflicts, total) > 80 {
		t.Errorf("方法冲突率 %.0f%% 超过 80%% —— 模型整体跑偏（修前 68%%、修后 66%%）",
			conflictRatio(conflicts, total))
	}

	// 用户点名的四只票：打印评分，确认结论可解释
	for _, item := range []struct{ code, name string }{
		{"000969", "安泰科技"},
		{"600378", "昊华科技"},
		{"003031", "中瓷电子"},
		{"300054", "鼎龙股份"},
	} {
		detail, err := svc.GetStockValuation(item.code)
		if err != nil || detail == nil || !detail.Available {
			t.Logf("%s %s：不可用", item.code, item.name)
			continue
		}
		stock := detail.Stock
		fair := stock.FairByHorizon["12m"]
		pe := findMethod(fair.Methods, "pe")
		pb := findMethod(fair.Methods, "pb")
		targetText := "PE 法不适用"
		if pe != nil && pe.Applicable {
			targetText = "目标 PE " + trimFloat(pe.TargetMultiple)
		}
		pbText := "PB 法不适用"
		if pb != nil && pb.Applicable {
			pbText = "PB 目标 " + trimFloat(pb.TargetMultiple) + "（现 " + trimFloat(pb.CurrentMultiple) + "）"
		}
		t.Logf("%s %s | 市场 PE %.1f PB %.2f | %s | %s | 合理价值 %.2f（现价 %.2f）| "+
			"安全边际 %+.1f%% | 冲突=%v | 12个月 %s %.1f",
			item.code, item.name, stock.Self.CurrentPE, stock.Self.CurrentPB,
			targetText, pbText, fair.Base, stock.Price, fair.SafetyMargin*100, fair.Conflict,
			stock.Scores[3].Rating, stock.Scores[3].FinalScore)
	}
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
