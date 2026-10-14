// Package email 负责把每日调仓日报渲染成 HTML 并投递出去。
//
// 只用标准库（net/smtp + crypto/tls）自己走一遍 SMTP 会话，理由：
//
//  1. 国内主流邮箱（QQ / 163 / 企业邮箱）的 SMTP 默认端口是 **465**，
//     那是"连上就 TLS 握手"的**隐式 TLS**。`net/smtp.SendMail` 只支持
//     明文连接 + STARTTLS 升级，对 465 直接不可用。
//  2. 换第三方库要多一份依赖，而这个项目对依赖很克制（见 README 技术栈）。
//
// 代价是握手、认证、DATA 的点号转义都要自己写对 —— 所以这些都有单测
// （见 smtp_test.go，用进程内的假 SMTP 服务器真跑一遍会话）。
package email

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 加密方式。与 config 包的常量同名同值，但这里**不 import config** ——
// 邮件发送不该依赖"配置存哪、怎么存"。service 层负责把两边接起来。
const (
	EncryptAuto     = "auto"
	EncryptSSL      = "ssl"
	EncryptSTARTTLS = "starttls"
	EncryptNone     = "none"
)

// Message 是一封待发送的邮件。
type Message struct {
	From     string // 信封发件地址（SMTP 的 MAIL FROM）
	FromName string // 显示名，可为空
	To       []string
	Subject  string
	// HTML 是正文（HTML 片段或整篇文档）；Text 是可选的纯文本备选。
	// 两个都给时发 multipart/alternative —— 只给一个就发单一分部。
	HTML string
	Text string
}

// Config 是一次投递所需的全部参数。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	// Encryption 取 EncryptAuto / EncryptSSL / EncryptSTARTTLS / EncryptNone
	Encryption string
	Timeout    time.Duration
	// InsecureSkipVerify 只给自签名证书的内网 relay 用，界面上默认关闭。
	InsecureSkipVerify bool
}

// resolvedEncryption 把 auto 按端口解析成确定的方式。
//
// 端口约定：465 = 隐式 TLS，587 = 提交端口的 STARTTLS，25 = 老式明文（仍尝试 STARTTLS）。
func resolvedEncryption(cfg Config) string {
	if cfg.Encryption != "" && cfg.Encryption != EncryptAuto {
		return cfg.Encryption
	}
	if cfg.Port == 465 {
		return EncryptSSL
	}
	return EncryptSTARTTLS
}

// Send 投递一封邮件。**全部参数先校验再建连** —— 建连之后才发现收件人写错，
// 用户拿到的是一个 SMTP 报错码（550），而不是"第 2 个地址不合法"。
func Send(cfg Config, msg Message) error {
	if err := Validate(cfg, msg); err != nil {
		return err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: timeout}

	tlsConfig := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // 见字段注释：内网自签名场景
		MinVersion:         tls.VersionTLS12,
	}

	mode := resolvedEncryption(cfg)

	var conn net.Conn
	var err error
	if mode == EncryptSSL {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
		if err != nil {
			return fmt.Errorf("连接 %s 失败（隐式 TLS / 465）：%w", address, err)
		}
	} else {
		conn, err = dialer.Dial("tcp", address)
		if err != nil {
			return fmt.Errorf("连接 %s 失败：%w", address, err)
		}
	}
	defer func() { _ = conn.Close() }()

	// 握手之后所有读写都受同一个总超时约束 —— 不设的话卡在半路会
	// 让调度器的这一轮永远不返回。
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)

	session := &smtpSession{
		conn:      conn,
		reader:    bufio.NewReader(conn),
		host:      cfg.Host,
		tlsConfig: tlsConfig,
		timeout:   timeout,
	}

	if err := session.handshake(); err != nil {
		return err
	}
	return session.deliver(cfg, msg)
}

