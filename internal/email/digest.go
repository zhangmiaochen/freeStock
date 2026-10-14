package email

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 邮件内容视图模型
//
// 这几个结构是**邮件自己的**，不从 portfolio / score 包里借类型 ——
// 邮件的版式与接口模型是两件事：接口加了字段不该让邮件排版跟着变，
// 邮件想多带一个"给持有人看的口径说明"也不该污染接口模型。
// service 层负责把领域模型映射过来。
// ---------------------------------------------------------------------------

// Digest 是一封日报的全部内容。
type Digest struct {
	// GeneratedAt 是渲染时间（本地时区）
	GeneratedAt time.Time
	// TradeDate 是这一次评分/调仓建议所依据的交易日，YYYYMMDD
	TradeDate string
	// MarketStateLabel 是市场状态的中文标签（如「震荡」），可为空
	MarketStateLabel string
	// Portfolios 是本次要发的组合
	Portfolios []PortfolioDigest
	// Notes 是渲染过程中积累的提示（如"某组合没有评分数据"），
	// 会原样出现在邮件末尾 —— 这类信息不能只在日志里
	Notes []string
}

// PortfolioDigest 是一个组合在邮件里的全部内容。
type PortfolioDigest struct {
	Name          string
	TypeLabel     string
	StrategyLabel string
	// HealthLabel 是健康度标签（强势/稳健/观察/风险）
	HealthLabel string
	// PortfolioScore 是组合评分（0~100）
	PortfolioScore float64
	// ReturnPct 是组合收益率（持仓收益的加权和，不含现金）
	ReturnPct float64
	// MaxDrawdownPct 是持仓 120 日回撤里最负那个的绝对值
	MaxDrawdownPct float64

	PositionCount   int
	UsedLayers      int
	TotalLayers     int
	AvailableLayers int
	PositionRatio   float64
	PriceDate       string
	MissingPrices   int
	// TradeDate 是这个组合这次建议所依据的评分交易日（YYYYMMDD）。
	// 各组合可能停在不同日期（评分是全局刷新的，但归档前的组合可能更旧），
	// 所以它是**组合级**字段，而不是整封邮件一个。
	TradeDate string

	// Actions 是调仓动作（邮件主体）
	Actions []ActionDigest
	// Holdings 是当前持仓明细
	Holdings []HoldingDigest
	// Candidates 是候选池 Top N（可选）
	Candidates []CandidateDigest

	// NoActionReasons 是"为什么没有调仓动作"（最多 3 条）
	NoActionReasons []string
	// Notes 是这个组合自己的提示
	Notes []string
	// PlanError 非空表示这次没能生成建议（评分未刷新等），
	// 邮件里**照发**并写明原因 —— 静默不发的话，用户会以为策略今天没动作。
	PlanError string
}

// ActionDigest 是一条调仓动作。
type ActionDigest struct {
	Action       string
	ActionLabel  string
	ActionPrefix string
	Code         string
	Name         string
	Score        float64
	ScoreGap     float64
	Candidate    string // 候选代码
	CandidateNme string
	// LayersText 是"1 → 2"这样的层数变化，无变化时是 "1"
	LayersText string
	// Executable 表示这条动作能不能被应用（不可执行的只是提醒）
	Executable           bool
	RequiresConfirmation bool
	Reason               string
}

// HoldingDigest 是一条持仓。
type HoldingDigest struct {
	Code             string
	Name             string
	Layers           int
	WeightPct        float64
	EntryPrice       float64
	CurrentPrice     float64
	ProfitLossPct    float64
	EntryDate        string
	HoldingType      string
	HoldingTradeDays int
}

