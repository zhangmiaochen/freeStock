package volprice

import (
	"encoding/json"
	"strings"
	"testing"
)

// `_collect_signal_rows` 的筛选与去重规则。
//
// 这些规则决定"哪些信号真的进了表"：
//
//	只收 buy / candidate_buy
//	trade_date 必须是合法 8 位数字
//	严格大于 min_trade_date_exclusive（增量刷新用）
//	去重键 (ts_code, trade_date, model_id, signal_type) —— **含 model_id**，
//	  所以不同模型在同一天同一票上的信号会各留一条，不是"每票每天只留一条"
func TestCollectSignalRowsFilterAndDedup(t *testing.T) {
	totalRows := 0
	for _, tc := range goldenBacktestCases {
		bars := barsFromGolden(tc.Bars)
		rows := CollectSignalRows(bars, CollectSignalOptions{
			TsCode: "600000.SH", Name: "测试票", Now: "2026-01-01T00:00:00+08:00",
		})
		totalRows += len(rows)

		seen := map[string]bool{}
		for _, row := range rows {
			if row.SignalType != "buy" && row.SignalType != "candidate_buy" {
				t.Errorf("%s：不应收集 signal_type=%q", tc.Name, row.SignalType)
			}
			if row.TradeDate < 20000101 || row.TradeDate > 29991231 {
				t.Errorf("%s：trade_date 非法：%d", tc.Name, row.TradeDate)
			}
			if row.TsCode != "600000.SH" || row.Name != "测试票" {
				t.Errorf("%s：代码/名称未透传：%q / %q", tc.Name, row.TsCode, row.Name)
			}
			if row.ModelID == "" || row.ModelName == "" {
				t.Errorf("%s：模型 id/名称不应为空", tc.Name)
			}
			if row.MarketState != "bull" && row.MarketState != "range" && row.MarketState != "bear" {
				t.Errorf("%s：market_state 非归一化值：%q", tc.Name, row.MarketState)
			}
			key := row.ModelID + "|" + itoaVol(row.TradeDate) + "|" + row.SignalType
			if seen[key] {
				t.Errorf("%s：去重失败，重复键 %s", tc.Name, key)
			}
			seen[key] = true

			// reason_json 的五个数组键 + explanation 必须在
			//（对应 `_build_signal_history_row` 的 reason_payload）
			for _, field := range []string{
				"explanation", "condition_hits", "condition_misses",
				"confirmation_hits", "confirmation_misses", "quality_block_reasons",
			} {
				if !strings.Contains(row.ReasonJSON, `"`+field+`"`) {
					t.Errorf("%s：reason_json 缺少 %q：%s", tc.Name, field, row.ReasonJSON)
				}
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(row.ReasonJSON), &payload); err != nil {
				t.Errorf("%s：reason_json 不是合法 JSON：%v（%s）", tc.Name, err, row.ReasonJSON)
			}
			var quality map[string]any
			if err := json.Unmarshal([]byte(row.BehaviorQualityJSON), &quality); err != nil {
				t.Errorf("%s：behavior_quality_json 不是合法 JSON：%v", tc.Name, err)
			} else if _, ok := quality["score"]; !ok {
				t.Errorf("%s：behavior_quality_json 缺少 score", tc.Name)
			}
		}
	}
	t.Logf("共收集 %d 行信号（来自 %d 个形态用例）", totalRows, len(goldenBacktestCases))
	if totalRows == 0 {
		t.Fatal("没有收集到任何信号 —— 筛选规则可能过严")
	}
}

// 增量刷新用的 `min_trade_date_exclusive` 必须是**严格大于**。
//
// ⚠ 不要把候选形态写死成"最长的那个"：信号出不出取决于形态与市场状态，
// 挑错形态会让这个测试静默 Skip（第一版就 Skip 了）。
func TestCollectSignalRowsMinDateExclusiveIsStrict(t *testing.T) {
	checked := 0
	for _, tc := range goldenBacktestCases {
		bars := barsFromGolden(tc.Bars)
		all := CollectSignalRows(bars, CollectSignalOptions{TsCode: "600000.SH", Now: "t"})
		// 只需要 1 条就足以区分"严格大于"与"大于等于"：
		// 把边界设成这条信号自己的日期，严格语义下它必须被剔除。
		if len(all) < 1 {
			continue
		}
		var earliest int64
		for _, row := range all {
			if earliest == 0 || row.TradeDate < earliest {
				earliest = row.TradeDate
			}
		}
		bounded := CollectSignalRows(bars, CollectSignalOptions{
			TsCode: "600000.SH", Now: "t",
			MinTradeDateExclusive: itoaVol(earliest),
		})
		for _, row := range bounded {
			if row.TradeDate <= earliest {
				t.Errorf("%s：min_trade_date_exclusive 应为严格大于：留下了 %d（边界 %d）",
					tc.Name, row.TradeDate, earliest)
			}
		}
		if len(bounded) >= len(all) {
			t.Errorf("%s：加了边界后条数应减少：%d → %d", tc.Name, len(all), len(bounded))
		}
		checked++
		if checked >= 3 {
			break
		}
	}
	if checked == 0 {
		t.Fatal("没有任何形态产出信号，无法验证边界语义")
	}
	t.Logf("在 %d 个形态上验证了严格大于语义", checked)
}

