package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"freestock/internal/config"
	"freestock/internal/email"
)

// ---------------------------------------------------------------------------
// 到点判定与当天去重
//
// 这段逻辑的失败模式非常隐蔽：**少发**（那天一次都没发）或者**多发**
// （重启之后又发一遍）。两种都不会报错，只有收件人会觉得奇怪，
// 所以这里把边界逐个钉住。
// ---------------------------------------------------------------------------

func mustClock(t *testing.T, layout, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatalf("解析时间失败: %v", err)
	}
	return parsed
}

func TestDueForScheduleFiresInsideWindow(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "17:00"
	prefs.Weekdays = []int{1} // 周一

	// 2026-10-12 是周一
	due, key := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 17:00:00"))
	if !due {
		t.Fatal("恰好整点应当触发")
	}
	if key != "20261012 17:00" {
		t.Errorf("去重键 = %q", key)
	}

	// 窗口内（29 秒后）仍应触发 —— 轮询间隔 30 秒，错过这一秒就漏一天
	due, _ = dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 17:00:29"))
	if !due {
		t.Error("窗口内（17:00:29）也应当触发")
	}
}

func TestDueForScheduleSkipsOutsideWindow(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "17:00"
	prefs.Weekdays = nil // 每天

	cases := []string{
		"2026-10-12 16:59:59", // 还没到
		"2026-10-12 17:02:00", // 窗口（2 分钟）已过
		"2026-10-12 22:00:00", // 当天很晚才打开应用 → 不补发
	}
	for _, value := range cases {
		if due, _ := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", value)); due {
			t.Errorf("%s 不该触发", value)
		}
	}
}

// TestDueForScheduleWindowIsWideEnoughForPolling 窗口必须容得下
// 至少两次 30 秒轮询 —— 否则一次错过就是漏一天。
func TestDueForScheduleWindowIsWideEnoughForPolling(t *testing.T) {
	if emailSendWindow < 2*emailSchedulerInterval {
		t.Fatalf("窗口 %v 容不下两次轮询（间隔 %v）", emailSendWindow, emailSchedulerInterval)
	}

	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "17:00"
	prefs.Weekdays = nil

	// 17:00:01 与 17:00:31 都应当触发
	for _, value := range []string{"2026-10-12 17:00:01", "2026-10-12 17:00:31"} {
		if due, _ := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", value)); !due {
			t.Errorf("%s 应当触发（轮询落点）", value)
		}
	}
}

func TestDueForScheduleRespectsWeekdays(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "17:00"
	prefs.Weekdays = []int{1, 2, 3, 4, 5} // 工作日

	// 2026-10-12 周一 → 发；2026-10-17 周六 → 不发
	if due, _ := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 17:00:00")); !due {
		t.Error("周一应当触发")
	}
	if due, _ := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-17 17:00:00")); due {
		t.Error("周六不该触发（只勾了工作日）")
	}
}

func TestDueForScheduleDisabledNeverFires(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = false
	prefs.SendTime = "17:00"
	if due, _ := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 17:00:00")); due {
		t.Error("关闭时不该触发")
	}
}

func TestDueForScheduleRejectsBrokenClock(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "not-a-time"
	if due, _ := dueForSchedule(prefs, time.Now()); due {
		t.Error("时间格式非法时不该触发（也不该 panic）")
	}
}

// TestDueForScheduleKeyIncludesClockTime 改时间之后当天还能按新时间再发一次。
//
// 这是有意的：用户改发送时间，往往正因为上一次没发出去。
func TestDueForScheduleKeyIncludesClockTime(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.SendTime = "09:00"
	_, keyA := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 09:00:00"))

	prefs.SendTime = "17:00"
	_, keyB := dueForSchedule(prefs, mustClock(t, "2006-01-02 15:04:05", "2026-10-12 17:00:00"))

	if keyA == keyB {
		t.Fatalf("改时间后去重键应当变化，两者都是 %q", keyA)
	}
}

