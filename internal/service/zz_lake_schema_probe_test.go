package service

import (
	"encoding/json"
	"os"
	"testing"

	"freestock/internal/cnlake"
)

// 数据湖字段探针（默认跳过）：在写新策略测试前先确认列名与样例值。
//
//	$env:FREESTOCK_CA_LAKE="D:\...\cnequity"
//	go test ./internal/service -run TestLakeSchemaProbe -v
func TestLakeSchemaProbe(t *testing.T) {
	root := os.Getenv("FREESTOCK_CA_LAKE")
	if root == "" {
		t.Skip("未设置 FREESTOCK_CA_LAKE")
	}
	lake := cnlake.Open(root)
	for _, name := range []string{
		"valuation_metrics", "financial_statement_items", "instruments",
		"announcement_index", "earnings_disclosure_schedule", "trading_status",
		"share_structure", "daily_bars",
	} {
		rows, err := lake.Rows(name, 3)
		if err != nil {
			t.Logf("%s 读取失败：%v", name, err)
			continue
		}
		raw, _ := json.Marshal(rows)
		t.Logf("%s: %s", name, string(raw))
	}
	if parts := lake.Partitions("valuation_metrics"); len(parts) > 0 {
		t.Logf("valuation 分区数 %d，首 %v 末 %v", len(parts), parts[0], parts[len(parts)-1])
	}
	if values, err := lake.ValuationsOn(20261019); err == nil {
		nonPE, nonPB, nonMV := 0, 0, 0
		for _, v := range values {
			if v.PeTTM != 0 {
				nonPE++
			}
			if v.PB != 0 {
				nonPB++
			}
			if v.TotalMV > 0 {
				nonMV++
			}
		}
		t.Logf("20261019 估值行 %d（PE %d / PB %d / 市值 %d）", len(values), nonPE, nonPB, nonMV)
	}
	for _, date := range []int64{20241014, 20250701, 20260105} {
		if values, err := lake.ValuationsOn(date); err == nil {
			t.Logf("%d 估值行 %d", date, len(values))
		}
	}
	for _, codes := range [][]string{{"600519"}, {"600519", "000001", "300750"}} {
		if rows, err := lake.ValuationsMulti(codes, 2023, 2026); err == nil {
			t.Logf("ValuationsMulti(%v) 行数 %d", codes, len(rows))
		}
	}
	if rows, err := lake.AllShareStructuresSince(2021); err == nil {
		t.Logf("share_structure 行数 %d；样例 %+v", len(rows), rows[:minInt(2, len(rows))])
	}
	if rows, err := lake.AllStatementItems([]string{"total_equity"}, 2021, 2026); err == nil {
		t.Logf("total_equity 行数 %d；样例 %+v", len(rows), rows[:minInt(2, len(rows))])
	}
	if rows, err := lake.AllStatementItems([]string{"net_profit"}, 2022, 2026); err == nil {
		t.Logf("net_profit 行数 %d；样例 %+v", len(rows), rows[:minInt(3, len(rows))])
		shown := 0
		for _, row := range rows {
			if row.Symbol == "600519.SH" && row.ReportPeriod >= "2024Q1" && row.ReportPeriod <= "2025Q4" {
				t.Logf("茅台 %s 净利润 %.0f 公告 %d", row.ReportPeriod, row.ItemValue, row.AnnounceDate)
				shown++
			}
			if shown >= 8 {
				break
			}
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
