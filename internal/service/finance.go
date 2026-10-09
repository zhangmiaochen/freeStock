package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/stockdb"
)

// 以下三个是给前端用的视图结构：cnlake 的行结构只有 parquet 标签，
// 直接序列化会输出 Go 字段名，这里转成稳定的 JSON 契约。
type ShareStructureItem struct {
	Date             int64   `json:"date"`
	TotalShares      float64 `json:"totalShares"`
	FloatShares      float64 `json:"floatShares"`
	RestrictedShares float64 `json:"restrictedShares"`
	FreeFloatShares  float64 `json:"freeFloatShares"`
	ChangeReason     string  `json:"changeReason"`
}

type ShareholderCountItem struct {
	Date            int64   `json:"date"`
	HolderCount     float64 `json:"holderCount"`
	ChangePct       float64 `json:"changePct"`
	AvgFloatShares  float64 `json:"avgFloatShares"`
	AvgHoldingValue float64 `json:"avgHoldingValue"`
}

type TopHolderItem struct {
	RecordDate    int64   `json:"recordDate"`
	Scope         string  `json:"scope"`
	Rank          int32   `json:"rank"`
	Name          string  `json:"name"`
	HoldingShares float64 `json:"holdingShares"`
	HoldingPct    float64 `json:"holdingPct"`
	IsInstitution bool    `json:"isInstitution"`
	HolderType    string  `json:"holderType"`
}

// FinancialStatements 描述财报/财务数据的可得性与内容。
//
// 财报不在 free-stockdb 的默认数据集里，来源是 cnequity 数据湖的
// financial_statement_items、share_structure、shareholder_counts、top_holders。
//
// **这些数据与当前选中的行情源无关**：即使行情走 free-stockdb，
// 财务数据照样从这里出；行情切到数据湖时也是同一份。
type FinancialStatements struct {
	// Source 目前只有 "cnlake" 一种；为空表示没有可用的财报来源
	Source string `json:"source"`
	// Available 表示该股票的财报科目是否可读
	Available bool `json:"available"`
	// LakeAvailable 表示数据湖本身是否可达（跟某个数据集在不在库是两回事）
	LakeAvailable bool                   `json:"lakeAvailable"`
	LakeRoot      string                 `json:"lakeRoot"`
	Detail        string                 `json:"detail"`
	Datasets      []cnlake.DatasetStatus `json:"datasets"`
	ReportPeriods []string               `json:"reportPeriods"`

	// 财报科目（长表，按报告期）
	Items []cnlake.StatementItem `json:"items"`
	// 股本结构变更历史
	ShareStructure []ShareStructureItem `json:"shareStructure"`
	// 股东户数（筹码集中度）
	ShareholderCounts []ShareholderCountItem `json:"shareholderCounts"`
	// 十大股东（只返回最新一期）
	TopHolders []TopHolderItem `json:"topHolders"`
	// 十大股东对应的报告期
	TopHolderDate int64 `json:"topHolderDate"`

	// 每张表的入库情况，便于界面区分「这只股票没有数据」和「表没入库」
	TableStatus map[string]bool `json:"tableStatus"`
}

// datasetStatus 找出某个数据集的状态。
func datasetStatus(all []cnlake.DatasetStatus, name string) (cnlake.DatasetStatus, bool) {
	for _, item := range all {
		if item.Name == name {
			return item, true
		}
	}
	return cnlake.DatasetStatus{}, false
}

// 财务数据集的入库情况，供界面区分「没有数据」和「表没入库」。
var financeTableNames = []string{
	"financial_statement_items",
	"share_structure",
	"shareholder_counts",
	"top_holders",
}

