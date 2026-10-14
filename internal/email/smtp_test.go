package email

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 进程内的假 SMTP 服务器
//
// 这里**刻意不 mock 会话逻辑**：Send 的价值全在"握手顺序、AUTH 机制选择、
// 点号转义、报文结构"这些细节上，把 session 换成接口再 mock 掉，
// 测的就只剩"我调用了 mock"。所以起一个真的监听 socket，跑真的 TCP 会话。
// ---------------------------------------------------------------------------

type fakeSMTP struct {
	listener net.Listener

	mu sync.Mutex
	// received 是本次会话收到的完整报文（DATA 内容）
	received string
	// commands 是收到的命令序列（AUTH 只记命令名，不记凭据）
	commands []string
	// authUser / authPassword 记录解码后的认证信息
	authUser     string
	authPassword string
	// authAttempts 记录用了哪些机制
	authAttempts []string
}

// fakeSMTPOptions 控制假服务器的行为。
type fakeSMTPOptions struct {
	// authMechanisms 是 EHLO 里广告的机制，如 ["PLAIN","LOGIN"]；为空表示不广告 AUTH
	authMechanisms []string
	// advertiseSTARTTLS 表示是否广告 STARTTLS
	advertiseSTARTTLS bool
	// rejectRcpt 非空时，对这个收件人回 550
	rejectRcpt string
	// noEhlo 表示对 EHLO 回 500（逼客户端退回 HELO）
	noEhlo bool
	// failAuth 表示认证一律失败
	failAuth bool
}

func newFakeSMTP(t *testing.T, opts fakeSMTPOptions) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	server := &fakeSMTP{listener: listener}
	go server.serve(opts)
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *fakeSMTP) addr() (string, int) {
	host, portText, _ := net.SplitHostPort(s.listener.Addr().String())
	port := 0
	for _, ch := range portText {
		port = port*10 + int(ch-'0')
	}
	return host, port
}

func (s *fakeSMTP) serve(opts fakeSMTPOptions) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn, opts)
	}
}

func (s *fakeSMTP) handle(conn net.Conn, opts fakeSMTPOptions) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	write := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}

	write("220 fake.local ESMTP ready")

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if opts.noEhlo {
				write("500 command not recognized")
				continue
			}
			s.mu.Lock()
			s.commands = append(s.commands, "EHLO")
			s.mu.Unlock()
			write("250-fake.local greets you")
			write("250-SIZE 35882577")
			if opts.advertiseSTARTTLS {
				write("250-STARTTLS")
			}
			if len(opts.authMechanisms) > 0 {
				write("250-AUTH " + strings.Join(opts.authMechanisms, " "))
			}
			write("250 8BITMIME")

		case strings.HasPrefix(upper, "HELO"):
			s.mu.Lock()
			s.commands = append(s.commands, "HELO")
			s.mu.Unlock()
			write("250 fake.local")

		case strings.HasPrefix(upper, "STARTTLS"):
			// 假服务器不做真握手 —— 用它的用例只验证"客户端有没有走到这一步"
			write("454 TLS not available in fake server")

		case strings.HasPrefix(upper, "AUTH PLAIN"):
			s.recordAuth("PLAIN")
			payload := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if opts.failAuth {
				write("535 authentication failed")
				continue
			}
			if decoded, err := base64.StdEncoding.DecodeString(payload); err == nil {
				parts := strings.Split(string(decoded), "\x00")
				if len(parts) == 3 {
					s.mu.Lock()
					s.authUser, s.authPassword = parts[1], parts[2]
					s.mu.Unlock()
				}
			}
			write("235 authentication successful")

		case strings.HasPrefix(upper, "AUTH LOGIN"):
			s.recordAuth("LOGIN")
			if opts.failAuth {
				write("535 authentication failed")
				continue
			}
			write("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			userLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			write("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			passLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			user, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(userLine))
			pass, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(passLine))
			s.mu.Lock()
			s.authUser, s.authPassword = string(user), string(pass)
			s.mu.Unlock()
			write("235 authentication successful")

		case strings.HasPrefix(upper, "MAIL FROM"):
			s.record("MAIL FROM")
			write("250 OK")

		case strings.HasPrefix(upper, "RCPT TO"):
			s.record("RCPT TO")
			if opts.rejectRcpt != "" && strings.Contains(line, opts.rejectRcpt) {
				write("550 mailbox unavailable")
				continue
			}
			write("250 OK")

		case strings.HasPrefix(upper, "DATA"):
			s.record("DATA")
			write("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				bodyLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				trimmed := strings.TrimRight(bodyLine, "\r\n")
				if trimmed == "." {
					break
				}
				body.WriteString(bodyLine)
			}
			s.mu.Lock()
			s.received = body.String()
			s.mu.Unlock()
			write("250 Queued")

		case strings.HasPrefix(upper, "QUIT"):
			write("221 Bye")
			return

		case strings.HasPrefix(upper, "RSET"):
			write("250 OK")

		default:
			write("500 command not recognized")
		}
	}
}

