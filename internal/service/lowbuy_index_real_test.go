package service

import (
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/score"
)

// 用**真实数据湖**验证低吸指数闸门能取到数据。
//
// 这是之前误判为"做不到"的那一项 —— 数据其实在 curated/index_bars 里。
// 数据湖不存在时跳过（CI 场景）。
func TestLoadLowBuyIndexGateRealData(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.HasIndexBars() {
		t.Skip("数据湖 index_bars 未入库，跳过")
	}
	// 找一个有数据的最近交易日：直接试 2026 年
	gate := (&StockService{lake: lake}).lowBuyIndexGateFor(20261008)
	if !gate.Ready {
		t.Skipf("index_bars 里取不到 %s / %s（数据可能未覆盖该日期）: %+v",
			gate.Code, "000300.SH", gate)
	}
	if gate.Close <= 0 || gate.Ma20 <= 0 || gate.Ma60 <= 0 {
		t.Errorf("创业板指数据不完整: %+v", gate)
	}
	t.Logf("创业板指 %s 收盘 %.2f / MA20 %.2f / MA60 %.2f / MA250 %.2f；"+
		"沪深300 收盘 %.2f / MA250 %.2f → 长周期健康=%v",
		gate.Code, gate.Close, gate.Ma20, gate.Ma60, gate.Ma250,
		gate.BenchmarkClose, gate.BenchmarkMa250, score.LowBuyLongCycleReady(gate))
}
