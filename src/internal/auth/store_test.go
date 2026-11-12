package auth

import (
	"context"
	"database/sql"
	"errors"
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

func TestRecoverAdminPasswordPreservesOtherDataAndRevokesOnlyTargetSessions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.RegisterUser(ctx, "owner", "Owner", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.RegisterUser(ctx, "member", "Member", "member-password")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := store.CreateSession(ctx, admin.ID, "test", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := store.CreateSession(ctx, member.ID, "test", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimAccount(ctx, 9, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRegistrationEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAdminPassword(ctx, "OWNER", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, "owner", "old-password"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := store.Authenticate(ctx, "owner", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UserBySession(ctx, adminToken); err == nil {
		t.Fatal("old administrator session still works")
	}
	if _, _, err := store.UserBySession(ctx, memberToken); err != nil {
		t.Fatalf("member session lost: %v", err)
	}
	if owner, err := store.AccountOwner(ctx, 9); err != nil || owner != member.ID {
		t.Fatalf("account ownership changed: %d, %v", owner, err)
	}
	if enabled, err := store.RegistrationEnabled(ctx); err != nil || enabled {
		t.Fatalf("registration setting changed: %v, %v", enabled, err)
	}
	for _, username := range []string{"member", "missing"} {
		if err := store.RecoverAdminPassword(ctx, username, "another-password"); err == nil {
			t.Fatalf("recovered non-admin %q", username)
		}
	}
	if _, err := store.Authenticate(ctx, "member", "member-password"); err != nil {
		t.Fatal("member password was modified")
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE users SET enabled=FALSE WHERE id=?", admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAdminPassword(ctx, "owner", "another-password"); err == nil {
		t.Fatal("disabled admin was recovered")
	}
}

func TestRecoverAdminPasswordRollsBackWhenSessionDeletionFails(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BootstrapAdmin(ctx, "owner", "old-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TABLE user_sessions"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAdminPassword(ctx, "owner", "new-password"); err == nil {
		t.Fatal("recovery succeeded without revoking sessions")
	}
	if _, err := store.Authenticate(ctx, "owner", "old-password"); err != nil {
		t.Fatalf("password was changed despite failed transaction: %v", err)
	}
}

// TestLoginCountTracksSuccessfulLogins 钉住上游 v0.3.3 的 users.login_count 语义：
// 每次登录成功 +1、失败不计数，并且所有用户查询（ListUsers / GetUser /
// ListUsersWithEmail / GetUserWithEmail）都按同一列顺序读回同一个值——
// 这几处 SELECT 各自维护列清单，漏改一处就会 scan 错位。
func TestLoginCountTracksSuccessfulLogins(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	member, err := store.RegisterUser(ctx, "counter", "Counter", "counter-password")
	if err != nil {
		t.Fatalf("RegisterUser() error = %v", err)
	}
	if member.LoginCount != 0 {
		t.Fatalf("fresh user login_count = %d, want 0", member.LoginCount)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		logged, err := store.Authenticate(ctx, "counter", "counter-password")
		if err != nil {
			t.Fatalf("Authenticate() #%d error = %v", attempt, err)
		}
		if logged.LoginCount != int64(attempt) {
			t.Fatalf("login_count after login #%d = %d, want %d", attempt, logged.LoginCount, attempt)
		}
	}
	// 失败登录不该计数。
	if _, err := store.Authenticate(ctx, "counter", "wrong-password"); err == nil {
		t.Fatal("Authenticate() with a wrong password succeeded")
	}

	checkCounter := func(where string, got int64) {
		t.Helper()
		if got != 2 {
			t.Fatalf("%s login_count = %d, want 2", where, got)
		}
	}
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	for _, user := range users {
		if user.ID == member.ID {
			checkCounter("ListUsers", user.LoginCount)
		}
	}
	byID, err := store.GetUser(ctx, member.ID)
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	checkCounter("GetUser", byID.LoginCount)
	withEmail, _, err := store.GetUserWithEmail(ctx, member.ID)
	if err != nil {
		t.Fatalf("GetUserWithEmail() error = %v", err)
	}
	checkCounter("GetUserWithEmail", withEmail.LoginCount)
	listed, _, err := store.ListUsersWithEmail(ctx)
	if err != nil {
		t.Fatalf("ListUsersWithEmail() error = %v", err)
	}
	for _, user := range listed {
		if user.ID == member.ID {
			checkCounter("ListUsersWithEmail", user.LoginCount)
		}
	}
	// userSelectColumns/scanUser 走的是按邮箱查的那条路径，一并确认没有错位。
	byEmail, err := store.UserByEmail(ctx, "counter@example.com")
	if err == nil {
		checkCounter("UserByEmail", byEmail.LoginCount)
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UserByEmail() unexpected error = %v", err)
	}
	if err := store.SetUserEmail(ctx, member.ID, "counter@example.com"); err != nil {
		t.Fatalf("SetUserEmail() error = %v", err)
	}
	byEmail, err = store.UserByEmail(ctx, "counter@example.com")
	if err != nil {
		t.Fatalf("UserByEmail() after SetUserEmail error = %v", err)
	}
	checkCounter("UserByEmail", byEmail.LoginCount)
}

// TestOpenMigratesLegacyUsersTableWithoutLoginCount 覆盖升级场景：老库的 users 表
// 没有 login_count，CREATE TABLE IF NOT EXISTS 不会补列，必须靠显式迁移。
func TestOpenMigratesLegacyUsersTableWithoutLoginCount(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	legacySchema := `CREATE TABLE users (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        username TEXT NOT NULL UNIQUE,
        display_name TEXT NOT NULL,
        password_hash TEXT NOT NULL,
        role TEXT NOT NULL DEFAULT 'user',
        enabled BOOLEAN NOT NULL DEFAULT TRUE,
        last_login_at DATETIME NULL,
        created_at DATETIME NOT NULL,
        updated_at DATETIME NOT NULL
    )`
	if _, err := raw.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
	now := time.Now().UTC()
	if _, err := raw.ExecContext(ctx,
		"INSERT INTO users (username, display_name, password_hash, role, enabled, created_at, updated_at) VALUES (?, ?, ?, 'user', TRUE, ?, ?)",
		"legacy", "Legacy", "not-a-bcrypt-hash", now, now); err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatalf("Open() on legacy database error = %v", err)
	}
	defer store.Close()

	// 老数据补列后取默认值 0，且查询不再报 "no such column: login_count"。
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() on migrated database error = %v", err)
	}
	if len(users) != 1 || users[0].Username != "legacy" || users[0].LoginCount != 0 {
		t.Fatalf("migrated users = %+v, want one legacy user with login_count 0", users)
	}
	// 迁移后的库照常能登录计数（新用户走 Go 侧 bcrypt）。
	created, err := store.CreateUser(ctx, "fresh", "Fresh", "fresh-password", "user")
	if err != nil {
		t.Fatalf("CreateUser() after migration error = %v", err)
	}
	if _, err := store.Authenticate(ctx, "fresh", "fresh-password"); err != nil {
		t.Fatalf("Authenticate() after migration error = %v", err)
	}
	counted, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser() after migration error = %v", err)
	}
	if counted.LoginCount != 1 {
		t.Fatalf("login_count after first login on migrated database = %d, want 1", counted.LoginCount)
	}
	// 重复触发迁移必须幂等（第二次 Open 不能再 ALTER 报 duplicate column）。
	second, err := Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatalf("second Open() must stay idempotent: %v", err)
	}
	_ = second.Close()
}
