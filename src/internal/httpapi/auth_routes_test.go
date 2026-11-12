package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticatedConsoleAutomationRoutes(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AdminUser: "owner", AdminPassword: "owner-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Handler()
	// 下游定制：/wxapp/*、/wx/* 挂在 requireAPIToken 分组上，没带令牌会先 401。
	// 本用例校验的是「路由存在 + 方法分派 + 会话/登录页语义」，所以带上自动生成的
	// 令牌；令牌闸门自身的行为由 api_token_test.go 覆盖。
	apiToken := app.cfg.APIToken
	for _, remote := range []string{"127.0.0.1:12345", "192.0.2.10:12345"} {
		for _, tc := range []struct {
			method   string
			path     string
			status   int
			redirect string
		}{
			{method: http.MethodPost, path: "/wxapp/getCode", status: http.StatusBadRequest},
			{method: http.MethodPost, path: "/wx/code", status: http.StatusBadRequest},
			{method: http.MethodGet, path: "/wxapp/getCode", status: http.StatusMethodNotAllowed},
			{method: http.MethodGet, path: "/wx/code", status: http.StatusMethodNotAllowed},
			// 未注册的路径：合并后 router.Use(requireBrowserSession) 在 NoRoute 之前生效，
			// 所以「不存在」表现为 303 到登录页（页面形态）或 401（/api/ 形态），不再是 404。
			{method: http.MethodPost, path: "/wxapp/getcode", status: http.StatusSeeOther, redirect: "/login?next="},
			{method: http.MethodPost, path: "/api/wxapp/getCode", status: http.StatusUnauthorized},
			// 页面形态入口：未登录返回 303 到登录页（不再是 401 JSON）。
			{method: http.MethodGet, path: "/accounts", status: http.StatusSeeOther, redirect: "/login?next="},
			{method: http.MethodPost, path: "/accounts/proxy", status: http.StatusSeeOther, redirect: "/login?next="},
			{method: http.MethodGet, path: "/api/auth/me", status: http.StatusUnauthorized},
		} {
			t.Run(remote+tc.method+tc.path, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+apiToken)
				req.RemoteAddr = remote
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d location=%q body=%s", response.Code, tc.status, response.Header().Get("Location"), response.Body.String())
				}
				if tc.redirect != "" {
					if !strings.HasPrefix(response.Header().Get("Location"), tc.redirect) {
						t.Fatalf("path %s location=%q want prefix %q", tc.path, response.Header().Get("Location"), tc.redirect)
					}
					return
				}
				if response.Header().Get("Location") != "" || !json.Valid(response.Body.Bytes()) {
					t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
				}
			})
		}
	}
	for _, path := range []string{"/", "/settings", "/proxies", "/scan"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/login?next=") {
			t.Fatalf("page %s no longer requires login: %d", path, response.Code)
		}
	}
}
