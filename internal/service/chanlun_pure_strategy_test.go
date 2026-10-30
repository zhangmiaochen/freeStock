package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/chanlun"
	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 纯缠论策略实验（**只读、只测，不改生产代码**）。
//
// 策略定义：
//   - 买入信号：只来自缠论确认买点（可配 三买 / 二+三买 / 一+二+三买）；
//   - 卖出信号：只来自缠论确认卖点（可配 三卖 / 一+二+三卖）；
//   - 前置过滤（按信号日时点）：
//     总市值 100亿~2000亿；PE(TTM) 10~300；PB 2~10；ROE=PB/PE>8%；
//     排除 ST；最近一期单季净利润同比 >15%（用公告日做时点门控）。
//
// 市场组合：主板 / 创业板 / 科创板 / 主板+创业板 / 创业板+科创板 / 全部。
//
//	$env:FREESTOCK_CA_DB="...\bin\portfolio.db"
//	$env:FREESTOCK_CA_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_CA_PURE_START="20241014"; $env:FREESTOCK_CA_PURE_END="20261019"
//	$env:FREESTOCK_CA_PURE_OUT=".gotmp\chanlun-pure.json"
//	go test ./internal/service -run TestPureChanlunStrategy -v -timeout 180m
func TestPureChanlunStrategy(t *testing.T) {
	lakeRoot := os.Getenv("FREESTOCK_CA_LAKE")
	start := envInt(t, "FREESTOCK_CA_PURE_START", 0)
	end := envInt(t, "FREESTOCK_CA_PURE_END", 0)
	if lakeRoot == "" || start <= 0 || end <= 0 {
		t.Skip("未设置 FREESTOCK_CA_LAKE / FREESTOCK_CA_PURE_START / FREESTOCK_CA_PURE_END")
	}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	cfgDir, err := os.MkdirTemp("", "freestock-ca-pure-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfgDir) })
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	dbPath := os.Getenv("FREESTOCK_CA_DB")
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "portfolio.db")
	}
	svc := NewPortfolioServiceAt(stock, dbPath)
	t.Cleanup(svc.Close)

	lake := stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") || !lake.HasDataset("valuation_metrics") {
		t.Fatalf("数据湖缺少 daily_bars / valuation_metrics")
	}
	began := time.Now()
	data := buildPureData(t, svc, start, end)
	t.Logf("数据准备完成：%d 只股票、%d 个交易日、买信号 %d 个、卖信号 %d 个、耗时 %s",
		len(data.codes), len(data.dates), data.buySignalCount(), data.sellSignalCount(),
		time.Since(began).Round(time.Second))
	t.Logf("买信号逐项过滤通过数（非累计）：%v", data.filterStages())

	cases := pureCases()
	results := make([]pureResult, 0, len(cases))
	for _, c := range cases {
		runBegan := time.Now()
		result, err := runPureCase(data, c)
		if err != nil {
			t.Fatalf("%s 回测失败：%v", c.name, err)
		}
		result.ElapsedSeconds = time.Since(runBegan).Seconds()
		results = append(results, result)
		m := result.Metrics
		t.Logf("%-28s 收益 %8.2f%% 年化 %7.2f%% 回撤 %6.2f%% 夏普 %5.2f 成交 %3d 胜率 %5.1f%% 仓位 %5.1f%%",
			c.name, m.TotalReturn, m.AnnualReturn, m.MaxDrawdown, m.SharpeRatio,
			m.TotalTrades, m.WinRate, m.AvgPositionRatio)
	}

	out := os.Getenv("FREESTOCK_CA_PURE_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "chanlun-pure.json")
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	raw, err := json.MarshalIndent(map[string]any{
		"generatedAt": time.Now().Format(time.RFC3339),
		"startDate":   start, "endDate": end,
		"stocks": len(data.codes), "tradingDays": len(data.dates),
		"cases": casedescriptions(cases), "results": results,
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

// ---------------------------------------------------------------------------
// 数据
// ---------------------------------------------------------------------------

type sharePoint struct {
	date   int
	shares float64
}

type equityPoint struct {
	announce int
	period   string
	equity   float64
}

type quarterPoint struct {
	announce int
	period   string
	qValue   float64
	qYoY     float64
}

type pureEvent struct {
	code    string
	typ     chanlun.SignalType
	score   float64
	confirm int
	invalid int
	expire  int
}

type pureCandidate struct {
	event pureEvent
	age   int
}

// dirPoint 是某一周/月结束时、**当时**能确认的大周期方向。
type dirPoint struct {
	date    int
	up      bool
	notDown bool
}

type pureData struct {
	start, end int
	dates      []int
	dateIndex  map[int]int
	codes      []string
	names      map[string]string
	market     map[string]string
	bars       []backtest.PriceBar
	benchmark  map[string]float64
	buyEvents  map[int][]pureEvent
	sellEvents map[string]map[int]pureEvent
	shares     map[string][]sharePoint
	equity     map[string][]equityPoint
	quarters   map[string][]quarterPoint
	closes     map[string]map[int]float64
	weeklyDir  map[string][]dirPoint
	monthlyDir map[string][]dirPoint
	weeklySig  map[string][]dirPoint
	monthlySig map[string][]dirPoint
}

type pureCase struct {
	name       string
	market     string // main/gem/star/main+gem/gem+star/all
	entry      string // buy3/buy23/buy123
	exit       string // sell3/sell123
	positions  int
	layersEach int
	freshBars  int    // 0 = 只在确认日当天买；N = 确认后 N 个交易日内仍可买
	timeMode   string // ""/W_up/M_up/WM_up/W_notdown/M_notdown/W_sig/M_sig/WM_sig
	sizeMode   string // ""/W_up_half/W_sig_half/M_up_half：大周期向上满仓，否则半仓
}

func (c pureCase) String() string { return c.name }

func pureCases() []pureCase {
	markets := []string{"main", "gem", "star", "main+gem", "gem+star", "all"}
	cases := make([]pureCase, 0, len(markets)*3+4)
	for _, market := range markets {
		for _, entry := range []string{"buy3", "buy23", "buy123"} {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("%s|%s|sell123|5", market, entry),
				market: market, entry: entry, exit: "sell123",
				positions: 5, layersEach: 2,
			})
		}
	}
	for _, entry := range []string{"buy3", "buy23"} {
		cases = append(cases, pureCase{
			name:   fmt.Sprintf("all|%s|sell3|5", entry),
			market: "all", entry: entry, exit: "sell3",
			positions: 5, layersEach: 2,
		})
		cases = append(cases, pureCase{
			name:   fmt.Sprintf("all|%s|sell123|10", entry),
			market: "all", entry: entry, exit: "sell123",
			positions: 10, layersEach: 1,
		})
	}
	// 新鲜度：确认后 N 个交易日内仍允许买入（0 = 只在确认日当天）。
	for _, fresh := range []int{3, 5, 10, 20} {
		for _, market := range markets {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("%s|buy23|sell123|5|f%d", market, fresh),
				market: market, entry: "buy23", exit: "sell123",
				positions: 5, layersEach: 2, freshBars: fresh,
			})
		}
		for _, entry := range []string{"buy3", "buy123"} {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("all|%s|sell123|5|f%d", entry, fresh),
				market: "all", entry: entry, exit: "sell123",
				positions: 5, layersEach: 2, freshBars: fresh,
			})
		}
	}
	// 多周期：大周期（周/月）定方向，小周期（日线）定买卖点。
	for _, mode := range []string{"W_up", "M_up", "WM_up", "W_notdown", "M_notdown", "W_sig", "M_sig", "WM_sig"} {
		for _, market := range markets {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("%s|buy23|sell123|5|f10|%s", market, mode),
				market: market, entry: "buy23", exit: "sell123",
				positions: 5, layersEach: 2, freshBars: 10, timeMode: mode,
			})
		}
		for _, entry := range []string{"buy3", "buy123"} {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("all|%s|sell123|5|f10|%s", entry, mode),
				market: "all", entry: entry, exit: "sell123",
				positions: 5, layersEach: 2, freshBars: 10, timeMode: mode,
			})
		}
	}
	// 大周期方向做仓位：向上 2 层，否则 1 层（不直接否决日线买点）。
	for _, sizeMode := range []string{"W_up_half", "W_sig_half", "M_up_half"} {
		for _, market := range markets {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("%s|buy23|sell123|5|f10|%s", market, sizeMode),
				market: market, entry: "buy23", exit: "sell123",
				positions: 5, layersEach: 2, freshBars: 10, sizeMode: sizeMode,
			})
		}
		for _, entry := range []string{"buy3", "buy123"} {
			cases = append(cases, pureCase{
				name:   fmt.Sprintf("all|%s|sell123|5|f10|%s", entry, sizeMode),
				market: "all", entry: entry, exit: "sell123",
				positions: 5, layersEach: 2, freshBars: 10, sizeMode: sizeMode,
			})
		}
	}
	return cases
}

