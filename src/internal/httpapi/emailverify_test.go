package httpapi

// ---------- 注册邮箱验证码回归测试 ----------
//
// 覆盖 emailverify.go 的全部对外行为：验证码生成/校验、过期、重放、尝试次数、
// 发送限流、邮箱格式、SMTP 未配置时的拒绝、以及 /register 的邮箱唯一性。
//
// 所有用例都在进程内完成，不发任何真实邮件（send 被替换成内存捕获）。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 验证码本身 ----

func TestGenerateEmailCodeIsSixDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		code, err := generateEmailCode()
		if err != nil {
			t.Fatalf("generateEmailCode() error = %v", err)
		}
		if len(code) != 6 {
			t.Fatalf("验证码 = %q，长度 %d，want 6 位", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("验证码 %q 含非数字字符", code)
			}
		}
		seen[code] = true
	}
	// 2000 次里如果几乎都是同一个值，说明随机源根本没工作。
	if len(seen) < 100 {
		t.Fatalf("2000 次生成只出现 %d 个不同验证码，随机性不足", len(seen))
	}
}

func TestEmailVerifierAcceptsCorrectCodeOnce(t *testing.T) {
	sent := ""
	v := newEmailVerifier(func(email, code string) error { sent = code; return nil })
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("RegisterCode() error = %v", err)
	}
	if len(sent) != 6 {
		t.Fatalf("发出的验证码 = %q", sent)
	}
	if err := v.VerifyCode("user@example.com", sent); err != nil {
		t.Fatalf("正确验证码校验失败: %v", err)
	}
	// 一次性：同一个验证码不能再用第二次。
	err := v.VerifyCode("user@example.com", sent)
	var codeErr *EmailCodeError
	if !errors.As(err, &codeErr) || codeErr.Kind != "not_found" {
		t.Fatalf("重放验证码 err = %v, want not_found", err)
	}
}

func TestEmailVerifierRejectsWrongCode(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return nil })
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("RegisterCode() error = %v", err)
	}
	err := v.VerifyCode("user@example.com", "000000")
	var codeErr *EmailCodeError
	if !errors.As(err, &codeErr) || codeErr.Kind != "mismatch" {
		t.Fatalf("错误验证码 err = %v, want mismatch", err)
	}
}

func TestEmailVerifierRejectsCodeNeverSent(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return nil })
	err := v.VerifyCode("nobody@example.com", "123456")
	var codeErr *EmailCodeError
	if !errors.As(err, &codeErr) || codeErr.Kind != "not_found" {
		t.Fatalf("未发送就校验 err = %v, want not_found", err)
	}
}

func TestEmailVerifierExpiresAfterTenMinutes(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return nil })
	now := time.Now()
	v.nowFunc = func() time.Time { return now }
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("RegisterCode() error = %v", err)
	}
	// 9 分 59 秒后仍然有效。
	now = now.Add(emailCodeTTL - time.Second)
	if !v.Pending("user@example.com") {
		t.Fatal("有效期内的验证码被判为失效")
	}
	// 超过 10 分钟即过期。
	now = now.Add(2 * time.Second)
	err := v.VerifyCode("user@example.com", "whatever")
	var codeErr *EmailCodeError
	if !errors.As(err, &codeErr) || codeErr.Kind != "expired" {
		t.Fatalf("过期验证码 err = %v, want expired", err)
	}
}

func TestEmailVerifierInvalidatesAfterFiveWrongAttempts(t *testing.T) {
	sent := ""
	v := newEmailVerifier(func(email, code string) error { sent = code; return nil })
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("RegisterCode() error = %v", err)
	}
	for i := 0; i < emailCodeMaxAttempts; i++ {
		err := v.VerifyCode("user@example.com", "000000")
		var codeErr *EmailCodeError
		if !errors.As(err, &codeErr) {
			t.Fatalf("第 %d 次错误尝试 err = %v", i+1, err)
		}
	}
	// 超限后作废：即使拿到正确的验证码也不能通过。
	if err := v.VerifyCode("user@example.com", sent); err == nil {
		t.Fatal("尝试次数超限后正确验证码仍然通过，验证码未作废")
	}
}

