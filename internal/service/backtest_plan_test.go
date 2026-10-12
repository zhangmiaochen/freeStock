package service

import (
	"reflect"
	"sort"
	"testing"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **`rowToStrength` 必须逐字段镜像 `strengthToRow`**。
//
// 它是后者的逆变换，回测的候选池就是靠它把当日评分行转回 `StrengthRow`
// 再交给范围过滤的。曾经漏抄 `Market` 与 `Amount`，后果都不是"差一点"：
//
//   - `Market` 空串 → `MarketInScope("")` 恒 false → 只要组合设了市场范围
//     （界面默认「主板+创业板」），**候选池与广度帧全为空** →
//     回测一条动作都产不出、零成交、净值一条直线。
//   - `Amount` = 0 → 低吸候选是**按成交额**排入场顺序的
//     （`prioritizeLowBuy` 的 amount_entry / deep_amount / board_norm_amount），
//     全部为 0 时排序退化成"按代码"，选出的票与原版完全不同。
//
// 用往返比对而不是逐个断言：以后任一侧加字段，这里都会立刻红。
func TestRowToStrengthRoundTrip(t *testing.T) {
	src := portfolio.StrengthRow{
		TradeDate: 20261009, StrategyID: "s", Code: "600519", Name: "贵州茅台",
		Industry: "白酒", Market: "sh-main",
		TotalScore: 88.5, TrendScore: 25, StructureScore: 15, RSScore: 13,
		CapitalScore: 10, RepairScore: 9,
		Close: 1500, High: 1520, Low: 1480, Open: 1490, PctChg: 1.25, Amount: 3.2e9,
		Ma5: 1495, Ma20: 1450, Ma30: 1430, Ma60: 1400, Ma120: 1350, Ma250: 1300,
		RelativeStrength20D: 8.5, DrawdownFromHigh20: -6.5, DrawdownFromHigh120: -12.5,
		ReboundFromLow60: 0.42,
		StockReturn3D:    1.1, StockReturn5D: 2.2, StockReturn10D: 3.3, StockReturn20D: 4.4,
		VolumeRatio20: 1.15, VolumeRatio520: 1.05,
		Ma5Prev: 1490, Ma250Prev: 1295,
		IndustryReturn20D: 0.8, BenchmarkReturn20D: 0.5,
	}

	row := strengthToRow(src)
	got := rowToStrength(row, src.Industry)

	// `score.Row` 不承载 `StrategyID`，往返后会丢 —— 这是预期内的。
	//
	// ⚠ `TradeDate` **不再**属于"预期会丢"的字段：它已经进了 `score.Row`，
	// 因为贴信号要靠它算 `signal_age`（丢了就是 0 → attach 的日期区间非法
	// → 静默不贴信号 → 熊市闸门把每只候选都拒掉）。所以这里必须保留它，
	// 让往返比对真正覆盖这个字段。
	want := src
	want.StrategyID = ""

	if !reflect.DeepEqual(got, want) {
		t.Errorf("rowToStrength(strengthToRow(x)) 与 x 不一致：\n got=%+v\nwant=%+v", got, want)
	}
	// 单独把两处曾漏掉的字段点出来（失败信息更直白）
	if got.Market != src.Market {
		t.Errorf("Market 丢了：got %q，want %q（空串会让市场范围过滤剔掉所有行）", got.Market, src.Market)
	}
	if got.Amount != src.Amount {
		t.Errorf("Amount 丢了：got %v，want %v（低吸按成交额排候选，全 0 会选错票）", got.Amount, src.Amount)
	}
}

// **低吸回测/自动组合必须喂进两道闸门**，否则一条 add 都产不出来。
//
// 这两条闸门（广度就绪数 + 指数闸门）由调用方按日算好传进
// `score.RotationInput`。回测与"自动建组合"两条路径此前**都没传** ——
// `LowBuyBreadthReadyCount=0` 会让 `LowBuyIndexEntryAllowed` 直接判 false，
// 于是 `canAddNewPosition=false`、`desiredCount=0` →
// 低吸回测从纯现金起步必然零成交，自动组合必然报"没有候选"。
//
// 这个用例把"传闸门 → 有 add / 不传 → 没有 add"钉住。
func TestLowBuyGatesDecideWhetherAddsAppear(t *testing.T) {
	cfg := score.BuildStrategyConfig(
		portfolio.StrategyTrendLowBuy, score.StateRange, 5, 10, 10, "main_gem")

	// 一只满足低吸结构条件的候选（dd20=-10 在紧档(-18,-1.5)与宽档(-28,-3)内都成立）
	cand := score.Row{
		Code: "600519", Name: "贵州茅台", Market: "sh-main",
		TotalScore: 80, Amount: 3e9,
		Close: 100, Open: 99, High: 101, Low: 98,
		Ma5: 99, Ma20: 102, Ma30: 100, Ma60: 95, Ma120: 90, Ma250: 85,
		Ma5Prev: 98, Ma250Prev: 84,
		StockReturn20D: 5, DrawdownFromHigh20: -10,
	}
	// 广度帧：给足 200 只（>=100 闸门才启用），分数都低于建仓线，
	// 这样广度闸门不会改写动作，动作只由低吸那两道闸门决定。
	universe := make([]float64, 200)

	readyGate := score.LowBuyIndexGate{
		Code: score.LowBuyIndexCode, Ready: true,
		Close: 3300, Ma20: 3200, Ma60: 3100, Ma250: 3000,
		BenchmarkClose: 4000, BenchmarkMa250: 3800,
	}

	// ① 闸门齐备（=修复后的回测）：应产出 add
	planned := score.BuildRotationPlan(score.RotationInput{
		Holdings:                nil,
		Candidates:              []score.Row{cand},
		Config:                  cfg,
		MarketState:             score.StateRange,
		MaxStockLimit:           5,
		UniverseScores:          universe,
		LowBuyBreadthReadyCount: 1500,
		LowBuyIndex:             readyGate,
	})
	if countAdds(planned.Rows) == 0 {
		t.Errorf("闸门齐备时应产出 add 行，实际 %d 行（%+v）", len(planned.Rows), planned.Rows)
	}

	// ② 不传闸门（=修复前的回测/自动组合）：必须**出不来** add
	//    这条断言记录的是 bug 的现象，防止以后有人把闸门又漏掉。
	before := score.BuildRotationPlan(score.RotationInput{
		Holdings:       nil,
		Candidates:     []score.Row{cand},
		Config:         cfg,
		MarketState:    score.StateRange,
		MaxStockLimit:  5,
		UniverseScores: universe,
	})
	if countAdds(before.Rows) != 0 {
		t.Errorf("不传闸门时不应产出 add —— 说明闸门已经不再必需，请同步更新回测/自动组合的调用点")
	}

	// ③ 指数闸门未就绪（数据取不到）→ 也不开新仓
	indexMissing := score.BuildRotationPlan(score.RotationInput{
		Holdings:                nil,
		Candidates:              []score.Row{cand},
		Config:                  cfg,
		MarketState:             score.StateRange,
		MaxStockLimit:           5,
		UniverseScores:          universe,
		LowBuyBreadthReadyCount: 1500,
		LowBuyIndex:             score.LowBuyIndexGate{Code: score.LowBuyIndexCode},
	})
	if countAdds(indexMissing.Rows) != 0 {
		t.Errorf("指数数据取不到时必须关闸（不伪造恒真判定），实际产出了 add")
	}
}

func countAdds(rows []score.RotationRow) int {
	n := 0
	for _, row := range rows {
		if row.Action == score.ActionAdd || row.Action == score.ActionReplaceIn {
			n++
		}
	}
	return n
}

// **回测的计划构建器必须同时做到三件事**（这三件以前各错一处，
// 任何一处错都让回测"零成交、净值一条直线"）：
//
//	① 评分行的 market 词表映射对（sh-main/chinext → main/gem）
//	② 评分行往返转换不丢 `Market`（`rowToStrength`）
//	③ 低吸的两道闸门逐日喂进 `score.RotationInput`
//
// 用真实 `backtest.PlanBuilder` 跑一遍，而不是直接调 score：
// 上面三件里有两件发生在**服务层**，直接调 score 测不到。
func TestBacktestPlanBuilderProducesLowBuyAdds(t *testing.T) {
	svc := &PortfolioService{}
	item := portfolio.Portfolio{
		ID: "pf_bt", Name: "回测组合",
		StrategyID:          portfolio.StrategyTrendLowBuy,
		TargetPositionCount: 5, MaxStockCount: 5, TotalLayers: 10,
		ScopeType: portfolio.ScopeAll,
		// 与库里真实组合一致：市场范围用的是原实现词汇
		ScopeValueJSON: `{"markets":["main","gem"]}`,
	}

	// 当日评分行：**市场字段是 `market.KeyOf` 的 key**（sh-main / chinext），
	// 与组合范围的 main/gem 不是同一套词表 —— 这正是词汇表那个坑。
	rows := make([]score.Row, 0, 1100)
	// 1000 只"烘托广度"的票：close>ma60 且 ma20>ma60，但结构不满足低吸条件
	//（close 高于 ma20*1.04），所以不会成为候选。
	for i := 0; i < 1000; i++ {
		rows = append(rows, score.Row{
			Code: codeAt(i), Name: "广度票", Market: "sh-main",
			TotalScore: 50, Amount: 1e7,
			Close: 100, Open: 99, High: 101, Low: 98,
			Ma5: 99, Ma20: 95, Ma30: 92, Ma60: 90, Ma120: 85, Ma250: 80,
			Ma5Prev: 98, Ma250Prev: 79,
			StockReturn20D: 3, DrawdownFromHigh20: -9,
		})
	}
	// 唯一一只合格的低吸候选（创业板），成交额给大 —— 低吸按成交额排入场顺序
	candidate := score.Row{
		Code: "300750", Name: "宁德时代", Market: "chinext",
		TotalScore: 78, Amount: 5e9,
		Close: 100, Open: 99, High: 101, Low: 98,
		Ma5: 99, Ma20: 102, Ma30: 100, Ma60: 95, Ma120: 90, Ma250: 85,
		Ma5Prev: 98, Ma250Prev: 84,
		StockReturn20D: 5, DrawdownFromHigh20: -10,
	}
	rows = append(rows, candidate)

	day := backtest.StrengthDay{Date: "20261009", MarketState: score.StateRange, Rows: rows}
	readyGate := score.LowBuyIndexGate{
		Code: score.LowBuyIndexCode, Ready: true,
		Close: 3300, Ma20: 3200, Ma60: 3100, Ma250: 3000,
		BenchmarkClose: 4000, BenchmarkMa250: 3800,
	}

	// ① 闸门齐备（=修复后的回测路径）：必须产出 add
	withGates := svc.backtestPlanBuilder(item, nil, nil, 5, 5,
		map[string]score.LowBuyIndexGate{day.Date: readyGate})(nil, day)
	if countAdds(withGates.Rows) == 0 {
		t.Errorf("闸门齐备时应产出 add，实际计划 %d 行：%+v", len(withGates.Rows), withGates.Rows)
	}
	found := false
	for _, row := range withGates.Rows {
		if row.Code == candidate.Code {
			found = true
		}
	}
	if !found {
		t.Errorf("合格的创业板候选 %s 没进计划 —— 市场词表/字段映射可能又断了："+
			"计划行 %+v", candidate.Code, withGates.Rows)
	}
	if withGates.NoActionReasons != nil && len(withGates.NoActionReasons) > 0 && countAdds(withGates.Rows) == 0 {
		t.Logf("无建议原因：%v", withGates.NoActionReasons)
	}

	// ② 不喂闸门（=修复前的回测路径）：必须**出不来** add
	//    这条断言就是"组合回测结果恒为 0"的最小复现。
	withoutGates := svc.backtestPlanBuilder(item, nil, nil, 5, 5, nil)(nil, day)
	if countAdds(withoutGates.Rows) != 0 {
		t.Errorf("不喂低吸闸门时不应产出 add（此前回测就是这样恒为 0 的），实际 %+v",
			withoutGates.Rows)
	}
}

func codeAt(i int) string {
	const digits = "0123456789"
	return "60" + string([]byte{
		digits[(i/1000)%10], digits[(i/100)%10], digits[(i/10)%10], digits[i%10],
	})
}

// **`回填股票数` 必须是"跨板块的等距抽样"，不能是"代码序前 N 只"。**
//
// `GetStockUniverse` 的列表按代码升序，所以"取前 N 只"= 只评代码最小的那一段。
// 实测本机库：limit=1000 那天只有 `000/001/002` 三个前缀（深市主板），
// 而全市场日有 14 个前缀。后果是组合设「主板+创业板」时候选恒为空，
// 且低吸广度被系统性低估 → 恒判广度不足 → **回测恒为 0 成交**。
func TestSampleUniverseSpansAllBoards(t *testing.T) {
	// 造一个"代码升序、板块齐全"的全市场列表（各板块数量不同，贴近真实分布）
	var eligible []string
	for i := 0; i < 400; i++ { // 深市主板 000/001/002
		eligible = append(eligible, "00"+pad3(i))
	}
	for i := 0; i < 900; i++ { // 创业板 300/301
		eligible = append(eligible, "30"+pad3(i))
	}
	for i := 0; i < 600; i++ { // 沪市主板 600/601/603
		eligible = append(eligible, "60"+pad3(i))
	}
	for i := 0; i < 600; i++ { // 科创板 688
		eligible = append(eligible, "68"+pad3(i))
	}
	sort.Strings(eligible)

	// ① limit <= 0 → 全量
	if got := sampleUniverse(eligible, 0); len(got) != len(eligible) {
		t.Errorf("limit=0 应返回全量 %d 只，实际 %d", len(eligible), len(got))
	}
	// ② limit >= 总数 → 全量
	if got := sampleUniverse(eligible, len(eligible)+10); len(got) != len(eligible) {
		t.Errorf("limit 超过总数应返回全量，实际 %d", len(got))
	}

	// ③ limit=1000 → 恰好 1000 只，且**四个板块都有**
	got := sampleUniverse(eligible, 1000)
	if len(got) != 1000 {
		t.Fatalf("应恰好抽出 1000 只，实际 %d", len(got))
	}
	boards := map[string]int{}
	seen := map[string]bool{}
	for _, code := range got {
		if seen[code] {
			t.Errorf("抽样出现重复代码 %s", code)
		}
		seen[code] = true
		boards[code[:2]]++
	}
	for _, prefix := range []string{"00", "30", "60", "68"} {
		if boards[prefix] == 0 {
			t.Errorf("板块 %sxx 一只都没抽到 —— 抽样仍有板块偏斜（boards=%v）", prefix, boards)
		}
	}
	t.Logf("1000 只抽样分布：%v", boards)

	// ④ 确定性：同样的输入必须给同样的输出
	again := sampleUniverse(eligible, 1000)
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("抽样不确定：第 %d 个不同（%s vs %s）", i, got[i], again[i])
		}
	}
}

func pad3(v int) string {
	const digits = "0123456789"
	return string([]byte{
		digits[(v/100)%10], digits[(v/10)%10], digits[v%10],
	})
}
