package httpapi

// 邮箱验证码：注册流程的「邮箱 + 邮箱验证码」后端实现。
//
// 设计要点（对应验收标准）：
//  1. 验证码为 6 位数字，用 crypto/rand 生成；服务端只保存 sha256(code)，
//     内存里没有明文验证码。
//  2. 有效期 10 分钟；校验成功立即作废（一次性）；同一验证码最多尝试 5 次，
//     超限直接作废。
//  3. 发送限流按邮箱做：60 秒内最多 1 次，1 小时内最多 5 次，超限返回 429。
//  4. 纯进程内状态（map + mutex），与 auth.go 里 loginAttempts 的写法一致：
//     多副本部署时每个副本各自计数，验证码也只在本副本内有效——这是
//     「不引入 Redis 等新依赖」下的明确取舍，单容器部署不受影响。
//  5. SMTP 未配置时【拒绝】发送，也【拒绝】注册，绝不静默跳过邮箱验证。

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// emailCodeTTL 验证码有效期。
	emailCodeTTL = 10 * time.Minute
	// emailCodeMaxAttempts 单个验证码允许的校验尝试次数，超限作废。
	emailCodeMaxAttempts = 5
	// emailCodeCooldown 同一邮箱两次发送之间的最小间隔。
	emailCodeCooldown = 60 * time.Second
	// emailCodeHourWindow / emailCodeHourLimit 同一邮箱 1 小时内的发送上限。
	emailCodeHourWindow = time.Hour
	emailCodeHourLimit  = 5
	// emailCodeEntriesMax 给内存表兜底，防止被刷爆。
	emailCodeEntriesMax = 4096
	// emailMaxLength 邮箱长度上限，取自 RFC 5321 的可寻址上限。
	emailMaxLength = 254
	// emailLabelMaxLength / emailDomainMaxLength 域名与标签长度上限。
	emailLabelMaxLength  = 63
	emailDomainMaxLength = 253
	// smtpDialTimeout 建立 SMTP 连接的超时。
	smtpDialTimeout = 15 * time.Second
)

// SMTP 配置错误：分开定义是为了让调用方能区分「没配置」与「配置错了」，
// 两种情况的用户提示不一样（前者提示联系管理员，后者是运维配置问题）。
var (
	errSMTPNotConfigured = errors.New("smtp not configured")
	errSMTPIncomplete    = errors.New("smtp configuration incomplete")
)

// EmailCodeError 描述一次验证码校验失败的原因。
// 用带类型的错误而不是字符串比较，测试与调用方都不依赖提示文案。
type EmailCodeError struct {
	// Kind 取值：not_found（还没发过）/ expired（已过期）/ mismatch（验证码错误）/ too_many_attempts（尝试次数超限）
	Kind    string
	Message string
}

func (e *EmailCodeError) Error() string { return e.Message }

func newEmailCodeError(kind, message string) *EmailCodeError {
	return &EmailCodeError{Kind: kind, Message: message}
}

// emailCodeSendError 描述一次发送失败，并带上给 HTTP 层用的状态码与中文提示。
type emailCodeSendError struct {
	Status  int
	Message string
	Err     error
}

func (e *emailCodeSendError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *emailCodeSendError) Unwrap() error { return e.Err }

// emailCodeEntry 是服务端保存的一条验证码记录。
// 只存 sha256(code) 哈希：内存转储或日志泄漏都拿不到可用验证码。
type emailCodeEntry struct {
	Hash      [32]byte
	ExpiresAt time.Time
	Attempts  int
	CreatedAt time.Time
}

// emailSendRecord 记录某个邮箱的发送次数，用于限流。
type emailSendRecord struct {
	LastSent time.Time
	HourFrom time.Time
	HourSent int
}

// emailVerifier 保存验证码与发送限流的进程内状态。
type emailVerifier struct {
	mu      sync.Mutex
	codes   map[string]emailCodeEntry
	sends   map[string]emailSendRecord
	send    func(email, code string) error
	nowFunc func() time.Time // 测试注入用；nil 表示 time.Now
}

