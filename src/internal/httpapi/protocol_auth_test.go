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

	"yyb_go/internal/protocol"
)

// Malformed bodies and nonexistent QR sessions keep these requests entirely
// local, including when this regression is run against the vulnerable code.
func TestProtocolRoutesRequireConfiguredToken(t *testing.T) {
	routes := []struct {
		path       string
		getStatus  int
		postStatus int
	}{
		{"/wx/oauth", 405, 400},
		{"/wxapp/getCode", 405, 400},
		{"/wxapp/getPhoneNumber", 405, 400},
		{"/wxapp/operateWxData", 405, 400},
		{"/wx/code", 405, 400},
		{"/wx/getuserinfo", 400, 400},
		{"/wx/encryptkey", 405, 400},
		{"/wx/getlatestuserkey", 405, 400},
		{"/wx/getphonenumber", 405, 400},
		{"/wx/cloud", 405, 400},
		{"/wx/mpgeta8key", 405, 400},
		{"/wx/appmsgext", 405, 400},
		{"/wx/appmsglike", 405, 400},
		{"/wx/qrcodeauth", 405, 400},
		{"/wx/qrcodeauth/synthetic-missing/image", 404, 405},
		{"/wx/qrcodeauth/synthetic-missing/poll", 404, 405},
		{"/wx/qrcodeauth/synthetic-missing/confirm", 405, 404},
		{"/wx/qrcodeauth/synthetic-missing/cancel", 405, 200},
	}
	for _, driver := range []string{"none", "sqlite"} {
		for _, token := range []string{"", "synthetic-protocol-token"} {
			t.Run(fmt.Sprintf("driver=%s/token=%t", driver, token != ""), func(t *testing.T) {
				// AllowNoAuth：下游在协议路由组前面还挂了一层 requireAPIToken()
				// （YYB_API_TOKEN 未配置时启动会自动生成并要求携带）。本用例验证的是
				// ProtocolToken（opt-in）这一层，所以先关掉下游闸门，让请求能走到
				// handler 级别，继续断言 405/400/404。下游闸门本身在下面单独钉住。
				app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: driver, ProtocolToken: token, AllowNoAuth: true})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				handler := app.Handler()
				for _, route := range routes {
					for _, method := range []string{http.MethodGet, http.MethodPost} {
						for _, authorization := range []string{"", "Bearer wrong", "Bearer synthetic-protocol-token"} {
							t.Run(method+route.path+"/"+authorization, func(t *testing.T) {
								want := route.getStatus
								if method == http.MethodPost {
									want = route.postStatus
								}
								if token != "" && authorization != "Bearer "+token {
									want = http.StatusUnauthorized
								}
								req := httptest.NewRequest(method, route.path, strings.NewReader("{"))
								req.Header.Set("Content-Type", "application/json")
								req.Header.Set("Authorization", authorization)
								recorder := httptest.NewRecorder()
								handler.ServeHTTP(recorder, req)
								if recorder.Code != want || !json.Valid(recorder.Body.Bytes()) {
									t.Fatalf("status=%d, want=%d; body=%s", recorder.Code, want, recorder.Body.String())
								}
							})
						}
					}
				}
			})
		}
	}

	// 下游安全定制（fail-closed）：不开 AllowNoAuth、也没配 YYB_API_TOKEN 时，
	// 启动会现场生成令牌，因此**不带任何凭据**的协议请求必须先拿到 401，
	// 而不是走到 handler。这里单独钉住这层闸门，防止上游语义回退。
	t.Run("downstream api token gate", func(t *testing.T) {
		app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite"})
		if err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		handler := app.Handler()
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized || !json.Valid(recorder.Body.Bytes()) {
				t.Fatalf("%s without credentials: status=%d want=%d; body=%s", route.path, recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		}
	})
}

