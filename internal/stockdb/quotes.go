package stockdb

import "math"

// 日k 记录的**快速解码**：不经过 map[string]any。
//
// 为什么需要单独一条路径（实测，全市场回填取数阶段）：
//
//	通用路径 = Decode(payload) -> []any -> 每条记录一个 21 键的 map
//	           -> 每个值装箱成 any -> QuoteFromMap 再做 21 次字符串哈希查找
//
// 全市场回填要取 7600 只 × 约 330 天 ≈ 250 万条记录，于是这条路径要建
// **250 万个 map**、装箱 **5200 万个 any**、做 **5200 万次** map 查找。
// CPU profile 里 `decoder.decode`（37% 累计）、`mallocgc`（26%）、
// `mapassign/mapaccess_faststr` + `aeshashbody` 合计占了取数阶段的绝大部分。
//
// 这里按"字段名 -> 结构体字段"直接落位，未知字段**按 msgpack 结构走位跳过**，
// 全程只有输出切片与字符串字段（code/name）会分配。
//
// ⚠ **语义必须与 `Decode` + `QuoteFromMap` 完全一致**，包括：
//   - 同样的字段名映射（见 dayKFields，与 QuoteFromMap 的 21 项一一对应）
//   - 缺失字段与类型不符时取同一个默认值（数值 0、字符串 ""、is_st false）
//   - 顶层是单条 map（cmd=get）还是 map 数组（cmd=vals）
//   - 数组里出现非 map 的条目时按旧行为**丢弃**
//
// 有差分测试逐字段比对（含 float32 / uint64 / int8..64 / bin / nil / 嵌套 /
// 缺失字段 / 非 map 条目等标记）—— 见 quotes_equivalence_test.go。

// 日k 字段序号。
const (
	dayKDate = iota
	dayKCode
	dayKName
	dayKOpen
	dayKHigh
	dayKLow
	dayKClose
	dayKPreClose
	dayKVolume
	dayKAmount
	dayKTurnover
	dayKPctChg
	dayKAmplitude
	dayKIsST
	dayKVolRatio
	dayKTotalShare
	dayKFloatShare
	dayKTotalMV
	dayKFloatMV
	dayKPeTTM
	dayKPB
)

// dayKFields 把记录里的键名映射到字段序号。
//
// ⚠ 与 `QuoteFromMap` 的读法**必须一一对应**：那边读 21 个键，这里就要认 21 个。
// 少一个键会让对应字段恒为默认值（数值 0 / 字符串空）—— 静默算错分。
var dayKFields = map[string]int{
	"date":        dayKDate,
	"code":        dayKCode,
	"name":        dayKName,
	"open":        dayKOpen,
	"high":        dayKHigh,
	"low":         dayKLow,
	"close":       dayKClose,
	"pre_close":   dayKPreClose,
	"volume":      dayKVolume,
	"amount":      dayKAmount,
	"turnover":    dayKTurnover,
	"pct_chg":     dayKPctChg,
	"amplitude":   dayKAmplitude,
	"is_st":       dayKIsST,
	"vol_ratio":   dayKVolRatio,
	"total_share": dayKTotalShare,
	"float_share": dayKFloatShare,
	"total_mv":    dayKTotalMV,
	"float_mv":    dayKFloatMV,
	"pe_ttm":      dayKPeTTM,
	"pb":          dayKPB,
}

