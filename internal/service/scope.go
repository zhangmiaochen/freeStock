package service

import (
	"strings"

	"freestock/internal/cnlake"
)

// ScopeMatcher 用数据湖的成员表做**行业/概念范围**匹配。
//
// 原实现用研究库的 `stock_board_link`（股票 ↔ 板块 的多对多关联表）。
// 本项目的数据湖里有等价物：
//
//	industry_members(symbol, classification_system, industry_code, industry_name)
//	sector_members  (symbol, sector_code, sector_name)
//
// 而 `stock_strength.industry` 只是"板块优先级第一名"的**单值**（见
// scoring.go 的 `industryByCode[code] = boards[0]`）—— 用它匹配会漏掉
// "这只票还属于其它行业/概念"的情况，组合设了行业范围却少选一批票。
//
// 注意：`stock_strength.industry` **保持不变**（仍供界面展示与
// 行业超额收益 `industry_strength_pct` 使用），只有**范围匹配**改走这里。
type ScopeMatcher struct {
	industries map[string]map[string]bool // 6 位代码 → 行业名集合
	sectors    map[string]map[string]bool // 6 位代码 → 板块名集合
	ready      bool
}

// scopeMatcherFor 构建（并缓存）范围匹配器。
//
// 成员表是静态数据（`Lake.IndustryMembers` 内部也做了缓存），
// 失败时返回一个 `ready=false` 的空匹配器 —— 调用方据此**退回单值匹配**，
// 而不是让所有行业/概念范围都匹配不到票。
func (s *StockService) scopeMatcherFor() *ScopeMatcher {
	s.scopeOnce.Do(func() {
		s.scopeMatch = buildScopeMatcher(s.lakeHandle())
	})
	return s.scopeMatch
}

// scopeMatcher 是 PortfolioService 侧的取用入口（内部持有 StockService）。
//
// StockService 为空（纯 CRUD 场景）时返回 nil 匹配器 → 调用方退回单值匹配。
func (s *PortfolioService) scopeMatcher() *ScopeMatcher {
	if s.stock == nil {
		return nil
	}
	return s.stock.scopeMatcherFor()
}

func buildScopeMatcher(lake *cnlake.Lake) *ScopeMatcher {
	out := &ScopeMatcher{
		industries: map[string]map[string]bool{},
		sectors:    map[string]map[string]bool{},
	}
	if lake == nil || !lake.HasDataset("industry_members") {
		return out
	}
	if rows, err := lake.IndustryMembers(); err == nil && len(rows) > 0 {
		for _, row := range rows {
			code := bareSix(row.Symbol)
			name := strings.TrimSpace(row.IndustryName)
			if code == "" || name == "" {
				continue
			}
			if out.industries[code] == nil {
				out.industries[code] = map[string]bool{}
			}
			out.industries[code][name] = true
		}
	}
	if lake.HasDataset("sector_members") {
		if rows, err := lake.SectorMembers(); err == nil && len(rows) > 0 {
			for _, row := range rows {
				code := bareSix(row.Symbol)
				name := strings.TrimSpace(row.SectorName)
				if code == "" || name == "" {
					continue
				}
				if out.sectors[code] == nil {
					out.sectors[code] = map[string]bool{}
				}
				out.sectors[code][name] = true
			}
		}
	}
	out.ready = len(out.industries) > 0 || len(out.sectors) > 0
	return out
}

// Ready 表示成员表是否真的读到了（行业或概念任一有数据）。
func (m *ScopeMatcher) Ready() bool { return m != nil && m.ready }

// HasIndustryData / HasSectorData 分别表示**行业表 / 概念表**是否读到了。
//
// 为什么要分开问：原实现 `_scope_filter` 的行业与概念分支判定条件不同 ——
// 行业是"单值行业名 **OR** 成员表命中"（成员表空也能靠单值选出票），
// 概念是"**只**看成员表"（成员表空 → 空集）。用一个合并的 Ready() 会把
// 两者混为一谈：概念范围在"只有行业表、没有概念表"时会被误判成可用。
func (m *ScopeMatcher) HasIndustryData() bool {
	return m != nil && len(m.industries) > 0
}

func (m *ScopeMatcher) HasSectorData() bool {
	return m != nil && len(m.sectors) > 0
}

// HasIndustry 判定股票是否属于该行业（成员表里的**任意一个**分类体系）。
func (m *ScopeMatcher) HasIndustry(code, name string) bool {
	if m == nil {
		return false
	}
	return m.industries[bareSix(code)][strings.TrimSpace(name)]
}

// HasSector 判定股票是否属于该概念板块。
func (m *ScopeMatcher) HasSector(code, name string) bool {
	if m == nil {
		return false
	}
	return m.sectors[bareSix(code)][strings.TrimSpace(name)]
}

// bareSix 取 6 位纯数字代码（湖泊的 symbol 可能带 .SH/.SZ 后缀）。
func bareSix(symbol string) string {
	value := strings.ToUpper(strings.TrimSpace(symbol))
	if index := strings.IndexAny(value, ". _-"); index > 0 {
		value = value[:index]
	}
	if len(value) != 6 {
		return ""
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	return value
}
