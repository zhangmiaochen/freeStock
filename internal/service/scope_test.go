package service

import (
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/portfolio"
)

// strengthRowWithIndustry 造一个只带代码与行业名的评分行。
func strengthRowWithIndustry(code, industry string) portfolio.StrengthRow {
	return portfolio.StrengthRow{Code: code, Industry: industry}
}

// 用**真实数据湖**验证行业/概念成员表能读到，且是**多值**的。
//
// 这是之前误判为"做不到"的第二项 —— 数据其实在
// curated/industry_members 与 curated/sector_members 里。
func TestScopeMatcherRealData(t *testing.T) {
	lake := cnlake.Open("")
	matcher := buildScopeMatcher(lake)
	if !matcher.Ready() {
		t.Skip("数据湖成员表未入库，跳过")
	}

	industries, err := lake.IndustryMembers()
	if err != nil || len(industries) == 0 {
		t.Fatalf("行业成员表读不到：%v", err)
	}

	// 统计"一只股票属于几个行业" —— 只要有 >1 的就说明单值匹配会漏
	perCode := map[string]int{}
	for _, row := range industries {
		if code := bareSix(row.Symbol); code != "" {
			perCode[code]++
		}
	}
	systems := map[string]bool{}
	for _, row := range industries {
		systems[row.ClassificationSystem] = true
	}
	multi := 0
	for _, count := range perCode {
		if count > 1 {
			multi++
		}
	}
	// ⚠ 实测结论：**本数据湖的行业是单值的**
	//（5572 行 / 5572 只股票 / 1 个分类体系 / 0 只多值）。
	// 所以行业那一半换成成员表**没有收益** —— 与原来的单值匹配等价。
	// 真正有收益的是**概念**（sector_members 八万多行，每只票平均十几个）。
	t.Logf("行业成员表 %d 行，覆盖 %d 只股票，分类体系 %d 个；其中 %d 只有多条行业记录",
		len(industries), len(perCode), len(systems), multi)
	if multi != 0 {
		t.Logf("（注意：本湖行业是多值的，%d 只有多条记录）", multi)
	}

	// 拿第一只有记录的股票，验证 matcher 能命中它的行业名
	for _, row := range industries {
		code := bareSix(row.Symbol)
		name := row.IndustryName
		if code == "" || name == "" {
			continue
		}
		if !matcher.HasIndustry(code, name) {
			t.Errorf("%s 应命中行业 %q", code, name)
		}
		// 不存在的行业名不该命中
		if matcher.HasIndustry(code, "肯定不存在的行业名xyz") {
			t.Errorf("%s 不该命中不存在的行业", code)
		}
		break
	}

	// **概念必须是多值的** —— 否则这次改动毫无意义
	sectors, err := lake.SectorMembers()
	if err != nil || len(sectors) == 0 {
		t.Skipf("概念成员表读不到：%v", err)
	}
	perCodeSector := map[string]int{}
	for _, row := range sectors {
		if code := bareSix(row.Symbol); code != "" {
			perCodeSector[code]++
		}
	}
	multiSector := 0
	for _, count := range perCodeSector {
		if count > 1 {
			multiSector++
		}
	}
	t.Logf("概念成员表 %d 行，覆盖 %d 只股票；其中 %d 只有多个概念",
		len(sectors), len(perCodeSector), multiSector)
	if multiSector == 0 {
		t.Error("概念也是单值的 —— 那这次改动就没有意义了")
	}
	for _, row := range sectors {
		code := bareSix(row.Symbol)
		if code == "" || row.SectorName == "" {
			continue
		}
		if !matcher.HasSector(code, row.SectorName) {
			t.Errorf("%s 应命中概念 %q", code, row.SectorName)
		}
		break
	}
}

// bareSix 的边界。
func TestBareSix(t *testing.T) {
	cases := map[string]string{
		"600519.SH": "600519",
		"000001.SZ": "000001",
		"600519":    "600519",
		"60051":     "",
		"ABCDEF":    "",
		"":          "",
		" 600519 ":  "600519",
	}
	for input, want := range cases {
		if got := bareSix(input); got != want {
			t.Errorf("bareSix(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// 成员表不可用时必须**退回单值**，不能把所有行业范围都匹配空。
func TestMatchScopeItemFallsBackToSingleValue(t *testing.T) {
	strength := strengthRowWithIndustry("600519", "白酒")
	empty := &ScopeMatcher{} // ready=false

	if !matchScopeItem([]string{"白酒"}, strength, empty, true) {
		t.Error("成员表不可用时应退回单值匹配并命中")
	}
	if matchScopeItem([]string{"银行"}, strength, empty, true) {
		t.Error("退回单值时不该命中别的行业")
	}
	// 空条件 = 不限
	if !matchScopeItem(nil, strength, empty, true) {
		t.Error("空条件应视为不限")
	}
	// matcher 为 nil 也不能 panic
	if !matchScopeItem([]string{"白酒"}, strength, nil, true) {
		t.Error("matcher 为 nil 时应退回单值")
	}
}