// DecodeQuotes 把日k记录的 msgpack 负载直接解成 []*Quote。
//
// 接受两种形态（与 HTTP 接口一致）：
//
//	map        —— cmd=get（单条点查询）
//	map 数组    —— cmd=vals（区间查询）
//
// 其它形态（含空数组、未命中返回的 `[]`）返回**空切片且不报错** ——
// 与旧路径 `ToSlice` 失败后返回空切片的行为一致。
func DecodeQuotes(payload []byte) ([]*Quote, error) {
	d := &decoder{buf: payload}
	if len(payload) == 0 {
		return []*Quote{}, nil
	}

	marker := payload[0]
	switch {
	case isFixMapMarker(marker):
		d.pos++
		q, ok, err := d.decodeQuoteFields(int(marker & 0x0f))
		if err != nil || !ok {
			return nil, err
		}
		return []*Quote{q}, nil
	case isFixArrayMarker(marker):
		d.pos++
		return d.decodeQuoteList(int(marker & 0x0f))
	case marker == 0xdc || marker == 0xdd:
		d.pos++
		width := 2
		if marker == 0xdd {
			width = 4
		}
		n, err := d.uintOfWidth(width)
		if err != nil {
			return nil, err
		}
		return d.decodeQuoteList(int(n))
	case marker == 0xde || marker == 0xdf:
		d.pos++
		width := 2
		if marker == 0xdf {
			width = 4
		}
		n, err := d.uintOfWidth(width)
		if err != nil {
			return nil, err
		}
		q, ok, err := d.decodeQuoteFields(int(n))
		if err != nil || !ok {
			return nil, err
		}
		return []*Quote{q}, nil
	}
	// 既不是 map 也不是数组：旧路径会静默返回空切片
	return []*Quote{}, nil
}

// decodeQuoteList 读 n 个条目，只保留是 map 的那些（与旧的 `ToMap` 判定一致）。
func (d *decoder) decodeQuoteList(n int) ([]*Quote, error) {
	if n < 0 {
		return nil, ErrTruncated
	}
	out := make([]*Quote, 0, n)
	for i := 0; i < n; i++ {
		if d.pos >= len(d.buf) {
			return nil, ErrTruncated
		}
		marker := d.buf[d.pos]
		switch {
		case isFixMapMarker(marker):
			d.pos++
			q, ok, err := d.decodeQuoteFields(int(marker & 0x0f))
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, q)
			}
		case marker == 0xde || marker == 0xdf:
			d.pos++
			width := 2
			if marker == 0xdf {
				width = 4
			}
			m, err := d.uintOfWidth(width)
			if err != nil {
				return nil, err
			}
			q, ok, err := d.decodeQuoteFields(int(m))
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, q)
			}
		default:
			// 非 map 条目：旧路径丢弃它，但值必须被消费掉
			if err := d.skipValue(); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// decodeQuoteFields 读一条 map 记录的 n 个键值对。
func (d *decoder) decodeQuoteFields(n int) (*Quote, bool, error) {
	if n < 0 {
		return nil, false, ErrTruncated
	}
	q := &Quote{}
	for i := 0; i < n; i++ {
		key, isString, err := d.takeKey()
		if err != nil {
			return nil, false, err
		}
		if !isString {
			// 非字符串键：旧路径 String(key) 得到的是数字/其它文本，
			// 不会是已知字段名 —— 跳过键与值即可。
			if err := d.skipValue(); err != nil {
				return nil, false, err
			}
			continue
		}
		// m[string(byteSlice)] 由编译器优化成不分配字符串的查找
		field, known := dayKFields[string(key)]
		if !known {
			if err := d.skipValue(); err != nil {
				return nil, false, err
			}
			continue
		}
		if err := d.takeQuoteField(q, field); err != nil {
			return nil, false, err
		}
	}
	return q, true, nil
}

