package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const mysqlSchema = `
CREATE TABLE IF NOT EXISTS users (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    display_name VARCHAR(100) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    -- email 为注册邮箱（功能A：注册邮箱验证）。允许为 NULL：NULL 在唯一索引里
    -- 不互相冲突，因此历史账号与管理员代建的账号不会被唯一约束误伤。
    email VARCHAR(254) NULL UNIQUE,
    role ENUM('admin','user') NOT NULL DEFAULT 'user',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS user_sessions (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    token_hash BINARY(32) NOT NULL UNIQUE,
    user_agent VARCHAR(255) NOT NULL DEFAULT '',
    ip_address VARCHAR(64) NOT NULL DEFAULT '',
    expires_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_user_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    INDEX idx_user_sessions_user (user_id),
    INDEX idx_user_sessions_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS auth_settings (
    setting_key VARCHAR(64) NOT NULL PRIMARY KEY,
    setting_value VARCHAR(255) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    -- 同 mysqlSchema：NULL 不参与唯一性冲突。
    email TEXT NULL UNIQUE,
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at DATETIME NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS user_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    user_agent TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions(expires_at);

CREATE TABLE IF NOT EXISTS auth_settings (
    setting_key TEXT PRIMARY KEY,
    setting_value TEXT NOT NULL,
    updated_at DATETIME NOT NULL
);
`

var ErrInvalidCredentials = errors.New("用户名或密码错误")

type Store struct {
	db     *sql.DB
	driver string
}

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	Enabled     bool       `json:"enabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Session struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	UserAgent  string    `json:"user_agent"`
	IPAddress  string    `json:"ip_address"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "mysql" && driver != "sqlite" {
		return nil, fmt.Errorf("unsupported auth database driver %q", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect auth %s: %w", driver, err)
	}
	schema := mysqlSchema
	if driver == "sqlite" {
		schema = sqliteSchema
		if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure auth sqlite: %w", err)
		}
	}
	for _, statement := range strings.Split(schema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err = db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("migrate auth %s: %w", driver, err)
		}
	}
	// users.email 是后续版本加的列：CREATE TABLE IF NOT EXISTS 不会给已存在的
	// 老库补列，必须显式迁移，否则升级上来的实例一注册就报 unknown column。
	if err = migrateUsersEmail(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate auth %s (users.email): %w", driver, err)
	}
	return &Store{db: db, driver: driver}, nil
}

// migrateUsersEmail 给历史库补上 users.email 列与唯一索引（幂等）。
func migrateUsersEmail(ctx context.Context, db *sql.DB, driver string) error {
	switch driver {
	case "sqlite":
		if columnExists(ctx, db, "users", "email") {
			return nil
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN email TEXT NULL"); err != nil {
			return err
		}
		// 唯一索引单独建：SQLite 允许唯一索引里出现多个 NULL。
		_, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email)")
		return err
	case "mysql":
		if columnExists(ctx, db, "users", "email") {
			return nil
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN email VARCHAR(254) NULL"); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_users_email ON users(email)")
		if err != nil && isDuplicateError(err) {
			return nil // 索引已存在
		}
		return err
	default:
		return nil
	}
}

