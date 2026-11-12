package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// registerFirstAdmin 按上游 0.3.x 的注册链路注册一个账号：先申请邮箱验证码，
// 再提交 username/displayName/password/email/code。
//
// 合并后注册是「邮箱验证硬前置」（见 auth.go handleRegister：SMTP 未配置直接 400），
// 且首次注册只允许内网来源，所以这里统一用回环地址 + 测试 SMTP 配置 + 内存发信捕获。
func registerFirstAdmin(t *testing.T, h http.Handler, app *App, username, displayName, password string) *httptest.ResponseRecorder {
	t.Helper()
	email := username + "@example.com"
	code := requestEmailCode(t, h, app, email)
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(
		`{"username":"`+username+`","displayName":"`+displayName+`","password":"`+password+`","email":"`+email+`","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = localTestRemoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRunsPageExposesAccountPushSettings(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: filepath.Join("..", "..", "resource")})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()

	page := httptest.NewRecorder()
	app.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/runs?view=push", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET /runs?view=push status = %d", page.Code)
	}
	for _, marker := range []string{"当前账号独立推送", "配置推送", "Server酱", "PushPlus", "企业微信机器人", `initialView === "push"`} {
		if !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("push settings marker %q missing from runs page", marker)
		}
	}

	nav := httptest.NewRecorder()
	app.Handler().ServeHTTP(nav, httptest.NewRequest(http.MethodGet, "/static/js/platform.js", nil))
	if nav.Code != http.StatusOK || !strings.Contains(nav.Body.String(), `["/runs?view=push", "push", "独立推送"`) {
		t.Fatalf("platform navigation does not expose independent push settings: %d %s", nav.Code, nav.Body.String())
	}
	// 系统维护页与在线更新入口已整体下线（用户需求：删除【系统维护】菜单与页面）：
	// 外壳里不应再注入版本按钮、更新弹窗或 /api/maintenance 调用。
	for _, removed := range []string{"platformUpdateDialog", "platformBuild", "/api/maintenance", "打开系统维护"} {
		if strings.Contains(nav.Body.String(), removed) {
			t.Fatalf("removed maintenance marker %q is still present in platform.js", removed)
		}
	}
	page2 := httptest.NewRecorder()
	app.Handler().ServeHTTP(page2, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	if page2.Code != http.StatusNotFound {
		t.Fatalf("GET /maintenance status = %d, want 404 after the page was removed", page2.Code)
	}
	api := httptest.NewRecorder()
	app.Handler().ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/maintenance", nil))
	if api.Code != http.StatusNotFound {
		t.Fatalf("GET /api/maintenance status = %d, want 404 after the API was removed", api.Code)
	}
}

func TestHealthEndpointsRemainPublicWithAuthEnabled(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AdminUser: "health-test", AdminPassword: "local-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, path := range []string{"/health", "/healthz"} {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("anonymous %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestHandlerServesGinRoutesAndSwaggerDocs(t *testing.T) {
	t.Setenv("GIN_MODE", "test")

	app, err := NewApp(Config{
		ResourceRoot:   t.TempDir(),
		RequestTimeout: time.Second,
		AvatarTimeout:  time.Second,
		SessionTTL:     time.Minute,
		QRSessionTTL:   time.Minute,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()

	handler := app.Handler()

	// 合并后 /wx/* 与 /wxapp/* 挂在 requireAPIToken 分组上（下游定制的令牌闸门），
	// 未带令牌的请求会先拿到 401，走不到 handler 的方法分派。本用例校验的是
	// 「路由存在 + GET 不被接受」，所以给这些请求带上自动生成的令牌；
	// 令牌闸门本身的行为由 api_token_test.go 单独覆盖。
	apiTokenRequest := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+app.cfg.APIToken)
		return req
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d", health.Code)
	}
	var healthBody struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatalf("decode health JSON: %v", err)
	}
	if healthBody.Code != 0 || healthBody.Msg != "success" || healthBody.Data["ok"] != true {
		t.Fatalf("GET /health body = %#v", healthBody)
	}
	legacyHealth := httptest.NewRecorder()
	handler.ServeHTTP(legacyHealth, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if legacyHealth.Code != http.StatusOK || legacyHealth.Body.String() != health.Body.String() {
		t.Fatalf("legacy health endpoint differs: status=%d body=%s", legacyHealth.Code, legacyHealth.Body.String())
	}

	versionResponse := httptest.NewRecorder()
	handler.ServeHTTP(versionResponse, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if versionResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/version status = %d", versionResponse.Code)
	}
	var versionBody struct {
		Code int `json:"code"`
		Data struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(versionResponse.Body.Bytes(), &versionBody); err != nil {
		t.Fatalf("decode version JSON: %v", err)
	}
	if versionBody.Code != 0 || versionBody.Data.Version == "" || versionBody.Data.Commit == "" {
		t.Fatalf("GET /api/version body = %#v", versionBody)
	}

	openapi := httptest.NewRecorder()
	handler.ServeHTTP(openapi, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if openapi.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status = %d", openapi.Code)
	}
	var spec map[string]any
	if err := json.Unmarshal(openapi.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode OpenAPI JSON: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Fatalf("openapi version = %v", spec["openapi"])
	}
	if _, ok := spec["code"]; ok {
		t.Fatalf("OpenAPI JSON should not be wrapped in API envelope")
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	wxappResponse := schemas["WxappResponse"].(map[string]any)
	wxappProperties := wxappResponse["properties"].(map[string]any)
	accountRef := wxappProperties["account"].(map[string]any)["$ref"]
	if accountRef != "#/components/schemas/WxappAccountLabel" {
		t.Fatalf("OpenAPI WxappResponse account schema = %v", accountRef)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI paths missing or invalid")
	}
	for _, path := range []string{"/quick-login", "/quick-login/{session_id}/confirm", "/account-link/{token}", "/account-link/{token}/qr", "/account-link/{token}/qr/{session_id}/poll", "/account-link/{token}/qr/{session_id}/confirm", "/api/account-links", "/qr/{session_id}/cancel", "/wx/code", "/wx/getuserinfo", "/wx/encryptkey", "/wx/getlatestuserkey", "/wx/getphonenumber", "/wx/cloud", "/wx/qrcodeauth", "/wx/mpgeta8key", "/wx/appmsgext", "/wx/appmsglike", "/wxapp/getCode", "/wxapp/getPhoneNumber", "/wxapp/operateWxData", "/accounts/repair", "/accounts/avatar", "/accounts/remark", "/accounts/proxy", "/accounts/proxy/test", "/api/proxy-profiles", "/api/proxy-profiles/{id}", "/api/proxy-profiles/areas/provinces", "/api/proxy-profiles/areas/cities", "/api/proxy-location/recommend", "/api/qinglong/config", "/api/qinglong/sync", "/api/qinglong/jobs", "/api/qinglong/push"} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("OpenAPI path %s missing", path)
		}
	}
	for _, path := range []string{"/wxapp/getCode", "/wxapp/getPhoneNumber", "/wxapp/operateWxData"} {
		pathItem := paths[path].(map[string]any)
		post := pathItem["post"].(map[string]any)
		tags := post["tags"].([]any)
		if len(tags) != 1 || tags[0] != "wxapp" {
			t.Fatalf("OpenAPI path %s tags = %#v, want [wxapp]", path, tags)
		}
	}
	for _, path := range []string{"/wx/code", "/wx/encryptkey", "/wx/getphonenumber", "/wx/cloud", "/wx/mpgeta8key", "/wx/appmsgext", "/wx/appmsglike"} {
		pathItem := paths[path].(map[string]any)
		post := pathItem["post"].(map[string]any)
		tags := post["tags"].([]any)
		if len(tags) != 1 || tags[0] != "wx" {
			t.Fatalf("OpenAPI path %s tags = %#v, want [wx]", path, tags)
		}
	}
	encryptPost := paths["/wx/encryptkey"].(map[string]any)["post"].(map[string]any)
	encryptSchema := encryptPost["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if encryptSchema["$ref"] != "#/components/schemas/OperateWXDataRequest" {
		t.Fatalf("OpenAPI /wx/encryptkey request schema = %#v", encryptSchema)
	}
	for _, path := range []string{"/accounts/{ref}", "/accounts/{ref}/getCode", "/accounts/{ref}/getPhoneNumber", "/accounts/{ref}/operateWxData", "/accounts/getCode", "/accounts/getPhoneNumber", "/accounts/operateWxData"} {
		if _, ok := paths[path]; ok {
			t.Fatalf("OpenAPI still exposes old account feature route %s", path)
		}
	}
	if _, ok := paths["/features"]; ok {
		t.Fatalf("OpenAPI still exposes /features")
	}

	docs := httptest.NewRecorder()
	handler.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if docs.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /docs status = %d", docs.Code)
	}
	if got := docs.Header().Get("Location"); got != "/docs/index.html" {
		t.Fatalf("GET /docs Location = %q", got)
	}

	proxies := httptest.NewRecorder()
	handler.ServeHTTP(proxies, httptest.NewRequest(http.MethodGet, "/proxies", nil))
	if proxies.Code != http.StatusOK || !strings.Contains(proxies.Body.String(), "代理设置") {
		t.Fatalf("GET /proxies = %d %s", proxies.Code, proxies.Body.String())
	}
	proxiesPost := httptest.NewRecorder()
	handler.ServeHTTP(proxiesPost, httptest.NewRequest(http.MethodPost, "/proxies", nil))
	if proxiesPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /proxies status = %d", proxiesPost.Code)
	}

	features := httptest.NewRecorder()
	handler.ServeHTTP(features, httptest.NewRequest(http.MethodGet, "/features", nil))
	if features.Code != http.StatusNotFound {
		t.Fatalf("GET /features status = %d", features.Code)
	}
	var notFoundBody struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(features.Body.Bytes(), &notFoundBody); err != nil {
		t.Fatalf("decode /features error JSON: %v", err)
	}
	if notFoundBody.Code == 0 || notFoundBody.Msg == "" || notFoundBody.Data != nil {
		t.Fatalf("GET /features body = %#v", notFoundBody)
	}

	oldPath := httptest.NewRecorder()
	handler.ServeHTTP(oldPath, httptest.NewRequest(http.MethodPost, "/accounts/getCode", nil))
	if oldPath.Code != http.StatusNotFound {
		t.Fatalf("POST old account feature route status = %d", oldPath.Code)
	}
	for _, path := range []string{"/wx/code", "/wx/encryptkey", "/wx/getlatestuserkey", "/wx/getphonenumber", "/wx/cloud", "/wx/mpgeta8key", "/wx/appmsgext", "/wx/appmsglike", "/wx/qrcodeauth"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, apiTokenRequest(httptest.NewRequest(http.MethodGet, path, nil)))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, http.StatusMethodNotAllowed)
		}
	}
	encryptKey := httptest.NewRecorder()
	encryptKeyRequest := httptest.NewRequest(http.MethodPost, "/wx/encryptkey", strings.NewReader(`{"ref":"1","app_id":"wx0000000000000000"}`))
	encryptKeyRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(encryptKey, apiTokenRequest(encryptKeyRequest))
	if encryptKey.Code != http.StatusBadRequest || !strings.Contains(encryptKey.Body.String(), "payload is required") {
		t.Fatalf("POST /wx/encryptkey without payload = %d %s", encryptKey.Code, encryptKey.Body.String())
	}
	userinfo := httptest.NewRecorder()
	handler.ServeHTTP(userinfo, apiTokenRequest(httptest.NewRequest(http.MethodGet, "/wx/getuserinfo", nil)))
	if userinfo.Code != http.StatusBadRequest {
		t.Fatalf("GET /wx/getuserinfo without ref status = %d, want %d", userinfo.Code, http.StatusBadRequest)
	}
}

func TestNormalizeEncryptKeyPayload(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "top level client name", in: map[string]any{"api_name": "getLatestUserKey", "data": map[string]any{"appid": "wx-test"}}, want: encryptKeyOperation},
		{name: "nested client name", in: map[string]any{"data": map[string]any{"api_name": "getLatestUserKey", "version": 2}}, want: encryptKeyOperation},
		{name: "legacy server name", in: map[string]any{"api_name": "getUserEncryptKey"}, want: encryptKeyOperation},
		{name: "server name unchanged", in: map[string]any{"api_name": encryptKeyOperation}, want: encryptKeyOperation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeEncryptKeyPayload(tt.in)
			if got["api_name"] == tt.want {
				return
			}
			if nested, ok := got["data"].(map[string]any); ok && nested["api_name"] == tt.want {
				return
			}
			t.Fatalf("normalized payload = %#v, want api_name %q", got, tt.want)
		})
	}
}

func TestAuthMeWithoutConfiguredAuthentication(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{
		ResourceRoot:   t.TempDir(),
		RequestTimeout: time.Second,
		AvatarTimeout:  time.Second,
		SessionTTL:     time.Minute,
		QRSessionTTL:   time.Minute,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/me status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			AuthEnabled bool `json:"auth_enabled"`
			User        struct {
				Username    string `json:"username"`
				DisplayName string `json:"display_name"`
				Role        string `json:"role"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode /api/auth/me JSON: %v", err)
	}
	if response.Code != 0 || response.Data.AuthEnabled || response.Data.User.Username != "local" || response.Data.User.DisplayName == "" || response.Data.User.Role != "admin" {
		t.Fatalf("GET /api/auth/me body = %#v", response)
	}
	for _, path := range []string{"/users"} {
		page := httptest.NewRecorder()
		app.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, path, nil))
		if page.Code != http.StatusSeeOther || page.Header().Get("Location") != "/" {
			t.Fatalf("GET %s status = %d, Location = %q", path, page.Code, page.Header().Get("Location"))
		}
	}
}