// takeQuoteField 读一个字段的值并落到 q 上，转换语义与 QuoteFromMap 一致。
func (d *decoder) takeQuoteField(q *Quote, field int) error {
	switch field {
	case dayKDate:
		v, err := d.takeIntValue()
		q.Date = v
		return err
	case dayKCode:
		v, err := d.takeStringValue()
		q.Code = v
		return err
	case dayKName:
		v, err := d.takeStringValue()
		q.Name = v
		return err
	case dayKIsST:
		v, err := d.takeBoolValue()
		q.IsST = v
		return err
	}

	v, err := d.takeFloatValue()
	if err != nil {
		return err
	}
	switch field {
	case dayKOpen:
		q.Open = v
	case dayKHigh:
		q.High = v
	case dayKLow:
		q.Low = v
	case dayKClose:
		q.Close = v
	case dayKPreClose:
		q.PreClose = v
	case dayKVolume:
		q.Volume = v
	case dayKAmount:
		q.Amount = v
	case dayKTurnover:
		q.Turnover = v
	case dayKPctChg:
		q.PctChg = v
	case dayKAmplitude:
		q.Amplitude = v
	case dayKVolRatio:
		q.VolRatio = v
	case dayKTotalShare:
		q.TotalShare = v
	case dayKFloatShare:
		q.FloatShare = v
	case dayKTotalMV:
		q.TotalMV = v
	case dayKFloatMV:
		q.FloatMV = v
	case dayKPeTTM:
		q.PeTTM = v
	case dayKPB:
		q.PB = v
	}
	return nil
}

