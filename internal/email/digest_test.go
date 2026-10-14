package email

import (
	"strings"
	"testing"
	"time"
)

func TestParseAddressAcceptsNormal(t *testing.T) {
	cases := []string{
		"a@example.com",
		"first.last@sub.example.com.cn",
		"user+tag@example.com",
		"x@a-b.com",
		"   spaced@example.com   ",
		"显示名 <person@example.com>",
	}
	for _, input := range cases {
		if _, err := ParseAddress(input); err != nil {
			t.Errorf("ParseAddress(%q) 不该失败: %v", input, err)
		}
	}
}

func TestParseAddressRejectsBad(t *testing.T) {
	cases := []struct {
		input  string
		reason string
	}{
		{"", "空"},
		{"no-at-sign", "缺少 @"},
		{"a@b", "域名没有点号"},
		{"a@@b.com", "两个 @"},
		{"@example.com", "本地部分为空"},
		{"a@", "域名为空"},
		{"a b@example.com", "含空格"},
		{"a@exa mple.com", "域名含空格"},
		{"a@.example.com", "域名以点开头"},
		{"a@example.com.", "域名以点结尾"},
		{"a@-example.com", "域名以连字符开头"},
		{"a@example..com", "连续点号"},
		{"中文@example.com", "非 ASCII"},
		{"a@例子.com", "非 ASCII 域名"},
	}
	for _, item := range cases {
		if _, err := ParseAddress(item.input); err == nil {
			t.Errorf("ParseAddress(%q) 应当失败（%s）", item.input, item.reason)
		}
	}
}

// TestParseRecipientsReportsInvalid 非法地址必须**原样报出来**而不是静默丢弃。
func TestParseRecipientsReportsInvalid(t *testing.T) {
	valid, invalid := ParseRecipients("a@example.com\nbad-address\nb@example.com, c@")
	if len(valid) != 2 {
		t.Fatalf("合法地址数 = %d，期望 2（%v）", len(valid), valid)
	}
	if len(invalid) != 2 {
		t.Fatalf("非法地址数 = %d，期望 2（%v）", len(invalid), invalid)
	}
	if invalid[0] != "bad-address" || invalid[1] != "c@" {
		t.Errorf("非法地址原文 = %v", invalid)
	}
}

func TestParseRecipientsDeduplicates(t *testing.T) {
	valid, _ := ParseRecipients("A@example.com\na@example.com\nb@example.com")
	if len(valid) != 2 {
		t.Fatalf("应去重（不区分大小写），实际 %v", valid)
	}
}

func TestParseRecipientsSplitsAllSeparators(t *testing.T) {
	valid, invalid := ParseRecipients("a@x.com, b@x.com；c@x.com，d@x.com;e@x.com\tf@x.com")
	if len(invalid) != 0 {
		t.Fatalf("不该有非法地址：%v", invalid)
	}
	if len(valid) != 6 {
		t.Fatalf("应切出 6 个地址，实际 %d（%v）", len(valid), valid)
	}
}

func TestValidateRequiresHostAndRecipients(t *testing.T) {
	msg := Message{From: "a@example.com", To: []string{"b@example.com"},
		Subject: "s", HTML: "h"}

	if err := Validate(Config{Port: 465}, msg); err == nil {
		t.Error("缺主机应当报错")
	}
	msg.To = nil
	if err := Validate(Config{Host: "smtp.example.com", Port: 465}, msg); err == nil {
		t.Error("缺收件人应当报错")
	}
}

// TestResolvedEncryptionByPort 端口决定默认加密方式。
func TestResolvedEncryptionByPort(t *testing.T) {
	cases := []struct {
		port int
		want string
	}{
		{465, EncryptSSL},
		{587, EncryptSTARTTLS},
		{25, EncryptSTARTTLS},
	}
	for _, item := range cases {
		got := resolvedEncryption(Config{Port: item.port, Encryption: EncryptAuto})
		if got != item.want {
			t.Errorf("端口 %d 解析成 %q，期望 %q", item.port, got, item.want)
		}
	}
	// 显式指定时不被端口覆盖
	got := resolvedEncryption(Config{Port: 465, Encryption: EncryptNone})
	if got != EncryptNone {
		t.Errorf("显式 EncryptNone 被覆盖成 %q", got)
	}
}

// ---------------------------------------------------------------------------
// HTML 渲染
// ---------------------------------------------------------------------------