// financialStatements 汇总财务数据的可得性与内容。
//
// itemLimit > 0 时才会真正把科目读出来；股本结构/股东户数/十大股东
// 只要对应的表在库就会读，因为它们本身就是按股票切片、体量很小。
func (s *StockService) financialStatements(code string, itemLimit int) FinancialStatements {
	ctx := context.Background()
	lake := s.lakeHandle()
	out := FinancialStatements{
		LakeRoot:      lake.Root(),
		LakeAvailable: lake.Available(),
		TableStatus:   map[string]bool{},
	}

	if !lake.Available() {
		out.Detail = "未找到 cnequity 数据湖。可在配置里指定数据湖根目录（curated 的上一级），或设置环境变量 FREESTOCK_CNLAKE。"
		return out
	}

	out.Source = "cnlake"
	out.Datasets = lake.Status(ctx)
	for _, name := range financeTableNames {
		if status, ok := datasetStatus(out.Datasets, name); ok {
			out.TableStatus[name] = status.Exists
		}
	}

	// 三张「股东/股本」表：与财报是否入库无关，各自独立判断
	bare := cnlake.BareSymbol(code)
	if out.TableStatus["share_structure"] {
		if rows, err := lake.ShareStructures(bare); err == nil {
			out.ShareStructure = make([]ShareStructureItem, 0, len(rows))
			for _, row := range rows {
				out.ShareStructure = append(out.ShareStructure, ShareStructureItem{
					Date:             row.Date(),
					TotalShares:      row.TotalShares,
					FloatShares:      row.FloatShares,
					RestrictedShares: row.RestrictedShares,
					FreeFloatShares:  row.FreeFloatShares,
					ChangeReason:     row.ChangeReason,
				})
			}
		}
	}
	if out.TableStatus["shareholder_counts"] {
		if rows, err := lake.ShareholderCounts(bare); err == nil {
			out.ShareholderCounts = make([]ShareholderCountItem, 0, len(rows))
			for _, row := range rows {
				out.ShareholderCounts = append(out.ShareholderCounts, ShareholderCountItem{
					Date:            row.Date(),
					HolderCount:     row.HolderCount,
					ChangePct:       row.HolderCountChangePct,
					AvgFloatShares:  row.AvgFloatShares,
					AvgHoldingValue: row.AvgHoldingValue,
				})
			}
		}
	}
	if out.TableStatus["top_holders"] {
		// 只用最新一期 → 用专门的方法：从最新年份分区往回读、拿到就停
		//（`top_holders` 有 60MB，整份读要 707ms/只）
		if rows, err := lake.TopHoldersLatest(bare); err == nil {
			// 只取最新一期，历史各期留给后续做对比功能
			for _, row := range rows {
				if row.Date() > out.TopHolderDate {
					out.TopHolderDate = row.Date()
				}
			}
			for _, row := range rows {
				if row.Date() != out.TopHolderDate {
					continue
				}
				out.TopHolders = append(out.TopHolders, TopHolderItem{
					RecordDate:    row.Date(),
					Scope:         row.HolderScope,
					Rank:          row.HolderRank,
					Name:          row.HolderName,
					HoldingShares: row.HoldingShares,
					HoldingPct:    row.HoldingPct,
					IsInstitution: row.IsInstitution,
					HolderType:    row.HolderType,
				})
			}
		}
	}

	// 财报科目
	statement, ok := datasetStatus(out.Datasets, "financial_statement_items")
	if !ok || !statement.Exists {
		built := lake.Datasets()
		out.Detail = fmt.Sprintf(
			"数据湖已找到（%s），但财报数据集尚未入库。当前已有：%s。"+
				"需要先让 cnequity 的 init 跑到 fundamentals 阶段（注意 phase2c_daily_bars_backfill 失败会阻断后续 phase）。",
			lake.Root(), strings.Join(built, "、"))
		return out
	}

	for _, partition := range statement.Partitions {
		if partition.Key == "report_period" {
			out.ReportPeriods = append(out.ReportPeriods, partition.Value)
		}
	}

	out.Available = true
	out.Detail = fmt.Sprintf("来自 cnequity 数据湖 %s，财报共 %d 行、%d 个报告期",
		lake.Root(), statement.Rows, len(out.ReportPeriods))

	if itemLimit > 0 {
		items, err := lake.StatementItems(ctx, bare, itemLimit)
		if err != nil {
			out.Available = false
			out.Detail = err.Error()
			return out
		}
		out.Items = items
	}
	return out
}

