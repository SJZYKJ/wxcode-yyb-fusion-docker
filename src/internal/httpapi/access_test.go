package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 多用户账号隔离回归测试 ----------
//
// 约定（见 access.go）：
//   - 控制台（浏览器会话）：管理员看全部；普通用户只看 owner_user_id = 自己 的账号。
//   - 公开 API（无会话）：不做归属过滤，但跳过 api_shared=false 的账号。

const (
	ownershipAdminPassword = "admin-password-1"
	ownershipUserPassword  = "alice-password-1"
)

func newOwnershipApp(t *testing.T) (http.Handler, *App) {
	t.Helper()
	t.Setenv("GIN_MODE", "test")
	// AllowNoAuth：本文件只验证「谁能看到/操作哪些账号」，与取码接口的令牌
	// 鉴权无关；令牌链路本身在 api_token_test.go 里单独覆盖。
	// 注册流程要求邮箱验证码，这里给一份可用的测试 SMTP 配置并把发信替换成
	// 内存捕获（见 api_token_test.go 的 installCodeCapture）。
	// AllowRegistration：本文件验证的是账号归属隔离，注册开关本身另有测试覆盖。
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP, AllowRegistration: true})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { app.Close() })
	installCodeCapture(app)
	return app.Handler(), app
}

func serveJSON(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	// httptest 默认把 RemoteAddr 设成 192.0.2.1（TEST-NET，公网段），
	// 会被「首次注册只允许内网地址」的引导规则挡掉；这里显式改成回环地址，
	// 模拟真实部署里在局域网完成首次初始化的场景。
	req.RemoteAddr = localTestRemoteAddr
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly 1 session cookie, got %d (body=%s)", len(cookies), rec.Body.String())
	}
	return cookies[0]
}

// decodeData 解出统一响应体 {"code":..,"msg":..,"data":..}。
func decodeData(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	if envelope.Code != 0 {
		t.Fatalf("unexpected response code %d (body=%s)", envelope.Code, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			t.Fatalf("decode data: %v (data=%s)", err, string(envelope.Data))
		}
	}
}

// accountListResponse 对应 /accounts 返回的 store.AccountPublic。
// 归并不再写 wechat_accounts.owner_user_id（上游 0.3.x 用 auth.account_owners），
// 所以列表里没有 owner_user_id 字段，归属要用 app.auth.AccountOwner 单独查。
type accountListResponse []struct {
	ID        int64  `json:"id"`
	OpenID    string `json:"openid"`
	APIShared bool   `json:"api_shared"`
}

type instancesResponse struct {
	Count     int `json:"count"`
	Instances []struct {
		OpenID   string `json:"openid"`
		Nickname string `json:"nickname"`
	} `json:"instances"`
}

// seedAccount 写入一个账号并返回其 ID。
func seedAccount(t *testing.T, app *App, openid string, ownerUserID *int64) int64 {
	t.Helper()
	nickname := "nick-" + openid
	status := "alive"
	acc, err := app.db.UpsertAccount(context.Background(), openid, "{}", nil, &nickname, nil, nil, nil, &status)
	if err != nil {
		t.Fatalf("UpsertAccount(%s): %v", openid, err)
	}
	// 归属改由上游 auth.account_owners 记录（ClaimAccount），
	// 不再作为 wechat_accounts 的列存在。
	if ownerUserID != nil {
		if err := app.auth.ClaimAccount(context.Background(), acc.ID, *ownerUserID); err != nil {
			t.Fatalf("ClaimAccount(%s -> user %d): %v", openid, *ownerUserID, err)
		}
	}
	return acc.ID
}

