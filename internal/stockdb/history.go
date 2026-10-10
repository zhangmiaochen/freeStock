package stockdb

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// TableAdjust 是复权因子表，同时记录了每次分红送转。
const TableAdjust = "复权"

// Factor 是一次除权（分红/送转）事件。
//
// 实测表内容形如：
//
//	["复权:600633:19930705", {"div":0,"give":0.1,"trans":0,"mult":1.1,"cum":1.1}]
//
// div 为每股派息（税前），give 为每股送股，trans 为每股转增，
// mult 为本次复权乘数，cum 为累计复权因子（用于计算前/后复权价格）。
type Factor struct {
	Date  int64   `json:"date"`
	Div   float64 `json:"div"`
	Give  float64 `json:"give"`
	Trans float64 `json:"trans"`
	Mult  float64 `json:"mult"`
	Cum   float64 `json:"cum"`
}

// FactorFromMap 由解码结果构造 Factor。
func FactorFromMap(date int64, m map[string]any) Factor {
	return Factor{
		Date:  date,
		Div:   Float(m["div"]),
		Give:  Float(m["give"]),
		Trans: Float(m["trans"]),
		Mult:  Float(m["mult"]),
		Cum:   Float(m["cum"]),
	}
}

// DividendFactors 读取某只股票的全部除权记录，按日期升序返回。
func (c *Client) DividendFactors(ctx context.Context, code string) ([]Factor, error) {
	raw, err := c.Get(ctx, TableAdjust, "key:"+code, "all:")
	if err != nil {
		return nil, err
	}

	items, _ := ToSlice(raw)
	factors := make([]Factor, 0, len(items))
	for _, item := range items {
		pair, ok := ToSlice(item)
		if !ok || len(pair) < 2 {
			continue
		}
		payload, ok := ToMap(pair[1])
		if !ok {
			continue
		}
		// 键名形如 复权:600633:19930705，最后一段是除权日
		key := String(pair[0])
		idx := strings.LastIndex(key, ":")
		if idx < 0 {
			continue
		}
		date, convErr := strconv.ParseInt(key[idx+1:], 10, 64)
		if convErr != nil {
			continue
		}
		factors = append(factors, FactorFromMap(date, payload))
	}

	sort.Slice(factors, func(i, j int) bool { return factors[i].Date < factors[j].Date })
	return factors, nil
}

// DailyHistory 读取某只股票 [start, end] 区间内的全部日 K，按日期升序返回。
//
// 注意：服务端的 fwd: 区间查询返回的是**倒序**（最新在前），这里统一翻正。
func (c *Client) DailyHistory(ctx context.Context, code string, start, end int) ([]*Quote, error) {
	if end <= 0 {
		return nil, fmt.Errorf("stockdb: 缺少结束日期")
	}
	if start <= 0 || start > end {
		start = 19900101
	}

	body, err := c.callRaw(ctx, "vals", TableDailyK, "key:"+code, fmt.Sprintf("fwd:%d,%d", start, end))
	if err != nil {
		return nil, err
	}
	quotes, err := DecodeQuotes(body)
	if err != nil {
		return nil, err
	}

	// 倒序 -> 升序
	for i, j := 0, len(quotes)-1; i < j; i, j = i+1, j-1 {
		quotes[i], quotes[j] = quotes[j], quotes[i]
	}
	return quotes, nil
}