func newEmailVerifier(send func(email, code string) error) *emailVerifier {
	return &emailVerifier{
		codes: map[string]emailCodeEntry{},
		sends: map[string]emailSendRecord{},
		send:  send,
	}
}

// setSend 在运行时替换发信函数，供管理员在界面上改完 SMTP 设置后立即生效。
//
// 为什么不整体重建 verifier：codes / sends 两张表里存着「已发出的验证码」和
// 「发送限流窗口」，整体重建会把这些内存状态清空——正等着收码的用户会被迫重发，
// 攻击者也能借一次设置变更绕过限流。这里只换发信函数，状态保持不动。
func (v *emailVerifier) setSend(send func(email, code string) error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.send = send
}

func (v *emailVerifier) now() time.Time {
	if v.nowFunc != nil {
		return v.nowFunc()
	}
	return time.Now()
}

// RegisterCode 生成、保存并通过 send 发送一枚验证码，返回有效期。
//
// 顺序很重要：先占限流窗口、再发信、最后才把验证码写进表。
// 发送失败时会回滚限流窗口，也不会留下一枚「用户收不到但服务端认」的幽灵验证码。
func (v *emailVerifier) RegisterCode(email string) (time.Duration, error) {
	email = normalizeEmail(email)
	if v.send == nil {
		return 0, &emailCodeSendError{Status: http.StatusServiceUnavailable, Message: smtpNotConfiguredMessage, Err: errSMTPNotConfigured}
	}
	code, err := generateEmailCode()
	if err != nil {
		return 0, &emailCodeSendError{Status: http.StatusInternalServerError, Message: "验证码生成失败，请稍后重试", Err: err}
	}
	now := v.now()

	v.mu.Lock()
	if wait, ok := v.sendLimitLocked(email, now); !ok {
		v.mu.Unlock()
		return 0, &emailCodeSendError{
			Status:  http.StatusTooManyRequests,
			Message: "验证码发送过于频繁，" + wait + "，请稍后重试",
		}
	}
	v.markSentLocked(email, now)
	v.mu.Unlock()

	if err := v.send(email, code); err != nil {
		v.mu.Lock()
		v.rollbackSendLocked(email, now)
		v.mu.Unlock()
		return 0, &emailCodeSendError{Status: http.StatusBadGateway, Message: "验证码邮件发送失败，请稍后重试或联系管理员", Err: err}
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.evictCodesLocked(now)
	v.codes[email] = emailCodeEntry{Hash: hashEmailCode(code), ExpiresAt: now.Add(emailCodeTTL), CreatedAt: now}
	return emailCodeTTL, nil
}

// VerifyCode 校验验证码。校验通过时立即作废（一次性），失败则累加尝试次数，
// 达到 emailCodeMaxAttempts 后同样作废——不给暴力枚举留窗口。
func (v *emailVerifier) VerifyCode(email, code string) error {
	email = normalizeEmail(email)
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.codes[email]
	if !ok {
		return newEmailCodeError("not_found", "验证码不存在或已失效，请先获取验证码")
	}
	now := v.now()
	if now.After(entry.ExpiresAt) {
		delete(v.codes, email)
		return newEmailCodeError("expired", "验证码已过期，请重新获取")
	}
	if entry.Attempts >= emailCodeMaxAttempts {
		delete(v.codes, email)
		return newEmailCodeError("too_many_attempts", "验证码错误次数过多，请重新获取")
	}
	want := hashEmailCode(code)
	entry.Attempts++
	if subtle.ConstantTimeCompare(entry.Hash[:], want[:]) != 1 {
		if entry.Attempts >= emailCodeMaxAttempts {
			delete(v.codes, email)
			return newEmailCodeError("too_many_attempts", "验证码错误次数过多，请重新获取")
		}
		v.codes[email] = entry
		return newEmailCodeError("mismatch", "验证码错误，请检查后重试")
	}
	// 一次性：校验成功后立即作废，重放必然落到 not_found。
	delete(v.codes, email)
	return nil
}

// Pending 返回该邮箱当前是否有一枚未过期的验证码。
func (v *emailVerifier) Pending(email string) bool {
	email = normalizeEmail(email)
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.codes[email]
	if !ok {
		return false
	}
	if v.now().After(entry.ExpiresAt) {
		delete(v.codes, email)
		return false
	}
	return true
}

// sendLimitLocked 判断这次发送是否被限流：60 秒冷却 + 1 小时 5 次。
// 返回 false 时第二个值是需要等待多久的中文提示片段。
func (v *emailVerifier) sendLimitLocked(email string, now time.Time) (string, bool) {
	record, ok := v.sends[email]
	if !ok {
		if len(v.sends) >= emailCodeEntriesMax {
			v.evictSendRecordsLocked(now)
		}
		return "", true
	}
	if elapsed := now.Sub(record.LastSent); elapsed < emailCodeCooldown {
		remain := int((emailCodeCooldown - elapsed + time.Second - 1) / time.Second)
		if remain < 1 {
			remain = 1
		}
		return "请 " + strconv.Itoa(remain) + " 秒后再试", false
	}
	if record.HourFrom.IsZero() || now.Sub(record.HourFrom) >= emailCodeHourWindow {
		return "", true
	}
	if record.HourSent >= emailCodeHourLimit {
		return "该邮箱 1 小时内最多发送 " + strconv.Itoa(emailCodeHourLimit) + " 次，请稍后再试", false
	}
	return "", true
}

func (v *emailVerifier) markSentLocked(email string, now time.Time) {
	record := v.sends[email]
	if record.HourFrom.IsZero() || now.Sub(record.HourFrom) >= emailCodeHourWindow {
		record.HourFrom = now
		record.HourSent = 0
	}
	record.LastSent = now
	record.HourSent++
	v.sends[email] = record
}

// rollbackSendLocked 在真正发信失败时回滚刚占用的限流窗口。
// 不这样做的话，SMTP 一次短暂报错就会把用户锁在 60 秒冷却里，
// 而他其实一封邮件都没收到。
func (v *emailVerifier) rollbackSendLocked(email string, now time.Time) {
	record, ok := v.sends[email]
	if !ok {
		return
	}
	if now.Sub(record.LastSent) > 2*time.Second {
		return // 记录已经被后续请求覆盖，别乱动
	}
	record.HourSent--
	if record.HourSent <= 0 {
		delete(v.sends, email)
		return
	}
	record.LastSent = time.Time{}
	v.sends[email] = record
}

// evictCodesLocked 在验证码表满时清理过期项；仍然满就整体重置。
// 宁可让个别用户重新获取验证码，也不让表无限增长。
func (v *emailVerifier) evictCodesLocked(now time.Time) {
	if len(v.codes) < emailCodeEntriesMax {
		return
	}
	for email, entry := range v.codes {
		if now.After(entry.ExpiresAt) {
			delete(v.codes, email)
		}
	}
	if len(v.codes) >= emailCodeEntriesMax {
		v.codes = map[string]emailCodeEntry{}
	}
}

func (v *emailVerifier) evictSendRecordsLocked(now time.Time) {
	for email, record := range v.sends {
		if now.Sub(record.LastSent) >= emailCodeHourWindow && now.Sub(record.HourFrom) >= emailCodeHourWindow {
			delete(v.sends, email)
		}
	}
	if len(v.sends) >= emailCodeEntriesMax {
		v.sends = map[string]emailSendRecord{}
	}
}

// generateEmailCode 生成 6 位数字验证码。
//
// 取值用 crypto/rand + 拒绝采样：直接对 1000000 取模会让低位数值比高位多出现
// 一次（modulo bias），偏差只有百万分之一，但拒绝采样几乎零成本，没理由不写对。
func generateEmailCode() (string, error) {
	// 2^64 不是 1000000 的整数倍，只接受 [0, limit) 区间内的均匀样本。
	const limit = (1 << 64) / 1000000 * 1000000
	var buf [8]byte
	for i := 0; i < 64; i++ {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		value := binary.BigEndian.Uint64(buf[:])
		if value >= limit {
			continue
		}
		return padEmailCode(value % 1000000), nil
	}
	return "", errors.New("crypto/rand unavailable")
}

func padEmailCode(value uint64) string {
	out := strconv.FormatUint(value, 10)
	for len(out) < 6 {
		out = "0" + out
	}
	return out
}

func hashEmailCode(code string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimSpace(code)))
}