// Validate 校验一次投递的参数是否完整可执行。
//
// 单独导出是为了让"保存设置"也能用它 —— 保存时就把问题说出来，
// 而不是等到第二天 17:00 才发现发不出去。
func Validate(cfg Config, msg Message) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("SMTP 服务器地址为空")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("SMTP 端口不合法：%d", cfg.Port)
	}
	switch cfg.Encryption {
	case "", EncryptAuto, EncryptSSL, EncryptSTARTTLS, EncryptNone:
	default:
		return fmt.Errorf("未知的加密方式：%q", cfg.Encryption)
	}
	if strings.TrimSpace(msg.From) == "" {
		return errors.New("发件人为空（未填 From 时会用 SMTP 用户名，两个都空就只能报错）")
	}
	if _, err := ParseAddress(msg.From); err != nil {
		return fmt.Errorf("发件人地址不合法（%s）：%w", msg.From, err)
	}
	if len(msg.To) == 0 {
		return errors.New("没有收件人")
	}
	for _, addr := range msg.To {
		if _, err := ParseAddress(addr); err != nil {
			return fmt.Errorf("收件人地址不合法（%s）：%w", addr, err)
		}
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return errors.New("邮件主题为空")
	}
	if strings.TrimSpace(msg.HTML) == "" && strings.TrimSpace(msg.Text) == "" {
		return errors.New("邮件正文为空")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------

type smtpSession struct {
	conn      net.Conn
	reader    *bufio.Reader
	host      string
	tlsConfig *tls.Config
	timeout   time.Duration
	// capabilities 是 EHLO 返回的能力列表（大写）
	capabilities map[string]string
}

// handshake 读问候语并发送 EHLO（失败则退回 HELO）。
func (s *smtpSession) handshake() error {
	code, text, err := s.readResponse()
	if err != nil {
		return fmt.Errorf("读取服务器问候失败：%w", err)
	}
	if code != 220 {
		return fmt.Errorf("服务器拒绝连接：%s", text)
	}

	if err := s.cmd(250, "EHLO freestock.local"); err != nil {
		// 老服务器不认 EHLO 时退回 HELO（此时没有 AUTH / STARTTLS 能力）
		if err2 := s.cmd(250, "HELO freestock.local"); err2 != nil {
			return fmt.Errorf("EHLO/HELO 失败：%v / %v", err, err2)
		}
		s.capabilities = map[string]string{}
		return nil
	}
	return nil
}

// upgradeTLS 执行 STARTTLS 并重新 EHLO。
func (s *smtpSession) upgradeTLS() error {
	if err := s.cmd(220, "STARTTLS"); err != nil {
		return err
	}
	tlsConn := tls.Client(s.conn, s.tlsConfig)
	_ = tlsConn.SetDeadline(time.Now().Add(s.timeout))
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("STARTTLS 握手失败：%w", err)
	}
	s.conn = tlsConn
	s.reader = bufio.NewReader(tlsConn)
	// 升级后必须重新 EHLO：能力列表（尤其 AUTH 的可用机制）可能变
	return s.cmd(250, "EHLO freestock.local")
}

func (s *smtpSession) deliver(cfg Config, msg Message) error {
	// STARTTLS：服务器声明支持才升级，声明不支持就按明文继续
	// （EncryptNone 是用户显式选择了不加密，就不再升级）
	mode := resolvedEncryption(cfg)
	if mode == EncryptSTARTTLS {
		if _, ok := s.capabilities["STARTTLS"]; ok {
			if err := s.upgradeTLS(); err != nil {
				return err
			}
		} else if cfg.Encryption == EncryptSTARTTLS {
			// 用户**明确要求** STARTTLS 而服务器不支持 → 不能静默降级成明文，
			// 那会把授权码明文发出去
			return fmt.Errorf("服务器 %s:%d 不支持 STARTTLS，"+
				"如果它用的是隐式 TLS（465）请把加密方式改成 SSL，"+
				"内网明文 relay 请改成「不加密」", cfg.Host, cfg.Port)
		}
	}

	if cfg.Username != "" {
		if err := s.authenticate(cfg.Username, cfg.Password); err != nil {
			return err
		}
	}

	if err := s.cmd(250, "MAIL FROM:<%s>", msg.From); err != nil {
		return fmt.Errorf("MAIL FROM 被拒绝：%w", err)
	}
	for _, addr := range msg.To {
		if err := s.cmd(250, "RCPT TO:<%s>", addr); err != nil {
			return fmt.Errorf("收件人 %s 被拒绝：%w", addr, err)
		}
	}
	if err := s.cmd(354, "DATA"); err != nil {
		return fmt.Errorf("DATA 被拒绝：%w", err)
	}

	payload, err := buildMIME(msg, time.Now())
	if err != nil {
		return err
	}
	if _, err := s.conn.Write(payload); err != nil {
		return fmt.Errorf("写正文失败：%w", err)
	}
	if code, text, err := s.readResponse(); err != nil {
		return fmt.Errorf("投递结果读取失败：%w", err)
	} else if code != 250 {
		return fmt.Errorf("服务器拒收：%s", text)
	}

	// QUIT 失败无所谓 —— 邮件已经在上一行被接受了
	_ = s.cmd(221, "QUIT")
	return nil
}