// CandidateDigest 是候选池里的一只。
type CandidateDigest struct {
	Code                string
	Name                string
	TotalScore          float64
	Close               float64
	RelativeStrength20  float64
	DrawdownFromHigh120 float64
	Industry            string
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// RenderHTML 把 Digest 渲染成一封完整的 HTML 邮件。
//
// 排版上的几个硬约束（邮件客户端比浏览器苛刻得多）：
//   - **全部样式内联**：Gmail / Outlook 会剥掉 <style> 里的选择器，
//     只有元素上的 style 属性可靠；<style> 里只留媒体查询这类兜底。
//   - **用 table 布局**：Outlook 的渲染引擎不支持 flex / grid。
//   - **不用外部图片**：默认被拦，拦掉之后版式会塌。
//   - 宽度 100% 且 max-width 880px：手机上自动收窄，桌面上不会拉得过宽。
func RenderHTML(d Digest) (string, error) {
	var b strings.Builder

	writeHead(&b)
	b.WriteString(`<body style="margin:0;padding:0;background:#f4f5f7;">`)
	// 外框：手机上加边距，桌面上居中
	b.WriteString(`<div style="width:100%;padding:16px 0;background:#f4f5f7;">`)
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" ` +
		`style="width:100%;max-width:880px;margin:0 auto;background:#ffffff;` +
		`border-radius:10px;border:1px solid #e3e6ea;font-family:'Segoe UI',` +
		`-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2430;">`)

	writeDigestHeader(&b, d)

	if len(d.Portfolios) == 0 {
		writeEmpty(&b, "本次没有可发送的组合",
			"全部组合都是「已归档」状态，或者发送范围指向的组合已不存在。")
	}

	for i := range d.Portfolios {
		writePortfolio(&b, d.Portfolios[i])
	}

	writeFooter(&b, d)

	b.WriteString(`</table></div></body></html>`)
	return b.String(), nil
}

// RenderText 生成纯文本备选正文。
//
// 一定要给：只发 HTML 时，某些客户端（或用户的纯文本模式）会显示成一堆
// 粘在一起的文字；multipart/alternative 给两个分部是标准做法。
func RenderText(d Digest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FreeStock 调仓日报\n")
	if d.TradeDate != "" {
		fmt.Fprintf(&b, "评分日：%s\n", displayDate(d.TradeDate))
	}
	if d.MarketStateLabel != "" {
		fmt.Fprintf(&b, "市场状态：%s\n", d.MarketStateLabel)
	}
	if len(d.Portfolios) == 0 {
		b.WriteString("\n本次没有可发送的组合（可能全部已归档）。\n")
	}

	for _, p := range d.Portfolios {
		b.WriteString("\n" + strings.Repeat("=", 48) + "\n")
		fmt.Fprintf(&b, "%s（%s · %s）\n", p.Name, p.TypeLabel, p.StrategyLabel)
		fmt.Fprintf(&b, "收益率 %s  持仓 %d 只  层数 %d/%d  健康度 %s%s\n",
			formatPct(p.ReturnPct), p.PositionCount, p.UsedLayers, p.TotalLayers,
			orDash(p.HealthLabel), scoreSuffix(p.PortfolioScore))

		if p.PlanError != "" {
			fmt.Fprintf(&b, "\n[未能生成调仓建议] %s\n", p.PlanError)
		}

		b.WriteString("\n-- 调仓动作 --\n")
		if len(p.Actions) == 0 {
			b.WriteString("（无调仓动作）\n")
		}
		for _, a := range p.Actions {
			fmt.Fprintf(&b, "%s %s %s  评分 %s  层数 %s%s\n",
				actionTitle(a), a.Code, orDash(a.Name), formatNum(a.Score, 2),
				a.LayersText, reasonSuffix(a.Reason))
		}
		for _, reason := range p.NoActionReasons {
			fmt.Fprintf(&b, "· %s\n", reason)
		}

		if len(p.Holdings) > 0 {
			b.WriteString("\n-- 当前持仓 --\n")
			for _, h := range p.Holdings {
				fmt.Fprintf(&b, "%s %s  %d层 %.2f%%  成本 %s  现价 %s  盈亏 %s\n",
					h.Code, orDash(h.Name), h.Layers, h.WeightPct,
					formatNum(h.EntryPrice, 2), formatNum(h.CurrentPrice, 2),
					formatPct(h.ProfitLossPct))
			}
		}

		if len(p.Candidates) > 0 {
			b.WriteString("\n-- 候选池 --\n")
			for _, c := range p.Candidates {
				fmt.Fprintf(&b, "%s %s  总分 %s  现价 %s  120日回撤 %s%%\n",
					c.Code, orDash(c.Name), formatNum(c.TotalScore, 2),
					formatNum(c.Close, 2), formatNum(c.DrawdownFromHigh120, 2))
			}
		}

		for _, note := range p.Notes {
			fmt.Fprintf(&b, "· %s\n", note)
		}
	}

	if len(d.Notes) > 0 {
		b.WriteString("\n-- 说明 --\n")
		for _, note := range d.Notes {
			fmt.Fprintf(&b, "· %s\n", note)
		}
	}
	b.WriteString("\n由 FreeStock 自动发送。\n")
	return b.String()
}