// TestSchedulerDoesNotStartWhenDisabled 默认关闭时**不启动后台协程**。
//
// 没配过邮件的用户机器上不该多一个每 30 秒醒一次的协程。
func TestSchedulerDoesNotStartWhenDisabled(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	// 默认 DefaultEmailPrefs().Enabled == false
	if service.emailPrefs().Enabled {
		t.Fatal("默认应当是关闭的，这个用例的前提不成立")
	}
	// 关着时 ServiceStartup 不该起协程
	_ = service.ServiceStartup(context.Background(), application.ServiceOptions{})
	service.emailSched.mu.Lock()
	running := service.emailSched.running
	service.emailSched.mu.Unlock()
	if running {
		t.Error("关闭状态下不该启动调度协程")
	}
}

// TestEnsureEmailSchedulerIsIdempotent 反复 ensure 只起一个协程，停一次要能停住。
func TestEnsureEmailSchedulerIsIdempotent(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	service.ensureEmailScheduler()
	service.ensureEmailScheduler()
	service.ensureEmailScheduler()

	service.emailSched.mu.Lock()
	running := service.emailSched.running
	service.emailSched.mu.Unlock()
	if !running {
		t.Fatal("ensure 之后应当处于运行状态")
	}

	service.stopEmailScheduler()
	// 停止是 close(stop) + stopOnce，重复调用不能 panic
	service.stopEmailScheduler()
}

// TestEnsureEmailSchedulerToleratesNil 没有调度器时不能 panic（测试里构造的零值服务）。
func TestEnsureEmailSchedulerToleratesNil(t *testing.T) {
	service := &PortfolioService{}
	service.ensureEmailScheduler()
	service.stopEmailScheduler()
	service.recordSend(nil)
	service.recordSendResult(false, "k", nil, "e")
	service.markSentKey("k")
}

// TestSchedulerBeginBlocksReentry 发送闸门必须能挡住重入。
//
// ⚠ 为什么必须有它：发送前要先刷评分（首刷全市场几十秒），比 30 秒的轮询间隔长。
// 没有闸门的话第二次轮询会在上一次还没发完时又进来 —— **刷两遍、发两封**。
func TestSchedulerBeginBlocksReentry(t *testing.T) {
	sched := newEmailScheduler()

	release, ok := sched.begin()
	if !ok {
		t.Fatal("第一次应当拿得到闸门")
	}

	if _, again := sched.begin(); again {
		t.Error("上一次还没放行时，第二次不该拿到闸门")
	}

	release()

	release2, ok := sched.begin()
	if !ok {
		t.Fatal("放行之后应当又能拿到闸门")
	}
	release2()
}

// TestSchedulerBeginReleaseIsIdempotent 重复放行不能把状态弄反。
//
// 放行函数会被 defer 调用；重复放行最多多解锁一次，
// 不能变成"闸门被别人关上"（那会导致之后再也不发送）。
func TestSchedulerBeginReleaseIsIdempotent(t *testing.T) {
	sched := newEmailScheduler()
	release, _ := sched.begin()
	release()
	release() // 再放一次不该出问题

	release3, ok := sched.begin()
	if !ok {
		t.Fatal("重复放行之后仍应能拿到闸门")
	}
	release3()

	sched.mu.Lock()
	sending := sched.sending
	sched.mu.Unlock()
	if sending {
		t.Error("最终不该停在「正在发送」状态")
	}
}

// TestSchedulerBeginConcurrent 并发调用只能有一个拿到闸门。
//
// 调度器是单个协程，但"手动立即发送"与轮询可能同时进来，所以按并发验证。
func TestSchedulerBeginConcurrent(t *testing.T) {
	sched := newEmailScheduler()

	const workers = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
		release func()
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, ok := sched.begin()
			if !ok {
				return
			}
			mu.Lock()
			granted++
			release = done
			mu.Unlock()
		}()
	}
	wg.Wait()

	if granted != 1 {
		t.Errorf("并发下应当恰好一个拿到闸门，实际 %d 个", granted)
	}
	if release != nil {
		release()
	}
}