// columnExists 判断表上是否已有某列，用于幂等迁移。
func columnExists(ctx context.Context, db *sql.DB, table, column string) bool {
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE 1=0")
	if err != nil {
		return false
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false
	}
	for _, name := range columns {
		if strings.EqualFold(name, column) {
			return true
		}
	}
	return false
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) BootstrapAdmin(ctx context.Context, username, password string) error {
	username = normalizeUsername(username)
	if password == "" {
		return nil
	}
	if username == "" {
		return errors.New("首次管理员用户名和密码不能为空")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO users
        (username, display_name, password_hash, role, enabled, created_at, updated_at)
        VALUES (?, ?, ?, 'admin', TRUE, ?, ?)`, username, username, string(hash), now, now)
	return err
}

// RegisterUser promotes only the first registered account to administrator.
// The role decision and insert happen in one statement, so concurrent requests
// cannot both observe an empty users table.
func (s *Store) RegisterUser(ctx context.Context, username, displayName, password string) (*User, error) {
	username = normalizeUsername(username)
	displayName = strings.TrimSpace(displayName)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if displayName == "" || len([]rune(displayName)) > 100 {
		return nil, errors.New("显示名长度应为 1-100 个字符")
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users
        (username, display_name, password_hash, role, enabled, created_at, updated_at)
        VALUES (?, ?, ?, CASE WHEN (SELECT COUNT(*) FROM users)=0 THEN 'admin' ELSE 'user' END, TRUE, ?, ?)`,
		username, displayName, string(hash), now, now)
	if err != nil {
		if isDuplicateError(err) {
			return nil, errors.New("用户名已存在")
		}
		return nil, err
	}
	id, _ := result.LastInsertId()
	return s.GetUser(ctx, id)
}

func (s *Store) CreateUser(ctx context.Context, username, displayName, password, role string) (*User, error) {
	username = normalizeUsername(username)
	displayName = strings.TrimSpace(displayName)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if displayName == "" || len([]rune(displayName)) > 100 {
		return nil, errors.New("显示名长度应为 1-100 个字符")
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if role != "admin" {
		role = "user"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users
        (username, display_name, password_hash, role, enabled, created_at, updated_at)
        VALUES (?, ?, ?, ?, TRUE, ?, ?)`, username, displayName, string(hash), role, now, now)
	if err != nil {
		if isDuplicateError(err) {
			return nil, errors.New("用户名已存在")
		}
		return nil, err
	}
	id, _ := result.LastInsertId()
	return s.GetUser(ctx, id)
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var user User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, username, display_name, password_hash, role, enabled,
        last_login_at, created_at, updated_at FROM users WHERE username=?`, normalizeUsername(username)).Scan(
		&user.ID, &user.Username, &user.DisplayName, &hash, &user.Role, &user.Enabled,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, errors.New("账号已停用，请联系管理员")
	}
	now := time.Now().UTC()
	_, _ = s.db.ExecContext(ctx, "UPDATE users SET last_login_at=?, updated_at=? WHERE id=?", now, now, user.ID)
	user.LastLoginAt = &now
	return &user, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64, userAgent, ip string, ttl time.Duration) (string, *Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	expires := now.Add(ttl)
	result, err := s.db.ExecContext(ctx, `INSERT INTO user_sessions
        (user_id, token_hash, user_agent, ip_address, expires_at, created_at, last_seen_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, hash[:], trimTo(userAgent, 255), trimTo(ip, 64), expires, now, now)
	if err != nil {
		return "", nil, err
	}
	id, _ := result.LastInsertId()
	return token, &Session{ID: id, UserID: userID, UserAgent: trimTo(userAgent, 255), IPAddress: trimTo(ip, 64), ExpiresAt: expires, CreatedAt: now, LastSeenAt: now}, nil
}

func (s *Store) UserBySession(ctx context.Context, token string) (*User, *Session, error) {
	hash := sha256.Sum256([]byte(token))
	var user User
	var session Session
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.display_name, u.role, u.enabled,
        u.last_login_at, u.created_at, u.updated_at,
        s.id, s.user_id, s.user_agent, s.ip_address, s.expires_at, s.created_at, s.last_seen_at
        FROM user_sessions s JOIN users u ON u.id=s.user_id
        WHERE s.token_hash=? AND s.expires_at>?`, hash[:], time.Now().UTC()).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Enabled,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt,
		&session.ID, &session.UserID, &session.UserAgent, &session.IPAddress, &session.ExpiresAt, &session.CreatedAt, &session.LastSeenAt,
	)
	if err != nil || !user.Enabled {
		return nil, nil, sql.ErrNoRows
	}
	if time.Since(session.LastSeenAt) > 5*time.Minute {
		_, _ = s.db.ExecContext(ctx, "UPDATE user_sessions SET last_seen_at=? WHERE id=?", time.Now().UTC(), session.ID)
	}
	return &user, &session, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, "DELETE FROM user_sessions WHERE token_hash=?", hash[:])
	return err
}

