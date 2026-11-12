package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yyb_go/internal/protocol"
	"yyb_go/internal/store"
)

type sentNotice struct {
	target         pushTarget
	title, content string
}

// captureNotices replaces delivery with an in-memory recorder.
func captureNotices(app *App) func() []sentNotice {
	var mu sync.Mutex
	var sent []sentNotice
	app.notifier.send = func(_ context.Context, target pushTarget, title, content string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, sentNotice{target, title, content})
		return nil
	}
	return func() []sentNotice {
		app.notifier.wait()
		mu.Lock()
		defer mu.Unlock()
		return append([]sentNotice(nil), sent...)
	}
}

func TestNotifyConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		channel, token, wantChannel string
		ok                          bool
	}{
		{"", "", "", true},
		{"none", "", "", true},
		{" PushPlus ", " pp-token ", "pushplus", true},
		{"serverchan", "", "", false},
		{"telegram", "token", "", false},
	} {
		app, err := NewApp(Config{ResourceRoot: t.TempDir(), NotifyChannel: tc.channel, NotifyToken: tc.token})
		if !tc.ok {
			if err == nil || app != nil {
				t.Fatalf("channel %q token %q accepted", tc.channel, tc.token)
			}
			continue
		}
		if err != nil {
			t.Fatalf("channel %q: %v", tc.channel, err)
		}
		if app.cfg.NotifyChannel != tc.wantChannel || tc.ok && tc.wantChannel != "" && app.cfg.NotifyToken != "pp-token" {
			t.Fatalf("effective channel = %q token trimmed = %t", app.cfg.NotifyChannel, app.cfg.NotifyToken == "pp-token")
		}
		_ = app.Close()
	}
}

func TestPushRequestShapes(t *testing.T) {
	ctx := context.Background()
	readForm := func(req *http.Request) url.Values {
		raw, _ := io.ReadAll(req.Body)
		values, _ := url.ParseQuery(string(raw))
		return values
	}
	readJSON := func(req *http.Request) map[string]any {
		var out map[string]any
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &out)
		return out
	}

	req, err := newPushRequest(ctx, pushTarget{Channel: "serverchan", Token: "SCTkey"}, "标题", "第一行\n第二行")
	if err != nil || req.URL.String() != "https://sctapi.ftqq.com/SCTkey.send" {
		t.Fatalf("serverchan url = %v, %v", req.URL, err)
	}
	if form := readForm(req); form.Get("title") != "标题" || form.Get("desp") != "第一行\n\n第二行" {
		t.Fatalf("serverchan form = %v", form)
	}
	req, _ = newPushRequest(ctx, pushTarget{Channel: "serverchan", Token: "sctp12345tABC"}, "t", "c")
	if req.URL.String() != "https://12345.push.ft07.com/send/sctp12345tABC.send" {
		t.Fatalf("Server酱³ url = %v", req.URL)
	}

	req, _ = newPushRequest(ctx, pushTarget{Channel: "pushplus", Token: "pp", Topic: "group"}, "t", "c")
	if body := readJSON(req); req.URL.String() != "https://www.pushplus.plus/send" || body["token"] != "pp" || body["topic"] != "group" || body["content"] != "c" {
		t.Fatalf("pushplus request = %v %v", req.URL, body)
	}

	req, _ = newPushRequest(ctx, pushTarget{Channel: "qywx", Token: "robot"}, "t", "c")
	body := readJSON(req)
	text, _ := body["text"].(map[string]any)
	if req.URL.String() != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=robot" || body["msgtype"] != "text" || text["content"] != "t\n\nc" {
		t.Fatalf("qywx request = %v %v", req.URL, body)
	}

	if _, err := newPushRequest(ctx, pushTarget{Channel: "unknown", Token: "x"}, "t", "c"); err == nil {
		t.Fatal("unsupported channel accepted")
	}
}

