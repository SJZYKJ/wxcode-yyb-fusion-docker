package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 安全默认值回归测试 ----------
//
// 覆盖 security.go / api_token.go / auth.go 里的「安全默认值」：
// 令牌 fail-closed、/wxcode/* 纳入鉴权、注册引导窗口、开放重定向、
// 登录限速不信任 X-Forwarded-For。

// localTestRemoteAddr 模拟「从局域网访问」的来源地址。
// httptest.NewRequest 默认给 192.0.2.1（TEST-NET-1，公网段），
// 那会被「首次注册只允许内网」的引导规则正确挡掉。
const localTestRemoteAddr = "127.0.0.1:45678"

func newSecurityApp(t *testing.T, cfg Config) *App {
	t.Helper()
	t.Setenv("GIN_MODE", "test")
	if cfg.ResourceRoot == "" {
		cfg.ResourceRoot = t.TempDir()
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	return app
}

// requestFrom 用指定的来源地址构造并执行一次请求。
func requestFrom(h http.Handler, method, path, body, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 未配置 YYB_API_TOKEN 时不再放行取码接口，而是自动生成令牌并强制鉴权。
func TestAPITokenIsGeneratedWhenUnsetRatherThanOpen(t *testing.T) {
	root := t.TempDir()
	app := newSecurityApp(t, Config{ResourceRoot: root, AuthDriver: "sqlite"})
	handler := app.Handler()

	generated := strings.TrimSpace(app.cfg.APIToken)
	if len(generated) != 64 {
		t.Fatalf("自动生成的令牌长度 = %d, want 64 hex chars", len(generated))
	}
	if rec := requestFrom(handler, http.MethodGet, "/instances", "", localTestRemoteAddr, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未带令牌 GET /instances status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}

	// 令牌落库：同一数据目录重新启动后必须是同一个令牌，
	// 否则每次重建容器都要去改青龙里的环境变量。
	app.Close()
	restarted := newSecurityApp(t, Config{ResourceRoot: root, AuthDriver: "sqlite"})
	if restarted.cfg.APIToken != generated {
		t.Fatalf("重启后令牌 = %q, want %q", restarted.cfg.APIToken, generated)
	}
	rec := requestFrom(restarted.Handler(), http.MethodGet, "/instances", "",
		localTestRemoteAddr, map[string]string{"Authorization": "Bearer " + generated})
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("带自动生成令牌 GET /instances 仍被拒绝 (body=%s)", rec.Body.String())
	}
}

// YYB_ALLOW_NO_AUTH=true 是唯一的「关闭鉴权」方式，行为等同旧版本。
func TestAPITokenAllowNoAuthOptOut(t *testing.T) {
	app := newSecurityApp(t, Config{AuthDriver: "sqlite", AllowNoAuth: true})
	handler := app.Handler()
	if app.cfg.APIToken != "" {
		t.Fatalf("AllowNoAuth 时令牌应为空，实际 %q", app.cfg.APIToken)
	}
	for _, path := range []string{"/instances"} {
		if rec := requestFrom(handler, http.MethodGet, path, "", localTestRemoteAddr, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

// 首次注册引导窗口：库中没有任何账号时，只允许内网/回环地址完成注册，
// 公网直连一律拒绝——否则实例刚暴露到公网就会被人抢注成管理员。
func TestRegistrationBootstrapRequiresPrivateNetwork(t *testing.T) {
	app := newSecurityApp(t, Config{AuthDriver: "sqlite", AllowNoAuth: true})
	handler := app.Handler()
	body := `{"username":"owner","displayName":"Owner","password":"` + ownershipAdminPassword + `"}`

	public := requestFrom(handler, http.MethodPost, "/register", body, "203.0.113.9:33333", nil)
	if public.Code != http.StatusForbidden {
		t.Fatalf("公网首次注册 status = %d, want 403 (body=%s)", public.Code, public.Body.String())
	}

	local := requestFrom(handler, http.MethodPost, "/register", body, localTestRemoteAddr, nil)
	if local.Code != http.StatusCreated {
		t.Fatalf("内网首次注册 status = %d, want 201 (body=%s)", local.Code, local.Body.String())
	}
	var registered struct {
		User struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	decodeData(t, local, &registered)
	if registered.User.Role != "admin" {
		t.Fatalf("首个注册账号角色 = %q, want admin", registered.User.Role)
	}

	// 已经有账号之后引导窗口关闭：内网也不能再公开注册。
	again := requestFrom(handler, http.MethodPost, "/register",
		`{"username":"intruder","displayName":"Intruder","password":"intruder-password-1"}`, localTestRemoteAddr, nil)
	if again.Code != http.StatusForbidden {
		t.Fatalf("已有账号后注册 status = %d, want 403 (body=%s)", again.Code, again.Body.String())
	}
}

// 开放重定向：//host、/\host、以及各种控制字符注入都要被收敛到站内。
func TestSafeNextBlocksOpenRedirect(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/settings", "/settings"},
		{"/accounts?ref=1", "/accounts?ref=1"},
		{"//evil.com", "/"},
		{"/\\evil.com", "/"},
		{"/\\/evil.com", "/"},
		{"https://evil.com", "/"},
		{"  //evil.com  ", "/"},
		{"/ok\x00\x0d\x0aSet-Cookie: x=1", "/okSet-Cookie: x=1"},
	}
	for _, tc := range cases {
		if got := safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 默认不信任 X-Forwarded-For：否则攻击者每次请求换一个伪造 IP，
// 就能把按来源地址计数的登录失败限速完全绕开。
func TestClientIPIgnoresForwardedForUnlessTrustProxy(t *testing.T) {
	plain := newSecurityApp(t, Config{AuthDriver: "sqlite", AllowNoAuth: true})
	proxied := newSecurityApp(t, Config{AuthDriver: "sqlite", AllowNoAuth: true, TrustProxy: true})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	if got := plain.clientIP(req); got != "10.0.0.5" {
		t.Fatalf("默认 clientIP = %q, want 10.0.0.5", got)
	}
	if got := proxied.clientIP(req); got != "203.0.113.7" {
		t.Fatalf("TrustProxy clientIP = %q, want 203.0.113.7", got)
	}
}

// 伪造 X-Forwarded-For 也刷不掉限速：按账号维度的计数始终生效。
func TestLoginLockoutSurvivesSpoofedForwardedFor(t *testing.T) {
	app := newSecurityApp(t, Config{AuthDriver: "sqlite", AllowNoAuth: true})
	handler := app.Handler()

	register := requestFrom(handler, http.MethodPost, "/register",
		`{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`"}`, localTestRemoteAddr, nil)
	if register.Code != http.StatusCreated {
		t.Fatalf("注册管理员 status = %d (body=%s)", register.Code, register.Body.String())
	}

	statuses := make([]int, 0, loginFailPerIP+1)
	for i := 0; i < loginFailPerIP+1; i++ {
		rec := requestFrom(handler, http.MethodPost, "/login",
			`{"username":"owner","password":"wrong-password-`+string(rune('a'+i))+`"}`,
			localTestRemoteAddr,
			// 每次都换一个伪造的来源地址，试图绕开按 IP 的计数。
			map[string]string{"X-Forwarded-For": "203.0.113." + string(rune('1'+i))})
		statuses = append(statuses, rec.Code)
	}
	for i, code := range statuses {
		want := http.StatusUnauthorized
		if i == len(statuses)-1 {
			want = http.StatusTooManyRequests
		}
		if code != want {
			t.Fatalf("第 %d 次错误登录 status = %d, want %d（全部=%v）", i+1, code, want, statuses)
		}
	}
}