// ---------------------------------------------------------------------------
// 财务/基本面数据的可用性说明
// ---------------------------------------------------------------------------

// FinanceSourceStatus 描述一类财务数据的可获得性与补充途径。
//
// 之所以要在接口里返回这个：free-stockdb 的默认本地数据集**不含财务报表**，
// 前端与其显示一堆「—」，不如直接说清楚缺什么、怎么补。
type FinanceSourceStatus struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
	Fallback  string `json:"fallback"`
}

// MoneyFlowSummary 资金流摘要（本机数据集自带）。
type MoneyFlowSummary struct {
	Date      int64   `json:"date"`
	MainNet   float64 `json:"mainNet"`   // 当日主力净流入
	JumboNet  float64 `json:"jumboNet"`  // 超大单
	BigNet    float64 `json:"bigNet"`    // 大单
	MidNet    float64 `json:"midNet"`    // 中单
	SmallNet  float64 `json:"smallNet"`  // 小单
	MainIn    float64 `json:"mainIn"`    // 主力流入
	MainOut   float64 `json:"mainOut"`   // 主力流出
	MainNet5  float64 `json:"mainNet5"`  // 近 5 日主力净流入合计
	MainNet20 float64 `json:"mainNet20"` // 近 20 日主力净流入合计
	Days      int     `json:"days"`      // 实际取到的天数
}

// DerivedFundamentals 是由行情**推导**出来的财务指标（TTM 口径）。
//
// 数学依据：ROE = EPS / BPS，而 EPS = 收盘价 / PE、BPS = 收盘价 / PB，
// 于是 ROE = PB / PE，与股价无关。同理可反推净利润与净资产：
//
//	净利润(TTM) = 总市值 / PE_TTM
//	净资产      = 总市值 / PB
//
// 因此不需要财报，就能拿到全市场、逐日的 ROE / EPS / BPS / 净利润 / 净资产。
type DerivedFundamentals struct {
	ROE          float64 `json:"roe"`          // 净资产收益率 %（TTM）
	EPS          float64 `json:"eps"`          // 每股收益（TTM）
	BPS          float64 `json:"bps"`          // 每股净资产
	NetProfitTTM float64 `json:"netProfitTTM"` // 归母净利润 TTM（元）
	NetAssets    float64 `json:"netAssets"`    // 净资产（元）
	PeTTM        float64 `json:"peTTM"`
	PB           float64 `json:"pb"`
}

// FinanceSnapshot 是一只股票的财务/基本面快照。
type FinanceSnapshot struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	TradeDate int64   `json:"tradeDate"`
	Close     float64 `json:"close"`

	Derived DerivedFundamentals `json:"derived"`

	TotalMV    float64 `json:"totalMV"`
	FloatMV    float64 `json:"floatMV"`
	TotalShare float64 `json:"totalShare"`
	FloatShare float64 `json:"floatShare"`

	DividendYield float64 `json:"dividendYield"`
	DividendTTM   float64 `json:"dividendTTM"`
	DividendCount int     `json:"dividendCount"`

	MoneyFlow *MoneyFlowSummary `json:"moneyFlow"`

	// 财报（来自 cnequity 数据湖；数据湖没有或未入库时 Available=false）
	Statements FinancialStatements `json:"statements"`

	Sources []FinanceSourceStatus `json:"sources"`
}

// deriveFundamentals 由行情推导 TTM 口径的财务指标。数据缺失时返回 0。
func deriveFundamentals(q *stockdb.Quote) DerivedFundamentals {
	d := DerivedFundamentals{PeTTM: q.PeTTM, PB: q.PB}
	if q.PeTTM == 0 || q.PB == 0 {
		return d
	}

	d.ROE = round(q.PB/q.PeTTM*100, 4)
	d.EPS = round(q.Close/q.PeTTM, 4)
	d.BPS = round(q.Close/q.PB, 4)
	d.NetProfitTTM = round(q.TotalMV/q.PeTTM, 2)
	d.NetAssets = round(q.TotalMV/q.PB, 2)
	return d
}

