package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ---------- 用户批量删除（POST /api/auth/users/batch-delete）回归测试 ----------
//
// 这个接口对应「勾选后一键删除」，最怕两件事：
//  1. 原子性被破坏——ids 里混进自己或最后一个管理员时只删了一半；
//  2. 校验被绕过——空 ids / 超上限 / 非管理员仍然删成功。
//
// 下面把每条规则单独钉一个用例。
const batchTestPassword = "batch-test-password-1"

type batchUser struct {
	ID   int64  `json:"id"`
	Role string `json:"role"`
}

type batchDeleteResponse struct {
	Deleted []int64 `json:"deleted"`
	Skipped []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Count int `json:"count"`
}

// batchFixture 是一个「管理员 + 普通用户 alice」的实例，管理员已登录。
type batchFixture struct {
	handler     http.Handler
	app         *App
	adminCookie *http.Cookie
	aliceCookie *http.Cookie
	adminID     int64
	aliceID     int64
}

func newBatchFixture(t *testing.T) *batchFixture {
	t.Helper()
	handler, app := newAPITokenApp(t, testAPIToken)
	adminCookie := registerSessionUser(t, handler, app)
	adminID := currentUserID(t, handler, adminCookie)

	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+batchTestPassword+`","role":"user"}`, adminCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建 alice status = %d body = %s", created.Code, created.Body.String())
	}
	var alice batchUser
	decodeData(t, created, &alice)

	login := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+batchTestPassword+`"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("alice 登录 status = %d body = %s", login.Code, login.Body.String())
	}

	return &batchFixture{
		handler:     handler,
		app:         app,
		adminCookie: adminCookie,
		aliceCookie: sessionCookieOf(t, login),
		adminID:     adminID,
		aliceID:     alice.ID,
	}
}

// createBatchUser 以管理员身份再建一个普通成员并返回其 ID。
func createBatchUser(t *testing.T, f *batchFixture, username string) int64 {
	t.Helper()
	rec := serveJSON(t, f.handler, http.MethodPost, "/api/auth/users",
		`{"username":"`+username+`","display_name":"`+username+`","password":"`+batchTestPassword+`","role":"user"}`,
		f.adminCookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建用户 %s status = %d body = %s", username, rec.Code, rec.Body.String())
	}
	var user batchUser
	decodeData(t, rec, &user)
	return user.ID
}

// batchBody 拼出 POST body：{ "ids": [1,2,3] }。
func batchBody(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return `{"ids":[` + strings.Join(parts, ",") + `]}`
}

func postBatchDelete(t *testing.T, f *batchFixture, cookie *http.Cookie, ids []int64) *httptest.ResponseRecorder {
	t.Helper()
	return serveJSON(t, f.handler, http.MethodPost, batchDeletePath, batchBody(ids), cookie)
}

// userExists 直接查库，用来验证「整个请求不生效」这类原子性断言。
func userExists(t *testing.T, f *batchFixture, id int64) bool {
	t.Helper()
	_, err := f.app.auth.GetUser(context.Background(), id)
	return err == nil
}

func decodeBatch(t *testing.T, rec *httptest.ResponseRecorder) batchDeleteResponse {
	t.Helper()
	var out batchDeleteResponse
	decodeData(t, rec, &out)
	return out
}

// 正常路径：两个普通成员一次性删掉，返回 deleted/count，且真的落库了。
func TestBatchDeleteRemovesSelectedUsers(t *testing.T) {
	f := newBatchFixture(t)
	first := createBatchUser(t, f, "memberone")
	second := createBatchUser(t, f, "membertwo")

	rec := postBatchDelete(t, f, f.adminCookie, []int64{first, second})
	if rec.Code != http.StatusOK {
		t.Fatalf("批量删除 status = %d body = %s", rec.Code, rec.Body.String())
	}
	out := decodeBatch(t, rec)
	if out.Count != 2 || len(out.Deleted) != 2 {
		t.Fatalf("count = %d deleted = %v, want 2 条", out.Count, out.Deleted)
	}
	if len(out.Skipped) != 0 {
		t.Fatalf("skipped = %v, want 空", out.Skipped)
	}
	for _, id := range []int64{first, second} {
		if userExists(t, f, id) {
			t.Fatalf("用户 %d 仍然存在，删除未生效", id)
		}
	}
	// 管理员与 alice 都不在 ids 里，必须原样保留。
	if !userExists(t, f, f.adminID) || !userExists(t, f, f.aliceID) {
		t.Fatal("未选中的用户被误删")
	}
}

// 原子性：ids 里混进当前登录管理员时整个请求 400，同一批里的其他用户也不能被删。
func TestBatchDeleteRejectsSelfWithoutPartialDelete(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")

	rec := postBatchDelete(t, f, f.adminCookie, []int64{member, f.adminID})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("含自己的批量删除 status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "不能删除当前登录用户") {
		t.Fatalf("错误文案 = %s, want 含「不能删除当前登录用户」", body)
	}
	if !userExists(t, f, f.adminID) {
		t.Fatal("管理员自己被删掉了")
	}
	if !userExists(t, f, member) {
		t.Fatal("请求被整体拒绝，但同一批里的成员却被删了——原子性被破坏")
	}
}

// 至少保留 1 个管理员：删掉多余管理员可以成功，删到 0 个必须被拒。
func TestBatchDeleteKeepsAtLeastOneAdmin(t *testing.T) {
	f := newBatchFixture(t)
	// 把 alice 提成管理员，此时系统有 2 个管理员。
	promote := serveJSON(t, f.handler, http.MethodPut,
		"/api/auth/users/"+strconv.FormatInt(f.aliceID, 10)+"/state",
		`{"role":"admin","enabled":true}`, f.adminCookie)
	if promote.Code != http.StatusOK {
		t.Fatalf("提升 alice 为管理员 status = %d body = %s", promote.Code, promote.Body.String())
	}

	// 删掉 alice 之后还剩自己 1 个管理员，应当成功。
	ok := postBatchDelete(t, f, f.adminCookie, []int64{f.aliceID})
	if ok.Code != http.StatusOK {
		t.Fatalf("删掉多余管理员 status = %d body = %s", ok.Code, ok.Body.String())
	}
	if userExists(t, f, f.aliceID) {
		t.Fatal("alice 未被删除")
	}

	// 现在自己既是唯一管理员、又不允许出现在 ids 里；store 层直接调用验证最后一道防线。
	deleted, skipped, err := f.app.auth.BatchDeleteUsers(context.Background(), f.adminID, []int64{f.adminID})
	if err == nil {
		t.Fatalf("删除唯一管理员竟然成功: deleted=%v skipped=%v", deleted, skipped)
	}
	if !userExists(t, f, f.adminID) {
		t.Fatal("唯一管理员被删掉了")
	}
}

// store 层防线：非管理员 actor 不能被 store 方法绕过 handler 鉴权。
func TestBatchDeleteStoreRejectsNonAdminActor(t *testing.T) {
	f := newBatchFixture(t)
	if _, _, err := f.app.auth.BatchDeleteUsers(context.Background(), f.aliceID, []int64{f.adminID}); err == nil {
		t.Fatal("非管理员 actor 调用 BatchDeleteUsers 竟然成功")
	}
	if !userExists(t, f, f.adminID) {
		t.Fatal("管理员被非管理员 actor 删掉了")
	}
}

// 空 ids：字段缺失、显式空数组、null 都要 400「未选择要删除的用户」。
func TestBatchDeleteRejectsEmptyIDs(t *testing.T) {
	f := newBatchFixture(t)
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"ids":null}`} {
		rec := serveJSON(t, f.handler, http.MethodPost, batchDeletePath, body, f.adminCookie)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status = %d, want 400 (body=%s)", body, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); !strings.Contains(got, "未选择要删除的用户") {
			t.Fatalf("body=%s 错误文案 = %s, want 含「未选择要删除的用户」", body, got)
		}
	}

	// 完全没有 body 时 decodeOptionalJSON 视作空对象，同样要 400。
	empty := serveJSON(t, f.handler, http.MethodPost, batchDeletePath, "", f.adminCookie)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("空 body status = %d, want 400 (body=%s)", empty.Code, empty.Body.String())
	}
}

