package portfolio

import (
	"testing"
)

// `signal_history` 的过滤 / 去重 / 计数语义。
//
// 这几条规则决定"哪些信号会被贴到评分行上"，错一条就会让候选说明里的
// `active_signal_*` 与 Python 侧不一致：
//
//	只取 buy / candidate_buy      （sell 不进候选）
//	ts_code 必须匹配 A 股主力板段   （北交所/指数被剔除）
//	(trade_date, ts_code) 去重      buy 优先于 candidate_buy，再按分数、模型
//	signal_count 是**去重前**的条数  （原实现 groupby().transform("size")）
func TestSignalHistoryFilterDedupAndCount(t *testing.T) {
	store := newStore(t)

	const day int64 = 20261009
	rows := []SignalHistoryRow{
		// 同一天同票：buy(88) 与 candidate_buy(95) → buy 胜出，count=2
		{TradeDate: day, Code: "600000.SH", ModelID: "box_range", ModelName: "箱体低吸高抛",
			SignalType: "candidate_buy", SignalLevel: "candidate", SignalLevelLabel: "候选", Score: 95.0},
		{TradeDate: day, Code: "600000.SH", ModelID: "trend_pullback", ModelName: "趋势缩量回踩",
			SignalType: "buy", SignalLevel: "confirmed", SignalLevelLabel: "确认", Score: 88.0},
		// sell 不进候选
		{TradeDate: day, Code: "600001.SH", ModelID: "box_range",
			SignalType: "sell", SignalLevel: "confirmed", Score: 76.0},
		// 北交所 → 被代码规则剔除
		{TradeDate: day, Code: "430047.BJ", ModelID: "trend_pullback",
			SignalType: "buy", SignalLevel: "confirmed", Score: 80.0},
		// 创业板 + 科创板 → 保留
		{TradeDate: day, Code: "300750.SZ", ModelID: "box_range", ModelName: "箱体低吸高抛",
			SignalType: "buy", SignalLevel: "confirmed", SignalLevelLabel: "确认", Score: 92.0},
		{TradeDate: day, Code: "688981.SH", ModelID: "trend_pullback",
			SignalType: "candidate_buy", SignalLevel: "candidate", Score: 71.0},
	}
	if err := store.SaveSignalHistory(rows); err != nil {
		t.Fatalf("写入信号失败：%v", err)
	}

	count, err := store.SignalHistoryCount()
	if err != nil {
		t.Fatalf("统计信号失败：%v", err)
	}
	if count != len(rows) {
		t.Fatalf("信号总条数：go %d，期望 %d", count, len(rows))
	}

	latest, err := store.LatestSignalDate()
	if err != nil {
		t.Fatalf("查询最新信号日期失败：%v", err)
	}
	if latest != day {
		t.Errorf("最新信号日期：go %d，期望 %d", latest, day)
	}

	got, err := store.LoadSignalCandidates(day, day)
	if err != nil {
		t.Fatalf("读取候选失败：%v", err)
	}
	byCode := map[string]SignalCandidate{}
	for _, item := range got {
		byCode[item.Code] = item
	}
	// ⚠ `SignalCandidate.Code` 是**裸 6 位代码**：表里存 tushare 格式（带后缀），
	// join 前归一化，所以这里的键要用裸码。
	if _, exists := byCode["430047"]; exists {
		t.Error("北交所代码不应进入候选")
	}
	if _, exists := byCode["600001"]; exists {
		t.Error("sell 信号不应进入候选")
	}
	if len(got) != 3 {
		t.Fatalf("候选条数：go %d，期望 3（600000/300750/688981）", len(got))
	}

	merged := byCode["600000"]
	if merged.SignalType != "buy" {
		t.Errorf("同票同日应保留 buy，实际 %q", merged.SignalType)
	}
	if merged.Score != 88.0 {
		t.Errorf("保留的应是 buy 的分数 88，实际 %v", merged.Score)
	}
	if merged.SignalCount != 2 {
		t.Errorf("signal_count 应为去重前的 2，实际 %d", merged.SignalCount)
	}
	if byCode["688981"].SignalCount != 1 {
		t.Errorf("单条信号的 signal_count 应为 1，实际 %d", byCode["688981"].SignalCount)
	}

	// 幂等：同一批再写一次不应让条数翻倍（主键 INSERT OR REPLACE）。
	if err := store.SaveSignalHistory(rows); err != nil {
		t.Fatalf("重复写入失败：%v", err)
	}
	again, err := store.SignalHistoryCount()
	if err != nil {
		t.Fatalf("统计信号失败：%v", err)
	}
	if again != len(rows) {
		t.Errorf("重复写入后条数：go %d，期望仍为 %d", again, len(rows))
	}

	// 清理：删掉该日之后的信号应当清空。
	removed, err := store.DeleteSignalHistoryFrom(day)
	if err != nil {
		t.Fatalf("清理信号失败：%v", err)
	}
	if removed != int64(len(rows)) {
		t.Errorf("清理条数：go %d，期望 %d", removed, len(rows))
	}
	after, _ := store.SignalHistoryCount()
	if after != 0 {
		t.Errorf("清理后仍剩 %d 条", after)
	}
}

// 代码白名单必须与原实现的正则完全一致 —— 多放一个板段或少放一个，
// 都会让"哪些票能吃到信号加分"与 Python 侧不同。
func TestSignalCodeAllowedMatchesOriginalRegex(t *testing.T) {
	allowed := []string{
		"600000.SH", "601398.SH", "603288.SH", "605499.SH",
		"688981.SH", "689009.SH",
		"000001.SZ", "001979.SZ", "002594.SZ", "003816.SZ",
		"300750.SZ", "301236.SZ",
		// ⚠ 原正则只看"前缀 + 形状"，不看交易所是否自洽：
		// `000300.SH`（沪深300指数）前缀是 000、后缀是 SH，所以**会通过**。
		// 我把这条先写成"应当拒绝"，结果测试挂了 —— 是我的预期错，不是实现错。
		"000300.SH",
	}
	for _, code := range allowed {
		if !SignalCodeAllowed(code) {
			t.Errorf("%s 应当被允许", code)
		}
	}
	rejected := []string{
		"430047.BJ", "830799.BJ", "870508.BJ", // 北交所
		"399001.SZ", "399006.SZ", // 指数（前缀不在白名单里）
		"900901.SH", "200011.SZ", // B 股
		"600000SZ", "600000.SZ9", "60000.SZ", "6000000.SZ", // 长度/分隔符不对
		"60000A.SH", // 数字位含字母
		"", "abc",
	}
	for _, code := range rejected {
		if SignalCodeAllowed(code) {
			t.Errorf("%s 不应当被允许", code)
		}
	}
}