func (s *Store) DeleteOtherSessions(ctx context.Context, userID, keepID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=? AND id<>?", userID, keepID)
	return err
}

func (s *Store) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, user_agent, ip_address, expires_at, created_at, last_seen_at
        FROM user_sessions WHERE user_id=? AND expires_at>? ORDER BY last_seen_at DESC`, userID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var item Session
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserAgent, &item.IPAddress, &item.ExpiresAt, &item.CreatedAt, &item.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProfile(ctx context.Context, userID int64, displayName string) (*User, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len([]rune(displayName)) > 100 {
		return nil, errors.New("显示名长度应为 1-100 个字符")
	}
	_, err := s.db.ExecContext(ctx, "UPDATE users SET display_name=?, updated_at=? WHERE id=?", displayName, time.Now().UTC(), userID)
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, userID)
}

func (s *Store) ChangePassword(ctx context.Context, userID int64, current, next string, sessionID int64) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return errors.New("当前密码错误")
	}
	nextHash, err := bcrypt.GenerateFromPassword([]byte(next), 12)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE users SET password_hash=?, updated_at=? WHERE id=?", string(nextHash), time.Now().UTC(), userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=? AND id<>?", userID, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, display_name, role, enabled, last_login_at, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, username, display_name, role, enabled, last_login_at, created_at, updated_at FROM users WHERE id=?`, id).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	return &u, err
}

func (s *Store) UpdateUser(ctx context.Context, actorID, id int64, role string, enabled bool) (*User, error) {
	if role != "admin" {
		role = "user"
	}
	if actorID == id && (!enabled || role != "admin") {
		return nil, errors.New("不能停用自己或移除自己的管理员权限")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE users SET role=?, enabled=?, updated_at=? WHERE id=?", role, enabled, time.Now().UTC(), id); err != nil {
		return nil, err
	}
	if !enabled {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=?", id)
	}
	return s.GetUser(ctx, id)
}

