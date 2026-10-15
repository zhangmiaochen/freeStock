package service

import (
	"errors"
	"strings"
	"testing"

	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// ---------------------------------------------------------------------------
// 发送前刷新评分
//
// 这一块要钉住的是**顺序**与**范围**两件事：
//   - 顺序：必须"先刷新、再拼内容"，否则刷了也没用上；
//   - 范围：只刷这封邮件用到的策略（去重），不白花时间去刷无关策略。
// ---------------------------------------------------------------------------

// fakeRefresher 记账用的假刷分实现。
type fakeRefresher struct {
	calls []struct {
		StrategyID string
		Limit      int
		Force      bool
	}
	// 按策略定制返回；没配的用默认（成功、非缓存）
	results map[string]*RefreshResult
	errs    map[string]error
}

func newFakeRefresher() *fakeRefresher {
	return &fakeRefresher{
		results: map[string]*RefreshResult{},
		errs:    map[string]error{},
	}
}

func (f *fakeRefresher) fn(strategyID string, limit int, force bool) (*RefreshResult, error) {
	f.calls = append(f.calls, struct {
		StrategyID string
		Limit      int
		Force      bool
	}{strategyID, limit, force})
	if err := f.errs[strategyID]; err != nil {
		return nil, err
	}
	if result, ok := f.results[strategyID]; ok {
		return result, nil
	}
	return &RefreshResult{TradeDate: 20261015, Saved: 10, Total: 100}, nil
}

func portfolioWith(id, strategy string) portfolio.Portfolio {
	return portfolio.Portfolio{ID: id, Name: id, StrategyID: strategy, Status: portfolio.StatusActive}
}

// TestRefreshScoresBeforeSendDedupesStrategies 同一个策略用在不同组合上时只刷一次。
//
// 不刷重复的很重要：全市场刷一次是几十秒，重复刷就是白等。
func TestRefreshScoresBeforeSendDedupesStrategies(t *testing.T) {
	fake := newFakeRefresher()
	prefs := config.DefaultEmailPrefs()

	selected := []portfolio.Portfolio{
		portfolioWith("a", score.StrategyTrendGrowth),
		portfolioWith("b", score.StrategyTrendGrowth), // 同一个策略
		portfolioWith("c", score.StrategyOversold),
	}

	notes := refreshScoresBeforeSend(selected, prefs, fake.fn)

	if len(fake.calls) != 2 {
		t.Fatalf("应当只刷 2 个去重后的策略，实际 %d 次：%+v", len(fake.calls), fake.calls)
	}
	got := []string{fake.calls[0].StrategyID, fake.calls[1].StrategyID}
	if got[0] != score.StrategyTrendGrowth || got[1] != score.StrategyOversold {
		t.Errorf("刷的策略不对：%v", got)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "已刷新评分") {
		t.Errorf("应当在说明里写明刷了新分，实际：%v", notes)
	}
}

// TestRefreshScoresBeforeSendUsesIncrementalAndLimit 必须走**增量**，并把只数上限传下去。
//
// force=true 会把当天已算过的全部重算一遍（全市场几分钟），
// 定时任务里绝不能这样。
func TestRefreshScoresBeforeSendUsesIncrementalAndLimit(t *testing.T) {
	fake := newFakeRefresher()
	prefs := config.DefaultEmailPrefs()
	prefs.ScoreLimit = 800

	refreshScoresBeforeSend([]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)}, prefs, fake.fn)

	if len(fake.calls) != 1 {
		t.Fatalf("应当刷 1 次，实际 %d", len(fake.calls))
	}
	if fake.calls[0].Force {
		t.Error("必须走增量（force=false）—— force=true 会把当天已算过的全部重算")
	}
	if fake.calls[0].Limit != 800 {
		t.Errorf("只数上限没传下去：%d", fake.calls[0].Limit)
	}
}

// TestRefreshScoresBeforeSendDefaultsToWholeMarket 默认全市场（limit=0）。
//
// 广度闸门按全市场统计，只评一部分会让它被系统性低估、判成"广度不足"。
func TestRefreshScoresBeforeSendDefaultsToWholeMarket(t *testing.T) {
	fake := newFakeRefresher()
	prefs := config.DefaultEmailPrefs()

	refreshScoresBeforeSend([]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)}, prefs, fake.fn)

	if prefs.ScoreLimit != 0 {
		t.Fatalf("默认 ScoreLimit 应当是 0（全市场），实际 %d", prefs.ScoreLimit)
	}
	if fake.calls[0].Limit != 0 {
		t.Errorf("应当把 0（全市场）传下去，实际 %d", fake.calls[0].Limit)
	}
}

// TestRefreshScoresBeforeSendCanBeDisabled 关掉开关就一次都不刷。
func TestRefreshScoresBeforeSendCanBeDisabled(t *testing.T) {
	fake := newFakeRefresher()
	prefs := config.DefaultEmailPrefs()
	prefs.RefreshScoresBeforeSend = false

	notes := refreshScoresBeforeSend(
		[]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)}, prefs, fake.fn)

	if len(fake.calls) != 0 {
		t.Errorf("关掉开关时不该刷评分，实际刷了 %d 次", len(fake.calls))
	}
	if len(notes) != 0 {
		t.Errorf("关掉开关时不该产生说明，实际：%v", notes)
	}
}