// TestRecordSendResultKeepsFailedKeyEmpty 失败的发送**不写去重键**，
// 否则配置修好之后当天再也不会重试。
func TestRecordSendResultKeepsFailedKeyEmpty(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	service.recordSendResult(false, "20261012 17:00", []string{"a@x.com"}, "认证失败")

	service.emailSched.mu.Lock()
	key := service.emailSched.lastSentKey
	status := service.emailSched.lastStatus
	errText := service.emailSched.lastError
	recipients := append([]string(nil), service.emailSched.lastRecipients...)
	service.emailSched.mu.Unlock()

	if key != "" {
		t.Errorf("失败的发送不该占用去重键，实际 %q", key)
	}
	if status != "失败" {
		t.Errorf("状态 = %q", status)
	}
	if !strings.Contains(errText, "认证失败") {
		t.Errorf("失败原因未记录: %q", errText)
	}
	if len(recipients) != 1 {
		t.Errorf("失败时也应记下尝试过的收件人: %v", recipients)
	}
}

// TestRecordSendMarksSuccess 成功后状态与去重键都要写上。
func TestRecordSendMarksSuccess(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	service.recordSend(&EmailSendResult{
		Sent:       true,
		Recipients: []string{"a@x.com", "b@x.com"},
		Portfolios: []string{"主力组合"},
	})
	service.markSentKey("20261012 17:00")

	service.emailSched.mu.Lock()
	defer service.emailSched.mu.Unlock()
	if service.emailSched.lastStatus != "成功" {
		t.Errorf("状态 = %q", service.emailSched.lastStatus)
	}
	if service.emailSched.lastError != "" {
		t.Errorf("成功后不该留着错误: %q", service.emailSched.lastError)
	}
	if len(service.emailSched.lastRecipients) != 2 {
		t.Errorf("收件人未记录: %v", service.emailSched.lastRecipients)
	}
	if service.emailSched.lastSentKey != "20261012 17:00" {
		t.Errorf("去重键未写入: %q", service.emailSched.lastSentKey)
	}
	if service.emailSched.lastSentAt.IsZero() {
		t.Error("发送时间未记录")
	}
}

// ---------------------------------------------------------------------------
// 下一次发送时间
// ---------------------------------------------------------------------------

func TestNextSendTimeToday(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.SendTime = "17:00"
	prefs.Weekdays = []int{1, 2, 3, 4, 5}

	now := mustClock(t, "2006-01-02 15:04:05", "2026-10-12 09:00:00") // 周一早上
	next, hint := nextSendTime(prefs, now)
	if next.Format("2006-01-02 15:04") != "2026-10-12 17:00" {
		t.Errorf("下一次 = %s", next.Format("2006-01-02 15:04"))
	}
	if !strings.Contains(hint, "今天") {
		t.Errorf("提示 = %q", hint)
	}
}

func TestNextSendTimeRollsToNextWeekday(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.SendTime = "17:00"
	prefs.Weekdays = []int{1, 2, 3, 4, 5}

	// 周五 17:30 已经过了发送时刻 → 应当落到下周一
	now := mustClock(t, "2006-01-02 15:04:05", "2026-10-16 17:30:00")
	next, _ := nextSendTime(prefs, now)
	if next.Weekday() != time.Monday {
		t.Errorf("下一次应当是周一，实际 %s", next.Weekday())
	}
	if next.Format("2006-01-02") != "2026-10-19" {
		t.Errorf("下一次日期 = %s", next.Format("2006-01-02"))
	}
}

func TestNextSendTimeEveryDayWhenWeekdaysEmpty(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.SendTime = "17:00"
	prefs.Weekdays = nil

	now := mustClock(t, "2006-01-02 15:04:05", "2026-10-16 17:30:00") // 周五
	next, _ := nextSendTime(prefs, now)
	if next.Format("2006-01-02") != "2026-10-17" {
		t.Errorf("空星期应每天发，下一次 = %s", next.Format("2006-01-02"))
	}
}

