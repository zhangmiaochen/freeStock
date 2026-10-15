package valuation

import (
	"math"
	"sort"
	"strings"
)

// pow 是 math.Pow 的薄封装。
//
// 单独抽出来只为两件事：一是让"这里用的是浮点幂"在调用处一眼可见，
// 二是方便在测试里替换成定点实现做数值对拍。
func pow(base, exponent float64) float64 { return math.Pow(base, exponent) }

// sortSlice 按 less 排序（Go 标准库的 sort.Slice 需要传 []T 与下标函数，
// 这里包一层让调用处只写一个比较函数，类型安全一些）。
func sortSlice[T any](items []T, less func(a, b T) bool) {
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
}

// joinStrings 用 sep 连接字符串。
func joinStrings(items []string, sep string) string { return strings.Join(items, sep) }
