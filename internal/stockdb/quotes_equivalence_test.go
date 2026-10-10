package stockdb

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"
)

// `DecodeQuotes`（快速路径）必须与 `Decode` + `QuoteFromMap`（通用路径）**完全一致**。
//
// 这条路径改的是"怎么解析"，不是"解析出什么"。任何一位不同都会顺着
// 指标 -> 评分 -> 候选池 -> 回测一路变下去，所以这里做逐字段差分：
//
//  1. 合成负载：把 21 个字段用**各种 msgpack 标记**写一遍（fixint/uint8..64/
//     int8..64/float32/float64/bool/nil/bin/fixstr/str8/数组/嵌套 map/缺字段/
//     非 map 条目/空数组/单条 map），逐条比对。
//  2. 真实负载：向本机 stockdb 取真实日k，同一份字节分别走两条路径比对。
func TestDecodeQuotesMatchesGenericDecoder(t *testing.T) {
	payloads := map[string][]byte{
		"单条完整记录": encMap(
			"date", encInt(20261009),
			"code", encStr("600519"),
			"name", encStr("贵州茅台"),
			"open", encFloat(1500.5),
			"high", encFloat(1520.25),
			"low", encFloat(1495),
			"close", encFloat(1510),
			"pre_close", encFloat(1502.5),
			"volume", encInt(18031500),
			"amount", encInt(189010000),
			"turnover", encFloat(1.42),
			"pct_chg", encFloat(-0.67),
			"amplitude", encFloat(2.38),
			"is_st", encBool(false),
			"vol_ratio", encFloat(0.9),
			"total_share", encInt(1268074472),
			"float_share", encInt(1268074472),
			"total_mv", encInt(13251000000),
			"float_mv", encInt(13251000000),
			"pe_ttm", encFloat(22.5),
			"pb", encFloat(1.3),
		),
		"数组两条": encArray(
			encMap("date", encInt(20261009), "code", encStr("000001"), "close", encFloat(10.5), "is_st", encBool(true)),
			encMap("date", encInt(20261008), "code", encStr("000002"), "close", encInt(11), "is_st", encInt(1)),
		),
		"空数组":       encArray(),
		"非法顶层":      encInt(7),
		"缺字段":       encMap("code", encStr("600000")),
		"未知字段":      encMap("code", encStr("600000"), "brand_new", encStr("x"), "nested", encMap("a", encArray(encInt(1), encNil()))),
		"非字符串键":     encMap(int64(5), encStr("ignored"), "code", encStr("600001")),
		"数组含非map":   encArray(encInt(1), encMap("code", encStr("600002")), encStr("junk"), encNil()),
		"各种整数标记":    encMap("volume", encUint(200, 1), "amount", encUint(40000, 2), "total_share", encUint(3000000000, 4), "float_share", encUint(1<<63, 8)),
		"各种负数标记":    encMap("pct_chg", encNegInt(-3, 1), "amplitude", encNegInt(-300, 2), "turnover", encNegInt(-70000, 4), "vol_ratio", encNegInt(-5, 8)),
		"float32标记": encMap("pe_ttm", encFloat32(19.32)),
		"类型不符": encMap(
			"close", encStr("10.5"), // 数值位给了字符串 -> Float() 得 0
			"date", encStr("20261009"), // Int() 得 0
			"code", encInt(600519), // String() 走 fmt.Sprint
			"is_st", encStr("yes"), // Bool() 得 false
			"name", encNil(),
		),
		"bin字符串":    encMap("code", encBin("600003"), "name", encBin("测试")),
		"nil数值字段":   encMap("close", encNil(), "date", encNil(), "is_st", encNil(), "code", encNil()),
		"bool数值字段":  encMap("close", encBool(true), "volume", encBool(false)),
		"map32记录":   encMapHeader(0xdf, 2, "code", encStr("600004"), "close", encFloat(1)),
		"array16列表": encArrayHeader(0xdc, 2, encMap("code", encStr("600005")), encMap("code", encStr("600006"))),
		// fixmap 声明 2 对，但第二对只有键、没有值（负载被截断）
		"截断的负载": {0x82, 0xa4, 'c', 'o', 'd', 'e', 0xa6, '6', '0', '0', '0', '0', '7', 0xa5, 'c', 'l', 'o', 's', 'e'},
	}

	// 键排序保证报错信息稳定
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			payload := payloads[name]

			got, gotErr := DecodeQuotes(payload)
			want, wantErr := decodeQuotesGeneric(payload)

			// 错误场景只要求"两条路径都失败"，不要求错误文本一致
			if (gotErr != nil) != (wantErr != nil) {
				t.Fatalf("错误行为不一致：快速=%v，通用=%v", gotErr, wantErr)
			}
			if gotErr != nil {
				return
			}
			if len(got) != len(want) {
				t.Fatalf("条数不一致：快速=%d，通用=%d", len(got), len(want))
			}
			for i := range want {
				if !reflect.DeepEqual(*got[i], *want[i]) {
					t.Errorf("第 %d 条不一致：\n快速 %+v\n通用 %+v", i, *got[i], *want[i])
				}
			}
		})
	}
}

