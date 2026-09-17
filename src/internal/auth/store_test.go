package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteAuthenticationLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	if err := store.BootstrapAdmin(ctx, "", ""); err != nil {
		t.Fatalf("BootstrapAdmin() without credentials error = %v", err)
	}
	if err := store.BootstrapAdmin(ctx, "admin", ""); err != nil {
		t.Fatalf("BootstrapAdmin() with blank password error = %v", err)
	}
	admin, err := store.RegisterUser(ctx, "owner", "Owner", "owner-password")
	if err != nil {
		t.Fatalf("RegisterUser(first) error = %v", err)
	}
	if admin.Role != "admin" {
		t.Fatalf("first registered role = %q, want admin", admin.Role)
	}
	user, err := store.RegisterUser(ctx, "member", "Member", "member-password")
	if err != nil {
		t.Fatalf("RegisterUser(second) error = %v", err)
	}
	if user.Role != "user" {
		t.Fatalf("second registered role = %q, want user", user.Role)
	}
	if _, err := store.RegisterUser(ctx, "member", "Duplicate", "another-password"); err == nil || !strings.Contains(err.Error(), "用户名已存在") {
		t.Fatalf("duplicate RegisterUser() error = %v", err)
	}

	authenticated, err := store.Authenticate(ctx, "OWNER", "owner-password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	token, session, err := store.CreateSession(ctx, authenticated.ID, "test-agent", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	gotUser, gotSession, err := store.UserBySession(ctx, token)
	if err != nil {
		t.Fatalf("UserBySession() error = %v", err)
	}
	if gotUser.ID != admin.ID || gotSession.ID != session.ID {
		t.Fatalf("UserBySession() = user %d session %d, want user %d session %d", gotUser.ID, gotSession.ID, admin.ID, session.ID)
	}
	if err := store.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, _, err := store.UserBySession(ctx, token); err == nil {
		t.Fatal("deleted session remains usable")
	}

	if err := store.SetRegistrationEnabled(ctx, false); err != nil {
		t.Fatalf("SetRegistrationEnabled(false) error = %v", err)
	}
	enabled, err := store.RegistrationEnabled(ctx)
	if err != nil || enabled {
		t.Fatalf("RegistrationEnabled() = %v, %v; want false, nil", enabled, err)
	}
	if err := store.SetRegistrationEnabled(ctx, true); err != nil {
		t.Fatalf("SetRegistrationEnabled(true) error = %v", err)
	}
	enabled, err = store.RegistrationEnabled(ctx)
	if err != nil || !enabled {
		t.Fatalf("RegistrationEnabled() = %v, %v; want true, nil", enabled, err)
	}
}

func TestSQLiteBootstrapAdmin(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	if err := store.BootstrapAdmin(ctx, "admin", "bootstrap-password"); err != nil {
		t.Fatalf("BootstrapAdmin() error = %v", err)
	}
	user, err := store.Authenticate(ctx, "admin", "bootstrap-password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("bootstrap role = %q, want admin", user.Role)
	}
}
