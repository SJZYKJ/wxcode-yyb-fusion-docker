package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"yyb_go/internal/protocol"
)

func TestLegacyRefreshPolicyKeepsProviderAndCredentialOrigin(t *testing.T) {
	for _, provider := range []string{"", "appstore", "pcmgr", "unsupported"} {
		for _, source := range []string{"", "pc_yyb", "pc_yyb_auth", "unsupported"} {
			t.Run(provider+"/"+source, func(t *testing.T) {
				app := &App{cfg: Config{ASRefreshMode: "legacy_auth"}}
				input := protocol.LoginBufferCredentials{Provider: provider, RefreshBusinessID: source, RefreshTokenObservedAt: 123}
				want := source
				if (provider == "" || provider == "appstore") && source != "unsupported" {
					want = "pc_yyb_auth"
				}
				calls := 0
				app.refreshCredentials = func(_ context.Context, used protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
					calls++
					if used.Provider != provider || used.RefreshBusinessID != want {
						t.Fatal("refresh policy crossed credential provider/source boundaries")
					}
					used.AccessToken = "rotated-access"
					return used, nil
				}
				fresh, err := app.refreshCredentialsWithProxy(context.Background(), input, "", false)
				if err != nil || calls != 1 || fresh.RefreshBusinessID != source || fresh.Provider != provider || fresh.RefreshTokenObservedAt != 123 || fresh.AccessToken != "rotated-access" {
					t.Fatal("refresh lost rotated material or changed credential origin")
				}
			})
		}
	}
}

func TestLegacyRefreshDoesNotProbeOtherRoutesOnFailure(t *testing.T) {
	revoked := &protocol.RefreshRejectedError{Code: -109, Operation: "check_login", Message: "error 42007 | 42007"}
	for _, tc := range []struct {
		name       string
		source     string
		failure    error
		wantChecks int
	}{
		{"network error is not probed", "pc_yyb", context.DeadlineExceeded, 0},
		{"other rejection is not probed", "pc_yyb", &protocol.RefreshRejectedError{Code: -1, Message: "busy"}, 0},
		{"QR -109 asks check-login once", "pc_yyb", &protocol.RefreshRejectedError{Code: -109, Message: "RC_PARAMS_INVALID"}, 1},
		{"unmarked QR -109 asks check-login once", "", &protocol.RefreshRejectedError{Code: -109, Message: "RC_PARAMS_INVALID"}, 1},
		{"wxapp _auth credential is not probed", "pc_yyb_auth", &protocol.RefreshRejectedError{Code: -109, Message: "RC_PARAMS_INVALID"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: Config{ASRefreshMode: "legacy_auth"}}
			refreshes, checks := 0, 0
			app.refreshCredentials = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				refreshes++
				if creds.RefreshBusinessID != "pc_yyb_auth" {
					t.Fatal("legacy route not selected; the main refresh route must never be tried")
				}
				return protocol.LoginBufferCredentials{}, tc.failure
			}
			app.checkLogin = func(context.Context, protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				checks++
				return protocol.LoginBufferCredentials{}, revoked
			}
			_, err := app.refreshCredentialsWithProxy(context.Background(), protocol.LoginBufferCredentials{RefreshBusinessID: tc.source}, "", false)
			if !errors.Is(err, tc.failure) || refreshes != 1 || checks != tc.wantChecks {
				t.Fatalf("refreshes=%d checks=%d err=%v", refreshes, checks, err)
			}
			// Only the check-login answer makes an ambiguous -109 conclusive.
			if got := definitiveCredentialFailure(err); got != (tc.wantChecks == 1) {
				t.Fatalf("definitive = %t, want %t: %v", got, tc.wantChecks == 1, err)
			}
		})
	}
}