func TestEmailVerifierSendRateLimit(t *testing.T) {
	sent := 0
	v := newEmailVerifier(func(email, code string) error { sent++; return nil })
	now := time.Now()
	v.nowFunc = func() time.Time { return now }

	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("首次发送 error = %v", err)
	}
	// 60 秒冷却：1 秒后再发一次必须 429。
	now = now.Add(time.Second)
	_, err := v.RegisterCode("user@example.com")
	var sendErr *emailCodeSendError
	if !errors.As(err, &sendErr) || sendErr.Status != http.StatusTooManyRequests {
		t.Fatalf("冷却期内发送 err = %v, want 429", err)
	}
	if !strings.Contains(sendErr.Message, "频繁") {
		t.Fatalf("限流提示 = %q，应包含中文说明", sendErr.Message)
	}

	// 冷却结束后逐次发送，1 小时窗口内第 6 次被拒。
	for i := 1; i < emailCodeHourLimit; i++ {
		now = now.Add(emailCodeCooldown)
		if _, err := v.RegisterCode("user@example.com"); err != nil {
			t.Fatalf("第 %d 次发送 error = %v", i+1, err)
		}
	}
	if sent != emailCodeHourLimit {
		t.Fatalf("1 小时内发送次数 = %d, want %d", sent, emailCodeHourLimit)
	}
	now = now.Add(emailCodeCooldown)
	_, err = v.RegisterCode("user@example.com")
	if !errors.As(err, &sendErr) || sendErr.Status != http.StatusTooManyRequests {
		t.Fatalf("超过 1 小时上限后发送 err = %v, want 429", err)
	}
	if !strings.Contains(sendErr.Message, "1 小时内最多") {
		t.Fatalf("小时限流提示 = %q", sendErr.Message)
	}

	// 窗口滑过后重新放行：再过 1 小时，前 5 次不再计入。
	now = now.Add(emailCodeHourWindow)
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("1 小时窗口过后发送 error = %v", err)
	}
}

func TestEmailVerifierRateLimitIsPerEmail(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return nil })
	if _, err := v.RegisterCode("a@example.com"); err != nil {
		t.Fatalf("a@example.com 首次发送 error = %v", err)
	}
	// 另一个邮箱不受 a@example.com 的冷却影响。
	if _, err := v.RegisterCode("b@example.com"); err != nil {
		t.Fatalf("b@example.com 发送 error = %v（限流应为按邮箱维度）", err)
	}
}

// 发信失败时回滚限流窗口，否则一次 SMTP 抖动就把用户锁在 60 秒冷却里。
func TestEmailVerifierSendFailureRollsBackRateLimit(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return errors.New("smtp down") })
	_, err := v.RegisterCode("user@example.com")
	var sendErr *emailCodeSendError
	if !errors.As(err, &sendErr) || sendErr.Status != http.StatusBadGateway {
		t.Fatalf("发送失败 err = %v, want 502", err)
	}
	if v.Pending("user@example.com") {
		t.Fatal("发送失败却留下了验证码记录")
	}
	// 换一个能发出去的 send：必须立刻可以重试，不该被冷却拦住。
	v.send = func(email, code string) error { return nil }
	if _, err := v.RegisterCode("user@example.com"); err != nil {
		t.Fatalf("失败后立即重试 error = %v", err)
	}
}

// ---- 邮箱格式 ----