func TestSQLiteAuthFirstRegistrationAndUnauthorizedAPI(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	// SMTP：合并后注册要求邮箱验证码，这里给一份可用的测试配置（不会真的联网）。
	app, err := NewApp(Config{
		ResourceRoot: t.TempDir(),
		AuthDriver:   "sqlite",
		SMTP:         testSMTP,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()
	handler := app.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/me status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	register := registerFirstAdmin(t, handler, app, "owner", "Owner", "owner-password")
	if register.Code != http.StatusCreated {
		t.Fatalf("POST /register status = %d body = %s", register.Code, register.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Next string `json:"next"`
			User struct {
				Role string `json:"role"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if body.Code != 0 || body.Data.User.Role != "admin" || body.Data.Next != "/" {
		t.Fatalf("POST /register body = %#v", body)
	}
	result := register.Result()
	if len(result.Cookies()) != 1 {
		t.Fatalf("POST /register cookies = %d, want 1", len(result.Cookies()))
	}

	index := httptest.NewRecorder()
	indexRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	indexRequest.AddCookie(result.Cookies()[0])
	handler.ServeHTTP(index, indexRequest)
	if index.Code != http.StatusOK {
		t.Fatalf("authenticated GET / status = %d", index.Code)
	}
}

func TestSQLite普通用户CanUsePlatformPages(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", SMTP: testSMTP})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()
	handler := app.Handler()

	register := registerFirstAdmin(t, handler, app, "admin", "Admin", "admin-password")
	if register.Code != http.StatusCreated {
		t.Fatalf("register admin status = %d", register.Code)
	}
	adminCookie := register.Result().Cookies()[0]

	create := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"username":"member","display_name":"Member","password":"member-password","role":"user"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(adminCookie)
	handler.ServeHTTP(create, createReq)
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status = %d body=%s", create.Code, create.Body.String())
	}

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"member","password":"member-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login member status = %d", login.Code)
	}
	memberCookie := login.Result().Cookies()[0]
	for _, path := range []string{"/", "/scan", "/proxies", "/runs", "/accounts", "/settings"} {
		page := httptest.NewRecorder()
		pageReq := httptest.NewRequest(http.MethodGet, path, nil)
		pageReq.AddCookie(memberCookie)
		handler.ServeHTTP(page, pageReq)
		if page.Code != http.StatusOK {
			t.Fatalf("普通用户 GET %s status = %d body=%s", path, page.Code, page.Body.String())
		}
	}
	// 管理员页面：普通用户被 303 重定向回工作台 /（合并后保留了
	// requireAdminSession 中间件的页面重定向语义）；管理接口仍返回 403。
	users := httptest.NewRecorder()
	usersReq := httptest.NewRequest(http.MethodGet, "/users", nil)
	usersReq.AddCookie(memberCookie)
	handler.ServeHTTP(users, usersReq)
	if users.Code != http.StatusSeeOther || users.Header().Get("Location") != "/" {
		t.Fatalf("普通用户 GET /users status = %d location=%q, want 303 -> /", users.Code, users.Header().Get("Location"))
	}
}

func TestSQLite普通用户账号隔离AndAdminCanInspectOwnership(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", SMTP: testSMTP})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer app.Close()
	handler := app.Handler()

	adminRegister := registerFirstAdmin(t, handler, app, "admin", "Admin", "admin-password")
	adminCookie := adminRegister.Result().Cookies()[0]

	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"username":"member","display_name":"Member","password":"member-password","role":"user"}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.AddCookie(adminCookie)
	handler.ServeHTTP(create, createRequest)
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status = %d body=%s", create.Code, create.Body.String())
	}
	users, err := app.auth.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	var memberID int64
	for _, user := range users {
		if user.Username == "member" {
			memberID = user.ID
		}
	}
	if memberID == 0 {
		t.Fatal("member user not found")
	}
	status := "alive"
	account, err := app.db.UpsertAccount(context.Background(), "member-openid", "buffer", nil, nil, nil, nil, nil, &status)
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	if err := app.auth.ClaimAccount(context.Background(), account.ID, memberID); err != nil {
		t.Fatalf("ClaimAccount() error = %v", err)
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"member","password":"member-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, loginRequest)
	memberCookie := login.Result().Cookies()[0]

	accounts := httptest.NewRecorder()
	accountsRequest := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	accountsRequest.AddCookie(memberCookie)
	handler.ServeHTTP(accounts, accountsRequest)
	if accounts.Code != http.StatusOK || !strings.Contains(accounts.Body.String(), "member-openid") {
		t.Fatalf("member accounts = %d %s", accounts.Code, accounts.Body.String())
	}

	// 说明：上游 0.3.x 没有 /api/auth/users/{id}/accounts 这个接口（合并后
	// adminRoutes 下只有 /api/auth/users 与 /api/auth/users/*path 的 handleUserAction，
	// 不含 accounts 子资源），下游原来「管理员按用户查其名下账号」的入口随之消失。
	// 归属关系现在由 auth.account_owners 记录，管理员视角直接看 /accounts（管理员可见全部），
	// 所以这里不再断言该 404 接口。

	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodGet, "/api/auth/users", nil)
	blockedRequest.AddCookie(memberCookie)
	handler.ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("member user list status = %d", blocked.Code)
	}

	// 面板配置（含 SMTP 密钥 / 青龙 client_id）是管理员专属：普通用户 403，且不下发密钥。
	config := httptest.NewRecorder()
	configRequest := httptest.NewRequest(http.MethodGet, "/api/qinglong/config", nil)
	configRequest.AddCookie(memberCookie)
	handler.ServeHTTP(config, configRequest)
	if config.Code != http.StatusForbidden || strings.Contains(config.Body.String(), `"client_id"`) {
		t.Fatalf("member panel config = %d %s", config.Code, config.Body.String())
	}

	foreign, err := app.db.UpsertAccount(context.Background(), "foreign-openid", "buffer", nil, nil, nil, nil, nil, &status)
	if err != nil {
		t.Fatalf("seed foreign account: %v", err)
	}
	for _, testRequest := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/qinglong/jobs?ref=" + strconv.FormatInt(foreign.ID, 10)},
		{method: http.MethodPost, path: "/accounts/refresh", body: `{"ref":"` + strconv.FormatInt(foreign.ID, 10) + `"}`},
		{method: http.MethodGet, path: "/accounts/proxy?ref=" + strconv.FormatInt(foreign.ID, 10)},
		{method: http.MethodPut, path: "/accounts/proxy", body: `{"ref":"` + strconv.FormatInt(foreign.ID, 10) + `","mode":"direct","protocol_proxy_mode":"direct"}`},
	} {
		blocked := httptest.NewRecorder()
		req := httptest.NewRequest(testRequest.method, testRequest.path, strings.NewReader(testRequest.body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(memberCookie)
		handler.ServeHTTP(blocked, req)
		if blocked.Code != http.StatusNotFound {
			t.Fatalf("member foreign account request %s %s status = %d body=%s", testRequest.method, testRequest.path, blocked.Code, blocked.Body.String())
		}
	}
}
