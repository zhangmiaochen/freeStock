package cnlake

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// 本文件用一个**自建的小数据湖**钉住"行业归属的时点语义"，不依赖本机真实数据湖。
//
// 为什么必须自建：真实数据湖里 industry_members 是"日更 + 申万回填"混出来的，
// 快照日期每月都在变；要验证"某一天它属于哪个行业"这条逻辑，
// 需要**可控的多快照 + 双体系**数据。
//
// ⚠ 不用 cnlaketest.Build 的原因：那个合成湖被 5 个测试文件共用，
// 改它会牵连既有用例；而且它按**年**分区，验不了"月末离散快照 + 前向填充"。

// dayValue 把年月日折成 parquet DATE 的天数。
func dayValue(y, m, d int) int32 {
	return dateToDays(int64(y*10000 + m*100 + d))
}

// writeIndustryFixture 造一个只含 industry_members 的小数据湖。
//
// 数据设计（**每一行都为了验证一条规则**）：
//
//	体系 A（"alpha"，3 个快照）：2024-01-31 / 2024-02-29 / 2024-03-31
//	  600001 在 A 里**变更过行业**：P1 → P2 → P2   ← 验"取该日生效的那一份"
//	  600002 全程 P1                              ← 验前向填充
//	体系 B（"beta"，1 个快照）：2024-03-31
//	  600003 只在 B 里有记录                       ← 验"补缺"与"不跳体系"
//
// 于是：
//
//	asOf=20240115（早于 A 的第一份）→ 该体系无归属，不得回退到今天
//	asOf=20240131 → 600001=P1, 600002=P1
//	asOf=20240215 → 沿用 2024-01-31（前向填充）→ 600001=P1
//	asOf=20240229 → 600001=P2（变更生效）
//	asOf=20240315 → 仍沿用 2024-02-29 → 600001=P2
func writeIndustryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "curated", "industry_members")
	mk := func(name string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
	}
	type row = IndustryMemberRow
	write := func(partition string, rows []row) {
		mk(partition)
		path := filepath.Join(dir, partition, "part-merged.parquet")
		if err := parquet.WriteFile(path, rows); err != nil {
			t.Fatalf("写 parquet 失败: %v", err)
		}
	}

	write("as_of_date=2024-01-31", []row{
		{Symbol: "600001.SH", ClassificationSystem: "alpha", IndustryCode: "P1", IndustryName: "P1", AsOfDate: dayValue(2024, 1, 31)},
		{Symbol: "600002.SH", ClassificationSystem: "alpha", IndustryCode: "P1", IndustryName: "P1", AsOfDate: dayValue(2024, 1, 31)},
	})
	write("as_of_date=2024-02-29", []row{
		{Symbol: "600001.SH", ClassificationSystem: "alpha", IndustryCode: "P2", IndustryName: "P2", AsOfDate: dayValue(2024, 2, 29)},
		{Symbol: "600002.SH", ClassificationSystem: "alpha", IndustryCode: "P1", IndustryName: "P1", AsOfDate: dayValue(2024, 2, 29)},
	})
	write("as_of_date=2024-03-31", []row{
		{Symbol: "600001.SH", ClassificationSystem: "alpha", IndustryCode: "P2", IndustryName: "P2", AsOfDate: dayValue(2024, 3, 31)},
		{Symbol: "600002.SH", ClassificationSystem: "alpha", IndustryCode: "P1", IndustryName: "P1", AsOfDate: dayValue(2024, 3, 31)},
		{Symbol: "600003.SH", ClassificationSystem: "beta", IndustryCode: "Q9", IndustryName: "Q9", AsOfDate: dayValue(2024, 3, 31)},
	})
	return root
}

func industryCodeFor(snapshot IndustrySnapshot, symbol string) string {
	for _, row := range snapshot.Members {
		if BareSymbol(row.Symbol) == symbol {
			return row.ClassificationSystem + ":" + row.IndustryCode
		}
	}
	return ""
}