// authenticate 做 SMTP AUTH。
//
// 机制选择：服务器声明了 AUTH 就**优先用 LOGIN**（QQ / 163 / Exchange 对它
// 兼容性最好），没声明 AUTH 时直接试 LOGIN —— 有些企业网关不广告 AUTH，
// 直接发命令反而能过。LOGIN 不被接受时再退回 PLAIN（RFC 4616）。
func (s *smtpSession) authenticate(username, password string) error {
	mechanisms := map[string]bool{}
	if raw, ok := s.capabilities["AUTH"]; ok {
		for _, m := range strings.Fields(strings.ToUpper(raw)) {
			mechanisms[m] = true
		}
	}
	advertised := len(mechanisms) > 0

	if !advertised || mechanisms["LOGIN"] {
		if err := s.authLogin(username, password); err == nil {
			return nil
		} else if !advertised || !mechanisms["PLAIN"] {
			return err
		}
	}
	if mechanisms["PLAIN"] {
		return s.authPlain(username, password)
	}
	names := make([]string, 0, len(mechanisms))
	for name := range mechanisms {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("服务器只支持这些认证机制：%s（本程序只实现 PLAIN / LOGIN）",
		strings.Join(names, ", "))
}

// authPlain 走 RFC 4616 的 PLAIN 机制。
func (s *smtpSession) authPlain(username, password string) error {
	token := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + username + "\x00" + password))
	// 235 = 认证成功；334 是要求继续的中间态（这里不该出现）
	if err := s.cmd(235, "AUTH PLAIN %s", token); err != nil {
		return fmt.Errorf("SMTP 认证失败（PLAIN，用户名 %s）：%w", username, err)
	}
	return nil
}

// authLogin 走已被广泛实现的 LOGIN 机制（非正式 RFC，但 QQ/163/Exchange 都认）。
func (s *smtpSession) authLogin(username, password string) error {
	if err := s.cmd(334, "AUTH LOGIN"); err != nil {
		return authError("AUTH LOGIN 未被接受", username, err)
	}
	if err := s.cmd(334, "%s", base64.StdEncoding.EncodeToString([]byte(username))); err != nil {
		return authError("用户名被拒绝", username, err)
	}
	if err := s.cmd(235, "%s", base64.StdEncoding.EncodeToString([]byte(password))); err != nil {
		return authError("口令被拒绝", username, err)
	}
	return nil
}

// authError 把认证失败包装成**能指导用户下一步**的提示。
//
// 认证失败最常见的原因是"填了登录密码而不是 SMTP 授权码"，
// 而服务器只会回一句 535 Authentication failed —— 不说这句话，
// 用户会去怀疑服务器地址、端口、防火墙。所以这里把可能性写进错误里。
func authError(step, username string, cause error) error {
	return fmt.Errorf("SMTP 认证失败（%s，用户名 %s）：%w。"+
		"请确认填的是邮箱的「SMTP 授权码」而不是登录密码"+
		"（QQ / 163 / Gmail 都需要先在网页端开启 SMTP 服务并生成授权码）", step, username, cause)
}

// ---------------------------------------------------------------------------
// 协议原语
// ---------------------------------------------------------------------------

// cmd 发一条命令并断言响应码属于 want。
func (s *smtpSession) cmd(want int, format string, args ...any) error {
	line := fmt.Sprintf(format, args...)
	if _, err := s.conn.Write([]byte(line + "\r\n")); err != nil {
		return fmt.Errorf("发送 %q 失败：%w", commandName(line), err)
	}
	code, text, err := s.readResponse()
	if err != nil {
		return fmt.Errorf("命令 %q 无响应：%w", commandName(line), err)
	}
	if code != want {
		return fmt.Errorf("命令 %q 期望 %d，实际 %s", commandName(line), want, text)
	}
	return nil
}