func TestNextSendTimeReportsBrokenClock(t *testing.T) {
	prefs := config.DefaultEmailPrefs()
	prefs.SendTime = "77:00"
	next, hint := nextSendTime(prefs, time.Now())
	if !next.IsZero() {
		t.Error("非法时间不该算出下一次")
	}
	if !strings.Contains(hint, "不合法") {
		t.Errorf("应当说明时间格式不合法，实际 %q", hint)
	}
}

// ---------------------------------------------------------------------------
// 其他
// ---------------------------------------------------------------------------

func TestParseClock(t *testing.T) {
	cases := []struct {
		value    string
		hour     int
		minute   int
		expected bool
	}{
		{"17:00", 17, 0, true},
		{"7:5", 7, 5, true},
		{"00:00", 0, 0, true},
		{"23:59", 23, 59, true},
		{"24:00", 0, 0, false},
		{"17:60", 0, 0, false},
		{"1700", 0, 0, false},
		{"", 0, 0, false},
		{"17:00:00", 0, 0, false},
	}
	for _, item := range cases {
		hour, minute, ok := parseClock(item.value)
		if ok != item.expected {
			t.Errorf("parseClock(%q) ok = %v，期望 %v", item.value, ok, item.expected)
			continue
		}
		if ok && (hour != item.hour || minute != item.minute) {
			t.Errorf("parseClock(%q) = %d:%d", item.value, hour, minute)
		}
	}
}

func TestWeekdayText(t *testing.T) {
	if got := weekdayText(nil); got != "每天" {
		t.Errorf("weekdayText(nil) = %q", got)
	}
	if got := weekdayText([]int{1, 2, 3, 4, 5}); got != "周一至周五" {
		t.Errorf("weekdayText(工作日) = %q", got)
	}
	if got := weekdayText([]int{1, 3}); got != "周一、周三" {
		t.Errorf("weekdayText([1,3]) = %q", got)
	}
}

// TestRenderSubjectPlaceholders 主题里的占位符要展开成人能读的内容。
func TestRenderSubjectPlaceholders(t *testing.T) {
	digest := &email.Digest{
		TradeDate: "20261013",
		Portfolios: []email.PortfolioDigest{
			{Name: "主力组合"}, {Name: "卫星组合"},
		},
	}

	got := renderSubject("【FreeStock】{date} 调仓日报 · {portfolio}（{count}）", digest)
	if !strings.Contains(got, "2026-10-13") {
		t.Errorf("日期未展开: %q", got)
	}
	if !strings.Contains(got, "主力组合 等 2 个组合") {
		t.Errorf("多组合时的名称未展开: %q", got)
	}
	if !strings.Contains(got, "（2）") {
		t.Errorf("组合数未展开: %q", got)
	}

	// 单个组合时直接写名字，不带"等 N 个"
	single := &email.Digest{
		TradeDate:  "20261013",
		Portfolios: []email.PortfolioDigest{{Name: "主力组合"}},
	}
	if got := renderSubject("{portfolio}", single); got != "主力组合" {
		t.Errorf("单组合主题 = %q", got)
	}

	// 没有评分日时用今天兜底，不能留一个空占位
	empty := &email.Digest{}
	got = renderSubject("{date}", empty)
	if strings.Contains(got, "{date}") || got == "" {
		t.Errorf("没有评分日时应当兜底成今天: %q", got)
	}

	// 模板为空时要有默认主题，不能发一封没有主题的邮件
	if got := renderSubject("   ", digest); got == "" {
		t.Error("空模板应当回落成默认主题")
	}
}

// TestLatestTradeDatePicksNewest 各组合的评分日可能不同，取最新的那个。
func TestLatestTradeDatePicksNewest(t *testing.T) {
	portfolios := []email.PortfolioDigest{
		{TradeDate: "20261009"},
		{TradeDate: ""},
		{TradeDate: "20261013"},
		{TradeDate: "20261010"},
	}
	if got := latestTradeDate(portfolios); got != "20261013" {
		t.Errorf("latestTradeDate = %q", got)
	}
	if got := latestTradeDate(nil); got != "" {
		t.Errorf("空列表应当返回空串，实际 %q", got)
	}
}

