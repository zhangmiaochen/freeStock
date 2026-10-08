package stockdb

import "strings"

// Quote 是一条日 K（或分钟 K）记录中被本应用使用的字段。
type Quote struct {
	Date       int64   `json:"date"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	Close      float64 `json:"close"`
	PreClose   float64 `json:"preClose"`
	Volume     float64 `json:"volume"`
	Amount     float64 `json:"amount"`
	Turnover   float64 `json:"turnover"`
	PctChg     float64 `json:"pctChg"`
	Amplitude  float64 `json:"amplitude"`
	IsST       bool    `json:"isST"`
	VolRatio   float64 `json:"volRatio"`
	TotalShare float64 `json:"totalShare"`
	FloatShare float64 `json:"floatShare"`
	TotalMV    float64 `json:"totalMV"`
	FloatMV    float64 `json:"floatMV"`
	PeTTM      float64 `json:"peTTM"`
	PB         float64 `json:"pb"`
}

// QuoteFromMap 将解码后的记录映射为 Quote。
func QuoteFromMap(m map[string]any) *Quote {
	return &Quote{
		Date:       Int(m["date"]),
		Code:       String(m["code"]),
		Name:       String(m["name"]),
		Open:       Float(m["open"]),
		High:       Float(m["high"]),
		Low:        Float(m["low"]),
		Close:      Float(m["close"]),
		PreClose:   Float(m["pre_close"]),
		Volume:     Float(m["volume"]),
		Amount:     Float(m["amount"]),
		Turnover:   Float(m["turnover"]),
		PctChg:     Float(m["pct_chg"]),
		Amplitude:  Float(m["amplitude"]),
		IsST:       Bool(m["is_st"]),
		VolRatio:   Float(m["vol_ratio"]),
		TotalShare: Float(m["total_share"]),
		FloatShare: Float(m["float_share"]),
		TotalMV:    Float(m["total_mv"]),
		FloatMV:    Float(m["float_mv"]),
		PeTTM:      Float(m["pe_ttm"]),
		PB:         Float(m["pb"]),
	}
}

// Board 是一个行业/概念板块。
type Board struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	Type     string   `json:"type"`
	Group    string   `json:"group"`
	Category string   `json:"category"`
	Symbols  []string `json:"symbols"`
}

// BoardFromMap 将解码后的板块记录映射为 Board。
func BoardFromMap(m map[string]any) Board {
	b := Board{
		Code:     String(m["code"]),
		Name:     String(m["name"]),
		Source:   String(m["source"]),
		Type:     String(m["type"]),
		Group:    String(m["group"]),
		Category: String(m["category"]),
	}
	if items, ok := ToSlice(m["symbols"]); ok {
		b.Symbols = make([]string, 0, len(items))
		for _, item := range items {
			if code := strings.TrimSpace(String(item)); code != "" {
				b.Symbols = append(b.Symbols, code)
			}
		}
	}
	return b
}
