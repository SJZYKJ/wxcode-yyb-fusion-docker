package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 公开取码接口访问令牌回归测试 ----------
//
// 约定（见 api_token.go / security.go）：
//   - YYB_API_TOKEN 未配置 → 启动时自动生成并落库，取码接口照样需要鉴权
//     （fail-closed；见 security_test.go 的 TestAPITokenIsGeneratedWhenUnset...）；
//   - YYB_API_TOKEN 已配置 → 需 Bearer / X-API-Token / ?token= 令牌，
//     或持有一个有效的控制台登录会话（工作台「调用配置」依赖这条）；
//   - 只有 YYB_ALLOW_NO_AUTH=true 才会真的关闭鉴权；
//   - /health 永远开放（容器健康检查）。

const testAPIToken = "test-api-token-0123456789"

func newAPITokenApp(t *testing.T, token string) (http.Handler, *App) {
	t.Helper()
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", APIToken: token})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	return app.Handler(), app
}

func serveWithHeaders(
	t *testing.T,
	h http.Handler,
	method, path, body string,
	headers map[string]string,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// 首次注册引导要求内网来源，统一用回环地址（见 security_test.go）。
	req.RemoteAddr = localTestRemoteAddr
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// registerSessionUser 注册首个账号（自动成为管理员）并返回其会话 Cookie。
func registerSessionUser(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := serveJSON(t, h, http.MethodPost, "/register", `{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`"}`, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("POST /register status = %d, want 200/201 (body=%s)", rec.Code, rec.Body.String())
	}
	return sessionCookieOf(t, rec)
}

// AllowNoAuth 是唯一的「关闭鉴权」开关，行为与旧版本一致。
func TestAPITokenAllowNoAuthKeepsLegacyBehaviour(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	handler := app.Handler()

	for _, path := range []string{"/instances"} {
		rec := serveJSON(t, handler, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("AllowNoAuth: GET %s status = %d, want 200 (body=%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPITokenProtectsPublicEndpoints(t *testing.T) {
	handler, _ := newAPITokenApp(t, testAPIToken)

	// 无令牌 → 401
	rec := serveJSON(t, handler, http.MethodGet, "/instances", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: GET /instances status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}

	// 错误令牌 → 401
	for _, headers := range []map[string]string{
		{"Authorization": "Bearer wrong-token"},
		{"X-API-Token": "wrong-token"},
	} {
		rec := serveWithHeaders(t, handler, http.MethodGet, "/instances", "", headers, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token %v: status = %d, want 401", headers, rec.Code)
		}
	}

	// 正确令牌的三种携带方式 → 200
	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"Authorization Bearer", "/instances", map[string]string{"Authorization": "Bearer " + testAPIToken}},
		{"Authorization 裸令牌", "/instances", map[string]string{"Authorization": testAPIToken}},
		{"X-API-Token", "/instances", map[string]string{"X-API-Token": testAPIToken}},
		{"查询参数", "/instances?token=" + testAPIToken, nil},
		{"/openapi.json 带令牌", "/openapi.json", map[string]string{"Authorization": "Bearer " + testAPIToken}},
	}
	for _, tc := range cases {
		rec := serveWithHeaders(t, handler, http.MethodGet, tc.path, "", tc.headers, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid token via %s: GET %s status = %d, want 200 (body=%s)",
				tc.name, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPITokenKeepsHealthRouteOpen(t *testing.T) {
	handler, _ := newAPITokenApp(t, testAPIToken)

	// 健康检查必须无令牌可用，否则 Docker healthcheck 会一直失败。
	rec := serveJSON(t, handler, http.MethodGet, "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", rec.Code)
	}
}

// /login 一址两用：取码分支要令牌，控制台登录分支必须放行，
// 否则配了 YYB_API_TOKEN 之后浏览器连登录页都提交不了。
func TestAPITokenBypassesConsoleLogin(t *testing.T) {
	handler, _ := newAPITokenApp(t, testAPIToken)

	// 登录页面本身（GET /login，无 appId）。
	rec := serveJSON(t, handler, http.MethodGet, "/login", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// 账号密码登录（POST /login with username）必须无令牌可用。
	registerSessionUser(t, handler)
	rec = serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"owner","password":"`+ownershipAdminPassword+`"}`, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("POST /login (console) status = %d, want 200/201 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatalf("POST /login (console) 未下发会话 Cookie")
	}

	// 取码分支（带 appId / app_id）仍然必须出示令牌。
	codePaths := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/login?appId=wx1234567890abcdef", ""},
		{http.MethodGet, "/login?app_id=wx1234567890abcdef", ""},
		{http.MethodPost, "/login", `{"app_id":"wx1234567890abcdef"}`},
	}
	for _, tc := range codePaths {
		rec := serveJSON(t, handler, tc.method, tc.path, tc.body, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 无令牌 status = %d, want 401 (body=%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		rec = serveWithHeaders(t, handler, tc.method, tc.path, tc.body,
			map[string]string{"Authorization": "Bearer " + testAPIToken}, nil)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("%s %s 带令牌仍被拒", tc.method, tc.path)
		}
	}
}

func TestAPITokenAcceptsBrowserConsoleSession(t *testing.T) {
	handler, _ := newAPITokenApp(t, testAPIToken)
	cookie := registerSessionUser(t, handler)

	// 工作台「调用配置」在浏览器里带会话调用 /wxapp/*、/wx/code，
	// 没有携带令牌，必须放行，否则 Web 界面会被自己的令牌拦住。
	for _, path := range []string{"/instances", "/openapi.json"} {
		rec := serveJSON(t, handler, http.MethodGet, path, "", cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("session cookie: GET %s status = %d, want 200 (body=%s)", path, rec.Code, rec.Body.String())
		}
	}

	// 伪造的会话 Cookie 不该被当成有效会话。
	fake := &http.Cookie{Name: sessionCookie, Value: "not-a-real-session"}
	rec := serveJSON(t, handler, http.MethodGet, "/instances", "", fake)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("fake session cookie: status = %d, want 401", rec.Code)
	}
}