func casedescriptions(cases []pureCase) []map[string]string {
	out := make([]map[string]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, map[string]string{
			"name": c.name, "market": c.market, "entry": c.entry,
			"exit": c.exit, "positions": strconv.Itoa(c.positions),
		})
	}
	return out
}

func buildPureData(t *testing.T, svc *PortfolioService, start, end int) *pureData {
	t.Helper()
	lake := svc.stock.lakeHandle()
	if lake == nil {
		t.Fatal("数据湖未就绪")
	}
	instruments, err := lake.Instruments()
	if err != nil {
		t.Fatalf("读 instruments 失败：%v", err)
	}
	data := &pureData{
		start: start, end: end,
		names: map[string]string{}, market: map[string]string{},
		buyEvents: map[int][]pureEvent{}, sellEvents: map[string]map[int]pureEvent{},
		shares: map[string][]sharePoint{}, equity: map[string][]equityPoint{},
		quarters: map[string][]quarterPoint{}, closes: map[string]map[int]float64{},
		dateIndex: map[int]int{},
		weeklyDir: map[string][]dirPoint{}, monthlyDir: map[string][]dirPoint{},
		weeklySig: map[string][]dirPoint{}, monthlySig: map[string][]dirPoint{},
	}
	var codes []string
	for _, item := range instruments {
		code := portfolio.BareCode(item.Symbol)
		market := pureMarketOf(code)
		if market == "" || item.AssetType != "stock" {
			continue
		}
		if item.ListDate > 0 && int(item.ListDate) > end {
			continue
		}
		if item.DelistDate > 0 && int(item.DelistDate) < start {
			continue
		}
		codes = append(codes, code)
		data.names[code] = item.Name
		data.market[code] = market
	}
	sort.Strings(codes)
	data.codes = codes
	if len(codes) == 0 {
		t.Fatal("没有可用的股票")
	}
	t.Logf("标的池 %d 只（主板/创业板/科创板）", len(codes))

	// 日线（含 550 天预热）
	barsByCode, err := svc.loadChanlunBars(codes, shiftBack(start, 1700), end)
	if err != nil {
		t.Fatalf("取日线失败：%v", err)
	}
	if len(barsByCode) == 0 {
		t.Fatal("没有日线")
	}
	// 股本（用于市值）；变更日作为可用日（数据湖 typed 视图没有公告日，
	// 这是本实验的一个已知近似）。
	yearFrom := start/10000 - 1
	shareRows, err := lake.AllShareStructuresSince(yearFrom)
	if err != nil {
		t.Fatalf("读取股本结构失败：%v", err)
	}
	for _, row := range shareRows {
		code := portfolio.BareCode(row.Symbol)
		if row.TotalShares <= 0 {
			continue
		}
		data.shares[code] = append(data.shares[code], sharePoint{
			date: int(row.Date()), shares: row.TotalShares,
		})
	}
	for code := range data.shares {
		sort.Slice(data.shares[code], func(i, j int) bool {
			return data.shares[code][i].date < data.shares[code][j].date
		})
	}

	// 财务：净利润（单季同比）+ 净资产（PB/ROE）
	finRows, err := lake.AllStatementItems([]string{"net_profit", "total_equity"}, start/10000-2, end/10000)
	if err != nil {
		t.Fatalf("读取财务失败：%v", err)
	}
	buildFinancials(finRows, data)
	t.Logf("股本变更 %d 行（%d 只）；财务 %d 行（单季 %d 只 / 净资产 %d 只）",
		len(shareRows), len(data.shares), len(finRows), len(data.quarters), len(data.equity))

	// 缠论信号 + 回测价格
	for _, code := range codes {
		bars := barsByCode[code]
		if len(bars) < 80 {
			continue
		}
		name := data.names[code]
		if data.closes[code] == nil {
			data.closes[code] = map[int]float64{}
		}
		for _, bar := range bars {
			if bar.Date < start || bar.Date > end {
				continue
			}
			data.closes[code][bar.Date] = bar.Close
			data.bars = append(data.bars, backtest.PriceBar{
				Date: strconv.Itoa(bar.Date), Code: code, Name: name,
				Open: bar.Open, Close: bar.Close,
			})
		}
		analysis := chanlun.Analyze(bars, chanlun.LevelDay, chanlun.DefaultConfig(), chanlun.AdaptInfo{})
		weeklyBars := aggregateBars(bars, "week")
		monthlyBars := aggregateBars(bars, "month")
		data.weeklyDir[code] = replayDirection(weeklyBars, chanlun.LevelWeek)
		data.monthlyDir[code] = replayDirection(monthlyBars, chanlun.LevelMonth)
		data.weeklySig[code] = signalDirection(weeklyBars, chanlun.LevelWeek)
		data.monthlySig[code] = signalDirection(monthlyBars, chanlun.LevelMonth)
		for _, sig := range analysis.Signals {
			if sig.ConfirmDate < start || sig.ConfirmDate > end || sig.ConfirmIndex < 0 {
				continue
			}
			event := pureEvent{
				code: code, typ: sig.Type, score: sig.Score,
				confirm: sig.ConfirmDate, invalid: sig.InvalidDate, expire: sig.ExpireDate,
			}
			switch sig.Type {
			case chanlun.SignalBuy1, chanlun.SignalBuy2, chanlun.SignalBuy3:
				data.buyEvents[sig.ConfirmDate] = append(data.buyEvents[sig.ConfirmDate], event)
			case chanlun.SignalSell1, chanlun.SignalSell2, chanlun.SignalSell3:
				if data.sellEvents[code] == nil {
					data.sellEvents[code] = map[int]pureEvent{}
				}
				if old, ok := data.sellEvents[code][sig.ConfirmDate]; !ok || event.score > old.score {
					data.sellEvents[code][sig.ConfirmDate] = event
				}
			}
		}
	}
	// 同一确认日同类型的买信号去重，保留高分。
	for date, list := range data.buyEvents {
		seen := map[string]pureEvent{}
		for _, event := range list {
			key := event.code + "|" + string(event.typ)
			if old, ok := seen[key]; !ok || event.score > old.score {
				seen[key] = event
			}
		}
		list = list[:0]
		for _, event := range seen {
			list = append(list, event)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].score != list[j].score {
				return list[i].score > list[j].score
			}
			return list[i].code < list[j].code
		})
		data.buyEvents[date] = list
	}

	// 交易日
	calendar, err := lake.TradingCalendar()
	if err != nil {
		t.Fatalf("读交易日历失败：%v", err)
	}
	for _, date := range calendar {
		if int(date) >= start && int(date) <= end {
			data.dates = append(data.dates, int(date))
		}
	}
	if len(data.dates) == 0 {
		t.Fatal("区间内没有交易日")
	}
	for idx, date := range data.dates {
		data.dateIndex[date] = idx
	}
	if codes, points, up := dirStats(data.weeklyDir); codes > 0 {
		t.Logf("周线趋势方向：%d 只 / %d 个点 / 向上 %.0f%%", codes, points, float64(up)/float64(points)*100)
	}
	if codes, points, up := dirStats(data.monthlyDir); codes > 0 {
		t.Logf("月线趋势方向：%d 只 / %d 个点 / 向上 %.0f%%", codes, points, float64(up)/float64(points)*100)
	}
	if codes, points, up := dirStats(data.weeklySig); codes > 0 {
		t.Logf("周线买卖点方向：%d 只 / %d 个点 / 多头 %.0f%%", codes, points, float64(up)/float64(points)*100)
	}
	if codes, points, up := dirStats(data.monthlySig); codes > 0 {
		t.Logf("月线买卖点方向：%d 只 / %d 个点 / 多头 %.0f%%", codes, points, float64(up)/float64(points)*100)
	}
	// 基准
	if svc.stock != nil {
		if closes, err := svc.stock.loadBenchmark("000300.SH", start, end); err == nil {
			data.benchmark = map[string]float64{}
			for date, closePrice := range closes {
				data.benchmark[strconv.FormatInt(date, 10)] = closePrice
			}
		}
	}
	return data
}

