// Package stockdb 提供 free-stockdb 本地/远程 HTTP 接口的 Go 客户端。
//
// free-stockdb 的 HTTP 接口响应体是 MessagePack（Content-Type: application/x-msgpack），
// 因此本包自带一个无第三方依赖的 MessagePack 解码器。
//
// 已确认的接口约定：
//
//	GET /?cmd=get|vals|keys&t=<表名>&k1=<一级键>&k2=<二级键>
//
// 键表达式：
//
//	all:                 全部
//	key:600633           指定一级键
//	fwd:20260901,20260930 区间查询（返回顺序为倒序，最新的在前）
//	*                    通配（注意：k1 使用 * 时 k2 过滤会被忽略，可能返回数百 MB，勿用）
//
// 表结构：
//
//	股票代码              map[首位数字][]6位代码
//	板块*                 [[]any{key, {code,name,source,type,group,category,symbols}}]
//	日k:代码:日期          {date,code,name,open,high,low,close,pre_close,volume,amount,...}
package stockdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ErrTruncated 表示 MessagePack 数据在中途被截断。
var ErrTruncated = errors.New("stockdb: msgpack 数据不完整")

// Decode 将 MessagePack 负载解析为 Go 值：
// map -> map[string]any，array -> []any，str -> string，
// 整数 -> int64（超出 int64 时保留 uint64），浮点 -> float64，另有 bool / nil。
func Decode(payload []byte) (any, error) {
	d := &decoder{buf: payload}
	return d.decode()
}

type decoder struct {
	buf []byte
	pos int
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.buf) {
		return nil, ErrTruncated
	}
	out := d.buf[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

func (d *decoder) marker() (byte, error) {
	raw, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return raw[0], nil
}

func (d *decoder) uintOfWidth(n int) (uint64, error) {
	raw, err := d.take(n)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return uint64(raw[0]), nil
	case 2:
		return uint64(binary.BigEndian.Uint16(raw)), nil
	case 4:
		return uint64(binary.BigEndian.Uint32(raw)), nil
	case 8:
		return binary.BigEndian.Uint64(raw), nil
	default:
		return 0, fmt.Errorf("stockdb: 不支持 %d 字节整数", n)
	}
}

func (d *decoder) decode() (any, error) {
	m, err := d.marker()
	if err != nil {
		return nil, err
	}

	switch {
	case m <= 0x7f: // positive fixint
		return int64(m), nil
	case m >= 0xe0: // negative fixint
		return int64(int8(m)), nil
	case m >= 0x80 && m <= 0x8f: // fixmap
		return d.decodeMap(int(m & 0x0f))
	case m >= 0x90 && m <= 0x9f: // fixarray
		return d.decodeArray(int(m & 0x0f))
	case m >= 0xa0 && m <= 0xbf: // fixstr
		return d.decodeString(int(m & 0x1f))
	}

	switch m {
	case 0xc0:
		return nil, nil
	case 0xc1:
		return nil, fmt.Errorf("stockdb: 非法的 msgpack 标记 0xc1")
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xc4, 0xc5, 0xc6: // bin 8/16/32
		n, err := d.uintOfWidth(1 << (m - 0xc4))
		if err != nil {
			return nil, err
		}
		raw, err := d.take(int(n))
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), raw...), nil
	case 0xca: // float 32
		v, err := d.uintOfWidth(4)
		if err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(uint32(v))), nil
	case 0xcb: // float 64
		v, err := d.uintOfWidth(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(v), nil
	case 0xcc, 0xcd, 0xce, 0xcf: // uint 8/16/32/64
		v, err := d.uintOfWidth(1 << (m - 0xcc))
		if err != nil {
			return nil, err
		}
		if v <= math.MaxInt64 {
			return int64(v), nil
		}
		return v, nil
	case 0xd0: // int 8
		v, err := d.uintOfWidth(1)
		return int64(int8(uint8(v))), err
	case 0xd1: // int 16
		v, err := d.uintOfWidth(2)
		return int64(int16(uint16(v))), err
	case 0xd2: // int 32
		v, err := d.uintOfWidth(4)
		return int64(int32(uint32(v))), err
	case 0xd3: // int 64
		v, err := d.uintOfWidth(8)
		return int64(v), err
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8: // fixext 1/2/4/8/16
		size := 1 << (m - 0xd4)
		if _, err := d.take(1 + size); err != nil {
			return nil, err
		}
		return nil, nil
	case 0xd9, 0xda, 0xdb: // str 8/16/32
		lenWidth := map[byte]int{0xd9: 1, 0xda: 2, 0xdb: 4}[m]
		n, err := d.uintOfWidth(lenWidth)
		if err != nil {
			return nil, err
		}
		return d.decodeString(int(n))
	case 0xdc, 0xdd: // array 16/32
		lenWidth := 2
		if m == 0xdd {
			lenWidth = 4
		}
		n, err := d.uintOfWidth(lenWidth)
		if err != nil {
			return nil, err
		}
		return d.decodeArray(int(n))
	case 0xde, 0xdf: // map 16/32
		lenWidth := 2
		if m == 0xdf {
			lenWidth = 4
		}
		n, err := d.uintOfWidth(lenWidth)
		if err != nil {
			return nil, err
		}
		return d.decodeMap(int(n))
	case 0xc7, 0xc8, 0xc9: // ext 8/16/32
		lenWidth := map[byte]int{0xc7: 1, 0xc8: 2, 0xc9: 4}[m]
		n, err := d.uintOfWidth(lenWidth)
		if err != nil {
			return nil, err
		}
		if _, err := d.take(1 + int(n)); err != nil {
			return nil, err
		}
		return nil, nil
	}

	return nil, fmt.Errorf("stockdb: 不支持的 msgpack 标记 0x%02x", m)
}

func (d *decoder) decodeString(length int) (string, error) {
	raw, err := d.take(length)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (d *decoder) decodeArray(length int) ([]any, error) {
	if length < 0 {
		return nil, ErrTruncated
	}
	out := make([]any, 0, length)
	for i := 0; i < length; i++ {
		item, err := d.decode()
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (d *decoder) decodeMap(length int) (map[string]any, error) {
	if length < 0 {
		return nil, ErrTruncated
	}
	out := make(map[string]any, length)
	for i := 0; i < length; i++ {
		key, err := d.decode()
		if err != nil {
			return nil, err
		}
		value, err := d.decode()
		if err != nil {
			return nil, err
		}
		out[String(key)] = value
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 类型转换辅助
// ---------------------------------------------------------------------------

// ToMap 将解码结果转换为 map。
func ToMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// ToSlice 将解码结果转换为切片。
func ToSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

// String 将解码结果转换为字符串。
func String(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// Float 将解码结果转换为 float64。
func Float(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	case int:
		return float64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// Int 将解码结果转换为 int64。
func Int(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint64:
		return int64(t)
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	case int:
		return int64(t)
	default:
		return 0
	}
}

// Bool 将解码结果转换为 bool。
func Bool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}
