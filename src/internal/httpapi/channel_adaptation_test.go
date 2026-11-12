package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yyb_go/internal/eventlog"
	"yyb_go/internal/pcmgr"
	"yyb_go/internal/protocol"
	"yyb_go/internal/qbbrowser"
	"yyb_go/internal/qr"
	"yyb_go/internal/store"
)

// 四条新客户端凭据链（电脑管家 / 手游助手 / 雷电 / QQ 浏览器）在下游这层壳里
// 的适配回归。上游把渠道实现带过来了，但下游独有的三件事——fail-closed 的
// API 令牌闸门、api_shared 可见性、编队（fleet）派发——都可能单独掐断某条
// 渠道，所以这里按「渠道清单 / 凭据写读与 Provider 路由 / 渠道侧端点状态码 /
// 编队」逐项钉住。

func channelRegistryIDs() []string {
	channels := qr.ScanChannels()
	ids := make([]string, 0, len(channels))
	for _, channel := range channels {
		ids = append(ids, channel.ID)
	}
	return ids
}

// GET /api/scan-channels 是控制台所有渠道选择器的唯一来源（扫码页、补扫、
// 账号卡片、系统设置都读它）。四条新渠道必须都在这里出现。
func TestScanChannelsEndpointListsEveryChannel(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/scan-channels", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Channels []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Badge       string `json:"badge"`
				Description string `json:"description"`
			} `json:"channels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
	}
	got := make(map[string]bool, len(payload.Data.Channels))
	for _, channel := range payload.Data.Channels {
		if channel.Name == "" || channel.Badge == "" || channel.Description == "" {
			t.Errorf("channel %q is incomplete: %+v", channel.ID, channel)
		}
		got[channel.ID] = true
	}
	for _, want := range []string{"appstore", "pcmgr", "gameassist", "ldplayer", "qbbrowser"} {
		if !got[want] {
			t.Errorf("/api/scan-channels is missing channel %q", want)
		}
	}
}

// 对外 OpenAPI 文档里的渠道枚举必须跟着注册表走。历史上三处枚举硬编码成
// appstore/pcmgr/gameassist，雷电与 QQ 浏览器并进来以后文档里根本没有它们。
func TestOpenAPIChannelEnumsMatchTheRegistry(t *testing.T) {
	spec := newOpenAPISpec()
	want := channelRegistryIDs()
	enums := 0
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch value := node.(type) {
		case map[string]any:
			if enum, ok := value["enum"].([]string); ok && len(enum) > 0 {
				// 渠道枚举只有两种写法：以 appstore 开头，或（个人默认渠道）
				// 以空串开头表示跟随系统默认。
				if enum[0] == "appstore" || (enum[0] == "" && len(enum) > 1 && enum[1] == "appstore") {
					enums++
					seen := make(map[string]bool, len(enum))
					for _, item := range enum {
						seen[item] = true
					}
					for _, id := range want {
						if !seen[id] {
							t.Errorf("enum at %s is missing channel %q: %v", path, id, enum)
						}
					}
				}
			}
			for key, child := range value {
				walk(path+"."+key, child)
			}
		case []any:
			for index, child := range value {
				walk(fmt.Sprintf("%s[%d]", path, index), child)
			}
		}
	}
	walk("spec", spec)
	if enums < 5 {
		t.Fatalf("found %d channel enums in the OpenAPI spec, want at least 5", enums)
	}
}

// 渠道侧凭据落库 → 读出 → 渠道识别（ScanProduct / ProviderOf）→ 对外可见性
// （api_shared + 归属），四条渠道逐条走一遍真实数据库。
func TestChannelCredentialsRoundTripAndProviderRouting(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", APIToken: "synthetic-api-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()

	cases := []struct {
		provider string
		openid   string
	}{
		{provider: "pcmgr", openid: "pcmgr:10001"},
		{provider: "gameassist", openid: "synthetic-gameassist-openid"},
		{provider: "ldplayer", openid: "ldplayer:synthetic"},
		{provider: "qbbrowser", openid: "qbbrowser:synthetic"},
	}
	for _, channel := range cases {
		t.Run(channel.provider, func(t *testing.T) {
			creds := protocol.LoginBufferCredentials{
				Provider:               channel.provider,
				OpenID:                 channel.openid,
				AccessToken:            "synthetic-access",
				RefreshToken:           "synthetic-refresh",
				RefreshTokenObservedAt: time.Now().Unix(),
				ExpiresAt:              time.Now().Add(time.Hour).Unix(),
				ExpiresIn:              3600,
			}
			if channel.provider == "qbbrowser" {
				creds.QBBrowser = qbbrowser.Credentials{
					QBAccessToken:  "synthetic-ctk",
					OpenID:         "synthetic-wx-openid",
					UserID:         "synthetic-machine",
					WXRefreshToken: "synthetic-wx-refresh",
				}
			}
			status := "alive"
			acc, err := app.db.UpsertAccount(ctx, channel.openid, "synthetic-buffer", nil, nil, nil, nil, creds.ToMap(), &status)
			if err != nil {
				t.Fatalf("UpsertAccount: %v", err)
			}
			stored, err := app.db.GetAccount(ctx, acc.ID)
			if err != nil {
				t.Fatalf("GetAccount: %v", err)
			}
			if got := stored.ScanProduct(); got != channel.provider {
				t.Fatalf("ScanProduct()=%q, want %q", got, channel.provider)
			}
			if got := protocol.ProviderOf(stored); got != channel.provider {
				t.Fatalf("ProviderOf()=%q, want %q", got, channel.provider)
			}
			public := stored.Public()
			if public.ScanProduct != channel.provider {
				t.Fatalf("Public().ScanProduct=%q, want %q", public.ScanProduct, channel.provider)
			}
			// 下游默认 api_shared=1：脚本默认能读。
			if !public.APIShared {
				t.Fatal("a fresh scan must default to api_shared=true")
			}
			tokenRequest := httptest.NewRequest(http.MethodPost, "/wxapp/getCode", strings.NewReader("{}"))
			tokenRequest.Header.Set(apiTokenHeader, "synthetic-api-token")
			if _, err := app.resolveReadableAccount(tokenRequest, channel.openid); err != nil {
				t.Fatalf("token channel cannot read a shared account: %v", err)
			}
			// 关闭脚本读取后，令牌通道必须被挡住（而不是照旧返回 code）。
			if err := app.db.SetAccountShare(ctx, acc.ID, false); err != nil {
				t.Fatal(err)
			}
			if _, err := app.resolveReadableAccount(tokenRequest, channel.openid); !errors.Is(err, store.ErrAccountNotShared) {
				t.Fatalf("api_shared=false token read error=%v, want ErrAccountNotShared", err)
			}
		})
	}
}

// 渠道识别必须扛得住凭据被清空：失效账号只留 provider 标记（或 openid 命名空间），
// 保活/刷新/日志的渠道归属全部依赖它。
func TestChannelIdentitySurvivesCredentialWipe(t *testing.T) {
	app, err := NewApp(Config{ResourceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()
	for _, provider := range []string{"pcmgr", "gameassist", "ldplayer", "qbbrowser"} {
		status := "alive"
		creds := protocol.LoginBufferCredentials{
			Provider: provider, OpenID: provider + ":synthetic",
			AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}
		acc, err := app.db.UpsertAccount(ctx, creds.OpenID, "synthetic-buffer", nil, nil, nil, nil, creds.ToMap(), &status)
		if err != nil {
			t.Fatal(err)
		}
		applied, err := app.db.ApplyCredentialRefresh(ctx, acc, "", nil, store.StatusSessionOnly)
		if err != nil || !applied {
			t.Fatalf("%s: ApplyCredentialRefresh applied=%t err=%v", provider, applied, err)
		}
		limited, err := app.db.GetAccount(ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := limited.ScanProduct(); got != provider {
			t.Fatalf("%s: wiped account reports channel %q", provider, got)
		}
		if got := eventlog.ForAccount(limited).Channel; got != provider {
			t.Fatalf("%s: wiped account logs as channel %q", provider, got)
		}
	}
}

// 保活/续期调度按渠道分流：雷电与不支持刷新的 QQ 浏览器不能进自动续期循环，
// 电脑管家按原生密钥的续期时间表而不是 OAuth 有效期，手游助手按 syzs 的 token。
func TestRefreshSchedulingRoutesByProvider(t *testing.T) {
	now := time.Now()
	ahead := 45 * time.Minute
	cases := []struct {
		name  string
		creds protocol.LoginBufferCredentials
		want  bool
	}{
		{name: "appstore due soon", creds: protocol.LoginBufferCredentials{ExpiresAt: now.Add(time.Minute).Unix()}, want: true},
		{name: "appstore fresh", creds: protocol.LoginBufferCredentials{ExpiresAt: now.Add(2 * time.Hour).Unix()}, want: false},
		{
			name: "pcmgr follows its own renewal schedule",
			creds: protocol.LoginBufferCredentials{Provider: "pcmgr", ExpiresAt: now.Add(time.Minute).Unix(),
				PCMgr: pcmgr.Credentials{AccountID: 10001, LoginKey: "synthetic", ClientGUID: "00112233445566778899aabbccddeeff", RenewedAt: now.Add(time.Hour).Unix()}},
			want: false,
		},
		{
			// 已知续期间隔小于 15 天时官方策略是「尽快续」，这里是 60 秒后。
			name: "pcmgr renews when the native key is due",
			creds: protocol.LoginBufferCredentials{Provider: "pcmgr",
				PCMgr: pcmgr.Credentials{AccountID: 10001, LoginKey: "synthetic", ClientGUID: "00112233445566778899aabbccddeeff",
					RenewedAt: now.Add(-5 * time.Minute).Unix(), RenewInterval: 3600}},
			want: true,
		},
		{name: "gameassist due soon", creds: protocol.LoginBufferCredentials{Provider: "gameassist", ExpiresAt: now.Add(time.Minute).Unix()}, want: true},
		{name: "gameassist fresh", creds: protocol.LoginBufferCredentials{Provider: "gameassist", ExpiresAt: now.Add(2 * time.Hour).Unix()}, want: false},
		{name: "ldplayer never renews", creds: protocol.LoginBufferCredentials{Provider: "ldplayer", ExpiresAt: now.Add(-time.Hour).Unix()}, want: false},
		{name: "qbbrowser without refresh token never renews", creds: protocol.LoginBufferCredentials{Provider: "qbbrowser", ExpiresAt: now.Add(-time.Hour).Unix()}, want: false},
		{
			name: "qbbrowser with refresh token renews before expiry",
			creds: protocol.LoginBufferCredentials{Provider: "qbbrowser", ExpiresAt: now.Add(time.Minute).Unix(),
				QBBrowser: qbbrowser.Credentials{OpenID: "synthetic-wx-openid", WXRefreshToken: "synthetic-wx-refresh"}},
			want: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := credentialsDueForRefresh(testCase.creds, now, ahead); got != testCase.want {
				t.Fatalf("credentialsDueForRefresh=%t, want %t", got, testCase.want)
			}
		})
	}
}

// 渠道侧端点在两个令牌层同时配置时的状态码。QQ 浏览器伴生扩展只有一个
// Authorization 请求头，所以「API 令牌也算通过协议令牌层」必须成立，否则
// /api/qbbrowser/token 的上报链路在双令牌部署下永远是 401。
func TestChannelSideEndpointsUnderTheTokenGates(t *testing.T) {
	const (
		apiToken      = "synthetic-api-token"
		protocolToken = "synthetic-protocol-token"
	)
	app, err := NewApp(Config{
		ResourceRoot:  t.TempDir(),
		AuthDriver:    "sqlite",
		APIToken:      apiToken,
		ProtocolToken: protocolToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Handler()

	post := func(path, body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	cases := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
		want    int
	}{
		// 扩展只带协议令牌：API 令牌层仍然 fail-closed。
		{name: "extension with protocol token only", path: "/api/qbbrowser/token", body: "{}", headers: map[string]string{"Authorization": "Bearer " + protocolToken}, want: http.StatusUnauthorized},
		// 扩展只带 API 令牌：必须穿过协议令牌层并到达 handler（body 无效 → 400）。
		{name: "extension with api token as bearer", path: "/api/qbbrowser/token", body: "{}", headers: map[string]string{"Authorization": "Bearer " + apiToken}, want: http.StatusBadRequest},
		{name: "extension with api token header", path: "/api/qbbrowser/token", body: "{}", headers: map[string]string{apiTokenHeader: apiToken}, want: http.StatusBadRequest},
		{name: "extension with both tokens", path: "/api/qbbrowser/token", body: "{}", headers: map[string]string{"Authorization": "Bearer " + protocolToken, apiTokenHeader: apiToken}, want: http.StatusBadRequest},
		{name: "extension without credentials", path: "/api/qbbrowser/token", body: "{}", want: http.StatusUnauthorized},
		// 扫码入口（脚本侧）：渠道参数校验发生在鉴权之后。
		{name: "qr entry rejects unknown channel", path: "/wx/qrcodeauth", body: `{"product":"bogus"}`, headers: map[string]string{apiTokenHeader: apiToken}, want: http.StatusUnprocessableEntity},
		{name: "qr entry without credentials", path: "/wx/qrcodeauth", body: `{"product":"pcmgr"}`, want: http.StatusUnauthorized},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := post(testCase.path, testCase.body, testCase.headers)
			if recorder.Code != testCase.want {
				t.Fatalf("%s: status=%d want=%d body=%s", testCase.path, recorder.Code, testCase.want, recorder.Body.String())
			}
			if !json.Valid(recorder.Body.Bytes()) {
				t.Fatalf("%s: body is not JSON: %s", testCase.path, recorder.Body.String())
			}
		})
	}
}

// 编队：/login（wxcode 兼容取码）必须和 /wxapp/getCode 走同一条派发。
// 成员账号不能被直接调用，请求应得到 409 并指向主账号，且一个上游请求都不发。
func TestLoginCodeRouteHonoursFleetDispatch(t *testing.T) {
	app, primary, member := fleetTestSetup(t)
	handler := app.Handler()
	for _, ref := range []string{fmt.Sprint(member.ID), member.OpenID} {
		body := fmt.Sprintf(`{"app_id":"wxsynthetic","ref":%q}`, ref)
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(apiTokenHeader, app.cfg.APIToken)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("ref=%q: status=%d body=%s", ref, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"member_ref_not_allowed":true`) ||
			!strings.Contains(recorder.Body.String(), fmt.Sprintf(`"primary_ref":%d`, primary.ID)) {
			t.Fatalf("ref=%q: body=%s", ref, recorder.Body.String())
		}
	}
}

// 扫码入口的渠道参数校验与注册表一致：五条渠道都收，未知渠道报错。
func TestScanProductAcceptsEveryChannel(t *testing.T) {
	for _, id := range channelRegistryIDs() {
		if got, err := normalizeLoginProduct(id); err != nil || got != id {
			t.Fatalf("normalizeLoginProduct(%q)=(%q,%v)", id, got, err)
		}
	}
	if got, err := normalizeLoginProduct(""); err != nil || got != "appstore" {
		t.Fatalf("empty channel: (%q,%v)", got, err)
	}
	if _, err := normalizeLoginProduct("unknown-channel"); err == nil {
		t.Fatal("unknown channel was accepted")
	}
}
