package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ---- 邮件（SMTP）设置接口的测试 ----
//
// 这一组用例锁住三件事，任何一件被改坏都意味着「管理员配好了邮件但实际发不出去」：
//  1. 只有管理员能读写（含密码，绝不能对普通用户开放）；
//  2. GET 永不回显密码，只给 password_set 布尔值；
//  3. 保存后的设置必须**立即生效**——注册路径要能观察到「已配置」这个状态变化。

// smtpAdminSession 建一个管理员应用并返回已登录的管理员会话 cookie。
func smtpAdminSession(t *testing.T, cfg Config) (http.Handler, *App, *http.Cookie) {
	t.Helper()
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	handler := app.Handler()
	cookie := loginAsAdmin(t, handler, app)
	return handler, app, cookie
}

// loginAsAdmin 用随机的管理员凭据登录，返回会话 cookie。
func loginAsAdmin(t *testing.T, handler http.Handler, app *App) *http.Cookie {
	t.Helper()
	const user, pass = "admin", "AdminPassw0rd123"
	if err := app.auth.BootstrapAdmin(app.settingsCtx(), user, pass); err != nil {
		// 库中已有账号时 BootstrapAdmin 是 no-op；继续用既有账号登录。
	}
	rec := serveWithHeaders(t, handler, http.MethodPost, "/login",
		`{"username":"`+user+`","password":"`+pass+`","next":"/"}`, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie in login response")
	return nil
}

// decodeSMTPView 把 /api/auth/smtp 的 GET 响应解成 map，避免为测试再引入一套结构体。
func decodeSMTPView(t *testing.T, body string) map[string]any {
	t.Helper()
	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode smtp view: %v (body=%s)", err, body)
	}
	if envelope.Code != 0 {
		t.Fatalf("smtp view code = %d (body=%s)", envelope.Code, body)
	}
	return envelope.Data
}

// TestSMTPSettingsGetNeverLeaksPassword 锁住「密码只进不出」这条硬约束。
func TestSMTPSettingsGetNeverLeaksPassword(t *testing.T) {
	handler, _, cookie := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP,
	})
	rec := serveWithHeaders(t, handler, http.MethodGet, "/api/auth/smtp", "", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/smtp status = %d", rec.Code)
	}
	body := rec.Body.String()
	// 明文密码绝不能出现在响应里 —— 这是最容易在重构中被"顺手带上"的字段。
	if strings.Contains(body, testSMTP.Password) {
		t.Fatalf("GET /api/auth/smtp leaked the SMTP password: %s", body)
	}
	view := decodeSMTPView(t, body)
	if view["password_set"] != true {
		t.Fatalf("password_set = %v, want true (password exists but must not be echoed)", view["password_set"])
	}
	if view["configured"] != true {
		t.Fatalf("configured = %v, want true", view["configured"])
	}
}

// TestSMTPSettingsRequiresAdmin 证明普通用户与匿名都不能碰邮件设置。
func TestSMTPSettingsRequiresAdmin(t *testing.T) {
	// AllowRegistration 打开：本用例需要真的注册出一个普通用户来验证越权被挡，
	// 注册开关本身另有测试覆盖（与 api_token_test.go 的处理一致）。
	handler, app, _ := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true,
		SMTP: testSMTP, AllowRegistration: true,
	})
	// 1) 匿名
	anon := serveWithHeaders(t, handler, http.MethodGet, "/api/auth/smtp", "", nil, nil)
	if anon.Code != http.StatusUnauthorized && anon.Code != http.StatusForbidden {
		t.Fatalf("anonymous GET status = %d, want 401/403", anon.Code)
	}
	// 2) 普通用户
	userCookie := registerOrdinaryUser(t, handler, app)
	member := serveWithHeaders(t, handler, http.MethodGet, "/api/auth/smtp", "", nil, userCookie)
	if member.Code != http.StatusForbidden {
		t.Fatalf("ordinary user GET status = %d, want 403 (body=%s)", member.Code, member.Body.String())
	}
	// 3) 普通用户也不能写
	write := serveWithHeaders(t, handler, http.MethodPut, "/api/auth/smtp",
		`{"host":"evil.example.com"}`, nil, userCookie)
	if write.Code != http.StatusForbidden {
		t.Fatalf("ordinary user PUT status = %d, want 403", write.Code)
	}
}