// takeKey 读 map 的键：字符串键返回其原始字节（不分配），其它标记返回 isString=false。
func (d *decoder) takeKey() ([]byte, bool, error) {
	if d.pos >= len(d.buf) {
		return nil, false, ErrTruncated
	}
	marker := d.buf[d.pos]
	switch {
	case marker >= 0xa0 && marker <= 0xbf:
		d.pos++
		raw, err := d.take(int(marker & 0x1f))
		return raw, true, err
	case marker == 0xd9 || marker == 0xda || marker == 0xdb:
		d.pos++
		width := map[byte]int{0xd9: 1, 0xda: 2, 0xdb: 4}[marker]
		n, err := d.uintOfWidth(width)
		if err != nil {
			return nil, false, err
		}
		raw, err := d.take(int(n))
		return raw, true, err
	}
	// 非字符串键（不合 schema，但旧路径不会崩）：把键本身走位掉
	if err := d.skipValue(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// takeFloatValue 按 `Float()` 的语义读一个数值：整数/浮点/bool 取值，其余为 0。
func (d *decoder) takeFloatValue() (float64, error) {
	if d.pos >= len(d.buf) {
		return 0, ErrTruncated
	}
	marker := d.buf[d.pos]
	switch {
	case marker <= 0x7f: // positive fixint
		d.pos++
		return float64(marker), nil
	case marker >= 0xe0: // negative fixint
		d.pos++
		return float64(int8(marker)), nil
	}
	switch marker {
	case 0xca:
		d.pos++
		raw, err := d.uintOfWidth(4)
		if err != nil {
			return 0, err
		}
		return float64(math.Float32frombits(uint32(raw))), nil
	case 0xcb:
		d.pos++
		raw, err := d.uintOfWidth(8)
		if err != nil {
			return 0, err
		}
		return math.Float64frombits(raw), nil
	case 0xcc, 0xcd, 0xce, 0xcf:
		width := 1 << (marker - 0xcc)
		d.pos++
		raw, err := d.uintOfWidth(width)
		if err != nil {
			return 0, err
		}
		return float64(raw), nil
	case 0xd0:
		d.pos++
		raw, err := d.uintOfWidth(1)
		if err != nil {
			return 0, err
		}
		return float64(int8(uint8(raw))), nil
	case 0xd1:
		d.pos++
		raw, err := d.uintOfWidth(2)
		if err != nil {
			return 0, err
		}
		return float64(int16(uint16(raw))), nil
	case 0xd2:
		d.pos++
		raw, err := d.uintOfWidth(4)
		if err != nil {
			return 0, err
		}
		return float64(int32(uint32(raw))), nil
	case 0xd3:
		d.pos++
		raw, err := d.uintOfWidth(8)
		if err != nil {
			return 0, err
		}
		return float64(int64(raw)), nil
	case 0xc2: // false -> Float(bool) = 0
		d.pos++
		return 0, nil
	case 0xc3: // true -> Float(bool) = 1
		d.pos++
		return 1, nil
	}
	// 字符串/nil/数组等：Float() 返回 0，但整个值必须走位
	return 0, d.skipValue()
}

// takeIntValue 按 `Int()` 的语义读一个整数：整数取值、浮点截断，其余为 0。
func (d *decoder) takeIntValue() (int64, error) {
	if d.pos >= len(d.buf) {
		return 0, ErrTruncated
	}
	marker := d.buf[d.pos]
	switch {
	case marker <= 0x7f:
		d.pos++
		return int64(marker), nil
	case marker >= 0xe0:
		d.pos++
		return int64(int8(marker)), nil
	}
	switch marker {
	case 0xca:
		d.pos++
		raw, err := d.uintOfWidth(4)
		if err != nil {
			return 0, err
		}
		return int64(math.Float32frombits(uint32(raw))), nil
	case 0xcb:
		d.pos++
		raw, err := d.uintOfWidth(8)
		if err != nil {
			return 0, err
		}
		return int64(math.Float64frombits(raw)), nil
	case 0xcc, 0xcd, 0xce, 0xcf:
		width := 1 << (marker - 0xcc)
		d.pos++
		raw, err := d.uintOfWidth(width)
		if err != nil {
			return 0, err
		}
		return int64(raw), nil
	case 0xd0:
		d.pos++
		raw, err := d.uintOfWidth(1)
		if err != nil {
			return 0, err
		}
		return int64(int8(uint8(raw))), nil
	case 0xd1:
		d.pos++
		raw, err := d.uintOfWidth(2)
		if err != nil {
			return 0, err
		}
		return int64(int16(uint16(raw))), nil
	case 0xd2:
		d.pos++
		raw, err := d.uintOfWidth(4)
		if err != nil {
			return 0, err
		}
		return int64(int32(uint32(raw))), nil
	case 0xd3:
		d.pos++
		raw, err := d.uintOfWidth(8)
		if err != nil {
			return 0, err
		}
		return int64(raw), nil
	}
	return 0, d.skipValue()
}

// takeBoolValue 按 `Bool()` 的语义读一个布尔：bool 本身、整数/浮点非 0 为真。
func (d *decoder) takeBoolValue() (bool, error) {
	if d.pos >= len(d.buf) {
		return false, ErrTruncated
	}
	marker := d.buf[d.pos]
	switch {
	case marker == 0xc2:
		d.pos++
		return false, nil
	case marker == 0xc3:
		d.pos++
		return true, nil
	case marker <= 0x7f:
		d.pos++
		return marker != 0, nil
	case marker >= 0xe0:
		d.pos++
		return int8(marker) != 0, nil
	}
	switch marker {
	case 0xca:
		d.pos++
		raw, err := d.uintOfWidth(4)
		if err != nil {
			return false, err
		}
		return math.Float32frombits(uint32(raw)) != 0, nil
	case 0xcb:
		d.pos++
		raw, err := d.uintOfWidth(8)
		if err != nil {
			return false, err
		}
		return math.Float64frombits(raw) != 0, nil
	case 0xcc, 0xcd, 0xce, 0xcf:
		width := 1 << (marker - 0xcc)
		d.pos++
		raw, err := d.uintOfWidth(width)
		if err != nil {
			return false, err
		}
		return raw != 0, nil
	case 0xd0, 0xd1, 0xd2, 0xd3:
		width := 1 << (marker - 0xd0)
		d.pos++
		raw, err := d.uintOfWidth(width)
		if err != nil {
			return false, err
		}
		return raw != 0, nil
	}
	return false, d.skipValue()
}

// takeStringValue 按 `String()` 的语义读一个字符串。
//
// 只有字符串与 bin 会走到快路径；nil 返回空串；其它类型退回通用解码
// （`String()` 对数字用 fmt.Sprint —— 日k 的 code/name 不会是数字，但语义要对齐）。
func (d *decoder) takeStringValue() (string, error) {
	if d.pos >= len(d.buf) {
		return "", ErrTruncated
	}
	marker := d.buf[d.pos]
	switch {
	case marker >= 0xa0 && marker <= 0xbf:
		d.pos++
		raw, err := d.take(int(marker & 0x1f))
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case marker == 0xd9 || marker == 0xda || marker == 0xdb:
		d.pos++
		width := map[byte]int{0xd9: 1, 0xda: 2, 0xdb: 4}[marker]
		n, err := d.uintOfWidth(width)
		if err != nil {
			return "", err
		}
		raw, err := d.take(int(n))
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case marker == 0xc4 || marker == 0xc5 || marker == 0xc6:
		d.pos++
		n, err := d.uintOfWidth(1 << (marker - 0xc4))
		if err != nil {
			return "", err
		}
		raw, err := d.take(int(n))
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case marker == 0xc0:
		d.pos++
		return "", nil
	}
	value, err := d.decode()
	if err != nil {
		return "", err
	}
	return String(value), nil
}

// skipValue 按 msgpack 结构走位，不构造任何值。
func (d *decoder) skipValue() error {
	marker, err := d.marker()
	if err != nil {
		return err
	}
	switch {
	case marker <= 0x7f || marker >= 0xe0:
		return nil
	case marker >= 0xa0 && marker <= 0xbf:
		_, err = d.take(int(marker & 0x1f))
		return err
	case marker >= 0x90 && marker <= 0x9f:
		return d.skipItems(int(marker & 0x0f))
	case marker >= 0x80 && marker <= 0x8f:
		return d.skipItems(2 * int(marker&0x0f))
	}

	switch marker {
	case 0xc0, 0xc2, 0xc3:
		return nil
	case 0xc4, 0xc5, 0xc6:
		n, err := d.uintOfWidth(1 << (marker - 0xc4))
		if err != nil {
			return err
		}
		_, err = d.take(int(n))
		return err
	case 0xc7, 0xc8, 0xc9:
		width := map[byte]int{0xc7: 1, 0xc8: 2, 0xc9: 4}[marker]
		n, err := d.uintOfWidth(width)
		if err != nil {
			return err
		}
		_, err = d.take(1 + int(n))
		return err
	case 0xca:
		_, err := d.take(4)
		return err
	case 0xcb:
		_, err := d.take(8)
		return err
	case 0xcc, 0xd0:
		_, err := d.take(1)
		return err
	case 0xcd, 0xd1:
		_, err := d.take(2)
		return err
	case 0xce, 0xd2:
		_, err := d.take(4)
		return err
	case 0xcf, 0xd3:
		_, err := d.take(8)
		return err
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8:
		_, err := d.take(1 + 1<<(marker-0xd4))
		return err
	case 0xd9, 0xda, 0xdb:
		width := map[byte]int{0xd9: 1, 0xda: 2, 0xdb: 4}[marker]
		n, err := d.uintOfWidth(width)
		if err != nil {
			return err
		}
		_, err = d.take(int(n))
		return err
	case 0xdc, 0xdd:
		width := 2
		if marker == 0xdd {
			width = 4
		}
		n, err := d.uintOfWidth(width)
		if err != nil {
			return err
		}
		return d.skipItems(int(n))
	case 0xde, 0xdf:
		width := 2
		if marker == 0xdf {
			width = 4
		}
		n, err := d.uintOfWidth(width)
		if err != nil {
			return err
		}
		return d.skipItems(2 * int(n))
	}
	return ErrTruncated
}

// skipItems 连续走位 n 个值。
func (d *decoder) skipItems(n int) error {
	for i := 0; i < n; i++ {
		if err := d.skipValue(); err != nil {
			return err
		}
	}
	return nil
}

func isFixMapMarker(marker byte) bool   { return marker >= 0x80 && marker <= 0x8f }
func isFixArrayMarker(marker byte) bool { return marker >= 0x90 && marker <= 0x9f }