func TestAccountOwnershipIsolatesConsoleButKeepsAPIReadable(t *testing.T) {
	handler, app := newOwnershipApp(t)

	// 首个注册账号自动成为管理员。
	register := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`","email":"owner@example.com","code":"`+requestEmailCode(t, handler, app, "owner@example.com")+`"}`, nil)
	if register.Code != http.StatusCreated {
		t.Fatalf("POST /register status = %d body = %s", register.Code, register.Body.String())
	}
	adminCookie := sessionCookieOf(t, register)
	var registerData struct {
		User struct {
			ID   int64  `json:"id"`
			Role string `json:"role"`
		} `json:"user"`
	}
	decodeData(t, register, &registerData)
	if registerData.User.Role != "admin" {
		t.Fatalf("first registered user role = %q, want admin", registerData.User.Role)
	}

	// 管理员创建普通用户 alice。
	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+ownershipUserPassword+`","role":"user"}`, adminCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST /api/auth/users status = %d body = %s", created.Code, created.Body.String())
	}
	var createdUser struct {
		ID   int64  `json:"id"`
		Role string `json:"role"`
	}
	decodeData(t, created, &createdUser)
	if createdUser.Role != "user" {
		t.Fatalf("created user role = %q, want user", createdUser.Role)
	}

	aliceLogin := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+ownershipUserPassword+`"}`, nil)
	if aliceLogin.Code != http.StatusOK {
		t.Fatalf("alice login status = %d body = %s", aliceLogin.Code, aliceLogin.Body.String())
	}
	aliceCookie := sessionCookieOf(t, aliceLogin)

	// 三个账号：alice 自己的、别人的、以及无归属的历史账号。
	aliceOwner := createdUser.ID
	// 归属记录在 auth.account_owners 里，user_id 有外键约束，
	// 所以「别人」必须是真实用户，不能再用 9999 这种裸 ID。
	createdOther := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"bob","display_name":"Bob","password":"bob-password-1","role":"user"}`, adminCookie)
	if createdOther.Code != http.StatusCreated {
		t.Fatalf("POST /api/auth/users(bob) status = %d body = %s", createdOther.Code, createdOther.Body.String())
	}
	var otherUser struct {
		ID int64 `json:"id"`
	}
	decodeData(t, createdOther, &otherUser)
	otherOwner := otherUser.ID
	aliceAccountID := seedAccount(t, app, "openid-alice", &aliceOwner)
	otherAccountID := seedAccount(t, app, "openid-other", &otherOwner)
	seedAccount(t, app, "openid-legacy", nil)

	// 管理员看全部 3 个。
	var adminList accountListResponse
	decodeData(t, serveJSON(t, handler, http.MethodGet, "/accounts", "", adminCookie), &adminList)
	if len(adminList) != 3 {
		t.Fatalf("admin sees %d accounts, want 3", len(adminList))
	}

	// 普通用户只看自己那 1 个。
	var aliceList accountListResponse
	decodeData(t, serveJSON(t, handler, http.MethodGet, "/accounts", "", aliceCookie), &aliceList)
	if len(aliceList) != 1 || aliceList[0].OpenID != "openid-alice" {
		t.Fatalf("alice list = %#v, want only openid-alice", aliceList)
	}
	if owner, err := app.auth.AccountOwner(context.Background(), aliceList[0].ID); err != nil || owner != aliceOwner {
		t.Fatalf("account_owners[%d] = %d, err = %v, want owner %d", aliceList[0].ID, owner, err, aliceOwner)
	}

	// 公开 API（无会话）默认仍能读到全部账号（含别人的、无归属的）。
	// 注意：/instances 是 wxcode 兼容格式，不套 {code,msg,data} 外壳。
	var instances instancesResponse
	if err := json.Unmarshal(serveJSON(t, handler, http.MethodGet, "/instances", "", nil).Body.Bytes(), &instances); err != nil {
		t.Fatalf("decode /instances: %v", err)
	}
	if instances.Count != 3 || len(instances.Instances) != 3 {
		t.Fatalf("/instances count = %d, want 3", instances.Count)
	}

	// 普通用户不能操作归属他人的账号。
	// 合并后（上游 0.3.x 的 resolveAccountRef + 下游 accountVisible）对不可见账号
	// 统一返回 404「账号不存在」，不再回 403，避免向会话用户泄露账号归属信息。
	crossOp := serveJSON(t, handler, http.MethodPut, "/accounts/share",
		`{"ref":"`+"openid-other"+`","shared":false}`, aliceCookie)
	if crossOp.Code != http.StatusNotFound {
		t.Fatalf("cross-owner share status = %d, want 404 (body=%s)", crossOp.Code, crossOp.Body.String())
	}
	crossDelete := serveJSON(t, handler, http.MethodDelete, "/accounts?ref=openid-other", "", aliceCookie)
	if crossDelete.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete status = %d, want 404 (body=%s)", crossDelete.Code, crossDelete.Body.String())
	}
	if acc, err := app.db.GetAccount(context.Background(), otherAccountID); err != nil || !acc.APIShared {
		t.Fatalf("cross-owner share must not mutate other account: api_shared = %v, err = %v", acc.APIShared, err)
	}

	// 普通用户可以操作自己的账号：关闭「脚本可读」后该账号从公开 API 消失。
	ownOp := serveJSON(t, handler, http.MethodPut, "/accounts/share",
		`{"ref":"`+"openid-alice"+`","shared":false}`, aliceCookie)
	if ownOp.Code != http.StatusOK {
		t.Fatalf("own share status = %d, want 200 (body=%s)", ownOp.Code, ownOp.Body.String())
	}
	if acc, err := app.db.GetAccount(context.Background(), aliceAccountID); err != nil || acc.APIShared {
		t.Fatalf("after share=false: api_shared = %v, err = %v (want false)", acc.APIShared, err)
	}

	var afterShare instancesResponse
	if err := json.Unmarshal(serveJSON(t, handler, http.MethodGet, "/instances", "", nil).Body.Bytes(), &afterShare); err != nil {
		t.Fatalf("decode /instances: %v", err)
	}
	if afterShare.Count != 2 {
		t.Fatalf("/instances count after share=false = %d, want 2", afterShare.Count)
	}
	for _, inst := range afterShare.Instances {
		if inst.OpenID == "openid-alice" {
			t.Fatalf("openid-alice still exposed via /instances after share=false")
		}
	}

	// 关闭共享的账号也不能被脚本按 ref 取码；自己的控制台仍可见。
	viaAPI := serveJSON(t, handler, http.MethodPost, "/wxapp/getCode",
		`{"ref":"`+"openid-alice"+`","app_id":"wx1234567890abcdef"}`, nil)
	if viaAPI.Code != http.StatusForbidden && viaAPI.Code != http.StatusNotFound {
		t.Fatalf("script getCode on unshared account status = %d, want 403/404 (body=%s)", viaAPI.Code, viaAPI.Body.String())
	}
	var stillVisible accountListResponse
	decodeData(t, serveJSON(t, handler, http.MethodGet, "/accounts", "", aliceCookie), &stillVisible)
	if len(stillVisible) != 1 || stillVisible[0].ID != aliceAccountID {
		t.Fatalf("owner should still see own unshared account, got %#v", stillVisible)
	}

	// 管理员可见性不受 api_shared 影响。
	var adminAgain accountListResponse
	decodeData(t, serveJSON(t, handler, http.MethodGet, "/accounts", "", adminCookie), &adminAgain)
	if len(adminAgain) != 3 {
		t.Fatalf("admin sees %d accounts after share=false, want 3", len(adminAgain))
	}

	// 交叉校验：otherAccountID 未被误改。
	if acc, err := app.db.GetAccount(context.Background(), otherAccountID); err != nil || !acc.APIShared {
		t.Fatalf("other account api_shared = %v, err = %v (want true)", acc.APIShared, err)
	}
	_ = registerData
}

// 普通用户不再被重定向到个人设置页，也不能进管理员专属页面。
func TestConsoleRoutesAreAvailableToNormalUsers(t *testing.T) {
	handler, app := newOwnershipApp(t)

	register := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`","email":"owner@example.com","code":"`+requestEmailCode(t, handler, app, "owner@example.com")+`"}`, nil)
	adminCookie := sessionCookieOf(t, register)
	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+ownershipUserPassword+`","role":"user"}`, adminCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST /api/auth/users status = %d body = %s", created.Code, created.Body.String())
	}
	aliceLogin := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+ownershipUserPassword+`"}`, nil)
	aliceCookie := sessionCookieOf(t, aliceLogin)

	// 这些页面普通用户必须能打开（原先全部 302 到 /settings）。
	for _, path := range []string{"/", "/scan", "/runs", "/settings", "/accounts", "/docs/index.html"} {
		rec := serveJSON(t, handler, http.MethodGet, path, "", aliceCookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("normal user GET %s status = %d, want 200", path, rec.Code)
		}
	}

	// 管理员专属：普通用户进入管理员页面会被 303 重定向回工作台 /，
	// 管理接口才是 403（见下面的 /api/auth/users）。
	page := serveJSON(t, handler, http.MethodGet, "/users", "", aliceCookie)
	if page.Code != http.StatusSeeOther || page.Header().Get("Location") != "/" {
		t.Fatalf("normal user GET /users status = %d, Location = %q (want 303 -> /)", page.Code, page.Header().Get("Location"))
	}
	adminOnly := serveJSON(t, handler, http.MethodGet, "/api/auth/users", "", aliceCookie)
	if adminOnly.Code != http.StatusForbidden {
		t.Fatalf("normal user GET /api/auth/users status = %d, want 403", adminOnly.Code)
	}
	// 管理员级配置（含 SMTP 密钥、青龙 Cookie）挂在 adminRoutes 分组上（下游安全定制），
	// 普通用户直接 403，不下发任何降级视图。
	restricted := serveJSON(t, handler, http.MethodGet, "/api/qinglong/config", "", aliceCookie)
	if restricted.Code != http.StatusForbidden {
		t.Fatalf("normal user GET /api/qinglong/config = %d %s, want 403", restricted.Code, restricted.Body.String())
	}

	// /api/auth/me 必须暴露 role，前端据此隐藏管理员入口。
	me := serveJSON(t, handler, http.MethodGet, "/api/auth/me", "", aliceCookie)
	var meData struct {
		User struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	decodeData(t, me, &meData)
	if meData.User.Role != "user" {
		t.Fatalf("/api/auth/me role = %q, want user", meData.User.Role)
	}
}