// TestIndustryMembersAsOfTakesEffectiveSnapshot 钉住"取该日生效的那一份快照"。
//
// 这是消除行业归属**前视偏差**的核心：用今天的名单去回测 2022 年，
// 等于把后来才纳入行业/才改归属的公司提前算进去。
func TestIndustryMembersAsOfTakesEffectiveSnapshot(t *testing.T) {
	lake := Open(writeIndustryFixture(t))

	cases := []struct {
		asOf   int64
		want1  string // 600001
		want2  string // 600002
		reason string
	}{
		{20240115, "", "", "早于第一份快照：不得回退到最新，宁可没有归属"},
		{20240131, "alpha:P1", "alpha:P1", "第一份快照当天（含）即生效"},
		{20240215, "alpha:P1", "alpha:P1", "两次快照之间：前向填充上一份"},
		{20240229, "alpha:P2", "alpha:P1", "第二份快照当天：行业变更生效"},
		{20240315, "alpha:P2", "alpha:P1", "仍沿用 2024-02-29（不是 03-31）"},
		{20240331, "alpha:P2", "alpha:P1", "第三份快照当天"},
		{20240630, "alpha:P2", "alpha:P1", "晚于最后一份：沿用最后一份"},
	}
	for _, item := range cases {
		snapshot := lake.IndustryMembersAsOf(item.asOf, "alpha")
		if snapshot.System != "alpha" {
			t.Errorf("asOf=%d 选中体系 %q，应为 alpha", item.asOf, snapshot.System)
		}
		if got := industryCodeFor(snapshot, "600001"); got != item.want1 {
			t.Errorf("asOf=%d 600001 的归属 = %q，应为 %q（%s）", item.asOf, got, item.want1, item.reason)
		}
		if got := industryCodeFor(snapshot, "600002"); got != item.want2 {
			t.Errorf("asOf=%d 600002 的归属 = %q，应为 %q（%s）", item.asOf, got, item.want2, item.reason)
		}
	}
}

// TestIndustryMembersAsOfDoesNotMixSystems 钉住"跨体系只能补缺，不能在时间轴上跳"。
func TestIndustryMembersAsOfDoesNotMixSystems(t *testing.T) {
	lake := Open(writeIndustryFixture(t))

	// 选定 alpha：600003 在 alpha 里没有记录 → 用 beta 补一次
	snapshot := lake.IndustryMembersAsOf(20240331, "alpha")
	if got := industryCodeFor(snapshot, "600003"); got != "beta:Q9" {
		t.Errorf("600003 只存在于 beta，应被补进来，实际 %q", got)
	}
	if got := industryCodeFor(snapshot, "600001"); got != "alpha:P2" {
		t.Errorf("600001 在 alpha 里有记录，不应被 beta 覆盖，实际 %q", got)
	}
	if len(snapshot.ExcludedSystems) == 0 {
		t.Error("使用了其它体系补缺时必须说明（ExcludedSystems 用于界面提示）")
	}
	t.Logf("补缺说明: %v", snapshot.ExcludedSystems)

	// 选定 beta：600001/600002 在 beta 里没有记录，会被补进来；
	// 但 beta 自己那份快照里的 600003 必须来自 beta
	snapshot = lake.IndustryMembersAsOf(20240331, "beta")
	if got := industryCodeFor(snapshot, "600003"); got != "beta:Q9" {
		t.Errorf("选定 beta 时 600003 应来自 beta，实际 %q", got)
	}
	// 早于 beta 唯一快照的日期：beta 内无归属，补缺才可能给出结果
	early := lake.IndustryMembersAsOf(20240115, "beta")
	if got := industryCodeFor(early, "600003"); got != "" {
		t.Errorf("asOf=20240115 时 beta 内 600003 不该有归属（不拿今天填昨天），实际 %q", got)
	}
}