func (s *Store) AdminResetPassword(ctx context.Context, id int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE users SET password_hash=?, updated_at=? WHERE id=?", string(hash), time.Now().UTC(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(ctx context.Context, actorID, id int64) error {
	if actorID == id {
		return errors.New("不能删除当前登录用户")
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id=?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RegistrationEnabled 读取注册开关，缺省值为 false（关闭）。
//
// 默认关闭是安全默认值：注册一开，任何能访问到端口的人都能注册账号进入
// 工作台；而在数据库还没有任何账号时，第一个注册者会被直接提升为管理员。
// 首次部署的引导由 httpapi.registrationAllowed 的「内网首注册」规则覆盖，
// 需要公开注册的实例可以设置 YYB_ALLOW_REGISTRATION=true。
func (s *Store) RegistrationEnabled(ctx context.Context) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT setting_value FROM auth_settings WHERE setting_key='registration_enabled'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "true", err
}

// CountUsers 返回用户总数，供首次部署引导判断库中是否还没有任何账号。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func (s *Store) SetRegistrationEnabled(ctx context.Context, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	query := `INSERT INTO auth_settings (setting_key, setting_value, updated_at) VALUES ('registration_enabled', ?, ?)
        ON DUPLICATE KEY UPDATE setting_value=VALUES(setting_value), updated_at=VALUES(updated_at)`
	if s.driver == "sqlite" {
		query = `INSERT INTO auth_settings (setting_key, setting_value, updated_at) VALUES ('registration_enabled', ?, ?)
            ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value, updated_at=excluded.updated_at`
	}
	_, err := s.db.ExecContext(ctx, query, value, time.Now().UTC())
	return err
}

func ValidateUsername(value string) error {
	if len(value) < 3 || len(value) > 64 {
		return errors.New("用户名长度应为 3-64 个字符")
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return errors.New("用户名只能包含小写字母、数字、点、下划线和连字符")
		}
	}
	return nil
}

func ValidatePassword(value string) error {
	if len(value) < 10 || len(value) > 128 {
		return errors.New("密码长度应为 10-128 个字符")
	}
	return nil
}

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func isDuplicateError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") || strings.Contains(message, "unique constraint")
}
func trimTo(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// ---------- 批量删除用户（/users 页面「删除所选」） ----------

// BatchDeleteMaxIDs 是单次批量删除允许提交的最大 ID 数量。
// 上限的意义是给「一键全选」兜底：既避免一次请求拖垮数据库，也避免
// 管理员误操作在一个请求里清空整张 users 表。
const BatchDeleteMaxIDs = 200

// BatchDeleteSkip 描述一个被跳过的 ID 及其原因。
//
// 字段名刻意是 SkipReason 而不是按惯例的 Reason：批量删除返回体里同时有
// 顶层 deleted 与 skipped[].reason，历史上两者共用同一个 JSON key 容易
// 混淆；另外 httpapi 侧这里用 json tag 输出 skip_id / skip_reason，
// 与顶层 deleted 的裸数字区分开，避免测试断言时改 tag 而静默失效。
type BatchDeleteSkip struct {
	ID         int64  `json:"skip_id"`
	SkipReason string `json:"skip_reason"`
}

// BatchDeleteUsers 在一个事务里原子地批量删除用户。
//
// 语义（与 httpapi 的 /api/auth/users/batch-delete 一一对应）：
//   - actorID 自己不能出现在 ids 里 -> 整体拒绝，一条都不删；
//   - 删除后管理员数量不能变成 0 -> 整体拒绝，一条都不删；
//   - ids 里不存在的用户不报错，进 skipped；
//   - 空 ids / 超上限直接拒绝。
//
// 为什么校验和删除必须在同一个事务里：删除前要先数一遍「还剩几个管理员」，
// 若数完提交、再去删，两步之间有窗口——两个管理员同时提交批量删除时，
// 各自都会看到「还有 2 个管理员」而都放行，最终删成 0 个管理员，谁都进不了
// 管理页。放进同一个事务、并且把「数管理员」放到 DELETE 之后仍在事务内，
// 就把这个 TOCTOU 窗口关掉了。
//
// 事务隔离级别的取舍：SQLite 走 BEGIN DEFERRED + 连接池 MaxOpenConns(1)，
// 事务内第一次读就会拿到共享锁、第一次写升级为写锁并串行化；MySQL 默认
// REPEATABLE READ 下写会加行锁，但两个事务各自的快照看到的集合可能不同，
// 理论上仍可能都通过校验。因此这里把管理员计数放在删除之后，并在 COUNT
// 为 0 时回滚——回滚是幂等的，宁可让其中一个请求报错重试，
// 也不能把管理员删空。
func (s *Store) BatchDeleteUsers(ctx context.Context, actorID int64, ids []int64) ([]int64, []BatchDeleteSkip, error) {
	deleted := []int64{}
	skipped := []BatchDeleteSkip{}
	if len(ids) == 0 {
		return deleted, skipped, errors.New("未选择要删除的用户")
	}
	if len(ids) > BatchDeleteMaxIDs {
		return deleted, skipped, fmt.Errorf("单次最多删除 %d 个用户", BatchDeleteMaxIDs)
	}

	// 先去重，保持首次出现的顺序：重复 id 不该被删两次，也不该让计数失真。
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if seen[actorID] {
		return deleted, skipped, errors.New("不能删除当前登录用户")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return deleted, skipped, err
	}
	defer func() { _ = tx.Rollback() }()

	// 删除前先确认 actor 是管理员。actorID 来自会话，正常情况下必然已经是
	// 管理员；这里再查一次是为了挡住「直接调用 store 方法」绕过 handler 鉴权。
	var actorRole string
	if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", actorID).Scan(&actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deleted, skipped, errors.New("当前登录用户不存在")
		}
		return deleted, skipped, err
	}
	if actorRole != "admin" {
		return deleted, skipped, errors.New("需要管理员权限")
	}

	for _, id := range unique {
		var role string
		err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", id).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			// 不存在的用户不整体失败，记进 skipped 让前端能逐个提示。
			skipped = append(skipped, BatchDeleteSkip{ID: id, SkipReason: "用户不存在"})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=?", id); err != nil {
			return nil, nil, err
		}
		// 用户删掉后会话本应由外键 ON DELETE CASCADE 清理（SQLite 侧依赖 Open()
		// 设置的 PRAGMA foreign_keys=ON，MySQL 侧由建表语句保证）。这里显式再删一次，
		// 是为了兼容历史库上没建成外键的情况；删不到也不报错。
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=?", id); err != nil {
			return nil, nil, err
		}
		deleted = append(deleted, id)
		_ = role
	}

	// 事务内的删除后校验：管理员数量不能归零。
	// 放在 DELETE 之后而不是之前，是上面注释里说的 TOCTOU 收敛点。
	var admins int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&admins); err != nil {
		return nil, nil, err
	}
	if admins == 0 {
		return nil, nil, errors.New("至少需要保留 1 个管理员，本次删除已取消")
	}

	// actor 被并发删掉时，它的会话随即失效；这里显式回滚，避免出现
	// 「操作者已不存在，却成功删除了一批人」的审计空洞。
	var actorExists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id=?", actorID).Scan(&actorExists); err != nil {
		return nil, nil, err
	}
	if actorExists == 0 {
		return nil, nil, errors.New("当前登录用户已被删除")
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return deleted, skipped, nil
}

// ---- 邮箱注册支持（功能A：注册邮箱验证） ----
//
// 下面这些是为「注册填写邮箱 + 邮箱验证码」新增的能力。
// 为了不与同一仓库的其他改动冲突，全部集中追加在文件末尾，
// 已有的 RegisterUser / GetUser 等方法没有改签名。

// ErrEmailExists 表示该邮箱已被其他账号占用。
// 单列为一个哨兵错误，HTTP 层可以直接翻译成「该邮箱已被注册」。
var ErrEmailExists = errors.New("该邮箱已被注册")

// RegisterUserWithEmail 是 RegisterUser 的邮箱版本：多写一列 email。
//
// 邮箱的格式与验证码校验都在上层（httpapi）完成，这里只保证
// 「落库时大小写归一 + 唯一性冲突翻译成明确的中文错误」。
//
// 首账号提权逻辑与 RegisterUser 完全一致：角色判定和插入在同一条语句里，
// 并发请求不会同时看到一个空的 users 表。
func (s *Store) RegisterUserWithEmail(ctx context.Context, username, displayName, password, email string) (*User, error) {
	username = normalizeUsername(username)
	displayName = strings.TrimSpace(displayName)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if displayName == "" || len([]rune(displayName)) > 100 {
		return nil, errors.New("显示名长度应为 1-100 个字符")
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	email = normalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	// 先查一次：能给用户明确的「该邮箱已被注册」，而不是笼统的唯一约束错误。
	// 真正的并发兜底靠下面的唯一索引（冲突会落到 isEmailUniqueViolation）。
	if _, err := s.UserByEmail(ctx, email); err == nil {
		return nil, ErrEmailExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users
	        (username, display_name, password_hash, email, role, enabled, created_at, updated_at)
	        VALUES (?, ?, ?, ?, CASE WHEN (SELECT COUNT(*) FROM users)=0 THEN 'admin' ELSE 'user' END, TRUE, ?, ?)`,
		username, displayName, string(hash), email, now, now)
	if err != nil {
		if isEmailUniqueViolation(err) {
			return nil, ErrEmailExists
		}
		if isDuplicateError(err) {
			return nil, errors.New("用户名已存在")
		}
		return nil, err
	}
	id, _ := result.LastInsertId()
	return s.GetUser(ctx, id)
}

