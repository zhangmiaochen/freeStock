package cnlake

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 财务科目：全市场一次读取（供「多期限性价比评估」用）
//
// 与 `NetProfitsByPeriod`（只要一个科目）的区别：这里要**一批**科目，
// 而且要在读取时就按科目白名单裁剪 —— 880 万行里真正用得到的只有 21 个
// (statement_type, item_code) 组合，其余全部丢掉可以省下大量内存与 GC。
//
// ⚠ 保留 `announce_date`：**时点正确性完全依赖它**。评估时点之前的财报
// 才是"已知"的，本机这一列 100% 有值（实测 2025Q4 的 161676 行全部非空），
// 所以门控是硬的。没有公告日的行会被上层丢弃。
// ---------------------------------------------------------------------------

// StatementRow 是 `financial_statement_items` 的一行（扁平化视图）。
//
// 字段名刻意与 valuation 包的 Report 解耦：cnlake 不认识"报告期怎么算 TTM"，
// 它只负责把行读出来。
type StatementRow struct {
	Symbol        string  `json:"symbol"`
	ReportPeriod  string  `json:"reportPeriod"`
	StatementType string  `json:"statementType"`
	ItemCode      string  `json:"itemCode"`
	ItemValue     float64 `json:"itemValue"`
	AnnounceDate  int64   `json:"announceDate"` // YYYYMMDD，0 表示缺失
}

// AllStatementItems 一次读出全市场的指定财务科目。
//
// items 为空时读取全部科目（不推荐：会有 880 万行 × 30 个科目组合）。
// 返回顺序为文件顺序（按 report_period 分区，即按报告期升序），
// 调用方不要依赖它——按需自建索引。
//
// 性能：列裁剪（只读 6 列）+ 读取时按科目白名单过滤 + 32 路并发。
// 实测全量读取约 1.5~3 秒（880 万行的列式解码是 CPU 密集的，
// 见 readTyped 的说明）。
func (l *Lake) AllStatementItems(items []string) ([]StatementRow, error) {
	const dataset = "financial_statement_items"
	if !l.HasDataset(dataset) {
		return nil, fmt.Errorf("cnlake: %s 尚未入库", dataset)
	}
	files := l.DatasetFiles(dataset)
	if len(files) == 0 {
		return nil, nil
	}

	// 白名单按 "statement_type/item_code" 建索引，读取时 O(1) 判断
	want := map[string]struct{}{}
	for _, item := range items {
		if item == "" {
			continue
		}
		want[strings.ToLower(item)] = struct{}{}
	}
	all := len(want) == 0

	rows, err := readTyped[financeStatementRow](files, func(row *financeStatementRow) bool {
		if row.ItemCode == "" || row.ReportPeriod == "" {
			return false
		}
		if all {
			return true
		}
		_, ok := want[strings.ToLower(row.ItemCode)]
		return ok
	})
	if err != nil {
		return nil, err
	}

	out := make([]StatementRow, 0, len(rows))
	for _, row := range rows {
		item := StatementRow{
			Symbol:        row.Symbol,
			ReportPeriod:  row.ReportPeriod,
			StatementType: row.StatementType,
			ItemCode:      row.ItemCode,
			ItemValue:     row.ItemValue,
		}
		if row.AnnounceDate > 0 {
			item.AnnounceDate = daysToDate(row.AnnounceDate)
		}
		out = append(out, item)
	}
	return out, nil
}

// AllStatementItemsSorted 与 AllStatementItems 相同，但结果按
// (证券代码, 报告期) 升序返回，便于上层顺序建索引。
func (l *Lake) AllStatementItemsSorted(items []string) ([]StatementRow, error) {
	rows, err := l.AllStatementItems(items)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Symbol != rows[j].Symbol {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].ReportPeriod < rows[j].ReportPeriod
	})
	return rows, nil
}

// AllIndexConstituents 读出全部指数成分（当前只有 1 个快照）。
//
// 返回 index_symbol -> 成分（按权重降序）。
func (l *Lake) AllIndexConstituents() (map[string][]IndexConstituentRow, error) {
	const dataset = "index_constituents"
	if !l.HasDataset(dataset) {
		return nil, fmt.Errorf("cnlake: %s 尚未入库", dataset)
	}
	rows, err := readTyped[IndexConstituentRow](l.DatasetFiles(dataset), nil)
	if err != nil {
		return nil, err
	}
	out := map[string][]IndexConstituentRow{}
	for _, row := range rows {
		if row.IndexSymbol == "" || row.Symbol == "" {
			continue
		}
		out[row.IndexSymbol] = append(out[row.IndexSymbol], row)
	}
	for key := range out {
		sort.Slice(out[key], func(i, j int) bool { return out[key][i].Weight > out[key][j].Weight })
	}
	return out, nil
}

// IndexConstituentRow 是 `index_constituents` 的一行。
type IndexConstituentRow struct {
	IndexSymbol string  `parquet:"index_symbol"`
	Symbol      string  `parquet:"symbol"`
	AsOfDate    string  `parquet:"as_of_date"`
	Weight      float64 `parquet:"weight"`
}
