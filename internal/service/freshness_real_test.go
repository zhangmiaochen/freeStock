package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/cne"
	"freestock/internal/config"
)

// **用真实数据湖验证新鲜度检查**。
//
// 这是漏日补跑能不能"真的发现缺口"的关键 —— 方案 §2.2 实测：
// `--stale-only` 算出的落后清单里**没有 daily_bars**，而行情当时停在
// 2026-10-08、日历上 10-09 是交易日。只看 stale 的结论会以为没落后。
//
// 这里直接对真实数据湖跑 Check，把它报出来的东西打出来人工核对。
func TestCheckFreshnessOnRealLake(t *testing.T) {
	root := os.Getenv("FREESTOCK_LAKE_ROOT")
	if root == "" {
		root = config.NewStore().CnLakeRoot()
	}
	if root == "" {
		t.Skip("没有数据湖根目录：设 FREESTOCK_LAKE_ROOT 或配置 cnLakeRoot")
	}
	if _, err := os.Stat(filepath.Join(root, "curated", "trading_calendar")); err != nil {
		t.Skipf("数据湖没有 trading_calendar：%v", err)
	}

	report, err := cne.Check(root, time.Now())
	if err != nil {
		t.Fatalf("新鲜度检查失败：%v", err)
	}

	t.Logf("最后已收盘交易日：%d", report.LastTrading)
	for _, d := range report.Datasets {
		if d.Missing {
			t.Logf("  %-18s 未入库", d.Name)
			continue
		}
		status := "已最新"
		if len(d.BehindDays) > 0 {
			status = "落后"
		}
		t.Logf("  %-18s 最新 %d  %s", d.Name, d.LatestDate, status)
		if len(d.BehindDays) > 0 {
			t.Logf("  %-18s   缺 %v", "", d.BehindDays)
		}
	}
	t.Logf("需要补的交易日（并集）：%v", report.BehindDays)
	t.Logf("结论：%s", report.Summary)

	// 决策也要跑一遍，确认不会一开机就跑几小时
	decision := cne.DecideCatchUp(report)
	t.Logf("补跑决策：ShouldRun=%v Auto=%v 原因=%s",
		decision.ShouldRun, decision.Auto, decision.Reason)

	// 硬断言：报告必须自洽
	if report.LastTrading <= 0 {
		t.Error("最后已收盘交易日必须 > 0")
	}
	// 最后交易日不能是"今天"或更晚 —— 方案明确：当天数据盘后也不一定齐，
	// 别把今天算成"应该有"，否则每天都报假落后。
	today := int64(time.Now().Year()*10000 + int(time.Now().Month())*100 + time.Now().Day())
	if report.LastTrading >= today {
		t.Errorf("最后已收盘交易日 %d 不应 >= 今天 %d（方案 §4.3 的口径）",
			report.LastTrading, today)
	}
}
