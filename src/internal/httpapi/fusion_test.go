package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v11 起只有原生扫码（login_buffer）协议，设备 hook 相关测试已随安卓支持一并删除。
func newFusionApp(t *testing.T) *App {
	t.Helper()
	app, err := NewApp(Config{
		ResourceRoot:   t.TempDir(),
		RequestTimeout: time.Second,
		AvatarTimeout:  time.Second,
		SessionTTL:     time.Minute,
		QRSessionTTL:   time.Minute,
		// 本文件验证取码协议，不涉及访问令牌；
		// 令牌鉴权覆盖见 api_token_test.go / security_test.go。
		AllowNoAuth: true,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func decodeEnvelope(t *testing.T, body []byte) (map[string]any, map[string]any) {
	t.Helper()
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	return map[string]any{"code": float64(env.Code), "msg": env.Msg}, env.Data
}

func TestUnifiedLoginPostMissingAppID(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t)
	handler := app.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"app_id":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "app_id is required") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"app_id":"wx123","prefer":"bogus"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("prefer status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestUnifiedLoginGetFailsWithoutAccounts(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t)
	handler := app.Handler()

	// GET /login?appId= is native-only since v11; with no accounts it must
	// return the wxcode wire format (HTTP 200 + err field) with a native error.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?appId=wxabc123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode wxcode error JSON: %v", err)
	}
	if raw["err"] == nil || raw["err"].(float64) == 0 {
		t.Fatalf("expected err != 0, got %#v", raw)
	}
	msg, _ := raw["msg"].(string)
	if msg == "" {
		t.Fatalf("expected non-empty error message, got %s", rec.Body.String())
	}
}

func TestWXCompatInstances(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/instances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode instances: %v", err)
	}
	for _, key := range []string{"count", "instances", "current", "currentUserId"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("instances missing key %q: %s", key, rec.Body.String())
		}
	}
}

func TestClassifyCode(t *testing.T) {
	// Mirrors the wxcode module's classifier order exactly:
	// hex -> base64 ("[A-Za-z0-9+/=]") -> base64url ("[A-Za-z0-9_-]") -> alnum.
	// Note: plain alnum input matches the base64 class first, which is the
	// original module's observable behavior.
	tests := []struct {
		code string
		want string
	}{
		{"", "invalid"},
		{"071a2b3c", "hex"},
		{"deadbeef", "hex"},
		{"aGVsbG8=", "base64"},
		{"hello", "base64"},
		{"a_b-c", "base64url"},
		{"a b", "other"},
		{"a" + "\n" + "b", "other"},
		{"YWJjZA==", "base64"},
		{"abc-def", "base64url"},
		{"abc_def", "base64url"},
	}
	for _, tt := range tests {
		got := classifyCode(tt.code)
		if got != tt.want {
			t.Errorf("classifyCode(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestDefaultAccount(t *testing.T) {
	app := newFusionApp(t)
	ctx := context.Background()
	alive, expired := "alive", "expired"

	// 0 accounts -> error
	_, err := app.defaultAccount(ctx)
	if err == nil || !strings.Contains(err.Error(), "no native") {
		t.Fatalf("expected 'no native' error, got %v", err)
	}

	acc1, err := app.db.UpsertAccount(ctx, "openid-expired", "buf1", nil, nil, nil, nil, nil, &expired, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1 account -> it
	acc, err := app.defaultAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc.OpenID != "openid-expired" {
		t.Fatalf("expected single account, got %s", acc.OpenID)
	}

	// 2 accounts: alive beats non-alive
	_, err = app.db.UpsertAccount(ctx, "openid-alive", "buf2", nil, nil, nil, nil, nil, &alive, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc, err = app.defaultAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc.OpenID != "openid-alive" {
		t.Fatalf("expected alive account, got %s (acc1=%d was expired)", acc.OpenID, acc1.ID)
	}
}

func TestUnifiedLoginGetWithRef(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t)
	handler := app.Handler()

	ctx := context.Background()
	alive := "alive"
	acc1, err := app.db.UpsertAccount(ctx, "openid-one", "buf1", nil, nil, nil, nil, nil, &alive, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc2, err := app.db.UpsertAccount(ctx, "openid-two", "buf2", nil, nil, nil, nil, nil, &alive, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = acc1
	_ = acc2

	// GET /login?appId=wx&ref=openid-two should try to resolve "openid-two"
	// (native code fetch will fail, but the error must NOT be the old
	// "multiple native accounts" message, proving the ref was passed through)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?appId=wxabc&ref=openid-two", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	msg, _ := raw["msg"].(string)
	if strings.Contains(msg, "multiple native accounts configured") {
		t.Fatalf("ref should have been passed through, got: %s", rec.Body.String())
	}
	if strings.Contains(msg, "no native WeChat accounts") {
		t.Fatalf("accounts exist, got: %s", rec.Body.String())
	}
	t.Logf("native error (expected): %s", msg)
}
