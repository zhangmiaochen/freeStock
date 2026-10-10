package portfolio

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 分批写入必须与"逐行写入"落库**完全一样**。
//
// 批的边界是这类改动最容易出错的地方（余数批、空批、跨批的列顺序错位），
// 所以这里把 0/1/199/200/201/401/407 行都过一遍，并**逐字段读回比对**。
func TestSaveStrengthBatchedRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 199, 200, 201, strengthInsertBatch * 2, strengthInsertBatch*2 + 7}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "batch.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			const tradeDate = int64(20261009)
			rows := benchStrengthRows(size)
			if err := store.SaveStrength(tradeDate, StrategyTrendGrowth, rows); err != nil {
				t.Fatalf("写入失败：%v", err)
			}

			got, err := store.LoadStrength(tradeDate, StrategyTrendGrowth, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(rows) {
				t.Fatalf("读回 %d 行，写入 %d 行", len(got), len(rows))
			}
			// LoadStrength 按 total_score DESC, ts_code 升序
			want := append([]StrengthRow(nil), rows...)
			sort.SliceStable(want, func(i, j int) bool {
				if want[i].TotalScore != want[j].TotalScore {
					return want[i].TotalScore > want[j].TotalScore
				}
				return want[i].Code < want[j].Code
			})
			for i := range want {
				assertStrengthEqual(t, i, want[i], got[i], tradeDate, StrategyTrendGrowth)
			}
		})
	}
}