func TestLegacyRefreshAcceptsCheckLoginRecovery(t *testing.T) {
	app := &App{cfg: Config{ASRefreshMode: "legacy_auth"}}
	app.refreshCredentials = func(context.Context, protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
		return protocol.LoginBufferCredentials{}, &protocol.RefreshRejectedError{Code: -109, Message: "RC_PARAMS_INVALID"}
	}
	app.checkLogin = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
		creds.AccessToken = "checked-access"
		creds.RefreshBusinessID = "pc_yyb"
		return creds, nil
	}
	fresh, err := app.refreshCredentialsWithProxy(context.Background(), protocol.LoginBufferCredentials{RefreshTokenObservedAt: 77}, "", false)
	if err != nil || fresh.AccessToken != "checked-access" || fresh.RefreshBusinessID != "" || fresh.RefreshTokenObservedAt != 77 {
		t.Fatalf("check-login recovery = %+v, %v; want rotated access with the original source and scan time", fresh, err)
	}
}

func TestLegacyRefreshPersistsBeforeFetchingTicket(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "background", true: "forced"}[force], func(t *testing.T) {
			app := newKeepAliveTestApp(t)
			defer app.Close()
			app.cfg.ASRefreshMode = "legacy_auth"
			acc := insertKeepAliveTestAccount(t, app, "legacy-mode", time.Now().Add(time.Minute))
			original := protocol.CredentialsFromMap(acc.Credentials)
			app.refreshCredentials = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				if creds.RefreshBusinessID != "pc_yyb_auth" {
					t.Fatal("legacy refresh mode not applied to lifecycle refresh")
				}
				creds.AccessToken = "legacy-rotated-access"
				creds.ExpiresAt = time.Now().Add(2 * time.Hour).Unix()
				return creds, nil
			}
			fetches := 0
			app.fetchLoginBuffer = func(_ context.Context, creds protocol.LoginBufferCredentials) (string, error) {
				fetches++
				stored, err := app.db.GetAccount(context.Background(), acc.ID)
				if err != nil || stored.Credentials["accesstoken"] != "legacy-rotated-access" || creds.RefreshBusinessID != original.RefreshBusinessID {
					t.Fatal("ticket request preceded durable refresh or origin was changed")
				}
				return "legacy-fresh-ticket", nil
			}
			status, renewed, err := app.refreshAccount(context.Background(), acc, force)
			wantFetches := 0
			if force {
				wantFetches = 1
			}
			if err != nil || status != "alive" || !renewed || fetches != wantFetches {
				t.Fatalf("refresh status=%s renewed=%t fetches=%d err=%v", status, renewed, fetches, err)
			}
			stored, err := app.db.GetAccount(context.Background(), acc.ID)
			if err != nil || stored.CreatedAt != acc.CreatedAt || protocol.CredentialsFromMap(stored.Credentials).RefreshBusinessID != original.RefreshBusinessID {
				t.Fatal("legacy mode changed scan/source metadata")
			}
		})
	}
}

func TestInvalidAppstoreRefreshModeRejectedBeforeResourceCreation(t *testing.T) {
	if app, err := NewApp(Config{ASRefreshMode: "unexpected"}); err == nil || app != nil {
		t.Fatal("invalid refresh mode silently accepted")
	}
}

func TestDefaultAndExplicitAppstoreRefreshRouting(t *testing.T) {
	for _, mode := range []string{"", "  ", "legacy_auth", " LEGACY_AUTH ", "source"} {
		t.Run(mode, func(t *testing.T) {
			app, err := NewApp(Config{ResourceRoot: t.TempDir(), ASRefreshMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			wantMode, wantBusiness := "legacy_auth", "pc_yyb_auth"
			if mode == "source" {
				wantMode, wantBusiness = "source", "pc_yyb"
			}
			if app.cfg.ASRefreshMode != wantMode {
				t.Fatalf("effective mode = %q, want %q", app.cfg.ASRefreshMode, wantMode)
			}
			calls := 0
			app.refreshCredentials = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferCredentials, error) {
				calls++
				if creds.RefreshBusinessID != wantBusiness {
					t.Fatalf("refresh business = %q, want %q", creds.RefreshBusinessID, wantBusiness)
				}
				return creds, nil
			}
			fresh, err := app.refreshCredentialsWithProxy(context.Background(), protocol.LoginBufferCredentials{Provider: "appstore", RefreshBusinessID: "pc_yyb"}, "", false)
			if err != nil || calls != 1 || fresh.RefreshBusinessID != "pc_yyb" {
				t.Fatal("default/explicit route changed credential origin or call count", err)
			}
		})
	}
}