func TestPushAcceptedRequiresExplicitSuccess(t *testing.T) {
	code := func(v int) *int { return &v }
	for _, tc := range []struct {
		channel string
		resp    pushResponse
		want    bool
	}{
		{"serverchan", pushResponse{Code: code(0)}, true},
		{"serverchan", pushResponse{Errno: code(0)}, true},
		{"serverchan", pushResponse{Code: code(40001)}, false},
		{"serverchan", pushResponse{}, false},
		{"pushplus", pushResponse{Code: code(200)}, true},
		{"pushplus", pushResponse{Code: code(900)}, false},
		{"qywx", pushResponse{ErrCode: code(0)}, true},
		{"qywx", pushResponse{ErrCode: code(93000)}, false},
		{"qywx", pushResponse{}, false},
	} {
		if got := pushAccepted(tc.channel, tc.resp); got != tc.want {
			t.Fatalf("pushAccepted(%s, %+v) = %t, want %t", tc.channel, tc.resp, got, tc.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostPushKeepsSecretsOutOfErrors(t *testing.T) {
	const secret = "SCT_SECRET_KEY_VALUE"
	target := pushTarget{Channel: "serverchan", Token: secret}
	for name, transport := range map[string]roundTripFunc{
		"transport": func(*http.Request) (*http.Response, error) { return nil, errors.New("dial failed") },
		"status": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(secret))}, nil
		},
		"rejection": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":40001,"message":"` + secret + `"}`))}, nil
		},
	} {
		err := postPush(context.Background(), &http.Client{Transport: transport}, target, "t", "c")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	ok := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})
	if err := postPush(context.Background(), &http.Client{Transport: ok}, target, "t", "c"); err != nil {
		t.Fatalf("accepted notice reported %v", err)
	}
}

func scannedAccount(t *testing.T, app *App, openID string, scannedAt int64) *store.WechatAccount {
	t.Helper()
	acc := insertKeepAliveTestAccount(t, app, openID, time.Now().Add(time.Minute))
	acc.Credentials["refresh_token_observed_at"] = scannedAt
	if err := app.db.SetAccountCredential(context.Background(), acc.ID, acc.LoginBuffer, acc.Credentials); err != nil {
		t.Fatal(err)
	}
	acc, err := app.db.GetAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestOfflineNoticeOncePerAuthorization(t *testing.T) {
	app := newKeepAliveTestApp(t)
	defer app.Close()
	app.cfg.NotifyChannel, app.cfg.NotifyToken = "pushplus", "PP_OPERATOR_TOKEN"
	sent := captureNotices(app)
	acc := scannedAccount(t, app, "openid-notice", 1000)
	remark := "主号"
	acc.Remark = &remark
	cause := errors.Join(
		&protocol.RefreshRejectedError{Code: -109, Operation: "refresh_token", Message: "RC_PARAMS_INVALID"},
		&protocol.RefreshRejectedError{Code: -109, Operation: "check_login", Message: "error 42007 | 42007"},
	)

	app.notifyOffline(acc, store.StatusSessionOnly, cause)
	app.notifyOffline(acc, store.StatusSessionOnly, cause)
	app.notifyOffline(acc, "expired", cause) // later stage of the same authorization
	got := sent()
	if len(got) != 1 {
		t.Fatalf("notices for one authorization = %d, want 1", len(got))
	}
	notice := got[0]
	if notice.target != (pushTarget{Channel: "pushplus", Token: "PP_OPERATOR_TOKEN"}) {
		t.Fatalf("target = %+v", notice.target)
	}
	for _, want := range []string{"#" + strconv.FormatInt(acc.ID, 10) + " 主号", "应用宝", "需补授权", "check_login -109 / 微信 42007"} {
		if !strings.Contains(notice.content, want) {
			t.Fatalf("notice lacks %q:\n%s", want, notice.content)
		}
	}
	for _, secret := range []string{"openid-notice", "access-old", "refresh-old", "PP_OPERATOR_TOKEN"} {
		if strings.Contains(notice.title+notice.content, secret) {
			t.Fatalf("notice leaked %q", secret)
		}
	}

	rescanned := *acc
	rescanned.Credentials = map[string]any{"refresh_token_observed_at": int64(2000)}
	app.notifyOffline(&rescanned, "expired", nil)
	if got := sent(); len(got) != 2 || !strings.Contains(got[1].content, "授权已失效") || strings.Contains(got[1].content, "诊断") {
		t.Fatalf("re-scanned authorization notices = %+v", got)
	}
}

func TestOfflineNoticeNeedsAConfiguredChannel(t *testing.T) {
	app := newKeepAliveTestApp(t)
	defer app.Close()
	sent := captureNotices(app)
	app.notifyOffline(scannedAccount(t, app, "openid-quiet", 1), "expired", nil)
	if got := sent(); len(got) != 0 {
		t.Fatalf("notices without a channel = %d", len(got))
	}
}

// Reproduces the deployed v0.2.57 failure: in the default legacy mode _auth
// answers a revoked QR credential with -109 RC_PARAMS_INVALID. The account
// must reach a conclusive state, notify once and stop renewing.
func TestLegacyKeepAliveRevocationIsConclusiveAndNotifies(t *testing.T) {
	for _, withSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "session_only"}[withSession], func(t *testing.T) {
			app := newKeepAliveTestApp(t)
			defer app.Close()
			if app.cfg.ASRefreshMode != "legacy_auth" {
				t.Fatalf("default refresh mode = %q", app.cfg.ASRefreshMode)
			}
			app.cfg.NotifyChannel, app.cfg.NotifyToken = "qywx", "ROBOT_KEY"
			sent := captureNotices(app)
			acc := scannedAccount(t, app, "openid-revoked", 1000)
			if withSession {
				putEligibleSession(t, app, acc)
			}
			refreshes, checks := 0, 0
			app.refreshCredentials = func(context.Context, protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				refreshes++
				return protocol.LoginBufferCredentials{}, &protocol.RefreshRejectedError{Code: -109, Operation: "refresh_token", Message: "RC_PARAMS_INVALID"}
			}
			app.checkLogin = func(context.Context, protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				checks++
				return protocol.LoginBufferCredentials{}, &protocol.RefreshRejectedError{Code: -109, Operation: "check_login", Message: "error 42007 | 42007"}
			}

			ctx := context.Background()
			app.refreshDueAccounts(ctx)
			app.refreshDueAccounts(ctx)
			current, err := app.db.GetAccount(ctx, acc.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "expired"
			if withSession {
				want = store.StatusSessionOnly
			}
			if accountStatus(current) != want || refreshes != 1 || checks != 1 {
				t.Fatalf("status=%s refreshes=%d checks=%d; want %s after one conclusive attempt", accountStatus(current), refreshes, checks, want)
			}
			notices := sent()
			if len(notices) != 1 || notices[0].target.Channel != "qywx" || !strings.Contains(notices[0].content, "微信 42007") {
				t.Fatalf("notices = %+v", notices)
			}
		})
	}
}

