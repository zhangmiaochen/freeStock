package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"freestock/internal/config"
	"freestock/internal/email"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// ---------------------------------------------------------------------------
// 邮件服务：设置 / 预览 / 测试 / 立即发送 / 定时发送
//
// 放在 PortfolioService 上（而不是单独一个 service）是因为它的**全部输入**
// 都是组合数据：内容要靠 BuildPlan + GetPortfolioDetail 拼出来，
// 单独一个 service 反而要把这两条路径再引一遍。
// ---------------------------------------------------------------------------

// emailSchedulerInterval 是调度器的轮询间隔。
//
// 用"轮询 + 当天去重"而不是"算到下一次触发时刻再 sleep"：
// 后者在**休眠唤醒 / 系统改时间 / 手动改发送时间**之后会算出错误的下一次，
// 而多睡的那段时间是完全静默的。30 秒的轮询代价可以忽略，
// 且发送窗口是 1 分钟宽（见 dueForSchedule），不会漏。
const emailSchedulerInterval = 30 * time.Second

// emailSendWindow 是"到点"的判定窗口。
//
// ⚠ 必须是**一段窗口**而不是 `now == HH:MM`：轮询落在 17:00:29 这类时刻时，
// 严格相等整分钟只命中一次，那一次再被跳过（电脑休眠、编辑器占用）这一天就没了。
//
// 取 2 分钟而不是 1 分钟：轮询间隔是 30 秒，1 分钟宽只够两次机会；
// 2 分钟内必然有 4 次机会，且**不会**因此发两遍 —— 同一天的同一个
// 去重键只发一次（见 maybeSendScheduledEmail）。
//
// 代价要如实说明：窗口之外（比如应用直到 17:10 才启动）当天就不补发。
// 这是有意的 —— 到了晚上才补一封下午的日报没有意义，
// 而放宽到"整天窗口"会让"用户几点开应用就几点收到"。
const emailSendWindow = 2 * time.Minute

// EmailPrefsInfo 是给界面的邮件设置视图。
//
// ⚠ **不含授权码原文**。界面只需要知道"有没有存过" ——
// 把授权码回传给前端等于让它出现在 DOM 与开发者工具里，
// 而编辑时留空表示"不改"就够了（见 SaveEmailPrefs）。
type EmailPrefsInfo struct {
	Prefs config.EmailPrefs `json:"prefs"`
	// PasswordSet 表示已经存过授权码
	PasswordSet bool `json:"passwordSet"`
	// PasswordNote 是明文存储的提示，界面直接显示
	PasswordNote string `json:"passwordNote"`
	// ConfigPath 是配置文件路径（用户要备份/核对时用得上）
	ConfigPath string `json:"configPath"`
	// Recipients 是解析后的收件人列表（合法地址）
	Recipients []string `json:"recipients"`
	// InvalidRecipients 是解析失败的原文，界面要显示出来
	InvalidRecipients []string `json:"invalidRecipients"`
	// Schedule 是当前的调度状态
	Schedule EmailScheduleInfo `json:"schedule"`
	// Ready 表示现在能不能发（配置是否完整）
	Ready bool `json:"ready"`
	// ReadyError 是 Ready=false 的原因
	ReadyError string `json:"readyError"`
}

// EmailScheduleInfo 是调度器的状态快照。
//
// 界面上必须能看到"下次什么时候发"与"上次发的结果" —— 只有一个开关的话，
// 用户无法区分"功能开着但时间设错了"和"功能没生效"。
type EmailScheduleInfo struct {
	Enabled bool `json:"enabled"`
	// SendTime 是设置的发送时刻 HH:MM
	SendTime string `json:"sendTime"`
	// Weekdays 是发送的星期（0=周日）
	Weekdays []int `json:"weekdays"`
	// NextRun 是下一次计划发送时间，RFC3339；关闭或没有可发日时为空
	NextRun string `json:"nextRun"`
	// NextRunHint 是它的中文说明
	NextRunHint string `json:"nextRunHint"`
	// LastSentAt / LastStatus / LastError 是上一次发送的结果
	LastSentAt string `json:"lastSentAt"`
	LastStatus string `json:"lastStatus"`
	LastError  string `json:"lastError"`
	// LastRecipients 是上一次实际发出的收件人
	LastRecipients []string `json:"lastRecipients"`
	// Running 表示现在正在发
	Running bool `json:"running"`
}

// EmailSendResult 是一次发送的结果。
type EmailSendResult struct {
	Sent       bool     `json:"sent"`
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject"`
	Portfolios []string `json:"portfolios"`
	// HTML 是本次真正发出去的正文（立即发送时返回，便于用户核对）
	HTML     string   `json:"html"`
	Elapsed  string   `json:"elapsed"`
	SentAt   string   `json:"sentAt"`
	Warnings []string `json:"warnings"`
}

// EmailPreview 是邮件预览。
type EmailPreview struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
	// Portfolios 是本次渲染进正文的组合名
	Portfolios []string `json:"portfolios"`
	// Warnings 是渲染过程中的提示（某组合没有评分数据等）
	Warnings []string `json:"warnings"`
}

// PasswordNoteText 是授权码存储方式的固定说明文案。
const PasswordNoteText = "授权码以**明文**保存在 config.json 里（没有加密）。" +
	"加密密钥只能放在同一台机器的同一目录，挡不住能读到该文件的人，" +
	"却会让备份/迁移配置变得需要解释 —— 所以选择写明而不是假装加密。" +
	"建议使用邮箱的「SMTP 授权码」而不是登录密码，并给该邮箱单独设置。"

// emailScheduler 是组合服务的后台调度状态。
//
// 与 PortfolioService 分开放，是为了让"邮件"这件事的状态**不混进**
// 组合库的锁里：发送要跑 BuildPlan（可能几秒），不能占着 db() 的互斥锁。
type emailScheduler struct {
	mu       sync.Mutex
	stopOnce sync.Once
	stop     chan struct{}
	running  bool

	// tick 是每次醒来时执行的动作（由 NewPortfolioService 注入）。
	// 用回调而不是直接持有 PortfolioService，是为了让"到点判定 + 当天去重"
	// 这段逻辑可以脱离数据库单独测。
	tick emailTick

	// lastSentKey 是当天去重键（"20261013 17:00"），为空表示今天还没发过。
	// 失败的发送**不写这个键** —— 否则配置修好之后当天不会再重试。
	lastSentKey string

	// sending 表示"这一轮正在发送"。
	//
	// ⚠ 需要它是因为发送前要先刷评分（首刷全市场几十秒），比 30 秒的轮询间隔长：
	// 没有这道闸门的话，第二次轮询会在上一次还没发完时又进来，**刷两遍、发两封**。
	sending bool

	lastSentAt     time.Time
	lastStatus     string
	lastError      string
	lastRecipients []string
	lastPortfolios []string
}

