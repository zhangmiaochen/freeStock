package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/chanlunassist"
	"freestock/internal/config"
	"freestock/internal/score"
)

// 缠论买卖时序实验（**只读、只测，不改策略代码**）。
//
// 背景：趋势低吸的买卖点比缠论确认更早，所以「事后叠加」天然效果有限。
// 这里在测试层重写买卖时序，直接验证几种「以缠论为主」的逻辑：
//
//	W 类 等缠论再买：策略想买时先挂起，等确认/触发二/三买再买；超时则放弃或照常买
//	X 类 等缠论再卖：策略要清仓/减仓时先压住，等卖出信号再卖；或只按缠论退出
//	S 类 抢时间：用「触发」级别的三卖（比确认早）做减仓
//
// 所有结果与基线共用同一份评分、日线、撮合与费用，只改「计划行怎么生成/拦截」。
//
//	$env:FREESTOCK_CA_DB="...\bin\portfolio.db"
//	$env:FREESTOCK_CA_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_CA_PORTFOLIOS="pf_...0002,pf_...0001"
//	$env:FREESTOCK_CA_LOGIC_OUT=".gotmp\chanlun-logic.json"
//	go test ./internal/service -run TestChanlunAssistLogic -v -timeout 180m
func TestChanlunAssistLogic(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_CA_DB")
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	rawPortfolios := os.Getenv("FREESTOCK_CA_PORTFOLIOS")
	if rawPortfolios == "" {
		rawPortfolios = os.Getenv("FREESTOCK_CA_PORTFOLIO")
	}
	if srcDB == "" || lakeRoot == "" || rawPortfolios == "" {
		t.Skip("未设置 FREESTOCK_CA_DB / FREESTOCK_CA_LAKE / FREESTOCK_CA_PORTFOLIOS")
	}

	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	dstDB := os.Getenv("FREESTOCK_CA_WORKDB")
	if dstDB == "" {
		dstDB = filepath.Join(t.TempDir(), "portfolio.db")
		copyFile(t, srcDB, dstDB)
	} else if _, err := os.Stat(dstDB); err != nil {
		copyFile(t, srcDB, dstDB)
	}
	cfgDir, err := os.MkdirTemp("", "freestock-ca-logic-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfgDir) })
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dstDB)
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}

	windows := []struct {
		name       string
		start, end int
	}{
		{"W1_2022-01-12_2023-10-12", 20220112, 20231012},
		{"W2_2023-10-13_2024-10-11", 20231013, 20241011},
		{"W3_2024-10-14_2025-10-13", 20241014, 20251013},
		{"W4_2025-10-14_2026-10-19", 20251014, 20261019},
	}
	cases := logicCases()
	if raw := os.Getenv("FREESTOCK_CA_LOGIC_CASES"); raw != "" {
		wanted := map[string]bool{"L0_base": true}
		for _, name := range splitCSV(raw) {
			wanted[name] = true
		}
		filtered := make([]logicCase, 0, len(cases))
		for _, c := range cases {
			if wanted[c.name] {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) < 2 {
			t.Fatalf("FREESTOCK_CA_LOGIC_CASES 只匹配到 %d 个方案", len(filtered))
		}
		cases = filtered
	}

	type logicRun struct {
		Portfolio string           `json:"portfolio"`
		Window    string           `json:"window"`
		Case      string           `json:"case"`
		Metrics   backtest.Metrics `json:"metrics"`
		Stats     logicStats       `json:"stats"`
	}
	runs := make([]logicRun, 0, len(windows)*len(cases)*2)
	for _, rawID := range splitCSV(rawPortfolios) {
		item, err := store.Get(rawID)
		if err != nil {
			t.Fatalf("读组合 %s 失败：%v", rawID, err)
		}
		for _, win := range windows {
			stock.applySource(config.KindCnLake, lakeRoot)
			began := time.Now()
			w := buildGridWindow(t, svc, store, item, win.name, win.start, win.end)
			t.Logf("%s / %s：准备 %s", item.ID, win.name, time.Since(began).Round(time.Second))
			for _, c := range cases {
				result, stats, err := runLogicCase(w, c)
				if err != nil {
					t.Fatalf("%s / %s / %s 失败：%v", item.ID, win.name, c.name, err)
				}
				runs = append(runs, logicRun{
					Portfolio: item.ID, Window: win.name, Case: c.name,
					Metrics: backtest.CalculateMetrics(result.Snapshots, result.Trades, w.cfg.InitialCapital),
					Stats:   stats,
				})
			}
			w.days, w.bars, w.analyzer, w.buildPlan = nil, nil, nil, nil
			runtime.GC()
		}
	}

	// 逐窗口打印：与同窗口 base 比。
	by := map[string]map[string]logicRun{}
	order := make([]string, 0, len(runs))
	for _, r := range runs {
		key := r.Portfolio + "|" + r.Window
		if by[key] == nil {
			by[key] = map[string]logicRun{}
			order = append(order, key)
		}
		by[key][r.Case] = r
	}
	sort.Strings(order)
	for _, key := range order {
		group := by[key]
		base, ok := group["L0_base"]
		if !ok {
			continue
		}
		for _, c := range cases {
			if c.name == "L0_base" {
				continue
			}
			r, ok := group[c.name]
			if !ok {
				continue
			}
			t.Logf("%s / %-26s 收益 %8.2f%%（%+7.2f）回撤 %6.2f%%（%+5.2f）成交 %3d（%+3d）",
				key, c.name,
				r.Metrics.TotalReturn, r.Metrics.TotalReturn-base.Metrics.TotalReturn,
				r.Metrics.MaxDrawdown, r.Metrics.MaxDrawdown-base.Metrics.MaxDrawdown,
				r.Metrics.TotalTrades, r.Metrics.TotalTrades-base.Metrics.TotalTrades)
		}
	}

	out := os.Getenv("FREESTOCK_CA_LOGIC_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "chanlun-logic.json")
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	raw, err := json.MarshalIndent(map[string]any{
		"generatedAt": time.Now().Format(time.RFC3339),
		"windows":     windowDescriptions(windows),
		"cases":       logicCaseDescriptions(cases),
		"runs":        runs,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	t.Logf("结果已写入：%s", out)
}

// logicCase 描述一种「以缠论为主」的买卖时序。
type logicCase struct {
	name string
	note string

	// 买入：策略想买时先挂起，等缠论二/三买确认（或触发）。
	waitBuy        string // "" / "buy3" / "buy23"
	waitBars       int    // 最多等多少个交易日；<=0 表示不等
	waitFallback   bool   // 超时后是否照常买入
	allowTriggered bool   // 触发信号也算（更早，但噪声更大）

	// 卖出：压住原策略的 Clear/Reduce，等缠论卖点。
	deferExit      bool
	deferBars      int // <=0 表示一直等到卖点（不超时）
	exitOnlyOnSell bool

	// 卖出叠加：直接按「触发」级别减仓（保留原策略退出）。
	sellTriggered bool
	sell3Layers   int

	// providerMode="" 用确认/触发信号；"candidate" 用「形成中」的候选信号
	// （比确认更早，但可靠性更低）。
	providerMode string
}

func logicCases() []logicCase {
	return []logicCase{
		{name: "L0_base", note: "基线：现有趋势低吸"},
		{name: "W1_wait_buy3_10", note: "想买时等确认三买，最多10日，超时放弃", waitBuy: "buy3", waitBars: 10},
		{name: "W2_wait_buy3_20", note: "等确认三买，最多20日", waitBuy: "buy3", waitBars: 20},
		{name: "W3_wait_buy3_trigger_10", note: "等三买（触发也算），最多10日", waitBuy: "buy3", waitBars: 10, allowTriggered: true},
		{name: "W4_wait_buy23_10", note: "等二/三买确认，最多10日", waitBuy: "buy23", waitBars: 10},
		{name: "W5_wait_or_enter_10", note: "等三买最多10日；超时仍照常买", waitBuy: "buy3", waitBars: 10, waitFallback: true},
		{name: "X1_defer_exit_5", note: "策略要卖先压5日，等卖点；超时照卖", deferExit: true, deferBars: 5},
		{name: "X2_defer_exit_10", note: "策略要卖先压10日，等卖点", deferExit: true, deferBars: 10},
		{name: "X3_defer_until_sell", note: "策略要卖就一直压，直到出现卖点", deferExit: true, deferBars: 0},
		{name: "X4_exit_only_on_sell", note: "完全忽略策略退出，只按缠论卖点退出", exitOnlyOnSell: true},
		{name: "X5_sell3_triggered_2", note: "保留策略退出，并用触发级三卖额外减2层", sellTriggered: true, sell3Layers: 2},
		{name: "W6_wait_buy3_10_plus_sell3trig", note: "等三买+触发级三卖减仓", waitBuy: "buy3", waitBars: 10, sellTriggered: true, sell3Layers: 2},
		{name: "C1_wait_buy3_cand_10", note: "等「形成中」的三买（候选级），最多10日", waitBuy: "buy3", waitBars: 10, providerMode: "candidate"},
		{name: "C2_defer_exit_cand", note: "策略要卖先压住，直到出现候选级卖点", deferExit: true, deferBars: 0, providerMode: "candidate"},
		{name: "C3_wait_buy3_cand_10_exit_cand", note: "候选级三买等10日 + 候选级卖点退出", waitBuy: "buy3", waitBars: 10, deferExit: true, deferBars: 0, providerMode: "candidate"},
	}
}

func logicCaseDescriptions(cases []logicCase) []map[string]string {
	out := make([]map[string]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, map[string]string{"name": c.name, "note": c.note})
	}
	return out
}

// logicStats 记录测试逻辑实际发生了什么，避免「看起来有效其实一笔没动」。
type logicStats struct {
	EntriesPending  int `json:"entriesPending"`
	EntriesFilled   int `json:"entriesFilled"`
	EntriesExpired  int `json:"entriesExpired"`
	EntriesFallback int `json:"entriesFallback"`
	ExitsDeferred   int `json:"exitsDeferred"`
	ExitsOnSignal   int `json:"exitsOnSignal"`
	ExitsExpired    int `json:"exitsExpired"`
	ExitsSuppressed int `json:"exitsSuppressed"`
	SellTriggers    int `json:"sellTriggers"`
	LayersSold      int `json:"layersSold"`
}

type logicPending struct {
	row        score.RotationRow
	createdDay int
	deadline   int
}

type logicDeferred struct {
	deadline int // 0 = 一直等
}

type logicState struct {
	lc       logicCase
	provider chanlunassist.Provider
	day      int
	pending  map[string]*logicPending
	deferred map[string]*logicDeferred
	stats    logicStats
}

func newLogicState(lc logicCase, analyzer *chanlunassist.Analyzer) *logicState {
	fresh := lc.waitBars
	if fresh < 10 {
		fresh = 10
	}
	providerCfg := chanlunassist.Config{
		Enable: true, Buy2ExtraLayers: 1, Buy3ExtraLayers: 1,
		BuyFreshBars: fresh, Sell1Layers: 1, Sell2Layers: 1, Sell3Layers: 1,
		SellFreshBars: 10, MaxLayersPerStock: 10,
		AllowTriggered: lc.allowTriggered || lc.sellTriggered,
	}
	return &logicState{
		lc: lc, provider: gridSignalProvider{inner: analyzer, cfg: providerCfg},
		pending: map[string]*logicPending{}, deferred: map[string]*logicDeferred{},
	}
}

func runLogicCase(w *gridWindow, lc logicCase) (*backtest.Result, logicStats, error) {
	engine := backtest.New(w.cfg, w.days, w.bars, w.benchmark)
	if len(w.actions) > 0 {
		engine.SetCorporateActions(w.actions)
	}
	state := newLogicState(lc, w.analyzer)
	if lc.providerMode == "candidate" {
		state.provider = newCandidateProvider(w.chanBars, lc.waitBars)
	}
	base := w.buildPlan
	builder := func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		asOf, err := strconv.Atoi(day.Date)
		if err != nil {
			return base(held, day)
		}
		return state.transform(base(held, day), held, asOf)
	}
	// 叠加「触发级卖出」时复用既有 Apply（只开卖出侧）。
	if lc.sellTriggered {
		cfg := chanlunassist.Config{
			Enable: true, Sell3Layers: lc.sell3Layers,
			SellFreshBars: 10, MaxLayersPerStock: 7,
			AllowTriggered: true,
		}
		provider := gridSignalProvider{inner: w.analyzer, cfg: chanlunassist.Config{
			Enable: true, Buy2ExtraLayers: 1, Buy3ExtraLayers: 1, BuyFreshBars: 10,
			Sell1Layers: 1, Sell2Layers: 1, Sell3Layers: 1, SellFreshBars: 10,
			MaxLayersPerStock: 10, AllowTriggered: true,
		}}
		inner := builder
		overlayState := chanlunassist.NewState()
		builder = func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
			plan := inner(held, day)
			asOf, err := strconv.Atoi(day.Date)
			if err != nil {
				return plan
			}
			beforeSold := overlayState.Stats.SellLayersSold
			out := chanlunassist.Apply(plan, held, asOf, provider, cfg, overlayState)
			state.stats.LayersSold += overlayState.Stats.SellLayersSold - beforeSold
			return out
		}
	}
	result, err := engine.Run(builder)
	if err != nil {
		return nil, logicStats{}, err
	}
	return result, state.stats, nil
}

