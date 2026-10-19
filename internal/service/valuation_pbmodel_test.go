package service

import (
	"testing"

	"freestock/internal/cnlake"
)

// TestDiagnosePBROEModel 单独检查 PB-ROE 模型是否系统性偏低。
//
// 上一条用例显示：在 338 只样本里，PB 法**从不**是最高方、PE 法**从不**是最低方，
// PB 相对 PE 中位折价 −45%。这种一边倒的分布不可能是市场分歧 ——
// 只能是模型本身偏了。
//
// 本用例挑几只"PE 与 PB 两个市场倍数本来大致自洽"的公司（ROE 稳定、
// 净资产是主要的估值锚），把两个模型的输出与市场倍数并列，看差在哪。
func TestDiagnosePBROEModel(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	// 贵州茅台：ROE 极高且稳定，市场 PB 6 倍左右是长期常态
	// 长江电力：公用事业，PB 3 倍左右，净资产锚很硬
	// 招商银行：银行股，PB 与 ROE 的关系最经典
	codes := []struct{ code, name string }{
		{"600519", "贵州茅台"},
		{"600900", "长江电力"},
		{"600036", "招商银行"},
		{"000001", "平安银行"},
	}

	for _, item := range codes {
		detail, err := svc.GetStockValuation(item.code)
		if err != nil || detail == nil || !detail.Available {
			t.Logf("%s %s：不可用", item.code, item.name)
			continue
		}
		stock := detail.Stock
		fair := stock.FairByHorizon["12m"]
		t.Logf("──────────────────────────────────────────────")
		t.Logf("%s %s | 价 %.2f | 市场 PE %.1f PB %.2f | TTM ROE %.1f%%",
			item.code, stock.Name, stock.Price,
			stock.Self.CurrentPE, stock.Self.CurrentPB, stock.Growth.ROE*100)
		for _, method := range fair.Methods {
			if !method.Applicable {
				continue
			}
			t.Logf("   %-16s 目标倍数 %7.2f  当前倍数 %7.2f  合理价值 %8.2f 元",
				method.Label, method.TargetMultiple, method.CurrentMultiple, method.FairValue)
			t.Logf("        口径：%s", method.Basis)
		}
		t.Logf("   汇总：悲观 %.2f / 基准 %.2f / 乐观 %.2f | 安全边际 %+.1f%% | 冲突=%v",
			fair.Pessimistic, fair.Base, fair.Optimistic, fair.SafetyMargin*100, fair.Conflict)
	}
}