func sampleDigest() Digest {
	return Digest{
		GeneratedAt:      time.Date(2026, 10, 13, 17, 0, 0, 0, time.Local),
		TradeDate:        "20261013",
		MarketStateLabel: "震荡",
		Portfolios: []PortfolioDigest{
			{
				Name:           "主力组合",
				TypeLabel:      "半自动组合",
				StrategyLabel:  "趋势成长",
				HealthLabel:    "稳健",
				PortfolioScore: 76.4,
				ReturnPct:      12.34,
				MaxDrawdownPct: 8.5,
				PositionCount:  2,
				UsedLayers:     3,
				TotalLayers:    10,
				PositionRatio:  30,
				PriceDate:      "20261013",
				TradeDate:      "20261013",
				Actions: []ActionDigest{
					{
						Action: "clear", ActionLabel: "清仓", Code: "600519", Name: "贵州茅台",
						Score: 42.1, ScoreGap: -18.3, LayersText: "2 → 0",
						Executable: true, Reason: "评分跌破清仓线",
					},
					{
						Action: "add", ActionLabel: "新建仓", Code: "000001", Name: "平安银行",
						Score: 81.5, ScoreGap: 5.2, Candidate: "000001", CandidateNme: "平安银行",
						LayersText: "0 → 2", Executable: true, Reason: "评分进入建仓区间",
					},
				},
				Holdings: []HoldingDigest{
					{
						Code: "600519", Name: "贵州茅台", Layers: 2, WeightPct: 20,
						EntryPrice: 1500, CurrentPrice: 1680, ProfitLossPct: 12,
						EntryDate: "20260801", HoldingType: "底仓",
					},
				},
			},
		},
	}
}

func TestRenderHTMLContainsCoreSections(t *testing.T) {
	html, err := RenderHTML(sampleDigest())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	musts := []string{
		"调仓日报",
		"2026-10-13", // 评分日
		"主力组合",       // 组合名
		"组合收益率",      // 汇总
		"+12.34%",    // 收益率带符号
		"调仓动作",       // 动作表
		"600519",     // 代码
		"贵州茅台",       // 名称
		"清仓",         // 动作标签
		"评分跌破清仓线",    // 原因
		"当前持仓",       // 持仓表
		"浮动盈亏",
		"数量口径是", // 页脚口径说明
		"不构成投资建议",
	}
	for _, want := range musts {
		if !strings.Contains(html, want) {
			t.Errorf("渲染结果里缺少 %q", want)
		}
	}
}

// TestRenderHTMLEscapesUserContent 组合名/名称里的 HTML 必须转义。
//
// 这些字段来自用户输入（组合名）与数据源（股票名），不转义就是注入面。
func TestRenderHTMLEscapesUserContent(t *testing.T) {
	digest := sampleDigest()
	digest.Portfolios[0].Name = `<script>alert('x')</script>`
	digest.Portfolios[0].Actions[0].Name = `<img src=x onerror=alert(1)>`
	digest.Portfolios[0].Actions[0].Reason = `a & b < c`

	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatal("组合名里的 <script> 没有被转义")
	}
	if strings.Contains(html, "<img src=x") {
		t.Fatal("股票名里的 <img> 没有被转义")
	}
	if !strings.Contains(html, "a &amp; b &lt; c") {
		t.Error("原因里的 & 与 < 没有被转义")
	}
}

// TestRenderHTMLNoActionExplainsWhy 没有动作时要给出原因，而不是空白。
func TestRenderHTMLNoActionExplainsWhy(t *testing.T) {
	digest := sampleDigest()
	digest.Portfolios[0].Actions = nil
	digest.Portfolios[0].NoActionReasons = []string{"候选池里没有股票达到建仓质量门槛"}

	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(html, "候选池里没有股票达到建仓质量门槛") {
		t.Error("没有动作时应当写出「为什么没有动作」")
	}
}

// TestRenderHTMLSurfacesPlanError 生成建议失败要写在邮件里照发。
//
// 静默不发或者发一封空邮件，用户会以为"策略今天没动作"。
func TestRenderHTMLSurfacesPlanError(t *testing.T) {
	digest := sampleDigest()
	digest.Portfolios[0].PlanError = "还没有评分数据，请先刷新评分"
	digest.Portfolios[0].Actions = nil

	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(html, "本次未能生成调仓建议") {
		t.Error("应当明确写出未能生成建议")
	}
	if !strings.Contains(html, "还没有评分数据，请先刷新评分") {
		t.Error("应当给出具体原因")
	}
}

// TestRenderHTMLEmptyPortfolios 没有可发组合时也要给出一封可读的邮件。
func TestRenderHTMLEmptyPortfolios(t *testing.T) {
	digest := sampleDigest()
	digest.Portfolios = nil
	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(html, "没有可发送的组合") {
		t.Error("应当说明没有可发送的组合")
	}
}