// normalizeEmail 统一小写并去空白，保证 a@B.com 与 A@b.com 是同一个账号。
func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ValidateEmail 校验邮箱：基本 RFC 形态 + 长度上限（整体 254）。
//
// 只做「形态」校验，不做 DNS/MX 探测：唯一能真正证明邮箱可用的方式，
// 就是往它发一封验证码邮件——那正是本流程在做的事。
func ValidateEmail(value string) error {
	email := strings.TrimSpace(value)
	if email == "" {
		return errors.New("请填写邮箱")
	}
	if len(email) > emailMaxLength {
		return errors.New("邮箱长度不能超过 254 个字符")
	}
	for i := 0; i < len(email); i++ {
		ch := email[i]
		if ch <= 0x20 || ch >= 0x7f {
			return errors.New("邮箱格式不正确")
		}
	}
	if strings.ContainsAny(email, ",;<>\"()[]\\") {
		return errors.New("邮箱格式不正确")
	}
	at := strings.Index(email, "@")
	if at <= 0 || at != strings.LastIndex(email, "@") || at == len(email)-1 {
		return errors.New("邮箱格式不正确")
	}
	local, domain := email[:at], email[at+1:]
	if len(local) > 64 {
		return errors.New("邮箱格式不正确")
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return errors.New("邮箱格式不正确")
	}
	if len(domain) > emailDomainMaxLength || strings.Contains(domain, "..") {
		return errors.New("邮箱格式不正确")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		// 必须带顶级域：放行 user@localhost 只会让注册进来的邮箱
		// 根本收不到验证码，属于无效配置。
		return errors.New("邮箱格式不正确")
	}
	for _, label := range labels {
		if label == "" || len(label) > emailLabelMaxLength {
			return errors.New("邮箱格式不正确")
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
				continue
			}
			return errors.New("邮箱格式不正确")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("邮箱格式不正确")
		}
	}
	return nil
}

