package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

// **财务页各条取数路径的耗时分解**。
//
// 财务页（GetFinanceSnapshot / GetFinancialStatements）会读好几个湖数据集，
// 先量清楚每一段花多少时间，再决定优化哪里 —— 不然就是瞎猜。
//
// 用户反馈"财务数据页面查询数据也不够快"，这个用例负责给出数字。
func TestFinanceReadTiming(t *testing.T) {
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	lake := cnlake.Open(root)
	svc := NewStockService(config.NewStoreAt(t.TempDir() + "/config.json"))

	const code = "600519"

	// ① 单只财报科目（读 financial_statement_items 的多个分区）
	began := time.Now()
	items, err := lake.StatementItems(nil, code, 200)
	itemElapsed := time.Since(began)
	if err != nil {
		t.Logf("① StatementItems(200) 失败：%v", err)
	} else {
		t.Logf("① StatementItems(%s, limit=200)：%d 条，耗时 %s", code, len(items), itemElapsed.Round(time.Millisecond))
		if len(items) > 0 {
			first := items[0].ReportPeriod
			last := items[len(items)-1].ReportPeriod
			t.Logf("   返回的期间范围：%s ~ %s（从最早开始读，凑够 200 条就停）", first, last)
		}
	}

	began = time.Now()
	itemsAll, err := lake.StatementItems(nil, code, 0)
	allElapsed := time.Since(began)
	if err == nil {
		t.Logf("② StatementItems(%s, limit=0 全部)：%d 条，耗时 %s", code, len(itemsAll), allElapsed.Round(time.Millisecond))
		if len(itemsAll) > 0 {
			t.Logf("   期间范围：%s ~ %s", itemsAll[0].ReportPeriod, itemsAll[len(itemsAll)-1].ReportPeriod)
		}
	}
	if allElapsed > 0 && itemElapsed > 0 {
		t.Logf("   → 读全部是读 200 条的 %.1f 倍", float64(allElapsed)/float64(itemElapsed))
	}

	// ③ 完整快照（财务页实际调的那个）
	began = time.Now()
	snapshot, err := svc.GetFinanceSnapshot(code)
	snapshotElapsed := time.Since(began)
	if err != nil {
		t.Logf("③ GetFinanceSnapshot 失败：%v", err)
	} else {
		t.Logf("③ GetFinanceSnapshot(%s)：耗时 %s（来源 %d 个）",
			code, snapshotElapsed.Round(time.Millisecond), len(snapshot.Sources))
	}

	// ④ 带科目的完整财报
	began = time.Now()
	statements, err := svc.GetFinancialStatements(code, 200)
	stElapsed := time.Since(began)
	if err != nil {
		t.Logf("④ GetFinancialStatements 失败：%v", err)
	} else {
		t.Logf("④ GetFinancialStatements(%s, 200)：耗时 %s（科目 %d 条）",
			code, stElapsed.Round(time.Millisecond), len(statements.Items))
	}

	// ⑤ 财务页另外三个按代码读的数据集（各读一遍整个数据集再过滤）
	type probe struct {
		name string
		run  func() (int, error)
	}
	probes := []probe{
		{"ShareStructures(股本结构)", func() (int, error) {
			rows, err := lake.ShareStructures(code)
			return len(rows), err
		}},
		{"ShareholderCounts(股东户数)", func() (int, error) {
			rows, err := lake.ShareholderCounts(code)
			return len(rows), err
		}},
		{"TopHolders(旧·全历史)", func() (int, error) {
			rows, err := lake.TopHolders(code)
			return len(rows), err
		}},
		{"TopHoldersLatest(新·最新一期)", func() (int, error) {
			rows, err := lake.TopHoldersLatest(code)
			return len(rows), err
		}},
	}
	for _, pr := range probes {
		began := time.Now()
		n, err := pr.run()
		elapsed := time.Since(began)
		if err != nil {
			t.Logf("⑤ %-28s 失败：%v", pr.name, err)
			continue
		}
		t.Logf("⑤ %-28s %d 行，耗时 %s", pr.name, n, elapsed.Round(time.Millisecond))
	}

	// ⑥ **条件选股的"数据湖条件"**：这两个是批量调用，但每次筛选都要付一次全量读
	began = time.Now()
	holders, err := lake.LatestTopHolders(int64(20261008))
	if err != nil {
		t.Logf("⑥ LatestTopHolders 失败：%v", err)
	} else {
		t.Logf("⑥ LatestTopHolders(全市场最新一期)  %d 行，耗时 %s",
			len(holders), time.Since(began).Round(time.Millisecond))
	}
	began = time.Now()
	profits, err := lake.NetProfitsByPeriod()
	if err != nil {
		t.Logf("⑦ NetProfitsByPeriod 失败：%v", err)
	} else {
		t.Logf("⑦ NetProfitsByPeriod(全市场归母净利润)  %d 只，耗时 %s",
			len(profits), time.Since(began).Round(time.Millisecond))
	}

	// 参考：这个数据集有多大
	if files := lake.DatasetFiles("financial_statement_items"); len(files) > 0 {
		t.Logf("参考：financial_statement_items 共 %d 个分区文件", len(files))
	}
}