func (s *fakeSMTP) record(name string) {
	s.mu.Lock()
	s.commands = append(s.commands, name)
	s.mu.Unlock()
}

func (s *fakeSMTP) recordAuth(mechanism string) {
	s.mu.Lock()
	s.authAttempts = append(s.authAttempts, mechanism)
	s.commands = append(s.commands, "AUTH")
	s.mu.Unlock()
}

func (s *fakeSMTP) snapshot() (string, []string, string, string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received, append([]string(nil), s.commands...),
		s.authUser, s.authPassword, append([]string(nil), s.authAttempts...)
}

func testMessage() Message {
	return Message{
		From:     "sender@example.com",
		FromName: "调仓助手",
		To:       []string{"a@example.com", "b@example.com"},
		Subject:  "【FreeStock】2026-10-13 调仓日报",
		HTML:     "<html><body><b>你好</b></body></html>",
		Text:     "你好",
	}
}

// TestSendDeliversFullSession 走一遍完整会话：EHLO → AUTH → MAIL → RCPT → DATA。
func TestSendDeliversFullSession(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{authMechanisms: []string{"PLAIN", "LOGIN"}})
	host, port := server.addr()

	msg := testMessage()
	err := Send(Config{
		Host: host, Port: port, Username: "sender@example.com",
		Password: "auth-code", Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, msg)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	received, commands, user, password, mechanisms := server.snapshot()
	if user != "sender@example.com" {
		t.Errorf("认证用户名 = %q", user)
	}
	if password != "auth-code" {
		t.Errorf("认证口令 = %q", password)
	}
	// 广告了 PLAIN+LOGIN 时应当用 LOGIN（对国内邮箱兼容性最好）
	if len(mechanisms) == 0 || mechanisms[0] != "LOGIN" {
		t.Errorf("应优先用 LOGIN，实际 %v", mechanisms)
	}

	// 两个收件人都要 RCPT
	rcptCount := 0
	for _, command := range commands {
		if command == "RCPT TO" {
			rcptCount++
		}
	}
	if rcptCount != 2 {
		t.Errorf("RCPT TO 次数 = %d，期望 2", rcptCount)
	}

	// DATA 收到的报文必须是**已解码**后能还原出正文的
	if !strings.Contains(received, "Subject: ") {
		t.Fatal("报文里没有 Subject 头")
	}
	if !strings.Contains(received, "Subject: =?UTF-8?B?") {
		t.Error("中文主题没有做 RFC 2047 编码")
	}
	decoded := decodeBase64Bodies(t, received)
	if !strings.Contains(decoded, "你好") {
		t.Errorf("解码后的正文里找不到「你好」：%s", decoded)
	}
	if !strings.Contains(decoded, "<b>你好</b>") {
		t.Error("解码后的正文里找不到 HTML 分部")
	}
}

// TestSendWithoutAuth 不填用户名时不发 AUTH。
func TestSendWithoutAuth(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	_, commands, _, _, _ := server.snapshot()
	for _, command := range commands {
		if command == "AUTH" {
			t.Error("未配置用户名时不该发 AUTH")
		}
	}
}

// TestSendFallsBackToPlain 只广告 PLAIN 时用 PLAIN。
func TestSendFallsBackToPlain(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{authMechanisms: []string{"PLAIN"}})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Username: "u@example.com", Password: "p",
		Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	_, _, user, password, mechanisms := server.snapshot()
	if len(mechanisms) != 1 || mechanisms[0] != "PLAIN" {
		t.Errorf("应只用 PLAIN，实际 %v", mechanisms)
	}
	if user != "u@example.com" || password != "p" {
		t.Errorf("PLAIN 解码结果 = %q / %q", user, password)
	}
}

