// 行业归属的**时点视图**（point-in-time）。
//
// 为什么需要单独一层：`IndustryMembers()` 返回的是"每个行业**最新**一份快照"，
// 它服务于"看当下"的场景（股票列表、板块行情、条件选股），语义不能改。
// 而多期限性价比评估的历史回测要问的是另一个问题：
//
//	**在 2023-06-30 那天，这只股票属于哪个行业？**
//
// 本机的 industry_members 是**两套体系混存**的：
//
//	eastmoney  日更快照，每个交易日一份，industry_name 是中文名（如"白酒Ⅲ"）
//	sw         申万 StockClassifyUse_stock.xls 回填，2020-01-23 起 82 个月末快照，
//	           **industry_name 就是代码本身**（如 "340501"）
//
// ⚠ 两套体系**不能混在一条时间序列里**：同一只股票在 sw 里是 6 位代码、
// 在 eastmoney 里是 4 位代码，混着取会让行业代码在时间轴上跳变，
// 用它算出来的"行业历史分位"没有任何意义。
//
// 所以这里的做法是：
//  1. **一次性载入内存并建索引**（3.3 万行，成本可忽略）；
//  2. 调用方**显式选定一个体系**（PickIndustrySystem 给出推荐）；
//  3. 按 asOf 在**该体系内**取"截至该日最近一份快照"，并做**前向填充**
//     （快照是月末/日末的离散步长，两次快照之间的交易日必须沿用上一份，
//     否则那些日期会没有行业归属）；
//  4. 选定体系里没有记录的标的，才回落到其它体系补缺 —— 且这只影响
//     "这一只有没有归属"，不会在时间轴上跳体系。
package cnlake

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// industryIndex 是 industry_members 的内存索引。
type industryIndex struct {
	loaded bool
	// asOfDates 是该体系的全部快照日期（升序），前向填充时二分用。
	asOfDates map[string][]int64
	// bySystem 是 classification_system -> 代码 -> 快照日期 -> 行业行。
	bySystem map[string]map[string]map[int64][]IndustryMemberRow
	// latestBySystem 是该体系内每只标的的最新快照（回落补缺用）。
	latestBySystem map[string]map[string]IndustryMemberRow
	// allSymbols 是有任何行业记录的标的集合（判断"这只票根本没有归属"）。
	allSymbols map[string]struct{}
}

// IndustrySnapshot 是一个**时点-体系**上的行业归属。
type IndustrySnapshot struct {
	// AsOf 是评估日（YYYYMMDD）。
	AsOf int64 `json:"asOf"`
	// System 是实际采用的分类体系（sw / eastmoney / ...）。
	System string `json:"system"`
	// Members 是"该日生效"的行业归属（每只标的一条）。
	Members []IndustryMemberRow `json:"members"`
	// ExcludedSystems 列出了**刻意没有采用**的其它体系及原因，供界面说明。
	ExcludedSystems []string `json:"excludedSystems"`
}

// SystemUsage 描述一个分类体系在本湖里的可用情况（供 PickIndustrySystem 决策）。
type SystemUsage struct {
	System string `json:"system"`
	// SnapshotDates 是快照数。
	SnapshotDates int `json:"snapshotDates"`
	// First / Last 是快照区间（YYYYMMDD）。
	First int64 `json:"first"`
	Last  int64 `json:"last"`
	// Symbols 是该体系最新一份快照覆盖的标的数。
	Symbols int `json:"symbols"`
	// HasNames 表示 industry_name 是否给出了可读名称（而不是把代码抄一遍）。
	HasNames bool `json:"hasNames"`
}

// IndustrySystems 返回各分类体系的可用情况（按快照数降序）。
//
// 有了它，调用方（以及界面）才能做出"用哪套体系"的决定，而不是靠猜。
// 实测本机：sw 82 个快照 / 5934 只（无中文名）、eastmoney 5 个快照 / 5574 只（有中文名）。
func (l *Lake) IndustrySystems() []SystemUsage {
	index := l.industryIndex()
	if index == nil {
		return nil
	}
	out := make([]SystemUsage, 0, len(index.bySystem))
	for system := range index.bySystem {
		usage := SystemUsage{System: system}
		usage.SnapshotDates = len(index.asOfDates[system])
		if usage.SnapshotDates > 0 {
			usage.First = index.asOfDates[system][0]
			usage.Last = index.asOfDates[system][usage.SnapshotDates-1]
		}
		latest := index.latestBySystem[system]
		usage.Symbols = len(latest)
		// 名称是否可用：只要有一半以上的名称**不等于**代码（去掉前导零后也不等），
		// 就算这个体系提供了可读名称。
		named := 0
		for _, row := range latest {
			if !sameAsCode(row.IndustryName, row.IndustryCode) {
				named++
			}
		}
		usage.HasNames = len(latest) > 0 && named*2 >= len(latest)
		out = append(out, usage)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SnapshotDates != out[j].SnapshotDates {
			return out[i].SnapshotDates > out[j].SnapshotDates
		}
		return out[i].Symbols > out[j].Symbols
	})
	return out
}