// registerOrdinaryUser 注册并返回一个普通用户的会话 cookie。
// 注意：首个注册者会被提升为管理员，所以这里先确保已有管理员（smtpAdminSession 已 Bootstrap 过）。
func registerOrdinaryUser(t *testing.T, handler http.Handler, app *App) *http.Cookie {
	t.Helper()
	// 复用项目已有的取码助手：它把发信替换为「只记录不发送」，并真的走一遍发码接口。
	capture := installCodeCapture(app)
	email := "member@example.com"
	rec := serveWithHeaders(t, handler, http.MethodPost, "/api/auth/email-code",
		`{"email":"`+email+`"}`, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("send email code status = %d, body = %s", rec.Code, rec.Body.String())
	}
	code := capture()
	if code == "" {
		t.Fatalf("email code was not captured")
	}
	rec = serveWithHeaders(t, handler, http.MethodPost, "/register",
		`{"username":"member01","displayName":"Member","email":"`+email+`","password":"Password12345","code":"`+code+`"}`, nil, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register ordinary user status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie for registered user")
	return nil
}

// TestSMTPSettingsSaveTakesEffectImmediately 是本功能最关键的用例：
// 保存设置后，注册路径必须立刻从「未配置」变成「已配置」——
// 否则会出现「设置页显示已保存、用户注册仍被拒」这种最难排查的状态。
func TestSMTPSettingsSaveTakesEffectImmediately(t *testing.T) {
	// 故意不传 SMTP：模拟「部署时没配环境变量」的真实场景。
	app, err := NewApp(Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, AllowRegistration: true,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	handler := app.Handler()
	cookie := loginAsAdmin(t, handler, app)

	// 初始状态：未配置。
	before := decodeSMTPView(t, serveWithHeaders(t, handler, http.MethodGet, "/api/auth/smtp", "", nil, cookie).Body.String())
	if before["configured"] != false {
		t.Fatalf("initial configured = %v, want false", before["configured"])
	}

	// 保存一套完整设置。
	saved := serveWithHeaders(t, handler, http.MethodPut, "/api/auth/smtp",
		`{"host":"smtp.example.com","port":465,"username":"u@example.com","password":"secret","from":"u@example.com","tls":true}`,
		nil, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("PUT /api/auth/smtp status = %d, body = %s", saved.Code, saved.Body.String())
	}
	after := decodeSMTPView(t, saved.Body.String())
	if after["configured"] != true {
		t.Fatalf("configured after save = %v, want true", after["configured"])
	}

	// 关键断言：注册路径现在必须认为自己「有邮件服务」。
	// 未配置时返回的是 smtpNotConfiguredMessage；配置后应当变成验证码相关提示。
	rec := serveWithHeaders(t, handler, http.MethodPost, "/register",
		`{"username":"somebody01","displayName":"Some Body","email":"x@example.com","password":"Password12345","code":"000000"}`,
		nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), smtpNotConfiguredMessage) {
		t.Fatalf("register still reports SMTP unconfigured after saving settings: %s", rec.Body.String())
	}
}

// TestSMTPSettingsValidation 挡掉明显填错的配置，避免存进一个永远发不出去的半成品。
func TestSMTPSettingsValidation(t *testing.T) {
	handler, _, cookie := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP,
	})
	cases := []struct{ name, body string }{
		{"bad port", `{"port":70000}`},
		{"empty host", `{"host":""}`},
		{"empty username", `{"username":""}`},
		{"from without at", `{"from":"nope"}`},
		{"no fields", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveWithHeaders(t, handler, http.MethodPut, "/api/auth/smtp", tc.body, nil, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT %s status = %d, want 400 (body=%s)", tc.body, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSMTPSettingsKeepsPasswordWhenBlank 锁住「密码留空 = 不修改」的语义。
// 若实现被改成「留空即清空」，管理员改一次端口就会把密码抹掉、注册静默失效。
func TestSMTPSettingsKeepsPasswordWhenBlank(t *testing.T) {
	handler, _, cookie := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP,
	})
	// 只改端口，不带 password 字段。
	rec := serveWithHeaders(t, handler, http.MethodPut, "/api/auth/smtp", `{"port":587}`, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}
	view := decodeSMTPView(t, rec.Body.String())
	if view["password_set"] != true {
		t.Fatalf("password_set = %v after updating only the port, want true", view["password_set"])
	}
	if view["port"] != float64(587) {
		t.Fatalf("port = %v, want 587", view["port"])
	}
	if view["configured"] != true {
		t.Fatalf("configured = %v, want true", view["configured"])
	}
}

// TestSMTPSettingsRejectsNonPUTMethods 保证只有约定的方法可用。
func TestSMTPSettingsRejectsNonPUTMethods(t *testing.T) {
	handler, _, cookie := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP,
	})
	rec := serveWithHeaders(t, handler, http.MethodDelete, "/api/auth/smtp", "", nil, cookie)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", rec.Code)
	}
}