func TestValidateEmail(t *testing.T) {
	valid := []string{
		"user@example.com",
		"first.last@sub.example.co.uk",
		"user+tag@example.com",
		"USER@EXAMPLE.COM",
	}
	for _, email := range valid {
		if err := ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q) = %v, want nil", email, err)
		}
	}
	invalid := []string{
		"",
		"   ",
		"plainaddress",
		"@example.com",
		"user@",
		"user@@example.com",
		"user@localhost",
		"user example@example.com",
		"user@example",
		".user@example.com",
		"user.@example.com",
		"us..er@example.com",
		"user@example..com",
		"user@-example.com",
		"user@example-.com",
		"user@exam ple.com",
		"<user@example.com>",
	}
	for _, email := range invalid {
		if err := ValidateEmail(email); err == nil {
			t.Errorf("ValidateEmail(%q) = nil, want error", email)
		}
	}
	// 长度上限 254：恰好 254 位可用，多一位即拒绝。
	// 64(local) + 1(@) + 域名；域名取 63.63.61 共 189 位，总长正好 254。
	maxLen := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(maxLen) != 254 {
		t.Fatalf("边界测试数据长度 = %d, want 254", len(maxLen))
	}
	if err := ValidateEmail(maxLen); err != nil {
		t.Errorf("254 位邮箱被判非法: %v", err)
	}
	over := maxLen + "m"
	if err := ValidateEmail(over); err == nil || !strings.Contains(err.Error(), "254") {
		t.Errorf("255 位邮箱 err = %v, want 长度错误", err)
	}
}

// ---- SMTP 配置解析 ----

func TestParseSMTPEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(key string) string { return env[key] }

	// 默认：未配置任何变量 → 不算已配置，端口默认 465，TLS 默认 true。
	cfg, err := parseSMTPEnv(getenv)
	if err != nil {
		t.Fatalf("parseSMTPEnv(empty) error = %v", err)
	}
	if cfg.Configured() {
		t.Fatal("空环境下 Configured() = true，SMTP 未配置必须能被识别")
	}
	if cfg.Port != 465 || !cfg.TLS {
		t.Fatalf("默认 Port=%d TLS=%v, want 465/true", cfg.Port, cfg.TLS)
	}

	env["YYB_SMTP_HOST"] = "smtp.example.com"
	env["YYB_SMTP_USERNAME"] = "noreply@example.com"
	env["YYB_SMTP_PASSWORD"] = "secret"
	env["YYB_SMTP_FROM"] = "noreply@example.com"
	cfg, err = parseSMTPEnv(getenv)
	if err != nil {
		t.Fatalf("parseSMTPEnv() error = %v", err)
	}
	if !cfg.Configured() || cfg.Port != 465 || !cfg.TLS {
		t.Fatalf("解析结果 = %#v", cfg)
	}

	env["YYB_SMTP_PORT"] = "587"
	env["YYB_SMTP_TLS"] = "false"
	cfg, err = parseSMTPEnv(getenv)
	if err != nil {
		t.Fatalf("parseSMTPEnv() error = %v", err)
	}
	if cfg.Port != 587 || cfg.TLS {
		t.Fatalf("Port=%d TLS=%v, want 587/false", cfg.Port, cfg.TLS)
	}

	// 非法端口 / 非法布尔值必须报错，而不是静默取默认值。
	env["YYB_SMTP_PORT"] = "not-a-port"
	if _, err := parseSMTPEnv(getenv); err == nil {
		t.Fatal("非法 YYB_SMTP_PORT 未被拒绝")
	}
	env["YYB_SMTP_PORT"] = "465"
	env["YYB_SMTP_TLS"] = "ture"
	if _, err := parseSMTPEnv(getenv); err == nil {
		t.Fatal("非法 YYB_SMTP_TLS 未被拒绝")
	}
}

func TestSMTPConfigRequiresAllFields(t *testing.T) {
	if !testSMTP.Configured() {
		t.Fatal("完整配置 Configured() = false")
	}
	missing := []SMTPConfig{
		{Host: "", Port: 465, Username: "u", Password: "p", From: "f@example.com"},
		{Host: "h", Port: 0, Username: "u", Password: "p", From: "f@example.com"},
		{Host: "h", Port: 465, Username: "", Password: "p", From: "f@example.com"},
		{Host: "h", Port: 465, Username: "u", Password: "", From: "f@example.com"},
		{Host: "h", Port: 465, Username: "u", Password: "p", From: ""},
	}
	for i, cfg := range missing {
		if cfg.Configured() {
			t.Errorf("缺失字段的第 %d 个配置 Configured() = true，应为 false：%#v", i+1, cfg)
		}
	}
}

// ---- HTTP 层 ----

