package service

import (
	"context"
	"testing"
)

// 「不限起点补抓」的结果缓存：同一天里历史最后几根 K 线不会变，
// 缓存之后重复构建全量列表（每 10 分钟一次）不必再扫整个 daily_bars。
//
// 这条用例盯两件事：
//  1. 缓存命中时**结果完全一致**（口径不能因为缓存而变）；
//  2. 缓存确实被填上了（quotes + empty 两份索引）。
func TestLakeRetryQuotesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要读数据湖）")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	provider := NewLakeProvider(svc.lakeHandle()).(*lakeProvider)
	ctx := context.Background()

	date, err := provider.LatestTradeDate(ctx)
	if err != nil || date == 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	dateInt := int(date)

	// 取一批"最近窗口内没有数据"的代码：先用 RecentQuotes 找出缺口
	recent, missed, err := provider.bulkQuotes([]string{"000001", "600519"}, shiftBack(dateInt, quoteLookbackDays), dateInt)
	if err != nil {
		t.Fatalf("bulkQuotes 失败: %v", err)
	}
	_ = recent
	_ = missed

	// 直接从 instruments 里挑几个"已退市"的代码（它们就是这条路的常客）
	instruments, err := svc.lakeHandle().Instruments()
	if err != nil {
		t.Fatalf("读取 instruments 失败: %v", err)
	}
	delisted := make([]string, 0, 8)
	for _, row := range instruments {
		if row.DelistDate > 0 && len(delisted) < 5 {
			delisted = append(delisted, row.Symbol)
		}
	}
	if len(delisted) == 0 {
		t.Skip("数据湖里没有带退市日期的标的")
	}

	// 第一次：真实扫描（可能什么也找不到，那也是有效结果）
	first, err := provider.retryQuotes(delisted, dateInt)
	if err != nil {
		t.Fatalf("第一次 retryQuotes 失败: %v", err)
	}
	provider.retryMu.Lock()
	cache := provider.retryCache
	provider.retryMu.Unlock()
	if cache == nil || cache.date != dateInt {
		t.Fatalf("第一次调用之后应当留下缓存: %+v", cache)
	}
	filled, empty := len(cache.quotes), len(cache.empty)
	if filled+empty == 0 {
		t.Error("缓存里既没有行情也没有「确认无数据」的代码，等于什么都没记住")
	}
	t.Logf("第一次：得到 %d 条行情，%d 个确认无数据（缓存已就绪）", filled, empty)

	// 第二次：必须完全走缓存、结果一致
	second, err := provider.retryQuotes(delisted, dateInt)
	if err != nil {
		t.Fatalf("第二次 retryQuotes 失败: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("缓存前后结果条数不一致：%d vs %d", len(first), len(second))
	}
	for code, quote := range first {
		other, ok := second[code]
		if !ok {
			t.Errorf("%s 第二次没返回", code)
			continue
		}
		if *quote != *other {
			t.Errorf("%s 缓存前后行情不一致：\n%+v\n%+v", code, quote, other)
		}
	}

	// 子集请求：也应当由缓存直接满足（不需要再扫）
	subset := []string{delisted[0]}
	provider.retryMu.Lock()
	before := provider.retryCache
	provider.retryMu.Unlock()
	if _, err := provider.retryQuotes(subset, dateInt); err != nil {
		t.Fatalf("子集请求失败: %v", err)
	}
	provider.retryMu.Lock()
	after := provider.retryCache
	provider.retryMu.Unlock()
	if after != before {
		t.Error("子集请求不该重建缓存（说明它又扫了一遍）")
	}
}