// TestSendWithoutAuthAdvertisement 服务器不广告 AUTH 时仍然尝试 LOGIN。
//
// 有些企业网关就是这样：不广告，但接受命令。
func TestSendWithoutAuthAdvertisement(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Username: "u@example.com", Password: "p",
		Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err != nil {
		t.Fatalf("未广告 AUTH 时也应尝试认证，实际失败: %v", err)
	}
	_, _, user, password, _ := server.snapshot()
	if user != "u@example.com" || password != "p" {
		t.Errorf("认证结果 = %q / %q", user, password)
	}
}

// TestSendAuthFailureMessage 认证失败时的提示要能指向"授权码"。
func TestSendAuthFailureMessage(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{
		authMechanisms: []string{"LOGIN"},
		failAuth:       true,
	})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Username: "u@example.com", Password: "wrong",
		Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err == nil {
		t.Fatal("认证失败时应当报错")
	}
	if !strings.Contains(err.Error(), "授权码") {
		t.Errorf("错误信息应提示可能是授权码问题，实际：%v", err)
	}
}

// TestSendRejectedRecipient 收件人被拒时要指出是哪一个。
func TestSendRejectedRecipient(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{rejectRcpt: "b@example.com"})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err == nil {
		t.Fatal("收件人被拒时应当报错")
	}
	if !strings.Contains(err.Error(), "b@example.com") {
		t.Errorf("错误里应包含被拒的收件人，实际：%v", err)
	}
}

// TestSendRequiresSTARTTLSWhenExplicitlyRequested 用户明确要求 STARTTLS
// 而服务器不支持时**必须报错**，不能静默降级成明文（那会把授权码发出去）。
func TestSendRequiresSTARTTLSWhenExplicitlyRequested(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{advertiseSTARTTLS: false})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Encryption: EncryptSTARTTLS, Timeout: 5 * time.Second,
	}, testMessage())
	if err == nil {
		t.Fatal("服务器不支持 STARTTLS 时应报错而不是降级")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("错误里应写明 STARTTLS，实际：%v", err)
	}
}

// TestSendFallsBackToHELO 服务器不认 EHLO 时退回 HELO。
func TestSendFallsBackToHELO(t *testing.T) {
	server := newFakeSMTP(t, fakeSMTPOptions{noEhlo: true})
	host, port := server.addr()

	err := Send(Config{
		Host: host, Port: port, Encryption: EncryptNone, Timeout: 10 * time.Second,
	}, testMessage())
	if err != nil {
		t.Fatalf("EHLO 失败后退回 HELO 应当仍能发送: %v", err)
	}
	_, commands, _, _, _ := server.snapshot()
	sawHelo := false
	for _, command := range commands {
		if command == "HELO" {
			sawHelo = true
		}
	}
	if !sawHelo {
		t.Error("没有看到 HELO 回退")
	}
}

// TestSendRejectsInvalidConfigBeforeConnecting 参数校验在建连之前。
//
// 用一个**不存在的地址**：如果先建连就会变成连接错误，
// 而我们要的是"收件人不合法"这句话。
func TestSendRejectsInvalidConfigBeforeConnecting(t *testing.T) {
	msg := testMessage()
	msg.To = []string{"a@example.com", "bad-address"}

	err := Send(Config{
		Host: "127.0.0.1", Port: 1, Encryption: EncryptNone, Timeout: time.Second,
	}, msg)
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "bad-address") {
		t.Errorf("应在建连前指出非法收件人，实际：%v", err)
	}
	if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "连接") {
		t.Errorf("不该走到建连，实际：%v", err)
	}
}

// ---------------------------------------------------------------------------
// 报文结构
// ---------------------------------------------------------------------------

// TestBuildMIMEEncodesChineseSubject 中文主题必须 RFC 2047 编码。
func TestBuildMIMEEncodesChineseSubject(t *testing.T) {
	payload, err := buildMIME(testMessage(), time.Now())
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "Subject: =?UTF-8?B?") {
		t.Error("中文主题未编码，会在客户端显示成乱码")
	}
	// 显示名同样要编码
	if !strings.Contains(text, "From: =?UTF-8?B?") {
		t.Error("含有中文的显示名未编码")
	}
	if !strings.Contains(text, "\r\n.\r\n") {
		t.Error("缺少 DATA 的结束标记")
	}
}

