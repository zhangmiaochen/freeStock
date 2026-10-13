package cnlake

import (
	"sort"
	"sync"
)

// ---------------------------------------------------------------------------
// 公司行为（curated/corporate_actions）
//
// 复权解决了**指标**的正确性（价格序列连续），但**持仓账本**还需要单独处理公司行为：
//
//	送股/转增/拆股 → 持股数量变化（成本价相应摊薄）
//	现金分红       → 现金入账（除息日价格已经掉了，钱要用红利补回来）
//
// 只复权价格、不复权股数，会让跨越除权日的交易在"名义金额"上算错
// （收益率仍然正确，因为复权序列本身是总收益序列）。
//
// 数据字段（实测）：
//
//	symbol / ex_date / action_type ∈ {cash_dividend, bonus, transfer, unit_split, allotment}
//	cash_dividend 每股现金分红（税前，如 0.733）
//	bonus_ratio   每股送股（10 送 3 → 0.3）
//	transfer_ratio 每股转增
//	split_factor  拆股比例（unit_split；非拆股为 1.0）
//	allotment_ratio / allotment_price 配股（本策略不参与配股，仅记录）
//
// ⚠ `cash_dividend` 等字段的**口径以数据湖为准**；本文件只做"每股 → 持仓"的换算，
// 换算规则集中在 ShareMultiplier / CashPerShare 两个纯函数里，便于核对与替换。
// ---------------------------------------------------------------------------

// CorporateAction 是一次除权除息事件（每股口径）。
type CorporateAction struct {
	Symbol        string  `parquet:"symbol"`
	ExDateRaw     int32   `parquet:"ex_date,date"`
	ActionType    string  `parquet:"action_type"`
	CashDividend  float64 `parquet:"cash_dividend"`
	BonusRatio    float64 `parquet:"bonus_ratio"`
	TransferRatio float64 `parquet:"transfer_ratio"`
	SplitFactor   float64 `parquet:"split_factor"`
	AllotRatio    float64 `parquet:"allotment_ratio"`
	AllotPrice    float64 `parquet:"allotment_price"`
}

// ExDate 返回 YYYYMMDD。
func (a CorporateAction) ExDate() int64 { return daysToDate(a.ExDateRaw) }

// ShareMultiplier 返回除权日之后**持股数量**的乘数。
//
//	送股 + 转增：1 + bonus + transfer（10 送 3 转 2 → 1.5）
//	拆股：再乘 split_factor（缺失或 ≤0 时按 1 处理）
func (a CorporateAction) ShareMultiplier() float64 {
	mul := 1 + maxFloat(a.BonusRatio, 0) + maxFloat(a.TransferRatio, 0)
	if a.SplitFactor > 1 {
		mul *= a.SplitFactor
	}
	if mul <= 0 {
		return 1
	}
	return mul
}

// CashPerShare 返回每股现金分红（税前；负值按 0 处理）。
func (a CorporateAction) CashPerShare() float64 { return maxFloat(a.CashDividend, 0) }

func maxFloat(v, floor float64) float64 {
	if v > floor {
		return v
	}
	return floor
}

// corporateActionCache 惰性载入整个除权除息表（约 1.6 万行，一次读完即可）。
type corporateActionCache struct {
	mu     sync.Mutex
	loaded bool
	byCode map[string][]CorporateAction // 6 位代码 -> 按除权日升序
	byDate map[int64][]CorporateAction  // YYYYMMDD -> 当日事件
	all    []CorporateAction
}

// CorporateActions 返回某只股票的全部公司行为（按除权日升序）；没有则返回 nil。
func (l *Lake) CorporateActions(code string) []CorporateAction {
	c := l.corporateActions()
	if c == nil {
		return nil
	}
	return c.byCode[BareSymbol(code)]
}

// CorporateActionsOn 返回某个除权日的全部公司行为。
func (l *Lake) CorporateActionsOn(date int64) []CorporateAction {
	c := l.corporateActions()
	if c == nil {
		return nil
	}
	return c.byDate[date]
}

// ActionsInRange 返回某只股票在 [from, to]（YYYYMMDD，含端点）内的公司行为。
func (l *Lake) ActionsInRange(code string, from, to int64) []CorporateAction {
	list := l.CorporateActions(code)
	if len(list) == 0 {
		return nil
	}
	lo := sort.Search(len(list), func(i int) bool { return list[i].ExDate() >= from })
	hi := sort.Search(len(list), func(i int) bool { return list[i].ExDate() > to })
	if lo >= hi {
		return nil
	}
	return list[lo:hi]
}

// ActionsByDate 返回 [from, to]（YYYYMMDD，含端点）内、按除权日索引的全部公司行为。
//
// 一次性遍历已载入的表，适合回测前批量取数（避免逐只股票调用）。
func (l *Lake) ActionsByDate(from, to int64) map[int64][]CorporateAction {
	out := map[int64][]CorporateAction{}
	c := l.corporateActions()
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.all {
		d := a.ExDate()
		if d >= from && d <= to {
			out[d] = append(out[d], a)
		}
	}
	return out
}

// ApplyCorporateActions 按公司行为调整持仓：返回新的股数、应入账的现金。
//
//	shares' = shares × Π ShareMultiplier（区间内所有送转/拆股）
//	cash    = shares × Σ CashPerShare（区间内所有现金分红；**按除权前股数**计）
//
// 成本价由调用方自行摊薄（cost' = cost × shares / shares'，再减去分红部分视口径而定）。
func ApplyCorporateActions(shares float64, actions []CorporateAction) (newShares, cashDividend float64) {
	newShares = shares
	cashDividend = 0
	for _, a := range actions {
		cashDividend += newShares * a.CashPerShare()
		newShares *= a.ShareMultiplier()
	}
	return newShares, cashDividend
}

func (l *Lake) corporateActions() *corporateActionCache {
	l.adjMu.Lock()
	if l.corp == nil {
		l.corp = &corporateActionCache{}
	}
	c := l.corp
	l.adjMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c
	}
	c.loaded = true
	c.byCode = map[string][]CorporateAction{}
	c.byDate = map[int64][]CorporateAction{}

	if !l.HasDataset("corporate_actions") {
		return c
	}
	rows, err := readTyped[CorporateAction](l.DatasetFiles("corporate_actions"), nil)
	if err != nil {
		return c
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Symbol != rows[j].Symbol {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].ExDateRaw < rows[j].ExDateRaw
	})
	c.all = rows
	for _, a := range rows {
		code := BareSymbol(a.Symbol)
		if code == "" || a.ExDate() <= 0 {
			continue
		}
		c.byCode[code] = append(c.byCode[code], a)
		d := a.ExDate()
		c.byDate[d] = append(c.byDate[d], a)
	}
	return c
}

// HasCorporateActions 判断数据湖是否有公司行为数据集。
func (l *Lake) HasCorporateActions() bool {
	return l != nil && l.HasDataset("corporate_actions")
}