// SetUserEmail 补写/更新某个用户的邮箱，供管理员代建账号或补齐历史数据使用。
func (s *Store) SetUserEmail(ctx context.Context, id int64, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return errors.New("请填写邮箱")
	}
	if err := ValidateEmail(email); err != nil {
		return err
	}
	if owner, err := s.UserByEmail(ctx, email); err == nil && owner.ID != id {
		return ErrEmailExists
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE users SET email=?, updated_at=? WHERE id=?", email, time.Now().UTC(), id)
	if err != nil && isEmailUniqueViolation(err) {
		return ErrEmailExists
	}
	return err
}

// UserByEmail 按邮箱查用户；找不到时返回 sql.ErrNoRows，与 GetUser 保持一致。
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, sql.ErrNoRows
	}
	return s.scanUser(ctx, userSelectColumns+" WHERE LOWER(email)=?", email)
}

// GetUserWithEmail 按 id 查用户，并附带邮箱。
func (s *Store) GetUserWithEmail(ctx context.Context, id int64) (*User, string, error) {
	var u User
	var email sql.NullString
	err := s.db.QueryRowContext(ctx, userSelectColumns+", email FROM users WHERE id=?", id).Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &email)
	if err != nil {
		return nil, "", err
	}
	return &u, email.String, nil
}