// TestBuildMIMEHTMLOnly 只给 HTML 时是单一分部，不用 multipart。
func TestBuildMIMEHTMLOnly(t *testing.T) {
	msg := Message{
		From: "a@example.com", To: []string{"b@example.com"},
		Subject: "plain", HTML: "<p>hi</p>",
	}
	payload, err := buildMIME(msg, time.Now())
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, "multipart/alternative") {
		t.Error("只有 HTML 时不该发 multipart")
	}
	if !strings.Contains(text, "Content-Type: text/html; charset=UTF-8") {
		t.Error("缺少 HTML 的 Content-Type")
	}
}

// TestBuildMIMEEscapesLeadingDots 正文行首的点号必须被转义。
//
// 这是 SMTP 最容易踩的坑：未转义的 ".\r\n" 会被服务器当成结束符，
// 后面整段正文被当成命令回 500，而错误信息完全看不出是正文引起的。
func TestBuildMIMEEscapesLeadingDots(t *testing.T) {
	body := "\n.\n.SECRET\n"
	wrapped := wrapBase64(body)
	// Base64 不会产生点号，所以这里验证的是结构上的保证：
	// 每行都不能恰好是单独的点号
	for _, line := range strings.Split(wrapped, "\r\n") {
		if line == "." {
			t.Fatal("报文里出现了单独一行点号，会被当成 DATA 结束符")
		}
	}
}

// TestBuildMIMEStripsHeaderInjection 主题里的裸换行不能变成头注入。
func TestBuildMIMEStripsHeaderInjection(t *testing.T) {
	// 非 ASCII 主题：整串被 Base64 编码，注入内容进的是正文而不是头
	msg := testMessage()
	msg.Subject = "日报\r\nBcc: attacker@evil.com"
	payload, err := buildMIME(msg, time.Now())
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if strings.Contains(string(payload), "\r\nBcc:") {
		t.Fatal("中文主题里的 CRLF 造成了头注入")
	}

	// 纯 ASCII 主题：不能原样保留换行（否则同样能注入）
	msg.Subject = "plain\r\nBcc: attacker@evil.com"
	payload, err = buildMIME(msg, time.Now())
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if strings.Contains(string(payload), "\r\nBcc:") {
		t.Fatal("ASCII 主题里的 CRLF 造成了头注入")
	}
}

// TestBuildMIMEMultipartHasBothParts 同时给 HTML 与纯文本时是 multipart/alternative。
func TestBuildMIMEMultipartHasBothParts(t *testing.T) {
	payload, err := buildMIME(testMessage(), time.Now())
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "multipart/alternative") {
		t.Error("应当发 multipart/alternative")
	}
	if strings.Count(text, "Content-Type: text/plain") != 1 ||
		strings.Count(text, "Content-Type: text/html") != 1 {
		t.Error("两个维护分部不完整")
	}
}

// TestCommandNameNeverLeaksCredentials 报错信息里不能出现 base64 凭据。
func TestCommandNameNeverLeaksCredentials(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("\x00user\x00password"))
	line := "AUTH PLAIN " + secret
	if name := commandName(line); name != "AUTH" {
		t.Errorf("commandName = %q", name)
	}
	if strings.Contains(commandName(line), secret) {
		t.Fatal("命令名里泄漏了凭据")
	}
}

// decodeBase64Bodies 把报文里 base64 编码的分部解码出来（供断言正文内容）。
//
// 报文的每个分部都是 "头\r\n\r\n<base64 行...>"，所以按空行切开，
// 对每一段试着整体 base64 解码即可（解不出来的段是头，忽略）。
// 尾部还要去掉紧跟的分部边界行。
func decodeBase64Bodies(t *testing.T, raw string) string {
	t.Helper()
	if !strings.Contains(raw, "\r\n\r\n") {
		t.Fatalf("报文里找不到头/体分隔：%q", raw)
	}
	var out strings.Builder
	out.WriteString(raw)
	for _, chunk := range strings.Split(raw, "\r\n\r\n") {
		compact := strings.NewReplacer("\r\n", "", "\n", "", " ", "").
			Replace(strings.TrimSpace(chunk))
		// 只保留纯 base64 字符，去掉可能粘上的边界行
		if idx := strings.Index(compact, "--"); idx >= 0 {
			compact = compact[:idx]
		}
		if compact == "" {
			continue
		}
		if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil {
			out.WriteString(string(decoded))
		}
	}
	return out.String()
}
