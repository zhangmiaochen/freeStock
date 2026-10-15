package service

import (
	"testing"
)

// TestGetNamePinyinMatchesSearchIndex 钉住"排名表的拼音与股票列表页是同一套规则"。
//
// ## 为什么必须钉这一条
//
// 排名表的拼音搜索没有另引 JS 拼音库，而是复用 `kline.go` 的 `pinyinVariants`
// （go-pinyin + 列出多音字所有读音）。这个选择只有在"两边结果真的一致"时才成立 ——
// 一旦有人为了"让排名表搜得更准"而改这里的实现，就会变成同一个名称在
// 股票列表页与性价比页搜出不同结果，用户只会觉得"搜索坏了"。
//
// 所以断言直接对比 `GetNamePinyin` 与 `pinyinVariants` 的输出。
func TestGetNamePinyinMatchesSearchIndex(t *testing.T) {
	svc := &StockService{}
	names := []string{"贵州茅台", "平安银行", "紫金矿业", "招商银行", "中国平安", "宁德时代"}

	got := svc.GetNamePinyin(names)
	if len(got) != len(names) {
		t.Fatalf("返回 %d 个名称，应为 %d", len(got), len(names))
	}

	for _, name := range names {
		wantFull, wantInitials := pinyinVariants(name)
		info, ok := got[name]
		if !ok {
			t.Errorf("%s 没有返回拼音", name)
			continue
		}
		if info.Pinyin != wantFull || info.Initials != wantInitials {
			t.Errorf("%s 的拼音与搜索索引不一致：\n  got  %q / %q\n  want %q / %q",
				name, info.Pinyin, info.Initials, wantFull, wantInitials)
		}
	}
}

// TestGetNamePinyinHandlesMultitone 钉住多音字：所有读法都要在结果里。
//
// 「平安银行」的「行」读 háng 而不是 xíng。go-pinyin 默认只给最常见读音，
// 所以 `pinyinVariants` 特意用 `Heteronym = true` 把每种读法都列出来 ——
// 用户输入 `payh` 与 `payx` 都必须能搜到。
func TestGetNamePinyinHandlesMultitone(t *testing.T) {
	svc := &StockService{}
	got := svc.GetNamePinyin([]string{"平安银行"})

	info, ok := got["平安银行"]
	if !ok {
		t.Fatal("平安银行 没有返回拼音")
	}
	t.Logf("平安银行 → 全拼 %q / 首字母 %q", info.Pinyin, info.Initials)

	for _, want := range []string{"payh", "payx"} {
		if !containsToken(info.Initials, want) {
			t.Errorf("首字母 %q 里应包含 %q（多音字的所有读法都要给出）", info.Initials, want)
		}
	}
}

// containsToken 判断空格分隔的多读法串里是否含某个写法。
func containsToken(value, token string) bool {
	for _, field := range splitFields(value) {
		if field == token {
			return true
		}
	}
	return false
}

func splitFields(value string) []string {
	out := []string{}
	current := ""
	for _, r := range value {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// TestGetNamePinyinEdges 钉住边界：空入参、空名称、重复名称、无中文名称。
func TestGetNamePinyinEdges(t *testing.T) {
	svc := &StockService{}

	if got := svc.GetNamePinyin(nil); len(got) != 0 {
		t.Errorf("空入参应返回空 map，实际 %d 项", len(got))
	}
	if got := svc.GetNamePinyin([]string{}); len(got) != 0 {
		t.Errorf("空切片应返回空 map，实际 %d 项", len(got))
	}

	// 空名称应被跳过（不该产生一个 "" 键）
	got := svc.GetNamePinyin([]string{"", "贵州茅台", "贵州茅台"})
	if _, exists := got[""]; exists {
		t.Error("空名称不该出现在结果里")
	}
	if len(got) != 1 {
		t.Errorf("重复名称应只算一次、只返回一项，实际 %d 项：%v", len(got), got)
	}
	if info := got["贵州茅台"]; info.Initials == "" {
		t.Error("贵州茅台 的首字母不该为空")
	}

	// 无中文（例如 ETF 名称里的纯 ASCII）不该 panic
	_ = svc.GetNamePinyin([]string{"TMF", "510300", "-"})
}

// TestGetNamePinyinIsCached 钉住缓存：同一名称第二次调用不再重算。
//
// 这条不好直接断言"算了几次"（`pinyinVariants` 是包内函数，没有计数器），
// 所以用**指针/内容一致性 + 复用行为**间接验证：
// 第二次调用必须仍然返回正确值（缓存不能返回空），且 map 值完全相同。
func TestGetNamePinyinIsCached(t *testing.T) {
	svc := &StockService{}
	first := svc.GetNamePinyin([]string{"贵州茅台", "五粮液"})
	second := svc.GetNamePinyin([]string{"贵州茅台", "五粮液"})

	if len(first) != len(second) {
		t.Fatalf("两次调用项数不同：%d vs %d", len(first), len(second))
	}
	for name, info := range first {
		other, ok := second[name]
		if !ok {
			t.Errorf("%s 第二次调用丢了", name)
			continue
		}
		if info != other {
			t.Errorf("%s 两次结果不同：%+v vs %+v", name, info, other)
		}
	}

	// 缓存必须能被清空重建（那条超上限就整体清空的路径）
	namePinyinCache.mu.Lock()
	namePinyinCache.entries = map[string]PinyinInfo{}
	namePinyinCache.mu.Unlock()
	third := svc.GetNamePinyin([]string{"贵州茅台"})
	info, ok := third["贵州茅台"]
	if !ok {
		t.Fatal("清空缓存后重建丢了贵州茅台")
	}
	// ⚠ 用 token 包含而不是相等：「茅台」的「台」也是多音字
	// （tái / yí / sī），所以首字母是多读法 "gzmt gzmy gzms"。
	// 断言"包含 gzmt"才是这里真正要保证的事（用户输入 gzmt 必须命中）。
	if !containsToken(info.Initials, "gzmt") {
		t.Errorf("清空缓存后重建的首字母 %q 应包含 gzmt", info.Initials)
	}
	if !containsToken(info.Pinyin, "guizhoumaotai") {
		t.Errorf("清空缓存后重建的全拼 %q 应包含 guizhoumaotai", info.Pinyin)
	}
}
