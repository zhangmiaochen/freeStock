package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestEmailPrefsDefaultsAreSafe 默认必须是**关着的**。
//
// 打开它意味着真的把持仓数据发到互联网上，不能因为装了个软件就默认开启。
func TestEmailPrefsDefaultsAreSafe(t *testing.T) {
	def := DefaultEmailPrefs()
	if def.Enabled {
		t.Error("默认不该开启定时发送")
	}
	if def.SendTime != "17:00" {
		t.Errorf("默认发送时间 = %q", def.SendTime)
	}
	if len(def.Recipients) != 0 {
		t.Error("默认不该有收件人")
	}
	if def.Port != 465 {
		t.Errorf("默认端口 = %d，国内邮箱常用 465", def.Port)
	}
	if !def.IncludeHoldings {
		t.Error("持仓明细默认应当包含（邮件的主要内容之一）")
	}
	if def.IncludeCandidates {
		t.Error("候选池默认应当关闭（会让邮件长很多）")
	}
}

// TestEmailPrefsSanitizeNeverFails 非法值只纠正、不报错（与 KlinePrefs 同一约定）。
func TestEmailPrefsSanitizeNeverFails(t *testing.T) {
	bad := EmailPrefs{
		Port:           -1,
		Encryption:     "量子加密",
		Scope:          "whatever",
		SendTime:       "25:99",
		Weekdays:       []int{9, -3, 1, 1},
		CandidateLimit: 999,
		TimeoutSeconds: 1,
	}
	got := bad.Sanitize()

	if got.Port != DefaultEmailPrefs().Port {
		t.Errorf("非法端口未回落: %d", got.Port)
	}
	if got.Encryption != EmailEncryptAuto {
		t.Errorf("非法加密方式未回落: %q", got.Encryption)
	}
	if got.Scope != EmailScopeActive {
		t.Errorf("非法范围未回落: %q", got.Scope)
	}
	if got.SendTime != "17:00" {
		t.Errorf("非法时间未回落: %q", got.SendTime)
	}
	// 越界的星期被丢掉，重复的被去掉，结果只剩 1
	if !reflect.DeepEqual(got.Weekdays, []int{1}) {
		t.Errorf("星期收敛结果 = %v", got.Weekdays)
	}
	if got.CandidateLimit > 50 {
		t.Errorf("候选数未收敛: %d", got.CandidateLimit)
	}
	if got.TimeoutSeconds < 5 {
		t.Errorf("超时未收敛: %d", got.TimeoutSeconds)
	}
}

// TestEmailPrefsSanitizeKeepsEmptyWeekdays 空星期表示"每天发"，
// **不能被默认值填成工作日** —— 那是两件不同的事。
func TestEmailPrefsSanitizeKeepsEmptyWeekdays(t *testing.T) {
	got := EmailPrefs{Weekdays: nil}.Sanitize()
	if len(got.Weekdays) != 0 {
		t.Errorf("空星期应当保持为空（=每天），实际 %v", got.Weekdays)
	}
}

// TestNormalizeRecipientsHandlesMixedSeparators 从邮件客户端/Excel 里
// 粘贴出来的列表常常是换行、逗号、分号混在一起的。
func TestNormalizeRecipientsHandlesMixedSeparators(t *testing.T) {
	raw := "a@x.com, b@x.com；c@x.com\n d@x.com;e@x.com\tf@x.com"
	got := NormalizeRecipients(raw)
	lines := strings.Split(got, "\n")
	if len(lines) != 6 {
		t.Fatalf("应切出 6 个地址，实际 %d：%q", len(lines), got)
	}
	for _, line := range lines {
		if strings.ContainsAny(line, ",;，； \t") {
			t.Errorf("地址里残留了分隔符: %q", line)
		}
	}
}

func TestNormalizeRecipientsDeduplicatesIgnoringCase(t *testing.T) {
	got := NormalizeRecipients("A@x.com\na@x.com\nb@x.com")
	if strings.Count(got, "\n") != 1 {
		t.Errorf("应当去重，实际：%q", got)
	}
}

// TestEmailPrefsRoundTrip 设置要能落盘并被重新读出来。
func TestEmailPrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store := NewStoreAt(path)

	if _, saved := store.EmailPrefs(); saved {
		t.Error("首次读取不该报告已保存")
	}

	prefs := DefaultEmailPrefs()
	prefs.Enabled = true
	prefs.Host = "smtp.qq.com"
	prefs.Port = 465
	prefs.Username = "me@qq.com"
	prefs.Password = "auth-code"
	prefs.Recipients = "a@x.com\nb@x.com"
	prefs.SendTime = "18:30"
	prefs.Weekdays = []int{1, 2, 3, 4, 5}
	store.SetEmailPrefs(prefs)

	reopened := NewStoreAt(path)
	got, saved := reopened.EmailPrefs()
	if !saved {
		t.Fatal("重新打开后应当报告已保存")
	}
	if got.Host != "smtp.qq.com" || got.Username != "me@qq.com" || got.Password != "auth-code" {
		t.Errorf("SMTP 参数未正确持久化: %+v", got)
	}
	if got.SendTime != "18:30" {
		t.Errorf("发送时间未持久化: %q", got.SendTime)
	}
	if !got.Enabled {
		t.Error("开关未持久化")
	}
}