// ---- SMTP 配置与发送 ----

const smtpNotConfiguredMessage = "服务端未配置邮件服务，请联系管理员"

// SMTPConfig 是 SMTP 发信配置，全部来自环境变量（见 cmd/yyb-go/main.go）。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// TLS 为 true 时用 SMTPS（465，连接建立即 TLS）；
	// 为 false 时用明文端口 + STARTTLS 升级（587/25）。
	TLS bool
}

// Configured 表示配置是否完整到足以发送验证码。
// 只要任一必填项缺失就视为「未配置」，注册会被拒绝而不是静默放行。
func (c SMTPConfig) Configured() bool {
	if strings.TrimSpace(c.Host) == "" || c.Port <= 0 {
		return false
	}
	if strings.TrimSpace(c.Username) == "" || c.Password == "" {
		return false
	}
	return strings.TrimSpace(c.From) != ""
}

// mailSender 抽象「把验证码邮件发出去」，便于在测试里不碰网络。
type mailSender interface {
	SendVerificationCode(email, code string) error
}

// smtpSender 用标准库 net/smtp 发信，不引入任何新依赖。
type smtpSender struct {
	cfg SMTPConfig
}

func newSMTPSender(cfg SMTPConfig) *smtpSender { return &smtpSender{cfg: cfg} }

func (s *smtpSender) SendVerificationCode(email, code string) error {
	return sendVerificationMail(s.cfg, email, code)
}