// PickIndustrySystem 推荐一个体系：**历史最长**的那个。
//
// 排序规则（实测本机会选中 sw）：
//  1. 快照数多的优先 —— 直接决定"能不能算历史分位"；
//  2. 快照数相同（或都很少）时，有可读名称的优先 —— 界面体验更好；
//  3. 再相同则按体系名字典序，保证结果稳定可复现。
func (l *Lake) PickIndustrySystem() string {
	usages := l.IndustrySystems()
	if len(usages) == 0 {
		return ""
	}
	best := usages[0]
	for _, usage := range usages[1:] {
		switch {
		case usage.SnapshotDates > best.SnapshotDates:
			best = usage
		case usage.SnapshotDates == best.SnapshotDates && usage.HasNames && !best.HasNames:
			best = usage
		}
	}
	return best.System
}

// IndustryMembersAsOf 返回评估日 asOf 的行业归属（时点正确）。
//
// system 为空时用 PickIndustrySystem 的推荐值。返回的 Members 里每只标的**只有一条**
// 记录（该体系内截至 asOf 最近一份快照）。
//
// 语义要点：
//   - **前向填充**：快照是离散的（申万是月末），asOf 落在两次快照之间时沿用上一份。
//     这不是"凑数"——行业归属本来就是低频变更的事实，月末快照在之后直到下一次
//     调整前都有效。不做前向填充会让区间内绝大多数交易日没有行业归属。
//   - **早于第一份快照的日期返回空**：不拿"今天"的归属去填 2019 年 ——
//     那正是前视偏差。调用方拿到空应降置信度，而不是回退到 latest。
//   - 选定体系里查不到的标的，用**其它体系的最新快照**补一次（只补归属，
//     不在时间轴上跳体系）。
func (l *Lake) IndustryMembersAsOf(asOf int64, system string) IndustrySnapshot {
	out := IndustrySnapshot{AsOf: asOf}
	index := l.industryIndex()
	if index == nil || asOf <= 0 {
		return out
	}
	if system == "" {
		system = l.PickIndustrySystem()
	}
	out.System = system

	dates := index.asOfDates[system]
	effective := effectiveSnapshotDate(dates, asOf)
	if effective > 0 {
		byDate := index.bySystem[system]
		seen := make(map[string]struct{}, 8192)
		// byDate 的键是快照日期；按代码遍历，取该日有记录的行
		for code, bySnapshot := range byDate {
			rows := bySnapshot[effective]
			if len(rows) == 0 {
				continue
			}
			if _, dup := seen[code]; dup {
				continue
			}
			seen[code] = struct{}{}
			out.Members = append(out.Members, rows[0])
		}
	}

	// 补缺：选定体系没有记录的标的，用其它体系的最新快照补。
	// ⚠ 只在"选定体系里查不到"时补，所以不会让同一只标的在时间轴上跳体系。
	if missing := index.missingSymbols(out.Members); len(missing) > 0 {
		filled := 0
		for _, other := range index.systemOrder() {
			if other == system {
				continue
			}
			latest := index.latestBySystem[other]
			for code := range missing {
				row, ok := latest[code]
				if !ok {
					continue
				}
				out.Members = append(out.Members, row)
				delete(missing, code)
				filled++
			}
			if filled > 0 {
				out.ExcludedSystems = append(out.ExcludedSystems,
					other+"（仅用于补 "+itoa(filled)+" 只在选定体系里没有记录的标的）")
				break
			}
		}
	}
	return out
}