func (d *pureData) buySignalCount() int {
	total := 0
	for _, list := range d.buyEvents {
		total += len(list)
	}
	return total
}

func (d *pureData) sellSignalCount() int {
	total := 0
	for _, byDate := range d.sellEvents {
		total += len(byDate)
	}
	return total
}

// filterStages 统计每个前置条件单独通过多少条买信号（非累计），
// 用来定位「到底是哪个条件把候选池卡没了」。
func (d *pureData) filterStages() map[string]int {
	counts := map[string]int{
		"signals": 0, "fundamentals": 0, "mv": 0, "pe": 0, "pb": 0,
		"roe": 0, "growth": 0, "notST": 0, "all": 0,
	}
	for date, list := range d.buyEvents {
		for _, event := range list {
			counts["signals"]++
			f, ok := d.fundamentalsAt(event.code, date)
			if !ok {
				continue
			}
			counts["fundamentals"]++
			if f.mv >= 1e10 && f.mv <= 2e11 {
				counts["mv"]++
			}
			if f.pe >= 10 && f.pe <= 300 {
				counts["pe"]++
			}
			if f.pb >= 2 && f.pb <= 10 {
				counts["pb"]++
			}
			if f.roe > 8 {
				counts["roe"]++
			}
			if f.qYoY > 0.15 {
				counts["growth"]++
			}
			name := strings.ToUpper(d.names[event.code])
			if !strings.Contains(name, "ST") && !strings.Contains(name, "退") {
				counts["notST"]++
			}
			if d.fundamentalOK(event.code, date) {
				counts["all"]++
			}
		}
	}
	return counts
}