func writeHead(b *strings.Builder) {
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head>`)
	b.WriteString(`<meta charset="utf-8">`)
	// 手机端不加这行会按 980px 排版，整封邮件挤成一小条
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="color-scheme" content="light only">`)
	b.WriteString(`<title>FreeStock 调仓日报</title>`)
	b.WriteString(`</head>`)
}

func writeDigestHeader(b *strings.Builder, d Digest) {
	b.WriteString(`<tr><td style="padding:20px 24px 14px 24px;border-bottom:2px solid #1f2430;">`)
	b.WriteString(`<div style="font-size:19px;font-weight:600;letter-spacing:0.5px;">调仓日报</div>`)
	meta := make([]string, 0, 3)
	if d.TradeDate != "" {
		meta = append(meta, "评分日 "+displayDate(d.TradeDate))
	}
	if d.MarketStateLabel != "" {
		meta = append(meta, "市场状态 "+d.MarketStateLabel)
	}
	meta = append(meta, "生成于 "+d.GeneratedAt.Format("2006-01-02 15:04"))
	b.WriteString(`<div style="margin-top:5px;font-size:12px;color:#6b7280;">` +
		html.EscapeString(strings.Join(meta, " · ")) + `</div>`)
	b.WriteString(`</td></tr>`)
}

func writeEmpty(b *strings.Builder, title, detail string) {
	b.WriteString(`<tr><td style="padding:28px 24px;text-align:center;">`)
	b.WriteString(`<div style="font-size:14px;font-weight:600;color:#374151;">` +
		html.EscapeString(title) + `</div>`)
	b.WriteString(`<div style="margin-top:6px;font-size:12px;color:#6b7280;">` +
		html.EscapeString(detail) + `</div>`)
	b.WriteString(`</td></tr>`)
}

func writePortfolio(b *strings.Builder, p PortfolioDigest) {
	b.WriteString(`<tr><td style="padding:18px 24px 6px 24px;">`)

	// ---- 组合头 ----
	b.WriteString(`<div style="font-size:16px;font-weight:600;">` + html.EscapeString(p.Name))
	tags := make([]string, 0, 3)
	for _, tag := range []string{p.TypeLabel, p.StrategyLabel} {
		if strings.TrimSpace(tag) != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) > 0 {
		b.WriteString(`<span style="margin-left:8px;font-size:11px;font-weight:400;color:#6b7280;">` +
			html.EscapeString(strings.Join(tags, " · ")) + `</span>`)
	}
	b.WriteString(`</div>`)

	// ---- 汇总 ----
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" ` +
		`style="width:100%;margin-top:10px;border-collapse:collapse;font-size:12px;">`)
	b.WriteString(`<tr>`)
	writeSummaryCell(b, "组合收益率", formatPct(p.ReturnPct), trendColor(p.ReturnPct))
	writeSummaryCell(b, "健康度", healthText(p.HealthLabel, p.PortfolioScore), "#1f2430")
	writeSummaryCell(b, "持仓", strconv.Itoa(p.PositionCount)+" 只", "#1f2430")
	writeSummaryCell(b, "层数", fmt.Sprintf("%d / %d", p.UsedLayers, p.TotalLayers), "#1f2430")
	writeSummaryCell(b, "仓位", formatNum(p.PositionRatio, 1)+"%", "#1f2430")
	// ⚠ **没有持仓时回撤是 0，但 0% 与「没有数据」是两件事**：
	// 空仓组合显示「0.00%」会被读成"回撤为零，很安全"。
	writeSummaryCell(b, "最大回撤", drawdownText(p.MaxDrawdownPct, p.PositionCount),
		trendColor(-p.MaxDrawdownPct))
	b.WriteString(`</tr></table>`)

	// 现价口径与缺失提示：不写出来的话，用户会拿"停牌记 0"的盈亏去对账
	priceNote := ""
	if p.PriceDate != "" {
		priceNote = "现价取自 " + displayDate(p.PriceDate)
	}
	if p.MissingPrices > 0 {
		if priceNote != "" {
			priceNote += "；"
		}
		priceNote += fmt.Sprintf("有 %d 只没取到现价（停牌/退市/未同步），这些的盈亏按 0 计",
			p.MissingPrices)
	}
	if priceNote != "" {
		b.WriteString(`<div style="margin-top:6px;font-size:11px;color:#6b7280;">` +
			html.EscapeString(priceNote) + `</div>`)
	}

	// ---- 计划生成失败 ----
	if p.PlanError != "" {
		b.WriteString(`<div style="margin-top:12px;padding:10px 12px;background:#fef3c7;` +
			`border-left:3px solid #f59e0b;border-radius:4px;font-size:12px;color:#7c4a03;">`)
		b.WriteString(`<b>本次未能生成调仓建议</b><br>` + html.EscapeString(p.PlanError))
		b.WriteString(`</div>`)
	}

	writeActions(b, p)
	if len(p.Holdings) > 0 {
		writeHoldings(b, p.Holdings)
	}
	if len(p.Candidates) > 0 {
		writeCandidates(b, p.Candidates)
	}
	if len(p.Notes) > 0 {
		writeNotes(b, p.Notes)
	}

	b.WriteString(`</td></tr>`)
}