func TestUserInfoProtocolTokenAndAccountOwnership(t *testing.T) {
	const token = "synthetic-userinfo-token"
	// AllowNoAuth：同上，先关掉下游的 API 令牌闸门，专测 ProtocolToken + 归属判定。
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", ProtocolToken: token, AllowNoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()
	sessions := map[string]string{"invalid": "invalid-session"}
	for _, role := range []string{"owner", "outsider", "admin"} {
		userRole := "user"
		if role == "admin" {
			userRole = "admin"
		}
		user, err := app.auth.CreateUser(ctx, role, role, "synthetic-password", userRole)
		if err != nil {
			t.Fatal(err)
		}
		session, _, err := app.auth.CreateSession(ctx, user.ID, "test", "192.0.2.10", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		sessions[role] = session
		if role == "owner" {
			for i := 1; i <= 2; i++ {
				status := "alive"
				creds := protocol.LoginBufferCredentials{
					OpenID: fmt.Sprintf("synthetic-openid-%d", i), AccessToken: "synthetic-access",
					RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(),
				}
				account, err := app.db.UpsertAccount(ctx, creds.OpenID, "synthetic-buffer", nil, nil, nil, nil, creds.ToMap(), &status)
				if err != nil {
					t.Fatal(err)
				}
				if account.ID != int64(i) {
					t.Fatalf("unexpected synthetic account ID: %d", account.ID)
				}
				if err := app.auth.ClaimAccount(ctx, account.ID, user.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	fetchCalls := 0
	app.fetchUserInfo = func(_ context.Context, creds protocol.LoginBufferCredentials) (map[string]any, error) {
		fetchCalls++
		return map[string]any{"nickname": "Synthetic Account", "openid": creds.OpenID}, nil
	}
	handler := app.Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, ref := range []string{"1", "2", "synthetic-openid-1"} {
			for _, cookie := range []string{"", "invalid", "owner", "outsider", "admin"} {
				for _, authorization := range []string{"", "Bearer wrong", "Bearer " + token} {
					t.Run(method+"/"+ref+"/"+cookie+"/"+authorization, func(t *testing.T) {
						path, body := "/wx/getuserinfo?ref="+ref, ""
						if method == http.MethodPost {
							path, body = "/wx/getuserinfo", `{"ref":"`+ref+`"}`
						}
						req := httptest.NewRequest(method, path, strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Authorization", authorization)
						if cookie != "" {
							req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessions[cookie]})
						}
						want, wantCalls := http.StatusUnauthorized, 0
						if authorization == "Bearer "+token {
							want, wantCalls = http.StatusOK, 1
							if cookie == "outsider" {
								want, wantCalls = http.StatusNotFound, 0
							}
						}
						before := fetchCalls
						recorder := httptest.NewRecorder()
						handler.ServeHTTP(recorder, req)
						if recorder.Code != want || fetchCalls-before != wantCalls {
							t.Fatalf("status=%d want=%d, upstream calls=%d want=%d; body=%s", recorder.Code, want, fetchCalls-before, wantCalls, recorder.Body.String())
						}
						if want == http.StatusOK && !strings.Contains(recorder.Body.String(), `"nickname":"Synthetic Account"`) {
							t.Fatalf("missing synthetic user info: %s", recorder.Body.String())
						}
					})
				}
			}
		}
	}

	// Browser-managed QR aliases and public account links retain their own
	// authorization boundaries; the protocol token is not a browser session.
	for _, tc := range []struct {
		path, authorization, cookie string
		status                      int
		redirect                    string
	}{
		{path: "/qr", cookie: sessions["owner"], status: 405},
		// 没有会话时 /qr、/accounts 属于浏览器页面入口：合并后先被
		// requireBrowserSession 拦成 303（到登录页），协议令牌救不了浏览器会话。
		{path: "/qr", authorization: "Bearer " + token, status: 303, redirect: "/login?next="},
		{path: "/accounts", authorization: "Bearer " + token, status: 303, redirect: "/login?next="},
		{path: "/account-link/synthetic-missing", status: 410},
		{path: "/health", status: 200},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", tc.authorization)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tc.cookie})
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != tc.status {
			t.Errorf("boundary %s: status=%d want=%d; body=%s", tc.path, recorder.Code, tc.status, recorder.Body.String())
		}
		if tc.redirect != "" && !strings.HasPrefix(recorder.Header().Get("Location"), tc.redirect) {
			t.Errorf("boundary %s: location=%q want prefix %q", tc.path, recorder.Header().Get("Location"), tc.redirect)
		}
	}
}

func TestUnauthorizedProtocolQRDoesNotCancelSession(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), ProtocolToken: "synthetic-qr-token", AllowNoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Handler()
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, authorization := range []string{"", "Bearer wrong", "Bearer synthetic-qr-token"} {
			app.qrSessions["synthetic-session"] = &qrLoginSession{}
			req := httptest.NewRequest(method, "/wx/qrcodeauth/synthetic-session/cancel", nil)
			req.Header.Set("Authorization", authorization)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			_, exists := app.qrSessions["synthetic-session"]
			allowed := authorization == "Bearer synthetic-qr-token"
			want := http.StatusUnauthorized
			if allowed {
				want = http.StatusOK
			}
			if recorder.Code != want || exists == allowed {
				t.Fatalf("%s token=%q: status=%d want=%d, session exists=%t", method, authorization, recorder.Code, want, exists)
			}
		}
	}
}