// growthPeriod 是某报告期的累计净利润与首次公告日。
type growthPeriod struct {
	cum      float64
	announce int
}

// buildFinancials 把累计净利润换算成「单季同比」，并整理净资产（PB/ROE 用）。
func buildFinancials(rows []cnlake.StatementRow, data *pureData) {
	cum := map[string]map[string]growthPeriod{}
	equity := map[string]map[string]equityPoint{}
	for _, row := range rows {
		if row.AnnounceDate <= 0 || row.ReportPeriod == "" {
			continue
		}
		code := portfolio.BareCode(row.Symbol)
		switch strings.ToLower(row.ItemCode) {
		case "net_profit":
			if cum[code] == nil {
				cum[code] = map[string]growthPeriod{}
			}
			old, ok := cum[code][row.ReportPeriod]
			// 同一报告期有多次披露时取**最早**公告，避免用后续修订制造未来数据。
			if !ok || int(row.AnnounceDate) < old.announce {
				cum[code][row.ReportPeriod] = growthPeriod{cum: row.ItemValue, announce: int(row.AnnounceDate)}
			}
		case "total_equity":
			if equity[code] == nil {
				equity[code] = map[string]equityPoint{}
			}
			old, ok := equity[code][row.ReportPeriod]
			if !ok || int(row.AnnounceDate) < old.announce {
				equity[code][row.ReportPeriod] = equityPoint{
					announce: int(row.AnnounceDate), period: row.ReportPeriod, equity: row.ItemValue,
				}
			}
		}
	}

	for code, periods := range cum {
		var points []quarterPoint
		for period, cur := range periods {
			year, quarter, ok := parsePeriod(period)
			if !ok || quarter < 1 || quarter > 4 {
				continue
			}
			qValue, ok := singleQuarter(periods, year, quarter)
			if !ok {
				continue
			}
			prev, ok := singleQuarter(periods, year-1, quarter)
			if !ok || prev <= 0 {
				continue
			}
			points = append(points, quarterPoint{
				announce: cur.announce, period: period, qValue: qValue, qYoY: qValue/prev - 1,
			})
		}
		sort.Slice(points, func(i, j int) bool {
			if points[i].announce != points[j].announce {
				return points[i].announce < points[j].announce
			}
			return points[i].period < points[j].period
		})
		if len(points) > 0 {
			data.quarters[code] = points
		}
	}

	for code, periods := range equity {
		list := make([]equityPoint, 0, len(periods))
		for _, point := range periods {
			if point.equity > 0 {
				list = append(list, point)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].announce != list[j].announce {
				return list[i].announce < list[j].announce
			}
			return list[i].period < list[j].period
		})
		if len(list) > 0 {
			data.equity[code] = list
		}
	}
}

