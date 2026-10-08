// Package market 依据 6 位证券代码判定其所属市场板块。
package market

import "strings"

// Definition 描述一个市场板块。
type Definition struct {
	Key   string // 稳定标识，前端筛选使用
	Name  string // 展示名称
	Group string // 归类：股票 / 基金
	Order int    // 展示顺序
}

// 市场定义（顺序即前端展示顺序）。
const (
	KeySHMain   = "sh-main"
	KeySTAR     = "star"
	KeySZMain   = "sz-main"
	KeyChiNext  = "chinext"
	KeyBSE      = "bse"
	KeySHFund   = "sh-fund"
	KeySZFund   = "sz-fund"
	KeyUnknown  = "unknown"
	GroupStock  = "股票"
	GroupFund   = "基金"
)

// Definitions 是所有已知市场，按展示顺序排列。
var Definitions = []Definition{
	{Key: KeySHMain, Name: "沪市主板", Group: GroupStock, Order: 1},
	{Key: KeySTAR, Name: "科创板", Group: GroupStock, Order: 2},
	{Key: KeySZMain, Name: "深市主板", Group: GroupStock, Order: 3},
	{Key: KeyChiNext, Name: "创业板", Group: GroupStock, Order: 4},
	{Key: KeyBSE, Name: "北交所", Group: GroupStock, Order: 5},
	{Key: KeySHFund, Name: "沪市基金", Group: GroupFund, Order: 6},
	{Key: KeySZFund, Name: "深市基金", Group: GroupFund, Order: 7},
	{Key: KeyUnknown, Name: "其他", Group: GroupStock, Order: 99},
}

var byKey = func() map[string]Definition {
	m := make(map[string]Definition, len(Definitions))
	for _, d := range Definitions {
		m[d.Key] = d
	}
	return m
}()

// Lookup 按 key 取市场定义。
func Lookup(key string) (Definition, bool) {
	d, ok := byKey[key]
	return d, ok
}

// NameOf 返回市场展示名称，未知时返回 key 本身。
func NameOf(key string) string {
	if d, ok := byKey[key]; ok {
		return d.Name
	}
	return key
}

// KeyOf 依据证券代码判定市场。
//
// 已知代码分布（free-stockdb 股票代码表）：
//
//	6 -> 600/601/603/605 沪市主板，688/689 科创板
//	0 -> 000/001/002/003 深市主板
//	3 -> 300/301/302 创业板
//	9 -> 920 北交所
//	1 -> 159/16x/18x 深市基金
//	5 -> 5xx 沪市基金
func KeyOf(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return KeyUnknown
	}
	// 去掉可能存在的 .SH / .SZ 后缀
	if idx := strings.IndexAny(code, "."); idx > 0 {
		code = code[:idx]
	}
	if len(code) < 3 {
		return KeyUnknown
	}

	switch code[:3] {
	case "600", "601", "603", "605":
		return KeySHMain
	case "688", "689":
		return KeySTAR
	case "000", "001", "002", "003":
		return KeySZMain
	case "300", "301", "302":
		return KeyChiNext
	case "920":
		return KeyBSE
	case "159", "150", "151", "152", "153", "154", "155", "156", "157", "158",
		"160", "161", "162", "163", "164", "165", "166", "167", "168", "169",
		"180", "181", "182", "183", "184", "185", "186", "187", "188", "189":
		return KeySZFund
	}

	switch code[0] {
	case '4', '8': // 北交所存量代码（430/83x/87x）
		return KeyBSE
	case '5':
		return KeySHFund
	case '1':
		return KeySZFund
	case '6':
		return KeySHMain
	case '0':
		return KeySZMain
	case '3':
		return KeyChiNext
	case '9':
		return KeyBSE
	}
	return KeyUnknown
}

// IsFund 判断代码是否属于基金类市场。
func IsFund(code string) bool {
	key := KeyOf(code)
	return key == KeySHFund || key == KeySZFund
}
