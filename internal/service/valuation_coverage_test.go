package service

import (
	"strings"
	"testing"

	"freestock/internal/cnlake"
)

// TestIndustryBenchmarkUsesFullMarket 钉住"行业基准用全市场算"这条设计约束。
//
// 背景（实测踩过的坑）：第一版让行业聚合只用**被评估的那批标的**当成分。
// 默认只评估市值前 150 只，150 只摊到 61 个行业 → 56 个行业不足
// IndustryMinMembers(5) → 行业聚合不可用 → 相对估值整块失效：
//
//	修前：行业聚合 68 个，可用 7 个；12 个月榜 128 行里 80 行"对行业中位"= 0
//	修后：行业聚合 396 个，可用 271 个；同榜 128 行里只有 17 行为 0
//
// 所以这个测试**断言覆盖率**，而不只是打印诊断信息 —— 一旦有人把
// buildValuationReport 里的 universe 换回 evaluations，它会立刻失败。
func TestIndustryBenchmarkUsesFullMarket(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	report, err := svc.GetValuationReport(150, true)
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if !report.Available {
		t.Fatalf("评估不可用: %s", report.Message)
	}

	// ---- 1 行业聚合必须是"全市场量级"，不是"150 只摊出来的几十个" ----
	if len(report.Industries) < 100 {
		t.Errorf("行业聚合只有 %d 个：把行业基准退化成「被评估的那批标的」了？"+
			"（前 150 只摊出来只有几十个行业）", len(report.Industries))
	}
	available := 0
	for _, industry := range report.Industries {
		if industry.Available {
			available++
		}
	}
	// 成分 >=5 的行业占比应显著高于"只算前 150 只"的情形（那时是 7/68 约 10%）
	if ratio := float64(available) / float64(len(report.Industries)); ratio < 0.35 {
		t.Errorf("可用行业占比只有 %.0f%%（%d/%d），偏低 —— 行业成分很可能仍来自被评估的小集合",
			ratio*100, available, len(report.Industries))
	}
	t.Logf("行业聚合：%d 个（可用 %d）", len(report.Industries), available)

	// ---- 2 榜上"对行业中位"必须有值 ----
	//
	// ⚠ 这一项依赖**TTM 财报**：`PremiumVsMedian` 用的是 `公司PE(TTM) / 行业中位PE`，
	// 而公司PE 由 TTM 净利润算出来；财报没入库时它是 0（即使行情侧 `CurrentPE`
	// 有值、行业加权 PE 也有值），于是整列都是 0。
	// 那种环境下这条断言测不到"行业基准是否来自全市场"，只会误报 ——
	// 所以先用"有基本面分"的行做样本；一只都没有就跳过并说明原因。
	ranking := report.Rankings[3] // 12 个月
	qualified := make([]int, 0, len(ranking.Rows))
	for i, row := range ranking.Rows {
		if row.CurrentPE > 0 && row.IndustryWeightedPE > 0 && row.QualityScore > 0 {
			qualified = append(qualified, i)
		}
	}
	if len(qualified) == 0 {
		t.Skipf("12 个月榜 %d 行里没有同时具备 TTM 财报与行业基准的样本"+
			"（数据湖财报可能未入库），跳过「对行业中位」断言", len(ranking.Rows))
	}
	// 整份报告里一条"对行业中位"都没有值时，说明本机缺的不是行业基准，而是
	// **TTM 净利润**（`公司PE = 市值 / 净利润TTM`，净利润缺失/为负时它恒为 0）。
	// 那种环境下这条断言无法判定实现是否正确，跳过并写清原因，而不是报假红。
	anyPremium := false
	for _, r := range report.Rankings {
		for _, row := range r.Rows {
			if row.PremiumVsMedian != 0 {
				anyPremium = true
			}
		}
	}
	if !anyPremium {
		t.Skipf("本机所有榜的「对行业中位」都为 0：TTM 净利润不可用（财报未入库？）"+
			"—— 无法判定相对估值实现，跳过该断言（行业聚合 %d 个已单独校验）",
			len(report.Industries))
	}
	zero := 0
	for _, i := range qualified {
		if ranking.Rows[i].PremiumVsMedian == 0 {
			zero++
		}
	}
	zeroRatio := float64(zero) / float64(len(qualified))
	if zeroRatio > 0.25 {
		t.Errorf("「对行业中位」为 0 的行占 %.0f%%（%d/%d，样本=%d），过高 —— "+
			"相对估值大面积不可用；行业基准应当来自全市场，只有成分确实不足 5 只的行业才允许为空",
			zeroRatio*100, zero, len(qualified), len(ranking.Rows))
	}
	t.Logf("12 个月榜：%d 行，其中可判定 %d 行，对行业中位为 0 的 %d 行（%.0f%%）",
		len(ranking.Rows), len(qualified), zero, zeroRatio*100)

	// ---- 3 行业估值环境分不能退化成同一个常数 ----
	//
	// 行业聚合不可用时这个维度恒为 50。断言它至少有多种取值 ——
	// 否则"行业估值环境"这个维度白设。
	industryScores := map[float64]bool{}
	for i := range report.Industries {
		industryScores[report.Industries[i].Score] = true
	}
	if len(industryScores) < 3 {
		t.Errorf("行业估值环境分只有 %d 种取值 —— 维度可能退化成了常数", len(industryScores))
	}

	// ---- 3b 行业历史分位必须真的算出来了 ----
	//
	// 这是吃下申万 82 个快照之后**唯一新增的能力**：在它之前，行业环境分
	// 只能靠"加权与中位 PE 的背离"这个横截面口径，回答不了"行业比它自己
	// 历史上贵还是便宜"。断言有相当比例的行业拿到了真实历史分位。
	withPercentile := 0
	withBand := 0
	for i := range report.Industries {
		item := &report.Industries[i]
		if item.PEPercentile > 0 {
			withPercentile++
		}
		if item.PEBand.P50 > 0 {
			withBand++
		}
	}
	if withPercentile < 30 {
		t.Errorf("只有 %d 个行业算出了历史 PE 分位 —— 行业历史序列重建可能没生效", withPercentile)
	}
	t.Logf("行业历史分位：%d/%d 个行业有分位，%d 个有历史区间",
		withPercentile, len(report.Industries), withBand)

	// ---- 4 排名行必须带名称（界面要显示，缺了只能看到一串代码）----
	missingName := 0
	for _, row := range ranking.Rows {
		if row.Name == "" {
			missingName++
		}
	}
	if missingName > len(ranking.Rows)/10 {
		t.Errorf("%d/%d 行的名称为空 —— instruments 的名称映射或 RankRow 组装有问题",
			missingName, len(ranking.Rows))
	}

	// ---- 6 行业码必须带体系前缀，且展示名不能只是把代码抄一遍 ----
	//
	// 本机 industry_members 混存 eastmoney（4 位代码 + 中文名）与申万
	// （6 位代码 + 名称即代码）。不带前缀会让两套体系的同名数字码撞车；
	// 而申万那一路若把 "340501" 直接当展示名，界面就没法看。
	checked := 0
	for i := range report.Industries {
		item := &report.Industries[i]
		if item.Code == "unclassified" {
			continue
		}
		if !strings.Contains(item.Code, ":") {
			t.Errorf("行业码 %q 没有体系前缀 —— 混体系下会与另一套体系撞车", item.Code)
			break
		}
		if item.Name == industryCodeOf(item.Code) {
			t.Errorf("行业 %q 的展示名与代码相同（%q）—— 申万那一路需要「体系 代码」的写法",
				item.Code, item.Name)
			break
		}
		checked++
	}
	if checked == 0 {
		t.Error("没有任何行业被检查到 —— 行业聚合可能整块为空")
	}

	// ---- 7 排名行必须带 industryCode（界面按它做行业筛选）----
	//
	// 界面上的行业筛选是"多选行业码"，需要每一行都知道自己属于哪个行业。
	// 只看 industryName 是不够的：本机行业体系混存（sw / eastmoney），
	// 名称可能重名或为空，而代码带体系前缀、是真正的身份。
	missingIndustryCode := 0
	for _, row := range ranking.Rows {
		if row.IndustryCode == "" {
			missingIndustryCode++
		}
	}
	if missingIndustryCode > 0 {
		t.Errorf("%d/%d 行的 industryCode 为空 —— 界面的行业筛选会失效",
			missingIndustryCode, len(ranking.Rows))
	}

	// ---- 8 排名行的名称必须能取到拼音（搜索用）----
	//
	// 拼音本身由 TestGetNamePinyinMatchesSearchIndex 覆盖，
	// 这里只断言"排名行确实带了名称"，否则前端无从算拼音。
	unnamed := 0
	for _, row := range ranking.Rows {
		if row.Name == "" {
			unnamed++
		}
	}
	if unnamed > 0 {
		t.Errorf("%d 行没有名称，拼音搜索无法生效", unnamed)
	}
}