func TestAccountPushChannelReceivesOfflineNotice(t *testing.T) {
	_, server := newFakeQingLong(t)
	app, handler, ref := newRunsTestApp(t, server.URL)
	_ = apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{"ref": ref, "script_key": "MDHY.js", "enabled": true})
	if save := apiRequest(t, handler, http.MethodPut, "/api/qinglong/push", map[string]any{"ref": ref, "channel": "serverchan", "token": "SCT_ACCOUNT_SECRET"}); save.Code != http.StatusOK {
		t.Fatalf("save push = %d %s", save.Code, save.Body.String())
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	acc, err := app.db.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	app.cfg.NotifyChannel, app.cfg.NotifyToken = "pushplus", "PP_OPERATOR_TOKEN"
	sent := captureNotices(app)

	app.notifyOffline(acc, "expired", nil)
	got := sent()
	if len(got) != 2 || got[0].target.Channel != "pushplus" || got[1].target != (pushTarget{Channel: "serverchan", Token: "SCT_ACCOUNT_SECRET"}) {
		t.Fatalf("targets = %+v", got)
	}

	app.cfg.DisableAccountPushNotify = true
	rescanned := *acc
	rescanned.Credentials = map[string]any{"refresh_token_observed_at": int64(99)}
	app.notifyOffline(&rescanned, "expired", nil)
	if got := sent(); len(got) != 3 || got[2].target.Channel != "pushplus" {
		t.Fatalf("disabled account push still notified: %+v", got)
	}
}