// transform 是「以缠论为主」的时序改写。
func (s *logicState) transform(plan score.RotationPlan, held []score.Row, asOf int) score.RotationPlan {
	s.day++
	heldLayers := map[string]int{}
	heldSet := map[string]bool{}
	for _, row := range held {
		heldLayers[row.Code] = row.PositionLayers
		heldSet[row.Code] = true
	}
	// 持股票数上限：挂起单最多保留 20 个，避免无限堆积。
	if len(s.pending) > 20 {
		s.dropOldestPending()
	}

	rows := append([]score.RotationRow(nil), plan.Rows...)
	out := make([]score.RotationRow, 0, len(rows)+4)

	// ① 处理已压住的退出：有卖点/超时 → 补一条 Clear。
	handledDeferred := map[string]bool{}
	for _, code := range sortedKeys(s.deferred) {
		state := s.deferred[code]
		if !heldSet[code] {
			delete(s.deferred, code)
			continue
		}
		signal := bestSell(s.provider.Signals(code, asOf))
		if signal != nil {
			out = append(out, s.clearRow(code, held, heldLayers[code],
				"缠论接力退出|"+signal.TypeCN+"确认"))
			s.stats.ExitsOnSignal++
			handledDeferred[code] = true
			delete(s.deferred, code)
			continue
		}
		if state.deadline > 0 && s.day >= state.deadline {
			out = append(out, s.clearRow(code, held, heldLayers[code], "延迟到期|按原计划退出"))
			s.stats.ExitsExpired++
			handledDeferred[code] = true
			delete(s.deferred, code)
		}
	}
	// 已补过 Clear 的持仓，把基计划里同股的行全部丢掉（避免重复卖/加层）。
	if len(handledDeferred) > 0 {
		filtered := rows[:0]
		for _, row := range rows {
			if handledDeferred[row.Code] && row.SourceType == "holding" {
				continue
			}
			filtered = append(filtered, row)
		}
		rows = filtered
	}

	// ② 逐行处理基计划。
	for _, row := range rows {
		switch row.Action {
		case score.ActionClear, score.ActionReduce:
			if row.SourceType == "holding" && (s.lc.deferExit || s.lc.exitOnlyOnSell) {
				if _, ok := s.deferred[row.Code]; ok {
					continue // 已经在压着
				}
				if s.lc.exitOnlyOnSell {
					if bestSell(s.provider.Signals(row.Code, asOf)) != nil {
						out = append(out, row)
					} else {
						s.stats.ExitsSuppressed++
					}
					continue
				}
				if signal := bestSell(s.provider.Signals(row.Code, asOf)); signal != nil {
					out = append(out, row)
					s.stats.ExitsOnSignal++
					continue
				}
				deadline := 0
				if s.lc.deferBars > 0 {
					deadline = s.day + s.lc.deferBars
				}
				s.deferred[row.Code] = &logicDeferred{deadline: deadline}
				s.stats.ExitsDeferred++
				continue
			}
			out = append(out, row)

		case score.ActionAdd, score.ActionAddLayer, score.ActionReplaceIn:
			if s.lc.waitBuy == "" || row.CurrentLayers > 0 {
				out = append(out, row)
				continue
			}
			if buyOK(s.provider.Signals(row.Code, asOf), s.lc.waitBuy) {
				out = append(out, row)
				continue
			}
			if _, exists := s.pending[row.Code]; exists {
				continue
			}
			deadline := 0
			if s.lc.waitBars > 0 {
				deadline = s.day + s.lc.waitBars
			}
			s.pending[row.Code] = &logicPending{row: row, createdDay: s.day, deadline: deadline}
			s.stats.EntriesPending++

		default:
			out = append(out, row)
		}
	}

	// ③ 挂起单：每天检查一次信号。
	for _, code := range sortedKeys(s.pending) {
		p := s.pending[code]
		if heldSet[code] {
			delete(s.pending, code)
			continue
		}
		signal := bestBuy(s.provider.Signals(code, asOf), s.lc.waitBuy)
		if signal != nil {
			out = append(out, s.entryRow(p, signal))
			s.stats.EntriesFilled++
			delete(s.pending, code)
			continue
		}
		if p.deadline > 0 && s.day >= p.deadline {
			if s.lc.waitFallback {
				out = append(out, s.entryRow(p, nil))
				s.stats.EntriesFallback++
			} else {
				s.stats.EntriesExpired++
			}
			delete(s.pending, code)
		}
	}

	plan.Rows = out
	return plan
}

