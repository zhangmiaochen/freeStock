package service

import (
	"testing"

	"freestock/internal/cnlake"
)

// TestDiagnoseMethodConflictPattern 量化"方法冲突"在全市场的分布，
// 看它是不是**系统性地**在惩罚某一类公司。
//
// 起因：用户点名的高成长股（安泰科技 / 昊华科技 / 中瓷电子 / 鼎龙股份）
// 四只全部命中 `method_conflict`，而冲突的一方**永远是 PB 法给出远低的估值**。
// 命中冲突会扣置信度 20 分，把置信度压到 50 分以下，从而：
//   - 触发 `data_quality` 风险（评级封顶）；
//   - 数据质量乘子降到 0.78（综合分再乘一次）。
//
// 所以这不是某一两只票的问题，而是**一类公司**的问题。这条用例把分布打出来，
// 用于判断该怎么改（是 PB 法本身不适用于轻资产高 PB 公司，
// 还是冲突判定的口径太激进）。
func TestDiagnoseMethodConflictPattern(t *testing.T) {
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

	total := 0
	conflicts := 0
	// 冲突时"最低的那个方法是什么"的分布
	lowestIsPB := 0
	lowestIsEV := 0
	lowestIsPE := 0
	lowestIsPS := 0
	// PB 法相对 PE 法的折价分布
	discounts := []float64{}
	// 高 PB（>3 倍）公司的冲突率
	highPBTotal, highPBConflict := 0, 0
	lowPBTotal, lowPBConflict := 0, 0

	for _, row := range report.Rankings[3].Rows { // 12 个月档
		detail, err := svc.GetStockValuation(row.Code)
		if err != nil || detail == nil || !detail.Available {
			continue
		}
		stock := detail.Stock
		fair := stock.FairByHorizon["12m"]
		total++

		var pe, pb, ev float64
		for _, method := range fair.Methods {
			if !method.Applicable || method.FairValue <= 0 {
				continue
			}
			switch string(method.Method) {
			case "pe":
				pe = method.FairValue
			case "pb":
				pb = method.FairValue
			case "ev_ebitda":
				ev = method.FairValue
			}
		}
		if pb > 0 && pe > 0 {
			discounts = append(discounts, pb/pe-1)
		}

		highPB := stock.Self.CurrentPB > 3
		if highPB {
			highPBTotal++
		} else {
			lowPBTotal++
		}

		if !fair.Conflict {
			continue
		}
		conflicts++
		if highPB {
			highPBConflict++
		} else {
			lowPBConflict++
		}

		// 找出最低的那个适用方法
		lowest := ""
		lowestValue := 1e18
		for _, method := range fair.Methods {
			if !method.Applicable || method.FairValue <= 0 {
				continue
			}
			if method.FairValue < lowestValue {
				lowestValue = method.FairValue
				lowest = string(method.Method)
			}
		}
		_ = ev
		switch lowest {
		case "pb":
			lowestIsPB++
		case "ev_ebitda":
			lowestIsEV++
		case "pe":
			lowestIsPE++
		case "ps":
			lowestIsPS++
		}
	}

	t.Logf("样本 %d 只（市值前 400 的 12 个月档，逐只用详情页口径算）", total)
	t.Logf("命中方法冲突 %d 只（%.0f%%）", conflicts, conflictRatio(conflicts, total))
	t.Logf("冲突时**最低的方法**是：PB %d / EV-EBITDA %d / PE %d / PS %d",
		lowestIsPB, lowestIsEV, lowestIsPE, lowestIsPS)
	if len(discounts) > 0 {
		t.Logf("PB 法相对 PE 法的估值折价：中位 %.0f%%、P25 %.0f%%、P75 %.0f%%（样本 %d）",
			percentileOf(discounts, 0.5)*100, percentileOf(discounts, 0.25)*100,
			percentileOf(discounts, 0.75)*100, len(discounts))
	}
	t.Logf("按当前 PB 分组：高 PB(>3倍) %d 只中冲突 %d 只（%.0f%%）；"+
		"低 PB(≤3倍) %d 只中冲突 %d 只（%.0f%%）",
		highPBTotal, highPBConflict, conflictRatio(highPBConflict, highPBTotal),
		lowPBTotal, lowPBConflict, conflictRatio(lowPBConflict, lowPBTotal))
}

func conflictRatio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// percentileOf 取分位（输入未排序，内部拷贝排序）。
func percentileOf(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	index := int(q * float64(len(sorted)-1))
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