// sendVerificationMail 真正建立 SMTP 连接并投递邮件。
//
// 依赖 net/smtp（标准库）。该包已被 Go 官方标记为 deprecated，但按任务要求
// 不引入新依赖；同文件用 crypto/tls 手工完成握手与加密。
func sendVerificationMail(cfg SMTPConfig, email, code string) error {
	if !cfg.Configured() {
		return errSMTPNotConfigured
	}
	host := strings.TrimSpace(cfg.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port))
	auth := smtp.PlainAuth("", strings.TrimSpace(cfg.Username), cfg.Password, host)
	payload := []byte(buildVerificationMail(cfg, email, code))

	var (
		client *smtp.Client
		err    error
	)
	if cfg.TLS {
		// 465：连接建立后立即 TLS（SMTPS）。
		conn, dialErr := tlsDial(addr, host, smtpDialTimeout)
		if dialErr != nil {
			return dialErr
		}
		client, err = smtp.NewClient(conn, host)
	} else {
		// 587/25：先明文建连，再 STARTTLS 升级。
		client, err = smtp.Dial(addr)
	}
	if err != nil {
		return err
	}
	defer client.Close()

	if !cfg.TLS {
		if err = client.StartTLS(tlsConfigFor(host)); err != nil {
			return err
		}
	}
	if err = client.Auth(auth); err != nil {
		return err
	}
	if err = client.Mail(strings.TrimSpace(cfg.From)); err != nil {
		return err
	}
	if err = client.Rcpt(strings.TrimSpace(email)); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(payload); err != nil {
		_ = writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// buildVerificationMail 生成一封最简 MIME 邮件（纯文本 + UTF-8）。
// Subject 走 RFC 2047 编码，避免中文主题在部分客户端乱码。
func buildVerificationMail(cfg SMTPConfig, email, code string) string {
	date := time.Now().Format(time.RFC1123Z)
	var b strings.Builder
	b.WriteString("From: " + strings.TrimSpace(cfg.From) + "\r\n")
	b.WriteString("To: " + strings.TrimSpace(email) + "\r\n")
	b.WriteString("Subject: " + encodeMailHeader("YYB Go 邮箱验证码") + "\r\n")
	b.WriteString("Date: " + date + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString("您的邮箱验证码是：" + code + "\r\n\r\n")
	b.WriteString("验证码 10 分钟内有效，仅可使用一次。\r\n")
	b.WriteString("如果这不是您本人的操作，请忽略本邮件。\r\n")
	return b.String()
}

// encodeMailHeader 做 RFC 2047 的 Base64 编码，主题里出现非 ASCII 时必须编码。
func encodeMailHeader(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
		}
	}
	return value
}

// ---- App 上的对外方法 ----

// emailCodeFor 返回注册验证码处理器；main.go 广播模式（auth == nil）下返回 nil，
// 此时邮箱验证码在语义上不适用（那个模式根本没有账号体系）。
func (a *App) emailCodeFor() *emailVerifier {
	if a.auth == nil {
		return nil
	}
	return a.emailCode
}

// EmailVerificationEnabled 表示服务端是否具备完成邮箱验证的条件。
// 前端可据此在注册页提示「邮件服务未配置」，但即便前端不判断，
// 后端也会在发送与注册两处硬拒（见 handleEmailCode / handleRegister）。
func (a *App) EmailVerificationEnabled() bool {
	return a.emailCodeFor() != nil && a.activeSMTPConfig().Configured()
}