// decodeQuotesGeneric 是**旧的通用路径**（Decode + ToSlice/ToMap + QuoteFromMap），
// 作为差分测试的参照实现。
func decodeQuotesGeneric(payload []byte) ([]*Quote, error) {
	raw, err := Decode(payload)
	if err != nil {
		return nil, err
	}
	if m, ok := ToMap(raw); ok {
		if len(m) == 0 {
			return nil, nil
		}
		return []*Quote{QuoteFromMap(m)}, nil
	}
	items, _ := ToSlice(raw)
	out := make([]*Quote, 0, len(items))
	for _, item := range items {
		if m, ok := ToMap(item); ok {
			out = append(out, QuoteFromMap(m))
		}
	}
	return out, nil
}

// TestDecodeQuotesRealData 用真实日k负载比对两条路径（21 个字段逐位）。
//
// 本机 stockdb 可用时跑；设 FREESTOCK_QUOTES_PARITY=1 显式开启（会发请求）。
func TestDecodeQuotesRealData(t *testing.T) {
	if os.Getenv("FREESTOCK_QUOTES_PARITY") != "1" {
		t.Skip("要打 stockdb：设 FREESTOCK_QUOTES_PARITY=1 才跑")
	}
	client := NewClient(DefaultLocalURL)
	ctx := context.Background()

	buckets, err := client.StockCodes(ctx)
	if err != nil || len(buckets) == 0 {
		t.Skipf("stockdb 不可用：%v", err)
	}
	all := make([]string, 0, 8192)
	for _, list := range buckets {
		all = append(all, list...)
	}
	sort.Strings(all)
	if len(all) == 0 {
		t.Skip("代码表为空")
	}

	latest, err := client.LatestTradeDate(ctx)
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}

	// 取 20 只、约 400 个自然日（覆盖各种字段缺省的真实情况）
	const codes = 20
	if len(all) > codes {
		all = all[:codes]
	}
	// ⚠ 日期不能直接在 YYYYMMDD 上减天数（20261009-400 = 20260609 是个
	// "看着合法但其实错"的值）—— 这就是本项目反复强调的那个坑。
	parsed, err := time.Parse("20060102", itoa(latest))
	if err != nil {
		t.Skipf("解析交易日失败：%v", err)
	}
	start, err := strconv.Atoi(parsed.AddDate(0, 0, -400).Format("20060102"))
	if err != nil {
		t.Fatalf("格式化起始日失败：%v", err)
	}

	rows, mismatches := 0, 0
	for _, code := range all {
		body, err := client.callRaw(ctx, "vals", TableDailyK, "key:"+code, rangeExpr(start, latest))
		if err != nil {
			t.Fatalf("%s 取数失败：%v", code, err)
		}
		fast, err := DecodeQuotes(body)
		if err != nil {
			t.Fatalf("%s 快速解码失败：%v", code, err)
		}
		slow, err := decodeQuotesGeneric(body)
		if err != nil {
			t.Fatalf("%s 通用解码失败：%v", code, err)
		}
		if len(fast) != len(slow) {
			t.Fatalf("%s 条数不一致：快速 %d，通用 %d", code, len(fast), len(slow))
		}
		for i := range slow {
			rows++
			if !reflect.DeepEqual(*fast[i], *slow[i]) {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("%s 第 %d 条不一致：\n快速 %+v\n通用 %+v", code, i, *fast[i], *slow[i])
				}
			}
		}
	}
	if rows == 0 {
		t.Skip("没有取到可比对的行")
	}
	if mismatches > 0 {
		t.Fatalf("%d 行不一致", mismatches)
	}
	t.Logf("真实数据逐字段一致：%d 只、%d 行（%d ~ %d）", len(all), rows, start, latest)
}