// 超上限：BatchDeleteMaxIDs+1 个 ID 必须 400，且一个都不能被删。
func TestBatchDeleteRejectsTooManyIDs(t *testing.T) {
	f := newBatchFixture(t)
	victim := createBatchUser(t, f, "memberone")

	ids := make([]int64, 0, batchDeleteMaxIDs+1)
	ids = append(ids, victim)
	for i := 0; i < batchDeleteMaxIDs; i++ {
		ids = append(ids, int64(900000+i))
	}
	rec := postBatchDelete(t, f, f.adminCookie, ids)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超上限 status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "单次最多删除") {
		t.Fatalf("错误文案 = %s, want 含「单次最多删除」", got)
	}
	if !userExists(t, f, victim) {
		t.Fatal("超上限请求被拒绝，但用户仍被删除了")
	}
}

// 未知 id：不整体失败，进 skipped；其余有效 id 照常删除。
func TestBatchDeleteSkipsUnknownIDs(t *testing.T) {
	f := newBatchFixture(t)
	known := createBatchUser(t, f, "memberone")
	const unknown int64 = 987654

	rec := postBatchDelete(t, f, f.adminCookie, []int64{known, unknown})
	if rec.Code != http.StatusOK {
		t.Fatalf("含未知 id status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	out := decodeBatch(t, rec)
	if len(out.Deleted) != 1 || out.Deleted[0] != known || out.Count != 1 {
		t.Fatalf("deleted = %v count = %d, want [%d]/1", out.Deleted, out.Count, known)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].ID != unknown || out.Skipped[0].Reason == "" {
		t.Fatalf("skipped = %+v, want 一条 id=%d 且带 reason", out.Skipped, unknown)
	}
	if userExists(t, f, known) {
		t.Fatalf("有效用户 %d 未被删除", known)
	}
}

// 重复 id 只删一次，计数不会因为重复而虚高。
func TestBatchDeleteDeduplicatesIDs(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")

	rec := postBatchDelete(t, f, f.adminCookie, []int64{member, member, member})
	if rec.Code != http.StatusOK {
		t.Fatalf("重复 id status = %d body = %s", rec.Code, rec.Body.String())
	}
	out := decodeBatch(t, rec)
	if out.Count != 1 || len(out.Deleted) != 1 {
		t.Fatalf("count = %d deleted = %v, want 1 条", out.Count, out.Deleted)
	}
}

// 非管理员：403，且一个都不能删。
func TestBatchDeleteForbiddenForNonAdmin(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")

	rec := postBatchDelete(t, f, f.aliceCookie, []int64{member})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("alice 调用 status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if !userExists(t, f, member) {
		t.Fatal("非管理员的请求删掉了用户")
	}
}

// 未登录：401。
func TestBatchDeleteRequiresSession(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")

	rec := postBatchDelete(t, f, nil, []int64{member})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("匿名调用 status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if !userExists(t, f, member) {
		t.Fatal("匿名请求删掉了用户")
	}
}

// 非 POST 方法：405。
func TestBatchDeleteRejectsNonPost(t *testing.T) {
	f := newBatchFixture(t)
	rec := serveJSON(t, f.handler, http.MethodGet, batchDeletePath, "", f.adminCookie)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
	}
}

// 删除后，被删用户的会话必须一起失效（外键 CASCADE / 显式清理）。
func TestBatchDeleteDropsDeletedUsersSessions(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")
	login := serveJSON(t, f.handler, http.MethodPost, "/login",
		`{"username":"memberone","password":"`+batchTestPassword+`"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("memberone 登录 status = %d body = %s", login.Code, login.Body.String())
	}
	memberCookie := sessionCookieOf(t, login)
	me := serveJSON(t, f.handler, http.MethodGet, "/api/auth/me", "", memberCookie)
	if me.Code != http.StatusOK {
		t.Fatalf("memberone 会话在删除前就不可用: status = %d", me.Code)
	}

	rec := postBatchDelete(t, f, f.adminCookie, []int64{member})
	if rec.Code != http.StatusOK {
		t.Fatalf("批量删除 status = %d body = %s", rec.Code, rec.Body.String())
	}

	after := serveJSON(t, f.handler, http.MethodGet, "/api/auth/me", "", memberCookie)
	if after.Code == http.StatusOK {
		t.Fatalf("被删用户的会话仍然可用: status = %d body = %s", after.Code, after.Body.String())
	}
}

// 返回体必须是约定的 deleted / skipped[{id,reason}] / count 形状。
func TestBatchDeleteResponseShape(t *testing.T) {
	f := newBatchFixture(t)
	member := createBatchUser(t, f, "memberone")
	rec := postBatchDelete(t, f, f.adminCookie, []int64{member, 424242})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &raw); err != nil {
		t.Fatalf("解析 data 失败: %v (data=%s)", err, string(envelope.Data))
	}
	for _, key := range []string{"deleted", "skipped", "count"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("响应缺少字段 %q (data=%s)", key, string(envelope.Data))
		}
	}
	var skipped []map[string]json.RawMessage
	if err := json.Unmarshal(raw["skipped"], &skipped); err != nil {
		t.Fatalf("解析 skipped 失败: %v", err)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want 1 条", skipped)
	}
	if _, ok := skipped[0]["id"]; !ok {
		t.Fatalf("skipped[0] 缺少 id 字段: %v", skipped[0])
	}
	if _, ok := skipped[0]["reason"]; !ok {
		t.Fatalf("skipped[0] 缺少 reason 字段: %v", skipped[0])
	}
}
