package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newFusionApp(t *testing.T, wxcodeURL string) *App {
	t.Helper()
	app, err := NewApp(Config{
		ResourceRoot:   t.TempDir(),
		RequestTimeout: time.Second,
		AvatarTimeout:  time.Second,
		SessionTTL:     time.Minute,
		QRSessionTTL:   time.Minute,
		WXCodeURLs:     []string{wxcodeURL},
		WXCodeTimeout:  time.Second,
		WXCodeHookPort: 18089,
		// 本文件验证取码协议与回退链路，不涉及访问令牌；
		// 令牌与 /wxcode/* 的鉴权覆盖见 api_token_test.go / security_test.go。
		AllowNoAuth: true,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func mockWXCodeServer(t *testing.T, login func(appID string) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/whoami":
			fmt.Fprint(w, `{"packageName":"com.tencent.mm","version":"8.0.62","port":18089}`)
		case "/login":
			status, body := login(r.URL.Query().Get("appId"))
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
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

func TestUnifiedLoginGetTransparentWXCode(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	srv := mockWXCodeServer(t, func(appID string) (int, string) {
		if appID != "wxabc123" {
			return http.StatusBadRequest, `{"err":-2,"msg":"bad appId"}`
		}
		return http.StatusOK, `{"err":0,"msg":"success","appId":"wxabc123","status":"ok","code":"071a2b3c","codeType":"hex","codeLength":8}`
	})
	app := newFusionApp(t, srv.URL)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?appId=wxabc123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw wxcode JSON: %v", err)
	}
	if raw["err"] != float64(0) || raw["code"] != "071a2b3c" || raw["appId"] != "wxabc123" {
		t.Fatalf("transparent body = %#v", raw)
	}
	if _, wrapped := raw["data"]; wrapped {
		t.Fatalf("wxcode wire format must not be envelope-wrapped: %s", rec.Body.String())
	}
}

func TestUnifiedLoginPostDeviceSource(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	srv := mockWXCodeServer(t, func(appID string) (int, string) {
		return http.StatusOK, `{"err":0,"msg":"success","appId":"` + appID + `","status":"ok","code":"deadbeef","codeType":"hex","codeLength":8}`
	})
	app := newFusionApp(t, srv.URL)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"app_id":"wx123"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	env, data := decodeEnvelope(t, rec.Body.Bytes())
	if env["code"] != float64(0) {
		t.Fatalf("envelope = %#v", env)
	}
	if data["source"] != "device" || data["fallback"] != false {
		t.Fatalf("source/fallback = %#v", data)
	}
	result, _ := data["result"].(map[string]any)
	if result["code"] != "deadbeef" {
		t.Fatalf("result = %#v", result)
	}
}

func TestUnifiedLoginPostDeviceDownFallsBackMessage(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"app_id":"wx123"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "device failed") || !strings.Contains(rec.Body.String(), "native failed") {
		t.Fatalf("error should mention both sources: %s", rec.Body.String())
	}
}

func TestUnifiedLoginPostPreferNativeFallsBackToDevice(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	srv := mockWXCodeServer(t, func(appID string) (int, string) {
		return http.StatusOK, `{"err":0,"msg":"success","appId":"` + appID + `","status":"ok","code":"cafe","codeType":"hex","codeLength":4}`
	})
	app := newFusionApp(t, srv.URL)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"app_id":"wx123","prefer":"native"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	_, data := decodeEnvelope(t, rec.Body.Bytes())
	if data["source"] != "device" || data["fallback"] != true {
		t.Fatalf("expected device fallback, got %#v", data)
	}
}

func TestUnifiedLoginPostMissingAppID(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
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

func TestHookCfgEndpoint(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wxcode/hookcfg?v=8.0.76", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"port 18089", "j1 hm0.j1", "c cl0.c", "a1 plugin.appbrand.jsapi.auth.i2", "a7 plugin.appbrand.jsapi.auth.m2", "j1_static d", "j1_instance g", "matched 1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("hookcfg missing %q in:\n%s", want, body)
		}
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wxcode/hookcfg?v=9.9.9", nil))
	if !strings.Contains(rec.Body.String(), "matched 0") {
		t.Fatalf("unknown version should report matched 0:\n%s", rec.Body.String())
	}
}

func TestHookMappingSelection(t *testing.T) {
	app := newFusionApp(t, "http://127.0.0.1:1")
	entry, matched := app.hookMapping("8.0.76")
	if !matched || entry.J1 != "hm0.j1" || entry.C != "cl0.c" {
		t.Fatalf("exact 8.0.76 mapping = %+v matched=%v", entry, matched)
	}
	entry, matched = app.hookMapping("8.0.62.2410")
	if !matched || entry.J1 != "of0.j1" {
		t.Fatalf("prefix 8.0.62 mapping = %+v matched=%v", entry, matched)
	}
	entry, matched = app.hookMapping("7.0.21")
	if matched {
		t.Fatalf("unknown 7.0.21 should not match, got %+v", entry)
	}
	if entry.J1 == "" || entry.A1 == "" {
		t.Fatalf("fallback entry must be non-empty: %+v", entry)
	}
}

// ---- wxcode compatibility layer tests ----

func TestUnifiedLoginGetFallsBackToNative(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	// No device endpoints configured
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	// GET /login?appId= should fall back to native (no accounts scanned yet)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?appId=wxabc123", nil))
	// wxcode wire format: always HTTP 200 with err field
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
	if msg == "" || strings.Contains(msg, "device hook not configured") {
		t.Fatalf("expected native error message, got %s", rec.Body.String())
	}
}

func TestUnifiedLoginGetTransparentWXCodeWithFallback(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	// A device that is configured but unreachable (port 1) should fail,
	// then fall back to native (which also fails because no accounts).
	// The error message should mention native, not just "device not configured".
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?appId=wxabc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	msg, _ := raw["msg"].(string)
	if strings.Contains(msg, "device hook not configured") {
		t.Fatalf("should not report device-not-configured for reachable endpoint; got %s", rec.Body.String())
	}
}

func TestWXCompatWhoami(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	for _, key := range []string{"packageName", "userId", "port", "version", "j1", "c", "a1", "a7"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("whoami missing key %q: %s", key, rec.Body.String())
		}
	}
}

func TestWXCompatInstances(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
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

func TestWXCodeRegisterDualApp(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	app := newFusionApp(t, "http://127.0.0.1:1")
	handler := app.Handler()

	register := func(port, userID int) {
		t.Helper()
		rec := httptest.NewRecorder()
		u := fmt.Sprintf("/wxcode/register?port=%d&userId=%d&version=8.0.76", port, userID)
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("register status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	register(18089, 0)  // main WeChat
	register(18099, 10) // dual-app clone

	eps := app.deviceEndpoints()
	joined := strings.Join(eps, ",")
	if !strings.Contains(joined, "127.0.0.1:18089") {
		t.Fatalf("deviceEndpoints missing main hook: %v", eps)
	}
	if !strings.Contains(joined, "127.0.0.1:18099") {
		t.Fatalf("deviceEndpoints missing dual-app hook: %v", eps)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/instances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("instances status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode instances: %v", err)
	}
	insts, ok := raw["instances"].([]any)
	if !ok {
		t.Fatalf("instances not array: %s", rec.Body.String())
	}
	seen := map[float64]bool{}
	for _, it := range insts {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["packageName"] == "com.tencent.mm" {
			seen[m["userId"].(float64)] = true
		}
	}
	if !seen[0] || !seen[10] {
		t.Fatalf("instances missing userId 0/10: %s", rec.Body.String())
	}
}

func TestDefaultAccount(t *testing.T) {
	app := newFusionApp(t, "http://127.0.0.1:1")
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
	app := newFusionApp(t, "http://127.0.0.1:1")
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
