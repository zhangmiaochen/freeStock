package chanlun

import (
	"strconv"
	"strings"
)

// formatPrice 把价格格式化成便于阅读的字符串（最多 4 位、去掉多余的 0）。
func formatPrice(value float64) string {
	text := strconv.FormatFloat(round4(value), 'f', 4, 64)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" || text == "-" {
		return "0"
	}
	return text
}

// formatRatio 把力度比值格式化成两位小数。
func formatRatio(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// itoa 是 strconv.Itoa 的短名字，避免在拼提示文案时反复打全名。
func itoa(value int) string { return strconv.Itoa(value) }

// pullbackNoBreakText 返回二买/二卖的依据文案。
func pullbackNoBreakText(isBuy bool) string {
	if isBuy {
		return "不破前低"
	}
	return "不破前高"
}

// pivotNoReturnText 返回三买/三卖的依据文案。
func pivotNoReturnText(isBuy bool) string {
	if isBuy {
		return "不跌回中枢"
	}
	return "不涨回中枢"
}