// readResponse 读一条（或多行）SMTP 响应。
//
// 多行响应的形式是 `250-XXX` 续行、`250 XXX` 收尾 —— EHLO 的能力列表
// 就是多行的，只读第一行会漏掉 AUTH / STARTTLS。
//
// 能力只在**本次响应结束时**整体替换，不逐行写回：EHLO 之后的一条普通
// 响应不该把能力列表清空（清空会让后面的 STARTTLS 判定失效）。
func (s *smtpSession) readResponse() (int, string, error) {
	var last string
	capabilities := map[string]string{}

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return 0, last, err
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 3 {
			return 0, line, fmt.Errorf("响应格式异常：%q", line)
		}
		status, err := strconv.Atoi(line[:3])
		if err != nil {
			return 0, line, fmt.Errorf("响应码无法解析：%q", line)
		}
		last = line

		// 第 4 个字符是 '-' 表示还有续行
		more := len(line) > 3 && line[3] == '-'
		body := ""
		if len(line) > 4 {
			body = line[4:]
		}
		if more {
			// EHLO 的能力行形如 "AUTH PLAIN LOGIN" / "STARTTLS" / "SIZE 35882577"
			fields := strings.Fields(body)
			if len(fields) > 0 {
				key := strings.ToUpper(fields[0])
				capabilities[key] = strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
			}
			continue
		}

		if len(capabilities) > 0 {
			s.capabilities = capabilities
		}
		return status, last, nil
	}
}

// commandName 从命令行里取出命令名，用于报错（**不能把整行打出去**：
// AUTH 行里带 base64 过的授权码）。
func commandName(line string) string {
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		return strings.ToUpper(line[:idx])
	}
	return strings.ToUpper(line)
}

// ---------------------------------------------------------------------------
// MIME 组装
// ---------------------------------------------------------------------------

const altBoundary = "FreeStockBoundaryAlt3b1e5d"

