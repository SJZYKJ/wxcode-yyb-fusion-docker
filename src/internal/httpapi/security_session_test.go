package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------- 会话横向越权（IDOR）回归测试 ----------
//
// 背景：早期 requireAPIToken 只判断“请求有没有浏览器会话”就放行，且不把会话
// 身份注入请求上下文。于是任何登录用户——哪怕是最普通的账号——访问 /instances
// 都会被 handler 当成“无会话的公开 API 调用”，拿到全部账号（含管理员）的 openid；
// 再借 api_shared 默认开启，用同一个会话按 ref 就能取到他人的 code。
//
// 修复后的模型（见 access.go 顶部注释）：
//   - API 令牌 → 公开 API 语义：不做归属过滤，可见性由 api_shared 决定；
//   - 浏览器会话 → 控制台语义：管理员全可读，普通用户只能读自己的账号。
//
// 本文件把这条边界逐项钉住。

type consoleFixture struct {
	handler      http.Handler
	app          *App
	adminCookie  *http.Cookie
	aliceCookie  *http.Cookie
	adminID      int64
	aliceID      int64
	adminOpenID  string
	aliceOpenID  string
	legacyOpenID string
}

// newConsoleFixture 构造「管理员 + 普通用户 alice + 三个账号」的场景：
// 管理员名下 1 个、alice 名下 1 个、还有 1 个无归属的历史账号。
func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	handler, app := newAPITokenApp(t, testAPIToken)
	adminCookie := registerSessionUser(t, handler)

	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+ownershipUserPassword+`","role":"user"}`, adminCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建普通用户 status = %d body = %s", created.Code, created.Body.String())
	}
	var createdUser struct {
		ID int64 `json:"id"`
	}
	decodeData(t, created, &createdUser)

	login := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+ownershipUserPassword+`"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("alice 登录 status = %d body = %s", login.Code, login.Body.String())
	}

	f := &consoleFixture{
		handler:      handler,
		app:          app,
		adminCookie:  adminCookie,
		aliceCookie:  sessionCookieOf(t, login),
		aliceID:      createdUser.ID,
		adminOpenID:  "openid-admin",
		aliceOpenID:  "openid-alice",
		legacyOpenID: "openid-legacy",
	}
	f.adminID = currentUserID(t, handler, adminCookie)
	seedAccount(t, app, f.adminOpenID, &f.adminID)
	seedAccount(t, app, f.aliceOpenID, &f.aliceID)
	seedAccount(t, app, f.legacyOpenID, nil)
	return f
}

func currentUserID(t *testing.T, h http.Handler, cookie *http.Cookie) int64 {
	t.Helper()
	rec := serveJSON(t, h, http.MethodGet, "/api/auth/me", "", cookie)
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	decodeData(t, rec, &me)
	if me.User.ID == 0 {
		t.Fatalf("无法取得当前用户 ID (body=%s)", rec.Body.String())
	}
	return me.User.ID
}

func decodeInstances(t *testing.T, rec *httptest.ResponseRecorder) instancesResponse {
	t.Helper()
	var out instancesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 /instances 失败: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func instanceOpenIDs(inst instancesResponse) []string {
	ids := make([]string, 0, len(inst.Instances))
	for _, i := range inst.Instances {
		ids = append(ids, i.OpenID)
	}
	return ids
}

// requestAs 构造一个「已登录为该用户」的请求，等价于经过会话中间件的真实请求。
func requestAs(t *testing.T, app *App, userID int64, method, path string) *http.Request {
	t.Helper()
	user, err := app.auth.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", userID, err)
	}
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(context.WithValue(req.Context(), authUserKey, user))
}

// 普通用户会话访问 /instances 只能看到自己的账号；令牌通道不受影响。
func TestInstancesScopesToOwnerForBrowserSession(t *testing.T) {
	f := newConsoleFixture(t)

	// 1) 普通用户会话：只应看到归属自己的 1 个账号。
	rec := serveWithHeaders(t, f.handler, http.MethodGet, "/instances", "", nil, f.aliceCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("alice 会话 GET /instances status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	aliceInst := decodeInstances(t, rec)
	if aliceInst.Count != 1 || len(aliceInst.Instances) != 1 || aliceInst.Instances[0].OpenID != f.aliceOpenID {
		t.Fatalf("alice 会话看到 %v，只应看到 [%s]", instanceOpenIDs(aliceInst), f.aliceOpenID)
	}

	// 2) 管理员会话：全部可见。
	adminInst := decodeInstances(t, serveWithHeaders(t, f.handler, http.MethodGet, "/instances", "", nil, f.adminCookie))
	if adminInst.Count != 3 {
		t.Fatalf("管理员会话看到 %d 个账号，want 3 (%v)", adminInst.Count, instanceOpenIDs(adminInst))
	}

	// 3) API 令牌（脚本通道）：仍不做归属过滤，三个账号全在 —— 青龙脚本不受影响。
	tokenInst := decodeInstances(t, serveWithHeaders(t, f.handler, http.MethodGet, "/instances", "",
		map[string]string{"X-API-Token": testAPIToken}, nil))
	if tokenInst.Count != 3 {
		t.Fatalf("令牌通道看到 %d 个账号，want 3 (%v)", tokenInst.Count, instanceOpenIDs(tokenInst))
	}

	// 4) 匿名：仍然 401。
	if rec := serveWithHeaders(t, f.handler, http.MethodGet, "/instances", "", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("匿名 GET /instances status = %d, want 401", rec.Code)
	}
}

// 普通用户会话不能按 ref 取到别人（含管理员）的 code。
func TestSessionCannotFetchCodeOfAnotherUsersAccount(t *testing.T) {
	f := newConsoleFixture(t)
	body := `{"ref":"` + f.adminOpenID + `","app_id":"wx1234567890abcdef"}`

	rec := serveWithHeaders(t, f.handler, http.MethodPost, "/wxapp/getCode", body, nil, f.aliceCookie)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("alice 会话取管理员账号 code status = %d, want 403/404 (body=%s)", rec.Code, rec.Body.String())
	}

	// 同样的 ref 换成令牌通道：权限检查应通过（后续取码失败是账号没有凭据所致，
	// 不应再是 403/404）——证明上面拒绝的是“越权”，而不是“账号不存在”。
	tokenRec := serveWithHeaders(t, f.handler, http.MethodPost, "/wxapp/getCode", body,
		map[string]string{"Authorization": "Bearer " + testAPIToken}, nil)
	if tokenRec.Code == http.StatusForbidden || tokenRec.Code == http.StatusNotFound {
		t.Fatalf("令牌通道本应通过权限检查，实际 status = %d (body=%s)", tokenRec.Code, tokenRec.Body.String())
	}
}

// resolveReadableAccount 的判定完全取决于凭据类型。
func TestResolveReadableAccountFollowsCredentialType(t *testing.T) {
	f := newConsoleFixture(t)
	ctx := context.Background()
	asAlice := func() *http.Request {
		return requestAs(t, f.app, f.aliceID, http.MethodPost, "/wxapp/getCode")
	}
	bare := httptest.NewRequest(http.MethodPost, "/wxapp/getCode", nil)

	// 会话：读别人的账号必须失败，读自己的必须成功。
	if acc, err := f.app.resolveReadableAccount(asAlice(), f.adminOpenID); err == nil {
		t.Fatalf("alice 会话读取管理员账号 %s 竟然成功: %v", f.adminOpenID, acc.OpenID)
	}
	if acc, err := f.app.resolveReadableAccount(asAlice(), f.aliceOpenID); err != nil || acc.OpenID != f.aliceOpenID {
		t.Fatalf("alice 会话读取自己的账号失败: acc=%v err=%v", acc, err)
	}

	// 令牌（无会话）：按 api_shared 判定，三个账号都可读。
	for _, openid := range []string{f.adminOpenID, f.aliceOpenID, f.legacyOpenID} {
		if _, err := f.app.resolveReadableAccount(bare, openid); err != nil {
			t.Fatalf("令牌通道读取 %s 失败: %v", openid, err)
		}
	}

	// 拥有者关闭“允许脚本读取”后：令牌通道读不到，但其控制台会话仍能读
	// （控制台调试不应被自己关掉的开关挡住）。
	acc, err := f.app.db.ResolveAccount(ctx, f.aliceOpenID)
	if err != nil {
		t.Fatalf("ResolveAccount(%s): %v", f.aliceOpenID, err)
	}
	if err := f.app.db.SetAccountShare(ctx, acc.ID, false); err != nil {
		t.Fatalf("SetAccountShare: %v", err)
	}
	if _, err := f.app.resolveReadableAccount(bare, f.aliceOpenID); err == nil {
		t.Fatalf("关闭 api_shared 后令牌通道仍能读到 %s", f.aliceOpenID)
	}
	if _, err := f.app.resolveReadableAccount(asAlice(), f.aliceOpenID); err != nil {
		t.Fatalf("拥有者的控制台会话被自己的 api_shared 开关挡住: %v", err)
	}
}

// 不带 ref 取码时的“默认账号”：普通用户会话不能挑到别人的账号。
func TestDefaultAccountForSessionStaysWithinOwnScope(t *testing.T) {
	f := newConsoleFixture(t)

	req := requestAs(t, f.app, f.aliceID, http.MethodPost, "/wxapp/getCode")
	acc, err := f.app.defaultAccountFor(req)
	if err != nil {
		t.Fatalf("defaultAccountFor(alice) error = %v", err)
	}
	if acc.OpenID != f.aliceOpenID {
		t.Fatalf("alice 会话的默认账号 = %s，want %s（不得挑到他人账号）", acc.OpenID, f.aliceOpenID)
	}

	// 令牌通道保持脚本语义：全局共享账号里挑，不受归属限制。
	bare := httptest.NewRequest(http.MethodPost, "/wxapp/getCode", nil)
	if _, err := f.app.defaultAccountFor(bare); err != nil {
		t.Fatalf("令牌通道 defaultAccountFor error = %v", err)
	}
}