func singleQuarter(periods map[string]growthPeriod, year, quarter int) (float64, bool) {
	key := func(y, q int) string { return fmt.Sprintf("%dQ%d", y, q) }
	cur, ok := periods[key(year, quarter)]
	if !ok {
		return 0, false
	}
	if quarter == 1 {
		return cur.cum, true
	}
	prev, ok := periods[key(year, quarter-1)]
	if !ok {
		return 0, false
	}
	return cur.cum - prev.cum, true
}

func parsePeriod(period string) (int, int, bool) {
	// YYYYQn
	if len(period) != 6 || period[4] != 'Q' {
		return 0, 0, false
	}
	year, err := strconv.Atoi(period[:4])
	if err != nil {
		return 0, 0, false
	}
	quarter, err := strconv.Atoi(period[5:])
	if err != nil {
		return 0, 0, false
	}
	return year, quarter, true
}

// aggregateBars 把日线聚合成周线/月线（OHLCV 标准聚合，日期取区间最后一个交易日）。
func aggregateBars(bars []chanlun.Bar, period string) []chanlun.Bar {
	if len(bars) == 0 {
		return nil
	}
	out := make([]chanlun.Bar, 0, len(bars)/5+1)
	var key string
	var cur chanlun.Bar
	flush := func() {
		if cur.Date > 0 {
			out = append(out, cur)
		}
	}
	for _, bar := range bars {
		parsed, err := time.Parse("20060102", strconv.Itoa(bar.Date))
		if err != nil {
			continue
		}
		var bucket string
		if period == "week" {
			year, week := parsed.ISOWeek()
			bucket = fmt.Sprintf("%d-%02d", year, week)
		} else {
			bucket = fmt.Sprintf("%d-%02d", parsed.Year(), int(parsed.Month()))
		}
		if bucket != key {
			flush()
			key = bucket
			cur = bar
			continue
		}
		if bar.High > cur.High {
			cur.High = bar.High
		}
		if bar.Low < cur.Low {
			cur.Low = bar.Low
		}
		cur.Close = bar.Close
		cur.Date = bar.Date
		cur.Volume += bar.Volume
		cur.Amount += bar.Amount
	}
	flush()
	return out
}

// replayDirection 用逐段重算（BuildReplay）得到「每一周/月结束时、当时能确认」的方向。
// 返回的 date 是该周期最后一个交易日；使用方只取 date < 当日 的状态，避免用未走完的周期。
// 入参 bars 必须是已经聚合好的周线/月线。
func replayDirection(bars []chanlun.Bar, level chanlun.Level) []dirPoint {
	step := 2
	maxSteps := 120
	if level == chanlun.LevelMonth {
		step = 1
		maxSteps = 40
	}
	if len(bars) < 35 {
		return nil
	}
	cfg := chanlun.DefaultConfig()
	cfg.ReplayStep = step
	cfg.ReplayMaxSteps = maxSteps
	replay := chanlun.BuildReplay(bars, level, cfg, chanlun.AdaptInfo{})
	out := make([]dirPoint, 0, len(replay.Steps))
	for _, snapshot := range replay.Steps {
		up := snapshot.Trend.Type == chanlun.TrendUp || snapshot.State.Name == chanlun.StateReverseUp
		notDown := snapshot.Trend.Type != chanlun.TrendDown && snapshot.State.Name != chanlun.StateReverseDown
		out = append(out, dirPoint{date: snapshot.Date, up: up, notDown: notDown})
	}
	return out
}