// TestRenderHTMLMissingPriceShowsDash 取不到现价时，现价与盈亏列都显示 — 而不是 0%。
func TestRenderHTMLMissingPriceShowsDash(t *testing.T) {
	digest := sampleDigest()
	digest.Portfolios[0].Holdings = []HoldingDigest{
		{Code: "000002", Name: "停牌股", Layers: 1, WeightPct: 10,
			EntryPrice: 10, CurrentPrice: 0, ProfitLossPct: 0},
	}
	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 定位到这一行
	start := strings.Index(html, "000002")
	if start < 0 {
		t.Fatal("渲染结果里找不到这只持仓")
	}
	end := strings.Index(html[start:], "</tr>")
	if end < 0 {
		t.Fatal("找不到这一行的结束")
	}
	row := html[start : start+end]

	// 现价 / 盈亏 / 建仓日三列都应当是破折号
	if strings.Count(row, ">—<") < 3 {
		t.Errorf("现价/盈亏/建仓日三列应当都是 —，实际行：%s", row)
	}
	// 权重的 10.00% 里含有 "0.00%" 子串，所以按"以 > 开头的单元格"精确判断
	if strings.Contains(row, ">0.00%<") {
		t.Errorf("不该把缺失的盈亏渲染成 0.00%%，实际行：%s", row)
	}
}

// TestRenderHTMLAllStylesAreInline 版式必须靠内联样式。
//
// Gmail / Outlook 会剥掉 <style> 里的选择器，只保内联样式。
func TestRenderHTMLAllStylesAreInline(t *testing.T) {
	html, err := RenderHTML(sampleDigest())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(html, "<style") {
		t.Error("不该依赖 <style> 块 —— 邮件客户端会剥掉它")
	}
	if !strings.Contains(html, `style="`) {
		t.Error("缺少内联样式")
	}
	if !strings.Contains(html, `charset="utf-8"`) && !strings.Contains(html, `charset=utf-8`) {
		t.Error("缺少 UTF-8 声明，中文会乱码")
	}
	if !strings.Contains(html, "viewport") {
		t.Error("缺少 viewport，手机端排版会挤成一条")
	}
}

// TestRenderHTMLUsesChineseColorConvention 红涨绿跌。
func TestRenderHTMLUsesChineseColorConvention(t *testing.T) {
	digest := sampleDigest()
	html, err := RenderHTML(digest)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// +12.34% 的收益率应当是红色
	if !strings.Contains(html, "#dc2626") {
		t.Error("上涨应为红色（A 股习惯）")
	}
	if !strings.Contains(html, "#16a34a") {
		t.Error("下跌应为绿色")
	}
}

// ---------------------------------------------------------------------------
// 纯文本备选
// ---------------------------------------------------------------------------

func TestRenderTextCoversKeyFields(t *testing.T) {
	text := RenderText(sampleDigest())
	for _, want := range []string{
		"调仓日报", "主力组合", "600519", "贵州茅台", "清仓", "当前持仓", "评分日：2026-10-13",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("纯文本正文缺少 %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 动作排序
// ---------------------------------------------------------------------------

// TestSortActionsPutsSellsFirst 邮件是待办清单：先清仓/换出，再减仓，再买。
func TestSortActionsPutsSellsFirst(t *testing.T) {
	actions := []ActionDigest{
		{Action: "keep", Code: "000004"},
		{Action: "add", Code: "000003"},
		{Action: "reduce", Code: "000002"},
		{Action: "clear", Code: "000001"},
		{Action: "replace_out", Code: "000005"},
		{Action: "replace_in", Code: "000006"},
	}
	SortActions(actions)
	got := make([]string, 0, len(actions))
	for _, action := range actions {
		got = append(got, action.Action)
	}
	want := []string{"clear", "replace_out", "reduce", "replace_in", "add", "keep"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序结果 = %v，期望 %v", got, want)
		}
	}
}

// TestSortActionsIsStableWithinSameRank 同档内保持稳定（按代码），不抖动。
func TestSortActionsIsStableWithinSameRank(t *testing.T) {
	actions := []ActionDigest{
		{Action: "add", Code: "000003"},
		{Action: "add", Code: "000001"},
		{Action: "add", Code: "000002"},
	}
	SortActions(actions)
	if actions[0].Code != "000001" || actions[2].Code != "000003" {
		t.Errorf("同档内应按代码升序，实际 %v", actions)
	}
}

// ---------------------------------------------------------------------------
// 格式化
// ---------------------------------------------------------------------------

func TestFormatHelpers(t *testing.T) {
	if got := formatPct(0); got != "0.00%" {
		t.Errorf("formatPct(0) = %q", got)
	}
	if got := formatPct(3.5); got != "+3.50%" {
		t.Errorf("formatPct(3.5) = %q", got)
	}
	if got := formatPct(-3.5); got != "-3.50%" {
		t.Errorf("formatPct(-3.5) = %q", got)
	}
	if got := displayDate("20261013"); got != "2026-10-13" {
		t.Errorf("displayDate = %q", got)
	}
	if got := displayDate(""); got != "—" {
		t.Errorf("displayDate(空) = %q", got)
	}
	if got := trendColor(1); got != "#dc2626" {
		t.Errorf("trendColor(1) = %q", got)
	}
	if got := trendColor(-1); got != "#16a34a" {
		t.Errorf("trendColor(-1) = %q", got)
	}
}