// ListUsersWithEmail 返回用户列表及各自的邮箱（未设置时为空串）。
func (s *Store) ListUsersWithEmail(ctx context.Context) ([]User, map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, userSelectColumns+", email FROM users ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	users := []User{}
	emails := map[int64]string{}
	for rows.Next() {
		var u User
		var email sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &email); err != nil {
			return nil, nil, err
		}
		users = append(users, u)
		emails[u.ID] = email.String
	}
	return users, emails, rows.Err()
}

// userSelectColumns 是 users 表的公共列清单，保证各处扫描顺序一致。
const userSelectColumns = `SELECT id, username, display_name, role, enabled, last_login_at, created_at, updated_at FROM users`

// scanUser 执行一条返回单行用户数据的查询并扫描。
func (s *Store) scanUser(ctx context.Context, query string, args ...any) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// isEmailUniqueViolation 判断错误是否来自 email 列的唯一约束。
//
// MySQL 报 "Duplicate entry 'x@y' for key 'users.email'"，SQLite 报
// "UNIQUE constraint failed: users.email"，两者都带 email 字样；
// 先按列名辨认，认不出来再退回通用重复判断，保证不会把邮箱冲突报成用户名冲突。
func isEmailUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "duplicate") || strings.Contains(message, "unique constraint") {
		if strings.Contains(message, "email") {
			return true
		}
		// 两个唯一列都可能是罪魁：先看用户名是否已被占用。
		return false
	}
	return false
}

// normalizeEmail 统一小写去空白，与 httpapi 侧保持一致，避免同名函数风格分叉。
func normalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// ValidateEmail 与 httpapi.ValidateEmail 规则一致（长度上限 254、必须带顶级域）。
// 在 auth 包内再实现一份是为了让 store 层不依赖 httpapi；两处都有测试钉住。
func ValidateEmail(value string) error {
	email := strings.TrimSpace(value)
	if email == "" {
		return errors.New("请填写邮箱")
	}
	if len(email) > 254 {
		return errors.New("邮箱长度不能超过 254 个字符")
	}
	for i := 0; i < len(email); i++ {
		if email[i] <= 0x20 || email[i] >= 0x7f {
			return errors.New("邮箱格式不正确")
		}
	}
	at := strings.Index(email, "@")
	if at <= 0 || at != strings.LastIndex(email, "@") || at == len(email)-1 {
		return errors.New("邮箱格式不正确")
	}
	local, domain := email[:at], email[at+1:]
	if len(local) > 64 {
		return errors.New("邮箱格式不正确")
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return errors.New("邮箱格式不正确")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 || strings.Contains(domain, "..") {
		return errors.New("邮箱格式不正确")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return errors.New("邮箱格式不正确")
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
				continue
			}
			return errors.New("邮箱格式不正确")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("邮箱格式不正确")
		}
	}
	return nil
}
