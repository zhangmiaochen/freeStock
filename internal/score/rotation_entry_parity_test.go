package score

import (
	"strings"
	"testing"
)

// 与原版 `initial_entry_layers`（portfolio_rotation_logic.py:1438-1540）逐例比对。
//
// 黄金值由 `testdata/gen_rotation_entry_golden.py` 跑原版产出，共 268 例，
// **对每一个阈值都取"刚好不到 / 刚好等于 / 刚好超过"三点** —— 因为这个函数
// 有 4 组信号条件、二十多个阈值，靠读代码抄的话 `1.002` 抄成 `1.02`、
// `-16` 抄成 `-6`、`>=` 抄成 `>` 全都看不出来，而后果是
// "牛市首次建仓 1 层还是 2 层"，直接差一半仓位。
//
// 两件事分开断言，失败时能立刻定位：
//  1. `initialEntryLayers` 本体（用黄金值里的 desired_total_layers 直接构造 cfg，
//     把被测函数与配置构造解耦）；
//  2. `BuildStrategyConfig` 算出的 desired_total_layers 是否与原版 policy 一致
//     —— 它错了会让所有比例判定整体偏移，但错误表现不在这里而在更下游。
func TestInitialEntryLayersParityWithOriginalPython(t *testing.T) {
	byWant := map[int]int{}
	var mismatches int
	for _, c := range goldenEntryLayerCases {
		cfg := BuildStrategyConfig(
			c.StrategyID, c.MarketState, c.TargetPositionCount,
			c.TotalLayers, c.MaxLayersPerStock, "all",
		)
		// 配置构造单独校验（见函数注释第 2 条）
		if cfg.DesiredTotalLayers != c.DesiredTotalLayers {
			t.Errorf("[%s] BuildStrategyConfig 的 desired_total_layers：原版 %d，Go %d",
				c.Desc, c.DesiredTotalLayers, cfg.DesiredTotalLayers)
		}
		// 把期望值钉进 cfg，保证比对只反映被测函数
		cfg.DesiredTotalLayers = c.DesiredTotalLayers

		row := Row{
			Code: "000001",
			// 策略来自 cfg（原版是 `_row_strategy_id(row)`；两者同源，等价）
			TotalScore: c.TotalScore, TrendScore: c.TrendScore,
			StructureScore: c.StructureScore, RSScore: c.RSScore,
			// ⚠ 这里小写化，是为了**反映生产路径**：原版在
			// `prepare_strength_frame` 里对 `active_signal_type` 做 `.str.lower()`
			//（portfolio_rotation_logic.py:387），Go 对应的是 attach 处的
			// `strings.ToLower`（score/signal_attach.go）。测试直接构造 Row
			// 会绕过那一步，所以在这里补上 —— 黄金值里的 `"BUY"` 一例正是用来
			// 钉住"大写也必须被当成 buy"这个语义的。
			SignalType: strings.ToLower(c.SignalType), SignalAge: c.SignalAge, SignalScore: c.SignalScore,
			Close: c.Close, Ma20: c.Ma20, Ma30: c.Ma30,
			DrawdownFromHigh120: c.Drawdown120,
		}
		got := initialEntryLayers(row, cfg, c.FreeLayers, c.RemainingSlotNeed)
		byWant[c.Want]++
		if got != c.Want {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("[%s] 原版 %d 层，Go %d 层\n"+
					"  state=%s strategy=%s maxLayers=%d desired=%d free=%d need=%d\n"+
					"  score=%v trend=%v struct=%v rs=%v sig=%q age=%d sigScore=%v\n"+
					"  close=%v ma20=%v ma30=%v dd120=%v",
					c.Desc, c.Want, got,
					c.MarketState, c.StrategyID, c.MaxLayersPerStock, c.DesiredTotalLayers,
					c.FreeLayers, c.RemainingSlotNeed,
					c.TotalScore, c.TrendScore, c.StructureScore, c.RSScore,
					c.SignalType, c.SignalAge, c.SignalScore,
					c.Close, c.Ma20, c.Ma30, c.Drawdown120)
			}
		}
	}
	if mismatches > 20 {
		t.Errorf("共 %d 例不一致（上面只列了前 20 条）", mismatches)
	}
	// 用例必须**同时**覆盖 1 层与 2 层，否则这个测试可能因为"全都返回 1"而假绿
	if byWant[1] == 0 || byWant[2] == 0 {
		t.Fatalf("黄金值没有同时覆盖 1 层与 2 层：%v —— 说明生成器的用例构造失效了", byWant)
	}
	t.Logf("比对 %d 例，期望分布 %v", len(goldenEntryLayerCases), byWant)
}
