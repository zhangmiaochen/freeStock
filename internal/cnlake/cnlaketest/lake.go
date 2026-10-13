// Package cnlaketest 按真实 schema 造一个结构完整的小数据湖，供测试使用。
//
// 存在的理由：本机 cnequity 的 init 卡在 daily_bars 回填，
// daily_bars / valuation_metrics / fund_flow 等数据集还没入库，
// 但读取逻辑必须先验证。这里用 parquet-go 写出与真实布局一致的目录结构。
package cnlaketest

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/parquet-go/parquet-go"

	"freestock/internal/cnlake"
)

func day(y, m, d int) int32 {
	return int32(time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// Day 把年月日折算成 parquet DATE 的天数，方便测试里造日期。
func Day(y, m, d int) int32 { return day(y, m, d) }

func write[T any](path string, rows []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return parquet.WriteFile(path, rows)
}

// Build 在 root 下写出数据湖目录（root/curated/...）。
//
// 标的：600519.SH（贵州茅台，3 年日线）、000001.SZ（平安银行）、
// 510980.SH（ETF）、300750.SZ（宁德时代，只有标的没有行情）。
func Build(root string) error {
	curated := filepath.Join(root, "curated")

	// 标的清单（无分区）
	if err := write(filepath.Join(curated, "instruments", "part-merged.parquet"), []cnlake.InstrumentRow{
		{Symbol: "000001.SZ", Name: "平安银行", Exchange: "SZ", AssetType: "stock", ListDate: day(1991, 4, 3)},
		{Symbol: "300750.SZ", Name: "宁德时代", Exchange: "SZ", AssetType: "stock", ListDate: day(2018, 6, 11)},
		{Symbol: "510980.SH", Name: "上证综合", Exchange: "SH", AssetType: "etf", ListDate: day(2023, 12, 1)},
		{Symbol: "600519.SH", Name: "贵州茅台", Exchange: "SH", AssetType: "stock", ListDate: day(2001, 8, 27)},
	}); err != nil {
		return err
	}

	// 日线：按年分区。每年 3 月连续 20 个交易日，
	// 连续是为了能验证「昨收 / 涨跌幅」这类需要前一根的推导。
	for _, year := range []int{2024, 2025, 2026} {
		var rows []cnlake.BarRow
		for _, symbol := range []string{"000001.SZ", "600519.SH"} {
			base := 100.0
			if symbol == "600519.SH" {
				base = 1500
			}
			for i := 0; i < 20; i++ {
				price := base + float64(year-2024)*10 + float64(i)
				rows = append(rows, cnlake.BarRow{
					Symbol:    symbol,
					TradeDate: day(year, 3, i+1),
					Open:      price,
					High:      price + 5,
					Low:       price - 5,
					Close:     price + 1,
					Volume:    int64(1000 * (i + 1)),
					Amount:    price * 1000,
				})
			}
		}
		path := filepath.Join(curated, "daily_bars", "trade_date="+strconv.Itoa(year), "part-merged.parquet")
		if err := write(path, rows); err != nil {
			return err
		}
	}

	// 估值：只有 2026 年，日期对齐到最后一根 K 线
	if err := write(filepath.Join(curated, "valuation_metrics", "trade_date=2026", "part-merged.parquet"), []cnlake.ValuationRow{
		{Symbol: "600519.SH", TradeDate: day(2026, 3, 20), PeTTM: 19.32, PB: 6.26, PsTTM: 9.5, TotalMV: 1.57e12, FloatMV: 1.57e12},
		{Symbol: "000001.SZ", TradeDate: day(2026, 3, 20), PeTTM: 5.17, PB: 0.48, PsTTM: 1.6, TotalMV: 2.24e11, FloatMV: 2.24e11},
	}); err != nil {
		return err
	}

	// 分红送转（结构体与生产读取侧共用 cnlake.CorporateAction）
	if err := write(filepath.Join(curated, "corporate_actions", "ex_date=2025", "part-merged.parquet"), []cnlake.CorporateAction{
		{Symbol: "600519.SH", ExDateRaw: day(2025, 6, 19), ActionType: "cash_dividend", CashDividend: 23.882},
		{Symbol: "000001.SZ", ExDateRaw: day(2025, 5, 20), ActionType: "cash_dividend", CashDividend: 0.5},
	}); err != nil {
		return err
	}

	// 复权因子
	if err := write(filepath.Join(curated, "adj_factors", "part-merged.parquet"), []cnlake.AdjFactorRow{
		{Symbol: "600519.SH", TradeDate: day(2025, 6, 18), AdjustType: "qfq", Factor: 1.0},
		{Symbol: "600519.SH", TradeDate: day(2025, 6, 19), AdjustType: "qfq", Factor: 1.019},
	}); err != nil {
		return err
	}

	// 资金流
	if err := write(filepath.Join(curated, "fund_flow", "trade_date=2026", "part-merged.parquet"), []cnlake.FundFlowRow{
		{Symbol: "600519.SH", TradeDate: day(2026, 3, 19), MainNetInflow: -1.2e8, LargeNetInflow: -1e8, SmallNetInflow: 1.2e8},
		{Symbol: "600519.SH", TradeDate: day(2026, 3, 20), MainNetInflow: 5.39e8, SuperLargeNetInflow: 3.37e8, LargeNetInflow: 2.03e8},
		{Symbol: "000001.SZ", TradeDate: day(2026, 3, 20), MainNetInflow: 1e7},
	}); err != nil {
		return err
	}

	// 行业 / 板块成分
	//
	// 与真实数据湖一样**按 as_of_date 分区**，而且故意造两份快照：
	// 阅读方必须只取每个板块自己最新的一份（否则同一只股票会出现多行：
	// 界面重复 3 行、成分数与成交额被放大）。旧快照里的 300750 是"已被调出"
	// 的成分，用来验证取的是**最新**快照而不是并集。
	if err := write(filepath.Join(curated, "industry_members", "as_of_date=2026-01-01", "part-merged.parquet"), []cnlake.IndustryMemberRow{
		{Symbol: "600519.SH", ClassificationSystem: "sw_l1", IndustryCode: "340000", IndustryName: "食品饮料", AsOfDate: day(2026, 1, 1)},
		{Symbol: "000001.SZ", ClassificationSystem: "sw_l1", IndustryCode: "480000", IndustryName: "银行", AsOfDate: day(2026, 1, 1)},
		{Symbol: "300750.SZ", ClassificationSystem: "sw_l1", IndustryCode: "340000", IndustryName: "食品饮料", AsOfDate: day(2026, 1, 1)},
	}); err != nil {
		return err
	}
	if err := write(filepath.Join(curated, "industry_members", "as_of_date=2026-02-01", "part-merged.parquet"), []cnlake.IndustryMemberRow{
		{Symbol: "600519.SH", ClassificationSystem: "sw_l1", IndustryCode: "340000", IndustryName: "食品饮料", AsOfDate: day(2026, 2, 1)},
		{Symbol: "000001.SZ", ClassificationSystem: "sw_l1", IndustryCode: "480000", IndustryName: "银行", AsOfDate: day(2026, 2, 1)},
		// 同一份快照里的重复行：即使快照本身有重，也不该把成分数放大
		{Symbol: "000001.SZ", ClassificationSystem: "sw_l1", IndustryCode: "480000", IndustryName: "银行", AsOfDate: day(2026, 2, 1)},
	}); err != nil {
		return err
	}
	if err := write(filepath.Join(curated, "sector_members", "as_of_date=2026-01-01", "part-merged.parquet"), []cnlake.SectorMemberRow{
		{Symbol: "600519.SH", SectorCode: "BK0477", SectorName: "酿酒行业", AsOfDate: day(2026, 1, 1)},
		{Symbol: "300750.SZ", SectorCode: "BK0477", SectorName: "酿酒行业", AsOfDate: day(2026, 1, 1)},
	}); err != nil {
		return err
	}
	if err := write(filepath.Join(curated, "sector_members", "as_of_date=2026-02-01", "part-merged.parquet"), []cnlake.SectorMemberRow{
		{Symbol: "600519.SH", SectorCode: "BK0477", SectorName: "酿酒行业", AsOfDate: day(2026, 2, 1)},
	}); err != nil {
		return err
	}

	// 财务三件套
	if err := write(filepath.Join(curated, "share_structure", "part-merged.parquet"), []cnlake.ShareStructureRow{
		{Symbol: "600519.SH", ChangeDate: day(2024, 1, 1), TotalShares: 1.25e9, FloatShares: 1.25e9, ChangeReason: "定期报告"},
		{Symbol: "600519.SH", ChangeDate: day(2025, 7, 1), TotalShares: 1.256e9, FloatShares: 1.256e9, ChangeReason: "回购注销"},
	}); err != nil {
		return err
	}
	if err := write(filepath.Join(curated, "shareholder_counts", "part-merged.parquet"), []cnlake.ShareholderCountRow{
		{Symbol: "600519.SH", CountDate: day(2025, 3, 31), HolderCount: 200000, HolderCountChangePct: -5.2},
		{Symbol: "600519.SH", CountDate: day(2025, 6, 30), HolderCount: 185000, HolderCountChangePct: -7.5},
	}); err != nil {
		return err
	}
	if err := write(filepath.Join(curated, "top_holders", "record_date=2025Q2", "part-merged.parquet"), []cnlake.TopHolderRow{
		{Symbol: "600519.SH", RecordDate: day(2025, 6, 30), HolderScope: "top10", HolderRank: 1, HolderName: "中国贵州茅台酒厂(集团)有限责任公司", HoldingShares: 6.78e8, HoldingPct: 54.0, IsInstitution: true},
		{Symbol: "600519.SH", RecordDate: day(2025, 6, 30), HolderScope: "top10", HolderRank: 2, HolderName: "香港中央结算有限公司", HoldingShares: 1.0e8, HoldingPct: 8.0, IsInstitution: true},
	}); err != nil {
		return err
	}

	// 财报科目（长表，按报告期分区）
	if err := write(filepath.Join(curated, "financial_statement_items", "report_period=2025Q2", "part-merged.parquet"), []statementRow{
		{Symbol: "600519.SH", ReportPeriod: "2025Q2", StatementType: "income", ItemCode: "TOTAL_OPERATE_INCOME", ItemValue: 9.1e10, AnnounceDate: day(2025, 8, 15), Source: "eastmoney"},
		{Symbol: "600519.SH", ReportPeriod: "2025Q2", StatementType: "income", ItemCode: "PARENT_NETPROFIT", ItemValue: 4.5e10, AnnounceDate: day(2025, 8, 15), Source: "eastmoney"},
		{Symbol: "600519.SH", ReportPeriod: "2025Q2", StatementType: "balance", ItemCode: "TOTAL_ASSETS", ItemValue: 3.0e11, AnnounceDate: day(2025, 8, 15), Source: "eastmoney"},
	}); err != nil {
		return err
	}

	// 交易日历：故意排到 K 线之后（真实场景里日历是预生成的），
	// 用来验证「该日没有 K 线时回退到最近一根」。
	return write(filepath.Join(curated, "trading_calendar", "trade_date=2026", "part-merged.parquet"), []calendarRow{
		{TradeDate: day(2026, 3, 19), IsTrading: true},
		{TradeDate: day(2026, 3, 20), IsTrading: true},
		{TradeDate: day(2026, 3, 21), IsTrading: false},
		{TradeDate: day(2026, 9, 30), IsTrading: true},
	})
}

type statementRow struct {
	Symbol        string  `parquet:"symbol"`
	ReportPeriod  string  `parquet:"report_period"`
	StatementType string  `parquet:"statement_type"`
	ItemCode      string  `parquet:"item_code"`
	ItemValue     float64 `parquet:"item_value"`
	AnnounceDate  int32   `parquet:"announce_date,date"`
	Source        string  `parquet:"source"`
}

type calendarRow struct {
	TradeDate int32 `parquet:"trade_date,date"`
	IsTrading bool  `parquet:"is_trading"`
}

// 保证 cnlaketest 与 cnlake 的 schema 定义不会悄悄漂移
var (
	_ = cnlake.BarRow{}
	_ = cnlake.ValuationRow{}
	_ = cnlake.ShareStructureRow{}
)