// handleEmailCode 处理 POST /api/auth/email-code：发送一枚注册验证码。
//
// 挂载在「无需浏览器会话」的路由组（注册页没有会话）。
// 响应刻意不携带任何账号存在性信息：无论邮箱是否已注册，返回体完全一致，
// 避免把注册页变成邮箱枚举器。
func (a *App) handleEmailCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	verifier := a.emailCodeFor()
	if verifier == nil {
		writeError(w, http.StatusServiceUnavailable, "当前模式不支持邮箱验证注册")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	email := normalizeEmail(body.Email)
	if err := ValidateEmail(email); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 注册被关闭时发码接口同样关闭：否则注册页会先发出一封永远用不上的邮件。
	if allowed, reason := a.registrationAllowed(r); !allowed {
		writeError(w, http.StatusForbidden, reason)
		return
	}
	ttl, err := verifier.RegisterCode(email)
	if err != nil {
		var sendErr *emailCodeSendError
		if errors.As(err, &sendErr) {
			if sendErr.Status >= http.StatusInternalServerError {
				log.Printf("[邮箱验证码] 发送到 %s 失败：%v", maskEmail(email), err)
			}
			writeError(w, sendErr.Status, sendErr.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "验证码发送失败，请稍后重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sent":       true,
		"expires_in": int(ttl.Seconds()),
	})
}

// verifyRegistrationEmail 在注册前校验邮箱验证码。
// 返回的 error 是给用户看的中文提示，HTTP 层统一按 400 返回。
func (a *App) verifyRegistrationEmail(email, code string) error {
	verifier := a.emailCodeFor()
	if verifier == nil {
		return errors.New("当前模式不支持邮箱验证注册")
	}
	// 用「当前生效」的配置判断，而不是启动时的环境变量：管理员在界面上配好 SMTP 后，
	// 注册必须立刻可用，否则会出现「设置页显示已配置、注册却仍报未配置」的矛盾。
	if !a.activeSMTPConfig().Configured() {
		return errors.New(smtpNotConfiguredMessage)
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("请填写邮箱验证码")
	}
	return verifier.VerifyCode(email, code)
}

// maskEmail 只保留首字符与域名，日志里不落完整邮箱。
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := email[:at], email[at+1:]
	if len(local) <= 1 {
		return "*@" + domain
	}
	return local[:1] + "***@" + domain
}

// ---- 环境变量解析 ----

// parseSMTPEnv 从环境变量解析 SMTP 配置。
//
// getenv 为 nil 时读取进程环境；测试传入自定义函数即可完全隔离环境。
func parseSMTPEnv(getenv func(string) string) (SMTPConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := SMTPConfig{
		Host:     strings.TrimSpace(getenv("YYB_SMTP_HOST")),
		Port:     465,
		Username: strings.TrimSpace(getenv("YYB_SMTP_USERNAME")),
		Password: getenv("YYB_SMTP_PASSWORD"),
		From:     strings.TrimSpace(getenv("YYB_SMTP_FROM")),
		TLS:      true,
	}
	if raw := strings.TrimSpace(getenv("YYB_SMTP_PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 || port > 65535 {
			return cfg, errors.New("YYB_SMTP_PORT 必须是 1-65535 的整数")
		}
		cfg.Port = port
	}
	if raw := strings.TrimSpace(getenv("YYB_SMTP_TLS")); raw != "" {
		value, err := parseBoolEnv(raw)
		if err != nil {
			return cfg, errors.New("YYB_SMTP_TLS 只能是 true/false（或 1/0、yes/no、on/off）")
		}
		cfg.TLS = value
	}
	return cfg, nil
}

// parseBoolEnv 解析布尔环境变量；无法识别时报错而不是静默取默认值。
//
// 这里与 main.go 的 envBool 有一处刻意的区别：envBool 用于「安全开关」，
// 认不出来就当 false（关闭）；而 YYB_SMTP_TLS=false 意味着走明文 STARTTLS，
// 静默把拼错的 "ture" 当成 true 会让运维以为加密链路是通的。
func parseBoolEnv(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

// ParseSMTPConfig 是 parseSMTPEnv 的导出包装，供 cmd/yyb-go 在启动时解析配置。
// 解析失败（端口非法 / 布尔值无法识别）应当让进程启动失败：带着错误的邮件
// 配置跑起来，用户只会看到「验证码发不出去」而不知道原因。
func ParseSMTPConfig(getenv func(string) string) (SMTPConfig, error) {
	return parseSMTPEnv(getenv)
}
