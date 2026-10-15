package valuation

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 财务时点序列（point-in-time）
//
// ## 为什么必须自己重建，不能直接读供应商给的 PE
//
// 数据湖的 `valuation_metrics` 在本机**只有 5 个交易日**（2026-10-08 ~ 10-14），
// 所以"近 3 年历史估值分位"这类需求根本没有现成数据。必须自己重建：
//
//	历史市值 = 当期股本 × 当日不复权收盘价
//	历史 PE  = 历史市值 ÷ 当时**已披露**的 TTM 归母净利润
//	历史 PB  = 历史市值 ÷ 当时**已披露**的归母净资产
//
// "当时已披露"由 `announce_date` 门控 —— 本机这一列 100% 有值（实测 2025Q4 的
// 161676 行全部非空），所以门控是硬的，不是近似。
//
// ## 口径陷阱：报告期数值是**年内累计**，Q4 是**全年值**
//
// 实测 600519 归母净利润（亿元）：
//
//	2023Q1 207.95  2023Q2 359.80  2023Q3 528.76  2023Q4 747.34   ← Q4=全年
//	2024Q1 240.65  2024Q2 416.96  2024Q3 608.28  2024Q4 862.28   ← Q4=全年
//	2025Q4 823.20 = 2025 全年；2025Q4 单季 = 823.20 − 646.27 = 176.93
//
// 所以：Q1/Q2/Q3 是"年初至今累计"，而 Q4 是**全年累计**（不是第四季度单季）。
// 于是 TTM 只需两段，**不需要把 Q4 减成单季**：
//
//	TTM(报告期 P) = 上年全年 + 本年至今(P) − 去年同期(P)
//
// 这条公式已用**全市场 3941 只**对拍供应商 PE_TTM 验证：
// 自建 PE 与供应商 PE 的中位比值 = 1.0000，**99.87% 的标的落在 3% 以内**。
// 用"把 Q4 当单季再求和"的写法在同一批样本上会算出 2~3 倍的偏离
// （因为会把全年值再当一次单季加一遍），所以这里的写法不能"顺手改好"。
//
// 上年全年取不到时（新股、缺年报）回落到 Q1+Q2+Q3+Q4 求和。
// ---------------------------------------------------------------------------

// Report 是一个报告期的原始累计科目值（已按 (symbol, report_period) 去重，
// 只保留最新一次修订）。
type Report struct {
	// Period 形如 "2025Q4"。
	Period string
	// Year 与 Quarter 是 Period 的解析结果。
	Year    int
	Quarter int
	// AnnounceDate 是公告日 YYYYMMDD —— 时点门控就靠它。
	AnnounceDate int64
	// Values 是 科目代码 -> 累计值。
	Values map[string]float64
}