// signalDirection 用周线/月线**最近一个确认买卖点**定方向：
// 最近确认的是买点 → 多；是卖点 → 空。比「趋势类型=上涨」更宽松，也更贴近
// “大周期定方向”的实战用法。
func signalDirection(bars []chanlun.Bar, level chanlun.Level) []dirPoint {
	if len(bars) < 35 {
		return nil
	}
	analysis := chanlun.Analyze(bars, level, chanlun.DefaultConfig(), chanlun.AdaptInfo{})
	byDate := map[int]bool{} // date -> up（卖点优先）
	for _, signal := range analysis.Signals {
		if signal.ConfirmDate <= 0 {
			continue
		}
		up := signal.Direction == "buy"
		if old, ok := byDate[signal.ConfirmDate]; ok {
			// 同一天多空都有时，按空处理（风险优先）
			byDate[signal.ConfirmDate] = old && up
			continue
		}
		byDate[signal.ConfirmDate] = up
	}
	dates := make([]int, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Ints(dates)
	out := make([]dirPoint, 0, len(dates))
	for _, date := range dates {
		up := byDate[date]
		out = append(out, dirPoint{date: date, up: up, notDown: up})
	}
	return out
}

func (d *pureData) directionOK(code string, date int, mode string) bool {
	switch mode {
	case "":
		return true
	}
	switch mode {
	case "W_sig":
		point, ok := latestDir(d.weeklySig[code], date)
		return ok && point.up
	case "M_sig":
		point, ok := latestDir(d.monthlySig[code], date)
		return ok && point.up
	case "WM_sig":
		weekly, okW := latestDir(d.weeklySig[code], date)
		monthly, okM := latestDir(d.monthlySig[code], date)
		return okW && okM && weekly.up && monthly.up
	}
	weekly, okW := latestDir(d.weeklyDir[code], date)
	monthly, okM := latestDir(d.monthlyDir[code], date)
	switch mode {
	case "W_up":
		return okW && weekly.up
	case "M_up":
		return okM && monthly.up
	case "WM_up":
		return okW && okM && weekly.up && monthly.up
	case "W_notdown":
		return okW && weekly.notDown
	case "M_notdown":
		return okM && monthly.notDown
	case "WM_notdown":
		return okW && okM && weekly.notDown && monthly.notDown
	}
	return true
}

// latestDir 返回 date 之前（严格小于）最近一次已完成的周期状态。
func latestDir(list []dirPoint, date int) (dirPoint, bool) {
	if len(list) == 0 {
		return dirPoint{}, false
	}
	idx := sort.Search(len(list), func(i int) bool { return list[i].date >= date }) - 1
	if idx < 0 {
		return dirPoint{}, false
	}
	return list[idx], true
}

func dirStats(m map[string][]dirPoint) (codes, points, up int) {
	for _, list := range m {
		if len(list) == 0 {
			continue
		}
		codes++
		points += len(list)
		for _, point := range list {
			if point.up {
				up++
			}
		}
	}
	return codes, points, up
}

func pureMarketOf(code string) string {
	if len(code) != 6 {
		return ""
	}
	switch code[:3] {
	case "600", "601", "603", "605", "000", "001", "002", "003":
		return "main"
	case "300", "301", "302":
		return "gem"
	case "688", "689":
		return "star"
	}
	return ""
}

func marketAllowed(market string, combo string) bool {
	switch combo {
	case "all":
		return market != ""
	case "main+gem":
		return market == "main" || market == "gem"
	case "gem+star":
		return market == "gem" || market == "star"
	default:
		return market == combo
	}
}

// ---------------------------------------------------------------------------
// 回测
// ---------------------------------------------------------------------------

type pureResult struct {
	Name           string           `json:"name"`
	Market         string           `json:"market"`
	Entry          string           `json:"entry"`
	Exit           string           `json:"exit"`
	Positions      int              `json:"positions"`
	FreshBars      int              `json:"freshBars"`
	TimeMode       string           `json:"timeMode"`
	SizeMode       string           `json:"sizeMode"`
	Metrics        backtest.Metrics `json:"metrics"`
	BuySignals     int              `json:"buySignals"`
	BuyPassed      int              `json:"buySignalsPassFundamental"`
	Entries        int              `json:"entries"`
	Sells          int              `json:"sells"`
	ElapsedSeconds float64          `json:"elapsedSeconds"`
}

func runPureCase(data *pureData, c pureCase) (pureResult, error) {
	days := make([]backtest.StrengthDay, 0, len(data.dates))
	for _, date := range data.dates {
		days = append(days, backtest.StrengthDay{Date: strconv.Itoa(date)})
	}
	maxLayers := c.layersEach
	if maxLayers < 1 {
		maxLayers = 1
	}
	cfg := backtest.Config{
		StartDate: strconv.Itoa(data.start), EndDate: strconv.Itoa(data.end),
		TargetPositionCount: c.positions, MaxStockCount: c.positions,
		TotalLayers: 10, MaxLayersPerStock: maxLayers,
		InitialCapital: 1_000_000,
	}
	result := pureResult{
		Name: c.name, Market: c.market, Entry: c.entry, Exit: c.exit,
		Positions: c.positions, FreshBars: c.freshBars,
		TimeMode: c.timeMode, SizeMode: c.sizeMode,
	}
	entrySet := entryTypes(c.entry)
	exitSet := exitTypes(c.exit)

	// 统计：该市场组合下、通过基本面的买信号数量。
	for _, list := range data.buyEvents {
		for _, event := range list {
			if !marketAllowed(data.market[event.code], c.market) || !entrySet[event.typ] {
				continue
			}
			result.BuySignals++
			if data.fundamentalOK(event.code, event.confirm) {
				result.BuyPassed++
			}
		}
	}

	consumed := map[string]bool{}
	builder := func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		date, _ := strconv.Atoi(day.Date)
		heldCodes := map[string]bool{}
		usedLayers := 0
		for _, row := range held {
			heldCodes[row.Code] = true
			usedLayers += row.PositionLayers
		}
		var rows []score.RotationRow
		sold := map[string]bool{}
		// 卖：只认缠论确认卖点。
		for _, row := range held {
			event, ok := data.sellEvents[row.Code][date]
			if !ok || !exitSet[event.typ] {
				continue
			}
			if event.invalid > 0 && event.invalid <= date {
				continue
			}
			if event.expire > 0 && event.expire <= date {
				continue
			}
			rows = append(rows, score.RotationRow{
				Code: row.Code, Name: row.Name, Action: score.ActionClear,
				ActionLabel: score.ActionLabel(score.ActionClear), SourceType: "holding",
				CurrentLayers: row.PositionLayers, TargetLayers: 0,
				LayersDelta: -row.PositionLayers,
				Reason:      "纯缠论卖出|" + chanlun.SignalTypeCN(event.typ),
			})
			sold[row.Code] = true
			usedLayers -= row.PositionLayers
		}
		// 买：只认缠论确认买点 + 基本面前置过滤；允许在确认后 freshBars 个交易日内买入。
		candidates := make([]pureCandidate, 0, 8)
		queued := map[string]bool{}
		dayIdx, ok := data.dateIndex[date]
		if !ok {
			dayIdx = len(data.dates) - 1
		}
		for age := 0; age <= c.freshBars; age++ {
			idx := dayIdx - age
			if idx < 0 {
				break
			}
			eventDate := data.dates[idx]
			for _, event := range data.buyEvents[eventDate] {
				if !entrySet[event.typ] || !marketAllowed(data.market[event.code], c.market) {
					continue
				}
				if !data.directionOK(event.code, date, c.timeMode) {
					continue
				}
				if heldCodes[event.code] || sold[event.code] || queued[event.code] {
					continue
				}
				if consumed[buyEventKey(event)] {
					continue
				}
				if event.invalid > 0 && event.invalid <= date {
					continue
				}
				if event.expire > 0 && event.expire <= date {
					continue
				}
				if !data.fundamentalOK(event.code, date) {
					continue
				}
				candidates = append(candidates, pureCandidate{event: event, age: age})
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].age != candidates[j].age {
				return candidates[i].age < candidates[j].age
			}
			if candidates[i].event.score != candidates[j].event.score {
				return candidates[i].event.score > candidates[j].event.score
			}
			return candidates[i].event.code < candidates[j].event.code
		})
		slots := c.positions - (len(held) - len(sold))
		for _, candidate := range candidates {
			event := candidate.event
			targetLayers := maxLayers
			if c.sizeMode != "" {
				full := false
				switch c.sizeMode {
				case "W_up_half":
					full = data.directionOK(event.code, date, "W_up")
				case "W_sig_half":
					full = data.directionOK(event.code, date, "W_sig")
				case "M_up_half":
					full = data.directionOK(event.code, date, "M_up")
				}
				if !full && targetLayers > 1 {
					targetLayers = 1
				}
			}
			if slots <= 0 || usedLayers+targetLayers > 10 {
				break
			}
			reason := "纯缠论买入|" + chanlun.SignalTypeCN(event.typ)
			if candidate.age > 0 {
				reason += fmt.Sprintf("|确认后%d日", candidate.age)
			}
			if c.sizeMode != "" && targetLayers < maxLayers {
				reason += "|大周期未确认向上，半仓"
			}
			rows = append(rows, score.RotationRow{
				Code: event.code, Name: data.names[event.code], Action: score.ActionAdd,
				ActionLabel: score.ActionLabel(score.ActionAdd), SourceType: "candidate",
				Score: event.score, RankNo: len(rows) + 1,
				TargetLayers: targetLayers, LayersDelta: targetLayers,
				Reason: reason,
			})
			consumed[buyEventKey(event)] = true
			queued[event.code] = true
			slots--
			usedLayers += targetLayers
		}
		return score.RotationPlan{Rows: rows}
	}

	engine := backtest.New(cfg, days, data.bars, data.benchmark)
	res, err := engine.Run(builder)
	if err != nil {
		return result, err
	}
	result.Metrics = backtest.CalculateMetrics(res.Snapshots, res.Trades, cfg.InitialCapital)
	for _, trade := range res.Trades {
		if trade.Action == "buy" {
			result.Entries++
		} else {
			result.Sells++
		}
	}
	return result, nil
}