// TestPickIndustrySystemPrefersLongerHistory 钉住体系推荐规则。
//
// 快照数直接决定"能不能算历史分位"，所以它是第一优先级；
// 只有快照数相同时才看名称可读性。
func TestPickIndustrySystemPrefersLongerHistory(t *testing.T) {
	lake := Open(writeIndustryFixture(t))

	usages := lake.IndustrySystems()
	if len(usages) != 2 {
		t.Fatalf("应识别出 2 个体系，实际 %d 个: %+v", len(usages), usages)
	}
	// 排序：快照数多的在前
	if usages[0].System != "alpha" || usages[1].System != "beta" {
		t.Errorf("体系应按快照数降序，实际 %s / %s", usages[0].System, usages[1].System)
	}
	if usages[0].SnapshotDates != 3 || usages[1].SnapshotDates != 1 {
		t.Errorf("快照数应为 3 / 1，实际 %d / %d", usages[0].SnapshotDates, usages[1].SnapshotDates)
	}
	// 两个体系的 name 都等于 code → HasNames 都应为 false
	for _, usage := range usages {
		if usage.HasNames {
			t.Errorf("体系 %s 的 industry_name 等于代码，HasNames 应为 false", usage.System)
		}
	}
	if picked := lake.PickIndustrySystem(); picked != "alpha" {
		t.Errorf("应选中快照数多的 alpha，实际 %q", picked)
	}

	// 空体系名 → 用推荐值，不应 panic 或返回空
	snapshot := lake.IndustryMembersAsOf(20240229, "")
	if snapshot.System != "alpha" {
		t.Errorf("system 为空时应回落到推荐体系 alpha，实际 %q", snapshot.System)
	}
}

// TestIndustryMembersByDateRange 钉住快照日枚举（行业历史序列的样本日期来源）。
func TestIndustryMembersByDateRange(t *testing.T) {
	lake := Open(writeIndustryFixture(t))

	all := lake.IndustryMembersByDateRange("alpha", 0, 0)
	want := []int64{20240131, 20240229, 20240331}
	if len(all) != len(want) {
		t.Fatalf("alpha 的全部快照日 = %v，应为 %v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Errorf("第 %d 个快照日 = %d，应为 %d（必须升序）", i, all[i], want[i])
		}
	}
	// 区间裁剪
	part := lake.IndustryMembersByDateRange("alpha", 20240201, 20240301)
	if len(part) != 1 || part[0] != 20240229 {
		t.Errorf("[20240201,20240301] 内应只有 20240229，实际 %v", part)
	}
	// 体系名未知 → 走推荐体系，不应返回 nil
	if got := lake.IndustryMembersByDateRange("", 0, 0); len(got) != 3 {
		t.Errorf("体系名为空时应走推荐体系，实际 %v", got)
	}
}

// TestIndustryMembersUntouched 钉住**既有语义没有被改动**。
//
// `IndustryMembers()` 是"每个行业最新一份快照"，服务于股票列表 / 板块行情 /
// 条件选股。为了做时点评估而改它的语义会静默影响那些功能，所以它必须保持原样。
func TestIndustryMembersUntouched(t *testing.T) {
	lake := Open(writeIndustryFixture(t))

	rows, err := lake.IndustryMembers()
	if err != nil {
		t.Fatalf("IndustryMembers 失败: %v", err)
	}
	// 两套体系、两个行业（alpha:P1/P2、beta:Q9），各取最新快照
	codes := map[string]bool{}
	for _, row := range rows {
		codes[row.ClassificationSystem+":"+row.IndustryCode] = true
	}
	if !codes["alpha:P1"] || !codes["alpha:P2"] || !codes["beta:Q9"] {
		t.Errorf("IndustryMembers 应返回各行业最新快照，实际 %v", codes)
	}
	// 600001 的最新是 P2，所以 P1 里不该再有它
	for _, row := range rows {
		if BareSymbol(row.Symbol) == "600001" && row.IndustryCode == "P1" {
			t.Error("IndustryMembers 应只保留每个行业的最新快照：600001 不应还留在 P1 里")
		}
	}
}
