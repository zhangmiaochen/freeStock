package cne

import (
	"fmt"
	"sort"
	"time"

	"freestock/internal/cnlake"
)

// KeyDatasets 是判断"数据更没更新"的关键数据集。
//
// 以**行情组**为主：方案 §2.2 实测 `--stale-only` 算出的落后清单里
// **根本没有 daily_bars**，而当时行情数据集全部停在 2026-10-08 ——
// 也就是说只看 stale 的结论会以为"没落后"，实际行情缺了一天。
var KeyDatasets = []string{
	"daily_bars",
	"index_bars",
	"adj_factors",
	"trading_status",
	"sector_bars",
}

// DatasetFreshness 是一个数据集的新鲜度。
type DatasetFreshness struct {
	Name string `json:"name"`
	// LatestDate 是数据集里实际的最新日期（YYYYMMDD，0 表示没有数据）
	LatestDate int64 `json:"latestDate"`
	// LastTrading 是日历上**最后一个已收盘交易日**（YYYYMMDD）
	LastTrading int64 `json:"lastTrading"`
	// BehindDays 是缺失的交易日（升序）。空表示不落后。
	BehindDays []int64 `json:"behindDays"`
	// Missing 表示数据集根本没入库
	Missing bool `json:"missing"`
}

// BehindCount 是落后的交易日数。
func (f DatasetFreshness) BehindCount() int { return len(f.BehindDays) }

// FreshnessReport 是整体结论。
type FreshnessReport struct {
	Datasets []DatasetFreshness `json:"datasets"`
	// LastTrading 是日历上最后一个已收盘交易日
	LastTrading int64 `json:"lastTrading"`
	// MaxBehind 是所有数据集里落后的**最多**交易日数
	MaxBehind int `json:"maxBehind"`
	// BehindDays 是需要补的交易日（各数据集的并集，升序）
	BehindDays []int64 `json:"behindDays"`
	// Summary 是人类可读的一句话
	Summary string `json:"summary"`
}

// Check 检查数据湖里关键数据集的新鲜度。
//
// today 传入当前时间；**"最后已收盘交易日"取日历里 is_trading=true 且
// 日期 < 今天 的最大值** —— 方案明确：当天的数据在盘后也不一定齐，
// 别把今天算成"应该有"，否则每天都会报一次假落后。
func Check(root string, today time.Time) (*FreshnessReport, error) {
	lake := cnlake.Open(root)
	if !lake.HasDataset("trading_calendar") {
		return nil, fmt.Errorf("数据湖里没有 trading_calendar，无法判断该有哪些交易日")
	}
	calendar, err := lake.TradingCalendar()
	if err != nil {
		return nil, err
	}
	if len(calendar) == 0 {
		return nil, fmt.Errorf("trading_calendar 里没有交易日")
	}

	todayNum := int64(today.Year()*10000 + int(today.Month())*100 + today.Day())
	lastTrading := int64(0)
	for _, date := range calendar {
		if date < todayNum && date > lastTrading {
			lastTrading = date
		}
	}
	if lastTrading == 0 {
		return nil, fmt.Errorf("日历里找不到早于 %d 的交易日", todayNum)
	}

	report := &FreshnessReport{LastTrading: lastTrading}
	union := map[int64]bool{}

	for _, name := range KeyDatasets {
		item := DatasetFreshness{Name: name, LastTrading: lastTrading}
		if !lake.HasDataset(name) {
			item.Missing = true
			// 没入库的数据集不参与"补哪几天"的计算 —— 那是"没建库"，
			// 不是"漏了几天"，补跑也补不出来。
			report.Datasets = append(report.Datasets, item)
			continue
		}
		item.LatestDate = lake.LatestDatasetDate(name)
		if item.LatestDate > 0 && item.LatestDate < lastTrading {
			for _, date := range calendar {
				if date > item.LatestDate && date <= lastTrading {
					item.BehindDays = append(item.BehindDays, date)
					union[date] = true
				}
			}
		}
		if len(item.BehindDays) > report.MaxBehind {
			report.MaxBehind = len(item.BehindDays)
		}
		report.Datasets = append(report.Datasets, item)
	}

	for date := range union {
		report.BehindDays = append(report.BehindDays, date)
	}
	sort.Slice(report.BehindDays, func(i, j int) bool { return report.BehindDays[i] < report.BehindDays[j] })
	report.Summary = summarize(report)
	return report, nil
}

func summarize(report *FreshnessReport) string {
	if len(report.BehindDays) == 0 {
		return fmt.Sprintf("数据已是最新（最后交易日 %d）", report.LastTrading)
	}
	if len(report.BehindDays) <= 3 {
		return fmt.Sprintf("落后 %d 个交易日（%v），最后交易日应为 %d —— "+
			"需要按日补跑：普通日更和「补抓落后」都不会补行情缺口，只有 --trade-date 能补上。",
			len(report.BehindDays), report.BehindDays, report.LastTrading)
	}
	return fmt.Sprintf("落后 %d 个交易日（%d ~ %d）。缺口较多，"+
		"建议确认后再补，避免一开机就跑很久。",
		len(report.BehindDays), report.BehindDays[0], report.BehindDays[len(report.BehindDays)-1])
}

// CatchUpDecision 是"要不要在启动时自动补"的判断结果。
//
// 方案 §5.2 的策略：缺失 1~3 天自动补；更多则只提示、由用户确认 ——
// 避免一开机就跑几个小时。
type CatchUpDecision struct {
	ShouldRun bool    `json:"shouldRun"`
	Auto      bool    `json:"auto"` // true=自动补，false=只提示
	Dates     []int64 `json:"dates"`
	Reason    string  `json:"reason"`
}

const autoCatchUpMaxDays = 3

// DecideCatchUp 根据报告决定要不要补、自动还是等确认。
func DecideCatchUp(report *FreshnessReport) CatchUpDecision {
	if report == nil || len(report.BehindDays) == 0 {
		return CatchUpDecision{Reason: "数据已是最新，无需补跑"}
	}
	if len(report.BehindDays) <= autoCatchUpMaxDays {
		return CatchUpDecision{
			ShouldRun: true, Auto: true, Dates: report.BehindDays,
			Reason: fmt.Sprintf("落后 %d 个交易日，启动时自动补跑", len(report.BehindDays)),
		}
	}
	return CatchUpDecision{
		ShouldRun: false, Auto: false, Dates: report.BehindDays,
		Reason: fmt.Sprintf("落后 %d 个交易日，缺口较多 —— 需要你确认后再补"+
			"（补跑要按日逐个执行，可能很久）", len(report.BehindDays)),
	}
}
