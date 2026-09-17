package httpapi

import (
	"context"
	"fmt"
	"log"
	"time"

	"yyb_go/internal/protocol"
	"yyb_go/internal/store"
)

func (a *App) startKeepAlive() {
	if a.cfg.KeepAliveInterval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.keepAliveCancel = cancel
	a.keepAliveDone = make(chan struct{})
	log.Printf("keepalive: enabled interval=%s refresh_ahead=%s", a.cfg.KeepAliveInterval, a.cfg.KeepAliveAhead)
	go func() {
		defer close(a.keepAliveDone)
		a.keepAliveLoop(ctx)
	}()
}

func (a *App) keepAliveLoop(ctx context.Context) {
	a.refreshDueAccounts(ctx)
	ticker := time.NewTicker(a.cfg.KeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshDueAccounts(ctx)
		}
	}
}

func (a *App) refreshDueAccounts(ctx context.Context) {
	accounts, err := a.db.ListAccounts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("keepalive: list accounts: %v", err)
		}
		return
	}
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return
		}
		_, refreshed, err := a.refreshAccount(ctx, acc, false)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("keepalive: account id=%d refresh failed: %v", acc.ID, err)
			}
			continue
		}
		if refreshed {
			log.Printf("keepalive: account id=%d credentials renewed", acc.ID)
		}
	}
}

func (a *App) refreshLiveness(ctx context.Context, acc *store.WechatAccount) string {
	status, _, _ := a.refreshAccount(ctx, acc, true)
	if status == "alive" {
		if avatar := a.resolveAvatar(ctx, acc.OpenID, acc.UserInfo); avatar != "" {
			_ = a.db.SetAccountProfile(ctx, acc.ID, acc.Nickname, &avatar, acc.UserInfo)
		}
	}
	return status
}

func (a *App) refreshAccount(ctx context.Context, acc *store.WechatAccount, force bool) (string, bool, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	latest, err := a.db.GetAccount(ctx, acc.ID)
	if err != nil {
		return "unknown", false, err
	}
	if latest.Credentials == nil {
		err = fmt.Errorf("credentials are missing")
		if setErr := a.db.SetAccountStatus(ctx, latest.ID, "unknown"); setErr != nil {
			err = fmt.Errorf("%v; update status: %w", err, setErr)
		}
		return "unknown", false, err
	}

	creds := protocol.CredentialsFromMap(latest.Credentials)
	if !force && !credentialsDueForRefresh(creds, time.Now(), a.cfg.KeepAliveAhead) {
		return accountStatus(latest), false, nil
	}

	result, err := a.refreshLoginBuffer(ctx, creds)
	if err != nil {
		status := accountStatus(latest)
		if force || creds.ExpiresAt <= time.Now().Unix() {
			status = "expired"
		}
		if setErr := a.db.SetAccountStatus(ctx, latest.ID, status); setErr != nil {
			err = fmt.Errorf("%v; update status: %w", err, setErr)
		}
		return status, false, err
	}
	if err = a.db.SetAccountCredentialStatus(ctx, latest.ID, result.LoginBuffer, result.Credentials.ToMap(), "alive"); err != nil {
		return "expired", false, err
	}
	return "alive", true, nil
}

func credentialsDueForRefresh(creds protocol.LoginBufferCredentials, now time.Time, ahead time.Duration) bool {
	return creds.ExpiresAt <= 0 || now.Add(ahead).Unix() >= creds.ExpiresAt
}

func accountStatus(acc *store.WechatAccount) string {
	if acc.Status == nil || *acc.Status == "" {
		return "unknown"
	}
	return *acc.Status
}