// 扫码归属：普通用户认领自己的账号；管理员不夺走已归属他人的账号。
func TestOwnerForScanAssignment(t *testing.T) {
	_, app := newOwnershipApp(t)
	handler := app.Handler()

	register := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`","email":"owner@example.com","code":"`+requestEmailCode(t, handler, app, "owner@example.com")+`"}`, nil)
	adminCookie := sessionCookieOf(t, register)
	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+ownershipUserPassword+`","role":"user"}`, adminCookie)
	var createdUser struct {
		ID int64 `json:"id"`
	}
	decodeData(t, created, &createdUser)
	aliceLogin := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+ownershipUserPassword+`"}`, nil)
	aliceCookie := sessionCookieOf(t, aliceLogin)

	ctx := context.Background()
	// ownerForScan 从请求上下文读取当前用户（由 requireBrowserSession 中间件注入），
	// 这里直接构造该上下文，等价于一次已登录请求。
	requestAs := func(userID int64) *http.Request {
		user, err := app.auth.GetUser(ctx, userID)
		if err != nil {
			t.Fatalf("GetUser(%d): %v", userID, err)
		}
		req := httptest.NewRequest(http.MethodPost, "/qr", nil)
		return req.WithContext(context.WithValue(req.Context(), authUserKey, user))
	}
	var adminID int64
	adminMe := serveJSON(t, handler, http.MethodGet, "/api/auth/me", "", adminCookie)
	var adminMeData struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	decodeData(t, adminMe, &adminMeData)
	adminID = adminMeData.User.ID

	// 普通用户扫码 -> 记在自己名下。
	owner := app.ownerForScan(ctx, requestAs(createdUser.ID), "openid-new")
	if owner == nil || *owner != createdUser.ID {
		t.Fatalf("ownerForScan(alice) = %v, want %d", owner, createdUser.ID)
	}

	// 管理员扫码一个已归属 alice 的账号 -> 不夺走（返回 nil，保留原归属）。
	seedAccount(t, app, "openid-owned", &createdUser.ID)
	if owner := app.ownerForScan(ctx, requestAs(adminID), "openid-owned"); owner != nil {
		t.Fatalf("ownerForScan(admin on alice's account) = %v, want nil", owner)
	}

	// 管理员扫码一个新账号 -> 记在自己名下。
	if owner := app.ownerForScan(ctx, requestAs(adminID), "openid-admin-new"); owner == nil || *owner != adminID {
		t.Fatalf("ownerForScan(admin on new account) = %v, want %d", owner, adminID)
	}

	// 未启用鉴权时保持无归属。
	unauthenticated := httptest.NewRequest(http.MethodPost, "/qr", nil)
	savedAuth := app.auth
	app.auth = nil
	owner = app.ownerForScan(ctx, unauthenticated, "openid-anon")
	app.auth = savedAuth
	if owner != nil {
		t.Fatalf("ownerForScan(auth disabled) = %v, want nil", owner)
	}
	_ = aliceCookie
}