// emailTick 是调度器醒来时的回调。
type emailTick func()

func newEmailScheduler() *emailScheduler {
	return &emailScheduler{stop: make(chan struct{})}
}

// begin 尝试进入"正在发送"状态。
//
// 返回 (放行函数, 是否拿到)。拿到 false 表示**上一次还没发完**，调用方应当直接返回。
//
// ⚠ 拿到 true 时**必须**调用放行函数（defer 它）。不放行的话闸门会一直关着，
// 后续所有轮询都被挡在外面 —— 表现是"发过一次之后就再也不发了"，
// 而且不会报任何错。
//
// 抽成方法只是为了可测：这段判断没有别的依赖。
func (s *emailScheduler) begin() (func(), bool) {
	s.mu.Lock()
	if s.sending {
		s.mu.Unlock()
		return func() {}, false
	}
	s.sending = true
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.sending = false
		s.mu.Unlock()
	}, true
}

// ---------------------------------------------------------------------------
// Wails 生命周期
// ---------------------------------------------------------------------------

// ServiceStartup 启动邮件调度器。
//
// ⚠ **只在 Enabled 时真正起协程**：默认关着，没配过的用户机器上
// 不该多一个每 30 秒醒一次的协程。用户后来打开了开关，
// 由 SaveEmailPrefs 负责按需启动（见 ensureEmailScheduler）。
func (s *PortfolioService) ServiceStartup(context.Context, application.ServiceOptions) error {
	prefs := s.emailPrefs()
	if prefs.Enabled {
		s.ensureEmailScheduler()
	}
	return nil
}

// ServiceShutdown 停掉调度器。
func (s *PortfolioService) ServiceShutdown() error {
	s.stopEmailScheduler()
	return nil
}

// ---------------------------------------------------------------------------
// 调度器
// ---------------------------------------------------------------------------

func (s *PortfolioService) ensureEmailScheduler() {
	if s.emailSched == nil {
		return
	}
	s.emailSched.startOnce()
}

func (s *emailScheduler) startOnce() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go s.loop()
}

// loop 每 30 秒看一次"到点了吗"。
func (s *emailScheduler) loop() {
	ticker := time.NewTicker(emailSchedulerInterval)
	defer ticker.Stop()
	slog.Info("邮件调度器已启动", "interval", emailSchedulerInterval.String())

	for {
		select {
		case <-s.stop:
			slog.Info("邮件调度器已停止")
			return
		case <-ticker.C:
			s.fire()
		}
	}
}

