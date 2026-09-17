package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"yyb_go/internal/protocol"
	"yyb_go/internal/store"
)

func TestRefreshAccountRenewsDueCredentials(t *testing.T) {
	app := newKeepAliveTestApp(t)
	defer app.Close()

	acc := insertKeepAliveTestAccount(t, app, "openid-due", time.Now().Add(10*time.Minute))
	calls := 0
	app.refreshLoginBuffer = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferResult, error) {
		calls++
		creds.AccessToken = "access-new"
		creds.RefreshToken = "refresh-new"
		creds.ExpiresAt = time.Now().Add(2 * time.Hour).Unix()
		return protocol.LoginBufferResult{LoginBuffer: "buffer-new", Credentials: creds, Refreshed: true}, nil
	}

	status, refreshed, err := app.refreshAccount(context.Background(), acc, false)
	if err != nil {
		t.Fatalf("refreshAccount() error = %v", err)
	}
	if status != "alive" || !refreshed || calls != 1 {
		t.Fatalf("refreshAccount() = status %q, refreshed %v, calls %d", status, refreshed, calls)
	}
	updated, err := app.db.GetAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("GetAccount() error = %v", err)
	}
	if updated.LoginBuffer != "buffer-new" || updated.Credentials["accesstoken"] != "access-new" || updated.Credentials["refreshtoken"] != "refresh-new" {
		t.Fatalf("updated credentials = %#v, login buffer = %q", updated.Credentials, updated.LoginBuffer)
	}
}

func TestRefreshAccountSkipsFreshCredentials(t *testing.T) {
	app := newKeepAliveTestApp(t)
	defer app.Close()

	acc := insertKeepAliveTestAccount(t, app, "openid-fresh", time.Now().Add(2*time.Hour))
	calls := 0
	app.refreshLoginBuffer = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferResult, error) {
		calls++
		return protocol.LoginBufferResult{}, nil
	}

	status, refreshed, err := app.refreshAccount(context.Background(), acc, false)
	if err != nil {
		t.Fatalf("refreshAccount() error = %v", err)
	}
	if status != "alive" || refreshed || calls != 0 {
		t.Fatalf("refreshAccount() = status %q, refreshed %v, calls %d", status, refreshed, calls)
	}
}

func TestRefreshAccountRetriesAfterFailure(t *testing.T) {
	app := newKeepAliveTestApp(t)
	defer app.Close()

	acc := insertKeepAliveTestAccount(t, app, "openid-retry", time.Now().Add(10*time.Minute))
	calls := 0
	app.refreshLoginBuffer = func(_ context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferResult, error) {
		calls++
		if calls == 1 {
			return protocol.LoginBufferResult{}, errors.New("temporary failure")
		}
		creds.AccessToken = "access-recovered"
		creds.ExpiresAt = time.Now().Add(2 * time.Hour).Unix()
		return protocol.LoginBufferResult{LoginBuffer: "buffer-recovered", Credentials: creds, Refreshed: true}, nil
	}

	status, _, err := app.refreshAccount(context.Background(), acc, false)
	if err == nil || status != "alive" {
		t.Fatalf("first refresh = status %q, error %v", status, err)
	}
	status, refreshed, err := app.refreshAccount(context.Background(), acc, false)
	if err != nil || status != "alive" || !refreshed || calls != 2 {
		t.Fatalf("second refresh = status %q, refreshed %v, calls %d, error %v", status, refreshed, calls, err)
	}
}

func TestCloseStopsKeepAliveLoop(t *testing.T) {
	app, err := NewApp(Config{
		ResourceRoot:      t.TempDir(),
		RequestTimeout:    time.Second,
		KeepAliveInterval: time.Hour,
		KeepAliveAhead:    45 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- app.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not stop keepalive loop")
	}
}

func newKeepAliveTestApp(t *testing.T) *App {
	t.Helper()
	app, err := NewApp(Config{
		ResourceRoot:   t.TempDir(),
		RequestTimeout: time.Second,
		AvatarTimeout:  time.Second,
		SessionTTL:     time.Minute,
		QRSessionTTL:   time.Minute,
		KeepAliveAhead: 45 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	return app
}

func insertKeepAliveTestAccount(t *testing.T, app *App, openID string, expiresAt time.Time) *store.WechatAccount {
	t.Helper()
	status := "alive"
	creds := protocol.LoginBufferCredentials{
		OpenID:       openID,
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		ExpiresAt:    expiresAt.Unix(),
		ExpiresIn:    7200,
	}
	acc, err := app.db.UpsertAccount(context.Background(), openID, "buffer-old", nil, nil, nil, nil, creds.ToMap(), &status, nil)
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	return acc
}