func buyEventKey(event pureEvent) string {
	return event.code + "|" + string(event.typ) + "|" + strconv.Itoa(event.confirm)
}

func entryTypes(mode string) map[chanlun.SignalType]bool {
	switch mode {
	case "buy3":
		return map[chanlun.SignalType]bool{chanlun.SignalBuy3: true}
	case "buy23":
		return map[chanlun.SignalType]bool{chanlun.SignalBuy2: true, chanlun.SignalBuy3: true}
	default:
		return map[chanlun.SignalType]bool{
			chanlun.SignalBuy1: true, chanlun.SignalBuy2: true, chanlun.SignalBuy3: true,
		}
	}
}

func exitTypes(mode string) map[chanlun.SignalType]bool {
	switch mode {
	case "sell3":
		return map[chanlun.SignalType]bool{chanlun.SignalSell3: true}
	default:
		return map[chanlun.SignalType]bool{
			chanlun.SignalSell1: true, chanlun.SignalSell2: true, chanlun.SignalSell3: true,
		}
	}
}

// ---------------------------------------------------------------------------
// 基本面：按信号日时点判断
// ---------------------------------------------------------------------------

type fundamentalSnapshot struct {
	mv   float64
	pe   float64
	pb   float64
	roe  float64
	qYoY float64
}

