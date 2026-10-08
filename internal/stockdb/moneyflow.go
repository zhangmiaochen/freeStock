package stockdb

import (
	"context"
	"fmt"
)

// TableMoneyFlow 是资金流表（本机数据集里已有，无需额外接入）。
const TableMoneyFlow = "资金流"

// MoneyFlow 是某一天的个股资金流。
//
// 实测字段：
//
//	{"main_net":-256221021.53,"jumbo_net":36903541.36,"big_net":-293124562.89,
//	 "mid_net":266471718.22,"small_net":-10250696.69,
//	 "main_in":2037367485.4,"main_out":2293588506.93,
//	 "retail_in":1347678373.06,"retail_out":1091457351.53}
type MoneyFlow struct {
	Date      int64   `json:"date"`
	MainNet   float64 `json:"mainNet"`   // 主力净流入（超大单+大单）
	JumboNet  float64 `json:"jumboNet"`  // 超大单净流入
	BigNet    float64 `json:"bigNet"`    // 大单净流入
	MidNet    float64 `json:"midNet"`    // 中单净流入
	SmallNet  float64 `json:"smallNet"`  // 小单净流入
	MainIn    float64 `json:"mainIn"`    // 主力流入
	MainOut   float64 `json:"mainOut"`   // 主力流出
	RetailIn  float64 `json:"retailIn"`  // 散户流入
	RetailOut float64 `json:"retailOut"` // 散户流出
}

func moneyFlowFromMap(date int64, m map[string]any) MoneyFlow {
	return MoneyFlow{
		Date:      date,
		MainNet:   Float(m["main_net"]),
		JumboNet:  Float(m["jumbo_net"]),
		BigNet:    Float(m["big_net"]),
		MidNet:    Float(m["mid_net"]),
		SmallNet:  Float(m["small_net"]),
		MainIn:    Float(m["main_in"]),
		MainOut:   Float(m["main_out"]),
		RetailIn:  Float(m["retail_in"]),
		RetailOut: Float(m["retail_out"]),
	}
}

// MoneyFlowRange 读取 [start, end] 区间内的资金流，按日期升序返回。
// 数据缺失（次新股、退市股）时返回空列表而不是错误。
//
// 注意：这里用 cmd=vals，返回的是**纯值字典数组**（cmd=get 才是 [键, 值] 键值对），
// 且服务端返回顺序为倒序（最新在前），需要翻正。
func (c *Client) MoneyFlowRange(ctx context.Context, code string, start, end int) ([]MoneyFlow, error) {
	if end <= 0 {
		return nil, fmt.Errorf("stockdb: 缺少结束日期")
	}
	if start <= 0 || start > end {
		start = 19900101
	}

	raw, err := c.Vals(ctx, TableMoneyFlow, "key:"+code, fmt.Sprintf("fwd:%d,%d", start, end))
	if err != nil {
		return nil, err
	}

	items, _ := ToSlice(raw)
	out := make([]MoneyFlow, 0, len(items))
	for _, item := range items {
		payload, ok := ToMap(item)
		if !ok {
			continue
		}
		date := Int(payload["date"])
		if date == 0 {
			continue
		}
		out = append(out, moneyFlowFromMap(date, payload))
	}

	// 倒序 -> 升序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