func round(value float64, decimals int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	factor := math.Pow(10, float64(decimals))
	return math.Round(value*factor) / factor
}

// financeSources 返回各类财务数据的可得性。
func (s *StockService) financeSources(hasMoneyFlow bool, statements FinancialStatements) []FinanceSourceStatus {
	statementDetail := "free-stockdb 默认本地数据集不含财报"
	statementFallback := "可接入 cnequity 数据湖的 financial_statement_items"
	if statements.Available {
		statementDetail = statements.Detail
		statementFallback = ""
	} else if statements.Detail != "" {
		statementFallback = statements.Detail
	}

	lakeDetail := "日K里只有总股本与流通股本"
	if status, ok := datasetStatus(statements.Datasets, "share_structure"); ok && status.Exists {
		lakeDetail = fmt.Sprintf("来自 cnequity 数据湖，%d 行", status.Rows)
	}

	return []FinanceSourceStatus{
		{
			Key:       "derived",
			Name:      "行情派生指标（ROE / EPS / BPS / 净利润 / 净资产）",
			Available: true,
			Detail:    "由日K自带的 PE_TTM 与 PB 推导，全市场逐日更新",
			Fallback:  "",
		},
		{
			Key:       "dividend",
			Name:      "分红 / 股息率",
			Available: true,
			Detail:    "来自 free-stockdb 的「复权」表（含每次除权的每股派息）",
			Fallback:  "",
		},
		{
			Key:       "money_flow",
			Name:      "资金流（主力 / 超大单 / 大单 / 中单 / 小单）",
			Available: hasMoneyFlow,
			Detail:    "来自 free-stockdb 的「资金流」表",
			Fallback:  "该股没有资金流记录（多为次新股或已退市）",
		},
		{
			Key:       "statement",
			Name:      "财务报表（营收 / 毛利率 / 资产负债 / 现金流）",
			Available: statements.Available,
			Detail:    statementDetail,
			Fallback:  statementFallback,
		},
		{
			Key:  "share_structure",
			Name: "股本结构 / 股东户数 / 十大股东",
			Available: func() bool {
				status, ok := datasetStatus(statements.Datasets, "share_structure")
				return ok && status.Exists
			}(),
			Detail:   lakeDetail,
			Fallback: "随财报一起接入 cnequity 数据湖",
		},
	}
}