func rangeExpr(start, end int) string {
	return "fwd:" + itoa(start) + "," + itoa(end)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// 测试用 msgpack 编码器（只覆盖差分用例需要的标记）
// ---------------------------------------------------------------------------

func encNil() []byte { return []byte{0xc0} }

func encBool(v bool) []byte {
	if v {
		return []byte{0xc3}
	}
	return []byte{0xc2}
}

func encInt(v int64) []byte {
	switch {
	case v >= 0 && v <= 0x7f:
		return []byte{byte(v)}
	case v >= -32 && v < 0:
		return []byte{byte(int8(v))}
	case v >= math.MinInt8 && v <= math.MaxInt8:
		return []byte{0xd0, byte(int8(v))}
	case v >= math.MinInt16 && v <= math.MaxInt16:
		out := []byte{0xd1, 0, 0}
		binary.BigEndian.PutUint16(out[1:], uint16(int16(v)))
		return out
	case v >= math.MinInt32 && v <= math.MaxInt32:
		out := []byte{0xd2, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(int32(v)))
		return out
	default:
		out := []byte{0xd3, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(out[1:], uint64(v))
		return out
	}
}

// encNegInt 强制用宽度 w 的有符号标记写一个负数（覆盖 int8/16/32/64 四条分支）。
func encNegInt(v int64, w int) []byte {
	switch w {
	case 1:
		return []byte{0xd0, byte(v)}
	case 2:
		out := []byte{0xd1, 0, 0}
		binary.BigEndian.PutUint16(out[1:], uint16(v))
		return out
	case 4:
		out := []byte{0xd2, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(v))
		return out
	default:
		out := []byte{0xd3, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(out[1:], uint64(v))
		return out
	}
}

// encUint 强制用宽度 w 的无符号标记写一个非负数（覆盖 uint8/16/32/64）。
func encUint(v uint64, w int) []byte {
	switch w {
	case 1:
		return []byte{0xcc, byte(v)}
	case 2:
		out := []byte{0xcd, 0, 0}
		binary.BigEndian.PutUint16(out[1:], uint16(v))
		return out
	case 4:
		out := []byte{0xce, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(v))
		return out
	default:
		out := []byte{0xcf, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(out[1:], v)
		return out
	}
}

func encFloat(v float64) []byte {
	out := []byte{0xcb, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(out[1:], math.Float64bits(v))
	return out
}

func encFloat32(v float32) []byte {
	out := []byte{0xca, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], math.Float32bits(v))
	return out
}

func encStr(s string) []byte {
	out := encStrHeader(len(s))
	return append(out, s...)
}

func encStrHeader(n int) []byte {
	switch {
	case n <= 31:
		return []byte{0xa0 | byte(n)}
	case n <= 0xff:
		return []byte{0xd9, byte(n)}
	case n <= 0xffff:
		out := []byte{0xda, 0, 0}
		binary.BigEndian.PutUint16(out[1:], uint16(n))
		return out
	default:
		out := []byte{0xdb, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(n))
		return out
	}
}

func encBin(s string) []byte {
	out := []byte{0xc4, byte(len(s))}
	return append(out, s...)
}

// encMap 写一个记录（键值对按顺序给出，必须是"键, 值, 键, 值..."）。
// 按对数自动选 fixmap / map16 / map32 —— 真实的日k记录有 21 个字段，
// 必然走 map16（fixmap 最多 15 对）。
func encMap(pairs ...any) []byte {
	if len(pairs)%2 != 0 {
		panic("encMap 需要偶数个参数")
	}
	n := len(pairs) / 2
	var header []byte
	switch {
	case n <= 15:
		header = []byte{0x80 | byte(n)}
	case n <= 0xffff:
		header = []byte{0xde, byte(n >> 8), byte(n)}
	default:
		header = []byte{0xdf, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	out := header
	for _, item := range pairs {
		out = append(out, encodeAny(item)...)
	}
	return out
}

// encMapHeader 写一个指定 map 头（map16/map32）的记录。
func encMapHeader(marker byte, n int, pairs ...any) []byte {
	out := []byte{marker}
	switch marker {
	case 0xde:
		out = append(out, byte(n>>8), byte(n))
	case 0xdf:
		out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	default:
		panic("encMapHeader 只支持 map16/map32")
	}
	for _, item := range pairs {
		out = append(out, encodeAny(item)...)
	}
	return out
}

func encArray(items ...[]byte) []byte {
	if len(items) > 15 {
		panic("encArray 只支持 fixarray（<=15 项）")
	}
	out := []byte{0x90 | byte(len(items))}
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func encArrayHeader(marker byte, n int, items ...[]byte) []byte {
	out := []byte{marker}
	switch marker {
	case 0xdc:
		out = append(out, byte(n>>8), byte(n))
	case 0xdd:
		out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	default:
		panic("encArrayHeader 只支持 array16/array32")
	}
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

// encodeAny 让 encMap 能直接接受"已经编码好的字节"或"裸标量"。
func encodeAny(v any) []byte {
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return encStr(t)
	case int:
		return encInt(int64(t))
	case int64:
		return encInt(t)
	case float64:
		return encFloat(t)
	case bool:
		return encBool(t)
	case nil:
		return encNil()
	default:
		panic("encodeAny: 不支持的类型")
	}
}