// TestRefreshScoresBeforeSendFailureDoesNotAbort 一个策略失败不影响其它策略，
// 也不影响发送（调用方只看 notes）。
//
// 这条很关键：数据源没启动时刷新会失败，那时"照发 + 写明原因"比"什么都不发"有用。
func TestRefreshScoresBeforeSendFailureDoesNotAbort(t *testing.T) {
	fake := newFakeRefresher()
	fake.errs[score.StrategyOversold] = errors.New("数据源没启动")
	prefs := config.DefaultEmailPrefs()

	selected := []portfolio.Portfolio{
		portfolioWith("a", score.StrategyTrendGrowth),
		portfolioWith("b", score.StrategyOversold),
	}
	notes := refreshScoresBeforeSend(selected, prefs, fake.fn)

	if len(fake.calls) != 2 {
		t.Fatalf("失败的策略不该让后面的策略被跳过，实际只刷了 %d 次", len(fake.calls))
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "评分刷新失败") {
		t.Errorf("失败原因应当写进说明，实际：%v", notes)
	}
	if !strings.Contains(joined, "数据源没启动") {
		t.Errorf("说明里应当带上原始错误，实际：%v", notes)
	}
	if !strings.Contains(joined, "已刷新评分") {
		t.Errorf("成功的那个也应当计入，实际：%v", notes)
	}
}

// TestRefreshScoresBeforeSendAllFailedIsCalledOut 全部失败时要**明说**，
// 而不是给一句"已刷新"让人以为数字是新的。
func TestRefreshScoresBeforeSendAllFailedIsCalledOut(t *testing.T) {
	fake := newFakeRefresher()
	fake.errs[score.StrategyTrendGrowth] = errors.New("boom")
	prefs := config.DefaultEmailPrefs()

	notes := refreshScoresBeforeSend(
		[]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)}, prefs, fake.fn)

	joined := strings.Join(notes, " | ")
	if strings.Contains(joined, "已刷新评分") {
		t.Errorf("全失败时不该说「已刷新」，实际：%v", notes)
	}
	if !strings.Contains(joined, "都没成功") && !strings.Contains(joined, "可能基于旧评分") {
		t.Errorf("全失败时必须写明建议可能基于旧评分，实际：%v", notes)
	}
}

// TestRefreshScoresBeforeSendNotesCacheHit 当天已刷过时也要给一句说明。
//
// 否则用户看到"秒回"会以为根本没刷。
func TestRefreshScoresBeforeSendNotesCacheHit(t *testing.T) {
	fake := newFakeRefresher()
	fake.results[score.StrategyTrendGrowth] = &RefreshResult{
		TradeDate: 20261015, Reused: 100, Total: 100, SkippedFromCache: true,
	}
	prefs := config.DefaultEmailPrefs()

	notes := refreshScoresBeforeSend(
		[]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)}, prefs, fake.fn)

	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "已刷新评分") {
		t.Errorf("命中缓存也算刷过，应当有说明，实际：%v", notes)
	}
}

// TestRefreshScoresBeforeSendNoPortfolios 没有组合时不刷、也不产出说明。
func TestRefreshScoresBeforeSendNoPortfolios(t *testing.T) {
	fake := newFakeRefresher()
	if notes := refreshScoresBeforeSend(nil, config.DefaultEmailPrefs(), fake.fn); len(notes) != 0 {
		t.Errorf("没有组合时不该有说明：%v", notes)
	}
	if len(fake.calls) != 0 {
		t.Errorf("没有组合时不该刷评分")
	}
}

// TestRefreshScoresBeforeSendScoreLimitText 说明文案要把"全市场 / 前 N 只"说清楚。
func TestRefreshScoresBeforeSendScoreLimitText(t *testing.T) {
	if got := scoreLimitText(0); got != "全部标的" {
		t.Errorf("scoreLimitText(0) = %q", got)
	}
	if got := scoreLimitText(800); got != "前 800 只" {
		t.Errorf("scoreLimitText(800) = %q", got)
	}
}

// TestEmailPrefsDefaultsRefreshOn 默认**开启**先刷评分。
//
// 不刷的话发出去的可能是一封"准时但过期"的邮件，而收件人看不出来。
func TestEmailPrefsDefaultsRefreshOn(t *testing.T) {
	def := config.DefaultEmailPrefs()
	if !def.RefreshScoresBeforeSend {
		t.Error("默认应当开启「发送前先刷新评分」")
	}
	if def.ScoreLimit != 0 {
		t.Errorf("默认 ScoreLimit 应当是 0（全市场），实际 %d", def.ScoreLimit)
	}

	// ⚠ ScoreLimit=0 是**合法值**（全市场），Sanitize 不能把它当"没填"回落到别的值
	sanitized := config.EmailPrefs{ScoreLimit: 0}.Sanitize()
	if sanitized.ScoreLimit != 0 {
		t.Errorf("Sanitize 把 0 改掉了：%d —— 0 表示全市场，是合法值", sanitized.ScoreLimit)
	}
	// 负数才收敛
	if got := (config.EmailPrefs{ScoreLimit: -5}).Sanitize().ScoreLimit; got != 0 {
		t.Errorf("负数应当收敛到 0，实际 %d", got)
	}
}

// TestRefreshScoresBeforeSendNilRefresher 注入 nil 时安静跳过（防御性）。
func TestRefreshScoresBeforeSendNilRefresher(t *testing.T) {
	notes := refreshScoresBeforeSend(
		[]portfolio.Portfolio{portfolioWith("a", score.StrategyTrendGrowth)},
		config.DefaultEmailPrefs(), nil)
	if len(notes) != 0 {
		t.Errorf("nil refresher 应当安静跳过，实际：%v", notes)
	}
}
