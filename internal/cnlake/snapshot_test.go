package cnlake

import "testing"

// latestSnapshot 是「按 as_of_date 分区的每日快照」这类数据集的裁剪规则，
// 这里用最小的行结构把它的三条规则钉死：
//  1. 每个**实体各自**取最新一份快照（不取全局最新，避免缺席的实体整块消失）；
//  2. 同一份快照内的重复成员只留一条；
//  3. 成分代码按 BareSymbol 归一化后比较（000001 与 000001.SZ 是同一个人）。
func TestLatestSnapshotKeepsNewestPerEntity(t *testing.T) {
	type row struct {
		board  string
		asOf   int32
		symbol string
	}
	rows := []row{
		// 板块 A：旧快照有 STALE，新快照把它调出了
		{board: "A", asOf: 20260101, symbol: "000001.SZ"},
		{board: "A", asOf: 20260101, symbol: "300750.SZ"},
		{board: "A", asOf: 20260201, symbol: "000001.SZ"},
		// 板块 B：只出现在旧快照里 —— 取全局最新会让它整个消失，必须仍按自己的最新取
		{board: "B", asOf: 20260101, symbol: "600519.SH"},
		// 板块 C：新快照里有一条完全重复的成分行
		{board: "C", asOf: 20260201, symbol: "000002.SZ"},
		{board: "C", asOf: 20260201, symbol: "000002"},
		// 板块 D：快照日期缺失（全 0），等价于只有一份，全部保留
		{board: "D", asOf: 0, symbol: "430047.BJ"},
	}

	got := latestSnapshot(rows,
		func(r row) string { return r.board },
		func(r row) int32 { return r.asOf },
		func(r row) string { return BareSymbol(r.symbol) },
	)

	seen := map[string][]int32{}
	for _, item := range got {
		seen[item.board] = append(seen[item.board], item.asOf)
	}
	if len(seen["A"]) != 1 || seen["A"][0] != 20260201 {
		t.Errorf("板块 A 应只留新快照的 1 条，实际 %v", seen["A"])
	}
	if len(seen["B"]) != 1 || seen["B"][0] != 20260101 {
		t.Errorf("板块 B 不在最新快照里，应保留它自己的最新一条，实际 %v", seen["B"])
	}
	if len(seen["C"]) != 1 {
		t.Errorf("板块 C 的重复成分应去重，实际 %v", seen["C"])
	}
	if len(seen["D"]) != 1 {
		t.Errorf("没有快照日期的行不该被丢掉，实际 %v", seen["D"])
	}
	if len(got) != 4 {
		t.Errorf("期望 4 条，实际 %d: %+v", len(got), got)
	}
}