// GetFinanceSnapshot 返回一只股票的财务/基本面快照。
//
// 说明：当前"财报"类数据不可得（见 Sources 里的说明），
// 这里返回的是「行情派生指标 + 分红 + 资金流」。
func (s *StockService) GetFinanceSnapshot(code string) (*FinanceSnapshot, error) {
	provider, source := s.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tradeDate, err := s.cachedTradeDate(ctx, provider)
	if err != nil {
		return nil, err
	}

	quote, err := provider.QuotePoint(ctx, code, tradeDate)
	if err != nil {
		return nil, err
	}
	if quote == nil {
		return nil, fmt.Errorf("没有取到 %s 的最新行情（数据源 %s）", code, source.URL)
	}

	// 分红（用于股息率）
	factors, factorErr := provider.DividendFactors(ctx, code)
	if factorErr != nil {
		slog.Warn("读取分红数据失败", "code", code, "err", factorErr)
	}

	// 资金流：取近 30 个交易日做 5 日 / 20 日累计
	var flow *MoneyFlowSummary
	start := shiftDate(tradeDate, -60)
	flows, flowErr := provider.MoneyFlowRange(ctx, code, start, tradeDate)
	if flowErr != nil {
		slog.Warn("读取资金流失败", "code", code, "err", flowErr)
	}
	if len(flows) > 0 {
		last := flows[len(flows)-1]
		summary := &MoneyFlowSummary{
			Date:     last.Date,
			MainNet:  last.MainNet,
			JumboNet: last.JumboNet,
			BigNet:   last.BigNet,
			MidNet:   last.MidNet,
			SmallNet: last.SmallNet,
			MainIn:   last.MainIn,
			MainOut:  last.MainOut,
			Days:     len(flows),
		}
		for i := len(flows) - 1; i >= 0 && i >= len(flows)-5; i-- {
			summary.MainNet5 += flows[i].MainNet
		}
		for i := len(flows) - 1; i >= 0 && i >= len(flows)-20; i-- {
			summary.MainNet20 += flows[i].MainNet
		}
		flow = summary
	}

	profile := buildProfile(code, quote, factors, tradeDate)

	// 财报来自 cnequity 数据湖；这里只取状态（items 由 GetFinancialStatements 按需读）
	statements := s.financialStatements(code, 0)

	snapshot := &FinanceSnapshot{
		Code:          code,
		Name:          quote.Name,
		TradeDate:     quote.Date,
		Close:         quote.Close,
		Derived:       deriveFundamentals(quote),
		TotalMV:       quote.TotalMV,
		FloatMV:       quote.FloatMV,
		TotalShare:    quote.TotalShare,
		FloatShare:    quote.FloatShare,
		DividendYield: profile.DividendYield,
		DividendTTM:   profile.DividendTTM,
		DividendCount: profile.DividendCount,
		MoneyFlow:     flow,
		Statements:    statements,
		Sources:       s.financeSources(flow != nil, statements),
	}

	slog.Info("财务快照就绪",
		"code", code, "name", quote.Name,
		"roe", snapshot.Derived.ROE,
		"dividendYield", snapshot.DividendYield,
		"statements", snapshot.Statements.Available,
		"moneyFlowDays", func() int {
			if flow == nil {
				return 0
			}
			return flow.Days
		}(),
	)
	return snapshot, nil
}

// GetFinancialStatements 读取某只股票的财报科目。
//
// 数据湖不存在、或财报数据集尚未入库时不会报错，而是返回 Available=false
// 并在 Detail 里说明原因和下一步该做什么 —— 让界面能显示「为什么没有」，
// 而不是一句干巴巴的失败。
func (s *StockService) GetFinancialStatements(code string, limit int) (*FinancialStatements, error) {
	if limit <= 0 {
		limit = 200
	}
	result := s.financialStatements(code, limit)
	return &result, nil
}

// DataLakeInfo 是数据湖的整体状态，给「数据源设置」页展示用。
type DataLakeInfo struct {
	Root       string                 `json:"root"`
	Available  bool                   `json:"available"`
	Detail     string                 `json:"detail"`
	Datasets   []string               `json:"datasets"`
	Status     []cnlake.DatasetStatus `json:"status"`
	ConfigPath string                 `json:"configPath"`
}

// GetDataLakeStatus 返回数据湖状态。
func (s *StockService) GetDataLakeStatus() *DataLakeInfo {
	lake := s.lakeHandle()
	info := &DataLakeInfo{
		Root:      lake.Root(),
		Available: lake.Available(),
		Datasets:  lake.Datasets(),
	}
	if s.cfg != nil {
		info.ConfigPath = s.cfg.Path()
	}

	if !lake.Available() {
		info.Detail = "未找到 cnequity 数据湖。设置数据湖根目录（含 curated 子目录的那一级），" +
			"或设置环境变量 FREESTOCK_CNLAKE。"
		return info
	}

	info.Status = lake.Status(context.Background())
	info.Detail = fmt.Sprintf("数据湖可用，已有 %d 个数据集", len(info.Datasets))
	return info
}

// SetDataLakeRoot 设置数据湖根目录并重新探测。
func (s *StockService) SetDataLakeRoot(root string) *DataLakeInfo {
	if s.cfg != nil {
		s.cfg.SetCnLakeRoot(root)
	}
	s.lakeMu.Lock()
	s.lake = nil
	s.lakeMu.Unlock()
	return s.GetDataLakeStatus()
}