// IndustryMembersByDateRange 返回某体系在 [from, to] 内**实际有快照**的日期（升序）。
//
// 用途：调用方要知道"能重建哪些历史时点"，用它取样本日期列表。
func (l *Lake) IndustryMembersByDateRange(system string, from, to int64) []int64 {
	index := l.industryIndex()
	if index == nil {
		return nil
	}
	if system == "" {
		system = l.PickIndustrySystem()
	}
	dates := index.asOfDates[system]
	out := make([]int64, 0, len(dates))
	for _, date := range dates {
		if from > 0 && date < from {
			continue
		}
		if to > 0 && date > to {
			continue
		}
		out = append(out, date)
	}
	return out
}

// ---------------------------------------------------------------------------
// 内部：索引构建与查找
// ---------------------------------------------------------------------------

func (l *Lake) industryIndex() *industryIndex {
	l.mu.Lock()
	if l.industryIdx != nil {
		cached := l.industryIdx
		l.mu.Unlock()
		return cached
	}
	l.mu.Unlock()

	rows, err := readTyped[IndustryMemberRow](l.DatasetFiles("industry_members"), nil)
	if err != nil || len(rows) == 0 {
		return nil
	}

	index := &industryIndex{
		asOfDates:      map[string][]int64{},
		bySystem:       map[string]map[string]map[int64][]IndustryMemberRow{},
		latestBySystem: map[string]map[string]IndustryMemberRow{},
		allSymbols:     map[string]struct{}{},
	}
	dateSeen := map[string]map[int64]struct{}{}
	for _, row := range rows {
		system := strings.TrimSpace(row.ClassificationSystem)
		code := BareSymbol(row.Symbol)
		if system == "" || code == "" || row.AsOfDate == 0 {
			continue
		}
		date := daysToDate(row.AsOfDate)
		if index.bySystem[system] == nil {
			index.bySystem[system] = map[string]map[int64][]IndustryMemberRow{}
		}
		if index.bySystem[system][code] == nil {
			index.bySystem[system][code] = map[int64][]IndustryMemberRow{}
		}
		index.bySystem[system][code][date] = append(index.bySystem[system][code][date], row)

		if dateSeen[system] == nil {
			dateSeen[system] = map[int64]struct{}{}
		}
		if _, ok := dateSeen[system][date]; !ok {
			dateSeen[system][date] = struct{}{}
			index.asOfDates[system] = append(index.asOfDates[system], date)
		}
		if index.latestBySystem[system] == nil {
			index.latestBySystem[system] = map[string]IndustryMemberRow{}
		}
		if old, ok := index.latestBySystem[system][code]; !ok || date > daysToDate(old.AsOfDate) {
			index.latestBySystem[system][code] = row
		}
		index.allSymbols[code] = struct{}{}
	}
	for system := range index.asOfDates {
		sort.Slice(index.asOfDates[system], func(i, j int) bool {
			return index.asOfDates[system][i] < index.asOfDates[system][j]
		})
	}

	l.mu.Lock()
	l.industryIdx = index
	l.mu.Unlock()
	return index
}

// effectiveSnapshotDate 返回 <= asOf 的最后一个快照日期；没有则 0。
func effectiveSnapshotDate(dates []int64, asOf int64) int64 {
	idx := sort.Search(len(dates), func(i int) bool { return dates[i] > asOf })
	if idx == 0 {
		return 0
	}
	return dates[idx-1]
}

func (index *industryIndex) systemOrder() []string {
	out := make([]string, 0, len(index.bySystem))
	for system := range index.bySystem {
		out = append(out, system)
	}
	sort.Strings(out)
	return out
}

// missingSymbols 返回"有行业记录、但不在 given 里"的标的。
func (index *industryIndex) missingSymbols(given []IndustryMemberRow) map[string]struct{} {
	have := make(map[string]struct{}, len(given))
	for _, row := range given {
		have[BareSymbol(row.Symbol)] = struct{}{}
	}
	missing := make(map[string]struct{})
	for code := range index.allSymbols {
		if _, ok := have[code]; !ok {
			missing[code] = struct{}{}
		}
	}
	return missing
}

// sameAsCode 判断名称是不是只是把代码抄了一遍（申万回填就是这样）。
//
// 去前导零再比："001277" 与 "1277" 视为同一个。
func sameAsCode(name, code string) bool {
	trim := func(s string) string { return strings.TrimLeft(strings.TrimSpace(s), "0") }
	a, b := trim(name), trim(code)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := make([]byte, 0, 12)
	for value > 0 {
		buf = append([]byte{byte('0' + value%10)}, buf...)
		value /= 10
	}
	if negative {
		return "-" + string(buf)
	}
	return string(buf)
}

var _ = time.Now
var _ sync.Mutex
