package cnlake

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// 复权因子（meta/adj_factors_cache）
//
// 数据湖的 `daily_bars` 是**不复权**行情。实测：002594 在 2025-07-29 因 1:3 送转
// 由 337.00 直接变成 111.42，而未复权价算出来的 MA250 仍是 ~310（ma250/close≈2.9）。
//
// 后果（本项目实测）：
//   - 除权日 `close < MA30×0.96` 的发生率 **44.4%**，而平时只有 **29.8%**（1.49 倍）
//     → 大量"假破位"止损；
//   - 送转股在其后最长一年内**通过不了**任何 `close > MA` 条件
//     → 既被错误排除出候选池、也不计入广度 ready（实测候选池 -12.3%）。
//
// 更新器已把**后复权因子**落在 `<root>/meta/adj_factors_cache/<code>_<ex>_hfq.parquet`
// （阶梯函数，从基期 1900-01-01 起，除权日跳变）。据此：
//
//	hfq(t) = raw(t) × F(t)
//	qfq(t) = raw(t) × F(t) / F(latest)     // 前复权：锚定最新因子
//
// 语义要点：
//   - 只复权**价格**（OHLC）。成交量/成交额保持原始 —— 那是真实成交的钱，
//     而 F 是**每只股票各自的常数**，跨股票放大成交额会让"按成交额排序"失真；
//   - 前复权锚定最新价：新的除权事件会重算全部历史价格水平（信号不变、价格水平会变），
//     所以**回测要可复现时用 AdjHFQ**；
//   - 因子文件缺失（例如合成测试湖）时原样返回，不做任何改动。
// ---------------------------------------------------------------------------

// AdjMode 复权口径。
type AdjMode string

const (
	AdjNone AdjMode = "none" // 不复权（原始行情）
	AdjQFQ  AdjMode = "qfq"  // 前复权（锚定最新因子）
	AdjHFQ  AdjMode = "hfq"  // 后复权（历史固定，回测推荐）
)

// ParseAdjMode 解析配置值；空串与未知值都回落到**后复权**（本项目默认口径）。
//
// 默认为什么是 hfq 而不是 qfq：
//   - hfq 的 `F(t)` 只随时间前进，**历史值永不改变** → 增量入库安全、回测可复现；
//   - qfq 锚定"最新因子"，同一只股票的历史行若分批入库，**旧行与新行的尺度锚点不同**，
//     MA250 跨锚点会失真、产生假信号（详见文件顶部说明）；
//   - 两者对本策略的**信号完全等价**（均线关系/涨跌幅/回撤都是尺度不变的，
//     实测 45/45 笔首次破线日相同），差别只在价格水平 → 展示层可再换算成前复权。
func ParseAdjMode(v string) AdjMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "raw", "0", "false", "off":
		return AdjNone
	case "hfq", "back", "post":
		return AdjHFQ
	case "qfq", "front", "pre", "1", "true", "on":
		return AdjQFQ
	}
	return AdjHFQ
}

// adjFactorRow 对应 `<code>_<ex>_hfq.parquet` 的一行。
type adjFactorRow struct {
	TradeDate int32   `parquet:"trade_date,date"`
	Factor    float64 `parquet:"factor"`
}

// AdjFactorPoint 是阶梯函数上的一个点（Date 为 YYYYMMDD）。
type AdjFactorPoint struct {
	Date   int64
	Factor float64
}

// adjFactorCache 惰性载入每只股票的因子序列。
type adjFactorCache struct {
	mu     sync.Mutex
	byCode map[string][]AdjFactorPoint
	index  map[string]string // 6 位代码 -> 因子文件路径
	loaded bool
}

// AdjFactorDir 返回复权因子目录（`<root>/meta/adj_factors_cache`）。
func (l *Lake) AdjFactorDir() string {
	if l == nil || l.root == "" {
		return ""
	}
	return filepath.Join(l.root, "meta", "adj_factors_cache")
}