// `_build_signal_history_row` 的字段映射（含两个 JSON 列的确切文本）。
//
// JSON 文本的期望值来自**直接运行 Python 的 `json.dumps(..., ensure_ascii=False)`**，
// 分隔符是 `", "` 与 `": "` —— 用 Go 的紧凑格式会让与原版对照时全部不匹配。
func TestBuildSignalHistoryRowFieldMapping(t *testing.T) {
	entry := 12.3456
	stop := 11.0
	target := 15.5
	signal := Signal{
		ModelID: "box_range", ModelName: "箱体低吸高抛",
		MarketState: "range", MarketStateLabel: "震荡/轮动",
		SignalType: "buy", SignalLevel: "confirmed", SignalLevelLabel: "确认",
		SignalSubtype: "trend_platform_reentry", SignalSubtypeLabel: "趋势平台再启动",
		SignalDate: "2026-10-09",
		EntryPrice: &entry, StopPrice: &stop, TargetPrice: &target,
		Score: 83.666, RiskLevel: "low",
		Explanation:         "测试说明",
		ConditionHits:       []string{"a", "b"},
		ConditionMisses:     []string{},
		ConfirmationHits:    []string{"c"},
		ConfirmationMiss:    []string{},
		QualityBlockReasons: []string{"箱体下沿验证次数不足"},
		BehaviorQuality: BehaviorQuality{
			VolumeBehavior: "breakout_volume", VolumeBehaviorLabel: "放量突破",
			AcceptanceQuality: "strong_acceptance", AcceptanceQualityLabel: "强承接",
			UpperShadowBehavior: "none", UpperShadowBehaviorLbl: "无明显上影",
			Score: 100.0, DeathVolumeRisk: false,
		},
	}
	row, ok := BuildSignalHistoryRow(signal, "600000.SH", "浦发银行", "2026-10-09T15:00:00+08:00")
	if !ok {
		t.Fatal("应当构建成功")
	}
	if row.TradeDate != 20261009 {
		t.Errorf("trade_date 应归一化为 20261009，实际 %d", row.TradeDate)
	}
	if row.Score != 83.67 {
		t.Errorf("score 应四舍五入到 2 位：83.67，实际 %v", row.Score)
	}
	if row.EntryPrice == nil || *row.EntryPrice != 12.3456 {
		t.Errorf("entry_price 应保留 4 位：12.3456，实际 %v", row.EntryPrice)
	}
	if row.ModelID != "box_range" || row.ModelName != "箱体低吸高抛" {
		t.Errorf("模型字段未透传：%q / %q", row.ModelID, row.ModelName)
	}
	if row.SignalSubtype != "trend_platform_reentry" || row.SignalSubtypeLabel != "趋势平台再启动" {
		t.Errorf("子类型未透传：%q / %q", row.SignalSubtype, row.SignalSubtypeLabel)
	}

	wantReason := `{"explanation": "测试说明", "condition_hits": ["a", "b"], ` +
		`"condition_misses": [], "confirmation_hits": ["c"], ` +
		`"confirmation_misses": [], "quality_block_reasons": ["箱体下沿验证次数不足"]}`
	if row.ReasonJSON != wantReason {
		t.Errorf("reason_json 与原版 json.dumps 不一致：\n  go     %s\n  python %s", row.ReasonJSON, wantReason)
	}

	wantQuality := `{"volume_behavior": "breakout_volume", "volume_behavior_label": "放量突破", ` +
		`"acceptance_quality": "strong_acceptance", "acceptance_quality_label": "强承接", ` +
		`"upper_shadow_behavior": "none", "upper_shadow_behavior_label": "无明显上影", ` +
		`"score": 100.0, "death_volume_risk": false}`
	if row.BehaviorQualityJSON != wantQuality {
		t.Errorf("behavior_quality_json 与原版 json.dumps 不一致：\n  go     %s\n  python %s",
			row.BehaviorQualityJSON, wantQuality)
	}
}

// 没写 `signal_date` 的信号不进表（原实现会写一行 trade_date 为空的脏数据）。
func TestBuildSignalHistoryRowRejectsBadDate(t *testing.T) {
	for _, bad := range []string{"", "2026-10-9", "202610091", "2026abcd", "2026-10-09x"} {
		if _, ok := BuildSignalHistoryRow(Signal{SignalDate: bad}, "600000.SH", "x", "t"); ok {
			t.Errorf("signal_date=%q 应当被拒绝", bad)
		}
	}
	for _, good := range []string{"20261009", "2026-10-09"} {
		if _, ok := BuildSignalHistoryRow(Signal{SignalDate: good}, "600000.SH", "x", "t"); !ok {
			t.Errorf("signal_date=%q 应当被接受", good)
		}
	}
}

func itoaVol(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
