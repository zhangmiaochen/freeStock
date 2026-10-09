package score

import "math"

// 低吸的**指数闸门**。
//
// 1:1 移植自 portfolio_service.py:57-58,124-141 与
// portfolio_rotation_logic.py:474-489,1748-1768。
//
// 背景：低吸策略除了"广度就绪数"以外，还要看**创业板指**自身的位置 ——
// 广度很热但指数还在长周期均线之下时，属于"弱周期里的假热度"，
// 原实现会把开新仓的闸门关掉。
//
// 数据来源：本项目数据湖的 `index_bars`（curated，按年分区），
// 通过 service 层的 `loadBenchmark` 读取 —— 与选股器算 RS 用的是同一条路径。

const (
	// LowBuyIndexCode 是低吸闸门用的指数（创业板指）。
	// 原实现：`LOW_BUY_INDEX_CODE = "399006.SZ"`（portfolio_service.py:57）
	LowBuyIndexCode = "399006.SZ"
	// LowBuyIndexBenchmarkCode 是长周期比对的基准（沪深300）。
	LowBuyIndexBenchmarkCode = "000300.SH"
	// lowBuyHotBreadthReadyCount 是"广度过热"的阈值。
	// 原实现：`LOW_BUY_HOT_BREADTH_READY_COUNT = 2400`（portfolio_service.py:58）
	lowBuyHotBreadthReadyCount = 2400
)

// LowBuyIndexGate 是低吸指数闸门所需的全部数值。
//
// 这些值都是**市场级**的（同日所有股票相同），由调用方从指数日线算好。
type LowBuyIndexGate struct {
	Code string `json:"code"`
	// 创业板指当日的收盘与三条均线
	Close float64 `json:"close"`
	Ma20  float64 `json:"ma20"`
	Ma60  float64 `json:"ma60"`
	Ma250 float64 `json:"ma250"`
	// 基准（沪深300）的收盘与 250 日均线
	BenchmarkClose float64 `json:"benchmarkClose"`
	BenchmarkMa250 float64 `json:"benchmarkMa250"`
	// Ready 标记闸门数据是否**真的取到了**。
	//
	// 取不到时必须明确不可用，而不是把一堆 0 当成"指数在均线下方" ——
	// 那会把低吸的建仓永久关死，而且看不出来是数据问题。
	Ready bool `json:"ready"`
}

// LowBuyLongCycleReady 判定是否处于"长周期健康"状态。
//
// 原实现 portfolio_service.py:133-138：
//
//	long_cycle_ready = (index_close > index_ma250
//	                    and benchmark_close > benchmark_ma250
//	                    and index_ma250 > 0
//	                    and benchmark_ma250 > 0)
func LowBuyLongCycleReady(gate LowBuyIndexGate) bool {
	return gate.Close > gate.Ma250 &&
		gate.BenchmarkClose > gate.BenchmarkMa250 &&
		gate.Ma250 > 0 &&
		gate.BenchmarkMa250 > 0
}

// LowBuyIndexEntryAllowed 是低吸指数闸门的最终判定。
//
// 由原实现两段组合而成：
//
//	① `_low_buy_index_entry_allowed`（portfolio_rotation_logic.py:474-489）
//	   confirmed = entry_allowed AND index_close > index_ma20 AND index_close > index_ma60
//	   → 返回 confirmed.any()；因为指数值是全市场同一个数，等价于这一条本身
//
//	② `_apply_low_buy_hot_breadth_gate`（portfolio_service.py:124-141）
//	   hot = ready_count >= 2400 AND NOT long_cycle_ready
//	   entry_allowed = current_allowed AND NOT hot
//
// 合并后：
//
//	entry_allowed = (close > ma20 AND close > ma60)
//	                AND NOT (ready_count >= 2400 AND NOT long_cycle_ready)
//
// `Ready == false`（指数数据没取到）→ 返回 false，与"取不到就关闸"一致。
func LowBuyIndexEntryAllowed(gate LowBuyIndexGate, readyCount int) bool {
	if !gate.Ready {
		return false
	}
	// ① 指数要站在 20/60 日线上方
	if !(gate.Close > gate.Ma20 && gate.Close > gate.Ma60) {
		return false
	}
	// ② 弱周期里的"假热度"要关闸
	hotBreadthInWeakCycle := readyCount >= lowBuyHotBreadthReadyCount && !LowBuyLongCycleReady(gate)
	return !hotBreadthInWeakCycle
}

// LowBuyEntryScore 是低吸候选的入场优先级分（原实现 `_low_buy_entry_score_series`，
// portfolio_rotation_logic.py:492-522）。
//
//	drawdown_score * 0.30 + ma20_score * 0.22 + ma30_score * 0.22
//	+ amount_rank * 0.16 + ret5_score * 0.10
//
// 其中：
//
//	drawdown_score = clamp(|dd20 - 区间中心| / (区间跨度/2), 0, 1)   ← 离区间**中心**越近越好
//	ma20_score     = clamp(|close/ma20 - 1| * 100 / 8, 0, 1)
//	ma30_score     = clamp(|close/ma30 - 1| * 100 / 8, 0, 1)
//	ret5_score     = clamp((clip(ret5,-10,10) + 10) / 20, 0, 1)
//	amount_rank    = 1 - 成交额的百分位排名
//
// amountRank 由调用方算好传进来（需要全池的成交额）。
func LowBuyEntryScore(row Row, breadthReadyCount int, amountRank float64) float64 {
	ddLow, ddHigh := lowBuyDrawdownBounds(breadthReadyCount)
	span := ddHigh - ddLow
	if span < 1e-9 {
		span = 1e-9
	}
	center := (ddLow + ddHigh) / 2
	drawdownScore := clamp(math.Abs(row.DrawdownFromHigh20-center)/(span/2), 0, 1)

	ma20Score := 0.0
	if row.Ma20 != 0 {
		ma20Score = clamp(math.Abs(row.Close/row.Ma20-1)*100/8, 0, 1)
	}
	ma30Score := 0.0
	if row.Ma30 != 0 {
		ma30Score = clamp(math.Abs(row.Close/row.Ma30-1)*100/8, 0, 1)
	}
	ret5 := clamp(row.StockReturn5D, -10, 10)
	ret5Score := clamp((ret5+10)/20, 0, 1)

	return drawdownScore*0.30 + ma20Score*0.22 + ma30Score*0.22 +
		clamp(amountRank, 0, 1)*0.16 + ret5Score*0.10
}