// 先删后插的语义不能因为分批而变：重跑同一天时，
// 旧的行（这次没算出来的那些）必须被删掉，不能残留成幽灵行。
func TestSaveStrengthBatchedOverwriteRemovesStale(t *testing.T) {
	if strengthInsertBatch < 3 {
		t.Skip("批次太小，覆盖不到")
	}
	store, err := Open(filepath.Join(t.TempDir(), "overwrite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const tradeDate = int64(20261009)
	// 第一轮：整批
	first := benchStrengthRows(strengthInsertBatch)
	if err := store.SaveStrength(tradeDate, StrategyTrendGrowth, first); err != nil {
		t.Fatal(err)
	}
	// 第二轮：只有 2 行（余数批）—— 其余必须消失
	if err := store.SaveStrength(tradeDate, StrategyTrendGrowth, benchStrengthRows(2)); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadStrength(tradeDate, StrategyTrendGrowth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("覆盖后应只剩 2 行，实际 %d 行（幽灵行没删干净）", len(got))
	}
	// 另一个策略不受影响
	if err := store.SaveStrength(tradeDate, StrategyOversold, first); err != nil {
		t.Fatal(err)
	}
	other, err := store.LoadStrength(tradeDate, StrategyOversold, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != strengthInsertBatch {
		t.Fatalf("另一个策略的行被误删：%d", len(other))
	}
}

// **增量写入绝不能删掉当天其余的行**。
//
// 这是真实的**数据丢失**路径：增量刷新/增量回填只把"这次新算的代码"
// （`codes = missing`）交给落库，而 `SaveStrength` 是整分区替换 ——
// 于是当天已经算好的几千行被删光，只剩这次的增量。
//
// 本机库就是这么坏的：多数交易日只剩 ~990 行，正常应是 4400~7600 行。
// 后果不是"少几只票"，而是低吸广度就绪数被系统性低估 →
// 判定"广度不足 < 900" → 清仓 + 禁止开新仓 → **回测恒为 0 成交**。
//
// `UpsertStrength` 按主键 (trade_date, strategy_id, ts_code) 覆盖，
// 与原实现 `_write_batches(..., key_columns=[...], replace=True)` 一致。
func TestUpsertStrengthKeepsOtherRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "upsert.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const tradeDate = int64(20261009)
	// 第一轮：整批
	first := benchStrengthRows(strengthInsertBatch)
	if err := store.SaveStrength(tradeDate, StrategyTrendGrowth, first); err != nil {
		t.Fatal(err)
	}

	// 第二轮：增量 —— 只有 2 行，且**改过分数**（要能覆盖生效）
	delta := benchStrengthRows(2)
	delta[0].TotalScore = 11.5
	delta[1].Name = "增量改名"
	if err := store.UpsertStrength(tradeDate, StrategyTrendGrowth, delta); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadStrength(tradeDate, StrategyTrendGrowth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != strengthInsertBatch {
		t.Fatalf("增量写入后应仍有 %d 行，实际 %d 行 —— 当天的其它行被删了",
			strengthInsertBatch, len(got))
	}
	// 增量的那两行必须被**覆盖**
	byCode := map[string]StrengthRow{}
	for _, row := range got {
		byCode[row.Code] = row
	}
	if row := byCode[delta[0].Code]; row.TotalScore != 11.5 {
		t.Errorf("增量行 %s 的分数没被覆盖：%v", delta[0].Code, row.TotalScore)
	}
	if row := byCode[delta[1].Code]; row.Name != "增量改名" {
		t.Errorf("增量行 %s 的名称没被覆盖：%q", delta[1].Code, row.Name)
	}

	// 另一个策略完全不受影响
	if err := store.UpsertStrength(tradeDate, StrategyOversold, delta); err != nil {
		t.Fatal(err)
	}
	other, err := store.LoadStrength(tradeDate, StrategyOversold, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 2 {
		t.Errorf("另一个策略应只有 2 行，实际 %d", len(other))
	}
	if again, _ := store.LoadStrength(tradeDate, StrategyTrendGrowth, 0); len(again) != strengthInsertBatch {
		t.Errorf("写入别的策略后，本策略行数被改动：%d", len(again))
	}
}

// TestStrengthInsertSQLShape 语句形状：占位符个数必须与列数对上。
//
// 列顺序错位不会报错，只会**静默存错数据** —— 所以这里直接数一遍。
func TestStrengthInsertSQLShape(t *testing.T) {
	for _, n := range []int{1, 2, 5} {
		sql := strengthInsertSQL(n, false)
		if got := strings.Count(sql, "?"); got != n*strengthColumnCount {
			t.Errorf("%d 行的语句有 %d 个占位符，期望 %d", n, got, n*strengthColumnCount)
		}
		if got := strings.Count(sql, "("); got != n+1 { // n 个值组 + 列清单那一个括号
			t.Errorf("%d 行的语句有 %d 个左括号，期望 %d", n, got, n+1)
		}
	}
	// 列清单本身要能数出 39 列
	columns := 0
	for _, part := range strings.Split(strengthInsertColumns, ",") {
		if strings.TrimSpace(part) != "" {
			columns++
		}
	}
	if columns != strengthColumnCount {
		t.Errorf("列清单有 %d 列，strengthColumnCount=%d", columns, strengthColumnCount)
	}
	// 摊平的参数个数也必须对得上
	args := appendStrengthArgs(nil, benchStrengthRows(3), 20260101, StrategyTrendGrowth, "now")
	if len(args) != 3*strengthColumnCount {
		t.Errorf("3 行摊平出 %d 个参数，期望 %d", len(args), 3*strengthColumnCount)
	}
}

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

func assertStrengthEqual(t *testing.T, i int, want, got StrengthRow, tradeDate int64, strategyID string) {
	t.Helper()
	if want.Code != got.Code || got.TradeDate != tradeDate || got.StrategyID != strategyID {
		t.Fatalf("第 %d 行标识不符：want %s/%d/%s，got %s/%d/%s",
			i, want.Code, tradeDate, strategyID, got.Code, got.TradeDate, got.StrategyID)
	}
	if want.Name != got.Name || want.Industry != got.Industry || want.Market != got.Market {
		t.Fatalf("第 %d 行（%s）文本字段不符：want %q/%q/%q，got %q/%q/%q",
			i, want.Code, want.Name, want.Industry, want.Market,
			got.Name, got.Industry, got.Market)
	}
	pairs := []struct {
		name string
		x, y float64
	}{
		{"TotalScore", want.TotalScore, got.TotalScore},
		{"TrendScore", want.TrendScore, got.TrendScore},
		{"StructureScore", want.StructureScore, got.StructureScore},
		{"RSScore", want.RSScore, got.RSScore},
		{"CapitalScore", want.CapitalScore, got.CapitalScore},
		{"RepairScore", want.RepairScore, got.RepairScore},
		{"Close", want.Close, got.Close},
		{"High", want.High, got.High},
		{"Low", want.Low, got.Low},
		{"Open", want.Open, got.Open},
		{"PctChg", want.PctChg, got.PctChg},
		{"Amount", want.Amount, got.Amount},
		{"Ma5", want.Ma5, got.Ma5},
		{"Ma20", want.Ma20, got.Ma20},
		{"Ma30", want.Ma30, got.Ma30},
		{"Ma60", want.Ma60, got.Ma60},
		{"Ma120", want.Ma120, got.Ma120},
		{"Ma250", want.Ma250, got.Ma250},
		{"RelativeStrength20D", want.RelativeStrength20D, got.RelativeStrength20D},
		{"DrawdownFromHigh20", want.DrawdownFromHigh20, got.DrawdownFromHigh20},
		{"DrawdownFromHigh120", want.DrawdownFromHigh120, got.DrawdownFromHigh120},
		{"ReboundFromLow60", want.ReboundFromLow60, got.ReboundFromLow60},
		{"StockReturn3D", want.StockReturn3D, got.StockReturn3D},
		{"StockReturn5D", want.StockReturn5D, got.StockReturn5D},
		{"StockReturn10D", want.StockReturn10D, got.StockReturn10D},
		{"StockReturn20D", want.StockReturn20D, got.StockReturn20D},
		{"VolumeRatio20", want.VolumeRatio20, got.VolumeRatio20},
		{"VolumeRatio520", want.VolumeRatio520, got.VolumeRatio520},
		{"Ma5Prev", want.Ma5Prev, got.Ma5Prev},
		{"Ma250Prev", want.Ma250Prev, got.Ma250Prev},
		{"IndustryReturn20D", want.IndustryReturn20D, got.IndustryReturn20D},
		{"BenchmarkReturn20D", want.BenchmarkReturn20D, got.BenchmarkReturn20D},
	}
	for _, p := range pairs {
		if p.x != p.y {
			t.Fatalf("第 %d 行（%s）%s 不符：want %v，got %v", i, want.Code, p.name, p.x, p.y)
		}
	}
}
