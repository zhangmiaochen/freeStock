package service

import (
	"sync"
)

// ---------------------------------------------------------------------------
// 排名表筛选用：名称 → 拼音（全拼 / 首字母）
//
// ## 为什么放在后端而不是前端
//
// 拼音搜索这件事本项目**已经有一套实现**（`kline.go` 的 `pinyinVariants`，
// 基于 `github.com/mozillazg/go-pinyin`），服务对象是股票列表页与 K 线页的
// 搜索联想：支持代码 / 名称 / 拼音全拼 / 拼音首字母，并且**列出多音字的所有
// 读音**（「平安银行」的「行」读 háng，所以 `payh` 与 `payx` 都能搜到）。
//
// 多期限性价比的排名表要做的是同一件事，所以必须**复用同一套规则**：
//   - 在前端再引一个 JS 拼音库 → 两套字典、两套多音字策略，
//     同一个名称在两个页面可能给出不同结果，属于典型的"看起来能用但不一致"；
//   - 前端引库还要多打包几十 KB，而 Go 这边字典已经在二进制里。
//
// 所以这里只做一件事：把 `pinyinVariants` 暴露给排名表，并加一层缓存。
// ---------------------------------------------------------------------------

// pinyinCacheMaxEntries 是拼音缓存的条目上限。
//
// 全市场约 8000 个名称，缓存住它们只是几百 KB —— 上限设 20000 是为了
// 防御"调用方不断传新字符串"（例如把代码当名称传进来），不要让这个缓存
// 变成无界增长的内存泄漏。
const pinyinCacheMaxEntries = 20000

// pinyinCache 缓存"名称 → 拼音"。
//
// 为什么值得缓存：排名表一次要几千个名称，而一个名称的拼音（含多音字展开）
// 实测要几十微秒；更重要的是**切换期限档位时是同一批名称**，
// 不缓存就要把同一份计算再做 4 遍。
type pinyinCache struct {
	mu      sync.RWMutex
	entries map[string]PinyinInfo
}

var namePinyinCache = &pinyinCache{entries: make(map[string]PinyinInfo, 8192)}

// PinyinInfo 是一个名称的拼音（供前端做搜索匹配）。
type PinyinInfo struct {
	// Pinyin 是全拼，多音字的所有写法用空格分隔（如 "pinganyinxing pinganyinhang"）。
	Pinyin string `json:"pinyin"`
	// Initials 是首字母缩写，同样是多写法（如 "payx payh"）。
	Initials string `json:"initials"`
}

// GetNamePinyin 返回一批名称的拼音（全拼 + 首字母，含多音字多写法）。
//
// 返回的 map 以**入参名称原样**为键，未命中的名称不会出现在结果里
// （前端按"没有拼音就只按名称匹配"处理）。空名称与超长名称直接跳过 ——
// 排名表里名称理论上不会是空的，但接口不该假设调用方一定传对。
func (s *StockService) GetNamePinyin(names []string) map[string]PinyinInfo {
	out := make(map[string]PinyinInfo, len(names))
	if len(names) == 0 {
		return out
	}

	missing := make([]string, 0, len(names))
	namePinyinCache.mu.RLock()
	for _, name := range names {
		if name == "" {
			continue
		}
		if info, ok := namePinyinCache.entries[name]; ok {
			out[name] = info
			continue
		}
		missing = append(missing, name)
	}
	namePinyinCache.mu.RUnlock()

	if len(missing) == 0 {
		return out
	}

	computed := make(map[string]PinyinInfo, len(missing))
	for _, name := range missing {
		full, initials := pinyinVariants(name)
		info := PinyinInfo{Pinyin: full, Initials: initials}
		computed[name] = info
		out[name] = info
	}

	namePinyinCache.mu.Lock()
	if len(namePinyinCache.entries)+len(computed) > pinyinCacheMaxEntries {
		// 超上限就整体清空重建。刻意不做 LRU：这里的键是股票名称，
		// 集合基本上是稳定的，清空一次的代价（几千次拼音计算）远小于
		// 维护 LRU 链表的复杂度与出错风险。
		namePinyinCache.entries = make(map[string]PinyinInfo, len(computed))
	}
	for name, info := range computed {
		namePinyinCache.entries[name] = info
	}
	namePinyinCache.mu.Unlock()

	return out
}
