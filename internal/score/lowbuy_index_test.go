package score

import "testing"

// 低吸指数闸门的两条分支（原实现 portfolio_service.py:124-141
// + portfolio_rotation_logic.py:474-489 的组合）。
func TestLowBuyIndexEntryAllowed(t *testing.T) {
	// 指数站在 20/60 上方，且长周期健康 → 允许
	healthy := LowBuyIndexGate{
		Code: LowBuyIndexCode, Ready: true,
		Close: 2200, Ma20: 2100, Ma60: 2050, Ma250: 2000,
		BenchmarkClose: 4000, BenchmarkMa250: 3800,
	}
	if !LowBuyIndexEntryAllowed(healthy, 500) {
		t.Error("指数在均线上方 + 长周期健康 → 应允许")
	}
	if !LowBuyIndexEntryAllowed(healthy, 3000) {
		t.Error("长周期健康时，广度过热也应允许（不是弱周期假热度）")
	}

	// 指数跌破 MA20 → 关闸
	below20 := healthy
	below20.Close = 2000
	if LowBuyIndexEntryAllowed(below20, 500) {
		t.Error("指数跌破 MA20 应关闸")
	}
	// 指数在 MA20 上方但跌破 MA60 → 关闸
	below60 := healthy
	below60.Close, below60.Ma20, below60.Ma60 = 2040, 2100, 2050
	if LowBuyIndexEntryAllowed(below60, 500) {
		t.Error("指数跌破 MA60 应关闸")
	}

	// **弱周期假热度**：广度过热(>=2400) 但指数/基准没站上 MA250 → 关闸
	weakCycle := healthy
	weakCycle.Ma250 = 2500 // 指数在 MA250 之下
	if LowBuyIndexEntryAllowed(weakCycle, 2400) {
		t.Error("广度过热 + 弱周期 → 应关闸")
	}
	weakBench := healthy
	weakBench.BenchmarkMa250 = 4500 // 基准在 MA250 之下
	if LowBuyIndexEntryAllowed(weakBench, 2400) {
		t.Error("基准未站上 MA250 → 弱周期 → 应关闸")
	}
	// 广度过热但长周期健康 → 放行
	if !LowBuyIndexEntryAllowed(healthy, 2400) {
		t.Error("广度过热但长周期健康应放行")
	}
	// 未过热时不受长周期影响
	if !LowBuyIndexEntryAllowed(weakCycle, 2399) {
		t.Error("未过热时不该被长周期关闸")
	}

	// **数据取不到 → 明确不可用**（不能把 0 当成"指数在均线下方"）
	if LowBuyIndexEntryAllowed(LowBuyIndexGate{Code: LowBuyIndexCode}, 500) {
		t.Error("Ready=false 时必须关闸，且应能区分于真实判定")
	}
}

// 低吸候选的入场优先级分（原实现 `_low_buy_entry_score_series`）。
func TestLowBuyEntryScore(t *testing.T) {
	// 回撤落在区间正中、贴合均线、温和上涨 → 分高
	good := Row{
		Close: 100, Ma20: 100, Ma30: 100,
		DrawdownFromHigh20: -15.5, // 宽档 (-28,-3) 的中心
		StockReturn5D:      0,
	}
	score := LowBuyEntryScore(good, 500, 0.5)
	if score <= 0 || score > 1 {
		t.Errorf("分数应在 (0,1]，实际 %v", score)
	}
	// ⚠ 低吸的入场分是**惩罚项**（越低越好）——
	// 排序是 `amount_yuan DESC, _entry_priority_score ASC`，不是 DESC。
	// 所以"回撤离区间中心越远"应当**加分**（= 更差）。
	far := good
	far.DrawdownFromHigh20 = -28
	if LowBuyEntryScore(far, 500, 0.5) <= score {
		t.Error("低吸分是惩罚项：回撤远离区间中心应加分（更差）")
	}
	// 同样地，5 日涨得越多、回撤项越"接近中心"，分越低（越好）
	up := good
	up.StockReturn5D = 10
	if LowBuyEntryScore(up, 500, 0.5) <= score {
		t.Error("ret5 高 → ret5_score 高 → 分更高（更差）")
	}
	// amountRank 传进来的已经是 `1 - rank`：**低 = 冷门股 = 惩罚小 = 更好**。
	// （低吸偏好没人关注的票，这也和"去 rs 低吸"的取向一致。）
	if LowBuyEntryScore(good, 500, 0.0) >= LowBuyEntryScore(good, 500, 1.0) {
		t.Error("amountRank 低（冷门）应让分更低（更好）")
	}
	// 区间中心的回撤项 = 0（惩罚最小）
	if got := LowBuyEntryScore(good, 500, 0.0); got >= 0.30 {
		t.Errorf("回撤在区间中心时该项应为 0，实际总分 %v", got)
	}
	// 紧档（就绪数 >=1200）会改变回撤中心
	narrow := good
	narrow.DrawdownFromHigh20 = -9.75 // 紧档 (-18,-1.5) 的中心
	if got := LowBuyEntryScore(narrow, 2000, 0.5); got <= 0 {
		t.Errorf("紧档中心应得正分，实际 %v", got)
	}
	// 均线为 0 不 panic
	zero := Row{Close: 100, DrawdownFromHigh20: -15.5}
	if got := LowBuyEntryScore(zero, 500, 0.5); got < 0 {
		t.Errorf("均线缺失时不该为负，实际 %v", got)
	}
}