// ParsePeriod 解析 "2025Q4"；失败返回 false。
func ParsePeriod(period string) (year, quarter int, ok bool) {
	p := strings.TrimSpace(strings.ToUpper(period))
	if len(p) < 6 || p[4] != 'Q' {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(p[:4], "%d", &year); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(p[5:6], "%d", &quarter); err != nil {
		return 0, 0, false
	}
	if year < 1990 || year > 2200 || quarter < 1 || quarter > 4 {
		return 0, 0, false
	}
	return year, quarter, true
}

// ReportKey 拼出报告期的排序键（逐年逐季可比）。
func ReportKey(year, quarter int) int { return year*10 + quarter }

// ---------------------------------------------------------------------------
// 科目映射
//
// 数据湖 `financial_statement_items` 的 (statement_type, item_code) 一共 30 个
// 可用组合，实测覆盖率（2026Q2 有该科目的标的数，全市场约 5580 只）：
//
//	income:    revenue 5579 · operating_cost 5478 · operating_profit 5581
//	           total_profit 5581 · net_profit 5580 · net_profit_deducted 5581
//	           sale_expense 5419 · manage_expense 5576 · finance_expense 5480
//	           income_tax 5536
//	balance:   total_assets 5581 · total_liabilities 5581 · total_equity 5581
//	           monetary_funds 5581 · accounts_receivable 5525 · inventory 5425
//	           fixed_assets 5534
//	indicator: eps 5579 · bps 5579 · roe 5524 · gross_margin 5477
//	           revenue_yoy 5578 · net_profit_yoy 5580 · ocf_per_share 5579
//	           eps_deducted 5574
//	cashflow:  net_cash_operate 5581 · net_cash_invest 5576 · capex 5553
//	           net_cash_finance 5544 · end_cash 49   ← end_cash 只有 49 只，不可用
//
// ⚠ 没有"折旧摊销"科目 → EV/EBITDA 只能用 operating_profit 近似，
// 界面上必须写明这个近似口径（方案 §16 已如实降级）。
// ---------------------------------------------------------------------------

// 科目代码常量（避免全代码里散落字符串）。
const (
	ItemRevenue          = "revenue"
	ItemOperatingCost    = "operating_cost"
	ItemOperatingProfit  = "operating_profit"
	ItemTotalProfit      = "total_profit"
	ItemNetProfit        = "net_profit"
	ItemNetProfitDeduct  = "net_profit_deducted"
	ItemIncomeTax        = "income_tax"
	ItemTotalAssets      = "total_assets"
	ItemTotalLiabilities = "total_liabilities"
	ItemTotalEquity      = "total_equity"
	ItemRoe              = "roe"
	ItemGrossMargin      = "gross_margin"
	ItemRevenueYoy       = "revenue_yoy"
	ItemNetProfitYoy     = "net_profit_yoy"
	ItemBPS              = "bps"
	ItemEPS              = "eps"
	ItemOCFPerShare      = "ocf_per_share"
	ItemNetCashOperate   = "net_cash_operate"
	ItemNetCashInvest    = "net_cash_invest"
	ItemNetCashFinance   = "net_cash_finance"
	ItemCapex            = "capex"
)

// StatementOf 返回科目所属的 statement_type。
func StatementOf(item string) string {
	switch item {
	case ItemRevenue, ItemOperatingCost, ItemOperatingProfit, ItemTotalProfit,
		ItemNetProfit, ItemNetProfitDeduct, ItemIncomeTax:
		return "income"
	case ItemTotalAssets, ItemTotalLiabilities, ItemTotalEquity:
		return "balance"
	case ItemRoe, ItemGrossMargin, ItemRevenueYoy, ItemNetProfitYoy,
		ItemBPS, ItemEPS, ItemOCFPerShare:
		return "indicator"
	case ItemNetCashOperate, ItemNetCashInvest, ItemNetCashFinance, ItemCapex:
		return "cashflow"
	}
	return ""
}

// TrackedItems 是评估用到的全部科目（按 statement_type 分组读取时用）。
var TrackedItems = []string{
	ItemRevenue, ItemOperatingCost, ItemOperatingProfit, ItemTotalProfit,
	ItemNetProfit, ItemNetProfitDeduct, ItemIncomeTax,
	ItemTotalAssets, ItemTotalLiabilities, ItemTotalEquity,
	ItemRoe, ItemGrossMargin, ItemRevenueYoy, ItemNetProfitYoy,
	ItemBPS, ItemEPS, ItemOCFPerShare,
	ItemNetCashOperate, ItemNetCashInvest, ItemNetCashFinance, ItemCapex,
}

// ItemLabel 返回科目的中文名（依据追溯用）。
func ItemLabel(item string) string {
	switch item {
	case ItemRevenue:
		return "营业收入"
	case ItemOperatingCost:
		return "营业成本"
	case ItemOperatingProfit:
		return "营业利润"
	case ItemTotalProfit:
		return "利润总额"
	case ItemNetProfit:
		return "归母净利润"
	case ItemNetProfitDeduct:
		return "扣非归母净利润"
	case ItemIncomeTax:
		return "所得税"
	case ItemTotalAssets:
		return "总资产"
	case ItemTotalLiabilities:
		return "总负债"
	case ItemTotalEquity:
		return "归母净资产"
	case ItemRoe:
		return "净资产收益率"
	case ItemGrossMargin:
		return "毛利率"
	case ItemRevenueYoy:
		return "营收同比"
	case ItemNetProfitYoy:
		return "净利润同比"
	case ItemBPS:
		return "每股净资产"
	case ItemEPS:
		return "每股收益"
	case ItemOCFPerShare:
		return "每股经营现金流"
	case ItemNetCashOperate:
		return "经营活动现金流净额"
	case ItemNetCashInvest:
		return "投资活动现金流净额"
	case ItemNetCashFinance:
		return "筹资活动现金流净额"
	case ItemCapex:
		return "购建固定无形长期资产支付的现金"
	}
	return item
}

// ---------------------------------------------------------------------------
// 时点视图
// ---------------------------------------------------------------------------

// AsOf 是某只股票在某个评估日的财务时点视图。
type AsOf struct {
	// EvalDate 是评估日 YYYYMMDD。
	EvalDate int64 `json:"evalDate"`
	// LatestPeriod 是评估日之前**已披露**的最新报告期，如 "2026Q2"。
	LatestPeriod string `json:"latestPeriod"`
	// LatestAnnounce 是该报告期的公告日。
	LatestAnnounce int64 `json:"latestAnnounce"`
	// Reports 是已通过时点门控的报告期（升序），供趋势与单季拆解使用。
	Reports []Report `json:"-"`

	// Items 是各科目的 TTM 值（除资产负债类是**时点存量**，见下）。
	// 键用 ItemXxx 常量。
	Items map[string]float64 `json:"items"`
	// Missing 列出评估必需但缺失的科目。
	Missing []string `json:"missing"`
	// Quarters 是被用到的报告期数（用于"至少 8 个季度才能算同比趋势"这类判断）。
	Quarters int `json:"quarters"`

	// TTM 是流量类科目的 TTM（= Items 里对应键，显式带一个语义清楚的入口）。
	TTM TTMValues `json:"ttm"`
}

// TTMValues 是 TTM 口径的关键流量指标。
type TTMValues struct {
	Revenue         float64 `json:"revenue"`
	OperatingCost   float64 `json:"operatingCost"`
	OperatingProfit float64 `json:"operatingProfit"`
	NetProfit       float64 `json:"netProfit"`
	NetProfitDeduct float64 `json:"netProfitDeduct"`
	CashOperate     float64 `json:"cashOperate"`
	Capex           float64 `json:"capex"`
	FreeCashFlow    float64 `json:"freeCashFlow"`
	// CashOperateQuarters / CapexQuarters 是参与 TTM 求和的季度数。
	// 现金流科目在某些报告期会缺（实测 capex 覆盖率 5553/5581），
	// 所以必须记录"用几个季度拼出来的"，避免拿 2 个季度的和冒充全年。
	CashOperateQuarters int `json:"cashOperateQuarters"`
	CapexQuarters       int `json:"capexQuarters"`
}

// BuildAsOf 构造评估日的财务时点视图。
//
// reports 是**同一只股票**的全部报告期（可以含重复修订），本函数内部会
// 按 (report_period) 取 announce_date 最新的一条 —— 这一步不能省：
// 数据湖里同一报告期可能存在多次修订，把它们相加会把利润算成好几倍。
func BuildAsOf(reports []Report, evalDate int64) AsOf {
	return BuildAsOfFrom(keptReports(reports, evalDate), evalDate)
}

// keptReports 做去重 + 时点门控 + 排序，返回可直接喂给 BuildAsOfFrom 的前缀。
func keptReports(reports []Report, evalDate int64) []Report {
	// ① 按报告期去重：同一报告期保留 announce_date 最大的一条。
	byPeriod := map[string]Report{}
	for _, report := range reports {
		year, quarter, ok := ParsePeriod(report.Period)
		if !ok {
			continue
		}
		report.Year, report.Quarter = year, quarter
		if report.AnnounceDate <= 0 {
			// 没有公告日的行不能做时点门控 —— 直接丢弃，宁可少算不能算错
			continue
		}
		old, exists := byPeriod[report.Period]
		if !exists || report.AnnounceDate > old.AnnounceDate {
			byPeriod[report.Period] = report
		}
	}

	// ② 时点门控：只留公告日 <= 评估日的报告期。
	kept := make([]Report, 0, len(byPeriod))
	for _, report := range byPeriod {
		if evalDate > 0 && report.AnnounceDate > evalDate {
			continue
		}
		kept = append(kept, report)
	}
	sort.Slice(kept, func(i, j int) bool {
		return ReportKey(kept[i].Year, kept[i].Quarter) < ReportKey(kept[j].Year, kept[j].Quarter)
	})
	return kept
}

// BuildAsOfFrom 用**已经过滤好的**报告期构造时点视图。
//
// 存在的理由：历史估值序列重建要为每一个交易日算一次"当时已披露的 TTM"。
// 如果每次都重跑去重 + 排序（要建 map、要排序），1200 个交易日 × 5000 只股票
// 会变成不可接受的开销。调用方可以一次性把报告期去重排序（reportSchedule），
// 然后按公告日用二分切出前缀直接调用本函数。
//
// kept 必须满足：已按 (年, 季) 升序、已按报告期去重、公告日 ≤ 评估日、
// 且每行的 Year/Quarter 已填好（keptReports 保证这些）。
func BuildAsOfFrom(kept []Report, evalDate int64) AsOf {
	out := AsOf{EvalDate: evalDate, Items: map[string]float64{}}
	if len(kept) == 0 {
		return out
	}
	out.Reports = kept
	out.Quarters = len(kept)

	latest := kept[len(kept)-1]
	out.LatestPeriod = latest.Period
	out.LatestAnnounce = latest.AnnounceDate

	// ③ 每个科目按 TTM 公式汇总。
	for _, item := range TrackedItems {
		statement := StatementOf(item)
		if statement == "balance" {
			// 资产负债表是**时点存量**，直接取最新报告期，不做 TTM。
			if value, ok := latest.Values[item]; ok {
				out.Items[item] = value
			} else {
				out.Missing = append(out.Missing, item)
			}
			continue
		}
		value, ok := ttmOf(kept, item)
		if !ok {
			out.Missing = append(out.Missing, item)
			continue
		}
		out.Items[item] = value
	}

	// ④ 现金流类单独记季度数：它们缺季比利润科目常见得多。
	out.TTM = TTMValues{
		Revenue:         out.Items[ItemRevenue],
		OperatingCost:   out.Items[ItemOperatingCost],
		OperatingProfit: out.Items[ItemOperatingProfit],
		NetProfit:       out.Items[ItemNetProfit],
		NetProfitDeduct: out.Items[ItemNetProfitDeduct],
		CashOperate:     out.Items[ItemNetCashOperate],
		Capex:           out.Items[ItemCapex],
	}
	_, out.TTM.CashOperateQuarters, _ = ttmQuarters(kept, ItemNetCashOperate)
	_, out.TTM.CapexQuarters, _ = ttmQuarters(kept, ItemCapex)
	out.TTM.FreeCashFlow = out.TTM.CashOperate - out.TTM.Capex
	return out
}

// KeptReports 导出报告期去重 + 时点门控（供服务层建"报告期日程表"用）。
func KeptReports(reports []Report, evalDate int64) []Report {
	return keptReports(reports, evalDate)
}

// Retained 返回"把报告期截到 date 之前"的时点视图。
//
// 用途：算历史某个时点的 ROE / 资产负债率（同比要用"上年同期的时点视图"）。
// 由于 Reports 里每一行的公告日都已经 ≤ 原评估日，直接按日期截断即可，
// 并且截断后**重新算一遍 TTM 与存量科目**，而不是沿用最新值 ——
// 沿用最新值会让历史同比用上未来的净资产。
func (a AsOf) Retained(date int64) AsOf {
	if date <= 0 {
		return a
	}
	kept := make([]Report, 0, len(a.Reports))
	for _, report := range a.Reports {
		if report.AnnounceDate <= date {
			kept = append(kept, report)
		}
	}
	if len(kept) == len(a.Reports) {
		return a
	}
	return BuildAsOfFrom(kept, date)
}

// ttmOf 按 TTM 公式算出某科目的 TTM；数据不足返回 false。
//
//	TTM = 上年全年 + 本年至今 − 去年同期
func ttmOf(reports []Report, item string) (float64, bool) {
	value, quarters, ok := ttmQuarters(reports, item)
	if !ok || quarters <= 0 {
		return 0, false
	}
	return value, true
}

// ttmQuarters 返回 TTM 值与"这条 TTM 由几个报告期构成"。
//
// 为什么第二个返回值重要：现金流科目会缺季，若只有 3 个季度有值，
// 上面那条公式算出来的既不是 TTM 也不能当 TTM 用 —— 调用方据此降置信度。
func ttmQuarters(reports []Report, item string) (float64, int, bool) {
	if len(reports) == 0 {
		return 0, 0, false
	}
	latest := reports[len(reports)-1]
	ytd, hasYTD := cumulativeAt(reports, latest.Year, latest.Quarter, item)
	if !hasYTD {
		return 0, 0, false
	}
	// 年报期（Q4）本身就是全年值，无需再拼。
	if latest.Quarter == 4 {
		return ytd, 4, true
	}

	yearAnnual, hasAnnual := annualValue(reports, latest.Year-1, item)
	prevYTD, hasPrev := cumulativeAt(reports, latest.Year-1, latest.Quarter, item)
	if hasAnnual && hasPrev {
		return yearAnnual + ytd - prevYTD, latest.Quarter + 4, true
	}

	// 回落：上年年报缺失时，**只能用上年前三季之和**当基数。
	//
	// ⚠ 这种情况下不能再减"去年同期"：Q1~Q3 之和里本来就**不含第四季度**，
	// 减掉同期会把第四季度凭空算成负数。实测反例：上年前三季和 330、本年至今 230、
	// 去年同期 210 —— 正确结果是 330 + 230 = 560（缺一个 Q4 的近似），
	// 而不是 330 + 230 − 210 = 350（那等于把 2026Q2 之前的一段算了两遍又减掉一遍）。
	//
	// 返回的季度数按实际参与的报告期累计，调用方据此降置信度。
	if partial, quarters, ok := partialAnnual(reports, latest.Year-1, item); ok {
		return partial + ytd, quarters + latest.Quarter, true
	}
	return 0, 0, false
}

// cumulativeAt 取某年某季的"年初至今累计"值。
//
// 关键：Q2/Q3 的累计值直接就存在数据里（就是那一行的值），Q4 存的是全年值。
// 所以这里只是按 (年, 季) 精确取值，**不做任何减法**。
func cumulativeAt(reports []Report, year, quarter int, item string) (float64, bool) {
	for i := range reports {
		if reports[i].Year == year && reports[i].Quarter == quarter {
			value, ok := reports[i].Values[item]
			return value, ok
		}
	}
	return 0, false
}

// annualValue 取某年的全年值（即该年 Q4 行）。
func annualValue(reports []Report, year int, item string) (float64, bool) {
	return cumulativeAt(reports, year, 4, item)
}

// partialAnnual 把某年 Q1~Q3 求和当作全年近似；返回求和值与参与季度数。
func partialAnnual(reports []Report, year int, item string) (float64, int, bool) {
	sum := 0.0
	quarters := 0
	for _, quarter := range []int{1, 2, 3} {
		if value, ok := cumulativeAt(reports, year, quarter, item); ok {
			sum += value
			quarters++
		}
	}
	if quarters < 3 {
		return 0, 0, false
	}
	return sum, quarters, true
}

// ---------------------------------------------------------------------------
// 单季拆解与同比（成长趋势用）
// ---------------------------------------------------------------------------

// SingleQuarter 是一个"单季"的值（由累计值相减得到）。
type SingleQuarter struct {
	Period  string
	Year    int
	Quarter int
	// Items 是单季值，键为 ItemXxx。
	Items map[string]float64
}

// SingleQuarters 把累计序列拆成单季序列（升序）。
//
// 拆法与 TTM 同源：
//
//	Q1 单季 = Q1 累计
//	Q2 单季 = Q2 累计 − Q1 累计
//	Q3 单季 = Q3 累计 − Q2 累计
//	Q4 单季 = Q4 累计（全年）− Q3 累计
//
// ⚠ Q4 那一行本身是**全年值**，所以"Q4 单季"必须减掉 Q3 累计，
// 不能直接把 Q4 行当第四季度（那会把全年算成一个季度）。
func SingleQuarters(reports []Report, items []string) []SingleQuarter {
	out := make([]SingleQuarter, 0, len(reports))
	prev := map[string]float64{}
	prevYear, prevQuarter := 0, 0

	for _, report := range reports {
		single := SingleQuarter{
			Period:  report.Period,
			Year:    report.Year,
			Quarter: report.Quarter,
			Items:   map[string]float64{},
		}
		sequential := report.Year == prevYear && report.Quarter == prevQuarter+1
		for _, item := range items {
			value, ok := report.Values[item]
			if !ok {
				continue
			}
			if report.Quarter == 1 || !sequential {
				// Q1 直接是单季；跨年断档（缺上季）时不做减法，
				// 宁可不给这个单季值，也不给出一个错的
				if report.Quarter == 1 {
					single.Items[item] = value
				}
				continue
			}
			single.Items[item] = value - prev[item]
		}
		out = append(out, single)
		prev = report.Values
		prevYear, prevQuarter = report.Year, report.Quarter
	}
	return out
}

// YoY 计算同季度同比；不足 4 个季度或去年同期缺失返回 false。
//
// 用**单季同比**而不是累计同比：累计同比受季节性影响（Q4 累计天然比 Q2 大），
// 跨公司比较会引入系统性偏差。
func YoY(singles []SingleQuarter, item string, back int) (float64, bool) {
	if len(singles) < back+4 {
		return 0, false
	}
	current := singles[len(singles)-1-back]
	previous := findByYearQuarter(singles, current.Year-1, current.Quarter)
	if previous == nil {
		return 0, false
	}
	now, okNow := current.Items[item]
	before, okBefore := previous.Items[item]
	if !okNow || !okBefore || before == 0 {
		return 0, false
	}
	return now/before - 1, true
}

func findByYearQuarter(singles []SingleQuarter, year, quarter int) *SingleQuarter {
	for i := len(singles) - 1; i >= 0; i-- {
		if singles[i].Year == year && singles[i].Quarter == quarter {
			return &singles[i]
		}
	}
	return nil
}

// QuarterYoYSeries 返回最近 n 个季度的单季同比序列（升序，可能少于 n 个）。
func QuarterYoYSeries(singles []SingleQuarter, item string, n int) []float64 {
	out := make([]float64, 0, n)
	for i := len(singles) - 1; i >= 0 && len(out) < n; i-- {
		current := singles[i]
		previous := findByYearQuarter(singles, current.Year-1, current.Quarter)
		if previous == nil {
			continue
		}
		now, okNow := current.Items[item]
		before, okBefore := previous.Items[item]
		if !okNow || !okBefore || before == 0 {
			continue
		}
		out = append(out, now/before-1)
	}
	// 反转成升序（时间从前到后）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// CumulativeYoY 计算"年初至今累计"的同比（部分行业习惯看这个）。
func CumulativeYoY(reports []Report, item string) (float64, bool) {
	if len(reports) == 0 {
		return 0, false
	}
	latest := reports[len(reports)-1]
	now, okNow := latest.Values[item]
	before, okBefore := cumulativeAt(reports, latest.Year-1, latest.Quarter, item)
	if !okNow || !okBefore || before == 0 {
		return 0, false
	}
	return now/before - 1, true
}

// CAGR 计算 years 年的复合增长率；区间不足或基期为负返回 false。
//
// 基期为负时不给 CAGR：从亏损到盈利的"增长率"在数学上无意义
// （负基数会让符号反转）。这种情况由成长模块改用"扭亏"这一独立判据。
func CAGR(start, end float64, years int) (float64, bool) {
	if years <= 0 || start <= 0 || end <= 0 {
		return 0, false
	}
	ratio := end / start
	if ratio <= 0 {
		return 0, false
	}
	return pow(ratio, 1/float64(years)) - 1, true
}

// AnnualValues 抽出最近 n 年的全年值序列（升序），供 CAGR 与增长稳定性使用。
func AnnualValues(reports []Report, item string, n int) []float64 {
	byYear := map[int]float64{}
	years := make([]int, 0, 8)
	for _, report := range reports {
		if report.Quarter != 4 {
			continue
		}
		value, ok := report.Values[item]
		if !ok {
			continue
		}
		if _, seen := byYear[report.Year]; !seen {
			years = append(years, report.Year)
		}
		byYear[report.Year] = value
	}
	sort.Ints(years)
	values := make([]float64, 0, len(years))
	for _, year := range years {
		values = append(values, byYear[year])
	}
	if n > 0 && len(values) > n {
		values = values[len(values)-n:]
	}
	return values
}