// buildMIME 组装完整报文（含 DATA 结尾的 "."）。
//
// 关键点（都是容易被反垃圾/被截断的地方）：
//   - 主题与显示名用 RFC 2047 的 Base64 编码（中文主题不编码会变乱码）
//   - 正文用 Base64（8bit 中文直接塞进 SMTP 会被某些服务器改写）
//   - **点号转义**：正文行首的 "." 必须写成 ".."，否则服务器会把它当成结束符
func buildMIME(msg Message, now time.Time) ([]byte, error) {
	var b strings.Builder

	writeHeader(&b, "From", formatAddress(msg.From, msg.FromName))
	writeHeader(&b, "To", strings.Join(msg.To, ", "))
	// ⚠ Subject 先做 RFC 2047 编码再落头：含非 ASCII 时整串会被 Base64 掉
	// （顺带把里面的裸 CRLF 也编码进正文，不会造成头注入）；
	// 纯 ASCII 时保留原样，只由 writeHeader 清掉裸换行。
	writeHeader(&b, "Subject", encodeHeader(msg.Subject))
	writeHeader(&b, "Date", now.Format(time.RFC1123Z))
	writeHeader(&b, "MIME-Version", "1.0")
	// 明确要求不要自动回复，减少"两个机器人互相回信"的场面
	writeHeader(&b, "Auto-Submitted", "auto-generated")
	writeHeader(&b, "X-Mailer", "FreeStock")

	hasHTML := strings.TrimSpace(msg.HTML) != ""
	hasText := strings.TrimSpace(msg.Text) != ""

	switch {
	case hasHTML && hasText:
		writeHeader(&b, "Content-Type",
			fmt.Sprintf("multipart/alternative; boundary=%q", altBoundary))
		b.WriteString("\r\n")
		b.WriteString("--" + altBoundary + "\r\n")
		writePart(&b, "text/plain; charset=UTF-8", msg.Text)
		b.WriteString("--" + altBoundary + "\r\n")
		writePart(&b, "text/html; charset=UTF-8", msg.HTML)
		b.WriteString("--" + altBoundary + "--\r\n")
	case hasHTML:
		writeHeader(&b, "Content-Type", "text/html; charset=UTF-8")
		writeHeader(&b, "Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		b.WriteString(wrapBase64(msg.HTML))
	default:
		writePart(&b, "text/plain; charset=UTF-8", msg.Text)
	}

	// DATA 的结束符：单独一行的 "."
	b.WriteString("\r\n.\r\n")
	return []byte(b.String()), nil
}

func writePart(b *strings.Builder, contentType, body string) {
	writeHeader(b, "Content-Type", contentType)
	writeHeader(b, "Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(body))
}

// wrapBase64 按 76 字符折行（RFC 2045 要求），并做点号转义。
func wrapBase64(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		line := encoded[i:end]
		// Base64 里不会出现行首的点，但保持这层防护：
		// 将来若改成 quoted-printable 就会需要它。
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	return b.String()
}

// writeHeader 写一条头，值里的裸换行会被去掉（防头注入）。
func writeHeader(b *strings.Builder, name, value string) {
	clean := strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	b.WriteString(name + ": " + clean + "\r\n")
}

// formatAddress 生成 "显示名 <地址>"，显示名按需要做 RFC 2047 编码。
func formatAddress(address, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return address
	}
	return encodeHeader(name) + " <" + address + ">"
}

// encodeHeader 在含非 ASCII 时做 RFC 2047 Base64 编码。
func encodeHeader(value string) string {
	if isASCII(value) {
		return value
	}
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 地址解析与校验
// ---------------------------------------------------------------------------

// ParseAddress 校验一个邮箱地址并返回它。
//
// **刻意不追求 RFC 5322 全集**（那需要几乎完整实现词法分析，且真实世界里
// 用不到）：这里只接受 `local@domain.tld`，域名至少有一段点号，
// 且两边都不含空白、@ 只能有一个。
//
// 这个严格度是合适的：它的唯一用途是**在发信之前把明显写错的地址挑出来**，
// 而不是拒绝某个合法的古怪地址。
func ParseAddress(raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return "", errors.New("地址为空")
	}
	// 允许 "显示名 <a@b.c>" 的写法，取尖括号里的部分
	if open := strings.LastIndex(addr, "<"); open >= 0 {
		if close := strings.Index(addr[open:], ">"); close > 0 {
			addr = strings.TrimSpace(addr[open+1 : open+close])
		}
	}
	if strings.Count(addr, "@") != 1 {
		return "", errors.New("必须恰好包含一个 @")
	}
	at := strings.Index(addr, "@")
	local, domain := addr[:at], addr[at+1:]
	if local == "" {
		return "", errors.New("@ 前面为空")
	}
	if domain == "" {
		return "", errors.New("@ 后面为空")
	}
	if strings.ContainsAny(addr, " \t\r\n,;，；") {
		return "", errors.New("含空白或分隔符")
	}
	if !strings.Contains(domain, ".") {
		return "", errors.New("域名缺少点号，形如 example.com")
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.HasPrefix(domain, "-") || strings.HasSuffix(domain, "-") {
		return "", errors.New("域名以点号或连字符开头/结尾")
	}
	if strings.Contains(domain, "..") {
		return "", errors.New("域名里有连续的点号")
	}
	for _, r := range addr {
		if r > 0x7f {
			return "", errors.New("含非 ASCII 字符（国际域名请改用 punycode 写法）")
		}
	}
	return addr, nil
}

// ParseRecipients 把多行/逗号分隔的收件人列表解析成地址切片。
//
// 返回 (合法地址, 非法原文)。**非法的不丢弃** —— 调用方要把它们原样
// 显示给用户（"以下地址不合法：a@b, x@"），静默丢掉只会让人以为发成功了。
func ParseRecipients(raw string) (valid []string, invalid []string) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '\n', '\r', ',', '，', ';', '；', '\t', ' ':
			return true
		}
		return false
	})
	seen := map[string]bool{}
	for _, field := range fields {
		item := strings.TrimSpace(field)
		if item == "" {
			continue
		}
		addr, err := ParseAddress(item)
		if err != nil {
			invalid = append(invalid, item)
			continue
		}
		if seen[strings.ToLower(addr)] {
			continue
		}
		seen[strings.ToLower(addr)] = true
		valid = append(valid, addr)
	}
	return valid, invalid
}