func TestEmailCodeEndpointRejectsWithoutSMTP(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	// 不传 SMTP：等价于 YYB_SMTP_* 全未配置。
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	if app.EmailVerificationEnabled() {
		t.Fatal("未配置 SMTP 时 EmailVerificationEnabled() = true")
	}
	handler := app.Handler()

	rec := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"user@example.com"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 SMTP 时发码 status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "未配置邮件服务") {
		t.Fatalf("提示缺少「未配置邮件服务」：%s", rec.Body.String())
	}
}

func TestSendEmailCodeEndpoint(t *testing.T) {
	handler, app, lastCode := newEmailCodeApp(t)

	// GET 不允许。
	if rec := serveJSON(t, handler, http.MethodGet, "/api/auth/email-code", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/auth/email-code status = %d, want 405", rec.Code)
	}
	// 邮箱非法。
	if rec := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"not-an-email"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法邮箱 status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	// 正常发送。
	rec := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"User@Example.com"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("发码 status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var data struct {
		Sent      bool `json:"sent"`
		ExpiresIn int  `json:"expires_in"`
	}
	decodeData(t, rec, &data)
	if !data.Sent || data.ExpiresIn != int(emailCodeTTL.Seconds()) {
		t.Fatalf("发码响应 = %#v, want sent=true expires_in=600", data)
	}
	code := lastCode()
	if len(code) != 6 {
		t.Fatalf("发出的验证码 = %q", code)
	}
	// 邮箱大小写归一后仍然匹配。
	if !app.emailCode.Pending("user@example.com") {
		t.Fatal("规范化后的邮箱没有对应验证码")
	}
	// 立即再发一次：被 60 秒冷却拦住，返回 429。
	again := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"user@example.com"}`, nil)
	if again.Code != http.StatusTooManyRequests {
		t.Fatalf("冷却期内重复发送 status = %d, want 429 (body=%s)", again.Code, again.Body.String())
	}
}

// 无论邮箱是否已注册，发码响应都必须一模一样，否则注册页会变成邮箱枚举器。
func TestSendEmailCodeDoesNotLeakAccountExistence(t *testing.T) {
	handler, _, _ := newEmailCodeApp(t)
	known := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"someone@example.com"}`, nil)
	unknown := serveJSON(t, handler, http.MethodPost, "/api/auth/email-code", `{"email":"nobody@example.com"}`, nil)
	if known.Code != unknown.Code {
		t.Fatalf("已注册/未注册邮箱状态码不同：%d vs %d", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Fatalf("已注册/未注册邮箱响应体不同：%s vs %s", known.Body.String(), unknown.Body.String())
	}
}