func (d *pureData) fundamentalOK(code string, date int) bool {
	// ST：用当前名称近似（数据湖没有逐日 ST 标记的轻量读法）。
	name := strings.ToUpper(d.names[code])
	if strings.Contains(name, "ST") || strings.Contains(name, "退") {
		return false
	}
	f, ok := d.fundamentalsAt(code, date)
	if !ok {
		return false
	}
	if f.mv < 1e10 || f.mv > 2e11 { // 100亿 ~ 2000亿
		return false
	}
	if f.pe < 10 || f.pe > 300 {
		return false
	}
	if f.pb < 2 || f.pb > 10 {
		return false
	}
	if f.roe <= 8 {
		return false
	}
	if f.qYoY <= 0.15 {
		return false
	}
	return true
}

// fundamentalsAt 用「收盘价 × 总股本」和财报（TTM 净利润 / 净资产）现算
// 市值、PE、PB、ROE，全部按信号日时点取当时已公告的数据。
func (d *pureData) fundamentalsAt(code string, date int) (fundamentalSnapshot, bool) {
	closePrice, ok := d.closes[code][date]
	if !ok || closePrice <= 0 {
		return fundamentalSnapshot{}, false
	}
	shares, ok := d.latestShares(code, date)
	if !ok || shares <= 0 {
		return fundamentalSnapshot{}, false
	}
	ttm, ok := d.ttmNetProfit(code, date)
	if !ok || ttm <= 0 {
		return fundamentalSnapshot{}, false
	}
	equity, ok := d.latestEquity(code, date)
	if !ok || equity <= 0 {
		return fundamentalSnapshot{}, false
	}
	quarter, ok := d.latestQuarter(code, date)
	if !ok {
		return fundamentalSnapshot{}, false
	}
	mv := closePrice * shares
	return fundamentalSnapshot{
		mv: mv, pe: mv / ttm, pb: mv / equity,
		roe: ttm / equity * 100, qYoY: quarter.qYoY,
	}, true
}

func (d *pureData) latestShares(code string, date int) (float64, bool) {
	list := d.shares[code]
	if len(list) == 0 {
		return 0, false
	}
	idx := sort.Search(len(list), func(i int) bool { return list[i].date > date }) - 1
	if idx < 0 {
		return 0, false
	}
	return list[idx].shares, true
}

func (d *pureData) latestEquity(code string, date int) (float64, bool) {
	list := d.equity[code]
	if len(list) == 0 {
		return 0, false
	}
	idx := sort.Search(len(list), func(i int) bool { return list[i].announce > date }) - 1
	if idx < 0 {
		return 0, false
	}
	return list[idx].equity, true
}

func (d *pureData) latestQuarter(code string, date int) (quarterPoint, bool) {
	list := d.quarters[code]
	if len(list) == 0 {
		return quarterPoint{}, false
	}
	idx := sort.Search(len(list), func(i int) bool { return list[i].announce > date }) - 1
	if idx < 0 {
		return quarterPoint{}, false
	}
	return list[idx], true
}

// ttmNetProfit 取信号日已公告的最近 4 个单季净利润之和。
func (d *pureData) ttmNetProfit(code string, date int) (float64, bool) {
	list := d.quarters[code]
	byPeriod := map[string]float64{}
	for _, point := range list {
		if point.announce > date {
			continue
		}
		byPeriod[point.period] = point.qValue
	}
	if len(byPeriod) < 4 {
		return 0, false
	}
	periods := make([]string, 0, len(byPeriod))
	for period := range byPeriod {
		periods = append(periods, period)
	}
	sort.Strings(periods)
	periods = periods[len(periods)-4:]
	total := 0.0
	for _, period := range periods {
		total += byPeriod[period]
	}
	return total, true
}