// HasAdjFactors 判断复权因子目录是否可用。
func (l *Lake) HasAdjFactors() bool {
	dir := l.AdjFactorDir()
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// factors 返回复权因子缓存（惰性创建）。
func (l *Lake) factors() *adjFactorCache {
	l.adjMu.Lock()
	defer l.adjMu.Unlock()
	if l.adj == nil {
		l.adj = &adjFactorCache{byCode: map[string][]AdjFactorPoint{}, index: map[string]string{}}
	}
	return l.adj
}

// loadFactorIndex 扫描因子目录，建立"6 位代码 -> 文件路径"索引（只做一次）。
// 文件名形如 `002594_SZ_hfq.parquet`；只收 `_hfq` 后缀。
func (c *adjFactorCache) loadFactorIndex(dir string) {
	if c.loaded || dir == "" {
		return
	}
	c.loaded = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".parquet") || !strings.Contains(name, "_hfq") {
			continue
		}
		code := name
		if i := strings.IndexByte(name, '_'); i > 0 {
			code = name[:i]
		}
		if len(code) != 6 {
			continue
		}
		c.index[code] = filepath.Join(dir, name)
	}
}

// points 返回某只股票的因子阶梯（升序）；没有因子文件时返回 nil。
func (l *Lake) adjFactorPoints(code string) []AdjFactorPoint {
	code = BareSymbol(code)
	if code == "" {
		return nil
	}
	c := l.factors()
	c.mu.Lock()
	defer c.mu.Unlock()

	if pts, ok := c.byCode[code]; ok {
		return pts
	}

	c.loadFactorIndex(l.AdjFactorDir())
	path := c.index[code]
	if path == "" {
		c.byCode[code] = nil
		return nil
	}
	rows, err := readTyped[adjFactorRow]([]string{path}, nil)
	if err != nil || len(rows) == 0 {
		c.byCode[code] = nil
		return nil
	}
	pts := make([]AdjFactorPoint, 0, len(rows))
	for _, r := range rows {
		f := r.Factor
		if f <= 0 {
			continue
		}
		pts = append(pts, AdjFactorPoint{Date: daysToDate(r.TradeDate), Factor: f})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Date < pts[j].Date })
	c.byCode[code] = pts
	return pts
}

// adjScaleAt 是纯函数：在阶梯 points 上求该交易日的复权乘数（1.0 表示不变）。
// 单独抽出来是为了可以脱离文件系统做单元测试。
//
//	AdjHFQ: F(t)
//	AdjQFQ: F(t) / F(latest)
func adjScaleAt(points []AdjFactorPoint, date int64, mode AdjMode, anchor float64) float64 {
	if len(points) == 0 || mode == AdjNone {
		return 1
	}
	// 找最后一个 Date <= date 的点（points 升序）
	idx := sort.Search(len(points), func(i int) bool { return points[i].Date > date })
	if idx == 0 {
		return 1 // 该日之前还没有因子记录 → 不复权
	}
	f := points[idx-1].Factor
	if f <= 0 {
		return 1
	}
	switch mode {
	case AdjHFQ:
		return f
	case AdjQFQ:
		if anchor <= 0 {
			return 1
		}
		return f / anchor
	}
	return 1
}

// AdjustBars 返回按 mode 复权后的行情副本（**不变更入参**）。
//
// ⚠ 必须返回副本：批量缓存与"按股票"缓存共享同一份底层数组
// （`barsForRange` 返回的就是缓存切片的子切片），原地改写会污染缓存。
//
// 只改 OHLC；Volume / Amount 保持原始成交口径。
func (l *Lake) AdjustBars(code string, bars []BarRow, mode AdjMode) []BarRow {
	if l == nil || mode == AdjNone || len(bars) == 0 {
		return bars
	}
	points := l.adjFactorPoints(code)
	if len(points) == 0 {
		return bars
	}
	anchor := points[len(points)-1].Factor

	out := make([]BarRow, len(bars))
	copy(out, bars)
	changed := false
	for i := range out {
		s := adjScaleAt(points, out[i].Date(), mode, anchor)
		if s == 1 {
			continue
		}
		changed = true
		if out[i].Open > 0 {
			out[i].Open *= s
		}
		if out[i].High > 0 {
			out[i].High *= s
		}
		if out[i].Low > 0 {
			out[i].Low *= s
		}
		if out[i].Close > 0 {
			out[i].Close *= s
		}
	}
	if !changed {
		return bars
	}
	return out
}

// AdjScale 返回某只股票某交易日的复权乘数（不做任何 I/O 之外的副作用）；
// 没有因子时返回 1.0。用于只调整单个收盘价的场景（如基准/相对强度）。
func (l *Lake) AdjScale(code string, date int64, mode AdjMode) float64 {
	if l == nil || mode == AdjNone {
		return 1
	}
	points := l.adjFactorPoints(code)
	if len(points) == 0 {
		return 1
	}
	return adjScaleAt(points, date, mode, points[len(points)-1].Factor)
}