// 注册限制：注册关闭时发码接口同样关闭。
func TestSendEmailCodeFollowsRegistrationPolicy(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	installCodeCapture(app)
	handler := app.Handler()

	// 库里没有账号、请求来自公网 → 首次注册引导不放行 → 403。
	rec := requestFrom(handler, http.MethodPost, "/api/auth/email-code", `{"email":"user@example.com"}`, "203.0.113.9:33333", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("注册关闭时发码 status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterRequiresEmailCode(t *testing.T) {
	handler, app, _ := newEmailCodeApp(t)
	email := "user@example.com"
	body := func(code string) string {
		return `{"username":"newuser","displayName":"New","password":"newuser-password-1","email":"` + email + `","code":"` + code + `"}`
	}

	// 1) 完全不带 code → 400，提示先获取验证码。
	rec := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"newuser","displayName":"New","password":"newuser-password-1","email":"`+email+`"}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "验证码") {
		t.Fatalf("缺 code 注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 2) 没发过验证码就注册 → 400「不存在或已失效」。
	rec = serveJSON(t, handler, http.MethodPost, "/register", body("123456"), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "不存在或已失效") {
		t.Fatalf("未发送就注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 3) 发码后填错 → 400「验证码错误」。
	code := requestEmailCode(t, handler, app, email)
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	rec = serveJSON(t, handler, http.MethodPost, "/register", body(wrong), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "验证码错误") {
		t.Fatalf("错误验证码注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 4) 正确验证码 → 201。
	rec = serveJSON(t, handler, http.MethodPost, "/register", body(code), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("正确验证码注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 5) 验证码一次性：同一个 code 不能再次用于另一个账号。
	secondEmail := "second@example.com"
	secondCode := requestEmailCode(t, handler, app, secondEmail)
	rec = serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"second","displayName":"Second","password":"second-password-1","email":"`+secondEmail+`","code":"`+secondCode+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("第二个账号注册 status = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"third","displayName":"Third","password":"third-password-1","email":"`+secondEmail+`","code":"`+secondCode+`"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重放验证码注册 status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterRejectsInvalidEmail(t *testing.T) {
	handler, _, _ := newEmailCodeApp(t)
	for _, email := range []string{"", "not-an-email", "user@localhost", "user@example"} {
		rec := serveJSON(t, handler, http.MethodPost, "/register",
			`{"username":"newuser","displayName":"New","password":"newuser-password-1","email":"`+email+`","code":"123456"}`, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("邮箱 %q 注册 status = %d, want 400 (body=%s)", email, rec.Code, rec.Body.String())
		}
	}
}

// SMTP 未配置时注册必须被拒绝，且提示明确（不允许静默跳过邮箱验证）。
func TestRegisterRejectedWhenSMTPNotConfigured(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	handler := app.Handler()

	rec := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"newuser","displayName":"New","password":"newuser-password-1","email":"user@example.com","code":"123456"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("SMTP 未配置时注册 status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "服务端未配置邮件服务，请联系管理员") {
		t.Fatalf("提示不明确：%s", rec.Body.String())
	}
	// 且确实没有创建任何账号。
	if count, err := app.auth.CountUsers(context.Background()); err != nil || count != 0 {
		t.Fatalf("拒绝注册后用户数 = %d, err = %v", count, err)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	handler, app, _ := newEmailCodeApp(t)
	email := "dup@example.com"
	code := requestEmailCode(t, handler, app, email)
	rec := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"first","displayName":"First","password":"first-password-1","email":"`+email+`","code":"`+code+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("首个账号注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 同一邮箱（换大小写）再注册一个不同用户名 → 400「该邮箱已被注册」。
	code2 := requestEmailCode(t, handler, app, "DUP@example.com")
	rec = serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"second","displayName":"Second","password":"second-password-1","email":"DUP@example.com","code":"`+code2+`"}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "该邮箱已被注册") {
		t.Fatalf("重复邮箱注册 status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// ---- 并发安全（-race 下也能跑） ----

func TestEmailVerifierConcurrentAccess(t *testing.T) {
	v := newEmailVerifier(func(email, code string) error { return nil })
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			email := "user" + string(rune('a'+n%26)) + "@example.com"
			_, _ = v.RegisterCode(email)
			_ = v.VerifyCode(email, "000000")
			_ = v.Pending(email)
		}(i)
	}
	wg.Wait()
}

// ---- 纯函数小工具 ----

func TestMaskEmailHidesLocalPart(t *testing.T) {
	if got := maskEmail("alice@example.com"); got != "a***@example.com" {
		t.Fatalf("maskEmail = %q", got)
	}
	if got := maskEmail("a@example.com"); got != "*@example.com" {
		t.Fatalf("maskEmail = %q", got)
	}
	if got := maskEmail("broken"); got != "***" {
		t.Fatalf("maskEmail = %q", got)
	}
}

func TestBuildVerificationMailContainsCode(t *testing.T) {
	mail := buildVerificationMail(testSMTP, "user@example.com", "123456")
	if !strings.Contains(mail, "123456") {
		t.Fatalf("邮件正文缺少验证码：%q", mail)
	}
	if !strings.Contains(mail, "To: user@example.com") || !strings.Contains(mail, "From: noreply@example.com") {
		t.Fatalf("邮件头不完整：%q", mail)
	}
	// 中文主题必须做 RFC 2047 编码，否则部分客户端乱码。
	if !strings.Contains(mail, "=?UTF-8?B?") {
		t.Fatalf("中文主题未做 MIME 编码：%q", mail)
	}
}