// TestEmailPrefsDoesNotClobberOtherSettings 写邮件设置不能把数据源等设置冲掉。
func TestEmailPrefsDoesNotClobberOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store := NewStoreAt(path)

	store.SetDataSource("http://192.168.1.10:7899", KindStockDB)
	store.SetCnLakeRoot(`D:\lake`)
	store.SetKlinePrefs(DefaultKlinePrefs())
	store.SetUpdatePrefs(DefaultUpdatePrefs())

	prefs := DefaultEmailPrefs()
	prefs.Host = "smtp.qq.com"
	store.SetEmailPrefs(prefs)

	reopened := NewStoreAt(path)
	if got := reopened.DataSourceURL(); got != "http://192.168.1.10:7899" {
		t.Errorf("数据源被冲掉了: %q", got)
	}
	if got := reopened.CnLakeRoot(); got != `D:\lake` {
		t.Errorf("数据湖根目录被冲掉了: %q", got)
	}
	if _, saved := reopened.KlinePrefs(); !saved {
		t.Error("K 线偏好被冲掉了")
	}
	if _, saved := reopened.UpdatePrefs(); !saved {
		t.Error("数据更新设置被冲掉了")
	}
}

// TestEmailPrefsSanitizedOnRead 手工改坏配置文件不该让界面崩掉。
func TestEmailPrefsSanitizedOnRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	broken := `{
	  "emailPrefs": {
	    "enabled": true,
	    "host": "smtp.qq.com",
	    "port": 0,
	    "encryption": "nonsense",
	    "sendTime": "99:99",
	    "weekdays": [1, 1, 42],
	    "scope": "???"
	  }
	}`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}

	got, saved := NewStoreAt(path).EmailPrefs()
	if !saved {
		t.Fatal("应当读到已保存的设置")
	}
	if got.Port != 465 {
		t.Errorf("端口未回落: %d", got.Port)
	}
	if got.Encryption != EmailEncryptAuto {
		t.Errorf("加密方式未回落: %q", got.Encryption)
	}
	if got.SendTime != "17:00" {
		t.Errorf("时间未回落: %q", got.SendTime)
	}
	if !reflect.DeepEqual(got.Weekdays, []int{1}) {
		t.Errorf("星期未收敛: %v", got.Weekdays)
	}
	if got.Scope != EmailScopeActive {
		t.Errorf("范围未回落: %q", got.Scope)
	}
}

// TestEmailPrefsJSONShape 字段名是**前端契约**，改名会静默让界面读不到值。
func TestEmailPrefsJSONShape(t *testing.T) {
	raw, err := json.Marshal(DefaultEmailPrefs())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	for _, want := range []string{
		`"enabled"`, `"host"`, `"port"`, `"username"`, `"password"`,
		`"encryption"`, `"from"`, `"fromName"`, `"timeoutSeconds"`,
		`"recipients"`, `"subject"`, `"sendTime"`, `"weekdays"`,
		`"scope"`, `"portfolioId"`, `"includeHoldings"`, `"includeCandidates"`,
		`"candidateLimit"`,
		// 发送前刷新评分（不刷的话建议可能基于旧评分）
		`"refreshScoresBeforeSend"`, `"scoreLimit"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("序列化结果里缺少字段 %s", want)
		}
	}
}

// TestEmailScoreLimitZeroIsMeaningful ScoreLimit=0 表示"全市场"，
// 是**合法值**，不能被 Sanitize 当成"没填"回落到别的值。
//
// ⚠ 这条容易写错：项目里通用的 clampInt 会把 0 当作"未设置"回落到默认值，
// 而这里的默认值恰好也是 0 —— 一旦改用 clampInt，语义就反了。
func TestEmailScoreLimitZeroIsMeaningful(t *testing.T) {
	if def := DefaultEmailPrefs(); def.ScoreLimit != 0 {
		t.Fatalf("默认 ScoreLimit 应当是 0（全市场），实际 %d", def.ScoreLimit)
	}

	cases := []struct {
		in   int
		want int
	}{
		{0, 0},     // 全市场：保持
		{800, 800}, // 前 800 只：保持
		{-1, 0},    // 负数：收敛到全市场
		{-999, 0},  // 负数：收敛到全市场
	}
	for _, c := range cases {
		if got := (EmailPrefs{ScoreLimit: c.in}).Sanitize().ScoreLimit; got != c.want {
			t.Errorf("Sanitize(ScoreLimit=%d) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// TestEmailRefreshBeforeSendRoundTrip 开关与只数上限要能落盘、读回。
func TestEmailRefreshBeforeSendRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStoreAt(path)

	prefs := DefaultEmailPrefs()
	prefs.RefreshScoresBeforeSend = true
	prefs.ScoreLimit = 1500
	store.SetEmailPrefs(prefs)

	got, saved := NewStoreAt(path).EmailPrefs()
	if !saved {
		t.Fatal("重新打开后应当报告已保存")
	}
	if !got.RefreshScoresBeforeSend {
		t.Error("「发送前刷新评分」开关未持久化")
	}
	if got.ScoreLimit != 1500 {
		t.Errorf("评分只数上限未持久化：%d", got.ScoreLimit)
	}

	// 关掉之后也要能读回 false
	off := DefaultEmailPrefs()
	off.RefreshScoresBeforeSend = false
	store.SetEmailPrefs(off)

	got, _ = NewStoreAt(path).EmailPrefs()
	if got.RefreshScoresBeforeSend {
		t.Error("关掉之后读回仍是开启")
	}
}

// TestEmailPrefsSanitizeKeepsRefreshFlag Sanitize 不该动这个开关与只数上限。
func TestEmailPrefsSanitizeKeepsRefreshFlag(t *testing.T) {
	got := EmailPrefs{
		SendTime:                "not-a-time", // 会被纠正
		RefreshScoresBeforeSend: true,
		ScoreLimit:              1234,
	}.Sanitize()
	if !got.RefreshScoresBeforeSend {
		t.Error("Sanitize 不该把合法的开关改成 false")
	}
	if got.ScoreLimit != 1234 {
		t.Errorf("Sanitize 不该改合法的只数上限：%d", got.ScoreLimit)
	}
}