// TestLayersText 层数变化：无变化只给一个数，变化时给 "a → b"。
func TestLayersText(t *testing.T) {
	if got := layersText(2, 2); got != "2" {
		t.Errorf("layersText(2,2) = %q", got)
	}
	if got := layersText(2, 0); got != "2 → 0" {
		t.Errorf("layersText(2,0) = %q", got)
	}
}

// TestFromAddressFallsBackToUsername 发件人留空时用 SMTP 用户名。
//
// 用授权码登录时发件人**必须**是那个账号，让用户再抄一遍只会多一个写错的地方。
func TestFromAddressFallsBackToUsername(t *testing.T) {
	if got := fromAddress(config.EmailPrefs{Username: "me@qq.com"}); got != "me@qq.com" {
		t.Errorf("fromAddress = %q", got)
	}
	explicit := config.EmailPrefs{Username: "me@qq.com", From: "notice@qq.com"}
	if got := fromAddress(explicit); got != "notice@qq.com" {
		t.Errorf("显式 From 应当优先，实际 %q", got)
	}
}

// TestValidateEmailConfigReportsRecipients 配置不完整时要指出**具体**问题。
func TestValidateEmailConfigReportsRecipients(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}

	prefs := config.DefaultEmailPrefs()
	prefs.Host = "smtp.qq.com"
	prefs.Username = "me@qq.com"

	// 完全没有收件人
	if err := service.validateEmailConfig(prefs, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "收件人") {
		t.Errorf("应当提示没有收件人，实际: %v", err)
	}

	// 有非法收件人：要**点名**是哪一个
	err := service.validateEmailConfig(prefs, []string{"a@x.com"}, []string{"bad-address"})
	if err == nil || !strings.Contains(err.Error(), "bad-address") {
		t.Errorf("应当点名非法地址，实际: %v", err)
	}

	// 缺 SMTP 主机
	noHost := config.DefaultEmailPrefs()
	if err := service.validateEmailConfig(noHost, []string{"a@x.com"}, nil); err == nil {
		t.Error("缺 SMTP 主机应当报错")
	}
}

// TestValidateEmailConfigRequiresPortfolioForOneScope 选了「指定组合」却没选组合。
func TestValidateEmailConfigRequiresPortfolioForOneScope(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	prefs := config.DefaultEmailPrefs()
	prefs.Host = "smtp.qq.com"
	prefs.Username = "me@qq.com"
	prefs.Scope = config.EmailScopeOne

	err := service.validateEmailConfig(prefs, []string{"a@x.com"}, nil)
	if err == nil || !strings.Contains(err.Error(), "指定组合") {
		t.Errorf("应当提示没有选中组合，实际: %v", err)
	}
}

// TestEmailSchedulerInfoReportsDisabled 关闭时的说明要明确，别让用户猜。
func TestEmailSchedulerInfoReportsDisabled(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	info := service.emailSchedulerInfo()
	if info.Enabled {
		t.Error("默认应当是关闭的")
	}
	if !strings.Contains(info.NextRunHint, "关闭") {
		t.Errorf("关闭时的说明 = %q", info.NextRunHint)
	}
}

// TestEmailTransportMapsTimeout 超时设置要真的接进传输参数。
func TestEmailTransportMapsTimeout(t *testing.T) {
	service := &PortfolioService{emailSched: newEmailScheduler()}
	prefs := config.DefaultEmailPrefs()
	prefs.TimeoutSeconds = 45
	cfg := service.emailTransport(prefs)
	if cfg.Timeout.Seconds() != 45 {
		t.Errorf("超时 = %v", cfg.Timeout)
	}
	if cfg.Host != prefs.Host || cfg.Port != prefs.Port {
		t.Errorf("主机/端口未传递: %+v", cfg)
	}
}