func (s *emailScheduler) fire() {
	s.mu.Lock()
	fn := s.tick
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// bindTick 绑定调度动作（构造时调用一次）。
func (s *emailScheduler) bindTick(fn emailTick) {
	s.mu.Lock()
	s.tick = fn
	s.mu.Unlock()
}

// stopScheduler 停掉协程。
func (s *PortfolioService) stopEmailScheduler() {
	if s.emailSched == nil {
		return
	}
	s.emailSched.mu.Lock()
	stop := s.emailSched.stop
	s.emailSched.mu.Unlock()
	s.emailSched.stopOnce.Do(func() { close(stop) })
}

// emailPrefs 读当前邮件设置（stock 为空时回落默认值，供测试用）。
func (s *PortfolioService) emailPrefs() config.EmailPrefs {
	if s.stock == nil || s.stock.cfg == nil {
		return config.DefaultEmailPrefs()
	}
	prefs, _ := s.stock.cfg.EmailPrefs()
	return prefs.Sanitize()
}

// saveEmailPrefs 写邮件设置。
func (s *PortfolioService) saveEmailPrefs(prefs config.EmailPrefs) config.EmailPrefs {
	if s.stock == nil || s.stock.cfg == nil {
		return prefs.Sanitize()
	}
	return s.stock.cfg.SetEmailPrefs(prefs)
}

// configPath 返回配置文件路径。
func (s *PortfolioService) configPath() string {
	if s.stock == nil || s.stock.cfg == nil {
		return ""
	}
	return s.stock.cfg.Path()
}

// ---------------------------------------------------------------------------
// 设置读写
// ---------------------------------------------------------------------------

// GetEmailPrefs 返回邮件设置与调度状态。
func (s *PortfolioService) GetEmailPrefs() *EmailPrefsInfo {
	prefs := s.emailPrefs()
	valid, invalid := email.ParseRecipients(prefs.Recipients)

	info := &EmailPrefsInfo{
		Prefs:             prefs,
		PasswordSet:       strings.TrimSpace(prefs.Password) != "",
		PasswordNote:      PasswordNoteText,
		ConfigPath:        s.configPath(),
		Recipients:        valid,
		InvalidRecipients: invalid,
		Schedule:          s.emailSchedulerInfo(),
	}
	// 校验"现在能不能发"，把第一处问题直接说出来
	if err := s.validateEmailConfig(prefs, valid, invalid); err != nil {
		info.Ready = false
		info.ReadyError = err.Error()
	} else {
		info.Ready = true
	}
	return info
}

// SaveEmailPrefs 保存邮件设置并返回最新状态。
//
// 两处刻意行为：
//  1. **Password 留空表示"不改"**。界面拿到的是脱敏设置（见 EmailPrefsInfo），
//     所以直接整份回写会把授权码清掉。
//  2. 保存后按需**启停调度器**：打开开关立刻生效，不用重启应用。
func (s *PortfolioService) SaveEmailPrefs(in config.EmailPrefs) (*EmailPrefsInfo, error) {
	current := s.emailPrefs()

	trimmed := strings.TrimSpace(in.Password)
	if trimmed == "" {
		in.Password = current.Password
	}
	// ClearPassword 是个显式开关（前端传 passwordCleared=true 时置空）
	sanitized := in.Sanitize()

	// 保存前先校验一遍：能算出来的问题不该等到明天 17:00 才暴露
	valid, invalid := email.ParseRecipients(sanitized.Recipients)
	if sanitized.Enabled {
		if err := s.validateEmailConfig(sanitized, valid, invalid); err != nil {
			return nil, fmt.Errorf("设置未保存：%w", err)
		}
	}

	saved := s.saveEmailPrefs(sanitized)
	slog.Info("邮件设置已保存",
		"enabled", saved.Enabled, "host", saved.Host, "port", saved.Port,
		"sendTime", saved.SendTime, "recipients", len(valid))

	if saved.Enabled {
		s.ensureEmailScheduler()
	} else {
		s.stopEmailScheduler()
	}
	return s.GetEmailPrefs(), nil
}

// ClearEmailPassword 清掉已保存的授权码。
//
// 单独一个方法而不是"传空串即清空"：空串已经被用来表示"不改"了，
// 让同一个值承担两种相反的含义，迟早会有人误清或误留。
func (s *PortfolioService) ClearEmailPassword() (*EmailPrefsInfo, error) {
	prefs := s.emailPrefs()
	prefs.Password = ""
	s.saveEmailPrefs(prefs)
	slog.Info("邮件授权码已清除")
	return s.GetEmailPrefs(), nil
}

// validateEmailConfig 校验"能不能发"，返回第一处问题。
func (s *PortfolioService) validateEmailConfig(
	prefs config.EmailPrefs, valid, invalid []string,
) error {
	if len(invalid) > 0 {
		return fmt.Errorf("以下收件人地址不合法：%s", strings.Join(invalid, "、"))
	}
	if len(valid) == 0 {
		return errors.New("没有填写收件人")
	}
	if prefs.Scope == config.EmailScopeOne && strings.TrimSpace(prefs.PortfolioID) == "" {
		return errors.New("发送范围选了「指定组合」，但没有选中组合")
	}
	// 复用邮件包自己的校验：主机/端口/加密/发件人都在这条路径上
	cfg := s.emailTransport(prefs)
	msg := email.Message{
		From:    fromAddress(prefs),
		To:      valid,
		Subject: "校验",
		HTML:    "校验",
	}
	if err := email.Validate(cfg, msg); err != nil {
		return err
	}
	return nil
}

// emailTransport 把配置转成邮件包的传输参数。
func (s *PortfolioService) emailTransport(prefs config.EmailPrefs) email.Config {
	return email.Config{
		Host:       prefs.Host,
		Port:       prefs.Port,
		Username:   prefs.Username,
		Password:   prefs.Password,
		Encryption: prefs.Encryption,
		Timeout:    time.Duration(prefs.TimeoutSeconds) * time.Second,
	}
}

// fromAddress 返回发件地址：留空时用 SMTP 用户名。
//
// 这是必要的兜底而不是"顺手"：用授权码登录的邮箱，发件人**必须**是
// 那个账号本身，所以让用户再抄一遍只会多一个写错的地方。
func fromAddress(prefs config.EmailPrefs) string {
	if strings.TrimSpace(prefs.From) != "" {
		return strings.TrimSpace(prefs.From)
	}
	return strings.TrimSpace(prefs.Username)
}

// ---------------------------------------------------------------------------
// 预览
// ---------------------------------------------------------------------------

// PreviewEmail 渲染一封"当前设置下的日报"（不发送）。
//
// 预览是这套功能唯一的"所见即所得"入口：邮件发出去就收不回来，
// 所以必须能在发之前看到正文。
//
// ⚠ 预览**也会先刷新评分**（与真正发送同一条路径）：否则预览显示的是
// 上一帧评分的建议，而真正发出去的是刷过之后的 —— 两者对不上，
// 预览就失去了"所见即所得"的意义。代价是点预览可能等几十秒（首刷）。
func (s *PortfolioService) PreviewEmail() (*EmailPreview, error) {
	prefs := s.emailPrefs()
	digest, err := s.prepareDigest(prefs, s.defaultRefresher(), false) // 预览不自动应用
	if err != nil {
		return nil, err
	}
	htmlBody, err := email.RenderHTML(*digest)
	if err != nil {
		return nil, err
	}
	return &EmailPreview{
		Subject:    renderSubject(prefs.Subject, digest),
		HTML:       htmlBody,
		Text:       email.RenderText(*digest),
		Portfolios: portfolioNames(digest),
		Warnings:   digest.Notes,
	}, nil
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

// SendTestEmail 立刻发一封测试邮件。
//
// 与"立即发送日报"分开：测试不需要评分数据，**必须随时能用** ——
// 它要回答的问题是"SMTP 配对了没有"，而不是"策略今天有没有建议"。
func (s *PortfolioService) SendTestEmail(to string) (*EmailSendResult, error) {
	prefs := s.emailPrefs()

	// 收件人：显式传了就用传的（可以先试一个地址），否则用设置里的列表
	var recipients []string
	var invalid []string
	if strings.TrimSpace(to) != "" {
		recipients, invalid = email.ParseRecipients(to)
	} else {
		recipients, invalid = email.ParseRecipients(prefs.Recipients)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("以下收件人地址不合法：%s", strings.Join(invalid, "、"))
	}
	if len(recipients) == 0 {
		return nil, errors.New("没有收件人：请先填写收件人，或在测试框里填一个地址")
	}

	subject := "【FreeStock】SMTP 测试邮件"
	body, text := testEmailBody(prefs, recipients)

	cfg := s.emailTransport(prefs)
	msg := email.Message{
		From:     fromAddress(prefs),
		FromName: prefs.FromName,
		To:       recipients,
		Subject:  subject,
		HTML:     body,
		Text:     text,
	}

	started := time.Now()
	if err := email.Send(cfg, msg); err != nil {
		slog.Warn("测试邮件发送失败", "host", prefs.Host, "port", prefs.Port, "err", err)
		return nil, err
	}
	elapsed := time.Since(started)
	slog.Info("测试邮件已发送", "host", prefs.Host, "port", prefs.Port,
		"recipients", recipients, "elapsed", elapsed.Round(time.Millisecond).String())

	return &EmailSendResult{
		Sent:       true,
		Recipients: recipients,
		Subject:    subject,
		HTML:       body,
		Elapsed:    elapsed.Round(time.Millisecond).String(),
		SentAt:     time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

// SendDailyEmailNow 立刻发送一次日报（手动触发）。
//
// 与定时发送走**同一条**构建与投递路径（sendDigest），只是多返回正文。
func (s *PortfolioService) SendDailyEmailNow() (*EmailSendResult, error) {
	prefs := s.emailPrefs()
	recipients, invalid := email.ParseRecipients(prefs.Recipients)
	if err := s.validateEmailConfig(prefs, recipients, invalid); err != nil {
		return nil, err
	}

	result, err := s.sendDigest(prefs, recipients, false)
	if err != nil {
		return nil, err
	}
	// 手动发送也要更新"上一次发送"的状态，否则界面上会显示成没发过
	s.recordSend(result)
	return result, nil
}

// sendDigest 构建并投递一次日报。isScheduled 为 true 时是定时触发。
func (s *PortfolioService) sendDigest(
	prefs config.EmailPrefs, recipients []string, isScheduled bool,
) (*EmailSendResult, error) {
	return s.sendDigestWith(prefs, recipients, isScheduled, s.defaultRefresher())
}

// sendDigestWith 与 sendDigest 相同，只是允许注入"刷新评分"的实现（测试用）。
func (s *PortfolioService) sendDigestWith(
	prefs config.EmailPrefs, recipients []string, isScheduled bool, refresh scoreRefresher,
) (*EmailSendResult, error) {
	started := time.Now()

	digest, err := s.prepareDigest(prefs, refresh, true) // 真正发送时才自动应用
	if err != nil {
		return nil, err
	}
	htmlBody, err := email.RenderHTML(*digest)
	if err != nil {
		return nil, err
	}
	subject := renderSubject(prefs.Subject, digest)

	cfg := s.emailTransport(prefs)
	msg := email.Message{
		From:     fromAddress(prefs),
		FromName: prefs.FromName,
		To:       recipients,
		Subject:  subject,
		HTML:     htmlBody,
		Text:     email.RenderText(*digest),
	}
	if err := email.Send(cfg, msg); err != nil {
		return nil, err
	}

	elapsed := time.Since(started)
	slog.Info("调仓日报已发送",
		"scheduled", isScheduled, "host", prefs.Host, "port", prefs.Port,
		"recipients", recipients, "portfolios", portfolioNames(digest),
		"tradeDate", digest.TradeDate,
		"elapsed", elapsed.Round(time.Millisecond).String())

	return &EmailSendResult{
		Sent:       true,
		Recipients: recipients,
		Subject:    subject,
		Portfolios: portfolioNames(digest),
		HTML:       htmlBody,
		Elapsed:    elapsed.Round(time.Millisecond).String(),
		SentAt:     time.Now().Format("2006-01-02 15:04:05"),
		Warnings:   digest.Notes,
	}, nil
}

// testEmailBody 组装测试邮件的正文。
//
// 用**纯内联样式的简单 HTML**，不走 RenderHTML —— 测试邮件的目的是
// 验证"能不能发出去"，正文里出问题只会干扰判断。
func testEmailBody(prefs config.EmailPrefs, recipients []string) (string, string) {
	encryption := prefs.Encryption
	if encryption == "" || encryption == config.EmailEncryptAuto {
		encryption = "自动（按端口判断）"
	}
	rows := [][2]string{
		{"SMTP 服务器", fmt.Sprintf("%s:%d", prefs.Host, prefs.Port)},
		{"加密方式", encryption},
		{"发件人", fmt.Sprintf("%s <%s>", orDashString(prefs.FromName), fromAddress(prefs))},
		{"收件人", strings.Join(recipients, ", ")},
		{"发送时间", prefs.SendTime},
		{"发送日", weekdayText(prefs.Weekdays)},
		// 发送前刷不刷评分会直接影响邮件里的数字是不是最新的，
		// 测试邮件里一并交代，省得用户去设置里翻
		{"发送前刷新评分", refreshSettingText(prefs)},
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"></head>`)
	b.WriteString(`<body style="margin:0;padding:20px;background:#f4f5f7;` +
		`font-family:'Segoe UI','Microsoft YaHei',sans-serif;color:#1f2430;">`)
	b.WriteString(`<div style="max-width:560px;margin:0 auto;background:#fff;border:1px solid #e3e6ea;` +
		`border-radius:10px;padding:20px;">`)
	b.WriteString(`<div style="font-size:16px;font-weight:600;">SMTP 连通性测试</div>`)
	b.WriteString(`<div style="margin-top:6px;font-size:12px;color:#6b7280;">` +
		`如果你收到了这封邮件，说明下面的配置可以正常发信。` +
		`它由 FreeStock 的组合管理页发出，不包含任何持仓数据。</div>`)
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" ` +
		`style="width:100%;margin-top:14px;border-collapse:collapse;font-size:12px;">`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		b.WriteString(`<td style="padding:6px 8px;color:#6b7280;border-bottom:1px solid #eef0f3;` +
			`white-space:nowrap;vertical-align:top;">` + escapeHTML(row[0]) + `</td>`)
		b.WriteString(`<td style="padding:6px 8px;border-bottom:1px solid #eef0f3;">` +
			escapeHTML(row[1]) + `</td>`)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</table>`)
	b.WriteString(`<div style="margin-top:12px;font-size:11px;color:#9ca3af;">发送于 ` +
		time.Now().Format("2006-01-02 15:04:05") + `</div>`)
	b.WriteString(`</div></body></html>`)

	var t strings.Builder
	t.WriteString("SMTP 连通性测试\n\n")
	for _, row := range rows {
		t.WriteString(row[0] + "：" + row[1] + "\n")
	}
	t.WriteString("\n如果你收到了这封邮件，说明上面的配置可以正常发信。\n")
	return b.String(), t.String()
}

func orDashString(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// escapeHTML 是测试邮件用的最小转义（正文渲染走 internal/email 的完整实现）。
func escapeHTML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return replacer.Replace(value)
}

// weekdayText 把星期集合说成人话。
func weekdayText(days []int) string {
	if len(days) == 0 {
		return "每天"
	}
	if len(days) == 5 && containsAll(days, []int{1, 2, 3, 4, 5}) {
		return "周一至周五"
	}
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	parts := make([]string, 0, len(days))
	for _, day := range days {
		if day >= 0 && day <= 6 {
			parts = append(parts, names[day])
		}
	}
	return strings.Join(parts, "、")
}

func containsAll(haystack, needles []int) bool {
	set := make(map[int]bool, len(haystack))
	for _, item := range haystack {
		set[item] = true
	}
	for _, needle := range needles {
		if !set[needle] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 正文内容构建
// ---------------------------------------------------------------------------

// buildDigest 把当前组合数据拼成邮件视图模型。
//
// 逐组合"能算多少算多少"：某个组合的评分还没刷新时，**不中止整封邮件**，
// 而是在它的段落里写清原因（PortfolioDigest.PlanError）——
// 静默跳过会让用户以为那个组合今天没有动作。
func (s *PortfolioService) buildDigest(prefs config.EmailPrefs, applyAuto bool) (*email.Digest, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	selected, err := selectDigestPortfolios(store, prefs)
	if err != nil {
		return nil, err
	}
	return s.buildDigestFor(store, selected, prefs, applyAuto)
}

// buildDigestFor 用**已经挑好的**组合列表拼内容。
//
// 抽出来是为了让"发送前刷新评分"能复用同一份列表 ——
// 否则挑两次列表，万一期间组合被删/被归档，刷新的集合与发出去的集合会对不上。
func (s *PortfolioService) buildDigestFor(
	store *portfolio.Store, selected []portfolio.Portfolio, prefs config.EmailPrefs, applyAuto bool,
) (*email.Digest, error) {
	digest := &email.Digest{GeneratedAt: time.Now()}
	if len(selected) == 0 {
		digest.Notes = append(digest.Notes,
			"没有可发送的组合：全部组合都是「已归档」，或发送范围指向的组合已不存在")
		return digest, nil
	}

	for _, item := range selected {
		digest.Portfolios = append(digest.Portfolios, s.buildPortfolioDigest(item, prefs, applyAuto))
	}

	// 评分日取各组合里**最新的那一个**（原实现用全表 MAX(trade_date)）。
	// 各组合可能停在不同日期：某个组合刚刷过评分，另一个还是几天前的。
	digest.TradeDate = latestTradeDate(digest.Portfolios)

	// 市场状态：同一个交易日全市场只有一份，读最新评分日的那一份即可
	if label, err := s.latestMarketStateLabel(store); err == nil {
		digest.MarketStateLabel = label
	}

	return digest, nil
}

// selectDigestPortfolios 挑出这次要发进邮件的组合。
//
// 抽出来单独一个函数，是为了让"发送前刷新评分"能**只刷这些组合用到的策略**：
// 刷一堆跟这封邮件无关的策略，只是白白多花几十秒。
func selectDigestPortfolios(
	store *portfolio.Store, prefs config.EmailPrefs,
) ([]portfolio.Portfolio, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}

	out := make([]portfolio.Portfolio, 0, len(all))
	if prefs.Scope == config.EmailScopeOne {
		for _, item := range all {
			if item.ID == prefs.PortfolioID {
				out = append(out, item)
				break
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("指定的组合不存在（id=%s），请重新选择", prefs.PortfolioID)
		}
		return out, nil
	}

	for _, item := range all {
		// 归档组合默认不发：它们已经不在运行
		if item.Status == portfolio.StatusArchived {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 发送前刷新评分
// ---------------------------------------------------------------------------

// scoreRefresher 是"刷新某个策略的评分"这件事的抽象。
//
// 抽出来只有一个目的：**让这段流程可以测**。正常实现就是
// `s.RefreshStrength(strategyID, limit, false)`；测试里换成一个记账用的假函数，
// 就能断言"刷了哪几个策略、传了多大的 limit"，而不用真去拉全市场行情。
type scoreRefresher func(strategyID string, limit int, force bool) (*RefreshResult, error)

// refreshScoresBeforeSend 是"按组合列表刷评分"的实现。
//
// ⚠ 它**不依赖 PortfolioService**，所以可以脱离数据库单独测：
// 传一个记账用的假 refresher 就能断言"刷了哪几个策略、传了多大的 limit"。
func refreshScoresBeforeSend(
	selected []portfolio.Portfolio, prefs config.EmailPrefs, refresh scoreRefresher,
) []string {
	if !prefs.RefreshScoresBeforeSend || refresh == nil {
		return nil
	}

	// 策略去重（归一化后再比，与原实现的 normalize_strategy_ids 一致）
	seen := map[string]bool{}
	ids := make([]string, 0, len(selected))
	for _, item := range selected {
		id := portfolio.NormalizeStrategy(item.StrategyID, item.Type)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	began := time.Now()
	notes := make([]string, 0, len(ids)+1)
	refreshed := 0
	failed := 0
	for _, id := range ids {
		label := score.StrategyLabelOf(id)
		result, err := refresh(id, prefs.ScoreLimit, false)
		if err != nil {
			// ⚠ **不中止** —— 一个策略刷不动不该让整封邮件发不出去。
			// 但也不能吞掉：写进说明里，收件人才知道数字可能是旧的。
			failed++
			slog.Warn("发送前刷新评分失败", "strategy", id, "err", err)
			notes = append(notes, fmt.Sprintf(
				"评分刷新失败（%s）：%v —— 该策略的建议可能基于旧评分", label, err))
			continue
		}
		refreshed++
		if result == nil {
			continue
		}
		if result.SkippedFromCache {
			// 当天已经刷过：一次行情都没拉。说明白，
			// 免得用户看到"秒回"以为根本没刷
			slog.Info("发送前评分已是最新，跳过重算",
				"strategy", id, "tradeDate", result.TradeDate, "total", result.Total)
			continue
		}
		slog.Info("发送前已刷新评分",
			"strategy", id, "tradeDate", result.TradeDate,
			"saved", result.Saved, "reused", result.Reused, "total", result.Total)
	}

	elapsed := time.Since(began).Round(time.Millisecond)
	if refreshed > 0 {
		notes = append(notes, fmt.Sprintf(
			"发送前已刷新评分：%d 个策略（%s），耗时 %s",
			refreshed, scoreLimitText(prefs.ScoreLimit), elapsed))
	}
	if refreshed == 0 {
		notes = append(notes, fmt.Sprintf(
			"发送前刷新评分：%d 个策略都没成功（耗时 %s），下面的建议可能基于旧评分",
			failed, elapsed))
	}
	return notes
}

// scoreLimitText 把只数上限说成人话。
func scoreLimitText(limit int) string {
	if limit <= 0 {
		return "全部标的"
	}
	return fmt.Sprintf("前 %d 只", limit)
}

// refreshSettingText 把"发送前刷不刷评分"说成人话（测试邮件里用）。
func refreshSettingText(prefs config.EmailPrefs) string {
	if !prefs.RefreshScoresBeforeSend {
		return "关闭（用数据库里现有的评分）"
	}
	return "开启（" + scoreLimitText(prefs.ScoreLimit) + "，增量）"
}

// defaultRefresher 是生产用的刷分实现（增量刷新，已算过的不重算）。
func (s *PortfolioService) defaultRefresher() scoreRefresher {
	return func(strategyID string, limit int, force bool) (*RefreshResult, error) {
		return s.RefreshStrength(strategyID, limit, force)
	}
}

// prepareDigest 是"发送 / 预览"共用的准备步骤：
//
//	挑组合 → （可选）先把评分刷到最新 → 拼内容
//
// ⚠ 刷新**必须在拼内容之前**。反过来的话，刷完还得再拼一次才拿到新数字，
// 于是"刷了却没用上"—— 而那正是这个功能要避免的事。
//
// 预览与发送走同一条路径，是为了让"预览看到的 = 真正发出去的"。
// 预览也会触发一次刷新，这样它显示的评分日就是实际会用的那一天。
func (s *PortfolioService) prepareDigest(
	prefs config.EmailPrefs, refresh scoreRefresher, applyAuto bool,
) (*email.Digest, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	selected, err := selectDigestPortfolios(store, prefs)
	if err != nil {
		return nil, err
	}

	// ⚠ 顺序：**先刷新，再拼内容**（理由见下面的注释与 9.6.1）
	notes := refreshScoresBeforeSend(selected, prefs, refresh)

	digest, err := s.buildDigestFor(store, selected, prefs, applyAuto)
	if err != nil {
		return nil, err
	}
	// 刷新说明插到最前面：它是"这封邮件的数据新鲜度"的交代，
	// 放在一堆组合级提示之后容易被忽略
	if len(notes) > 0 {
		digest.Notes = append(notes, digest.Notes...)
	}
	return digest, nil
}

// planHasExecutableChange 判断建议里是否有"可执行、且会改变仓位"的动作。
//
// 包含减仓/加层这类权重调整 —— 自动组合不手动确认，权重也必须被应用，
// 否则仓位会一直漂移。
func planHasExecutableChange(plan *PlanResult) bool {
	if plan == nil {
		return false
	}
	for _, row := range plan.Plan.Rows {
		if !row.IsExecutable {
			continue
		}
		switch row.Action {
		case score.ActionAdd, score.ActionReplaceIn, score.ActionClear, score.ActionReplaceOut,
			score.ActionReduce, score.ActionAddLayer:
			if row.TargetLayers != row.CurrentLayers {
				return true
			}
		}
	}
	return false
}

// shouldAutoApply 决定这次发送要不要自动应用。
//
//   - 只有 type=auto 的组合；
//   - 必须有真实可执行的变化；
//   - **质量成长只在月末调仓日应用**：它的目标权重是按当日快照算的，
//     每天都应用等于每天再平衡，会打乱月频节奏、白交手续费。
//     其他策略（低吸等）的计划本身只在信号日产生动作，直接应用即可。
func (s *PortfolioService) shouldAutoApply(item portfolio.Portfolio, plan *PlanResult) bool {
	if item.Type != portfolio.TypeAuto {
		return false
	}
	if !planHasExecutableChange(plan) {
		return false
	}
	if item.StrategyID == portfolio.StrategyQualityGrowth {
		if !s.isMonthEndTradeDate(plan.StrengthDate) {
			return false
		}
		// 只有清仓、没有建仓时不自动应用（数据缺口时避免"清了不买"）
		if !planHasBuy(plan) {
			return false
		}
	}
	return true
}

func (s *PortfolioService) isMonthEndTradeDate(date int) bool {
	return isRebalanceWindowDate(date)
}

// planHasBuy 判断计划里是否有"建仓/换入"这类买入动作（用于防止只清仓不加仓）。
func planHasBuy(plan *PlanResult) bool {
	if plan == nil {
		return false
	}
	for _, row := range plan.Plan.Rows {
		if row.IsExecutable && (row.Action == score.ActionAdd || row.Action == score.ActionReplaceIn) && row.TargetLayers > 0 {
			return true
		}
	}
	return false
}

// buildPortfolioDigest 构建单个组合的段落。
func (s *PortfolioService) buildPortfolioDigest(
	item portfolio.Portfolio, prefs config.EmailPrefs, applyAuto bool,
) email.PortfolioDigest {
	// 质量成长必须是 100 层制（1 层 = 1%）：空仓的历史组合自动升级一次，
	// 否则 10 层制下 15% 的目标只能落到 2 层（20%），和回测口径对不上。
	// 已有持仓的组合不自动改（避免把旧层数按新分母重解释）。
	if item.StrategyID == portfolio.StrategyQualityGrowth && item.TotalLayers < 100 {
		if store, err := s.db(); err == nil {
			if positions, e := store.ListPositions(item.ID); e == nil && len(positions) == 0 {
				if _, e2 := store.Update(item.ID, portfolio.PortfolioInput{
					StrategyID: item.StrategyID, TotalLayers: 100,
				}); e2 == nil {
					if it2, e3 := store.Get(item.ID); e3 == nil {
						item = it2
					}
				}
			}
		}
	}
	out := email.PortfolioDigest{
		Name:          item.Name,
		TypeLabel:     item.TypeLabel,
		StrategyLabel: item.StrategyLabel,
		TotalLayers:   item.TotalLayers,
	}

	// ---- 持仓与汇总（现价来源与界面完全一致）----
	detail, err := s.GetPortfolioDetail(item.ID)
	if err != nil {
		out.PlanError = "读取组合详情失败：" + err.Error()
		return out
	}
	out.ReturnPct = detail.Summary.TotalReturnPct
	out.PositionCount = detail.Summary.PositionCount
	out.UsedLayers = detail.Summary.UsedLayers
	out.AvailableLayers = detail.Summary.AvailableLayers
	out.PositionRatio = detail.Summary.PositionRatio
	out.PriceDate = detail.Summary.PriceDate
	out.MissingPrices = detail.Summary.MissingPrices

	if prefs.IncludeHoldings {
		out.Holdings = holdingDigests(detail.Positions)
	}

	// ---- 调仓建议 ----
	plan, err := s.BuildPlan(item.ID)
	if err != nil {
		out.PlanError = err.Error()
		out.Notes = append(out.Notes, "调仓建议未能生成，下面只有持仓信息")
		return out
	}

	// ---- 自动组合：先补做漏掉的月度调仓，再判断这次要不要应用 ----
	//
	// 只对 type=auto 的质量成长生效；预览路径传 applyAuto=false，避免"看一眼就把仓调了"。
	if applyAuto && item.Type == portfolio.TypeAuto && item.StrategyID == portfolio.StrategyQualityGrowth {
		if note := s.catchUpQualityGrowthRebalance(item); note != "" {
			out.Notes = append(out.Notes, note)
			// 补做后重读持仓与建议，让邮件反映补做后的真实仓位
			if d2, e2 := s.GetPortfolioDetail(item.ID); e2 == nil {
				detail = d2
				out.ReturnPct = d2.Summary.TotalReturnPct
				out.PositionCount = d2.Summary.PositionCount
				out.UsedLayers = d2.Summary.UsedLayers
				out.AvailableLayers = d2.Summary.AvailableLayers
				out.PositionRatio = d2.Summary.PositionRatio
				out.PriceDate = d2.Summary.PriceDate
				if prefs.IncludeHoldings {
					out.Holdings = holdingDigests(d2.Positions)
				}
			}
			if p2, e3 := s.BuildPlan(item.ID); e3 == nil {
				plan = p2
			}
		}
	}

	// ---- 自动组合：出现真实调仓时自动应用（不用等手动确认）----
	//
	// 只对 type=auto 的组合生效；预览路径传 applyAuto=false，避免"看一眼就把仓调了"。
	// 只认"建仓/清仓/换入换出"这类真实调仓，单纯的权重微调（reduce/add_layer）
	// 不自动执行，否则质量成长这种按日算权重的策略会天天被再平衡。
	if applyAuto && s.shouldAutoApply(item, plan) {
		if res, applyErr := s.ApplyRotation(item.ID); applyErr == nil {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"已自动应用调仓：新增 %d 只、减仓 %d 只、清仓 %d 只",
				res.Applied.Added, res.Applied.Reduced, res.Applied.Cleared))
			// 应用后重读持仓与建议，让邮件反映应用后的真实仓位
			if d2, e2 := s.GetPortfolioDetail(item.ID); e2 == nil {
				detail = d2
				out.ReturnPct = d2.Summary.TotalReturnPct
				out.PositionCount = d2.Summary.PositionCount
				out.UsedLayers = d2.Summary.UsedLayers
				out.AvailableLayers = d2.Summary.AvailableLayers
				out.PositionRatio = d2.Summary.PositionRatio
				out.PriceDate = d2.Summary.PriceDate
				if prefs.IncludeHoldings {
					out.Holdings = holdingDigests(d2.Positions)
				}
			}
			if p2, e3 := s.BuildPlan(item.ID); e3 == nil {
				plan = p2
			}
		} else {
			out.Notes = append(out.Notes, "自动应用调仓失败："+applyErr.Error())
		}
	}

	out.HealthLabel = plan.HealthLabel
	out.PortfolioScore = plan.PortfolioScore
	out.MaxDrawdownPct = plan.MaxDrawdownPct
	out.TradeDate = strconv.Itoa(plan.StrengthDate)
	out.NoActionReasons = plan.Plan.NoActionReasons
	out.Notes = append(out.Notes, plan.Notes...)
	if prefs.IncludeHoldings {
		// 用计划里的持仓行覆盖一次：它带 120 日回撤、持有交易日等
		// 只在评分链路里才有的字段（有评分时才有值）。
		out.Holdings = mergeHoldingExtra(out.Holdings, plan.Holdings)
	}

	actions := make([]email.ActionDigest, 0, len(plan.Plan.Rows))
	for _, row := range plan.Plan.Rows {
		actions = append(actions, email.ActionDigest{
			Action:               row.Action,
			ActionLabel:          row.ActionLabel,
			ActionPrefix:         row.ActionPrefix,
			Code:                 row.Code,
			Name:                 row.Name,
			Score:                row.Score,
			ScoreGap:             row.ScoreGap,
			Candidate:            row.CandidateCode,
			CandidateNme:         row.CandidateName,
			LayersText:           layersText(row.CurrentLayers, row.TargetLayers),
			Executable:           row.IsExecutable,
			RequiresConfirmation: row.RequiresConfirmation,
			Reason:               row.Reason,
		})
	}
	// 邮件是"待办清单"的读法：先清仓/换出、再减仓、再买入
	email.SortActions(actions)
	out.Actions = actions

	if prefs.IncludeCandidates {
		limit := prefs.CandidateLimit
		if limit <= 0 {
			limit = 10
		}
		pool := plan.Candidates
		if limit < len(pool) {
			pool = pool[:limit]
		}
		out.Candidates = candidateDigests(pool)
	}

	return out
}

// holdingDigests 把持仓转成邮件模型。**入参已按权重倒序**（store 层的排序），
// 这里不再排 —— 重排会把同权重不同建仓日的那一维丢掉（与详情页保持一致）。
func holdingDigests(positions []portfolio.Position) []email.HoldingDigest {
	out := make([]email.HoldingDigest, 0, len(positions))
	for _, position := range positions {
		out = append(out, email.HoldingDigest{
			Code:          position.Code,
			Name:          position.Name,
			Layers:        position.PositionLayers,
			WeightPct:     position.PositionWeight,
			EntryPrice:    position.EntryPrice,
			CurrentPrice:  position.CurrentPrice,
			ProfitLossPct: position.ProfitLossPct,
			EntryDate:     position.EntryDate,
			HoldingType:   holdingTypeLabel(position.HoldingType),
		})
	}
	return out
}

func holdingTypeLabel(value string) string {
	switch value {
	case portfolio.HoldingBase:
		return "底仓"
	case portfolio.HoldingSwing:
		return "波段仓"
	}
	return ""
}

// mergeHoldingExtra 把计划行里的持有交易日等补充信息并回持仓列表。
func mergeHoldingExtra(
	holdings []email.HoldingDigest, rows []score.Row,
) []email.HoldingDigest {
	extra := make(map[string]score.Row, len(rows))
	for _, row := range rows {
		extra[row.Code] = row
	}
	for i := range holdings {
		if row, ok := extra[holdings[i].Code]; ok {
			holdings[i].HoldingTradeDays = row.HoldingTradeDays
		}
	}
	return holdings
}

func candidateDigests(rows []score.Row) []email.CandidateDigest {
	out := make([]email.CandidateDigest, 0, len(rows))
	for _, row := range rows {
		out = append(out, email.CandidateDigest{
			Code:                row.Code,
			Name:                row.Name,
			TotalScore:          row.TotalScore,
			Close:               row.Close,
			RelativeStrength20:  row.RelativeStrength20D,
			DrawdownFromHigh120: row.DrawdownFromHigh120,
			Industry:            row.Industry,
		})
	}
	return out
}

// layersText 渲染层数变化：无变化时只给一个数。
func layersText(current, target int) string {
	if current == target {
		return strconv.Itoa(current)
	}
	return fmt.Sprintf("%d → %d", current, target)
}

// latestTradeDate 取各组合评分日里最新的那个（原实现取全表 MAX(trade_date)）。
func latestTradeDate(portfolios []email.PortfolioDigest) string {
	latest := ""
	for _, p := range portfolios {
		// 评分日是 YYYYMMDD 定长字符串，字典序即时间序
		if p.TradeDate != "" && p.TradeDate > latest {
			latest = p.TradeDate
		}
	}
	return latest
}

// latestMarketStateLabel 读最新评分日的市场状态标签；读不到返回空串。
func (s *PortfolioService) latestMarketStateLabel(store *portfolio.Store) (string, error) {
	date, err := store.LatestStrengthDateAny()
	if err != nil || date == 0 {
		return "", err
	}
	row, err := store.LoadMarketState(date)
	if err != nil {
		return "", err
	}
	return row.Label, nil
}

// portfolioNames 返回组合名列表。
func portfolioNames(d *email.Digest) []string {
	out := make([]string, 0, len(d.Portfolios))
	for _, p := range d.Portfolios {
		out = append(out, p.Name)
	}
	return out
}

// renderSubject 展开主题里的占位符。
//
// 支持 {date} / {portfolio} / {count}：
//   - {date}      评分日（YYYY-MM-DD），没有评分日时用今天
//   - {portfolio} 第一个组合名；多个组合时是「A 等 3 个组合」
//   - {count}     组合数
func renderSubject(template string, d *email.Digest) string {
	date := d.TradeDate
	if date == "" {
		date = time.Now().Format("20060102")
	}
	display := date
	if len(date) == 8 {
		display = date[:4] + "-" + date[4:6] + "-" + date[6:]
	}

	portfolioText := "—"
	switch len(d.Portfolios) {
	case 0:
		portfolioText = "无组合"
	case 1:
		portfolioText = d.Portfolios[0].Name
	default:
		portfolioText = fmt.Sprintf("%s 等 %d 个组合", d.Portfolios[0].Name, len(d.Portfolios))
	}

	replacer := strings.NewReplacer(
		"{date}", display,
		"{portfolio}", portfolioText,
		"{count}", strconv.Itoa(len(d.Portfolios)),
	)
	out := strings.TrimSpace(replacer.Replace(template))
	if out == "" {
		out = "【FreeStock】调仓日报"
	}
	return out
}

// ---------------------------------------------------------------------------
// 定时调度
// ---------------------------------------------------------------------------

// emailSchedulerInfo 组装调度状态快照。
func (s *PortfolioService) emailSchedulerInfo() EmailScheduleInfo {
	prefs := s.emailPrefs()
	info := EmailScheduleInfo{
		Enabled:  prefs.Enabled,
		SendTime: prefs.SendTime,
		Weekdays: prefs.Weekdays,
	}
	if s.emailSched == nil {
		return info
	}

	s.emailSched.mu.Lock()
	info.Running = s.emailSched.running
	if !s.emailSched.lastSentAt.IsZero() {
		info.LastSentAt = s.emailSched.lastSentAt.Format("2006-01-02 15:04:05")
	}
	info.LastStatus = s.emailSched.lastStatus
	info.LastError = s.emailSched.lastError
	info.LastRecipients = append([]string(nil), s.emailSched.lastRecipients...)
	s.emailSched.mu.Unlock()

	if prefs.Enabled {
		next, hint := nextSendTime(prefs, time.Now())
		if !next.IsZero() {
			info.NextRun = next.Format(time.RFC3339)
			info.NextRunHint = hint
		} else {
			info.NextRunHint = hint
		}
	} else {
		info.NextRunHint = "定时发送已关闭"
	}
	return info
}

// nextSendTime 算出下一次发送时刻与中文说明。
//
// 按天往前找（最多 7 天）：勾了发送日的组合在"今天已经过了发送时刻"时
// 应该落到下一个勾选的日子，而不是简单 +24 小时。
func nextSendTime(prefs config.EmailPrefs, now time.Time) (time.Time, string) {
	hour, minute, ok := parseClock(prefs.SendTime)
	if !ok {
		return time.Time{}, "发送时间格式不合法（应为 HH:MM）"
	}
	allowed := map[time.Weekday]bool{}
	if len(prefs.Weekdays) == 0 {
		for day := time.Weekday(0); day <= 6; day++ {
			allowed[day] = true
		}
	} else {
		for _, day := range prefs.Weekdays {
			allowed[time.Weekday(day)] = true
		}
	}

	for offset := 0; offset <= 7; offset++ {
		day := now.AddDate(0, 0, offset)
		candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, now.Location())
		if !allowed[candidate.Weekday()] {
			continue
		}
		if !candidate.After(now) {
			continue
		}
		return candidate, describeNextRun(candidate, now)
	}
	return time.Time{}, "未来 7 天内没有满足条件的发送日"
}

func describeNextRun(next, now time.Time) string {
	sameDay := next.Year() == now.Year() && next.YearDay() == now.YearDay()
	if sameDay {
		return "今天 " + next.Format("15:04") + " 发送"
	}
	if next.Sub(now) < 48*time.Hour {
		return "明天 " + next.Format("15:04") + " 发送"
	}
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return next.Format("01-02") + " " + names[next.Weekday()] + " " + next.Format("15:04") + " 发送"
}

// parseClock 解析 HH:MM。
func parseClock(value string) (hour, minute int, ok bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// dueForSchedule 判断"现在该发了吗"，并返回用于当天去重的键。
//
// 键里带**日期**，所以"一天只发一次"是按自然日算的；
// 手动改发送时间会改了键（含 HH:MM），因此当天还能按新时间再发一次 ——
// 这是有意的：用户改时间往往正是因为上一次没发出去。
func dueForSchedule(prefs config.EmailPrefs, now time.Time) (bool, string) {
	if !prefs.Enabled {
		return false, ""
	}
	hour, minute, ok := parseClock(prefs.SendTime)
	if !ok {
		return false, ""
	}
	if len(prefs.Weekdays) > 0 {
		allowed := false
		for _, day := range prefs.Weekdays {
			if int(now.Weekday()) == day {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, ""
		}
	}
	scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	delta := now.Sub(scheduled)
	if delta < 0 || delta >= emailSendWindow {
		return false, ""
	}
	return true, now.Format("20060102") + " " + prefs.SendTime
}

// maybeSendScheduledEmail 是调度器每次醒来时跑的判定。
func (s *PortfolioService) maybeSendScheduledEmail(now time.Time) {
	prefs := s.emailPrefs()
	due, key := dueForSchedule(prefs, now)
	if !due {
		return
	}

	s.emailSched.mu.Lock()
	if s.emailSched.lastSentKey == key {
		s.emailSched.mu.Unlock()
		return // 今天这一档已经发过
	}
	s.emailSched.mu.Unlock()

	// ⚠ **重入保护**：发送前会先刷评分（首刷全市场几十秒），
	// 比 30 秒的轮询间隔长。没有这道闸门的话，第二次轮询会在
	// 上一次还没发完时又进来，于是**刷两遍、发两封**。
	done, ok := s.emailSched.begin()
	if !ok {
		slog.Debug("定时日报正在发送中，跳过本次轮询")
		return
	}
	// ⚠ `done` 必须 defer：下面任何一条 return 都要放行，
	// 否则闸门会一直关着、之后再也不会发送。
	defer done()

	recipients, invalid := email.ParseRecipients(prefs.Recipients)
	if err := s.validateEmailConfig(prefs, recipients, invalid); err != nil {
		// 配置不完整：**记状态但不重试**。每 30 秒重试一次会把
		// 一个配置错误刷成满屏日志，而用户要的是界面上那一行提示。
		s.recordSendResult(false, key, nil, err.Error())
		slog.Warn("定时日报未发送：配置不完整", "err", err)
		return
	}

	result, err := s.sendDigest(prefs, recipients, true)
	if err != nil {
		s.recordSendResult(false, key, recipients, err.Error())
		slog.Error("定时日报发送失败", "err", err)
		return
	}
	s.recordSend(result)
	s.markSentKey(key)
}

// recordSend 记录一次成功发送。
func (s *PortfolioService) recordSend(result *EmailSendResult) {
	if s.emailSched == nil || result == nil {
		return
	}
	s.emailSched.mu.Lock()
	s.emailSched.lastSentAt = time.Now()
	s.emailSched.lastStatus = "成功"
	s.emailSched.lastError = ""
	s.emailSched.lastRecipients = append([]string(nil), result.Recipients...)
	s.emailSched.lastPortfolios = append([]string(nil), result.Portfolios...)
	s.emailSched.mu.Unlock()
}

// recordSendResult 记录一次失败（或配置不完整）。
//
// ⚠ 失败的 key **不写入 lastSentKey** —— 否则配置修好之后当天就再也不会重试。
func (s *PortfolioService) recordSendResult(sent bool, key string, recipients []string, errText string) {
	if s.emailSched == nil {
		return
	}
	s.emailSched.mu.Lock()
	s.emailSched.lastStatus = "失败"
	s.emailSched.lastError = errText
	if len(recipients) > 0 {
		s.emailSched.lastRecipients = append([]string(nil), recipients...)
	}
	if sent {
		s.emailSched.lastSentKey = key
	}
	s.emailSched.mu.Unlock()
}

func (s *PortfolioService) markSentKey(key string) {
	if s.emailSched == nil {
		return
	}
	s.emailSched.mu.Lock()
	s.emailSched.lastSentKey = key
	s.emailSched.mu.Unlock()
}