func (s *logicState) entryRow(p *logicPending, signal *chanlunassist.Signal) score.RotationRow {
	row := p.row
	row.Action = score.ActionAdd
	row.ActionLabel = score.ActionLabel(score.ActionAdd)
	row.SourceType = "candidate"
	row.CurrentLayers = 0
	row.FromPair = false
	if row.TargetLayers < 1 {
		row.TargetLayers = 1
	}
	row.LayersDelta = row.TargetLayers
	row.RankNo = 10000 + s.day
	reason := fmt.Sprintf("缠论接力建仓|等待%d日", s.day-p.createdDay)
	if signal != nil {
		reason += "|" + signal.TypeCN + "确认"
	} else {
		reason += "|超时照常买"
	}
	row.Reason = appendReasonText(row.Reason, reason)
	return row
}

func (s *logicState) clearRow(code string, held []score.Row, layers int, reason string) score.RotationRow {
	name := code
	for _, row := range held {
		if row.Code == code {
			name = row.Name
			break
		}
	}
	if layers < 1 {
		layers = 1
	}
	return score.RotationRow{
		Code: code, Name: name, Action: score.ActionClear,
		ActionLabel: score.ActionLabel(score.ActionClear), SourceType: "holding",
		CurrentLayers: layers, TargetLayers: 0, LayersDelta: -layers, Reason: reason,
	}
}

func (s *logicState) dropOldestPending() {
	var oldest string
	oldestDay := 1 << 30
	for code, p := range s.pending {
		if p.createdDay < oldestDay {
			oldestDay, oldest = p.createdDay, code
		}
	}
	if oldest != "" {
		delete(s.pending, oldest)
	}
}

func bestBuy(snapshot chanlunassist.Snapshot, mode string) *chanlunassist.Signal {
	switch mode {
	case "buy3":
		return snapshot.Buy3
	case "buy23":
		if snapshot.Buy3 != nil {
			return snapshot.Buy3
		}
		return snapshot.Buy2
	}
	return nil
}

func buyOK(snapshot chanlunassist.Snapshot, mode string) bool {
	return bestBuy(snapshot, mode) != nil
}

func bestSell(snapshot chanlunassist.Snapshot) *chanlunassist.Signal {
	if snapshot.Sell3 != nil {
		return snapshot.Sell3
	}
	if snapshot.Sell2 != nil {
		return snapshot.Sell2
	}
	return snapshot.Sell1
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func appendReasonText(base, extra string) string {
	if base == "" {
		return extra
	}
	return base + "｜" + extra
}

func splitCSV(raw string) []string {
	out := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}