func writeSummaryCell(b *strings.Builder, label, value, color string) {
	b.WriteString(`<td style="padding:0 6px 0 0;vertical-align:top;">`)
	b.WriteString(`<div style="padding:8px 10px;background:#f8f9fb;border-radius:6px;">`)
	b.WriteString(`<div style="font-size:10px;color:#6b7280;white-space:nowrap;">` +
		html.EscapeString(label) + `</div>`)
	b.WriteString(`<div style="margin-top:2px;font-size:14px;font-weight:600;color:` + color +
		`;white-space:nowrap;">` + html.EscapeString(value) + `</div>`)
	b.WriteString(`</div></td>`)
}

func writeActions(b *strings.Builder, p PortfolioDigest) {
	b.WriteString(`<div style="margin-top:16px;font-size:13px;font-weight:600;">调仓动作`)
	if len(p.Actions) == 0 {
		b.WriteString(`<span style="font-weight:400;color:#6b7280;font-size:11px;">（今天没有动作）</span>`)
	} else {
		executable := 0
		for _, a := range p.Actions {
			if a.Executable {
				executable++
			}
		}
		b.WriteString(fmt.Sprintf(
			`<span style="font-weight:400;color:#6b7280;font-size:11px;">（%d 条，其中 %d 条可在界面上一键应用）</span>`,
			len(p.Actions), executable))
	}
	b.WriteString(`</div>`)

	if len(p.Actions) == 0 {
		if len(p.NoActionReasons) > 0 {
			b.WriteString(`<ul style="margin:8px 0 0 0;padding-left:18px;font-size:12px;color:#4b5563;">`)
			for _, reason := range p.NoActionReasons {
				b.WriteString(`<li style="margin:2px 0;">` + html.EscapeString(reason) + `</li>`)
			}
			b.WriteString(`</ul>`)
		}
		return
	}

	b.WriteString(tableOpen())
	b.WriteString(`<thead><tr>`)
	writeTH(b, "动作", "left")
	writeTH(b, "代码", "left")
	writeTH(b, "名称", "left")
	writeTH(b, "评分", "right")
	writeTH(b, "分差", "right")
	writeTH(b, "候选", "left")
	writeTH(b, "层数", "right")
	writeTH(b, "原因", "left")
	b.WriteString(`</tr></thead><tbody>`)

	for _, a := range p.Actions {
		b.WriteString(`<tr>`)
		writeTD(b, actionBadge(a), "left", "")
		writeTD(b, `<span style="font-family:Consolas,Monaco,monospace;">`+
			html.EscapeString(a.Code)+`</span>`, "left", "")
		writeTD(b, html.EscapeString(orDash(a.Name)), "left", "")
		writeTD(b, formatNum(a.Score, 2), "right", "")
		if a.ScoreGap == 0 {
			writeTD(b, "—", "right", "#9ca3af")
		} else {
			writeTD(b, formatSigned(a.ScoreGap), "right", trendColor(a.ScoreGap))
		}
		candidate := "—"
		if a.Candidate != "" {
			candidate = `<span style="font-family:Consolas,Monaco,monospace;">` +
				html.EscapeString(a.Candidate) + `</span>`
			if a.CandidateNme != "" {
				candidate += ` <span style="color:#6b7280;">` + html.EscapeString(a.CandidateNme) + `</span>`
			}
		}
		writeTD(b, candidate, "left", "")
		writeTD(b, html.EscapeString(a.LayersText), "right", "")
		writeTD(b, html.EscapeString(a.Reason), "left", "#6b7280")
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
}

// actionBadge 把动作渲染成一个带底色的小标签。
//
// 颜色按语义分三档：清仓/换出用红、买入/换入/加仓用蓝绿、减仓用橙、
// 其余（继续持有 / 观察）用灰。**与界面上的 Badge 变体一一对应**，
// 这样用户在邮件和界面里看到的是同一套颜色语言。
func actionBadge(a ActionDigest) string {
	bg, fg := "#f3f4f6", "#4b5563"
	switch a.Action {
	case "clear", "replace_out":
		bg, fg = "#fee2e2", "#b91c1c"
	case "add", "add_layer", "replace_in":
		bg, fg = "#dcfce7", "#15803d"
	case "reduce":
		bg, fg = "#fef3c7", "#b45309"
	}
	label := a.ActionLabel
	if label == "" {
		label = a.Action
	}
	if a.ActionPrefix != "" {
		label = a.ActionPrefix + "·" + label
	}
	suffix := ""
	if a.Executable {
		if a.RequiresConfirmation {
			suffix = `<span style="color:#6b7280;font-size:10px;"> 需确认</span>`
		}
	}
	return `<span style="display:inline-block;padding:1px 6px;border-radius:4px;font-size:11px;` +
		`background:` + bg + `;color:` + fg + `;white-space:nowrap;">` +
		html.EscapeString(label) + `</span>` + suffix
}

func writeHoldings(b *strings.Builder, holdings []HoldingDigest) {
	b.WriteString(`<div style="margin-top:16px;font-size:13px;font-weight:600;">当前持仓` +
		fmt.Sprintf(`<span style="font-weight:400;color:#6b7280;font-size:11px;">（%d 只）</span>`,
			len(holdings)) + `</div>`)
	b.WriteString(tableOpen())
	b.WriteString(`<thead><tr>`)
	writeTH(b, "代码", "left")
	writeTH(b, "名称", "left")
	writeTH(b, "层数", "right")
	writeTH(b, "权重", "right")
	writeTH(b, "成本", "right")
	writeTH(b, "现价", "right")
	writeTH(b, "浮动盈亏", "right")
	writeTH(b, "建仓日", "left")
	b.WriteString(`</tr></thead><tbody>`)
	for _, h := range holdings {
		b.WriteString(`<tr>`)
		writeTD(b, `<span style="font-family:Consolas,Monaco,monospace;">`+
			html.EscapeString(h.Code)+`</span>`, "left", "")
		name := html.EscapeString(orDash(h.Name))
		if h.HoldingType != "" {
			name += ` <span style="color:#6b7280;font-size:10px;">` + html.EscapeString(h.HoldingType) + `</span>`
		}
		writeTD(b, name, "left", "")
		writeTD(b, strconv.Itoa(h.Layers), "right", "")
		writeTD(b, formatNum(h.WeightPct, 2)+"%", "right", "")
		writeTD(b, formatNum(h.EntryPrice, 2), "right", "")
		if h.CurrentPrice > 0 {
			writeTD(b, formatNum(h.CurrentPrice, 2), "right", "")
			writeTD(b, formatPct(h.ProfitLossPct), "right", trendColor(h.ProfitLossPct))
		} else {
			// 取不到现价时**显示 — 而不是 0%** —— 0% 会被读成"平盘"
			writeTD(b, "—", "right", "#9ca3af")
			writeTD(b, "—", "right", "#9ca3af")
		}
		writeTD(b, displayDate(h.EntryDate), "left", "#6b7280")
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
}

func writeCandidates(b *strings.Builder, candidates []CandidateDigest) {
	b.WriteString(`<div style="margin-top:16px;font-size:13px;font-weight:600;">候选池 Top ` +
		strconv.Itoa(len(candidates)) + `</div>`)
	b.WriteString(tableOpen())
	b.WriteString(`<thead><tr>`)
	writeTH(b, "代码", "left")
	writeTH(b, "名称", "left")
	writeTH(b, "行业", "left")
	writeTH(b, "总分", "right")
	writeTH(b, "现价", "right")
	writeTH(b, "RS20", "right")
	writeTH(b, "120日回撤", "right")
	b.WriteString(`</tr></thead><tbody>`)
	for _, c := range candidates {
		b.WriteString(`<tr>`)
		writeTD(b, `<span style="font-family:Consolas,Monaco,monospace;">`+
			html.EscapeString(c.Code)+`</span>`, "left", "")
		writeTD(b, html.EscapeString(orDash(c.Name)), "left", "")
		writeTD(b, html.EscapeString(orDash(c.Industry)), "left", "#6b7280")
		writeTD(b, formatNum(c.TotalScore, 2), "right", "")
		writeTD(b, formatNum(c.Close, 2), "right", "")
		writeTD(b, formatSigned(c.RelativeStrength20), "right", trendColor(c.RelativeStrength20))
		writeTD(b, formatNum(c.DrawdownFromHigh120, 2)+"%", "right",
			trendColor(c.DrawdownFromHigh120))
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
}

func writeNotes(b *strings.Builder, notes []string) {
	b.WriteString(`<ul style="margin:10px 0 0 0;padding-left:18px;font-size:11px;color:#6b7280;">`)
	for _, note := range notes {
		b.WriteString(`<li style="margin:2px 0;">` + html.EscapeString(note) + `</li>`)
	}
	b.WriteString(`</ul>`)
}

// writeFooter 写页脚：**口径说明必须带上**。
//
// 这封邮件会被转发、会被隔几天再翻出来看，而"收益率不含现金""数量是层数"
// 这两条是最容易被误读的地方 —— 界面上写了，邮件里也必须写。
func writeFooter(b *strings.Builder, d Digest) {
	b.WriteString(`<tr><td style="padding:16px 24px 22px 24px;border-top:1px solid #e3e6ea;">`)
	if len(d.Notes) > 0 {
		b.WriteString(`<div style="font-size:12px;font-weight:600;color:#374151;">说明</div>`)
		b.WriteString(`<ul style="margin:6px 0 0 0;padding-left:18px;font-size:11px;color:#6b7280;">`)
		for _, note := range d.Notes {
			b.WriteString(`<li style="margin:2px 0;">` + html.EscapeString(note) + `</li>`)
		}
		b.WriteString(`</ul>`)
	}

	b.WriteString(`<ul style="margin:10px 0 0 0;padding-left:18px;font-size:11px;color:#6b7280;">`)
	b.WriteString(`<li style="margin:2px 0;">数量口径是<b>层数</b>，不是股数：1 层 = 总资金的 1/总层数</li>`)
	b.WriteString(`<li style="margin:2px 0;">组合收益率是<b>各持仓收益按计划权重加权求和</b>，不含未投入的现金</li>`)
	b.WriteString(`<li style="margin:2px 0;">权重是<b>计划权重</b>（层数 ÷ 总层数），不是市值权重</li>`)
	b.WriteString(`<li style="margin:2px 0;">调仓建议由本地策略引擎计算，<b>不构成投资建议</b></li>`)
	b.WriteString(`</ul>`)

	b.WriteString(`<div style="margin-top:10px;font-size:11px;color:#9ca3af;">` +
		`本邮件由 FreeStock 自动发送` +
		(bsOrDash(d.TradeDate)) + `</div>`)
	b.WriteString(`</td></tr>`)
}

func bsOrDash(tradeDate string) string {
	if tradeDate == "" {
		return ""
	}
	return " · 评分日 " + displayDate(tradeDate)
}

// ---------------------------------------------------------------------------
// 表格元件
// ---------------------------------------------------------------------------

func tableOpen() string {
	return `<table role="presentation" cellpadding="0" cellspacing="0" border="0" ` +
		`style="width:100%;margin-top:8px;border-collapse:collapse;font-size:12px;">`
}

func writeTH(b *strings.Builder, label, align string) {
	b.WriteString(`<th style="padding:6px 8px;text-align:` + align +
		`;font-weight:600;font-size:11px;color:#6b7280;background:#f8f9fb;` +
		`border-bottom:1px solid #e3e6ea;white-space:nowrap;">` +
		html.EscapeString(label) + `</th>`)
}

func writeTD(b *strings.Builder, content, align, color string) {
	style := `padding:6px 8px;text-align:` + align + `;border-bottom:1px solid #eef0f3;vertical-align:top;`
	if color != "" {
		style += `color:` + color + `;`
	}
	b.WriteString(`<td style="` + style + `">` + content + `</td>`)
}

// ---------------------------------------------------------------------------
// 格式化小工具（**与前端 lib/format.ts 的口径保持一致**）
// ---------------------------------------------------------------------------

// trendColor 返回红涨绿跌的颜色（A 股习惯，与界面一致）。
func trendColor(value float64) string {
	switch {
	case value > 0:
		return "#dc2626"
	case value < 0:
		return "#16a34a"
	default:
		return "#6b7280"
	}
}

func formatNum(value float64, digits int) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', digits, 64)
}

func formatSigned(value float64) string {
	text := formatNum(value, 2)
	if value > 0 {
		return "+" + text
	}
	return text
}

func formatPct(value float64) string {
	text := strconv.FormatFloat(value, 'f', 2, 64)
	if value > 0 {
		return "+" + text + "%"
	}
	return text + "%"
}

func scoreSuffix(score float64) string {
	if score == 0 {
		return ""
	}
	return "（评分 " + formatNum(score, 1) + "）"
}

func healthText(label string, score float64) string {
	if strings.TrimSpace(label) == "" {
		return "—"
	}
	if score == 0 {
		return label
	}
	return label + " " + formatNum(score, 1)
}

// drawdownText 渲染最大回撤；没有持仓就没有回撤数据，显示 — 而不是 0.00%。
func drawdownText(value float64, positionCount int) string {
	if positionCount == 0 {
		return "—"
	}
	return formatNum(value, 2) + "%"
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// actionTitle 是纯文本正文里的动作标签（HTML 版用 actionBadge）。
func actionTitle(a ActionDigest) string {
	label := a.ActionLabel
	if label == "" {
		label = a.Action
	}
	if a.ActionPrefix != "" {
		return "[" + a.ActionPrefix + "·" + label + "]"
	}
	return "[" + label + "]"
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "  " + reason
}

// displayDate 把 YYYYMMDD 显示成 YYYY-MM-DD（非 8 位原样返回）。
func displayDate(value string) string {
	text := strings.TrimSpace(value)
	if len(text) != 8 {
		return orDash(text)
	}
	if _, err := strconv.Atoi(text); err != nil {
		return text
	}
	return text[:4] + "-" + text[4:6] + "-" + text[6:]
}

// SortActions 按"该先做什么"排序：先清仓/换出，再减仓，再买入/换入/加仓，
// 最后是继续持有与观察。
//
// 排序放在渲染之前而不是依赖上游顺序：邮件是"待办清单"的读法，
// 按代码或评分排都会让用户自己去挑重点。
func SortActions(actions []ActionDigest) {
	rank := map[string]int{
		"clear": 0, "replace_out": 1, "reduce": 2,
		"replace_in": 3, "add": 4, "add_layer": 5,
	}
	sort.SliceStable(actions, func(i, j int) bool {
		ri, okI := rank[actions[i].Action]
		rj, okJ := rank[actions[j].Action]
		if !okI {
			ri = 9
		}
		if !okJ {
			rj = 9
		}
		if ri != rj {
			return ri < rj
		}
		return actions[i].Code < actions[j].Code
	})
}
